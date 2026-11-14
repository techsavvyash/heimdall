package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"testing"
)

func TestRecoveryRestartProcess(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RECOVERY_RESTART_URL")
	if url == "" {
		t.Skip("isolated restart worker")
	}
	mode := os.Getenv("HEIMDALL_TEST_RECOVERY_RESTART_MODE")
	if mode != "seed" && mode != "crash" && mode != "resume" {
		t.Fatal("invalid mode")
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/recovery-restart.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		ProductID, Environment  string
		DecisionIDs, RequestIDs []string
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	connector, e := NewConnector(url + "&options=-c%20role%3Dpublic_capacity_runtime")
	if e != nil {
		t.Fatal(e)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	a := backfillPort(t, db, f.ProductID)
	a.environment = f.Environment
	data := capacityWritesData(t)
	ctx := context.Background()
	if mode == "seed" {
		for i, id := range f.DecisionIDs {
			request := map[string]any{}
			for k, v := range data.Request {
				request[k] = v
			}
			request["productId"], request["environment"], request["decisionId"], request["eventId"] = f.ProductID, f.Environment, id, f.RequestIDs[i]
			if _, e = a.AppendRuntime(ctx, request); e != nil {
				t.Fatal(e)
			}
		}
		return
	}
	calls := 0
	propose := func(request map[string]any) (*contract.RecoveryProposal, error) {
		calls++
		if mode == "crash" && calls == 2 {
			p, e := os.FindProcess(os.Getpid())
			if e != nil {
				t.Fatal(e)
			}
			if e = p.Kill(); e != nil {
				t.Fatal(e)
			}
			select {}
		}
		return contract.BuildRecoveryProposal(request, contract.RecoveryOptions{TerminalID: "terminal-" + mode + "-" + request["decisionId"].(string), RequestID: "request-" + mode, Actor: map[string]any{"kind": "system", "id": mode + "-worker"}, At: int64(data.Terminal["recordedAt"].(float64)), Cutoff: data.Cutoff, SDKVersion: "0.0.0"})
	}
	result, e := RecoverPendingDecisions(ctx, a, data.Cutoff, 2, propose)
	if mode != "resume" || e != nil || result == nil || len(result.Observed) != 1 || result.Observed[0].DecisionID != f.DecisionIDs[1] || calls != 1 {
		t.Fatal(result, calls, e)
	}
	for i, id := range f.DecisionIDs {
		record, e := a.ReadResolution(ctx, id)
		owner := "resume"
		if i == 0 {
			owner = "crash"
		}
		if e != nil || record == nil || record.State != "recovered" || record.Terminal["eventId"] != "terminal-"+owner+"-"+id || !reflect.DeepEqual(record.Administration["actor"], map[string]any{"kind": "system", "id": owner + "-worker"}) {
			t.Fatal(record, e)
		}
	}
	result, e = RecoverPendingDecisions(ctx, a, data.Cutoff, 2, propose)
	if e != nil || result == nil || len(result.Observed) != 0 || result.More || calls != 1 {
		t.Fatal(result, calls, e)
	}
}
