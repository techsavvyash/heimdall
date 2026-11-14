package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"testing"
)

func TestRetainedWriteLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_WRITE_LOSS_URL")
	if url == "" {
		t.Skip("prepared history and COMMIT proxy required")
	}
	var cases []struct {
		Name, Base string
		Patch      map[string]any
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/retained-write-loss.json")
	if e != nil || json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixture", e)
	}
	bases := capacityWritesData(t)
	var event map[string]any
	for _, c := range cases {
		if c.Name == os.Getenv("HEIMDALL_TEST_RETAINED_WRITE_CASE") {
			event = map[string]map[string]any{"ordinary": bases.Ordinary, "request": bases.Request, "normal": bases.Normal}[c.Base]
			for k, v := range c.Patch {
				event[k] = v
			}
		}
	}
	if event == nil {
		t.Fatal("unknown case")
	}
	digest, e := contract.AuditDigest(event)
	if e != nil {
		t.Fatal(e)
	}
	expected := contract.AuditReceipt{ProductID: "ledger", Environment: "production", EventID: event["eventId"].(string), RecordedAt: int64(event["recordedAt"].(float64)), PayloadDigest: digest}
	fault := os.Getenv("HEIMDALL_TEST_RETAINED_WRITE_FAULT_URL")
	writerURL := url
	if fault != "" {
		writerURL = fault
	}
	connector, e := NewConnector(writerURL)
	if e != nil {
		t.Fatal(e)
	}
	writer := sql.OpenDB(connector)
	defer writer.Close()
	a := backfillPort(t, writer, "ledger")
	if fault == "" {
		a.sinks = []string{"changed-sink"}
	}
	receipt, e := a.AppendRetained(context.Background(), event)
	if fault != "" {
		if !errors.Is(e, contract.ErrOutcomeUnknown) {
			t.Fatal(e)
		}
	} else if e != nil || receipt != expected {
		t.Fatal(receipt, e)
	}
	connector, e = NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	primary := sql.OpenDB(connector)
	defer primary.Close()
	reader := backfillPort(t, primary, "ledger")
	resolved, e := reader.ResolveRetainedReceipt(context.Background(), event)
	if e != nil || resolved == nil || *resolved != expected {
		t.Fatal(resolved, e)
	}
	tenant := "tenant-1"
	inspection, e := reader.InspectRetainedEvent(context.Background(), &tenant, expected.EventID)
	if e != nil || inspection == nil || inspection.State != "live" || !reflect.DeepEqual(inspection.Identity.Routing, []string{"siem"}) {
		t.Fatal(inspection, e)
	}
}
