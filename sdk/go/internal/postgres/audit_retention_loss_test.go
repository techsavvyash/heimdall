package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestRetentionCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETENTION_LOSS_URL")
	if url == "" {
		t.Skip("prepared history and fault proxy required")
	}
	var f struct {
		ProductID, Environment, TenantID, Resolution string
		EventCount, DecisionCount                    int
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/retention-commit-loss.json")
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	connector, e := NewConnector(url)
	if e != nil {
		t.Fatal(e)
	}
	primary := sql.OpenDB(connector)
	defer primary.Close()
	fault := os.Getenv("HEIMDALL_TEST_RETENTION_FAULT_URL")
	writerURL := url
	if fault != "" {
		writerURL = fault
	}
	connector, e = NewConnector(writerURL)
	if e != nil {
		t.Fatal(e)
	}
	writer := sql.OpenDB(connector)
	defer writer.Close()
	a := backfillPort(t, writer, f.ProductID)
	a.environment = f.Environment
	var request, terminal string
	if e = primary.QueryRow("SELECT request_event_id,terminal_event_id FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2", f.ProductID, f.Environment).Scan(&request, &terminal); e != nil {
		t.Fatal(e)
	}
	_, e = a.RetainIdentityGroup(context.Background(), &f.TenantID, f.Resolution, []string{request, terminal})
	if fault != "" {
		if e != contract.ErrOutcomeUnknown {
			t.Fatal("expected outcome_unknown", e)
		}
	} else if e != nil {
		t.Fatal(e)
	}
	var events, decisions int
	if e = primary.QueryRow("SELECT (SELECT count(*) FROM heimdall_retention.event_identities),(SELECT count(*) FROM heimdall_retention.decision_identities)").Scan(&events, &decisions); e != nil || events != f.EventCount || decisions != f.DecisionCount {
		t.Fatal(events, decisions, e)
	}
}
