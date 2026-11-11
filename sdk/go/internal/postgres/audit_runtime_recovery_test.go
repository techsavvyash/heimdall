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
	decisionID := page.Items[0].Request["decisionId"].(string)
	rawHistory, err := os.ReadFile("../../../../contracts/fixtures/audit-capacity-backfill.json")
	if err != nil {
		t.Fatal(err)
	}
	var history struct {
		Scopes []struct {
			ProductID   string `json:"productId"`
			Environment string `json:"environment"`
			Resolution  string `json:"resolution"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(rawHistory, &history); err != nil {
		t.Fatal(err)
	}
	for _, scope := range history.Scopes {
		reader := *a
		reader.product, reader.environment = scope.ProductID, scope.Environment
		record, err := reader.ReadResolution(ctx, decisionID)
		if err != nil || record == nil || record.State != scope.Resolution || record.Request["productId"] != scope.ProductID || record.Request["environment"] != scope.Environment {
			t.Fatal(record, err)
		}
	}
	unrelated := *a
	unrelated.product = "unrelated"
	if record, err := unrelated.ReadResolution(ctx, decisionID); err != nil || record != nil {
		t.Fatal(record, err)
	}

	if absent, err := a.ReadResolution(ctx, "absent"); err != nil || absent != nil {
		t.Fatal(absent, err)
	}
	pending, err := a.ReadResolution(ctx, decisionID)
	if err != nil || pending == nil || pending.State != "pending" || pending.Terminal != nil || pending.Administration != nil || !reflect.DeepEqual(pending.Request, page.Items[0].Request) {
		t.Fatal(pending, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.ReadResolution(cancelled, decisionID); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
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
		observed, err := backfillPort(t, db, "ledger").ReadResolution(ctx, decisionID)
		if err != nil || observed == nil || observed.State != "recovered" || !reflect.DeepEqual(observed.Terminal, f.Terminal) || !reflect.DeepEqual(observed.Administration, f.Administration) {
			t.Fatal(observed, err)
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
	observed, err := backfillPort(t, db, "ledger").ReadResolution(ctx, decisionID)
	if err != nil || observed == nil || !reflect.DeepEqual(observed.Administration, f.Administration) {
		t.Fatal(observed, err)
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
	var originalDigest []byte
	if e = admin.QueryRow("SELECT payload_digest FROM heimdall.audit_events WHERE product_id='ledger' AND environment='production' AND event_id=$1", f.Administration["eventId"]).Scan(&originalDigest); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id='ledger' AND environment='production' AND event_id=$1", f.Administration["eventId"]); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ReadResolution(ctx, decisionID); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall.audit_events SET payload_digest=$2 WHERE product_id='ledger' AND environment='production' AND event_id=$1", f.Administration["eventId"], originalDigest); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec("UPDATE heimdall.schema_metadata SET state='migrating'"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ReadResolution(ctx, decisionID); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = a.RecoverRuntime(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
}
