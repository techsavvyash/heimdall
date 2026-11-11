package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
	"time"
)

func TestWorkspaceDecoder(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/managed-workspace-read.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name     string
		Row      workspaceRow
		Expected string
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	scope := contract.WorkspaceScope{ProductID: "ledger", Environment: "test", WorkspaceID: "acme"}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			r, e := decodeWorkspace(c.Row, scope)
			if c.Expected == "unavailable" {
				if !errors.Is(e, contract.ErrUnavailable) {
					t.Fatal(e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			got := "missing"
			if r != nil {
				got, e = contract.WorkspaceGate(&r.WorkspaceRecord, scope)
				if r.Revision != *c.Row.Revision || r.Name != *c.Row.Name {
					t.Fatal("record changed")
				}
			}
			if e != nil || got != c.Expected {
				t.Fatal(got, e)
			}
		})
	}
}
func TestWorkspaceInput(t *testing.T) {
	db, e := sql.Open("postgres", "")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = NewWorkspaceRecords(db, "ledger\n", "test"); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	s, e := NewWorkspaceRecords(db, "ledger", "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(context.Background(), "acme\n"); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.Get(ctx, "acme"); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if _, e = s.Get(nil, "acme"); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestWorkspacePrimary(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" {
		t.Skip("live workspace reads")
	}
	admin, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_managed_status")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e := NewWorkspaceRecords(db, "read-go", "test")
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if _, e = admin.Exec("GRANT SELECT ON heimdall_managed.workspaces TO heimdall_managed_status"); e != nil {
		t.Fatal(e)
	}
	if r, e := s.Get(ctx, "acme"); e != nil || r != nil {
		t.Fatal(r, e)
	}
	if _, e = admin.Exec("INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES('read-go','test','acme','Acme 🏢','active',false,0,0)"); e != nil {
		t.Fatal(e)
	}
	first, e := s.Get(ctx, "acme")
	if e != nil || first == nil || first.Name != "Acme 🏢" || first.MutationBlocked {
		t.Fatal(first, e)
	}
	for _, scope := range [][2]string{{"other", "test"}, {"read-go", "other"}} {
		other, _ := NewWorkspaceRecords(db, scope[0], scope[1])
		if r, e := other.Get(ctx, "acme"); e != nil || r != nil {
			t.Fatal(r, e)
		}
	}
	tx, e := admin.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE heimdall_managed.workspaces SET mutation_blocked=true,updated_at=1 WHERE product_id='read-go'"); e != nil {
		t.Fatal(e)
	}
	if r, e := s.Get(ctx, "acme"); e != nil || r == nil || r.MutationBlocked {
		t.Fatal(r, e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	blocked, e := s.Get(ctx, "acme")
	if e != nil || blocked == nil || !blocked.MutationBlocked || blocked.Revision == first.Revision {
		t.Fatal(blocked, e)
	}
	ro, e := sql.Open("postgres", url+"&options=-c%20default_transaction_read_only%3Don")
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	rs, _ := NewWorkspaceRecords(ro, "read-go", "test")
	if _, e = rs.Get(ctx, "absent"); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	// The TypeScript writer's row must decode identically through the Go adapter.
	peer, _ := NewWorkspaceRecords(db, "read-ts", "test")
	if r, e := peer.Get(ctx, "acme"); e != nil || r == nil || r.Name != "Acme 🏢" || !r.MutationBlocked {
		t.Fatal(r, e)
	}
	lock, e := admin.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback()
	if _, e = lock.Exec("LOCK TABLE heimdall_managed.workspaces IN ACCESS EXCLUSIVE MODE"); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = s.Get(deadline, "acme")
	cancel()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	lock.Rollback()
	if r, e := s.Get(ctx, "acme"); e != nil || r == nil || !r.MutationBlocked {
		t.Fatal(r, e)
	}

	db.Close()
	if _, e = s.Get(ctx, "acme"); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
}
