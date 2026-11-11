package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"sync"
	"testing"
	"time"
)

func TestInvalidScope(t *testing.T) {
	if _, err := NewRecords(nil, nil, "bad", "test", nil); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
}
func TestRecords(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL integration")
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/postgres-records.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		ProductID, Environment, KeyID, Value, Kind string
		KeyByte                                    byte
		ExpiresAt                                  int64
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := contract.NewRecordCipher(f.KeyID, map[string][]byte{f.KeyID: bytes.Repeat([]byte{f.KeyByte}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRecords(db, cipher, f.ProductID, f.Environment, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	value := []byte(f.Value)
	if os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		record, err := store.Get(ctx, "session", "from-ts")
		if err != nil || record == nil || !bytes.Equal(record.Value, value) {
			t.Fatalf("peer read: %v", err)
		}
		return
	}
	if _, err = store.Create(ctx, "session", "from-go", value, f.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(ctx, "session", "go-race", value, f.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(ctx, "session", "go-race", value, f.ExpiresAt); !errors.Is(err, contract.ErrConflict) {
		t.Fatal(err)
	}
	results := make(chan *Record, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := store.CompareAndSwap(ctx, "session", "go-race", first.Revision, value, f.ExpiresAt)
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for r := range results {
		if r != nil {
			winners++
		}
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if winners != 1 {
		t.Fatal(winners)
	}
	other, _ := NewRecords(db, cipher, "support", "test", nil)
	if r, e := other.Get(ctx, "session", "go-race"); r != nil || e != nil {
		t.Fatal("scope", e)
	}
	expired, _ := NewRecords(db, cipher, "ledger", "test", func() int64 { return f.ExpiresAt })
	if r, e := expired.Get(ctx, "session", "go-race"); r != nil || e != nil {
		t.Fatal("expiry", e)
	}
	if err = store.Delete(ctx, "session", "go-race"); err != nil {
		t.Fatal(err)
	}
	recreated, err := store.Create(ctx, "session", "go-race", value, f.ExpiresAt)
	if err != nil || recreated.Revision == first.Revision {
		t.Fatal("revision", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.Get(cancelled, "session", "go-race"); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE heimdall.records SET expires_at=expires_at+1 WHERE key_hash=sha256('go-race'::bytea)"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, "session", "go-race"); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("LOCK TABLE heimdall.records IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	defer stop()
	err = store.Delete(deadline, "session", "go-race")
	if !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if r, e := store.Get(ctx, "session", "from-go"); e != nil || r == nil || !bytes.Equal(r.Value, value) {
		t.Fatal("pool recovery", e)
	}
}
