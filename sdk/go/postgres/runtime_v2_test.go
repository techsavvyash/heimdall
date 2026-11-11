package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"os"
	"testing"
	"time"
)

func v2Events(t *testing.T, environment string) []map[string]any {
	t.Helper()
	raw, e := os.ReadFile("../../../contracts/fixtures/audit-backfill.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ Events []map[string]any }
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	for _, event := range f.Events {
		event["environment"] = environment
	}
	return f.Events
}
func v2Storage(t *testing.T, dsn, environment string) (*sql.DB, heimdall.RuntimeStorage) {
	t.Helper()
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	storage, e := NewStorage(db, StorageOptions{ProductID: "ledger", Environment: environment, AuditSinks: []string{"siem"}})
	if e != nil {
		t.Fatal(e)
	}
	return db, storage
}
func TestFactoryV2(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_FACTORY_URL")
	if dsn == "" {
		t.Skip("live PostgreSQL required")
	}
	db, storage := v2Storage(t, dsn, "factory-go")
	defer db.Close()
	ctx := context.Background()
	if e := storage.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	events := v2Events(t, "factory-go")
	for _, i := range []int{0, 1, 2, 3, 0, 1} {
		if _, e := storage.AppendAudit(ctx, events[i]); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := db.QueryRow("SELECT count(*) FROM heimdall.audit_decisions WHERE environment='factory-go'").Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	terminal := v2Events(t, "factory-go")[1]
	terminal["eventId"] = "loser"
	if _, e := storage.AppendAudit(ctx, terminal); !errors.Is(e, heimdall.ErrConflict) {
		t.Fatal(e)
	}
	terminal["outcome"] = "outcome_unknown"
	if _, e := storage.AppendAudit(ctx, terminal); !errors.Is(e, heimdall.ErrInvalidArgument) {
		t.Fatal(e)
	}
	terminal = v2Events(t, "factory-go")[1]
	terminal["eventId"] = "orphan"
	terminal["decisionId"] = "missing"
	if _, e := storage.AppendAudit(ctx, terminal); !errors.Is(e, heimdall.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	broken := v2Events(t, "factory-go")[0]
	broken["eventId"] = "broken-request"
	broken["decisionId"] = "broken"
	if _, e := storage.AppendAudit(ctx, broken); !errors.Is(e, heimdall.ErrUnavailable) {
		t.Fatal(e)
	}
	if e := db.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE environment='factory-go'").Scan(&count); e != nil || count != 4 {
		t.Fatal("failed append leaked", count, e)
	}
	for _, statement := range []string{"UPDATE heimdall.schema_metadata SET state='migrating'", "DELETE FROM heimdall.audit_decisions", "UPDATE heimdall.audit_events SET recorded_at=recorded_at", "CREATE TABLE heimdall.forbidden(id int)"} {
		if _, e := db.Exec(statement); e == nil {
			t.Fatal("runtime privilege", statement)
		}
	}
	for _, key := range []string{"HEIMDALL_TEST_FACTORY_V1", "HEIMDALL_TEST_FACTORY_MIGRATING"} {
		other, port := v2Storage(t, os.Getenv(key), "factory-go")
		if e := port.Ready(ctx); !errors.Is(e, heimdall.ErrUnavailable) {
			t.Fatal(e)
		}
		if _, e := port.AppendAudit(ctx, events[3]); !errors.Is(e, heimdall.ErrUnavailable) {
			t.Fatal(e)
		}
		other.Close()
	}
	admin, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_FACTORY_ADMIN"))
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	lock, e := admin.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = storage.AppendAudit(waiting, events[3])
	cancel()
	lock.Rollback()
	if !errors.Is(e, heimdall.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if _, e = admin.Exec("BEGIN; SELECT pg_advisory_xact_lock(74162401); UPDATE heimdall.schema_metadata SET state='migrating'; COMMIT"); e != nil {
		t.Fatal(e)
	}
	if _, e = storage.AppendAudit(ctx, events[3]); !errors.Is(e, heimdall.ErrUnavailable) {
		t.Fatal("cached admission", e)
	}
	if _, e = admin.Exec("BEGIN; SELECT pg_advisory_xact_lock(74162401); UPDATE heimdall.schema_metadata SET state='ready'; COMMIT"); e != nil {
		t.Fatal(e)
	}
	if _, e = storage.AppendAudit(ctx, events[3]); e != nil {
		t.Fatal("pool reuse", e)
	}
}
func TestFactoryV2Peer(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_FACTORY_URL")
	if dsn == "" {
		t.Skip("live PostgreSQL required")
	}
	db, storage := v2Storage(t, dsn, "factory-ts")
	defer db.Close()
	for _, event := range v2Events(t, "factory-ts") {
		if _, e := storage.AppendAudit(context.Background(), event); e != nil {
			t.Fatal(e)
		}
	}
}
func TestFactoryV2Loss(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_FACTORY_LOSS")
	if dsn == "" {
		t.Skip("commit proxy required")
	}
	db, storage := v2Storage(t, dsn, "factory-loss-go")
	defer db.Close()
	ctx := context.Background()
	event := v2Events(t, "factory-loss-go")[0]
	if _, e := storage.AppendAudit(ctx, event); !errors.Is(e, heimdall.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	direct, port := v2Storage(t, os.Getenv("HEIMDALL_TEST_FACTORY_URL"), "factory-loss-go")
	defer direct.Close()
	var n int
	if e := direct.QueryRow("SELECT count(*) FROM heimdall.audit_decisions WHERE environment='factory-loss-go'").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, e := port.AppendAudit(ctx, event); e != nil {
		t.Fatal(e)
	}
}
