package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestRetainedRetry(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_RETRY_URL")
	if url == "" {
		t.Skip("retired history required")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	var cases []struct {
		Name, ProductID, Environment, Base, Expected string
		Patch                                        map[string]any
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/retained-retry.json")
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixture")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			f := capacityWritesData(t)
			event := map[string]map[string]any{"request": f.Request, "normal": f.Normal, "terminal": f.Terminal, "administration": f.Administration, "ordinary": f.Ordinary}[c.Base]
			event["productId"] = c.ProductID
			event["environment"] = c.Environment
			if c.Base == "administration" {
				event["eventId"] = contract.RecoveryEventID(c.ProductID, c.Environment, f.Terminal["eventId"].(string))
			}
			for k, v := range c.Patch {
				event[k] = v
			}
			before, _ := json.Marshal(event)
			a := backfillPort(t, db, c.ProductID)
			a.environment = c.Environment
			receipt, e := a.ResolveRetainedReceipt(context.Background(), event)
			status := "absent"
			if e != nil {
				status = e.Error()
			} else if receipt != nil {
				status = "receipt"
			}
			if status != c.Expected {
				t.Fatal(status, e)
			}
			if receipt != nil {
				digest, e := contract.AuditDigest(event)
				expected := contract.AuditReceipt{ProductID: c.ProductID, Environment: c.Environment, EventID: event["eventId"].(string), RecordedAt: int64(event["recordedAt"].(float64)), PayloadDigest: digest}
				if e != nil || *receipt != expected {
					t.Fatal(receipt, e)
				}
			}
			if c.Expected != "absent" {
				appended, err := a.AppendRetained(context.Background(), event)
				outcome := "receipt"
				if err != nil {
					outcome = err.Error()
				}
				if outcome != c.Expected || (receipt != nil && appended != *receipt) {
					t.Fatal("transactional retry", appended, err)
				}
			}
			after, _ := json.Marshal(event)
			if string(after) != string(before) {
				t.Fatal("input mutated")
			}
		})
	}
}
