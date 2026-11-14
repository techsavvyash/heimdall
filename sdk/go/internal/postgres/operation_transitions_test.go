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

func transitionMeta(id string, now int64) OperationTransitionMetadata {
	return OperationTransitionMetadata{EventID: id, RequestID: "request", PolicyVersion: "policy", OccurredAt: now, RecordedAt: now, NextAttemptAt: now}
}
func transitionCreate(t *testing.T, s *OperationRecords, f journalFixture, scope, id string) *ManagedOperation {
	t.Helper()
	a := f.Allocation
	a.WorkspaceID = id
	a.OperationID = id
	a.EventID = "create-" + id
	r, e := s.Create(context.Background(), createIntent(f, scope), id, a)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestOperationTransitionInput(t *testing.T) {
	f := journalData(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	s := operationStore(t, db, f, "transition-input", nil)
	if _, e := s.Advance(context.Background(), "op", f.CreateIntent.Actor, 0, "dispatch", transitionMeta("e", 102)); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if _, e := s.Advance(context.Background(), "op", f.CreateIntent.Actor, 1, "force", transitionMeta("e", 102)); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Advance(ctx, "op", f.CreateIntent.Actor, 1, "dispatch", transitionMeta("e", 102)); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestOperationTransition(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if dsn == "" {
		t.Skip("managed database")
	}
	db, e := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_managed_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	admin, _ := sql.Open("postgres", dsn)
	defer admin.Close()
	f := journalData(t)
	scope := "transition-go"
	s := operationStore(t, db, f, scope, []string{"primary"})
	w, _ := testWorkspaceRecords(db, scope, f.Binding.Environment)
	ctx := context.Background()
	var vectors struct {
		Scenarios []struct {
			Name  string
			Steps []struct{ Event, Expected string }
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/operation-transitions.json")
	if e != nil || json.Unmarshal(raw, &vectors) != nil {
		t.Fatal("fixture", e)
	}
	for _, scenario := range vectors.Scenarios {
		record := transitionCreate(t, s, f, scope, scenario.Name)
		for index, step := range scenario.Steps {
			after, err := s.Advance(ctx, record.OperationID, f.CreateIntent.Actor, record.Generation, step.Event, transitionMeta(scenario.Name+"-"+strconv.Itoa(index), 102+int64(index)))
			if step.Expected == "conflict" {
				if !errors.Is(err, contract.ErrConflict) {
					t.Fatal(step, after, err)
				}
			} else {
				expectedCount := record.DispatchCount
				if step.Event == "dispatch" {
					expectedCount++
				}
				if err != nil || after.State != step.Expected || after.Generation != record.Generation+1 || after.DispatchCount != expectedCount {
					t.Fatal(step, after, err)
				}
				record = after
			}
			got, err := s.Get(ctx, record.OperationID, f.CreateIntent.Actor)
			if err != nil || !reflect.DeepEqual(got, record) {
				t.Fatal(got, record, err)
			}
			workspace, err := w.Get(ctx, record.WorkspaceID)
			lifecycle := "pending"
			blocked := true
			if record.State == "completed" {
				lifecycle, blocked = "active", false
			}
			if record.State == "cancelled" {
				lifecycle, blocked = "tombstoned", false
			}
			if err != nil || workspace.Lifecycle != lifecycle || workspace.MutationBlocked != blocked {
				t.Fatal(workspace, err)
			}
		}
	}
	var n int
	if e = admin.QueryRow("SELECT count(*) FROM heimdall_managed.membership_candidates WHERE product_id=$1", scope).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	transitionCreate(t, s, f, scope, "race")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Advance(ctx, "race", f.CreateIntent.Actor, 1, "dispatch", transitionMeta("race-"+strconv.Itoa(i), 102))
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, contract.ErrConflict) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatal(won)
	}
	r, e := s.Get(ctx, "race", f.CreateIntent.Actor)
	if e != nil || r.DispatchCount != 1 {
		t.Fatal(r, e)
	}
	if e = admin.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1 AND event_id LIKE 'race-%'", scope).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	wrong := f.CreateIntent.Actor
	wrong.Subject = "wrong"
	if _, e = s.Advance(ctx, "race", wrong, 2, "confirm", transitionMeta("wrong", 103)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, "race", f.CreateIntent.Actor, 1, "confirm", transitionMeta("stale", 103)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	original := transitionCreate(t, s, f, scope, "rollback")
	if _, e = s.Advance(ctx, "rollback", f.CreateIntent.Actor, 1, "dispatch", transitionMeta("create-rollback", 102)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	r, e = s.Get(ctx, "rollback", f.CreateIntent.Actor)
	if e != nil || !reflect.DeepEqual(r, original) {
		t.Fatal(r, e)
	}
	if _, e = s.Advance(ctx, "rollback", f.CreateIntent.Actor, 1, "dispatch", transitionMeta("rollback-dispatch", 102)); e != nil {
		t.Fatal(e)
	}
	confirmed, e := s.Advance(ctx, "rollback", f.CreateIntent.Actor, 2, "confirm", transitionMeta("rollback-confirm", 103))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, "rollback", f.CreateIntent.Actor, 3, "complete", transitionMeta("create-rollback", 104)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	r, e = s.Get(ctx, "rollback", f.CreateIntent.Actor)
	if e != nil || !reflect.DeepEqual(r, confirmed) {
		t.Fatal(r, e)
	}
	wr, e := w.Get(ctx, "rollback")
	if e != nil || !wr.MutationBlocked {
		t.Fatal(wr, e)
	}
	if e = admin.QueryRow("SELECT count(*) FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND workspace_id='rollback'", scope).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	transitionCreate(t, s, f, scope, "due")
	m := transitionMeta("due-failure", 102)
	m.NextAttemptAt = 200
	if _, e = s.Advance(ctx, "due", f.CreateIntent.Actor, 1, "pre_dispatch_failure", m); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, "due", f.CreateIntent.Actor, 2, "retry", transitionMeta("early", 199)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, "due", f.CreateIntent.Actor, 2, "retry", transitionMeta("due-retry", 200)); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall_managed.operations SET generation=9007199254740991 WHERE product_id=$1 AND operation_id='due'", scope); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, "due", f.CreateIntent.Actor, maxSafe, "dispatch", transitionMeta("overflow", 201)); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	peer := operationStore(t, db, f, "transition-ts", nil)
	r, e = peer.Get(ctx, "complete", f.CreateIntent.Actor)
	if e != nil || r == nil || r.State != "completed" || r.Generation != 4 || r.DispatchCount != 1 {
		t.Fatal(r, e)
	}
}
func TestOperationTransitionLoss(t *testing.T) {
	proxy := os.Getenv("HEIMDALL_TEST_TRANSITION_LOSS")
	if proxy == "" {
		t.Skip("fault proxy")
	}
	direct, _ := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL"))
	defer direct.Close()
	db, _ := sql.Open("postgres", proxy+"&options=-c%20role%3Dheimdall_managed_runtime")
	defer db.Close()
	f := journalData(t)
	s := operationStore(t, direct, f, "transition-loss-go", []string{"primary"})
	transitionCreate(t, s, f, "transition-loss-go", "lost")
	_, e := operationStore(t, db, f, "transition-loss-go", []string{"primary"}).Advance(context.Background(), "lost", f.CreateIntent.Actor, 1, "dispatch", transitionMeta("lost-dispatch", 102))
	var ce *contract.Error
	if !errors.Is(e, contract.ErrOutcomeUnknown) || !errors.As(e, &ce) || ce.OperationID != "lost" {
		t.Fatal(e)
	}
	r, e := s.Get(context.Background(), "lost", f.CreateIntent.Actor)
	if e != nil || r.State != "dispatching" || r.Generation != 2 || r.DispatchCount != 1 {
		t.Fatal(r, e)
	}
	if _, e = s.Advance(context.Background(), "lost", f.CreateIntent.Actor, 1, "dispatch", transitionMeta("retry", 103)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	w, _ := testWorkspaceRecords(direct, "transition-loss-go", f.Binding.Environment)
	wr, e := w.Get(context.Background(), "lost")
	if e != nil || !wr.MutationBlocked {
		t.Fatal(wr, e)
	}
}

func TestOperationMembershipCompletion(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if dsn == "" {
		t.Skip("managed database")
	}
	admin, _ := sql.Open("postgres", dsn)
	defer admin.Close()
	db, _ := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_managed_runtime")
	defer db.Close()
	f := journalData(t)
	scope := "candidate-go"
	s := operationStore(t, db, f, scope, []string{"primary"})
	w, _ := testWorkspaceRecords(db, scope, f.Binding.Environment)
	ctx := context.Background()
	var vectors struct {
		Membership []struct {
			Name, Kind, Lifecycle string
			Existing              bool
			ObservedAt            int64
			Mismatch, Error       bool
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/operation-transitions.json")
	if e != nil || json.Unmarshal(raw, &vectors) != nil {
		t.Fatal("fixture", e)
	}
	actor := f.CreateIntent.Actor
	target := actor
	target.Subject = "new-owner"
	actorHash, _ := identityBinding(actor.Issuer, actor.Subject)
	targetHash, _ := identityBinding(target.Issuer, target.Subject)
	actorPrincipal, _ := contract.EncodeIdentity(actor.Issuer, actor.Subject)
	targetPrincipal, _ := contract.EncodeIdentity(target.Issuer, target.Subject)
	for _, c := range vectors.Membership {
		i := f.CreateIntent
		i.ProductID = scope
		i.Kind = c.Kind
		i.WorkspaceID = c.Name
		i.Name = ""
		i.Roles = []string{}
		i.Target = target
		d, err := contract.IntentDigests(i, c.Name)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(i)
		if _, err = admin.Exec("INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,$2,$3,'Membership',$4,true,101,103)", scope, f.Binding.Environment, c.Name, c.Lifecycle); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec(`INSERT INTO heimdall_managed.operations(product_id,environment,operation_id,workspace_id,kind,actor_hash,target_hash,key_digest,fingerprint,intent_payload,store_id,model_id,model_hash,manifest_version,state,dispatch_count,generation,next_attempt_at,created_at,updated_at)
    VALUES($1,$2,$3,$3,$4,$5,$6,decode($7,'hex'),decode($8,'hex'),$9,$10,$11,decode($12,'hex'),$13,'confirmed',1,3,103,101,103)`, scope, f.Binding.Environment, c.Name, c.Kind, actorHash, targetHash, d.KeyDigest, d.Fingerprint, payload, f.Binding.StoreID, f.Binding.ModelID, f.Binding.ModelHash, f.Binding.ManifestVersion); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec("INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES($1,$2,$3,$4,$5,101)", scope, f.Binding.Environment, c.Name, actorHash, actorPrincipal); err != nil {
			t.Fatal(err)
		}
		if c.Existing {
			principal := targetPrincipal
			if c.Mismatch {
				principal = actorPrincipal
			}
			if _, err = admin.Exec("INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES($1,$2,$3,$4,$5,$6)", scope, f.Binding.Environment, c.Name, targetHash, principal, c.ObservedAt); err != nil {
				t.Fatal(err)
			}
		}
		type candidate struct {
			Hash, Principal, Position, Revision string
			Observed                            int64
		}
		candidates := func() []candidate {
			rows, err := admin.Query("SELECT encode(identity_hash,'hex'),principal,position::text,revision::text,observed_at FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND workspace_id=$2 ORDER BY position", scope, c.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []candidate
			for rows.Next() {
				var r candidate
				if err = rows.Scan(&r.Hash, &r.Principal, &r.Position, &r.Revision, &r.Observed); err != nil {
					t.Fatal(err)
				}
				out = append(out, r)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			return out
		}
		before := candidates()
		result, err := s.Advance(ctx, c.Name, actor, 3, "complete", transitionMeta("complete-"+c.Name, 200))
		if c.Error {
			if !errors.Is(err, contract.ErrConflict) {
				t.Fatal(c.Name, result, err)
			}
		} else if err != nil || result.State != "completed" {
			t.Fatal(c.Name, result, err)
		}
		workspace, err := w.Get(ctx, c.Name)
		if err != nil || workspace.Lifecycle != c.Lifecycle || workspace.MutationBlocked != c.Error {
			t.Fatal(c.Name, workspace, err)
		}
		after := candidates()
		if !reflect.DeepEqual(before[0], after[0]) {
			t.Fatal("source membership changed", c.Name)
		}
		if c.Error {
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rollback", c.Name)
			}
		} else if c.Kind == "remove_member" {
			if len(after) != 1 {
				t.Fatal("removal", after)
			}
		} else {
			if len(after) != 2 || after[1].Principal != targetPrincipal || after[1].Observed != 200 {
				t.Fatal("target", after)
			}
			if c.Existing && (after[1].Position != before[1].Position || after[1].Revision == before[1].Revision) {
				t.Fatal("candidate position/revision", before, after)
			}
		}
		r, err := s.Get(ctx, c.Name, actor)
		generation := int64(4)
		if c.Error {
			generation = 3
		}
		if err != nil || r.Generation != generation {
			t.Fatal(r, err)
		}
	}
}
