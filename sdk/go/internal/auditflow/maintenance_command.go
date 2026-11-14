package auditflow

import (
	"context"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type MaintenanceStore interface {
	ApplyHold(context.Context, contract.AuditHoldRequest, map[string]any) (contract.AuditReceipt, error)
	Retire(context.Context, contract.AuditRetirementRequest, map[string]any) (contract.AuditReceipt, error)
}

// MaintenanceOperation is an immutable operation proposal. A retry reuses its
// identifier and timestamp. Policy is fixed when the command is constructed.
type MaintenanceOperation struct {
	TenantID      *string `json:"tenantId"`
	Action        string  `json:"action"`
	TargetEventID string  `json:"targetEventId"`
	OperationID   string  `json:"operationId"`
	At            int64   `json:"at"`
}
type MaintenanceCommand struct {
	auth            *Authentication
	store           MaintenanceStore
	modelID         string
	manifestVersion int
	retainForMS     int64
	authorize       func(context.Context, MaintenanceContext) (bool, error)
}

func NewMaintenanceCommand(auth *Authentication, store MaintenanceStore, modelID string, manifestVersion int, retainForMS int64, authorize func(context.Context, MaintenanceContext) (bool, error)) (*MaintenanceCommand, error) {
	if auth == nil || auth.writer == nil || store == nil || !maintenanceToken.MatchString(modelID) || manifestVersion < 1 || manifestVersion > 2147483647 ||
		retainForMS < 1 || retainForMS > 9007199254740991 || authorize == nil {
		return nil, contract.ErrInvalidArgument
	}
	return &MaintenanceCommand{auth, store, modelID, manifestVersion, retainForMS, authorize}, nil
}

func (c *MaintenanceCommand) Run(ctx context.Context, bearer string, op MaintenanceOperation) (contract.AuditReceipt, error) {
	if c == nil || c.auth == nil || ctx == nil || op.At < 0 || op.At > 9007199254740991 || op.At > c.auth.writer.now().UnixMilli() {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return c.auth.RunMaintenance(ctx, bearer, MaintenanceRequest{
		TenantID: op.TenantID, Action: op.Action, TargetEventID: op.TargetEventID,
		OperationID: op.OperationID, ModelID: c.modelID, ManifestVersion: c.manifestVersion,
	}, c.authorize, func(ctx context.Context, scope MaintenanceContext) (contract.AuditReceipt, error) {
		if op.At > c.auth.writer.now().UnixMilli() {
			return contract.AuditReceipt{}, contract.ErrUnavailable
		}
		var tenant any
		if scope.TenantID != nil {
			tenant = *scope.TenantID
		}
		at := float64(op.At)
		admin := map[string]any{
			"schemaVersion": float64(1), "eventType": "heimdall.policy.completed", "eventVersion": float64(1),
			"occurredAt": at, "recordedAt": at, "productId": scope.ProductID, "environment": scope.Environment, "tenantId": tenant,
			"actor":  map[string]any{"kind": "user", "issuer": scope.Actor.Issuer, "subject": scope.Actor.Subject},
			"target": map[string]any{"type": "audit_event", "id": scope.TargetEventID},
			"action": scope.Action, "outcome": "succeeded", "requestId": scope.OperationID, "source": "sdk",
			"sdkVersion": "0.0.0", "attributes": map[string]any{}, "operationId": scope.OperationID, "policyVersion": scope.PolicyVersion,
		}
		if scope.Action == "audit.retention.retire" {
			r := contract.AuditRetirementRequest{
				ProductID: scope.ProductID, Environment: scope.Environment, TenantID: scope.TenantID,
				RetirementID: scope.OperationID, EventID: scope.TargetEventID, At: op.At, RetainForMS: c.retainForMS, PolicyVersion: scope.PolicyVersion,
			}
			admin["eventId"] = contract.AuditRetirementEventID(r)
			return c.store.Retire(ctx, r, admin)
		}
		purpose, operation := "administrative", "acquire"
		switch scope.Action {
		case "audit.hold.administrative.acquire":
		case "audit.hold.administrative.release":
			operation = "release"
		case "audit.hold.retry.acquire":
			purpose = "retry"
		case "audit.hold.retry.release":
			purpose, operation = "retry", "release"
		default:
			return contract.AuditReceipt{}, contract.ErrInvalidArgument
		}
		r := contract.AuditHoldRequest{
			ProductID: scope.ProductID, Environment: scope.Environment, TenantID: scope.TenantID,
			HoldID: scope.OperationID, EventID: scope.TargetEventID, At: op.At, Purpose: purpose, Operation: operation, PolicyVersion: scope.PolicyVersion,
		}
		admin["eventId"] = contract.AuditHoldEventID(r)
		return c.store.ApplyHold(ctx, r, admin)
	})
}
