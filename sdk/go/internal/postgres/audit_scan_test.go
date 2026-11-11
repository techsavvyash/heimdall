package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	neturl "net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type scanFixture struct {
	Cutoff   int64
	Limit    int
	Requests []struct {
		DecisionID  string
		RequestedAt int64
		Complete    bool
	}
	Pages            [][]string
	BadCursorPatches []map[string]any
}

func scanData(t *testing.T) scanFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-decision-scan.json")
	if e != nil {
		t.Fatal(e)
	}
	var f scanFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func scanEvent(t *testing.T, scope, id string, at int64) map[string]any {
	r, _ := decisionEvents(t, scope)
	r["eventId"] = "request-" + id
	r["decisionId"] = id
	r["recordedAt"] = float64(at)
	r["occurredAt"] = float64(at)
	return r
}
func scanTerminal(e map[string]any) map[string]any {
	c := map[string]any{}
	for k, v := range e {
		c[k] = v
	}
	c["eventId"] = "terminal-" + e["decisionId"].(string)
	c["eventType"] = "heimdall.authorization.completed"
	c["outcome"] = "allow"
	return c
}
func scanSeed(t *testing.T, db *sql.DB, scope string) {
	t.Helper()
	for _, r := range scanData(t).Requests {
		a := backfillPort(t, db, scope)
		e := scanEvent(t, scope, r.DecisionID, r.RequestedAt)
		if _, err := a.AppendDecision(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if r.Complete {
			if _, err := a.AppendDecision(context.Background(), scanTerminal(e)); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func scanPages(t *testing.T, db *sql.DB, scope string) {
	t.Helper()
	f := scanData(t)
	var after *AuditDecisionCursor
	for _, ids := range f.Pages {
		page, e := backfillPort(t, db, scope).ScanPendingDecisions(context.Background(), f.Cutoff, after, f.Limit)
		if e != nil {
			t.Fatal(e)
		}
		actual := []string{}
		for _, item := range page.Items {
			actual = append(actual, item.DecisionID)
			if !reflect.DeepEqual(item.Request, scanEvent(t, scope, item.DecisionID, item.RequestedAt)) {
				t.Fatal("request mismatch")
			}
		}
		if !reflect.DeepEqual(actual, ids) {
			t.Fatal(actual, ids)
		}
		after = page.Next
	}
	if after != nil {
		t.Fatal("unexpected next")
	}
}
func TestAuditScanInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	a := backfillPort(t, db, "scan-input")
	ctx := context.Background()
	for _, limit := range []int{0, 101} {
		if _, e := a.ScanPendingDecisions(ctx, 2000, nil, limit); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, cutoff := range []int64{-1, maxSafe + 1} {
		if _, e := a.ScanPendingDecisions(ctx, cutoff, nil, 1); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, patch := range scanData(t).BadCursorPatches {
		m := map[string]any{"productId": "scan-input", "environment": "production", "cutoff": 2000, "requestedAt": 1001, "decisionId": "A"}
		for k, v := range patch {
			m[k] = v
		}
		raw, _ := json.Marshal(m)
		var cursor AuditDecisionCursor
		if json.Unmarshal(raw, &cursor) != nil {
			t.Fatal("cursor fixture")
		}
		if _, e := a.ScanPendingDecisions(ctx, 2000, &cursor, 1); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := a.ScanPendingDecisions(cancelled, 2000, nil, 1); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditScan(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_SCAN_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	scanSeed(t, db, "scan-go")
	scanPages(t, db, "scan-go")
	scanPages(t, db, "scan-peer-ts")
	scanSeed(t, db, "scan-peer-go")
	empty, e := backfillPort(t, db, "scan-empty").ScanPendingDecisions(ctx, 2000, nil, 1)
	if e != nil || len(empty.Items) != 0 || empty.Next != nil {
		t.Fatal(empty, e)
	}
	a := backfillPort(t, db, "scan-go")
	first, e := a.ScanPendingDecisions(ctx, 2000, nil, 2)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.AppendDecision(ctx, scanTerminal(scanEvent(t, "scan-go", "later", 1002))); e != nil {
		t.Fatal(e)
	}
	empty, e = a.ScanPendingDecisions(ctx, 2000, first.Next, 2)
	if e != nil || len(empty.Items) != 0 || empty.Next != nil {
		t.Fatal(empty, e)
	}
	for scope, statement := range map[string]string{"scan-corrupt-go": "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id=$1"} {
		p := backfillPort(t, db, scope)
		if _, e = p.AppendDecision(ctx, scanEvent(t, scope, "bad", 1001)); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(statement, scope); e != nil {
			t.Fatal(e)
		}
		if _, e = p.ScanPendingDecisions(ctx, 2000, nil, 1); !errors.Is(e, contract.ErrAuditUnavailable) {
			t.Fatal(e)
		}
	}
	oversize := backfillPort(t, db, "scan-oversize-go")
	if _, e = oversize.AppendDecision(ctx, scanEvent(t, "scan-oversize-go", "bad", 1001)); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_events SET event=event || jsonb_build_object('padding',repeat('x',5000000)) WHERE product_id='scan-oversize-go'"); e == nil {
		t.Fatal("oversized event accepted")
	}
	pageAfterRefusal, e := oversize.ScanPendingDecisions(ctx, 2000, nil, 1)
	if e != nil || len(pageAfterRefusal.Items) != 1 {
		t.Fatal(e)
	}
	binding := backfillPort(t, db, "scan-binding-go")
	if _, e = binding.Append(ctx, scanEvent(t, "scan-binding-go", "bad", 1001)); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO heimdall.audit_decisions VALUES('scan-binding-go','production','bad','request-bad',1000,'pending',NULL,NULL)"); e != nil {
		t.Fatal(e)
	}
	if _, e = binding.ScanPendingDecisions(ctx, 2000, nil, 1); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	roURL, _ := neturl.Parse(url)
	q := roURL.Query()
	q.Set("options", "-c default_transaction_read_only=on")
	roURL.RawQuery = q.Encode()
	ro, _ := sql.Open("postgres", roURL.String())
	defer ro.Close()
	if _, e = backfillPort(t, ro, "scan-empty").ScanPendingDecisions(ctx, 2000, nil, 1); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	lock, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec("LOCK heimdall.audit_decisions IN ACCESS EXCLUSIVE MODE"); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = a.ScanPendingDecisions(deadline, 2000, nil, 1)
	cancel()
	lock.Rollback()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	scanPages(t, db, "scan-peer-go")
	large := backfillPort(t, db, "scan-byte-go")
	for i := 0; i < 100; i++ {
		r := scanEvent(t, "scan-byte-go", fmt.Sprintf("d%03d", i), 1001)
		r["target"] = map[string]any{"type": "invoice", "id": strings.Repeat("🚀", 8192)}
		if _, e = large.AppendDecision(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	page, e := large.ScanPendingDecisions(ctx, 2000, nil, 100)
	if e != nil || len(page.Items) == 0 || len(page.Items) >= 100 || page.Next == nil {
		t.Fatal("byte page", e)
	}
	seen := map[string]bool{}
	for rounds := 0; ; rounds++ {
		for _, item := range page.Items {
			if seen[item.DecisionID] {
				t.Fatal("duplicate")
			}
			seen[item.DecisionID] = true
		}
		if page.Next == nil {
			break
		}
		if rounds > 10 {
			t.Fatal("repeated page")
		}
		page, e = large.ScanPendingDecisions(ctx, 2000, page.Next, 100)
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 100 {
		t.Fatal(len(seen))
	}
}
