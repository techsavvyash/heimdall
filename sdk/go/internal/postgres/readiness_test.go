package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestReadinessCancelled(t *testing.T) {
	db, err := sql.Open("postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = CheckReadiness(ctx, db); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}
}
func TestReadiness(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live readiness matrix")
	}
	read := func(path string, target any) {
		t.Helper()
		raw, err := os.ReadFile("../../../../contracts/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	var catalog struct{ Session []struct{ Checksum string } }
	read("persistence/catalog.json", &catalog)
	var cases []struct {
		Name  string
		SQL   []string
		Ready bool
	}
	read("fixtures/postgres-readiness.json", &cases)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range cases {
		exec("DELETE FROM heimdall.schema_metadata; DELETE FROM heimdall.migrations")
		exec("INSERT INTO heimdall.schema_metadata(database_version,state,reader_min,reader_max,writer_min,writer_max) VALUES(1,'ready',1,1,1,1)")
		exec("INSERT INTO heimdall.migrations(version,checksum) VALUES(1,$1)", catalog.Session[0].Checksum)
		for _, query := range item.SQL {
			exec(query)
		}
		err = CheckReadiness(ctx, db)
		if item.Ready && err != nil || !item.Ready && !errors.Is(err, contract.ErrUnavailable) {
			t.Fatalf("%s: %v", item.Name, err)
		}
	}
	exec("DELETE FROM heimdall.migrations")
	exec("INSERT INTO heimdall.migrations(version,checksum) VALUES(1,$1)", catalog.Session[0].Checksum)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE heimdall.schema_metadata SET state='migrating'; UPDATE heimdall.migrations SET checksum=repeat('0',64)"); err != nil {
		t.Fatal(err)
	}
	if err = CheckReadiness(ctx, db); err != nil {
		t.Fatal("uncommitted migration", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = CheckReadiness(ctx, db); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("committed incompatible migration", err)
	}
	exec("UPDATE heimdall.schema_metadata SET state='ready'")
	exec("UPDATE heimdall.migrations SET checksum=$1", catalog.Session[0].Checksum)
	readOnly, err := sql.Open("postgres", url+"&options=-c%20default_transaction_read_only%3Don")
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	var readOnlySetting string
	if err = readOnly.QueryRow("SHOW transaction_read_only").Scan(&readOnlySetting); err != nil || readOnlySetting != "on" {
		t.Fatal("read-only connection setup", err)
	}
	if err = CheckReadiness(ctx, readOnly); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("read-only", err)
	}
	exec("ALTER TABLE heimdall.schema_metadata RENAME TO hidden_metadata")
	defer exec("ALTER TABLE heimdall.hidden_metadata RENAME TO schema_metadata")
	if err = CheckReadiness(ctx, db); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("missing table", err)
	}
}
