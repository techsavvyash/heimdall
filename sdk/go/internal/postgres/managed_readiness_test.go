package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
	"time"
)

func TestManagedReadinessCancelled(t *testing.T) {
	db, e := sql.Open("postgres", "")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = CheckManagedReadiness(ctx, db); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if e = MigrateManaged(ctx, db, []byte("SELECT 1")); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestManagedReadiness(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" {
		t.Skip("live managed readiness")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	raw, e := os.ReadFile("../../../../contracts/fixtures/managed-readiness.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name  string
		SQL   []string
		Ready bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixture")
	}
	reset := func() {
		t.Helper()
		for schema, checksum := range map[string]string{"heimdall": sessionChecksum, "heimdall_managed": managedChecksum} {
			if _, e = db.Exec("DELETE FROM " + schema + ".schema_metadata; DELETE FROM " + schema + ".migrations; INSERT INTO " + schema + ".schema_metadata VALUES(true,1,'ready',1,1,1,1)"); e != nil {
				t.Fatal(e)
			}
			if _, e = db.Exec("INSERT INTO "+schema+".migrations(version,checksum) VALUES(1,$1)", checksum); e != nil {
				t.Fatal(e)
			}
		}
	}
	defer reset()
	for _, c := range cases {
		reset()
		for _, s := range c.SQL {
			if _, e = db.Exec(s); e != nil {
				t.Fatal(e)
			}
		}
		e = CheckManagedReadiness(context.Background(), db)
		if (e == nil) != c.Ready || e != nil && !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(c.Name, e)
		}
	}
	reset()
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE heimdall_managed.schema_metadata SET state='migrating'"); e != nil {
		t.Fatal(e)
	}
	if e = CheckManagedReadiness(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = CheckManagedReadiness(context.Background(), db); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if e = CheckReadiness(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	reset()
	for _, profile := range []struct {
		Options string
		Valid   bool
	}{{"role%3Dheimdall_managed_status", true}, {"default_transaction_read_only%3Don", false}} {
		other, e := sql.Open("postgres", url+"&options=-c%20"+profile.Options)
		if e != nil {
			t.Fatal(e)
		}
		e = CheckManagedReadiness(context.Background(), other)
		other.Close()
		if (e == nil) != profile.Valid {
			t.Fatal("connection profile", e)
		}
	}
}
func TestManagedMigration(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" {
		t.Skip("live managed migration")
	}
	raw, e := os.ReadFile("../../../../contracts/persistence/managed/0001.sql")
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if e = MigrateManaged(ctx, db, raw); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall_managed.schema_metadata SET state='migrating'"); e != nil {
		t.Fatal(e)
	}
	if e = MigrateManaged(ctx, db, raw); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	var state string
	if e = db.QueryRow("SELECT state FROM heimdall_managed.schema_metadata").Scan(&state); e != nil || state != "migrating" {
		t.Fatal("repair", e)
	}
	if _, e = db.Exec("UPDATE heimdall_managed.schema_metadata SET state='ready'"); e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if e = MigrateManaged(deadline, db, raw); !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	tx.Rollback()
	if e = MigrateManaged(ctx, db, raw); e != nil {
		t.Fatal(e)
	}
}
