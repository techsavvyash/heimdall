package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// AppendDecision is a prepared v2 port; factory/readiness activation is separate.
// Recovery requires its own atomic administration event and is not accepted here.
func (a *Audit) AppendDecision(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
	if (event["eventType"] != "heimdall.authorization.requested" && event["eventType"] != "heimdall.authorization.completed") || event["outcome"] == "outcome_unknown" {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return a.appendFinalized(ctx, event, func(context.Context, *sql.Tx) error { return nil }, func(ctx context.Context, tx *sql.Tx, event map[string]any, _ bool) error {
		_, e := a.persistDecision(ctx, tx, event, nil)
		return e
	})
}
func (a *Audit) persistDecision(ctx context.Context, tx *sql.Tx, event map[string]any, cutoff *int64) (bool, error) {
	if event["eventType"] == "heimdall.authorization.requested" {
		b, e := contract.BindAuditDecision(a.product, a.environment, []map[string]any{event})
		if e != nil {
			return false, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall.audit_decisions(product_id,environment,decision_id,request_event_id,requested_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", a.product, a.environment, b.DecisionID, b.RequestEventID, b.RequestedAt); e != nil {
			return false, databaseError(ctx)
		}
		var id string
		var at int64
		e = tx.QueryRowContext(ctx, "SELECT request_event_id,requested_at FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2 AND decision_id=$3 FOR UPDATE", a.product, a.environment, b.DecisionID).Scan(&id, &at)
		if e == sql.ErrNoRows {
			return false, contract.ErrConflict
		}
		if e != nil {
			return false, databaseError(ctx)
		}
		if id != b.RequestEventID || at != b.RequestedAt {
			return false, contract.ErrConflict
		}
		return false, nil
	}
	var requestID, state string
	var requested, recorded int64
	var terminal *string
	var resolved *int64
	var digest, raw []byte
	e := tx.QueryRowContext(ctx, "SELECT d.request_event_id,d.requested_at,d.resolution,d.terminal_event_id,d.resolved_at,e.recorded_at,e.payload_digest,e.event FROM heimdall.audit_decisions d JOIN heimdall.audit_events e ON e.product_id=d.product_id AND e.environment=d.environment AND e.event_id=d.request_event_id WHERE d.product_id=$1 AND d.environment=$2 AND d.decision_id=$3 FOR UPDATE OF d", a.product, a.environment, event["decisionId"]).Scan(&requestID, &requested, &state, &terminal, &resolved, &recorded, &digest, &raw)
	if e == sql.ErrNoRows {
		return false, contract.ErrAuditUnavailable
	}
	if e != nil {
		return false, databaseError(ctx)
	}
	request, e := a.decodeStored(raw, requestID, recorded, hex.EncodeToString(digest))
	if e != nil {
		return false, e
	}
	b, e := contract.BindAuditDecision(a.product, a.environment, []map[string]any{request, event})
	if e != nil {
		return false, e
	}
	if cutoff != nil && b.RequestedAt > *cutoff {
		return false, contract.ErrConflict
	}
	if requested != b.RequestedAt {
		return false, contract.ErrAuditUnavailable
	}
	if state != "pending" {
		if state != b.Resolution || terminal == nil || resolved == nil || *terminal != *b.TerminalEventID || *resolved != *b.ResolvedAt {
			return false, contract.ErrConflict
		}
		return false, nil
	}
	if terminal != nil || resolved != nil {
		return false, contract.ErrAuditUnavailable
	}
	result, e := tx.ExecContext(ctx, "UPDATE heimdall.audit_decisions SET resolution=$4,terminal_event_id=$5,resolved_at=$6 WHERE product_id=$1 AND environment=$2 AND decision_id=$3 AND resolution='pending'", a.product, a.environment, b.DecisionID, b.Resolution, b.TerminalEventID, b.ResolvedAt)
	if e != nil {
		return false, databaseError(ctx)
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return false, contract.ErrAuditUnavailable
	}
	return true, nil
}

// AppendRuntime is the public factory's v2 path. Admission and event/slot writes
// share one transaction; deployment transitions take the exclusive advisory lock.
func (a *Audit) AppendRuntime(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
	if (event["eventType"] == "heimdall.authorization.requested" || event["eventType"] == "heimdall.authorization.completed") && event["outcome"] == "outcome_unknown" {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return a.appendFinalized(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared(74162401)"); e != nil {
			return databaseError(ctx)
		}
		var ready bool
		if e := tx.QueryRowContext(ctx, auditReadinessSQL, sessionChecksum, decisionChecksum).Scan(&ready); e != nil || !ready {
			return databaseError(ctx)
		}
		return nil
	}, func(ctx context.Context, tx *sql.Tx, snapshot map[string]any, _ bool) error {
		if snapshot["eventType"] == "heimdall.authorization.requested" || snapshot["eventType"] == "heimdall.authorization.completed" {
			_, e := a.persistDecision(ctx, tx, snapshot, nil)
			return e
		}
		return nil
	})
}
