package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestKeyUsageLive(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_KEYS_ADMIN")
	if url == "" {
		t.Skip("live key usage")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_KEYS_RUNTIME"))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx := context.Background()
	migration, err := os.ReadFile("../../../../contracts/persistence/keys/0001.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = MigrateKeyUsage(ctx, db, migration); err != nil {
			t.Fatal(err)
		}
	}
	if err = ReserveRecordKeyUsage(ctx, runtime, strings.Repeat("6", 64)); err != nil {
		t.Fatal(err)
	}
	readonly, err := sql.Open("postgres", url+"&options=-c%20default_transaction_read_only%3Don")
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if err = ReserveRecordKeyUsage(ctx, readonly, strings.Repeat("6", 64)); err != contract.ErrUnavailable {
		t.Fatal(err)
	}
	f := strings.Repeat("4", 64)
	if err = RegisterRecordKeyUsage(ctx, db, f, 7, false); err != contract.ErrInvalidArgument {
		t.Fatal(err)
	}
	if err = RegisterRecordKeyUsage(ctx, db, f, 7, true); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ReserveRecordKeyUsage(ctx, runtime, f)
			if err == nil {
				successes.Add(1)
			} else if err != contract.ErrUnavailable {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 7 {
		t.Fatal(successes.Load())
	}
	if err = RegisterRecordKeyUsage(ctx, db, f, 7, true); err != nil {
		t.Fatal(err)
	}
	if err = RegisterRecordKeyUsage(ctx, db, f, 8, true); err != contract.ErrConflict {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = RetireRecordKeyUsage(ctx, db, f); err != nil {
			t.Fatal(err)
		}
	}
	if err = RegisterRecordKeyUsage(ctx, db, f, 7, true); err != contract.ErrConflict {
		t.Fatal(err)
	}
	if err = ReserveRecordKeyUsage(ctx, runtime, f); err != contract.ErrUnavailable {
		t.Fatal(err)
	}
	if err = RetireRecordKeyUsage(ctx, runtime, f); err != contract.ErrUnavailable {
		t.Fatal(err)
	}
	if err = RegisterRecordKeyUsage(ctx, runtime, strings.Repeat("5", 64), 1, true); err != contract.ErrUnavailable {
		t.Fatal(err)
	}
}
func TestKeyUsageLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_KEYS_LOSS")
	if url == "" {
		t.Skip("live commit loss")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = ReserveRecordKeyUsage(context.Background(), db, strings.Repeat("3", 64)); err != contract.ErrOutcomeUnknown {
		t.Fatal(err)
	}
}

func TestKeyUsageInitialize(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_KEYS_ADMIN")
	if url == "" {
		t.Skip("live key initialization")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration, err := os.ReadFile("../../../../contracts/persistence/keys/0001.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = MigrateKeyUsage(context.Background(), db, migration); err != nil {
		t.Fatal(err)
	}
	if err = CheckKeyUsageReadiness(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
