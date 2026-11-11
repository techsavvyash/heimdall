package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type candidateFixture struct {
	Scope                   struct{ ProductID, Environment string }
	Actor                   contract.Identity
	Principal, IdentityHash string
	Cases                   []struct {
		Name         string
		Value        candidatePageRow
		Valid        bool
		After        string
		Limit        int
		Positions    []string
		NextPosition *string
	}
}

func candidateData(t *testing.T) candidateFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/candidate-page.json")
	if e != nil {
		t.Fatal(e)
	}
	var f candidateFixture
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func TestCandidateDecoder(t *testing.T) {
	f := candidateData(t)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			page, e := decodeCandidatePage(c.Value, f.Scope.ProductID, f.Scope.Environment, f.Principal, f.IdentityHash, c.After, c.Limit)
			if !c.Valid {
				if !errors.Is(e, contract.ErrUnavailable) {
					t.Fatal(page, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			positions := make([]string, 0, len(page.Items))
			for _, item := range page.Items {
				positions = append(positions, item.Position)
			}
			if !reflect.DeepEqual(positions, c.Positions) || !reflect.DeepEqual(page.NextPosition, c.NextPosition) {
				t.Fatal(page, c.Positions, c.NextPosition)
			}
		})
	}
}
func TestCandidateInput(t *testing.T) {
	f := candidateData(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	s, e := NewCandidateRecords(db, "ledger", "production")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = NewCandidateRecords(db, "bad/path", "production"); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	for _, after := range []string{"-1", "01", "1\n", "9223372036854775808"} {
		if _, e = s.Scan(context.Background(), f.Actor, after, 20); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, e = s.Scan(context.Background(), f.Actor, "0", limit); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.Scan(ctx, f.Actor, "0", 20); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	principal, e := contract.EncodeIdentity(f.Actor.Issuer, f.Actor.Subject)
	hash, he := identityBinding(f.Actor.Issuer, f.Actor.Subject)
	if e != nil || he != nil || principal != f.Principal || hex.EncodeToString(hash) != f.IdentityHash {
		t.Fatal("identity fixture")
	}
}
func TestCandidatePrimary(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if dsn == "" {
		t.Skip("managed database")
	}
	admin, _ := sql.Open("postgres", dsn)
	defer admin.Close()
	db, _ := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_managed_runtime")
	defer db.Close()
	f := candidateData(t)
	ctx := context.Background()
	s, _ := NewCandidateRecords(db, "scan-go", "production")
	if _, e := admin.Exec(`INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at)
  SELECT 'scan-go','production','w-'||lpad(n::text,3,'0'),'Same name',CASE WHEN n=102 THEN 'pending' WHEN n=103 THEN 'suspended' WHEN n=104 THEN 'tombstoned' ELSE 'active' END,n=105,100,100 FROM generate_series(1,105) n`); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(`INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
  SELECT product_id,environment,workspace_id,decode($1,'hex'),$2,100 FROM heimdall_managed.workspaces WHERE product_id='scan-go' ORDER BY workspace_id`, f.IdentityHash, f.Principal); e != nil {
		t.Fatal(e)
	}
	first, e := s.Scan(ctx, f.Actor, "0", 100)
	if e != nil || len(first.Items) != 100 || first.NextPosition == nil || *first.NextPosition != first.Items[99].Position {
		t.Fatal(first, e)
	}
	for index, item := range first.Items {
		suffix := strconv.Itoa(index + 1)
		for len(suffix) < 3 {
			suffix = "0" + suffix
		}
		if item.Workspace.WorkspaceID != "w-"+suffix {
			t.Fatal(index, item)
		}
	}
	last, e := s.Scan(ctx, f.Actor, *first.NextPosition, 100)
	if e != nil || len(last.Items) != 1 || last.Items[0].Workspace.WorkspaceID != "w-101" || last.NextPosition != nil {
		t.Fatal(last, e)
	}
	for _, scope := range [][2]string{{"other", "production"}, {"scan-go", "staging"}} {
		other, _ := NewCandidateRecords(db, scope[0], scope[1])
		page, err := other.Scan(ctx, f.Actor, "0", 100)
		if err != nil || len(page.Items) != 0 {
			t.Fatal(page, err)
		}
	}
	wrong := f.Actor
	wrong.Subject = "other"
	page, e := s.Scan(ctx, wrong, "0", 100)
	if e != nil || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
	page, e = s.Scan(ctx, f.Actor, first.Items[0].Position, 1)
	if e != nil || page.Items[0].Workspace.WorkspaceID != "w-002" {
		t.Fatal(page, e)
	}
	if _, e = admin.Exec("UPDATE heimdall_managed.workspaces SET mutation_blocked=true WHERE product_id='scan-go' AND workspace_id='w-002'"); e != nil {
		t.Fatal(e)
	}
	page, e = s.Scan(ctx, f.Actor, first.Items[0].Position, 1)
	if e != nil || page.Items[0].Workspace.WorkspaceID != "w-003" {
		t.Fatal(page, e)
	}
	if _, e = admin.Exec("UPDATE heimdall_managed.workspaces SET mutation_blocked=false WHERE product_id='scan-go' AND workspace_id='w-002'"); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall_managed.membership_candidates SET observed_at=101 WHERE product_id='scan-go' AND workspace_id='w-001'"); e != nil {
		t.Fatal(e)
	}
	page, e = s.Scan(ctx, f.Actor, "0", 1)
	if e != nil || page.Items[0].Position != first.Items[0].Position || page.Items[0].Revision == first.Items[0].Revision {
		t.Fatal(page, e)
	}
	if _, e = admin.Exec("INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES('scan-bad-go','production','bad','Bad','active',false,100,100)"); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES('scan-bad-go','production','bad',decode($1,'hex'),'user:wrong',100)", f.IdentityHash); e != nil {
		t.Fatal(e)
	}
	bad, _ := NewCandidateRecords(db, "scan-bad-go", "production")
	if _, e = bad.Scan(ctx, f.Actor, "0", 20); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	readonly, _ := sql.Open("postgres", dsn+"&options=-c%20default_transaction_read_only%3Don")
	defer readonly.Close()
	ro, _ := NewCandidateRecords(readonly, "missing", "production")
	if _, e = ro.Scan(ctx, f.Actor, "0", 20); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	blocker, e := admin.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.Exec("LOCK TABLE heimdall_managed.membership_candidates IN ACCESS EXCLUSIVE MODE"); e != nil {
		blocker.Rollback()
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = s.Scan(deadline, f.Actor, "0", 20)
	cancel()
	blocker.Rollback()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if page, e = s.Scan(ctx, f.Actor, "0", 1); e != nil || len(page.Items) != 1 {
		t.Fatal(page, e)
	}
	peer, _ := NewCandidateRecords(db, "scan-ts", "production")
	page, e = peer.Scan(ctx, f.Actor, "0", 100)
	if e != nil || len(page.Items) != 100 {
		t.Fatal(page, e)
	}
	final, e := peer.Scan(ctx, f.Actor, *page.NextPosition, 100)
	if e != nil || len(final.Items) != 1 || final.Items[0].Workspace.WorkspaceID != "w-101" {
		t.Fatal(final, e)
	}
	journal := journalData(t)
	completed, _ := NewCandidateRecords(db, "transition-ts", "production")
	page, e = completed.Scan(ctx, journal.CreateIntent.Actor, "0", 100)
	if e != nil || len(page.Items) != 1 || page.Items[0].Workspace.WorkspaceID != "complete" {
		t.Fatal(page, e)
	}
}
