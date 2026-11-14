package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
	"time"
)

func TestMigrationRejectsUntrustedSQL(t *testing.T) {
	db, err := sql.Open("postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = MigrateSession(context.Background(), db, []byte("SELECT 1")); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
}
func TestMigration(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live migration")
	}
	migration, err := os.ReadFile("../../../../contracts/persistence/session/0001.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = MigrateSession(ctx, db, migration); err != nil {
		t.Fatal("idempotent existing installation", err)
	}
	if _, err = db.Exec("UPDATE heimdall.schema_metadata SET state='migrating'"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("UPDATE heimdall.schema_metadata SET state='ready'")
	if err = MigrateSession(ctx, db, migration); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("must not repair", err)
	}
	var state string
	if err = db.QueryRow("SELECT state FROM heimdall.schema_metadata").Scan(&state); err != nil || state != "migrating" {
		t.Fatal("modified incompatible metadata", err)
	}
}

func TestMigrationLock(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live migration lock")
	}
	migration, err := os.ReadFile("../../../../contracts/persistence/session/0001.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("SELECT pg_advisory_xact_lock(74162401)"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err = MigrateSession(ctx, db, migration); !errors.Is(err, contract.ErrDeadlineExceeded) {
		t.Fatal("runner did not wait for exclusive lock", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = MigrateSession(context.Background(), db, migration); err != nil {
		t.Fatal("retry after lock released", err)
	}
}
