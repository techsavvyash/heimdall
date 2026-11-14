package postgres

import (
	"context"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// AuditResolution is validated durable history, not a fresh authorization decision.
type AuditResolution struct {
	DecisionID     string         `json:"decisionId"`
	State          string         `json:"state"`
	Request        map[string]any `json:"request"`
	Terminal       map[string]any `json:"terminal"`
	Administration map[string]any `json:"administration"`
}
type resolutionRow struct {
	capacityDecisionRow
	ReservationRequest    *string        `json:"reservationRequest"`
	ReservationResolution *string        `json:"reservationResolution"`
	ReservationTerminal   *string        `json:"reservationTerminal"`
	RecoveryID            *string        `json:"recoveryId"`
	Administration        *backfillEvent `json:"administration"`
}

// ReadResolution observes one primary snapshot. Nil means absent, never an allow.
func (a *Audit) ReadResolution(ctx context.Context, decisionID string) (*AuditResolution, error) {
	if ctx == nil || !valid(decisionID, 128) {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var ready bool
	var raw []byte
	e := a.db.QueryRowContext(ctx, resolutionSQL, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1], a.product, a.environment, decisionID).Scan(&ready, &raw)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if !ready {
		return nil, contract.ErrUnavailable
	}
	if raw == nil {
		return nil, nil
	}
	var row resolutionRow
	if json.Unmarshal(raw, &row) != nil || row.ID != decisionID {
		return nil, contract.ErrAuditUnavailable
	}
	request, e := a.decodeCapacityEvent(row.Request)
	if e != nil {
		return nil, e
	}
	events := []map[string]any{request}
	var terminal, administration map[string]any
	if row.TerminalID != nil {
		terminal, e = a.decodeCapacityEvent(row.Terminal)
		if e != nil {
			return nil, e
		}
		events = append(events, terminal)
	} else if row.Terminal != nil {
		return nil, contract.ErrAuditUnavailable
	}
	binding, e := contract.BindAuditDecision(a.product, a.environment, events)
	if e != nil {
		return nil, e
	}
	requested, ok := workspaceDecimal(&row.RequestedAt, maxSafe, false)
	if !ok || binding.DecisionID != decisionID || binding.RequestedAt != requested || binding.Resolution != row.Resolution || !sameText(binding.TerminalEventID, row.TerminalID) || (binding.ResolvedAt == nil) != (row.ResolvedAt == nil) || row.ReservationRequest == nil || *row.ReservationRequest != binding.RequestEventID || row.ReservationResolution == nil || *row.ReservationResolution != row.Resolution || !sameText(row.ReservationTerminal, row.TerminalID) {
		return nil, contract.ErrAuditUnavailable
	}
	if row.ResolvedAt != nil {
		at, ok := workspaceDecimal(row.ResolvedAt, maxSafe, false)
		if !ok || at != *binding.ResolvedAt {
			return nil, contract.ErrAuditUnavailable
		}
	}
	if row.Resolution == "recovered" {
		if row.RecoveryID == nil || row.TerminalID == nil || *row.RecoveryID != contract.RecoveryEventID(a.product, a.environment, *row.TerminalID) {
			return nil, contract.ErrAuditUnavailable
		}
		administration, e = a.decodeCapacityEvent(row.Administration)
		if e != nil {
			return nil, e
		}
		if e = contract.ValidateRecoveryPair(terminal, administration, requested); e != nil {
			return nil, contract.ErrAuditUnavailable
		}
	} else if row.RecoveryID != nil || row.Administration != nil {
		return nil, contract.ErrAuditUnavailable
	}
	return &AuditResolution{decisionID, row.Resolution, request, terminal, administration}, nil
}
func sameText(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

const resolutionSQL = `SELECT readiness.ready, CASE WHEN d.decision_id IS NULL THEN NULL ELSE jsonb_build_object(
 'id',d.decision_id,'requestedAt',d.requested_at::text,'resolution',d.resolution,'terminalId',d.terminal_event_id,'resolvedAt',d.resolved_at::text,
 'reservationRequest',c.request_event_id,'reservationResolution',c.resolution,'reservationTerminal',c.terminal_event_id,'recoveryId',c.recovery_event_id,
 'request',CASE WHEN r.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',r.event_id,'recorded_at',r.recorded_at::text,'payload_digest',encode(r.payload_digest,'hex'),'event',r.event) END,'terminal',CASE WHEN t.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',t.event_id,'recorded_at',t.recorded_at::text,'payload_digest',encode(t.payload_digest,'hex'),'event',t.event) END,'administration',CASE WHEN a.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',a.event_id,'recorded_at',a.recorded_at::text,'payload_digest',encode(a.payload_digest,'hex'),'event',a.event) END) END
 FROM (` + capacityReadinessSQL + `) readiness
 LEFT JOIN heimdall.audit_decisions d ON d.product_id=$5 AND d.environment=$6 AND d.decision_id=$7
 LEFT JOIN heimdall_capacity.reservations c ON c.product_id=d.product_id AND c.environment=d.environment AND c.decision_id=d.decision_id
 LEFT JOIN heimdall.audit_events r ON r.product_id=d.product_id AND r.environment=d.environment AND r.event_id=d.request_event_id
 LEFT JOIN heimdall.audit_events t ON t.product_id=d.product_id AND t.environment=d.environment AND t.event_id=d.terminal_event_id
 LEFT JOIN heimdall.audit_events a ON a.product_id=d.product_id AND a.environment=d.environment AND a.event_id=c.recovery_event_id`
