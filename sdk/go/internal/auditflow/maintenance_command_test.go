package auditflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type recordingMaintenanceStore struct {
	subject, issuer string
	calls           int
}

func (s *recordingMaintenanceStore) check(event map[string]any) (contract.AuditReceipt, error) {
	s.calls++
	actor := event["actor"].(map[string]any)
	if actor["kind"] != "user" || actor["issuer"] != s.issuer || actor["subject"] != s.subject || event["tenantId"] != nil ||
		event["productId"] != "ledger" || event["environment"] != "production" || event["policyVersion"] != "p1" {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return receipt(event), nil
}
func (s *recordingMaintenanceStore) ApplyHold(_ context.Context, r contract.AuditHoldRequest, event map[string]any) (contract.AuditReceipt, error) {
	if e := contract.ValidateAuditHold(r, event); e != nil {
		return contract.AuditReceipt{}, e
	}
	if event["action"] != "audit.hold."+r.Purpose+"."+r.Operation {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return s.check(event)
}
func (s *recordingMaintenanceStore) Retire(_ context.Context, r contract.AuditRetirementRequest, event map[string]any) (contract.AuditReceipt, error) {
	if e := contract.ValidateAuditRetirement(r, event); e != nil {
		return contract.AuditReceipt{}, e
	}
	if r.RetainForMS != 1000 {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return s.check(event)
}

func testMaintenanceCommand(t *testing.T, bearer string, now int64, issuer, audience, subject string) {
	ctx := context.Background()
	target := "bound-maintenance-go"
	var cases []struct {
		MaintenanceOperation
		Name, Expected string
		Future, Retry  bool
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/maintenance-command.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	fake := &recordingMaintenanceStore{subject: subject, issuer: issuer}
	var store MaintenanceStore = fake
	appendEvent := func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
		return receipt(event), nil
	}
	if url := os.Getenv("HEIMDALL_TEST_MAINTENANCE_COMMAND_URL"); url != "" {
		db, e := sql.Open("postgres", url)
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close()
		durable, e := postgres.NewRetainedRuntimeAudit(db, "ledger", "production", nil, contract.ValidateAuditShape)
		if e != nil {
			t.Fatal(e)
		}
		seed := map[string]any{
			"schemaVersion": float64(1), "eventId": target, "eventType": "heimdall.policy.completed", "eventVersion": float64(1),
			"occurredAt": float64(now - 5000), "recordedAt": float64(now - 5000), "productId": "ledger", "environment": "production", "tenantId": nil,
			"actor": map[string]any{"kind": "system", "id": "test-seed"}, "target": map[string]any{"type": "audit_event", "id": target},
			"action": "audit.test.seed", "outcome": "succeeded", "requestId": target, "source": "sdk", "sdkVersion": "0.0.0", "attributes": map[string]any{}, "policyVersion": "p1",
		}
		if _, e = durable.Append(ctx, seed); e != nil {
			t.Fatal(e)
		}
		store = durable
		appendEvent = durable.Append
		defer func() {
			result, e := durable.Inspect(ctx, nil, target)
			if e != nil || result == nil || result.State != "retired" {
				t.Error("target did not retire", e)
			}
		}()
	}
	auth, e := NewAuthentication(identity.StatusConfig{Config: identity.Config{Issuer: issuer, Audience: audience, JWKSURI: issuer + "/keys", Profile: "production"}, ProductID: "ledger", Environment: "production", PolicyVersion: "p1", ClientID: "client"},
		func(context.Context) (string, error) { return "secret", nil }, identity.Gate{Check: func(context.Context, identity.Identity) (bool, error) { return false, nil }, Invalidate: func(context.Context, identity.Negative) error { return nil }}, appendEvent, func() time.Time { return time.UnixMilli(now) })
	if e != nil {
		t.Fatal(e)
	}
	command, e := NewMaintenanceCommand(auth, store, "pinned-model", 7, 1000, func(_ context.Context, s MaintenanceContext) (bool, error) {
		return s.TenantID == nil && s.Actor.Issuer == issuer && s.Actor.Subject == subject && s.ModelID == "pinned-model" && s.ManifestVersion == 7 && s.TargetEventID == target, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var original contract.AuditReceipt
	for _, c := range cases {
		op := c.MaintenanceOperation
		op.At = now
		op.TargetEventID = target
		op.OperationID += "-go"
		if c.Future {
			op.At++
		}
		before := fake.calls
		result, err := command.Run(ctx, bearer, op)
		status := "receipt"
		if err != nil {
			status = err.Error()
		}
		if status != c.Expected {
			t.Fatalf("%s: expected %s, got %s", c.Name, c.Expected, status)
		}
		if err != nil && before != fake.calls {
			t.Fatal("unauthorized operation touched storage")
		}
		if c.Retry && result != original {
			t.Fatal("changed retry receipt")
		}
		if err == nil && strings.HasPrefix(c.Action, "audit.retention.") {
			original = result
		}
	}
	if durable, ok := store.(*postgres.RetainedRuntimeAudit); ok {
		persisted, err := durable.Inspect(ctx, nil, original.EventID)
		if err != nil || persisted == nil || persisted.Event == nil {
			t.Fatal("administrative event missing", err)
		}
		actor, _ := persisted.Event["actor"].(map[string]any)
		if actor["kind"] != "user" || actor["issuer"] != issuer || actor["subject"] != subject ||
			persisted.Event["policyVersion"] != "p1" || persisted.Event["action"] != "audit.retention.retire" {
			t.Fatal("persisted administrative binding mismatch")
		}
	}
	if store == fake && fake.calls != 6 {
		t.Fatal("wrong number of storage calls", fake.calls)
	}
}
