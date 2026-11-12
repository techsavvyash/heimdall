package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

type membershipFixture struct {
	Kinds []struct {
		Kind      string
		Roles     []string
		EventType string
	}
	Gates []struct {
		Name, Lifecycle string
		Blocked         bool
	}
	InvalidRevisions []string
}

func membershipData(t *testing.T) membershipFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/membership-intents.json")
	if e != nil {
		t.Fatal(e)
	}
	var f membershipFixture
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func membershipIntent(f journalFixture, scope, id, kind string, roles []string) []byte {
	i := f.CreateIntent
	i.ProductID = scope
	i.WorkspaceID = id
	i.Name = ""
	i.Kind = kind
	i.Roles = roles
	i.Target.Subject = "target"
	raw, _ := json.Marshal(i)
	return raw
}
func membershipAllocation(f journalFixture, workspace, id string, now int64) OperationAllocation {
	a := f.Allocation
	a.WorkspaceID = workspace
	a.OperationID = id
	a.EventID = id
	a.OccurredAt = now
	a.RecordedAt = now
	return a
}
func membershipSeed(t *testing.T, db *sql.DB, scope, id, lifecycle string, blocked bool) string {
	t.Helper()
	var revision string
	e := db.QueryRow("INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,'production',$2,'Existing',$3,$4,100,100) RETURNING revision::text", scope, id, lifecycle, blocked).Scan(&revision)
	if e != nil {
		t.Fatal(e)
	}
	return revision
}
func TestMembershipInput(t *testing.T) {
	f := journalData(t)
	vectors := membershipData(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	s := operationStore(t, db, f, "membership-input", nil)
	ctx := context.Background()
	raw := membershipIntent(f, "membership-input", "w", "add_member", []string{"viewer"})
	a := membershipAllocation(f, "w", "op", 200)
	for _, revision := range vectors.InvalidRevisions {
		if _, e := s.BeginMembership(ctx, raw, "key", a, revision); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	if _, e := s.BeginMembership(ctx, createIntent(f, "membership-input"), "key", a, "1"); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if _, e := s.Create(ctx, raw, "key", a); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	wrong := a
	wrong.WorkspaceID = "other"
	if _, e := s.BeginMembership(ctx, raw, "key", wrong, "1"); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.BeginMembership(cancelled, raw, "key", a, "1"); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestMembershipIntent(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if dsn == "" {
		t.Skip("managed database")
	}
	admin, _ := sql.Open("postgres", dsn)
	defer admin.Close()
	db, _ := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_managed_runtime")
	defer db.Close()
	f := journalData(t)
	vectors := membershipData(t)
	scope := "membership-go"
	s := operationStore(t, db, f, scope, []string{"primary"})
	w, _ := testWorkspaceRecords(db, scope, "production")
	ctx := context.Background()
	for _, c := range vectors.Kinds {
		revision := membershipSeed(t, admin, scope, c.Kind, "active", false)
		raw := membershipIntent(f, scope, c.Kind, c.Kind, c.Roles)
		first, e := s.BeginMembership(ctx, raw, "same-key-per-kind", membershipAllocation(f, c.Kind, c.Kind, 200), revision)
		if e != nil || first.State != "pending" || first.Generation != 1 || first.DispatchCount != 0 || first.Intent.Kind != c.Kind || !reflect.DeepEqual(first.Intent.Roles, c.Roles) {
			t.Fatal(first, e)
		}
		workspace, e := w.Get(ctx, c.Kind)
		if e != nil || workspace.Lifecycle != "active" || !workspace.MutationBlocked || workspace.Revision == revision {
			t.Fatal(workspace, e)
		}
		duplicate, e := s.BeginMembership(ctx, raw, "same-key-per-kind", membershipAllocation(f, c.Kind, "duplicate-"+c.Kind, 200), revision)
		if e != nil || !reflect.DeepEqual(first, duplicate) {
			t.Fatal(duplicate, e)
		}
		var wrapperRaw []byte
		if e = admin.QueryRow("SELECT event FROM heimdall.audit_events WHERE product_id=$1 AND event_id=$2", scope, c.Kind).Scan(&wrapperRaw); e != nil {
			t.Fatal(e)
		}
		var wrapper struct{ Payload string }
		if json.Unmarshal(wrapperRaw, &wrapper) != nil {
			t.Fatal("wrapper")
		}
		eventRaw, e := base64.RawURLEncoding.DecodeString(wrapper.Payload)
		if e != nil {
			t.Fatal(e)
		}
		var event struct{ EventType string }
		if json.Unmarshal(eventRaw, &event) != nil || event.EventType != c.EventType {
			t.Fatal(event)
		}
		var changed contract.Intent
		json.Unmarshal(raw, &changed)
		changed.Target.Subject = "different"
		changedRaw, _ := json.Marshal(changed)
		if _, e = s.BeginMembership(ctx, changedRaw, "same-key-per-kind", membershipAllocation(f, c.Kind, "changed-"+c.Kind, 200), revision); !errors.Is(e, contract.ErrConflict) {
			t.Fatal(e)
		}
		if _, e = s.Advance(ctx, c.Kind, f.CreateIntent.Actor, 1, "dispatch", transitionMeta("dispatch-"+c.Kind, 201)); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Advance(ctx, c.Kind, f.CreateIntent.Actor, 2, "confirm", transitionMeta("confirm-"+c.Kind, 202)); e != nil {
			t.Fatal(e)
		}
		done, e := s.Advance(ctx, c.Kind, f.CreateIntent.Actor, 3, "complete", transitionMeta("complete-"+c.Kind, 203))
		if e != nil {
			t.Fatal(e)
		}
		workspace, e = w.Get(ctx, c.Kind)
		if e != nil || workspace.MutationBlocked {
			t.Fatal(workspace, e)
		}
		duplicate, e = s.BeginMembership(ctx, raw, "same-key-per-kind", membershipAllocation(f, c.Kind, "terminal-"+c.Kind, 200), revision)
		if e != nil || !reflect.DeepEqual(done, duplicate) {
			t.Fatal(duplicate, e)
		}
	}
	for _, c := range vectors.Gates {
		revision := membershipSeed(t, admin, scope, c.Name, c.Lifecycle, c.Blocked)
		if _, e := s.BeginMembership(ctx, membershipIntent(f, scope, c.Name, "add_member", []string{"viewer"}), c.Name, membershipAllocation(f, c.Name, "gate-"+c.Name, 200), revision); !errors.Is(e, contract.ErrConflict) {
			t.Fatal(c, e)
		}
	}
	stale := membershipSeed(t, admin, scope, "stale", "active", false)
	if _, e := admin.Exec("UPDATE heimdall_managed.workspaces SET updated_at=101 WHERE product_id=$1 AND workspace_id='stale'", scope); e != nil {
		t.Fatal(e)
	}
	if _, e := s.BeginMembership(ctx, membershipIntent(f, scope, "stale", "add_member", []string{"viewer"}), "stale", membershipAllocation(f, "stale", "stale", 200), stale); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	revision := membershipSeed(t, admin, scope, "race", "active", false)
	raw := membershipIntent(f, scope, "race", "add_member", []string{"viewer"})
	type result struct {
		r *ManagedOperation
		e error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			r, e := s.BeginMembership(ctx, raw, "race-key", membershipAllocation(f, "race", "race-"+strconv.Itoa(index), 200), revision)
			results <- result{r, e}
		}(index)
	}
	wg.Wait()
	close(results)
	var first *ManagedOperation
	for got := range results {
		if got.e != nil {
			t.Fatal(got.e)
		}
		if first == nil {
			first = got.r
		}
		if got.r.OperationID != first.OperationID {
			t.Fatal(got.r, first)
		}
	}
	var committedCount int
	if e := admin.QueryRow("SELECT count(*) FROM heimdall_managed.operations WHERE product_id=$1 AND workspace_id='race'", scope).Scan(&committedCount); e != nil || committedCount != 1 {
		t.Fatal(committedCount, e)
	}
	if e := admin.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1 AND event_id LIKE 'race-%'", scope).Scan(&committedCount); e != nil || committedCount != 1 {
		t.Fatal(committedCount, e)
	}
	if _, e := s.Advance(ctx, first.OperationID, f.CreateIntent.Actor, 1, "dispatch", transitionMeta("race-dispatch", 201)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Advance(ctx, first.OperationID, f.CreateIntent.Actor, 2, "uncertain", transitionMeta("race-uncertain", 202)); e != nil {
		t.Fatal(e)
	}
	blocked, e := w.Get(ctx, "race")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginMembership(ctx, raw, "successor", membershipAllocation(f, "race", "successor", 203), blocked.Revision); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	retry, e := s.BeginMembership(ctx, raw, "race-key", membershipAllocation(f, "race", "uncertain-retry", 203), revision)
	if e != nil || retry.State != "uncertain" {
		t.Fatal(retry, e)
	}
	competing := membershipSeed(t, admin, scope, "competing", "active", false)
	out := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := s.BeginMembership(ctx, membershipIntent(f, scope, "competing", "add_member", []string{"viewer"}), "different-"+strconv.Itoa(index), membershipAllocation(f, "competing", "competing-"+strconv.Itoa(index), 200), competing)
			out <- err
		}(index)
	}
	wg.Wait()
	close(out)
	won := 0
	for err := range out {
		if err == nil {
			won++
		} else if !errors.Is(err, contract.ErrConflict) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatal(won)
	}
	rollback := membershipSeed(t, admin, scope, "rollback", "active", false)
	before, e := w.Get(ctx, "rollback")
	if e != nil {
		t.Fatal(e)
	}
	a := membershipAllocation(f, "rollback", "rollback", 200)
	a.EventID = "add_member"
	if _, e = s.BeginMembership(ctx, membershipIntent(f, scope, "rollback", "add_member", []string{"viewer"}), "rollback", a, rollback); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	after, e := w.Get(ctx, "rollback")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal(after, e)
	}
	missing, e := s.Get(ctx, "rollback", f.CreateIntent.Actor)
	if e != nil || missing != nil {
		t.Fatal(missing, e)
	}
	cancelRevision := membershipSeed(t, admin, scope, "cancel", "active", false)
	cancelRaw := membershipIntent(f, scope, "cancel", "add_member", []string{"viewer"})
	if _, e = s.BeginMembership(ctx, cancelRaw, "cancel", membershipAllocation(f, "cancel", "cancel", 200), cancelRevision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, "cancel", f.CreateIntent.Actor, 1, "cancel", transitionMeta("cancelled", 201)); e != nil {
		t.Fatal(e)
	}
	workspace, e := w.Get(ctx, "cancel")
	if e != nil || workspace.Lifecycle != "active" || workspace.MutationBlocked {
		t.Fatal(workspace, e)
	}
	if r, err := s.BeginMembership(ctx, cancelRaw, "after-cancel", membershipAllocation(f, "cancel", "after-cancel", 202), workspace.Revision); err != nil || r.State != "pending" {
		t.Fatal(r, err)
	}
	peer := operationStore(t, db, f, "membership-ts", []string{"primary"})
	for _, c := range vectors.Kinds {
		original, err := peer.Get(ctx, c.Kind, f.CreateIntent.Actor)
		if err != nil || original == nil || original.State != "completed" {
			t.Fatal(original, err)
		}
		duplicate, err := peer.BeginMembership(ctx, membershipIntent(f, "membership-ts", c.Kind, c.Kind, c.Roles), "same-key-per-kind", membershipAllocation(f, c.Kind, "peer-"+c.Kind, 300), "1")
		if err != nil || !reflect.DeepEqual(original, duplicate) {
			t.Fatal(duplicate, err)
		}
	}
}
func TestMembershipIntentLoss(t *testing.T) {
	proxy := os.Getenv("HEIMDALL_TEST_MEMBERSHIP_LOSS")
	if proxy == "" {
		t.Skip("fault proxy")
	}
	direct, _ := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL"))
	defer direct.Close()
	db, _ := sql.Open("postgres", proxy+"&options=-c%20role%3Dheimdall_managed_runtime")
	defer db.Close()
	f := journalData(t)
	scope := "membership-loss-go"
	revision := membershipSeed(t, direct, scope, "lost", "active", false)
	raw := membershipIntent(f, scope, "lost", "add_member", []string{"viewer"})
	s := operationStore(t, db, f, scope, []string{"primary"})
	ctx := context.Background()
	_, e := s.BeginMembership(ctx, raw, "key", membershipAllocation(f, "lost", "lost", 200), revision)
	var ce *contract.Error
	if !errors.Is(e, contract.ErrOutcomeUnknown) || !errors.As(e, &ce) || ce.OperationID != "lost" {
		t.Fatal(e)
	}
	reader := operationStore(t, direct, f, scope, []string{"primary"})
	r, e := reader.Get(ctx, "lost", f.CreateIntent.Actor)
	if e != nil || r.State != "pending" || r.DispatchCount != 0 {
		t.Fatal(r, e)
	}
	retry, e := reader.BeginMembership(ctx, raw, "key", membershipAllocation(f, "lost", "retry", 200), revision)
	if e != nil || !reflect.DeepEqual(r, retry) {
		t.Fatal(retry, e)
	}
	w, _ := testWorkspaceRecords(direct, scope, "production")
	workspace, e := w.Get(ctx, "lost")
	if e != nil || !workspace.MutationBlocked {
		t.Fatal(workspace, e)
	}
}
