package postgres

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// ApplyHold is internal privileged persistence. The caller authorizes the exact scope,
// actor and policy, and proves a retry obligation ended before requesting release.
func (port *RetainedRuntimeAudit) ApplyHold(ctx context.Context, request contract.AuditHoldRequest, administration map[string]any) (contract.AuditReceipt, error) {
	a := port.audit
	empty := contract.AuditReceipt{}
	if ctx == nil || request.ProductID != a.product || request.Environment != a.environment {
		return empty, contract.ErrInvalidArgument
	}
	r := request
	if r.TenantID != nil {
		v := *r.TenantID
		r.TenantID = &v
	}
	p, e := a.prepare(administration)
	if e != nil {
		return empty, e
	}
	if e = contract.ValidateAuditHold(r, p.snapshot); e != nil {
		return empty, e
	}
	change := false
	before := func(ctx context.Context, tx *sql.Tx) error {
		var target, purpose, placement string
		var tenant *string
		var placed int64
		var released sql.NullInt64
		var release sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT event_id,tenant_id,purpose,placed_at,placement_event_id,released_at,release_event_id FROM heimdall_retention.event_holds WHERE product_id=$1 AND environment=$2 AND hold_id=$3 FOR UPDATE`, a.product, a.environment, r.HoldID).Scan(&target, &tenant, &purpose, &placed, &placement, &released, &release)
		exists := err == nil
		if err != nil && err != sql.ErrNoRows {
			return databaseError(ctx)
		}
		if exists && (target != r.EventID || !sameText(tenant, r.TenantID) || purpose != r.Purpose) {
			return contract.ErrConflict
		}
		if r.Operation == "acquire" && exists {
			if placed != r.At || placement != p.receipt.EventID {
				return contract.ErrConflict
			}
		} else if r.Operation == "release" {
			if !exists {
				return contract.ErrNotFound
			}
			if r.At < placed {
				return contract.ErrInvalidArgument
			}
			if released.Valid && (released.Int64 != r.At || release.String != p.receipt.EventID) {
				return contract.ErrConflict
			}
		}
		change = !exists || (r.Operation == "release" && !released.Valid)
		prior, err := a.readRetainedTx(ctx, tx, p.receipt.EventID)
		if err != nil {
			return err
		}
		if (change && prior != nil) || (!change && prior == nil) {
			return contract.ErrAuditUnavailable
		}
		if !change {
			return nil
		}
		return a.validateHoldTarget(ctx, tx, r)
	}
	after := func(ctx context.Context, tx *sql.Tx) error {
		if !change {
			return nil
		}
		var err error
		if r.Operation == "acquire" {
			_, err = tx.ExecContext(ctx, `INSERT INTO heimdall_retention.event_holds(product_id,environment,hold_id,event_id,tenant_id,purpose,placed_at,placement_event_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, a.product, a.environment, r.HoldID, r.EventID, r.TenantID, r.Purpose, r.At, p.receipt.EventID)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE heimdall_retention.event_holds SET released_at=$4,release_event_id=$5 WHERE product_id=$1 AND environment=$2 AND hold_id=$3`, a.product, a.environment, r.HoldID, r.At, p.receipt.EventID)
		}
		if err != nil {
			return auditCapacityError(ctx, err)
		}
		return nil
	}
	return a.appendRetainedFinalized(ctx, p.snapshot, before, after)
}
func (a *Audit) validateHoldTarget(ctx context.Context, tx *sql.Tx, r contract.AuditHoldRequest) error {
	root, e := a.readRetainedTx(ctx, tx, r.EventID)
	if e != nil {
		return e
	}
	if root == nil || !sameText(root.Identity.TenantID, r.TenantID) || root.State == "retired" {
		return contract.ErrNotFound
	}
	if r.At < root.Identity.RecordedAt {
		return contract.ErrInvalidArgument
	}
	resolution := "ordinary"
	ids := pq.StringArray{r.EventID}
	if root.Identity.Kind != "ordinary" {
		if e = tx.QueryRowContext(ctx, `SELECT d.resolution,array_remove(ARRAY[d.request_event_id,d.terminal_event_id,d.recovery_event_id],NULL) AS ids FROM heimdall_retention.decision_identities d JOIN heimdall.audit_decisions s USING(product_id,environment,decision_id) JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id) WHERE d.product_id=$1 AND d.environment=$2 AND d.decision_id=$3 AND (s.request_event_id,s.requested_at,s.resolution,s.terminal_event_id,s.resolved_at) IS NOT DISTINCT FROM (d.request_event_id,d.requested_at,d.resolution,d.terminal_event_id,d.resolved_at) AND (r.request_event_id,r.resolution,r.terminal_event_id,r.recovery_event_id) IS NOT DISTINCT FROM (d.request_event_id,d.resolution,d.terminal_event_id,d.recovery_event_id)`, a.product, a.environment, root.Identity.DecisionID).Scan(&resolution, &ids); e != nil {
			if e != sql.ErrNoRows {
				return databaseError(ctx)
			}
			return contract.ErrAuditUnavailable
		}
	}
	if len(ids) < 1 || len(ids) > 3 {
		return contract.ErrAuditUnavailable
	}
	var locked int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT event_id FROM heimdall_retention.event_identities WHERE product_id=$1 AND environment=$2 AND event_id=ANY($3) ORDER BY event_id COLLATE "C" FOR UPDATE) locked`, a.product, a.environment, ids).Scan(&locked); e != nil {
		return databaseError(ctx)
	}
	if locked != len(ids) {
		return contract.ErrAuditUnavailable
	}
	// Re-read and validate every member after locking. Never backfill missing identity.
	for _, id := range ids {
		member, err := a.readRetainedTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if member == nil || member.State != "live" || !sameText(member.Identity.TenantID, r.TenantID) {
			return contract.ErrAuditUnavailable
		}
	}
	_, e = a.retentionIdentityGroupTx(ctx, tx, r.TenantID, resolution, ids, false)
	return e
}
