package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// RecoverRetained is internal. Its caller authenticates and authorizes the initiator.
func (a *Audit) RecoverRetained(ctx context.Context, terminal, administration map[string]any, cutoff int64) (*AuditRecoveryReceipt, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	t, e := a.prepare(terminal)
	if e != nil {
		return nil, e
	}
	admin, e := a.prepare(administration)
	if e != nil {
		return nil, e
	}
	if e = contract.ValidateRecoveryPair(t.snapshot, admin.snapshot, cutoff); e != nil {
		return nil, e
	}
	var tenant *string
	if v, ok := t.snapshot["tenantId"].(string); ok {
		tenant = &v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	conn, e := a.retainedConnection(ctx)
	if e != nil {
		return nil, databaseError(ctx)
	}
	defer conn.Close()
	tx, e := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); e != nil {
		return nil, databaseError(ctx)
	}
	if e = a.lockRetainedCapacity(ctx, tx); e != nil {
		return nil, e
	}
	completed, e := a.readRetainedTx(ctx, tx, t.receipt.EventID)
	if e != nil {
		return nil, e
	}
	recovered, e := a.readRetainedTx(ctx, tx, admin.receipt.EventID)
	if e != nil {
		return nil, e
	}
	for i, stored := range []*RetainedEventInspection{completed, recovered} {
		p := []*preparedAudit{t, admin}[i]
		if stored != nil && (stored.Identity.Digest != p.receipt.PayloadDigest || stored.Identity.RecordedAt != p.receipt.RecordedAt) {
			return nil, contract.ErrConflict
		}
	}
	var requestID, state string
	var requestedAt int64
	var terminalID, recoveryID *string
	e = tx.QueryRowContext(ctx, "SELECT request_event_id,requested_at,resolution,terminal_event_id,recovery_event_id FROM heimdall_retention.decision_identities WHERE product_id=$1 AND environment=$2 AND decision_id=$3", a.product, a.environment, t.snapshot["decisionId"]).Scan(&requestID, &requestedAt, &state, &terminalID, &recoveryID)
	if e == sql.ErrNoRows {
		return nil, contract.ErrAuditUnavailable
	}
	if e != nil {
		return nil, databaseError(ctx)
	}
	if requestedAt > cutoff {
		return nil, contract.ErrConflict
	}
	request, e := a.readRetainedTx(ctx, tx, requestID)
	if e != nil {
		return nil, e
	}
	if request == nil || !sameText(request.Identity.TenantID, tenant) {
		return nil, contract.ErrAuditUnavailable
	}
	if completed != nil || recovered != nil {
		if completed == nil || recovered == nil || state != "recovered" || terminalID == nil || recoveryID == nil || *terminalID != t.receipt.EventID || *recoveryID != admin.receipt.EventID {
			return nil, contract.ErrAuditUnavailable
		}
		if request.State != completed.State || request.State != recovered.State {
			return nil, contract.ErrAuditUnavailable
		}
		if request.State == "live" {
			sources := []contract.RetentionSource{}
			for _, member := range []*RetainedEventInspection{request, completed, recovered} {
				sources = append(sources, contract.RetentionSource{Event: member.Event, Digest: member.Identity.Digest, Routing: member.Identity.Routing, DeliveryRows: len(member.Identity.Routing)})
			}
			if _, e = contract.BuildRetentionIdentities(contract.RetentionScope{ProductID: a.product, Environment: a.environment, TenantID: tenant}, "recovered", sources); e != nil {
				return nil, e
			}
			if e = a.checkAuditReservation(ctx, tx, t.snapshot, &admin.receipt.EventID); e != nil {
				return nil, e
			}
		}
	} else {
		if state != "pending" {
			return nil, contract.ErrConflict
		}
		if request.State != "live" {
			return nil, contract.ErrAuditUnavailable
		}
		inserted, err := a.writePrepared(ctx, tx, t, false)
		if err != nil {
			return nil, err
		}
		changed, err := a.persistDecision(ctx, tx, t.snapshot, &cutoff)
		if err != nil {
			return nil, err
		}
		adminInserted, err := a.writePrepared(ctx, tx, admin, false)
		if err != nil {
			return nil, err
		}
		if !inserted || !changed || !adminInserted {
			return nil, contract.ErrAuditUnavailable
		}
		if e = a.resolveAuditCapacity(ctx, tx, t.snapshot, changed, &admin.receipt.EventID); e != nil {
			return nil, e
		}
		if e = a.chargeAuditEvent(ctx, tx, t.receipt.EventID, inserted); e != nil {
			return nil, e
		}
		if e = a.chargeAuditEvent(ctx, tx, admin.receipt.EventID, adminInserted); e != nil {
			return nil, e
		}
		if e = a.checkAuditReservation(ctx, tx, t.snapshot, &admin.receipt.EventID); e != nil {
			return nil, e
		}
		if _, e = a.retainIdentityGroupTx(ctx, tx, tenant, "recovered", []string{requestID, t.receipt.EventID, admin.receipt.EventID}); e != nil {
			return nil, e
		}
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, contract.ErrOutcomeUnknown
	}
	return &AuditRecoveryReceipt{t.receipt, admin.receipt}, nil
}
