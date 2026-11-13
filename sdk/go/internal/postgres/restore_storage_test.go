package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestRestoredStorage(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RESTORED_STORAGE_URL")
	if url == "" {
		t.Skip("restored storage required")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	audit := func(product, environment string) *RetainedRuntimeAudit {
		port, err := NewRetainedRuntimeAudit(db, product, environment, []string{"siem"}, backfillPort(t, db, product).validate)
		if err != nil {
			t.Fatal(err)
		}
		return port
	}
	pending, err := audit("ledger", "production").ReadResolution(ctx, "decision-1")
	if err != nil || pending == nil || pending.State != "pending" || pending.Terminal != nil {
		t.Fatal("restored pending decision", err)
	}
	tenant := "tenant-1"
	retired, err := audit("ledger", "staging").Inspect(ctx, &tenant, "z-request")
	if err != nil || retired == nil || retired.State != "retired" || retired.Event != nil {
		t.Fatal("restored retired identity", err)
	}
	var original struct{ Request map[string]any }
	raw, err := os.ReadFile("../../../../contracts/fixtures/audit-capacity-writes.json")
	if err != nil || json.Unmarshal(raw, &original) != nil {
		t.Fatal("original audit fixture", err)
	}
	original.Request["environment"] = "staging"
	receipt, err := audit("ledger", "staging").ResolveReceipt(ctx, original.Request)
	if err != nil || receipt == nil || receipt.PayloadDigest != retired.Identity.Digest {
		t.Fatal("restored retired receipt", err)
	}

	held, err := audit("holds-go", "production").Eligibility(ctx, &tenant, "ordinary", 100000, 1000)
	if err != nil || held != "retain_retry" {
		t.Fatal("restored hold", held, err)
	}
	f := journalData(t)
	binding := f.Binding
	binding.ProductID = "transition-go"
	store, err := NewRetainedOperationRecords(db, binding, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.Get(ctx, "safe-retry", f.CreateIntent.Actor)
	if err != nil || op == nil || op.State != "uncertain" {
		t.Fatal("restored uncertain operation", err)
	}
	workspaces, err := NewRetainedWorkspaceRecords(db, binding.ProductID, binding.Environment)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaces.Get(ctx, op.WorkspaceID)
	if err != nil || workspace == nil || !workspace.MutationBlocked {
		t.Fatal("restored workspace lost its mutation gate", err)
	}
	if _, err = audit("ledger", "staging").ReadResolution(ctx, "decision-1"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal("retired resolution remained available", err)
	}
}
