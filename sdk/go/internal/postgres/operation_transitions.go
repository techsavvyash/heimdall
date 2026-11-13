package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type OperationTransitionMetadata struct {
	EventID, RequestID, PolicyVersion, RecoveryOperator string
	OccurredAt, RecordedAt, NextAttemptAt               int64
}

// Advance commits local state only. Trusted orchestration must verify remote outcomes.
func (o *OperationRecords) Advance(ctx context.Context, id string, actor contract.Identity, expected int64, transition string, m OperationTransitionMetadata) (*ManagedOperation, error) {
	if ctx == nil || !valid(id, 128) || expected < 1 || expected > maxSafe || m.NextAttemptAt < 0 || m.NextAttemptAt > maxSafe || m.OccurredAt < 0 || m.RecordedAt < m.OccurredAt || m.RecordedAt > maxSafe {
		return nil, contract.ErrInvalidArgument
	}
	switch transition {
	case "dispatch", "pre_dispatch_failure", "retry", "confirm", "uncertain", "rejected", "complete", "cancel", "recover":
	default:
		return nil, contract.ErrInvalidArgument
	}
	if transition == "recover" && m.RecoveryOperator == "" ||
		m.RecoveryOperator != "" && (!valid(m.RecoveryOperator, 128) ||
			(transition != "recover" && transition != "confirm" && transition != "complete")) {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	actorHash, e := identityBinding(actor.Issuer, actor.Subject)
	if e != nil {
		return nil, e
	}
	before, e := o.read(ctx, o.db, id, actorHash, false)
	if e != nil {
		return nil, e
	}
	if before == nil {
		return nil, contract.ErrConflict
	}
	dispatch := transition == "dispatch" || transition == "recover"
	state, e := contract.NextOperationState(before.State, transition, before.DispatchCount, before.Generation, expected)
	if e != nil {
		return nil, e
	}
	if before.Generation == maxSafe || (dispatch && before.DispatchCount == maxSafe) {
		return nil, contract.ErrUnavailable
	}
	count := before.DispatchCount
	if dispatch {
		count++
	}
	family, stage, outcome := "membership", "completed", "failed"
	if before.Intent.Kind == "transfer_owner" {
		family = "ownership"
	}
	switch transition {
	case "dispatch":
		stage, outcome = "attempt", "attempted"
	case "retry":
		stage, outcome = "intent", "requested"
	case "uncertain":
		outcome = "outcome_unknown"
	case "confirm", "complete":
		outcome = "succeeded"
	}
	b := o.binding
	event := map[string]any{"schemaVersion": float64(1), "eventId": m.EventID, "eventType": "heimdall." + family + "." + stage, "eventVersion": float64(1), "occurredAt": float64(m.OccurredAt), "recordedAt": float64(m.RecordedAt), "productId": b.ProductID, "environment": b.Environment, "tenantId": before.WorkspaceID, "actor": map[string]any{"kind": "user", "issuer": before.Intent.Actor.Issuer, "subject": before.Intent.Actor.Subject}, "target": map[string]any{"type": "workspace", "id": before.WorkspaceID}, "action": before.Intent.Kind + "." + transition, "outcome": outcome, "requestId": m.RequestID, "source": "sdk", "sdkVersion": "0.0.0", "attributes": map[string]any{}, "operationId": id, "modelId": b.ModelID, "manifestVersion": float64(b.ManifestVersion), "policyVersion": m.PolicyVersion}
	if m.RecoveryOperator != "" {
		event["actor"] = map[string]any{"kind": "system", "id": m.RecoveryOperator}
	}
	if transition == "recover" {
		event["eventType"], event["outcome"] = "heimdall.recovery.completed", "succeeded"
	}
	if e = validateOperationAudit(event); e != nil {
		return nil, e
	}
	if m.RecordedAt < before.UpdatedAt || m.NextAttemptAt < m.RecordedAt || ((transition == "dispatch" || transition == "retry") && m.RecordedAt < before.NextAttemptAt) {
		return nil, contract.ErrConflict
	}
	var result *ManagedOperation
	_, e = o.audit.appendWith(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		// Keep the same workspace-first lock order in every managed writer.
		var row workspaceRow
		err := tx.QueryRowContext(ctx, `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
   product_id,environment,workspace_id,name,lifecycle,mutation_blocked,revision::text,created_at::text,updated_at::text
   FROM heimdall_managed.workspaces WHERE product_id=$1 AND environment=$2 AND workspace_id=$3 FOR UPDATE`, b.ProductID, b.Environment, before.WorkspaceID).Scan(&row.Primary, &row.Product, &row.Environment, &row.Workspace, &row.Name, &row.Lifecycle, &row.Blocked, &row.Revision, &row.Created, &row.Updated)
		if err != nil {
			return err
		}
		w, err := decodeWorkspace(row, contract.WorkspaceScope{ProductID: b.ProductID, Environment: b.Environment, WorkspaceID: before.WorkspaceID})
		if err != nil {
			return err
		}
		creating := before.Intent.Kind == "create_workspace"
		if w == nil || !w.MutationBlocked || m.RecordedAt < w.UpdatedAt || (creating && w.Lifecycle != "pending") || (!creating && w.Lifecycle != "active" && w.Lifecycle != "suspended") || (!creating && transition == "dispatch" && w.Lifecycle != "active") {
			return contract.ErrConflict
		}
		changed, err := tx.ExecContext(ctx, `UPDATE heimdall_managed.operations SET state=$5,dispatch_count=$6,generation=generation+1,next_attempt_at=$7,updated_at=$8
   WHERE product_id=$1 AND environment=$2 AND operation_id=$3 AND generation=$4 AND state=$9`, b.ProductID, b.Environment, id, expected, state, count, m.NextAttemptAt, m.RecordedAt, before.State)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return contract.ErrConflict
		}
		if transition == "complete" {
			targetHash, _ := identityBinding(before.Intent.Target.Issuer, before.Intent.Target.Subject)
			if before.Intent.Kind == "remove_member" {
				_, err = tx.ExecContext(ctx, "DELETE FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND environment=$2 AND workspace_id=$3 AND identity_hash=$4", b.ProductID, b.Environment, before.WorkspaceID, targetHash)
			} else {
				principal, _ := contract.EncodeIdentity(before.Intent.Target.Issuer, before.Intent.Target.Subject)
				changed, err = tx.ExecContext(ctx, `INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
     VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(product_id,environment,identity_hash,workspace_id)
     DO UPDATE SET observed_at=EXCLUDED.observed_at WHERE heimdall_managed.membership_candidates.principal=EXCLUDED.principal
     AND heimdall_managed.membership_candidates.observed_at<=EXCLUDED.observed_at`, b.ProductID, b.Environment, before.WorkspaceID, targetHash, principal, m.RecordedAt)
				if err == nil {
					n, err = changed.RowsAffected()
					if err == nil && n != 1 {
						return contract.ErrConflict
					}
				}
			}
			if err != nil {
				return err
			}
		}
		lifecycle := w.Lifecycle
		if creating && transition == "complete" {
			lifecycle = "active"
		}
		if creating && transition == "cancel" {
			lifecycle = "tombstoned"
		}
		_, err = tx.ExecContext(ctx, `UPDATE heimdall_managed.workspaces SET lifecycle=$4,mutation_blocked=$5,updated_at=$6
   WHERE product_id=$1 AND environment=$2 AND workspace_id=$3`, b.ProductID, b.Environment, before.WorkspaceID, lifecycle, transition != "complete" && transition != "cancel", m.RecordedAt)
		if err != nil {
			return err
		}
		result, err = o.read(ctx, tx, id, actorHash, false)
		if err != nil {
			return err
		}
		if result == nil {
			return contract.ErrUnavailable
		}
		return nil
	})
	if errors.Is(e, contract.ErrOutcomeUnknown) {
		return nil, contract.NewError(contract.OutcomeUnknown, id)
	}
	if e != nil {
		return nil, e
	}
	return result, nil
}
