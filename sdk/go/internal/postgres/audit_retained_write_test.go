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

func TestRetainedWrites(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_WRITE_URL")
	if url == "" {
		t.Skip("prepared retention schema required")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	var f struct {
		Snapshot string
		Cases    []struct {
			Name, Base, Expected, Before string
			Patch                        map[string]any
			Stable                       bool
			Checks                       []string
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/retained-writes.json")
	if e != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture", e)
	}
	a := backfillPort(t, db, "ledger")
	a.environment = "staging"
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Before != "" {
				if _, e = db.Exec(c.Before); e != nil {
					t.Fatal(e)
				}
			}
			var before, after string
			if e = db.QueryRow(f.Snapshot).Scan(&before); e != nil {
				t.Fatal(e)
			}
			bases := capacityWritesData(t)
			event := map[string]map[string]any{"ordinary": bases.Ordinary, "request": bases.Request, "normal": bases.Normal}[c.Base]
			for k, v := range c.Patch {
				event[k] = v
			}
			input, _ := json.Marshal(event)
			receipt, err := a.AppendRetained(context.Background(), event)
			status := "receipt"
			if err != nil {
				status = err.Error()
			}
			if status != c.Expected {
				t.Fatal(status, err)
			}
			if err == nil {
				digest, _ := contract.AuditDigest(event)
				if receipt.PayloadDigest != digest || receipt.EventID != event["eventId"] {
					t.Fatal(receipt)
				}
			}
			unchanged, _ := json.Marshal(event)
			if string(input) != string(unchanged) {
				t.Fatal("input mutated")
			}
			if e = db.QueryRow(f.Snapshot).Scan(&after); e != nil {
				t.Fatal(e)
			}
			if c.Stable && before != after {
				t.Fatal("failed or repeated write mutated storage")
			}
			for _, check := range c.Checks {
				var ok bool
				if e = db.QueryRow(check).Scan(&ok); e != nil || !ok {
					t.Fatal(check, e)
				}
			}
		})
		if t.Failed() {
			t.FailNow()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	event := capacityWritesData(t).Ordinary
	event["environment"] = "staging"
	if _, err := a.AppendRetained(ctx, event); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}

}
