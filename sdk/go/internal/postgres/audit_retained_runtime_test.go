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

func TestRetainedRuntime(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_RUNTIME_URL")
	if url == "" {
		t.Skip("v4 database required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	admin, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_RETAINED_RUNTIME_ADMIN"))
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	legacy := backfillPort(t, db, "ledger")
	port, e := NewRetainedRuntimeAudit(db, "ledger", "production", []string{"siem"}, legacy.validate)
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name, Operation, Base, Expected, Before string
			Patch                                   map[string]any
			Stable                                  bool
		}
		Loss map[string]any
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/retained-runtime.json")
	if e != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal(e)
	}
	var snapshot struct{ Snapshot string }
	raw, e = os.ReadFile("../../../../contracts/fixtures/retained-writes.json")
	if e != nil || json.Unmarshal(raw, &snapshot) != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	tenant := "tenant-1"
	if os.Getenv("HEIMDALL_TEST_RETAINED_RUNTIME_REJECT") == "1" {
		if _, e = port.Append(ctx, capacityWritesData(t).Ordinary); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(e)
		}
		f := capacityWritesData(t)
		if _, e = port.Recover(ctx, f.Terminal, f.Administration, 2001); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("HEIMDALL_TEST_RETAINED_RUNTIME_LOSS") == "1" {
		event := capacityWritesData(t).Ordinary
		for k, v := range fixture.Loss {
			event[k] = v
		}
		written, err := port.Append(ctx, event)
		if os.Getenv("HEIMDALL_TEST_RETAINED_RUNTIME_FAULT") == "1" {
			if !errors.Is(err, contract.ErrOutcomeUnknown) {
				t.Fatal(err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := port.ResolveReceipt(ctx, event)
		if err != nil || receipt == nil || !reflect.DeepEqual(written, *receipt) {
			t.Fatal(receipt, err)
		}
		return
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Before != "" {
				if _, e = admin.Exec(c.Before); e != nil {
					t.Fatal(e)
				}
			}
			var before, after string
			if e = admin.QueryRow(snapshot.Snapshot).Scan(&before); e != nil {
				t.Fatal(e)
			}
			f := capacityWritesData(t)
			event := map[string]map[string]any{"ordinary": f.Ordinary, "request": f.Request, "normal": f.Normal}[c.Base]
			for k, v := range c.Patch {
				event[k] = v
			}
			status := "receipt"
			var err error
			switch c.Operation {
			case "append":
				var receipt contract.AuditReceipt
				receipt, err = port.Append(ctx, event)
				if err == nil {
					resolved, x := port.ResolveReceipt(ctx, event)
					if x != nil || resolved == nil || !reflect.DeepEqual(receipt, *resolved) {
						t.Fatal(resolved, x)
					}
				}
			case "recover":
				_, err = port.Recover(ctx, f.Terminal, f.Administration, 2001)
			case "inspect":
				var found *RetainedEventInspection
				found, err = port.Inspect(ctx, &tenant, "ordinary")
				if found != nil {
					status = found.State
				}
			case "eligibility":
				status, err = port.Eligibility(ctx, &tenant, "ordinary", 3001, 1000)
			default:
				t.Fatal("operation")
			}
			if err != nil {
				status = err.Error()
			}
			if status != c.Expected {
				t.Fatal(status, c.Expected)
			}
			if e = admin.QueryRow(snapshot.Snapshot).Scan(&after); e != nil {
				t.Fatal(e)
			}
			if c.Stable && before != after {
				t.Fatal("unexpected storage change")
			}
		})
		if t.Failed() {
			t.FailNow()
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = port.Append(cancelled, capacityWritesData(t).Ordinary); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if _, e = db.Exec("DELETE FROM heimdall_retention.event_identities"); e == nil {
		t.Fatal("runtime DELETE permitted")
	}
	if _, e = db.Exec("UPDATE heimdall_retention.capacity SET row_limit=row_limit+1"); e == nil {
		t.Fatal("runtime policy update permitted")
	}
}
