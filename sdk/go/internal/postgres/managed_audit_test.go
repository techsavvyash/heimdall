package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"strings"
	"testing"
)

type managedAuditFixture struct {
	Event  map[string]any
	Writes []string
	Cases  []struct {
		Name                   string
		Error                  *string
		ManagedRows, AuditRows int
	}
}

func managedAuditData(t *testing.T) managedAuditFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/managed-audit-transaction.json")
	if e != nil {
		t.Fatal(e)
	}
	var f managedAuditFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func managedAuditValidate(e map[string]any) error {
	if err := contract.ValidateAuditShape(e); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return contract.ErrInvalidArgument
	}
	var event contract.AuditEvent
	if json.Unmarshal(raw, &event) != nil {
		return contract.ErrInvalidArgument
	}
	return contract.ValidateAuditEvent(event, "sdk", func(string, int, map[string]any) bool { return false })
}
func managedWrites(ctx context.Context, tx *sql.Tx, f managedAuditFixture, scope string) error {
	for _, q := range f.Writes {
		if _, e := tx.ExecContext(ctx, strings.ReplaceAll(q, "$scope", scope)); e != nil {
			return e
		}
	}
	return nil
}
func managedAuditCounts(t *testing.T, db *sql.DB, scope string) []int {
	t.Helper()
	var counts []int
	for _, table := range []string{"heimdall_managed.workspaces", "heimdall_managed.operations", "heimdall_managed.membership_candidates", "heimdall.audit_events", "heimdall.audit_deliveries"} {
		var n int
		if e := db.QueryRow("SELECT count(*) FROM "+table+" WHERE product_id=$1", scope).Scan(&n); e != nil {
			t.Fatal(e)
		}
		counts = append(counts, n)
	}
	return counts
}
func TestManagedAuditInvalid(t *testing.T) {
	db, e := sql.Open("postgres", "")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	a, _ := NewAudit(db, "ledger", "production", nil, managedAuditValidate)
	called := false
	fn := func(context.Context, *sql.Tx) error { called = true; return nil }
	if _, e = a.appendWith(context.Background(), map[string]any{}, fn); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.appendWith(ctx, managedAuditData(t).Event, fn); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if _, e = a.appendWith(nil, managedAuditData(t).Event, fn); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if called {
		t.Fatal("invoked before validation")
	}
}
func TestManagedAuditAtomic(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" {
		t.Skip("managed atomic audit")
	}
	admin, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_managed_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = admin.Exec("ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT atomic_go_failure CHECK(sink_id<>'atomic-denied-go')"); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec("ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT IF EXISTS atomic_go_failure")
	f := managedAuditData(t)
	ctx := context.Background()
	for _, c := range f.Cases {
		scope := "atomic-go-" + c.Name
		event := managedAuditData(t).Event
		event["productId"] = scope
		event["eventId"] = "atomic-event"
		sinks := []string{"primary", "secondary"}
		if c.Name == "outbox_failure" {
			sinks = []string{"a-good", "atomic-denied-go"}
		}
		a, e := NewAudit(db, scope, "production", sinks, managedAuditValidate)
		if e != nil {
			t.Fatal(e)
		}
		if c.Name == "audit_conflict" {
			event["action"] = "different"
			if _, e = a.Append(ctx, event); e != nil {
				t.Fatal(e)
			}
			event["action"] = "workspace.create"
		}
		digest, e := contract.AuditDigest(event)
		if e != nil {
			t.Fatal(e)
		}
		operationContext, cancel := context.WithCancel(ctx)
		receipt, e := a.appendWith(operationContext, event, func(ctx context.Context, tx *sql.Tx) error {
			if e := managedWrites(ctx, tx, f, scope); e != nil {
				return e
			}
			event["action"] = "caller-mutated-after-snapshot"
			if c.Name == "mutation_failure" {
				return contract.ErrConflict
			}
			if c.Name == "mutation_unique" {
				_, err := tx.ExecContext(ctx, strings.ReplaceAll(f.Writes[0], "$scope", scope))
				return err
			}
			if c.Name == "cancel_before_commit" {
				cancel()
			}
			return nil
		})
		cancel()
		if c.Error == nil {
			if e != nil || receipt.PayloadDigest != digest {
				t.Fatal(c.Name, receipt, e)
			}
		} else {
			if e == nil || e.Error() != *c.Error || receipt != (contract.AuditReceipt{}) {
				t.Fatal(c.Name, receipt, e)
			}
		}
		if got := managedAuditCounts(t, admin, scope); !reflect.DeepEqual(got, []int{c.ManagedRows, c.ManagedRows, c.ManagedRows, c.AuditRows, c.AuditRows * 2}) {
			t.Fatal(c.Name, got)
		}
	}
}
func TestManagedAuditCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_AUDIT_LOSS")
	if url == "" {
		t.Skip("managed audit commit loss")
	}
	direct, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer direct.Close()
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_managed_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	f := managedAuditData(t)
	scope := "atomic-loss-go"
	event := f.Event
	event["productId"] = scope
	event["eventId"] = "atomic-event"
	ctx := context.Background()
	a, e := NewAudit(db, scope, "production", []string{"primary", "secondary"}, managedAuditValidate)
	if e != nil {
		t.Fatal(e)
	}
	r, e := a.appendWith(ctx, event, func(ctx context.Context, tx *sql.Tx) error { return managedWrites(ctx, tx, f, scope) })
	if !errors.Is(e, contract.ErrOutcomeUnknown) || r != (contract.AuditReceipt{}) {
		t.Fatal(r, e)
	}
	if got := managedAuditCounts(t, direct, scope); !reflect.DeepEqual(got, []int{1, 1, 1, 1, 2}) {
		t.Fatal(got)
	}
	recovery, _ := NewAudit(direct, scope, "production", []string{"new-sink"}, managedAuditValidate)
	if _, e = recovery.Append(ctx, event); e != nil {
		t.Fatal(e)
	}
	if got := managedAuditCounts(t, direct, scope); !reflect.DeepEqual(got, []int{1, 1, 1, 1, 2}) {
		t.Fatal(got)
	}
}
