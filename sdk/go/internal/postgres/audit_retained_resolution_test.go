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

func TestRetainedResolution(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_RESOLUTION_URL")
	if url == "" {
		t.Skip("retained history required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	type scenario struct{ Name, Product, Environment, ID, Before, Expected string }
	var f struct{ Live, Retired []scenario }
	raw, e := os.ReadFile("../../../../contracts/fixtures/retained-resolution.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	cases := f.Live
	retired := os.Getenv("HEIMDALL_TEST_RETAINED_RESOLUTION_RETIRED") == "1"
	if retired {
		cases = f.Retired
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if _, e := db.Exec("BEGIN"); e != nil {
				t.Fatal(e)
			}
			defer db.Exec("ROLLBACK")
			if c.Before != "" {
				if _, e := db.Exec(c.Before); e != nil {
					t.Fatal(e)
				}
			}
			a, e := NewAudit(db, c.Product, c.Environment, []string{"siem"}, backfillPort(t, db, c.Product).validate)
			if e != nil {
				t.Fatal(e)
			}
			a.retainedRuntime = !retired || os.Getenv("HEIMDALL_TEST_RETAINED_RESOLUTION_V4") == "1"
			result, e := a.ReadRetainedResolution(context.Background(), c.ID)
			status := "absent"
			if e != nil {
				status = e.Error()
			} else if result != nil {
				status = result.State
				if result.DecisionID != c.ID || result.Request["eventId"] != "z-request" {
					t.Fatal(result)
				}
				if status != "pending" && result.Terminal == nil {
					t.Fatal("missing terminal")
				}
				if status == "recovered" && result.Administration == nil {
					t.Fatal("missing recovery")
				}
			}
			if status != c.Expected {
				t.Fatal(status, c.Expected)
			}
		})
	}
	a := backfillPort(t, db, "ledger")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := a.ReadRetainedResolution(ctx, "decision-1"); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
