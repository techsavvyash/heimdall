package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type backfillFixture struct {
	Events []map[string]any
	Steps  []AuditBackfillProgress
}

func backfillData(t *testing.T) backfillFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-backfill.json")
	if e != nil {
		t.Fatal(e)
	}
	var f backfillFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func backfillPort(t *testing.T, db *sql.DB, scope string) *Audit {
	t.Helper()
	a, e := NewAudit(db, scope, "production", []string{"siem"}, func(event map[string]any) error {
		if e := contract.ValidateAuditShape(event); e != nil {
			return e
		}
		raw, _ := json.Marshal(event)
		var typed contract.AuditEvent
		if json.Unmarshal(raw, &typed) != nil {
			return contract.ErrInvalidArgument
		}
		return contract.ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false })
	})
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func backfillSeed(t *testing.T, db *sql.DB, scope string, events []map[string]any) {
	t.Helper()
	a := backfillPort(t, db, scope)
	for _, original := range events {
		event := map[string]any{}
		for k, v := range original {
			event[k] = v
		}
		event["productId"] = scope
		if _, e := a.Append(context.Background(), event); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := db.Exec("INSERT INTO heimdall.audit_decision_backfill(product_id,environment) VALUES($1,'production')", scope); e != nil {
		t.Fatal(e)
	}
}
func backfillFinish(t *testing.T, db *sql.DB, scope string, steps []AuditBackfillProgress) {
	t.Helper()
	for _, expected := range steps {
		actual, e := backfillPort(t, db, scope).BackfillDecisionPage(context.Background(), 1)
		if e != nil || !reflect.DeepEqual(actual, &expected) {
			t.Fatalf("progress %s: %+v %+v %v", scope, actual, expected, e)
		}
	}
}
func TestAuditBackfillInput(t *testing.T) {
	db, e := sql.Open("postgres", "")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	a := backfillPort(t, db, "backfill")
	for _, limit := range []int{0, 101} {
		if _, e = a.BackfillDecisionPage(context.Background(), limit); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.BackfillDecisionPage(ctx, 1); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditBackfill(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_BACKFILL_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	f := backfillData(t)
	ctx := context.Background()
	backfillSeed(t, db, "backfill-go", f.Events)
	backfillFinish(t, db, "backfill-go", f.Steps)
	last, e := backfillPort(t, db, "backfill-go").BackfillDecisionPage(ctx, 100)
	if e != nil || !reflect.DeepEqual(last, &f.Steps[len(f.Steps)-1]) {
		t.Fatal(last, e)
	}
	var completed, pending int
	if e = db.QueryRow("SELECT count(*) FILTER(WHERE resolution='completed'),count(*) FILTER(WHERE resolution='pending') FROM heimdall.audit_decisions WHERE product_id='backfill-go'").Scan(&completed, &pending); e != nil || completed != 1 || pending != 1 {
		t.Fatal(completed, pending, e)
	}
	var events, deliveries int
	if e = db.QueryRow("SELECT (SELECT count(*) FROM heimdall.audit_events WHERE product_id='backfill-go'),(SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id='backfill-go')").Scan(&events, &deliveries); e != nil || events != 4 || deliveries != 4 {
		t.Fatal(events, deliveries, e)
	}
	backfillFinish(t, db, "backfill-peer-ts", f.Steps[2:])
	backfillSeed(t, db, "backfill-peer-go", f.Events)
	backfillFinish(t, db, "backfill-peer-go", f.Steps[:2])
	clone := func(event map[string]any, key string, value any) map[string]any {
		copy := map[string]any{}
		for k, v := range event {
			copy[k] = v
		}
		copy[key] = value
		return copy
	}
	request, terminal := f.Events[0], f.Events[1]
	cases := []struct {
		scope  string
		events []map[string]any
		passes int
		phase  string
	}{
		{"backfill-duplicate-go", []map[string]any{request, clone(request, "eventId", "other-request")}, 0, "requests"},
		{"backfill-orphan-go", []map[string]any{terminal}, 2, "outcomes"},
		{"backfill-mismatch-go", []map[string]any{request, clone(terminal, "permission", "other")}, 2, "outcomes"},
		{"backfill-ambiguous-go", []map[string]any{request, terminal, clone(terminal, "eventId", "other-terminal")}, 2, "outcomes"},
	}
	for _, c := range cases {
		backfillSeed(t, db, c.scope, c.events)
		a := backfillPort(t, db, c.scope)
		for range c.passes {
			if _, e = a.BackfillDecisionPage(ctx, 100); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = a.BackfillDecisionPage(ctx, 100); !errors.Is(e, contract.ErrAuditUnavailable) {
			t.Fatal(c.scope, e)
		}
		var phase string
		var after *string
		var count int
		if e = db.QueryRow("SELECT phase,last_event_id,scanned_count FROM heimdall.audit_decision_backfill WHERE product_id=$1", c.scope).Scan(&phase, &after, &count); e != nil || phase != c.phase || after != nil || count != 0 {
			t.Fatal(phase, after, count, e)
		}
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM heimdall.audit_decisions WHERE product_id='backfill-duplicate-go'").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	var resolution string
	if e = db.QueryRow("SELECT resolution FROM heimdall.audit_decisions WHERE product_id='backfill-ambiguous-go'").Scan(&resolution); e != nil || resolution != "pending" {
		t.Fatal(resolution, e)
	}
	backfillSeed(t, db, "backfill-corrupt-go", f.Events)
	if _, e = db.Exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id='backfill-corrupt-go'"); e != nil {
		t.Fatal(e)
	}
	if _, e = backfillPort(t, db, "backfill-corrupt-go").BackfillDecisionPage(ctx, 100); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	ro, e := sql.Open("postgres", url+"&options=-c%20default_transaction_read_only%3Don")
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	if _, e = backfillPort(t, ro, "backfill-go").BackfillDecisionPage(ctx, 1); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.schema_metadata SET state='ready'"); e != nil {
		t.Fatal(e)
	}
	_, denied := backfillPort(t, db, "backfill-go").BackfillDecisionPage(ctx, 1)
	if _, e = db.Exec("UPDATE heimdall.schema_metadata SET state='migrating'"); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(denied, contract.ErrUnavailable) {
		t.Fatal(denied)
	}
	if _, e = backfillPort(t, db, "unregistered").BackfillDecisionPage(ctx, 1); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	lock, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback()
	if _, e = lock.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = backfillPort(t, db, "backfill-go").BackfillDecisionPage(deadline, 1)
	cancel()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if e = lock.Rollback(); e != nil {
		t.Fatal(e)
	}
	backfillSeed(t, db, "backfill-empty-go", nil)
	for _, phase := range []string{"outcomes", "complete"} {
		p, e := backfillPort(t, db, "backfill-empty-go").BackfillDecisionPage(ctx, 100)
		if e != nil || p.Phase != phase || p.ScannedCount != 0 || p.ExpectedCount == nil || *p.ExpectedCount != 0 || p.After != nil {
			t.Fatal(p, e)
		}
	}
	backfillSeed(t, db, "backfill-drift-go", f.Events)
	drift := backfillPort(t, db, "backfill-drift-go")
	for range 2 {
		if _, e = drift.BackfillDecisionPage(ctx, 100); e != nil {
			t.Fatal(e)
		}
	}
	extra := clone(f.Events[3], "eventId", "zz-new")
	extra["productId"] = "backfill-drift-go"
	if _, e = drift.Append(ctx, extra); e != nil {
		t.Fatal(e)
	}
	if _, e = drift.BackfillDecisionPage(ctx, 100); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	padded := make([]map[string]any, 100)
	for i := range padded {
		event := clone(f.Events[3], "eventId", fmt.Sprintf("padded-%03d", i))
		event["target"] = map[string]any{"type": "document", "id": strings.Repeat("🚀", 8192)}
		padded[i] = event
	}
	backfillSeed(t, db, "backfill-budget-go", padded)
	budget := backfillPort(t, db, "backfill-budget-go")
	progress, e := budget.BackfillDecisionPage(ctx, 100)
	if e != nil || progress.Processed < 1 || progress.Processed >= 100 {
		t.Fatal(progress, e)
	}
	for i := 0; i < 10 && progress.Phase != "complete"; i++ {
		progress, e = budget.BackfillDecisionPage(ctx, 100)
		if e != nil {
			t.Fatal(e)
		}
	}
	if progress.Phase != "complete" || progress.ScannedCount != 100 || progress.ExpectedCount == nil || *progress.ExpectedCount != 100 || progress.After != nil {
		t.Fatal(progress)
	}
	backfillSeed(t, db, "backfill-race-go", f.Events)
	a := backfillPort(t, db, "backfill-race-go")
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := a.BackfillDecisionPage(ctx, 1)
			if e != nil {
				t.Error(e)
				return
			}
			results <- p.Processed
		}()
	}
	wg.Wait()
	close(results)
	total := 0
	for n := range results {
		total += n
	}
	if total != 7 {
		t.Fatal(total)
	}
	backfillFinish(t, db, "backfill-race-go", f.Steps[8:])
}
func TestAuditBackfillLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_BACKFILL_LOSS")
	if url == "" {
		t.Skip("commit fault")
	}
	db, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_BACKFILL_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	fault, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer fault.Close()
	f := backfillData(t)
	backfillSeed(t, db, "backfill-loss-go", f.Events)
	if _, e = backfillPort(t, fault, "backfill-loss-go").BackfillDecisionPage(context.Background(), 100); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	var count, slots int
	var after string
	if e = db.QueryRow("SELECT scanned_count,last_event_id,(SELECT count(*) FROM heimdall.audit_decisions WHERE product_id='backfill-loss-go') FROM heimdall.audit_decision_backfill WHERE product_id='backfill-loss-go'").Scan(&count, &after, &slots); e != nil || count != 4 || after != "z-request" || slots != 2 {
		t.Fatal(count, after, slots, e)
	}
	backfillFinish(t, db, "backfill-loss-go", f.Steps[4:])
}
