package postgres

import (
	"database/sql"
	"encoding/json"
	"github.com/lib/pq"
	"os"
	"strings"
	"testing"
)

func TestManagedSchema(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" {
		t.Skip("managed schema proof")
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/managed-schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Setup, Rollback                                []string
		Cases                                          []struct{ Name, SQL, SQLState string }
		Payload, Terminal, TerminalConflict, Successor string
	}
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_managed_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	query := func(s string) string { return strings.ReplaceAll(s, "$scope", "managed-go") }
	exec := func(s string) {
		t.Helper()
		if _, e := db.Exec(query(s)); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range f.Setup {
		exec(s)
	}
	for _, c := range f.Cases {
		_, err := db.Exec(query(c.SQL))
		pg, ok := err.(*pq.Error)
		if !ok || string(pg.Code) != c.SQLState {
			t.Fatal(c.Name, err)
		}
	}
	var payload []byte
	if e = db.QueryRow("SELECT intent_payload FROM heimdall_managed.operations WHERE product_id='managed-go' AND operation_id='original'").Scan(&payload); e != nil || string(payload) != f.Payload {
		t.Fatal("intent roundtrip", e)
	}
	var position, revision, nextPosition, nextRevision int64
	if e = db.QueryRow("SELECT position,revision FROM heimdall_managed.membership_candidates WHERE product_id='managed-go'").Scan(&position, &revision); e != nil {
		t.Fatal(e)
	}
	exec("UPDATE heimdall_managed.membership_candidates SET observed_at=101 WHERE product_id='$scope'")
	if e = db.QueryRow("SELECT position,revision FROM heimdall_managed.membership_candidates WHERE product_id='managed-go'").Scan(&nextPosition, &nextRevision); e != nil || position != nextPosition || nextRevision <= revision {
		t.Fatal("stable position and new revision", e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	for _, s := range f.Rollback {
		if _, e = tx.Exec(query(s)); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"workspaces", "operations", "membership_candidates"} {
		var count int
		if e = db.QueryRow("SELECT count(*) FROM heimdall_managed." + table + " WHERE product_id='managed-go' AND workspace_id='rollback'").Scan(&count); e != nil || count != 0 {
			t.Fatal(table, e)
		}
	}
	for _, table := range []string{"audit_events", "audit_deliveries"} {
		var count int
		if e = db.QueryRow("SELECT count(*) FROM heimdall." + table + " WHERE product_id='managed-go' AND event_id='managed-rollback'").Scan(&count); e != nil || count != 0 {
			t.Fatal(table, e)
		}
	}
	exec(f.Terminal)
	if _, e = db.Exec(query(f.TerminalConflict)); e == nil {
		t.Fatal("terminal key reused")
	} else if pg, ok := e.(*pq.Error); !ok || pg.Code != "23505" {
		t.Fatal(e)
	}
	exec(f.Successor)
	if os.Getenv("HEIMDALL_TEST_MANAGED_READ_PEER") != "" {
		if e = db.QueryRow("SELECT intent_payload FROM heimdall_managed.operations WHERE product_id='managed-ts' AND operation_id='original'").Scan(&payload); e != nil || string(payload) != f.Payload {
			t.Fatal("TS intent bytes", e)
		}
	}
}
