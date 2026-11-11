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

func TestRetentionEligibility(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETENTION_ELIGIBILITY_URL")
	if url == "" {
		t.Skip("prepared retention history required")
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
			Name, ProductID, Environment, EventID, Expected, Error, Before, After string
			TenantID                                                              *string
			Now, RetainForMS                                                      int64
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/retention-eligibility-read.json")
	if e != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture", e)
	}
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
			a := backfillPort(t, db, c.ProductID)
			a.environment = c.Environment
			result, err := a.InspectRetentionEligibility(context.Background(), c.TenantID, c.EventID, c.Now, c.RetainForMS)
			if c.Error != "" {
				if err == nil || err.Error() != c.Error {
					t.Fatal(result, err)
				}
			} else {
				if result == "" {
					result = "absent"
				}
				if err != nil || result != c.Expected {
					t.Fatal(result, err)
				}
			}
			if e = db.QueryRow(f.Snapshot).Scan(&after); e != nil || before != after {
				t.Fatal("inspection mutated storage", e)
			}
			if c.After != "" {
				if _, e = db.Exec(c.After); e != nil {
					t.Fatal(e)
				}
			}
		})
		if t.Failed() {
			t.FailNow()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = backfillPort(t, db, "ledger").InspectRetentionEligibility(ctx, nil, "ordinary", 3001, 1000); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
