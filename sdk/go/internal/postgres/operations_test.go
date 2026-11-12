package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

type journalFixture struct {
	Binding   OperationBinding
	ActorHash string
	Cases     []struct {
		Name  string
		Row   operationRow
		Valid bool
	}
	CreateIntent contract.Intent
	Key          string
	Allocation   OperationAllocation
}

func journalData(t *testing.T) journalFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/operation-journal.json")
	if e != nil {
		t.Fatal(e)
	}
	var f journalFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func TestOperationJournal(t *testing.T) {
	f := journalData(t)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			r, e := decodeOperation(c.Row, f.Binding, f.ActorHash)
			if !c.Valid {
				if !errors.Is(e, contract.ErrUnavailable) {
					t.Fatal(r, e)
				}
				return
			}
			if e != nil || r.State != c.Row.State {
				t.Fatal(r, e)
			}
		})
	}
}
func createIntent(f journalFixture, scope string) []byte {
	i := f.CreateIntent
	i.ProductID = scope
	raw, _ := json.Marshal(i)
	return raw
}
func createAllocation(f journalFixture, id string) OperationAllocation {
	a := f.Allocation
	a.WorkspaceID = "workspace-" + id
	a.OperationID = "operation-" + id
	a.EventID = "event-" + id
	return a
}
func testOperationRecords(db *sql.DB, b OperationBinding, sinks []string) (*OperationRecords, error) {
	if os.Getenv("HEIMDALL_TEST_MANAGED_OPERATIONS_V4") == "1" {
		return NewRetainedOperationRecords(db, b, sinks)
	}
	return NewOperationRecords(db, b, sinks)
}
func operationStore(t *testing.T, db *sql.DB, f journalFixture, scope string, sinks []string) *OperationRecords {
	t.Helper()
	b := f.Binding
	b.ProductID = scope
	s, e := testOperationRecords(db, b, sinks)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestOperationInput(t *testing.T) {
	f := journalData(t)
	db, e := sql.Open("postgres", "")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := operationStore(t, db, f, "creation-go", nil)
	ctx := context.Background()
	if _, e = s.Get(ctx, "bad\n", f.CreateIntent.Actor); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if _, e = s.Create(ctx, []byte("{}"), f.Key, f.Allocation); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if _, e = s.Create(ctx, createIntent(f, "other"), f.Key, f.Allocation); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.Create(cancelled, createIntent(f, "creation-go"), f.Key, f.Allocation); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if _, e = s.Get(cancelled, "missing", f.CreateIntent.Actor); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if _, e = s.Create(nil, createIntent(f, "creation-go"), f.Key, f.Allocation); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestOperationCreate(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_CREATE_RACE") != "" {
		t.Skip("live creation")
	}
	f := journalData(t)
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
	scope := "creation-go"
	s := operationStore(t, db, f, scope, []string{"primary", "secondary"})
	ctx := context.Background()
	counts := func(scope string, want []int) {
		t.Helper()
		if got := managedAuditCounts(t, admin, scope); !reflect.DeepEqual(got, want) {
			t.Fatal(scope, got)
		}
	}
	if r, e := s.Get(ctx, "missing", f.CreateIntent.Actor); e != nil || r != nil {
		t.Fatal(r, e)
	}
	first, e := s.Create(ctx, createIntent(f, scope), f.Key, createAllocation(f, "first"))
	if e != nil || first.State != "pending" || first.Generation != 1 || first.DispatchCount != 0 || first.Intent.Actor.Subject != f.CreateIntent.Actor.Subject {
		t.Fatal(first, e)
	}
	counts(scope, []int{1, 1, 0, 1, 2})
	var lifecycle string
	var blocked bool
	if e = admin.QueryRow("SELECT lifecycle,mutation_blocked FROM heimdall_managed.workspaces WHERE product_id=$1", scope).Scan(&lifecycle, &blocked); e != nil || lifecycle != "pending" || !blocked {
		t.Fatal(e)
	}
	if retry, e := s.Create(ctx, createIntent(f, scope), f.Key, createAllocation(f, "retry")); e != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal(retry, e)
	}
	changed := f
	changed.CreateIntent.Name = "Changed"
	if _, e = s.Create(ctx, createIntent(changed, scope), f.Key, createAllocation(f, "changed")); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	counts(scope, []int{1, 1, 0, 1, 2})
	other := f.CreateIntent.Actor
	other.Subject = "other"
	if r, e := s.Get(ctx, first.OperationID, other); e != nil || r != nil {
		t.Fatal(r, e)
	}
	if r, e := operationStore(t, db, f, "other", nil).Get(ctx, first.OperationID, f.CreateIntent.Actor); e != nil || r != nil {
		t.Fatal(r, e)
	}
	wrong := f
	wrong.Binding.ModelID = "different"
	if _, e = operationStore(t, db, wrong, scope, nil).Get(ctx, first.OperationID, f.CreateIntent.Actor); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall_managed.operations SET state='cancelled',generation=2,updated_at=102 WHERE product_id=$1", scope); e != nil {
		t.Fatal(e)
	}
	if r, e := s.Create(ctx, createIntent(f, scope), f.Key, createAllocation(f, "terminal")); e != nil || r.State != "cancelled" {
		t.Fatal(r, e)
	}
	counts(scope, []int{1, 1, 0, 1, 2})
	uncertain, e := s.Create(ctx, createIntent(f, scope), "uncertain-key", createAllocation(f, "uncertain"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall_managed.operations SET state='uncertain',dispatch_count=1,generation=2 WHERE product_id=$1 AND operation_id=$2", scope, uncertain.OperationID); e != nil {
		t.Fatal(e)
	}
	if r, e := s.Create(ctx, createIntent(f, scope), "uncertain-key", createAllocation(f, "never-replay")); e != nil || r.State != "uncertain" {
		t.Fatal(r, e)
	}
	collision := createAllocation(f, "collision")
	collision.EventID = "event-first"
	if _, e = s.Create(ctx, createIntent(f, scope), "audit-collision", collision); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	counts(scope, []int{2, 2, 0, 2, 4})
	raceScope := "creation-race-go"
	race := operationStore(t, db, f, raceScope, []string{"primary", "secondary"})
	var wg sync.WaitGroup
	results := make(chan *ManagedOperation, 8)
	failures := make(chan error, 8)
	for n := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r, e := race.Create(ctx, createIntent(f, raceScope), f.Key, createAllocation(f, "race-"+strconv.Itoa(n)))
			results <- r
			failures <- e
		}(n)
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	ids := map[string]bool{}
	for r := range results {
		ids[r.OperationID+"|"+r.WorkspaceID] = true
	}
	if len(ids) != 1 {
		t.Fatal(ids)
	}
	counts(raceScope, []int{1, 1, 0, 1, 2})
	conflictScope := "creation-conflict-go"
	conflict := operationStore(t, db, f, conflictScope, []string{"primary", "secondary"})
	outcomes := make(chan error, 2)
	for n := range 2 {
		go func(n int) {
			input := f
			if n == 1 {
				input.CreateIntent.Name = "Different"
			}
			_, e := conflict.Create(ctx, createIntent(input, conflictScope), f.Key, createAllocation(f, "race-"+strconv.Itoa(n)))
			outcomes <- e
		}(n)
	}
	success, clashes := 0, 0
	for range 2 {
		e := <-outcomes
		if e == nil {
			success++
		} else if errors.Is(e, contract.ErrConflict) {
			clashes++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || clashes != 1 {
		t.Fatal(success, clashes)
	}
	counts(conflictScope, []int{1, 1, 0, 1, 2})
	if _, e = admin.Exec("ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT create_go_failure CHECK(sink_id<>'z-create-blocked-go')"); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec("ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT IF EXISTS create_go_failure")
	if _, e = operationStore(t, db, f, "creation-fail-go", []string{"a-good", "z-create-blocked-go"}).Create(ctx, createIntent(f, "creation-fail-go"), f.Key, createAllocation(f, "failure")); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	counts("creation-fail-go", []int{0, 0, 0, 0, 0})
	readonly, e := sql.Open("postgres", url+"&options=-c%20default_transaction_read_only%3Don")
	if e != nil {
		t.Fatal(e)
	}
	defer readonly.Close()
	if _, e = operationStore(t, readonly, f, scope, nil).Get(ctx, "missing", f.CreateIntent.Actor); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	peer := operationStore(t, db, f, "creation-ts", []string{"new-sink"})
	r, e := peer.Get(ctx, "operation-first", f.CreateIntent.Actor)
	if e != nil || r == nil || r.State != "cancelled" || r.Intent.Name != f.CreateIntent.Name {
		t.Fatal(r, e)
	}
	retry, e := peer.Create(ctx, createIntent(f, "creation-ts"), f.Key, createAllocation(f, "go-peer-retry"))
	if e != nil || !reflect.DeepEqual(r, retry) {
		t.Fatal(retry, e)
	}
	counts("creation-ts", []int{2, 2, 0, 2, 4})
}
func TestOperationCreateLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CREATE_LOSS")
	if url == "" {
		t.Skip("create commit loss")
	}
	f := journalData(t)
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
	scope := "creation-loss-go"
	ctx := context.Background()
	r, e := operationStore(t, db, f, scope, []string{"primary", "secondary"}).Create(ctx, createIntent(f, scope), f.Key, createAllocation(f, "lost"))
	var unknown *contract.Error
	if r != nil || !errors.As(e, &unknown) || unknown.Code != contract.OutcomeUnknown || unknown.OperationID != "operation-lost" {
		t.Fatal(r, e)
	}
	if got := managedAuditCounts(t, direct, scope); !reflect.DeepEqual(got, []int{1, 1, 0, 1, 2}) {
		t.Fatal(got)
	}
	r, e = operationStore(t, direct, f, scope, []string{"new-sink"}).Create(ctx, createIntent(f, scope), f.Key, createAllocation(f, "retry"))
	if e != nil || r.OperationID != "operation-lost" || r.WorkspaceID != "workspace-lost" {
		t.Fatal(r, e)
	}
	if got := managedAuditCounts(t, direct, scope); !reflect.DeepEqual(got, []int{1, 1, 0, 1, 2}) {
		t.Fatal(got)
	}
}
func TestOperationCreateRaceCross(t *testing.T) {
	if os.Getenv("HEIMDALL_TEST_CREATE_RACE") == "" {
		t.Skip("cross language race")
	}
	f := journalData(t)
	db, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")+"&options=-c%20role%3Dheimdall_managed_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	r, e := operationStore(t, db, f, "creation-cross", []string{"primary", "secondary"}).Create(context.Background(), createIntent(f, "creation-cross"), f.Key, createAllocation(f, "cross-go"))
	if e != nil || (r.OperationID != "operation-cross-ts" && r.OperationID != "operation-cross-go") {
		t.Fatal(r, e)
	}
}
