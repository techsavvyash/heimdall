package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestRetainedOperationsAdmission(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_MANAGED_OPERATIONS_V4") != "1" {
		t.Skip("activated managed v4")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	runtime, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_managed_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.Close()
	f := journalData(t)
	b := f.Binding
	b.ProductID = "admission-go"
	store, e := NewRetainedOperationRecords(runtime, b, nil)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/managed-retained-runtime.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct{ Name, Change, Restore string }
	}
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture")
	}
	ctx := context.Background()
	reject := func(t *testing.T, s *OperationRecords) {
		t.Helper()
		workspace, err := NewRetainedWorkspaceRecords(s.db, b.ProductID, b.Environment)
		if err != nil {
			t.Fatal(err)
		}
		candidates, err := NewRetainedCandidateRecords(s.db, b.ProductID, b.Environment)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.Get(ctx, "absent"); !errors.Is(err, contract.ErrUnavailable) {
			t.Fatal("workspace", err)
		}
		if _, err := candidates.Scan(ctx, f.CreateIntent.Actor, "0", 10); !errors.Is(err, contract.ErrUnavailable) {
			t.Fatal("candidates", err)
		}
		if _, e := s.Get(ctx, "absent", f.CreateIntent.Actor); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal("get", e)
		}
		if _, e := s.Scan(ctx, "recovery", nil, 300, 10); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal("scan", e)
		}
		if _, e := s.Create(ctx, createIntent(f, b.ProductID), "key", createAllocation(f, "admission")); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal("create", e)
		}
		if _, e := s.RepairCandidates(ctx, "absent", "1", repairData(t).Observations, repairMeta("repair", 200)); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal("repair", e)
		}
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if _, e := db.Exec(c.Change); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if _, e := db.Exec(c.Restore); e != nil {
					t.Fatal(e)
				}
			}()
			reject(t, store)
		})
	}
	readonly, e := sql.Open("postgres", url+"&options=-c%20default_transaction_read_only%3Don")
	if e != nil {
		t.Fatal(e)
	}
	defer readonly.Close()
	ro, e := NewRetainedOperationRecords(readonly, b, nil)
	if e != nil {
		t.Fatal(e)
	}
	reject(t, ro)
	if r, e := store.Get(ctx, "absent", f.CreateIntent.Actor); e != nil || r != nil {
		t.Fatal(r, e)
	}
	if p, e := store.Scan(ctx, "recovery", nil, 300, 10); e != nil || len(p.Items) != 0 {
		t.Fatal(p, e)
	}
	var count int
	if e := db.QueryRow("SELECT count(*) FROM heimdall_managed.operations WHERE product_id=$1", b.ProductID).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
}
