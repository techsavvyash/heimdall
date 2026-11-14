package auditflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type ManagedExportRequest struct {
	Kind, OperationID  string
	WorkspaceID, After *string
	Limit              int
}

// RunManagedExport releases one sanitized page after a matching durable completion.
func (a *Authentication) RunManagedExport(
	ctx context.Context, raw string, request ManagedExportRequest, modelID string, manifestVersion int,
	policy func(context.Context, MaintenanceContext) (bool, error),
	scan contract.ManagedExportScan,
) ([]byte, error) {
	r := request
	if (r.Kind != "workspaces" && r.Kind != "operations") || (r.Kind == "operations" && r.WorkspaceID == nil) ||
		(r.After != nil && !maintenanceToken.MatchString(*r.After)) || r.Limit < 1 || r.Limit > 100 || scan == nil {
		return nil, contract.ErrInvalidArgument
	}
	target := "inventory"
	if r.WorkspaceID != nil {
		target = *r.WorkspaceID
	}
	if r.After != nil {
		value := *r.After
		r.After = &value
	}
	return runMaintenance(a, ctx, raw, MaintenanceRequest{
		TenantID: r.WorkspaceID, Action: "managed.export." + r.Kind, TargetEventID: target,
		OperationID: r.OperationID, ModelID: modelID, ManifestVersion: manifestVersion,
	}, policy, func(ctx context.Context, scope MaintenanceContext, current func() error) ([]byte, error) {
		started := a.writer.now().UnixMilli()
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var workspace *string
		if scope.TenantID != nil {
			value := *scope.TenantID
			workspace = &value
		}
		page, failure := bounded(readCtx, func(readCtx context.Context) ([]byte, error) {
			return scan(readCtx, r.Kind, workspace, r.After, r.Limit)
		})
		if failure == nil {
			failure = contract.CheckCancellation(readCtx)
		}
		if failure == nil {
			var header struct {
				SchemaVersion                int
				ProductID, Environment, Kind string
				WorkspaceID                  json.RawMessage
			}
			tenant, _ := json.Marshal(scope.TenantID)
			if json.Unmarshal(page, &header) != nil || header.SchemaVersion != 1 || header.ProductID != scope.ProductID ||
				header.Environment != scope.Environment || header.Kind != r.Kind || string(header.WorkspaceID) != string(tenant) {
				failure = contract.ErrUnavailable
			}
		}
		var id [16]byte
		if _, e := rand.Read(id[:]); e != nil {
			return nil, contract.ErrAuditUnavailable
		}
		event := map[string]any{
			"schemaVersion": float64(1), "eventId": hex.EncodeToString(id[:]), "eventType": "heimdall.export.completed", "eventVersion": float64(1),
			"occurredAt": float64(started), "recordedAt": float64(max(started, a.writer.now().UnixMilli())),
			"productId": scope.ProductID, "environment": scope.Environment, "tenantId": nil,
			"actor":  map[string]any{"kind": "user", "issuer": scope.Actor.Issuer, "subject": scope.Actor.Subject},
			"target": map[string]any{"type": "managed_export", "id": target},
			"action": scope.Action, "outcome": "succeeded", "requestId": scope.OperationID, "operationId": scope.OperationID,
			"policyVersion": scope.PolicyVersion, "source": "sdk", "sdkVersion": contract.SDKVersion, "attributes": map[string]any{},
		}
		if scope.TenantID != nil {
			event["tenantId"] = *scope.TenantID
		}
		if failure != nil {
			var typed *contract.Error
			if !errors.As(failure, &typed) {
				failure = contract.ErrUnavailable
			}
			event["outcome"] = "failed"
			event["reasonCode"] = string(contract.Unavailable)
			if typed != nil {
				event["reasonCode"] = string(typed.Code)
			}
		}
		if e := a.writer.commit(event); e != nil {
			return nil, contract.ErrAuditUnavailable
		}
		if failure != nil {
			return nil, failure
		}
		if e := current(); e != nil {
			return nil, e
		}
		return page, nil
	}, "managed_export")
}
