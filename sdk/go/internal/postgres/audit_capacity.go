package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// Internal prepared port used by migration tests; public writes require v3.
func (a *Audit) AppendCapacity(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
	return a.appendCapacityWith(ctx, event, lockAuditCapacity)
}
func (a *Audit) appendCapacityWith(ctx context.Context, event map[string]any, admit func(context.Context, *sql.Tx) error) (contract.AuditReceipt, error) {
	if event["eventType"] == "heimdall.authorization.completed" && event["outcome"] == "outcome_unknown" {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return a.appendFinalized(ctx, event, admit, func(ctx context.Context, tx *sql.Tx, snapshot map[string]any, inserted bool) error {
		request := snapshot["eventType"] == "heimdall.authorization.requested"
		completion := snapshot["eventType"] == "heimdall.authorization.completed"
		if request || completion {
			changed, e := a.persistDecision(ctx, tx, snapshot, nil)
			if e != nil {
				return e
			}
			if completion {
				if e = a.resolveAuditCapacity(ctx, tx, snapshot, changed, nil); e != nil {
					return e
				}
			}
		}
		if e := a.chargeAuditEvent(ctx, tx, snapshot["eventId"].(string), inserted); e != nil {
			return e
		}
		if request && inserted {
			if _, e := tx.ExecContext(ctx, "INSERT INTO heimdall_capacity.reservations(product_id,environment,decision_id,request_event_id) VALUES($1,$2,$3,$4)", a.product, a.environment, snapshot["decisionId"], snapshot["eventId"]); e != nil {
				return auditCapacityError(ctx, e)
			}
		}
		if request || completion {
			return a.checkAuditReservation(ctx, tx, snapshot, nil)
		}
		return nil
	})
}
func (a *Audit) RecoverCapacity(ctx context.Context, terminal, administration map[string]any, cutoff int64) (*AuditRecoveryReceipt, error) {
	return a.recoverCapacityWith(ctx, terminal, administration, cutoff, lockAuditCapacity)
}

// RecoverRuntime is a trusted maintenance port. The caller authorizes the initiator.
func (a *Audit) RecoverRuntime(ctx context.Context, terminal, administration map[string]any, cutoff int64) (*AuditRecoveryReceipt, error) {
	return a.recoverCapacityWith(ctx, terminal, administration, cutoff, lockRuntimeCapacity)
}
func (a *Audit) recoverCapacityWith(ctx context.Context, terminal, administration map[string]any, cutoff int64, admit func(context.Context, *sql.Tx) error) (*AuditRecoveryReceipt, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	completion, e := a.prepare(terminal)
	if e != nil {
		return nil, e
	}
	recovery, e := a.prepare(administration)
	if e != nil {
		return nil, e
	}
	if e = contract.ValidateRecoveryPair(completion.snapshot, recovery.snapshot, cutoff); e != nil {
		return nil, e
	}
	receipt, e := a.appendFinalized(ctx, completion.snapshot, admit, func(ctx context.Context, tx *sql.Tx, snapshot map[string]any, inserted bool) error {
		changed, e := a.persistDecision(ctx, tx, snapshot, &cutoff)
		if e != nil {
			return e
		}
		adminInserted, e := a.writePrepared(ctx, tx, recovery, !changed)
		if e != nil {
			return e
		}
		if e = a.resolveAuditCapacity(ctx, tx, snapshot, changed, &recovery.receipt.EventID); e != nil {
			return e
		}
		if e = a.chargeAuditEvent(ctx, tx, completion.receipt.EventID, inserted); e != nil {
			return e
		}
		if e = a.chargeAuditEvent(ctx, tx, recovery.receipt.EventID, adminInserted); e != nil {
			return e
		}
		return a.checkAuditReservation(ctx, tx, snapshot, &recovery.receipt.EventID)
	})
	if e != nil {
		return nil, e
	}
	return &AuditRecoveryReceipt{receipt, recovery.receipt}, nil
}
func lockAuditCapacity(ctx context.Context, tx *sql.Tx) error {
	if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared(74162401)"); e != nil {
		return databaseError(ctx)
	}
	var state string
	if e := tx.QueryRowContext(ctx, "SELECT state FROM heimdall_capacity.policy WHERE singleton AND NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' FOR UPDATE").Scan(&state); e != nil {
		if e == sql.ErrNoRows {
			return contract.ErrAuditUnavailable
		}
		return databaseError(ctx)
	}
	if state != "ready" {
		return contract.ErrAuditUnavailable
	}
	return nil
}
func auditCapacityError(ctx context.Context, e error) error {
	var pg *pq.Error
	if errors.As(e, &pg) && (pg.Code == "23514" || pg.Code == "23503" || pg.Code == "P0002") {
		return contract.ErrAuditUnavailable
	}
	return databaseError(ctx)
}
func (a *Audit) chargeAuditEvent(ctx context.Context, tx *sql.Tx, eventID string, inserted bool) error {
	if inserted {
		if _, e := tx.ExecContext(ctx, "INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) VALUES($1,$2,$3)", a.product, a.environment, eventID); e != nil {
			return auditCapacityError(ctx, e)
		}
	}
	// Runtime retries may not repair uncharged imported history.
	var valid bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM heimdall_capacity.event_charges c JOIN heimdall.audit_events e USING(product_id,environment,event_id)
 WHERE c.product_id=$1 AND c.environment=$2 AND c.event_id=$3 AND c.envelope_bytes=octet_length(e.event::text)
 AND c.delivery_rows=(SELECT count(*) FROM heimdall.audit_deliveries d WHERE d.product_id=c.product_id AND d.environment=c.environment AND d.event_id=c.event_id)
 )`, a.product, a.environment, eventID).Scan(&valid); e != nil {
		return databaseError(ctx)
	}
	if !valid {
		return contract.ErrAuditUnavailable
	}
	return nil
}
func (a *Audit) resolveAuditCapacity(ctx context.Context, tx *sql.Tx, event map[string]any, changed bool, recoveryID *string) error {
	if !changed {
		return nil
	}
	resolution := "completed"
	if recoveryID != nil {
		resolution = "recovered"
	}
	result, e := tx.ExecContext(ctx, "UPDATE heimdall_capacity.reservations SET resolution=$4,terminal_event_id=$5,recovery_event_id=$6 WHERE product_id=$1 AND environment=$2 AND decision_id=$3 AND resolution='pending'", a.product, a.environment, event["decisionId"], resolution, event["eventId"], recoveryID)
	if e != nil {
		return auditCapacityError(ctx, e)
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return contract.ErrAuditUnavailable
	}
	return nil
}
func (a *Audit) checkAuditReservation(ctx context.Context, tx *sql.Tx, event map[string]any, recoveryID *string) error {
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM heimdall_capacity.reservations r JOIN heimdall.audit_decisions d USING(product_id,environment,decision_id)
 JOIN heimdall_capacity.event_charges c ON c.product_id=r.product_id AND c.environment=r.environment AND c.event_id=r.request_event_id
 WHERE r.product_id=$1 AND r.environment=$2 AND r.decision_id=$3
 AND r.request_event_id=d.request_event_id AND r.resolution=d.resolution
 AND r.terminal_event_id IS NOT DISTINCT FROM d.terminal_event_id
 AND (($4 AND r.request_event_id=$5) OR (NOT $4 AND r.terminal_event_id=$5 AND r.recovery_event_id IS NOT DISTINCT FROM $6::text))
 )`, a.product, a.environment, event["decisionId"], event["eventType"] == "heimdall.authorization.requested", event["eventId"], recoveryID).Scan(&valid)
	if e != nil {
		return databaseError(ctx)
	}
	if !valid {
		return contract.ErrAuditUnavailable
	}
	return nil
}

func lockRuntimeCapacity(ctx context.Context, tx *sql.Tx) error {
	if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared(74162401)"); e != nil {
		return databaseError(ctx)
	}
	var ready bool
	if e := tx.QueryRowContext(ctx, capacityReadinessSQL, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1]).Scan(&ready); e != nil || !ready {
		return databaseError(ctx)
	}
	return lockAuditCapacity(ctx, tx)
}
