package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

type retiredDecisionFixture struct {
	TenantID, AdministrationID string
	RetiredAt, RetainForMS     int64
	Scopes                     []struct {
		ProductID, Environment, Resolution string
		Members                            int
	}
}

func retiredDecisionData(t *testing.T) retiredDecisionFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/retired-decision-inspection.json")
	if e != nil {
		t.Fatal(e)
	}
	var f retiredDecisionFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func TestRetirementReferences(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETIREMENT_REFERENCE_URL")
	if url == "" {
		t.Skip("prepared decision history required")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	f := retiredDecisionData(t)
	for _, scope := range f.Scopes {
		event := capacityWritesData(t).Ordinary
		event["productId"] = scope.ProductID
		event["environment"] = scope.Environment
		event["eventId"] = f.AdministrationID
		event["occurredAt"] = float64(f.RetiredAt)
		event["recordedAt"] = float64(f.RetiredAt)
		a := backfillPort(t, db, scope.ProductID)
		a.environment = scope.Environment
		if _, e = a.AppendRuntime(context.Background(), event); e != nil {
			t.Fatal(e)
		}
		if _, e = a.RetainIdentityGroup(context.Background(), &f.TenantID, "ordinary", []string{f.AdministrationID}); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRetiredDecisionInspection(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETIRED_DECISION_URL")
	if url == "" {
		t.Skip("retired decision history required")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	f := retiredDecisionData(t)
	for _, scope := range f.Scopes {
		var ids pq.StringArray
		var resolution string
		if e = db.QueryRow("SELECT array_remove(ARRAY[request_event_id,terminal_event_id,recovery_event_id],NULL),resolution FROM heimdall_retention.decision_identities WHERE product_id=$1 AND environment=$2", scope.ProductID, scope.Environment).Scan(&ids, &resolution); e != nil {
			t.Fatal(e)
		}
		if len(ids) != scope.Members || resolution != scope.Resolution {
			t.Fatal(ids, resolution)
		}
		a := backfillPort(t, db, scope.ProductID)
		a.environment = scope.Environment
		for _, id := range ids {
			result, e := a.InspectRetainedEvent(context.Background(), &f.TenantID, id)
			if e != nil || result == nil || result.State != "retired" || result.Event != nil || result.Identity.EventID != id || result.Identity.DecisionID == nil {
				t.Fatal(id, result, e)
			}
		}
		if _, err := a.InspectRetentionEligibility(context.Background(), &f.TenantID, "z-request", 4001, 1000); !errors.Is(err, contract.ErrNotFound) {
			t.Fatal(err)
		}
		if scope.Resolution == "recovered" {
			bases := capacityWritesData(t)
			for _, event := range []map[string]any{bases.Terminal, bases.Administration} {
				event["productId"] = scope.ProductID
				event["environment"] = scope.Environment
			}
			bases.Administration["eventId"] = contract.RecoveryEventID(scope.ProductID, scope.Environment, bases.Terminal["eventId"].(string))
			receipt, err := a.RecoverRetained(context.Background(), bases.Terminal, bases.Administration, 1001)
			if err != nil || receipt == nil {
				t.Fatal(receipt, err)
			}
			var count int
			if err = db.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=ANY($3)", scope.ProductID, scope.Environment, pq.Array(ids)).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		}
		admin, e := a.InspectRetainedEvent(context.Background(), &f.TenantID, f.AdministrationID)
		if e != nil || admin == nil || admin.State != "live" || admin.Event == nil {
			t.Fatal(admin, e)
		}
	}
}
