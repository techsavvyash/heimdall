package postgres

import (
	"context"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type RetainedEventInspection struct {
	State    string
	Identity contract.RetainedIdentity
	Event    map[string]any
}

type retainedRow struct {
	Ready    bool            `json:"ready"`
	Present  bool            `json:"present"`
	Live     bool            `json:"live"`
	Markers  int             `json:"markers"`
	Tenant   *string         `json:"tenant_id"`
	Identity json.RawMessage `json:"identity"`
	Payload  json.RawMessage `json:"payload"`
	Valid    bool            `json:"valid"`
}

// InspectRetainedEvent is prepared primary inspection, never fresh authorization.
func (a *Audit) InspectRetainedEvent(ctx context.Context, tenant *string, eventID string) (*RetainedEventInspection, error) {
	return a.inspectRetainedHistory(ctx, tenant, eventID, true)
}

// ResolveRetainedReceipt compares an immutable retry; it never recreates payloads.
func (a *Audit) ResolveRetainedReceipt(ctx context.Context, event map[string]any) (*contract.AuditReceipt, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	p, e := a.prepare(event)
	if e != nil {
		return nil, e
	}
	var tenant *string
	if value, ok := p.snapshot["tenantId"].(string); ok {
		tenant = &value
	}
	result, e := a.inspectRetainedHistory(ctx, tenant, p.receipt.EventID, false)
	if e != nil || result == nil {
		return nil, e
	}
	if result.Identity.Digest != p.receipt.PayloadDigest || result.Identity.RecordedAt != p.receipt.RecordedAt {
		return nil, contract.ErrConflict
	}
	return &p.receipt, nil
}
func (a *Audit) inspectRetainedHistory(ctx context.Context, tenant *string, eventID string, filterTenant bool) (*RetainedEventInspection, error) {
	if ctx == nil || (tenant != nil && !valid(*tenant, 128)) || !valid(eventID, 128) {
		return nil, contract.ErrInvalidArgument
	}
	var expectedTenant *string
	if tenant != nil {
		v := *tenant
		expectedTenant = &v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var row retainedRow
	var identityRaw, payload []byte
	e := a.db.QueryRowContext(ctx, a.retainedHistorySQL(retainedReadSQL), a.retainedHistoryArgs(eventID)...).Scan(&row.Ready, &row.Present, &row.Live, &row.Markers, &row.Tenant, &identityRaw, &payload, &row.Valid)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	row.Identity, row.Payload = identityRaw, payload
	return a.decodeRetainedRow(row, expectedTenant, eventID, filterTenant)
}
func (a *Audit) decodeRetainedRow(row retainedRow, expectedTenant *string, eventID string, filterTenant bool) (*RetainedEventInspection, error) {
	ready, present, live, consistent := row.Ready, row.Present, row.Live, row.Valid
	markers, storedTenant, identityRaw, payload := row.Markers, row.Tenant, row.Identity, row.Payload
	if !ready {
		return nil, contract.ErrUnavailable
	}
	if !present {
		if live || markers != 0 {
			return nil, contract.ErrAuditUnavailable
		}
		return nil, nil
	}
	if filterTenant && !sameText(storedTenant, expectedTenant) {
		return nil, nil
	}
	expectedMarkers := 1
	if live {
		expectedMarkers = 0
	}
	if !consistent || markers != expectedMarkers {
		return nil, contract.ErrAuditUnavailable
	}
	var identity contract.RetainedIdentity
	if json.Unmarshal(identityRaw, &identity) != nil {
		return nil, contract.ErrAuditUnavailable
	}
	result := &RetainedEventInspection{State: "retired", Identity: identity}
	if !live {
		return result, nil
	}
	event, e := a.decodeStored(payload, eventID, identity.RecordedAt, identity.Digest)
	if e != nil {
		return nil, e
	}
	kind := "ordinary"
	switch event["eventType"] {
	case "heimdall.authorization.requested":
		kind = "request"
	case "heimdall.authorization.completed":
		kind = "terminal"
	case "heimdall.recovery.completed":
		if event["action"] == "authorization.recover" {
			kind = "recovery"
		}
	}
	var eventTenant *string
	if v, ok := event["tenantId"].(string); ok {
		eventTenant = &v
	}
	if !sameText(eventTenant, storedTenant) || kind != identity.Kind || (kind != "ordinary" && (identity.DecisionID == nil || event["decisionId"] != *identity.DecisionID)) {
		return nil, contract.ErrAuditUnavailable
	}
	result.State = "live"
	result.Event = event
	return result, nil
}

const retainedReadSQL = `SELECT readiness.ready, i.event_id IS NOT NULL AS present, e.event_id IS NOT NULL AS live,
 coalesce(m.marker_count,0) AS markers,
 i.tenant_id,
 CASE WHEN i.event_id IS NULL THEN NULL ELSE jsonb_build_object('productId',i.product_id,'environment',i.environment,'tenantId',i.tenant_id,'eventId',i.event_id,'recordedAt',i.recorded_at,'digest',encode(i.payload_digest,'hex'),'kind',i.kind,'decisionId',i.decision_id,'routing',i.routing) END AS identity,
 e.event AS payload,
 coalesce(
  (i.kind='ordinary' OR (d.tenant_id IS NOT DISTINCT FROM i.tenant_id AND CASE i.kind WHEN 'request' THEN d.request_event_id=i.event_id WHEN 'terminal' THEN d.terminal_event_id=i.event_id WHEN 'recovery' THEN d.recovery_event_id=i.event_id ELSE false END))
  AND CASE WHEN e.event_id IS NOT NULL THEN
   m.retirement_id IS NULL AND e.recorded_at=i.recorded_at AND e.payload_digest=i.payload_digest
   AND c.envelope_bytes=octet_length(e.event::text) AND c.delivery_rows=jsonb_array_length(i.routing)
   AND i.routing=to_jsonb(ARRAY(SELECT sink_id FROM heimdall.audit_deliveries x WHERE x.product_id=$5 AND x.environment=$6 AND x.event_id=$7 ORDER BY sink_id COLLATE "C"))
  ELSE
   m.retirement_id IS NOT NULL AND m.tenant_id IS NOT DISTINCT FROM i.tenant_id
   AND m.target_event_id=CASE WHEN i.kind='ordinary' THEN i.event_id ELSE d.request_event_id END
   AND (i.kind='ordinary' OR d.resolution IN ('completed','recovered'))
   AND m.member_event_ids=CASE WHEN i.kind='ordinary' THEN jsonb_build_array(i.event_id) ELSE
    to_jsonb(ARRAY(SELECT id FROM unnest(array_remove(ARRAY[d.request_event_id,d.terminal_event_id,d.recovery_event_id],NULL)) id ORDER BY id COLLATE "C")) END
   AND m.retired_at>=i.recorded_at+m.retain_for_ms
   AND EXISTS(SELECT 1 FROM heimdall_retention.event_identities a WHERE a.product_id=$5 AND a.environment=$6 AND a.event_id=m.administration_event_id AND a.tenant_id IS NOT DISTINCT FROM i.tenant_id AND a.recorded_at=m.retired_at)
   AND NOT EXISTS(SELECT 1 FROM heimdall.audit_events remaining WHERE remaining.product_id=$5 AND remaining.environment=$6
    AND remaining.event_id=ANY(ARRAY[i.event_id,d.request_event_id,d.terminal_event_id,d.recovery_event_id]))
  END,false) AS valid
FROM (` + capacityReadinessSQL + `) readiness
LEFT JOIN heimdall_retention.event_identities i ON i.product_id=$5 AND i.environment=$6 AND i.event_id=$7
LEFT JOIN heimdall.audit_events e ON e.product_id=$5 AND e.environment=$6 AND e.event_id=$7
LEFT JOIN heimdall_capacity.event_charges c ON c.product_id=$5 AND c.environment=$6 AND c.event_id=$7
LEFT JOIN heimdall_retention.decision_identities d ON d.product_id=i.product_id AND d.environment=i.environment AND d.decision_id=i.decision_id
LEFT JOIN LATERAL (
 SELECT matches.*, count(*) OVER ()::int AS marker_count
 FROM (SELECT r.* FROM heimdall_retention.retirements r WHERE r.product_id=$5 AND r.environment=$6 AND r.member_event_ids ? $7 LIMIT 2) matches
 LIMIT 1
) m ON true
`
