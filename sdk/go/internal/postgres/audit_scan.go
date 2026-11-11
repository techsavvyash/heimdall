package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// AuditDecisionCursor is trusted maintenance state, not a public pagination token.
type AuditDecisionCursor struct {
	ProductID   string `json:"productId"`
	Environment string `json:"environment"`
	Cutoff      int64  `json:"cutoff"`
	RequestedAt int64  `json:"requestedAt"`
	DecisionID  string `json:"decisionId"`
}
type PendingDecision struct {
	DecisionID  string         `json:"decisionId"`
	RequestedAt int64          `json:"requestedAt"`
	Request     map[string]any `json:"request"`
}
type AuditDecisionPage struct {
	Items []PendingDecision    `json:"items"`
	Next  *AuditDecisionCursor `json:"next"`
}
type pendingDecisionRow struct {
	DecisionID     string          `json:"decisionId"`
	RequestedAt    int64           `json:"requestedAt"`
	RequestEventID string          `json:"requestEventId"`
	RecordedAt     int64           `json:"recordedAt"`
	Digest         string          `json:"digest"`
	Event          json.RawMessage `json:"event"`
}

// ScanPendingDecisions returns candidates; finalization must recheck the pending slot.
func (a *Audit) ScanPendingDecisions(ctx context.Context, cutoff int64, after *AuditDecisionCursor, limit int) (*AuditDecisionPage, error) {
	if ctx == nil || cutoff < 0 || cutoff > maxSafe || limit < 1 || limit > 100 || (after != nil && (after.ProductID != a.product || after.Environment != a.environment || after.Cutoff != cutoff || after.RequestedAt < 0 || after.RequestedAt > cutoff || !valid(after.DecisionID, 128))) {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var at, id any
	var previous *AuditDecisionCursor
	if after != nil {
		copy := *after
		previous = &copy
		at = copy.RequestedAt
		id = copy.DecisionID
	}
	var primary bool
	var total int
	var raw []byte
	e := a.db.QueryRowContext(ctx, `WITH selected AS MATERIALIZED (
 SELECT d.decision_id,d.requested_at,d.request_event_id,e.recorded_at,e.payload_digest,e.event
 FROM heimdall.audit_decisions d LEFT JOIN heimdall.audit_events e ON e.product_id=d.product_id AND e.environment=d.environment AND e.event_id=d.request_event_id
 WHERE d.product_id=$1 AND d.environment=$2 AND d.resolution='pending' AND d.requested_at<=$3
 AND ($4::bigint IS NULL OR (d.requested_at,d.decision_id COLLATE "C")>($4,$5 COLLATE "C"))
 ORDER BY d.requested_at,d.decision_id COLLATE "C" LIMIT $6
), encoded AS MATERIALIZED (
 SELECT decision_id,requested_at,jsonb_build_object('decisionId',decision_id,'requestedAt',requested_at,'requestEventId',request_event_id,'recordedAt',recorded_at,'digest',encode(payload_digest,'hex'),'event',event) AS record FROM selected
), sized AS (
 SELECT *,sum(octet_length(record::text)) OVER(ORDER BY requested_at,decision_id COLLATE "C") AS size FROM encoded
) SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off',
 (SELECT count(*)::int FROM selected),COALESCE(jsonb_agg(record ORDER BY requested_at,decision_id COLLATE "C"),'[]'::jsonb) FROM sized WHERE size<=4194304`, a.product, a.environment, cutoff, at, id, limit+1).Scan(&primary, &total, &raw)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if !primary {
		return nil, contract.ErrUnavailable
	}
	var rows []pendingDecisionRow
	if json.Unmarshal(raw, &rows) != nil || rows == nil || total < 0 || total > limit+1 || len(rows) > total || (total > 0 && len(rows) == 0) {
		return nil, contract.ErrAuditUnavailable
	}
	items := make([]PendingDecision, 0, len(rows))
	for _, row := range rows {
		digest, err := hex.DecodeString(row.Digest)
		if !valid(row.DecisionID, 128) || !valid(row.RequestEventID, 128) || row.RequestedAt < 0 || row.RequestedAt > cutoff || row.RecordedAt < 0 || row.RecordedAt > maxSafe || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != row.Digest || (previous != nil && (row.RequestedAt < previous.RequestedAt || (row.RequestedAt == previous.RequestedAt && row.DecisionID <= previous.DecisionID))) {
			return nil, contract.ErrAuditUnavailable
		}
		request, e := a.decodeStored(row.Event, row.RequestEventID, row.RecordedAt, row.Digest)
		if e != nil {
			return nil, e
		}
		b, e := contract.BindAuditDecision(a.product, a.environment, []map[string]any{request})
		if e != nil {
			return nil, e
		}
		if b.DecisionID != row.DecisionID || b.RequestedAt != row.RequestedAt {
			return nil, contract.ErrAuditUnavailable
		}
		items = append(items, PendingDecision{row.DecisionID, row.RequestedAt, request})
		previous = &AuditDecisionCursor{a.product, a.environment, cutoff, row.RequestedAt, row.DecisionID}
	}
	more := total > limit || total > len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	var next *AuditDecisionCursor
	if more {
		last := items[len(items)-1]
		next = &AuditDecisionCursor{a.product, a.environment, cutoff, last.RequestedAt, last.DecisionID}
	}
	return &AuditDecisionPage{items, next}, nil
}
