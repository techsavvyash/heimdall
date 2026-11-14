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

type batchCase struct{ Name, WriteError, ReadState, ReadError, ExpectedError string }
type batchFake struct {
	c       batchCase
	request map[string]any
	calls   []string
}

func (f *batchFake) ScanPendingDecisions(context.Context, int64, *AuditDecisionCursor, int) (*AuditDecisionPage, error) {
	f.calls = append(f.calls, "scan")
	return &AuditDecisionPage{Items: []PendingDecision{{DecisionID: f.request["decisionId"].(string), Request: f.request}}}, nil
}
func (f *batchFake) RecoverRuntime(context.Context, map[string]any, map[string]any, int64) (*AuditRecoveryReceipt, error) {
	f.calls = append(f.calls, "write")
	if f.c.WriteError != "" {
		return nil, contract.NewError(contract.Code(f.c.WriteError), "")
	}
	return &AuditRecoveryReceipt{}, nil
}
func (f *batchFake) ReadResolution(context.Context, string) (*AuditResolution, error) {
	f.calls = append(f.calls, "read")
	if f.c.ReadError != "" {
		return nil, contract.NewError(contract.Code(f.c.ReadError), "")
	}
	if f.c.ReadState == "absent" {
		return nil, nil
	}
	r := &AuditResolution{State: f.c.ReadState, Request: f.request}
	if f.c.ReadState != "pending" {
		r.Terminal = map[string]any{"eventId": "winner"}
	}
	if f.c.ReadState == "recovered" {
		r.Administration = map[string]any{"eventId": "winner-admin"}
	}
	return r, nil
}
func TestRecoveryBatch(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/recovery-batch.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct{ Cases []batchCase }
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	data := capacityWritesData(t)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			port := &batchFake{c: c, request: data.Request}
			result, e := RecoverPendingDecisions(context.Background(), port, data.Cutoff, 1, func(request map[string]any) (*contract.RecoveryProposal, error) {
				return contract.BuildRecoveryProposal(request, contract.RecoveryOptions{TerminalID: data.Terminal["eventId"].(string), RequestID: data.Administration["requestId"].(string), Actor: data.Administration["actor"].(map[string]any), At: int64(data.Terminal["recordedAt"].(float64)), Cutoff: data.Cutoff, SDKVersion: "0.0.0"})
			})
			got := ""
			if e != nil {
				got = e.Error()
			}
			if got != c.ExpectedError {
				t.Fatal(result, e)
			}
			if c.ExpectedError == "" {
				if result == nil || len(result.Observed) != 1 || result.Observed[0].State != c.ReadState || result.Observed[0].TerminalID != "winner" || result.More {
					t.Fatal(result)
				}
			} else if result != nil {
				t.Fatal(result)
			}
			expected := []string{"scan", "write", "read"}
			if c.WriteError == "unavailable" {
				expected = expected[:2]
			}
			if !reflect.DeepEqual(port.calls, expected) {
				t.Fatal(port.calls)
			}
		})
	}
}

func TestRecoveryBatchPostgres(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RECOVERY_BATCH_URL")
	if url == "" {
		t.Skip("activated mixed history required")
	}
	admin, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	selected := os.Getenv("HEIMDALL_TEST_RECOVERY_BATCH_LOSS")
	if selected == "" {
		selected = url
	}
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dpublic_capacity_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = admin.Exec("UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)"); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("HEIMDALL_TEST_RECOVERY_BATCH_V4") == "1" {
		if _, e = admin.Exec("UPDATE heimdall_retention.capacity SET row_limit=used_rows+reserved_rows; UPDATE heimdall_retention.upgrade SET row_limit=(SELECT row_limit FROM heimdall_retention.capacity)"); e != nil {
			t.Fatal(e)
		}
	}
	data := capacityWritesData(t)
	a := recoveryBatchPort(t, db, "ledger")
	fault, e := sql.Open("postgres", selected+"&options=-c%20role%3Dpublic_capacity_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer fault.Close()
	port := batchSplitPort{recoveryPort: a, writer: recoveryBatchPort(t, fault, "ledger")}

	calls := 0
	propose := func(request map[string]any) (*contract.RecoveryProposal, error) {
		calls++
		return contract.BuildRecoveryProposal(request, contract.RecoveryOptions{TerminalID: data.Terminal["eventId"].(string), RequestID: data.Administration["requestId"].(string), Actor: data.Administration["actor"].(map[string]any), At: int64(data.Terminal["recordedAt"].(float64)), Cutoff: data.Cutoff, SDKVersion: "0.0.0"})
	}
	result, e := RecoverPendingDecisions(context.Background(), port, data.Cutoff, 1, propose)
	if e != nil || result == nil || len(result.Observed) != 1 || result.Observed[0].State != "recovered" || result.More {
		t.Fatal(result, e)
	}
	result, e = RecoverPendingDecisions(context.Background(), port, data.Cutoff, 1, propose)
	if e != nil || result == nil || len(result.Observed) != 0 || result.More || calls != 1 {
		t.Fatal(result, calls, e)
	}
	original, e := a.ReadResolution(context.Background(), data.Request["decisionId"].(string))
	if e != nil || original == nil || !reflect.DeepEqual(original.Administration["actor"], data.Administration["actor"]) {
		t.Fatal(original, e)
	}
}

func TestRecoveryBatchRejectsBeforeIO(t *testing.T) {
	data := capacityWritesData(t)
	for _, limit := range []int{0, 101, -1} {
		port := &batchFake{request: data.Request}
		_, e := RecoverPendingDecisions(context.Background(), port, data.Cutoff, limit, func(map[string]any) (*contract.RecoveryProposal, error) {
			t.Fatal("unexpected proposal")
			return nil, nil
		})
		if !errors.Is(e, contract.ErrInvalidArgument) || len(port.calls) != 0 {
			t.Fatal(e, port.calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	port := &batchFake{request: data.Request}
	_, e := RecoverPendingDecisions(ctx, port, data.Cutoff, 1, func(map[string]any) (*contract.RecoveryProposal, error) {
		t.Fatal("unexpected proposal")
		return nil, nil
	})
	if !errors.Is(e, contract.ErrCancelled) || len(port.calls) != 0 {
		t.Fatal(e, port.calls)
	}
}

// Only the write goes through the single-connection COMMIT-loss proxy.
type batchSplitPort struct {
	recoveryPort
	writer recoveryPort
}

func (p batchSplitPort) RecoverRuntime(ctx context.Context, terminal, administration map[string]any, cutoff int64) (*AuditRecoveryReceipt, error) {
	return p.writer.RecoverRuntime(ctx, terminal, administration, cutoff)
}

func recoveryBatchPort(t *testing.T, db *sql.DB, product string) recoveryPort {
	t.Helper()
	a := backfillPort(t, db, product)
	if os.Getenv("HEIMDALL_TEST_RECOVERY_BATCH_V4") != "1" {
		return a
	}
	r, e := NewRetainedRuntimeAudit(db, product, "production", []string{"siem"}, a.validate)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
