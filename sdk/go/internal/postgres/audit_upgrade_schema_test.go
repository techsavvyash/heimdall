package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type upgradeSchemaFixture struct {
	SchemaSQLChecksum string
	SignatureChecksum string
	Cases             []struct{ Name, SQL string }
}

func schemaFixture(t *testing.T) upgradeSchemaFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-upgrade-schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var f upgradeSchemaFixture
	if json.Unmarshal(raw, &f) != nil || len(f.Cases) < 20 {
		t.Fatal("schema fixture")
	}
	for _, pair := range [][2]string{{auditUpgradeSchemaSQL, f.SchemaSQLChecksum}, {auditUpgradeSchemaJSON, f.SignatureChecksum}} {
		digest := sha256.Sum256([]byte(pair[0]))
		if hex.EncodeToString(digest[:]) != pair[1] {
			t.Fatal("schema profile checksum")
		}
	}
	return f
}
func TestAuditUpgradeSchemaFixture(t *testing.T) { schemaFixture(t) }
func TestAuditUpgradeSchema(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_UPGRADE_SCHEMA_URL")
	if dsn == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	f := upgradeFiles(t)
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); e != nil {
		t.Fatal("pristine preparation", e)
	}
	// Two connections with different search paths must recognize the same schema.
	for _, path := range []string{"public", "heimdall,public"} {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec("SET LOCAL search_path=" + path); e != nil {
			t.Fatal(e)
		}
		e = checkAuditUpgradeSchema(ctx, tx)
		tx.Rollback()
		if e != nil {
			t.Fatal(path, e)
		}
	}
	for _, c := range schemaFixture(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			tx, e := db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			if _, e = tx.ExecContext(ctx, c.SQL); e != nil {
				t.Fatal("mutation", e)
			}
			if e = checkAuditUpgradeSchema(ctx, tx); !errors.Is(e, contract.ErrAuditUnavailable) {
				t.Fatal("drift accepted or wrong error", e)
			}
		})
	}
	// A table lock, not only the cooperative advisory lock, prevents a concurrent
	// table rewrite while a successful schema check's transaction stays open.
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = checkAuditUpgradeSchema(ctx, tx); e != nil {
		t.Fatal(e)
	}
	other, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = db.ExecContext(other, "ALTER TABLE heimdall.audit_decisions ADD COLUMN unexpected text")
	deadline := other.Err()
	cancel()
	tx.Rollback()
	if e == nil || !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatal("DDL was not blocked until its deadline", e, deadline)
	}
	// Cancellation while waiting for a physical table lock preserves the error
	// distinction and releases earlier locks/connections on rollback.
	lock, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec("LOCK TABLE heimdall.audit_decisions IN ACCESS EXCLUSIVE MODE"); e != nil {
		t.Fatal(e)
	}
	waiting, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	e = PrepareAuditUpgrade(waiting, db, f[0], f[1], f[2], true)
	stop()
	lock.Rollback()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	// Persisted drift must gate both prepared retries and coordinator mutations.
	if _, e = db.Exec("ALTER TABLE heimdall.audit_decisions DISABLE TRIGGER decision_resolution"); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	if _, e = AdvanceAuditUpgrade(ctx, db, 10, true, func(map[string]any) error { return nil }); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	var count int
	if e = db.QueryRow("SELECT count(*) FROM heimdall.audit_decision_backfill").Scan(&count); e != nil || count != 0 {
		t.Fatal("partial checkpoint", count, e)
	}
	if _, e = db.Exec("ALTER TABLE heimdall.audit_decisions ENABLE TRIGGER decision_resolution"); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); e != nil {
		t.Fatal("pool reuse", e)
	}
}

func TestAuditUpgradeSchemaRollback(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_UPGRADE_SCHEMA_BAD_BASE_URL")
	if dsn == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	f := upgradeFiles(t)
	if _, e = db.Exec("ALTER TABLE heimdall.records DISABLE TRIGGER record_revision"); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	var untouched bool
	if e = db.QueryRow("SELECT (SELECT state='ready' AND database_version=1 FROM heimdall.schema_metadata) AND to_regclass('heimdall.audit_decisions') IS NULL AND to_regclass('heimdall.audit_decision_backfill') IS NULL AND to_regclass('heimdall.audit_upgrade') IS NULL").Scan(&untouched); e != nil || !untouched {
		t.Fatal("partial preparation", e)
	}
	if _, e = db.Exec("ALTER TABLE heimdall.records ENABLE TRIGGER record_revision"); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); e != nil {
		t.Fatal(e)
	}
}
