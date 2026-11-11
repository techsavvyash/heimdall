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

func TestRuntimeRecovery(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RUNTIME_RECOVERY_URL")
	if url == "" {
		t.Skip("activated mixed-language history required")
	}
	admin, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dpublic_capacity_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	f := capacityWritesData(t)
	a := backfillPort(t, db, "ledger")
	var used, reserved int64
	if e = admin.QueryRow("SELECT used_events,reserved_events FROM heimdall_capacity.policy").Scan(&used, &reserved); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)"); e != nil {
		t.Fatal(e)
	}
	page, e := a.ScanPendingDecisions(ctx, f.Cutoff, nil, 1)
	if e != nil || len(page.Items) != 1 {
		t.Fatal(page, e)
	}
	proposal, e := contract.BuildRecoveryProposal(page.Items[0].Request, contract.RecoveryOptions{TerminalID: f.Terminal["eventId"].(string), RequestID: f.Administration["requestId"].(string), SDKVersion: "0.0.0", Actor: f.Administration["actor"].(map[string]any), At: int64(f.Terminal["recordedAt"].(float64)), Cutoff: f.Cutoff})
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := json.Marshal(proposal)
	if e != nil {
		t.Fatal(e)
	}
	proposal, e = contract.DecodeRecoveryProposal(page.Items[0].Request, encoded)
	if e != nil {
		t.Fatal(e)
	}
	f.Terminal, f.Administration = proposal.Terminal, proposal.Administration
	for _, state := range []string{"migrating", "ready"} {
		if _, e = admin.Exec("UPDATE heimdall.schema_metadata SET database_version=2,state=$1", state); e != nil {
			t.Fatal(e)
		}
		if _, e = a.RecoverRuntime(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(e)
		}
	}
	if _, e = admin.Exec("UPDATE heimdall.schema_metadata SET database_version=3,state='ready'"); e != nil {
		t.Fatal(e)
	}
	if loss := os.Getenv("HEIMDALL_TEST_RUNTIME_RECOVERY_LOSS"); loss != "" {
		fault, e := sql.Open("postgres", loss)
		if e != nil {
			t.Fatal(e)
		}
		defer fault.Close()
		if _, e = backfillPort(t, fault, "ledger").RecoverRuntime(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrOutcomeUnknown) {
			t.Fatal(e)
		}
	}
	receipt, e := a.RecoverRuntime(ctx, f.Terminal, f.Administration, f.Cutoff)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := a.RecoverRuntime(ctx, f.Terminal, f.Administration, f.Cutoff)
	if e != nil || *retry != *receipt {
		t.Fatal(retry, e)
	}
	if _, e = a.AppendRuntime(ctx, f.Normal); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	var afterUsed, afterReserved int64
	if e = admin.QueryRow("SELECT used_events,reserved_events FROM heimdall_capacity.policy").Scan(&afterUsed, &afterReserved); e != nil || afterUsed != used+2 || afterReserved != reserved-2 {
		t.Fatal(afterUsed, afterReserved, e)
	}
	page, e = a.ScanPendingDecisions(ctx, f.Cutoff, nil, 1)
	if e != nil || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
	if _, e = admin.Exec("UPDATE heimdall.schema_metadata SET state='migrating'"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.RecoverRuntime(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
}
