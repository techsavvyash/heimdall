package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	neturl "net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

type decisionWriteFixture struct {
	NormalOutcomes    []string
	Mismatches        []map[string]any
	ConcurrentWriters int
}

func decisionEvents(t *testing.T, scope string) (map[string]any, map[string]any) {
	f := backfillData(t)
	for _, e := range f.Events {
		e["productId"] = scope
	}
	return f.Events[0], f.Events[1]
}
func decisionCounts(t *testing.T, db *sql.DB, scope string, n, d int) {
	t.Helper()
	var events, deliveries, decisions int
	e := db.QueryRow("SELECT (SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1),(SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id=$1),(SELECT count(*) FROM heimdall.audit_decisions WHERE product_id=$1)", scope).Scan(&events, &deliveries, &decisions)
	if e != nil || events != n || deliveries != n || decisions != d {
		t.Fatalf("counts %s: %d %d %d %v", scope, events, deliveries, decisions, e)
	}
}
func TestAuditDecisionInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	a := backfillPort(t, db, "input")
	r, c := decisionEvents(t, "input")
	c["outcome"] = "outcome_unknown"
	if _, e := a.AppendDecision(context.Background(), c); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if _, e := a.AppendDecision(nil, r); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := a.AppendDecision(ctx, r); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditDecision(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DECISION_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-decision-writes.json")
	if e != nil {
		t.Fatal(e)
	}
	var f decisionWriteFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	for _, outcome := range f.NormalOutcomes {
		scope := "decision-go-" + outcome
		a := backfillPort(t, db, scope)
		r, c := decisionEvents(t, scope)
		c["outcome"] = outcome
		first, e := a.AppendDecision(ctx, r)
		if e != nil {
			t.Fatal(e)
		}
		again, e := a.AppendDecision(ctx, r)
		if e != nil || !reflect.DeepEqual(first, again) {
			t.Fatal(e)
		}
		end, e := a.AppendDecision(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		again, e = a.AppendDecision(ctx, c)
		if e != nil || !reflect.DeepEqual(end, again) {
			t.Fatal(e)
		}
		again, e = a.AppendDecision(ctx, r)
		if e != nil || !reflect.DeepEqual(first, again) {
			t.Fatal(e)
		}
		decisionCounts(t, db, scope, 2, 1)
	}
	for i, patch := range f.Mismatches {
		scope := fmt.Sprintf("decision-go-bad-%d", i)
		a := backfillPort(t, db, scope)
		r, c := decisionEvents(t, scope)
		if _, e = a.AppendDecision(ctx, r); e != nil {
			t.Fatal(e)
		}
		for k, v := range patch {
			c[k] = v
		}
		if _, e = a.AppendDecision(ctx, c); !errors.Is(e, contract.ErrAuditUnavailable) {
			t.Fatal(e)
		}
		decisionCounts(t, db, scope, 1, 1)
	}
	scope := "decision-go-race"
	a := backfillPort(t, db, scope)
	r, c := decisionEvents(t, scope)
	var wg sync.WaitGroup
	errs := make(chan error, f.ConcurrentWriters)
	for i := 0; i < f.ConcurrentWriters; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := a.AppendDecision(ctx, r); errs <- e }()
	}
	wg.Wait()
	for i := 0; i < f.ConcurrentWriters; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < f.ConcurrentWriters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, terminal := decisionEvents(t, scope)
			terminal["eventId"] = fmt.Sprintf("terminal-%d", i)
			_, e := a.AppendDecision(ctx, terminal)
			errs <- e
		}(i)
	}
	wg.Wait()
	wins := 0
	for i := 0; i < f.ConcurrentWriters; i++ {
		e := <-errs
		if e == nil {
			wins++
		} else if !errors.Is(e, contract.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal(wins)
	}
	decisionCounts(t, db, scope, 2, 1)
	r["eventId"] = "other-request"
	if _, e = a.AppendDecision(ctx, r); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	decisionCounts(t, db, scope, 2, 1)
	_, c = decisionEvents(t, "decision-go-orphan")
	if _, e = backfillPort(t, db, "decision-go-orphan").AppendDecision(ctx, c); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	decisionCounts(t, db, "decision-go-orphan", 0, 0)
	a = backfillPort(t, db, "decision-go-corrupt")
	r, c = decisionEvents(t, "decision-go-corrupt")
	if _, e = a.AppendDecision(ctx, r); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id='decision-go-corrupt'"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.AppendDecision(ctx, c); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	decisionCounts(t, db, "decision-go-corrupt", 1, 1)
	r, _ = decisionEvents(t, "decision-go-fault")
	if _, e = backfillPort(t, db, "decision-go-fault").AppendDecision(ctx, r); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	decisionCounts(t, db, "decision-go-fault", 0, 0)
	failedScope := "decision-go-terminal-fault"
	r, c = decisionEvents(t, failedScope)
	withoutSink, e := NewAudit(db, failedScope, "production", nil, a.validate)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = withoutSink.AppendDecision(ctx, r); e != nil {
		t.Fatal(e)
	}
	if _, e = backfillPort(t, db, failedScope).AppendDecision(ctx, c); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	var eventsCount, deliveriesCount int
	var state string
	if e = db.QueryRow("SELECT (SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1),(SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id=$1),resolution FROM heimdall.audit_decisions WHERE product_id=$1", failedScope).Scan(&eventsCount, &deliveriesCount, &state); e != nil || eventsCount != 1 || deliveriesCount != 0 || state != "pending" {
		t.Fatalf("terminal rollback %d %d %s %v", eventsCount, deliveriesCount, state, e)
	}
	roURL, _ := neturl.Parse(url)
	q := roURL.Query()
	q.Set("options", "-c default_transaction_read_only=on")
	roURL.RawQuery = q.Encode()
	ro, e := sql.Open("postgres", roURL.String())
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	r, _ = decisionEvents(t, "decision-go-readonly")
	if _, e = backfillPort(t, ro, "decision-go-readonly").AppendDecision(ctx, r); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	lateScope := "decision-go-late"
	late := backfillPort(t, db, lateScope)
	var captured map[string]any
	runner, e := auditflow.New(lateScope, "production", func(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
		receipt, e := late.AppendDecision(ctx, event)
		if event["eventType"] == "heimdall.authorization.requested" {
			captured = event
		}
		return receipt, e
	}, late.validate, func() time.Time { return time.UnixMilli(2001) })
	if e != nil {
		t.Fatal(e)
	}
	r, _ = decisionEvents(t, lateScope)
	value, e := runner.Run(ctx, r, func(ctx context.Context) (bool, error) {
		terminal := map[string]any{}
		for k, v := range captured {
			terminal[k] = v
		}
		terminal["eventId"] = "rival-terminal"
		terminal["eventType"] = "heimdall.authorization.completed"
		terminal["outcome"] = "deny"
		terminal["recordedAt"] = float64(2001)
		terminal["occurredAt"] = float64(2001)
		_, e := late.AppendDecision(ctx, terminal)
		return true, e
	})
	if value || !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatalf("late release %v %v", value, e)
	}
	decisionCounts(t, db, lateScope, 2, 1)
	_, c = decisionEvents(t, "decision-peer-ts")
	a = backfillPort(t, db, "decision-peer-ts")
	receipt, e := a.AppendDecision(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	again, e := a.AppendDecision(ctx, c)
	if e != nil || !reflect.DeepEqual(receipt, again) {
		t.Fatal(e)
	}
	decisionCounts(t, db, "decision-peer-ts", 2, 1)
	r, _ = decisionEvents(t, "decision-peer-go")
	if _, e = backfillPort(t, db, "decision-peer-go").AppendDecision(ctx, r); e != nil {
		t.Fatal(e)
	}
}
func TestAuditDecisionLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DECISION_DATABASE_URL")
	loss := os.Getenv("HEIMDALL_TEST_DECISION_LOSS")
	if url == "" || loss == "" {
		t.Skip("fault proxy required")
	}
	db, _ := sql.Open("postgres", url)
	defer db.Close()
	fault, _ := sql.Open("postgres", loss)
	defer fault.Close()
	stage := os.Getenv("HEIMDALL_TEST_DECISION_STAGE")
	scope := "decision-loss-go-" + stage
	r, c := decisionEvents(t, scope)
	event := r
	n := 1
	if stage == "terminal" {
		if _, e := backfillPort(t, db, scope).AppendDecision(context.Background(), r); e != nil {
			t.Fatal(e)
		}
		event = c
		n = 2
	}
	if _, e := backfillPort(t, fault, scope).AppendDecision(context.Background(), event); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	receipt, e := backfillPort(t, db, scope).AppendDecision(context.Background(), event)
	if e != nil || receipt.EventID != event["eventId"] {
		t.Fatal(e)
	}
	decisionCounts(t, db, scope, n, 1)
}
