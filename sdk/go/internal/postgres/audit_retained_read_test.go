package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestRetainedInspection(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_READ_URL")
	if url == "" {
		t.Skip("retired history required")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	var fixture struct {
		ProductID, Environment, ConflictingMarkerSQL string
		Cases                                        []struct{ Name, TenantID, EventID, State string }
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/retained-inspection.json")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture")
	}
	a := backfillPort(t, db, fixture.ProductID)
	a.environment = fixture.Environment
	ctx := context.Background()
	tenant := "tenant-1"
	for _, c := range fixture.Cases {
		result, e := a.InspectRetainedEvent(ctx, &c.TenantID, c.EventID)
		if e != nil {
			t.Fatal(c.Name, e)
		}
		state := "absent"
		if result != nil {
			state = result.State
			if result.Identity.EventID != c.EventID || (result.Event != nil) != (c.State == "live") {
				t.Fatal(c.Name, "metadata")
			}
		}
		if state != c.State {
			t.Fatal(c.Name, state)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = a.InspectRetainedEvent(cancelled, &tenant, "ordinary"); e != contract.ErrCancelled {
		t.Fatal(e)
	}
	exec := func(q string) {
		if _, e := db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	exec("DELETE FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'; DELETE FROM heimdall_capacity.event_charges WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'; DELETE FROM heimdall.audit_events WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'")
	b := *a
	b.environment = "production"
	if _, e = b.InspectRetainedEvent(ctx, &tenant, "ordinary"); e != contract.ErrAuditUnavailable {
		t.Fatal(e)
	}
	exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id='ledger' AND environment='staging' AND event_id='normal-terminal'")
	if _, e = a.InspectRetainedEvent(ctx, &tenant, "normal-terminal"); e != contract.ErrAuditUnavailable {
		t.Fatal(e)
	}
	exec(fixture.ConflictingMarkerSQL)
	if _, e = a.InspectRetainedEvent(ctx, &tenant, "ordinary"); e != contract.ErrAuditUnavailable {
		t.Fatal("conflicting retirement marker", e)
	}
	exec("UPDATE heimdall.schema_metadata SET state='migrating'")
	if _, e = a.InspectRetainedEvent(ctx, &tenant, "ordinary"); e != contract.ErrUnavailable {
		t.Fatal(e)
	}
}
