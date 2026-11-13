package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// SetWorkspaceLifecycle commits the local gate and its audit together. The caller authorizes the change.
func (o *OperationRecords) SetWorkspaceLifecycle(ctx context.Context, before *ManagedWorkspace, lifecycle string, actor contract.Identity, m OperationAllocation) error {
	if ctx == nil || before == nil || (lifecycle != "active" && lifecycle != "suspended") {
		return contract.ErrInvalidArgument
	}
	b, workspaceID := o.binding, before.WorkspaceID
	action := "workspace.resume"
	if lifecycle == "suspended" {
		action = "workspace.suspend"
	}
	at := float64(m.RecordedAt)
	event := map[string]any{
		"schemaVersion": float64(1), "eventId": m.EventID, "eventType": "heimdall.policy.completed", "eventVersion": float64(1),
		"occurredAt": at, "recordedAt": at, "productId": b.ProductID, "environment": b.Environment, "tenantId": workspaceID,
		"actor":  map[string]any{"kind": "user", "issuer": actor.Issuer, "subject": actor.Subject},
		"target": map[string]any{"type": "workspace", "id": workspaceID},
		"action": action, "outcome": "succeeded", "requestId": m.RequestID, "source": "sdk", "sdkVersion": "0.0.0",
		"attributes": map[string]any{}, "modelId": b.ModelID, "manifestVersion": float64(b.ManifestVersion), "policyVersion": m.PolicyVersion,
	}
	if err := validateOperationAudit(event); err != nil {
		return err
	}
	_, err := o.audit.appendWith(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		changed, err := tx.ExecContext(ctx, `UPDATE heimdall_managed.workspaces SET lifecycle=$5,updated_at=$6
         WHERE product_id=$1 AND environment=$2 AND workspace_id=$3 AND revision=$4::bigint
         AND lifecycle IN ('active','suspended') AND updated_at<=$6
         AND ($5='suspended' OR (NOT mutation_blocked AND NOT EXISTS (
           SELECT 1 FROM heimdall_managed.operations o WHERE o.product_id=$1 AND o.environment=$2
           AND o.workspace_id=$3 AND o.state NOT IN ('completed','cancelled')
         )))`, b.ProductID, b.Environment, workspaceID, before.Revision, lifecycle, m.RecordedAt)
		if err != nil {
			return err
		}
		count, err := changed.RowsAffected()
		if err == nil && count != 1 {
			return contract.ErrConflict
		}
		return err
	})
	return err
}
