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

func TestRetainedRecovery(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_RECOVERY_URL")
	if url == "" {
		t.Skip("prepared pending history required")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	var f struct {
		Snapshot string
		Cutoff   int64
		Cases    []struct {
			Name, Expected, Before string
			Cutoff                 *int64
			AdminPatch             map[string]any
			Stable, Normal         bool
			Checks                 []string
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/retained-recovery-writes.json")
	if e != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture", e)
	}
	a := backfillPort(t, db, "ledger")
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
			for k, v := range c.AdminPatch {
				bases.Administration[k] = v
			}
			input, _ := json.Marshal([]map[string]any{bases.Terminal, bases.Administration})
			cutoff := f.Cutoff
			if c.Cutoff != nil {
				cutoff = *c.Cutoff
			}
			var receipt *AuditRecoveryReceipt
			var err error
			if c.Normal {
				_, err = a.AppendRetained(context.Background(), bases.Normal)
			} else {
				receipt, err = a.RecoverRetained(context.Background(), bases.Terminal, bases.Administration, cutoff)
			}
			status := "receipt"
			if err != nil {
				status = err.Error()
			}
			if status != c.Expected {
				t.Fatal(status, err)
			}
			if receipt != nil {
				td, _ := contract.AuditDigest(bases.Terminal)
				ad, _ := contract.AuditDigest(bases.Administration)
				if receipt.Terminal.PayloadDigest != td || receipt.Administration.PayloadDigest != ad {
					t.Fatal(receipt)
				}
			}
			unchanged, _ := json.Marshal([]map[string]any{bases.Terminal, bases.Administration})
			if string(input) != string(unchanged) {
				t.Fatal("input mutated")
			}
			if e = db.QueryRow(f.Snapshot).Scan(&after); e != nil {
				t.Fatal(e)
			}
			if c.Stable && before != after {
				t.Fatal("retry or failure mutated storage")
			}
			for _, q := range c.Checks {
				var ok bool
				if e = db.QueryRow(q).Scan(&ok); e != nil || !ok {
					t.Fatal(q, e)
				}
			}
		})
		if t.Failed() {
			t.FailNow()
		}
	}
	bases := capacityWritesData(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.RecoverRetained(ctx, bases.Terminal, bases.Administration, f.Cutoff); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestRetainedRecoveryLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_RECOVERY_LOSS_URL")
	if url == "" {
		t.Skip("fault history required")
	}
	writerURL := url
	fault := os.Getenv("HEIMDALL_TEST_RETAINED_RECOVERY_FAULT_URL")
	if fault != "" {
		writerURL = fault
	}
	connector, e := NewConnector(writerURL)
	if e != nil {
		t.Fatal(e)
	}
	writer := sql.OpenDB(connector)
	defer writer.Close()
	bases := capacityWritesData(t)
	receipt, e := backfillPort(t, writer, "ledger").RecoverRetained(context.Background(), bases.Terminal, bases.Administration, 1001)
	if fault != "" {
		if !errors.Is(e, contract.ErrOutcomeUnknown) {
			t.Fatal(e)
		}
	} else if e != nil || receipt == nil {
		t.Fatal(receipt, e)
	}
	connector, e = NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	primary := sql.OpenDB(connector)
	defer primary.Close()
	var ok bool
	e = primary.QueryRow("SELECT d.resolution='recovered' AND (SELECT count(*) FROM heimdall_retention.event_identities i WHERE i.product_id=d.product_id AND i.environment=d.environment AND i.decision_id=d.decision_id)=3 FROM heimdall_retention.decision_identities d WHERE d.product_id='ledger' AND d.environment='production' AND d.decision_id='decision-1'").Scan(&ok)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
}
