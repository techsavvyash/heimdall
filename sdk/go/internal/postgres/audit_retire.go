package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// Retire applies one privileged, pre-authorized retirement on the primary.
func (port *RetainedRuntimeAudit) Retire(ctx context.Context, r contract.AuditRetirementRequest, administration map[string]any) (contract.AuditReceipt, error) {
	a := port.audit
	empty := contract.AuditReceipt{}
	if ctx == nil || r.ProductID != a.product || r.Environment != a.environment {
		return empty, contract.ErrInvalidArgument
	}
	if r.TenantID != nil {
		tenant := *r.TenantID
		r.TenantID = &tenant
	}
	p, e := a.prepare(administration)
	if e != nil {
		return empty, e
	}
	if e = contract.ValidateAuditRetirement(r, p.snapshot); e != nil {
		return empty, e
	}
	var candidate retirementCandidate
	check := func(ctx context.Context, tx *sql.Tx) error {
		var same bool
		err := tx.QueryRowContext(ctx, `SELECT retirement_id=$3 AND target_event_id=$4 AND tenant_id IS NOT DISTINCT FROM $5::text
			AND retired_at=$6 AND retain_for_ms=$7 AND administration_event_id=$8
			FROM heimdall_retention.retirements WHERE product_id=$1 AND environment=$2
			AND (retirement_id=$3 OR (target_event_id=$4 AND tenant_id IS NOT DISTINCT FROM $5::text))`,
			a.product, a.environment, r.RetirementID, r.EventID, r.TenantID, r.At, r.RetainForMS, p.receipt.EventID).Scan(&same)
		prior, readErr := a.readRetainedTx(ctx, tx, p.receipt.EventID)
		if readErr != nil {
			return readErr
		}
		if err == nil {
			if !same {
				return contract.ErrConflict
			}
			root, readErr := a.readRetainedTx(ctx, tx, r.EventID)
			if readErr != nil {
				return readErr
			}
			if prior == nil || root == nil || root.State != "retired" || !sameText(root.Identity.TenantID, r.TenantID) {
				return contract.ErrAuditUnavailable
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return databaseError(ctx)
		}
		if prior != nil {
			return contract.ErrAuditUnavailable
		}
		candidate, err = a.readRetirementCandidate(ctx, tx, r.TenantID, r.EventID, r.At, r.RetainForMS)
		if err != nil {
			return err
		}
		if candidate.reason == "" {
			return contract.ErrNotFound
		}
		if candidate.reason != "candidate" {
			return contract.ErrConflict
		}
		return nil
	}
	retire := func(ctx context.Context, tx *sql.Tx) error {
		if len(candidate.ids) == 0 {
			return nil
		}
		members, _ := json.Marshal(candidate.ids)
		_, err := tx.ExecContext(ctx, `INSERT INTO heimdall_retention.retirements
			(product_id,environment,retirement_id,target_event_id,tenant_id,retired_at,retain_for_ms,administration_event_id,member_event_ids)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`,
			a.product, a.environment, r.RetirementID, r.EventID, r.TenantID, r.At, r.RetainForMS, p.receipt.EventID, string(members))
		if err != nil {
			return auditCapacityError(ctx, err)
		}
		remove := func(table, where string, key any, expected int) error {
			result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE product_id=$1 AND environment=$2 AND "+where, a.product, a.environment, key)
			if err != nil {
				return auditCapacityError(ctx, err)
			}
			if n, err := result.RowsAffected(); err != nil || (expected >= 0 && n != int64(expected)) {
				return contract.ErrAuditUnavailable
			}
			return nil
		}
		if candidate.decisionID != nil {
			for _, table := range []string{"heimdall_capacity.reservations", "heimdall.audit_decisions"} {
				if err = remove(table, "decision_id=$3", candidate.decisionID, 1); err != nil {
					return err
				}
			}
		}
		for _, table := range []string{"heimdall.audit_deliveries", "heimdall_capacity.event_charges", "heimdall.audit_events"} {
			expected := len(candidate.ids)
			if table == "heimdall.audit_deliveries" {
				expected = -1
			}
			if err = remove(table, "event_id=ANY($3)", pq.Array(candidate.ids), expected); err != nil {
				return err
			}
		}
		return nil
	}
	return a.appendRetainedFinalized(ctx, p.snapshot, check, retire)
}
