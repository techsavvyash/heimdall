package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestRetainedRetirement(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETIRE_URL")
	if url == "" {
		t.Skip("retirement database required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	admin, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_RETIRE_ADMIN"))
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	var f struct {
		Snapshot string
		Cases    []struct {
			Name           string
			Request        contract.AuditRetirementRequest
			Administration map[string]any
			Expected       string
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-retire-storage.json")
	if e != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("invalid fixture", e)
	}
	for _, c := range f.Cases {
		if c.Name != os.Getenv("HEIMDALL_TEST_RETIRE_CASE") {
			continue
		}
		r := c.Request
		legacy := backfillPort(t, db, r.ProductID)
		port, e := NewRetainedRuntimeAudit(db, r.ProductID, r.Environment, []string{"siem"}, legacy.validate)
		if e != nil {
			t.Fatal(e)
		}
		ctx := context.Background()
		snapshot := func() string {
			var s string
			if e := admin.QueryRow(f.Snapshot).Scan(&s); e != nil {
				t.Fatal(e)
			}
			return s
		}
		before := snapshot()
		expected := os.Getenv("HEIMDALL_TEST_RETIRE_EXPECT")
		if expected == "" {
			expected = c.Expected
		}
		if expected != "receipt" {
			_, err := port.Retire(ctx, r, c.Administration)
			if err == nil || err.Error() != expected {
				t.Fatalf("expected %s, got %v", expected, err)
			}
			if expected != "outcome_unknown" && snapshot() != before {
				t.Fatal("failed retirement changed storage")
			}
			return
		}
		original, e := port.Inspect(ctx, r.TenantID, r.EventID)
		if e != nil {
			t.Fatal(e)
		}
		receipt, e := port.Retire(ctx, r, c.Administration)
		if e != nil {
			t.Fatal(e)
		}
		saved, e := port.ResolveReceipt(ctx, c.Administration)
		if e != nil || saved == nil || !reflect.DeepEqual(*saved, receipt) {
			t.Fatal(saved, e)
		}
		retired, e := port.Inspect(ctx, r.TenantID, r.EventID)
		if e != nil || retired == nil || retired.State != "retired" {
			t.Fatal(retired, e)
		}
		if original != nil && original.Event != nil {
			saved, e = port.ResolveReceipt(ctx, original.Event)
			if e != nil || saved == nil || saved.PayloadDigest != original.Identity.Digest {
				t.Fatal(saved, e)
			}
		}
		if os.Getenv("HEIMDALL_TEST_RETIRE_RETRY") == "1" && snapshot() != before {
			t.Fatal("retry changed storage")
		}
		after := snapshot()
		c.Administration["actor"] = map[string]any{"kind": "system", "id": "other"}
		if _, e = port.Retire(ctx, r, c.Administration); e != contract.ErrConflict || snapshot() != after {
			t.Fatal("changed retry", e)
		}
		return
	}
	t.Fatal("missing retirement case")
}
