package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

type recoveryFixture struct {
	Request, Terminal, Administration map[string]any
	Cutoff                            int64
	BadAdministration                 []map[string]any
}

func recoveryData(t *testing.T, scope string) recoveryFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-recovery-write.json")
	if e != nil {
		t.Fatal(e)
	}
	var f recoveryFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	for _, event := range []map[string]any{f.Request, f.Terminal, f.Administration} {
		event["productId"] = scope
	}
	f.Administration["eventId"] = contract.RecoveryEventID(scope, "production", f.Terminal["eventId"].(string))
	return f
}
func recoveryCounts(t *testing.T, db *sql.DB, scope string, n int, state string) {
	t.Helper()
	decisionCounts(t, db, scope, n, 1)
	var actual string
	if e := db.QueryRow("SELECT resolution FROM heimdall.audit_decisions WHERE product_id=$1", scope).Scan(&actual); e != nil || actual != state {
		t.Fatal(actual, e)
	}
}
func TestAuditRecoveryInput(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-recovery-write.json")
	if e != nil {
		t.Fatal(e)
	}
	var f recoveryFixture
	json.Unmarshal(raw, &f)
	if contract.RecoveryEventID("ledger", "production", f.Terminal["eventId"].(string)) != f.Administration["eventId"] {
		t.Fatal("id mismatch")
	}
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	a := backfillPort(t, db, "ledger")
	for _, patch := range f.BadAdministration {
		admin := map[string]any{}
		for k, v := range f.Administration {
			admin[k] = v
		}
		for k, v := range patch {
			admin[k] = v
		}
		if _, e = a.RecoverDecision(context.Background(), f.Terminal, admin, f.Cutoff); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, cutoff := range []int64{-1, maxSafe + 1, 3000} {
		if _, e = a.RecoverDecision(context.Background(), f.Terminal, f.Administration, cutoff); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditRecovery(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RECOVERY_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	f := recoveryData(t, "recover-go")
	a := backfillPort(t, db, "recover-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	receipt, e := a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff)
	if e != nil {
		t.Fatal(e)
	}
	again, e := a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff)
	if e != nil || !reflect.DeepEqual(receipt, again) {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "recover-go", 3, "recovered")
	normal := map[string]any{}
	for k, v := range f.Terminal {
		normal[k] = v
	}
	normal["eventId"] = "normal-late"
	normal["outcome"] = "allow"
	if _, e = a.AppendDecision(ctx, normal); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	f.Administration["actor"] = map[string]any{"kind": "system", "id": "different-job"}
	if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "recover-go", 3, "recovered")
	f = recoveryData(t, "normal-first-go")
	a = backfillPort(t, db, "normal-first-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	normal = map[string]any{}
	for k, v := range f.Terminal {
		normal[k] = v
	}
	normal["eventId"] = "normal"
	normal["outcome"] = "allow"
	if _, e = a.AppendDecision(ctx, normal); e != nil {
		t.Fatal(e)
	}
	if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "normal-first-go", 2, "completed")
	f = recoveryData(t, "recent-go")
	a = backfillPort(t, db, "recent-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, 1000); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "recent-go", 1, "pending")
	f = recoveryData(t, "race-go")
	a = backfillPort(t, db, "race-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	normal = map[string]any{}
	for k, v := range f.Terminal {
		normal[k] = v
	}
	normal["eventId"] = "normal"
	normal["outcome"] = "allow"
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, e := a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff)
		errs <- e
	}()
	go func() { defer wg.Done(); _, e := a.AppendDecision(ctx, normal); errs <- e }()
	wg.Wait()
	wins := 0
	for i := 0; i < 2; i++ {
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
	var state string
	if e = db.QueryRow("SELECT resolution FROM heimdall.audit_decisions WHERE product_id='race-go'").Scan(&state); e != nil {
		t.Fatal(e)
	}
	n := 2
	if state == "recovered" {
		n = 3
	}
	recoveryCounts(t, db, "race-go", n, state)
	f = recoveryData(t, "many-go")
	a = backfillPort(t, db, "many-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	results := make(chan *AuditRecoveryReceipt, 8)
	errs = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff)
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	var previous *AuditRecoveryReceipt
	for i := 0; i < 8; i++ {
		r := <-results
		e := <-errs
		if e != nil {
			t.Fatal(e)
		}
		if previous != nil && !reflect.DeepEqual(previous, r) {
			t.Fatal("receipt changed")
		}
		previous = r
	}
	recoveryCounts(t, db, "many-go", 3, "recovered")
	f = recoveryData(t, "competing-go")
	a = backfillPort(t, db, "competing-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	errs = make(chan error, 8)
	for i := 0; i < 8; i++ {
		pair := recoveryData(t, "competing-go")
		id := fmt.Sprintf("attempt-%d", i)
		pair.Terminal["eventId"] = id
		pair.Administration["eventId"] = contract.RecoveryEventID("competing-go", "production", id)
		wg.Add(1)
		go func(pair recoveryFixture) {
			defer wg.Done()
			_, e := a.RecoverDecision(ctx, pair.Terminal, pair.Administration, pair.Cutoff)
			errs <- e
		}(pair)
	}
	wg.Wait()
	wins = 0
	for i := 0; i < 8; i++ {
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
	recoveryCounts(t, db, "competing-go", 3, "recovered")
	f = recoveryData(t, "recover-fault-go")
	a = backfillPort(t, db, "recover-fault-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "recover-fault-go", 1, "pending")
	f = recoveryData(t, "missing-go")
	a = backfillPort(t, db, "missing-go")
	if _, e = a.AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Append(ctx, f.Terminal); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_decisions SET resolution='recovered',terminal_event_id=$2,resolved_at=2001 WHERE product_id=$1", "missing-go", f.Terminal["eventId"]); e != nil {
		t.Fatal(e)
	}
	if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "missing-go", 2, "recovered")
	lateScope := "recovery-late-go"
	late := backfillPort(t, db, lateScope)
	var captured map[string]any
	runner, e := auditflow.New(lateScope, "production", func(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
		r, e := late.AppendDecision(ctx, event)
		if event["eventType"] == "heimdall.authorization.requested" {
			captured = event
		}
		return r, e
	}, late.validate, func() time.Time { return time.UnixMilli(2001) })
	if e != nil {
		t.Fatal(e)
	}
	f = recoveryData(t, lateScope)
	value, e := runner.Run(ctx, f.Request, func(ctx context.Context) (bool, error) {
		terminal := map[string]any{}
		for k, v := range captured {
			terminal[k] = v
		}
		terminal["eventId"] = "recovery-terminal"
		terminal["eventType"] = "heimdall.authorization.completed"
		terminal["outcome"] = "outcome_unknown"
		terminal["occurredAt"] = float64(2001)
		terminal["recordedAt"] = float64(2001)
		f.Administration["decisionId"] = captured["decisionId"]
		f.Administration["target"] = map[string]any{"type": "authorization_decision", "id": captured["decisionId"]}
		_, e := late.RecoverDecision(ctx, terminal, f.Administration, f.Cutoff)
		return true, e
	})
	if value || !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(value, e)
	}
	recoveryCounts(t, db, lateScope, 3, "recovered")
	f = recoveryData(t, "recovery-peer-ts")
	a = backfillPort(t, db, "recovery-peer-ts")
	if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); e != nil {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "recovery-peer-ts", 3, "recovered")
	f = recoveryData(t, "recovery-peer-go")
	if _, e = backfillPort(t, db, "recovery-peer-go").AppendDecision(ctx, f.Request); e != nil {
		t.Fatal(e)
	}
}
func TestAuditRecoveryLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RECOVERY_DATABASE_URL")
	loss := os.Getenv("HEIMDALL_TEST_RECOVERY_LOSS")
	if url == "" || loss == "" {
		t.Skip("fault proxy required")
	}
	db, _ := sql.Open("postgres", url)
	defer db.Close()
	fault, _ := sql.Open("postgres", loss)
	defer fault.Close()
	f := recoveryData(t, "recovery-loss-go")
	a := backfillPort(t, db, "recovery-loss-go")
	if _, e := a.AppendDecision(context.Background(), f.Request); e != nil {
		t.Fatal(e)
	}
	if _, e := backfillPort(t, fault, "recovery-loss-go").RecoverDecision(context.Background(), f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	recoveryCounts(t, db, "recovery-loss-go", 3, "recovered")
	r, e := a.RecoverDecision(context.Background(), f.Terminal, f.Administration, f.Cutoff)
	if e != nil || r.Administration.EventID != f.Administration["eventId"] {
		t.Fatal(e)
	}
}
