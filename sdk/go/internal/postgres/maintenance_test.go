package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func cleanupSeed(t *testing.T, db *sql.DB, product, key string, expiry int64, environment, kind string, identity bool) {
	t.Helper()
	_, err := db.Exec("INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,$3,sha256($4::bytea),'retired','unreadable',$5,CASE WHEN $6 THEN sha256('identity'::bytea) ELSE NULL END)", product, environment, kind, key, expiry, identity)
	if err != nil {
		t.Fatal(err)
	}
}
func TestRecordMaintenance(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/record-cleanup.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cutoff        int64
		InvalidLimits []int
		Records       []struct {
			Key, Kind, Environment string
			ExpiresAt              int64
			Identity               bool
		}
		Remaining []string
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := NewRecordMaintenance(db, "cleanup-go", "test", func() int64 { return f.Cutoff })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRecordMaintenance(db, "../bad", "test", nil); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, limit := range f.InvalidLimits {
		if _, err = m.CleanupExpired(ctx, limit); !errors.Is(err, contract.ErrInvalidArgument) {
			t.Fatal(err)
		}
	}
	for _, now := range []int64{-1, maxSafe + 1} {
		bad, _ := NewRecordMaintenance(db, "cleanup-go", "test", func() int64 { return now })
		if _, err = bad.CleanupExpired(ctx, 1); !errors.Is(err, contract.ErrInvalidArgument) {
			t.Fatal(err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = m.CleanupExpired(cancelled, 1); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}
	if os.Getenv("HEIMDALL_TEST_DATABASE_URL") == "" {
		t.Skip("live PostgreSQL integration")
	}
	if os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		peer, _ := NewRecordMaintenance(db, "cleanup-peer-ts", "test", func() int64 { return f.Cutoff })
		n, e := peer.CleanupExpired(ctx, 100)
		if e != nil || n != 1 {
			t.Fatalf("peer: %d %v", n, e)
		}
		return
	}
	for _, r := range f.Records {
		cleanupSeed(t, db, "cleanup-go", r.Key, r.ExpiresAt, r.Environment, r.Kind, r.Identity)
	}
	cleanupSeed(t, db, "cleanup-foreign-go", "foreign", 0, "test", "session", false)
	if _, err = db.Exec("INSERT INTO heimdall.identity_denials VALUES('cleanup-go','test',sha256('identity'::bytea),0,'deleted')"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ Limit, Want int }{{1, 1}, {100, 1}, {100, 0}} {
		n, e := m.CleanupExpired(ctx, c.Limit)
		if e != nil || n != c.Want {
			t.Fatalf("cleanup %d %v", n, e)
		}
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM heimdall.records WHERE product_id='cleanup-go'").Scan(&n); err != nil || n != len(f.Remaining) {
		t.Fatalf("remaining %d %v", n, err)
	}
	for _, key := range f.Remaining {
		if err = db.QueryRow("SELECT count(*) FROM heimdall.records WHERE product_id='cleanup-go' AND key_hash=sha256($1::bytea)", key).Scan(&n); err != nil || n != 1 {
			t.Fatal("missing survivor", err)
		}
	}
	if err = db.QueryRow("SELECT count(*) FROM heimdall.identity_denials WHERE product_id='cleanup-go'").Scan(&n); err != nil || n != 1 {
		t.Fatal("denial changed", err)
	}
	if err = db.QueryRow("SELECT count(*) FROM heimdall.records WHERE product_id='cleanup-foreign-go'").Scan(&n); err != nil || n != 1 {
		t.Fatal("foreign changed", err)
	}
	cleanupSeed(t, db, "cleanup-go", "locked", 999, "test", "session", true)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE heimdall.records SET expires_at=2000 WHERE product_id='cleanup-go' AND key_hash=sha256('locked'::bytea)"); err != nil {
		t.Fatal(err)
	}
	if n, err = m.CleanupExpired(ctx, 100); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n, err = m.CleanupExpired(ctx, 100); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	for i := range 101 {
		cleanupSeed(t, db, "cleanup-go", string(rune(65+i)), 0, "test", "session", false)
	}
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count, e := m.CleanupExpired(ctx, 20)
			if e != nil || count > 20 {
				t.Error(count, e)
			}
			results <- count
		}()
	}
	wg.Wait()
	close(results)
	total := 0
	for count := range results {
		total += count
	}
	if total != 101 {
		t.Fatal(total)
	}
	cleanupSeed(t, db, "cleanup-go", "timeout", 0, "test", "session", false)
	lock, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err = lock.Exec("LOCK TABLE heimdall.records IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = m.CleanupExpired(deadline, 1)
	stop()
	if !errors.Is(err, contract.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	if err = lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n, err = m.CleanupExpired(ctx, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for _, option := range []string{"default_transaction_read_only=on", "role=heimdall_identity_runtime"} {
		u, e := url.Parse(os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
		if e != nil {
			t.Fatal(e)
		}
		q := u.Query()
		q.Set("options", "-c "+option)
		u.RawQuery = q.Encode()
		restricted, e := sql.Open("postgres", u.String())
		if e != nil {
			t.Fatal(e)
		}
		port, _ := NewRecordMaintenance(restricted, "cleanup-go", "test", func() int64 { return f.Cutoff })
		if option == "default_transaction_read_only=on" {
			_, e = port.CleanupExpired(ctx, 1)
			if !errors.Is(e, contract.ErrUnavailable) {
				t.Fatal(e)
			}
		} else {
			cleanupSeed(t, db, "cleanup-go", "restricted", 0, "test", "session", false)
			count, e := port.CleanupExpired(ctx, 1)
			if e != nil || count != 1 {
				t.Fatal(count, e)
			}
		}
		restricted.Close()
	}
	cleanupSeed(t, db, "cleanup-peer-go", "from-go", 0, "test", "session", false)
}
func TestCleanupCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CLEANUP_LOSS")
	if url == "" {
		t.Skip("commit fault proxy")
	}
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fault, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	cleanupSeed(t, db, "cleanup-loss-go", "lost", 0, "test", "session", false)
	m, _ := NewRecordMaintenance(fault, "cleanup-loss-go", "test", func() int64 { return 1000 })
	if _, err = m.CleanupExpired(context.Background(), 1); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM heimdall.records WHERE product_id='cleanup-loss-go'").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	m, _ = NewRecordMaintenance(db, "cleanup-loss-go", "test", func() int64 { return 1000 })
	if n, err = m.CleanupExpired(context.Background(), 1); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
