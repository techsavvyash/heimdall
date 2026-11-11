package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"sync"
	"testing"
)

type repairFixture struct {
	Observations []CandidateObservation
	Gates        []struct {
		Lifecycle string
		Blocked   bool
	}
}

func repairData(t *testing.T) repairFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/candidate-repair.json")
	if e != nil {
		t.Fatal(e)
	}
	var v repairFixture
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func repairMeta(id string, now int64) CandidateRepairMetadata {
	return CandidateRepairMetadata{EventID: id, RequestID: "request", PolicyVersion: "policy", OccurredAt: now, RecordedAt: now}
}
func TestCandidateRepairInput(t *testing.T) {
	f, v := journalData(t), repairData(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	s := operationStore(t, db, f, "repair-input", nil)
	ctx := context.Background()
	for _, batch := range [][]CandidateObservation{nil, {v.Observations[0], v.Observations[0]}, make([]CandidateObservation, 101)} {
		if _, e := s.RepairCandidates(ctx, "w", "1", batch, repairMeta("e", 200)); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	if _, e := s.RepairCandidates(ctx, "w", "01", v.Observations, repairMeta("e", 200)); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	future := v.Observations[0]
	future.ObservedAt = 201
	if _, e := s.RepairCandidates(ctx, "w", "1", []CandidateObservation{future}, repairMeta("e", 200)); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.RepairCandidates(cancelled, "w", "1", v.Observations, repairMeta("e", 200)); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestCandidateRepair(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if dsn == "" {
		t.Skip("managed database")
	}
	admin, _ := sql.Open("postgres", dsn)
	defer admin.Close()
	db, _ := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_managed_runtime")
	defer db.Close()
	ctx := context.Background()
	f, v := journalData(t), repairData(t)
	scope := "repair-go"
	s := operationStore(t, db, f, scope, []string{"primary"})
	w, _ := NewWorkspaceRecords(db, scope, "production")
	type candidate struct{ Principal, Position, Revision, Observed string }
	candidates := func(id string) []candidate {
		rows, e := admin.Query("SELECT principal,position::text,revision::text,observed_at::text FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND workspace_id=$2 ORDER BY position", scope, id)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		out := []candidate{}
		for rows.Next() {
			var r candidate
			if e = rows.Scan(&r.Principal, &r.Position, &r.Revision, &r.Observed); e != nil {
				t.Fatal(e)
			}
			out = append(out, r)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	revision := membershipSeed(t, admin, scope, "w", "active", false)
	first, e := s.RepairCandidates(ctx, "w", revision, v.Observations, repairMeta("first", 200))
	if e != nil || first.Lifecycle != "active" || first.MutationBlocked || first.Revision == revision {
		t.Fatal(first, e)
	}
	both := candidates("w")
	if len(both) != 2 {
		t.Fatal(both)
	}
	if _, e = s.RepairCandidates(ctx, "w", revision, v.Observations, repairMeta("stale", 200)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	removal := v.Observations[0]
	removal.Member = false
	removal.ObservedAt = 201
	removed, e := s.RepairCandidates(ctx, "w", first.Revision, []CandidateObservation{removal}, repairMeta("removed", 201))
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(candidates("w"), both[1:]) {
		t.Fatal("omitted member removed")
	}
	before, e := w.Get(ctx, "w")
	if e != nil {
		t.Fatal(e)
	}
	removeB := v.Observations[1]
	removeB.Member = false
	removeB.ObservedAt = 202
	addA := v.Observations[0]
	addA.ObservedAt = 202
	if _, e = s.RepairCandidates(ctx, "w", removed.Revision, []CandidateObservation{removeB, addA}, repairMeta("first", 202)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	after, e := w.Get(ctx, "w")
	if e != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(candidates("w"), both[1:]) {
		t.Fatal("rollback", after, e)
	}
	old := v.Observations[1]
	old.ObservedAt = 199
	if _, e = s.RepairCandidates(ctx, "w", removed.Revision, []CandidateObservation{old}, repairMeta("old", 202)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	updatedB := v.Observations[1]
	updatedB.ObservedAt = 202
	updated, e := s.RepairCandidates(ctx, "w", removed.Revision, []CandidateObservation{updatedB}, repairMeta("updated", 202))
	if e != nil || updated.Revision == removed.Revision {
		t.Fatal(updated, e)
	}
	if _, e = s.RepairCandidates(ctx, "w", updated.Revision, []CandidateObservation{v.Observations[0]}, repairMeta("old-absent", 203)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	last := candidates("w")
	if last[0].Position != both[1].Position || last[0].Revision == both[1].Revision {
		t.Fatal(last, both)
	}
	for _, g := range v.Gates {
		id := g.Lifecycle
		if g.Blocked {
			id = "blocked"
		}
		rev := membershipSeed(t, admin, scope, id, g.Lifecycle, g.Blocked)
		if _, e = s.RepairCandidates(ctx, id, rev, v.Observations, repairMeta("gate-"+id, 200)); !errors.Is(e, contract.ErrConflict) {
			t.Fatal(g, e)
		}
	}
	raceRevision := membershipSeed(t, admin, scope, "race", "active", false)
	intent := membershipIntent(f, scope, "race", "add_member", []string{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.RepairCandidates(ctx, "race", raceRevision, v.Observations, repairMeta("race-repair", 200))
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, err := s.BeginMembership(ctx, intent, "race", membershipAllocation(f, "race", "race-mutation", 200), raceRevision)
		results <- err
	}()
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
	var count int
	if e = admin.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1 AND event_id IN ('race-repair','race-mutation')", scope).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	gate := membershipSeed(t, admin, scope, "changed", "active", false)
	if _, e = s.BeginMembership(ctx, membershipIntent(f, scope, "changed", "add_member", []string{}), "changed", membershipAllocation(f, "changed", "changed", 200), gate); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Advance(ctx, "changed", f.CreateIntent.Actor, 1, "cancel", transitionMeta("changed-cancel", 201)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RepairCandidates(ctx, "changed", gate, v.Observations, repairMeta("late", 202)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	if len(candidates("changed")) != 0 {
		t.Fatal("stale repair published")
	}
	source, _ := NewCandidateRecords(db, "repair-ts", "production")
	page, err := source.Scan(ctx, v.Observations[1].Identity, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	matching := 0
	for _, item := range page.Items {
		if item.Workspace.WorkspaceID == "w" {
			matching++
		}
	}
	if matching != 1 {
		t.Fatal(page)
	}
	peer, _ := NewWorkspaceRecords(db, "repair-ts", "production")
	wr, e := peer.Get(ctx, "w")
	if e != nil || wr == nil || wr.MutationBlocked {
		t.Fatal(wr, e)
	}
	if e = admin.QueryRow("SELECT count(*) FROM heimdall_managed.membership_candidates WHERE product_id='repair-ts' AND workspace_id='w'").Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if _, e = operationStore(t, db, f, "repair-ts", nil).RepairCandidates(ctx, "w", "1", v.Observations, repairMeta("peer", 300)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
}
func TestCandidateRepairLoss(t *testing.T) {
	proxy := os.Getenv("HEIMDALL_TEST_REPAIR_LOSS")
	if proxy == "" {
		t.Skip("fault proxy")
	}
	direct, _ := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL"))
	defer direct.Close()
	db, _ := sql.Open("postgres", proxy+"&options=-c%20role%3Dheimdall_managed_runtime")
	defer db.Close()
	f, v := journalData(t), repairData(t)
	scope := "repair-loss-go"
	revision := membershipSeed(t, direct, scope, "w", "active", false)
	ctx := context.Background()
	if _, e := operationStore(t, db, f, scope, []string{"primary"}).RepairCandidates(ctx, "w", revision, v.Observations, repairMeta("lost", 200)); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	w, _ := NewWorkspaceRecords(direct, scope, "production")
	workspace, e := w.Get(ctx, "w")
	if e != nil || workspace.Revision == revision {
		t.Fatal(workspace, e)
	}
	var count int
	if e = direct.QueryRow("SELECT count(*) FROM heimdall_managed.membership_candidates WHERE product_id=$1", scope).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	if _, e = operationStore(t, direct, f, scope, []string{"primary"}).RepairCandidates(ctx, "w", revision, v.Observations, repairMeta("retry", 200)); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
}
