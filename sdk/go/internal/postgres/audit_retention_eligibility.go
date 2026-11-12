package postgres

import (
	"context"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strings"
	"time"
)

// InspectRetentionEligibility reads one primary snapshot. A candidate is not purge authority.
// Empty result means unknown ID or a different tenant. Retired payload returns not_found.
func (a *Audit) InspectRetentionEligibility(ctx context.Context, tenant *string, eventID string, now, retainForMS int64) (string, error) {
	const max int64 = 9007199254740991
	if ctx == nil || (tenant != nil && !valid(*tenant, 128)) || !valid(eventID, 128) || now < 0 || now > max || retainForMS < 1 || retainForMS > max {
		return "", contract.ErrInvalidArgument
	}
	var expectedTenant *string
	if tenant != nil {
		v := *tenant
		expectedTenant = &v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return "", e
	}
	var rootRaw, membersRaw []byte
	var resolution *string
	var held, retry, bound bool
	e := a.db.QueryRowContext(ctx, a.retainedHistorySQL(retentionEligibilitySQL), a.retainedHistoryArgs(eventID)...).Scan(&rootRaw, &resolution, &membersRaw, &bound, &held, &retry)
	if e != nil {
		return "", databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return "", e
	}
	var row retainedRow
	if json.Unmarshal(rootRaw, &row) != nil {
		return "", contract.ErrAuditUnavailable
	}
	root, e := a.decodeRetainedRow(row, expectedTenant, eventID, true)
	if e != nil || root == nil {
		return "", e
	}
	if root.State == "retired" {
		return "", contract.ErrNotFound
	}
	if root.Identity.Kind != "ordinary" && root.Identity.Kind != "request" {
		return "", contract.ErrInvalidArgument
	}
	if resolution == nil || !bound {
		return "", contract.ErrAuditUnavailable
	}
	var members []struct {
		EventID    string                       `json:"eventId"`
		Row        retainedRow                  `json:"row"`
		Deliveries []contract.RetentionDelivery `json:"deliveries"`
	}
	if json.Unmarshal(membersRaw, &members) != nil {
		return "", contract.ErrAuditUnavailable
	}
	scope := contract.RetentionScope{ProductID: a.product, Environment: a.environment, TenantID: expectedTenant}
	sources := []contract.RetentionSource{}
	group := contract.RetentionGroup{Resolution: *resolution, HoldState: "clear", RetryState: "clear", Events: []contract.RetentionEvent{}}
	if held {
		group.HoldState = "held"
	}
	if retry {
		group.RetryState = "protected"
	}
	for _, m := range members {
		result, err := a.decodeRetainedRow(m.Row, expectedTenant, m.EventID, true)
		if err != nil {
			return "", err
		}
		if result == nil || result.State != "live" {
			return "", contract.ErrAuditUnavailable
		}
		i := result.Identity
		sources = append(sources, contract.RetentionSource{Event: result.Event, Digest: i.Digest, Routing: i.Routing, DeliveryRows: len(m.Deliveries)})
		group.Events = append(group.Events, contract.RetentionEvent{RetentionScope: scope, EventID: i.EventID, RecordedAt: i.RecordedAt, Deliveries: m.Deliveries})
	}
	if _, e = contract.BuildRetentionIdentities(scope, *resolution, sources); e != nil {
		return "", e
	}
	return contract.ClassifyAuditRetention(scope, group, now, retainForMS)
}

var eligibilityReadSQL = strings.ReplaceAll(retainedReadSQL, "e.event AS payload", "CASE WHEN octet_length(e.event::text)<=1500000 THEN e.event ELSE NULL END AS payload")
var retentionEligibilitySQL = `SELECT to_jsonb(root) AS root,
 CASE WHEN root.identity->>'kind'='ordinary' THEN 'ordinary' ELSE d.resolution END AS resolution,
 coalesce(group_rows.members,'[]'::jsonb) AS members,
 coalesce(root.identity->>'kind'='ordinary' OR (
  (d.request_event_id,d.requested_at,d.resolution,d.terminal_event_id,d.resolved_at) IS NOT DISTINCT FROM (source.request_event_id,source.requested_at,source.resolution,source.terminal_event_id,source.resolved_at)
  AND (d.request_event_id,d.resolution,d.terminal_event_id,d.recovery_event_id) IS NOT DISTINCT FROM (reservation.request_event_id,reservation.resolution,reservation.terminal_event_id,reservation.recovery_event_id)
  AND source.decision_id IS NOT NULL AND reservation.decision_id IS NOT NULL),false) AS bound,
 EXISTS(SELECT 1 FROM heimdall_retention.event_holds h WHERE h.product_id=$5 AND h.environment=$6 AND h.event_id=ANY(chosen.ids) AND h.purpose='administrative' AND h.released_at IS NULL) AS held,
 EXISTS(SELECT 1 FROM heimdall_retention.event_holds h WHERE h.product_id=$5 AND h.environment=$6 AND h.event_id=ANY(chosen.ids) AND h.purpose='retry' AND h.released_at IS NULL) AS retry
FROM (` + eligibilityReadSQL + `) root
LEFT JOIN heimdall_retention.decision_identities d ON d.product_id=$5 AND d.environment=$6 AND d.decision_id=root.identity->>'decisionId'
LEFT JOIN heimdall.audit_decisions source ON source.product_id=d.product_id AND source.environment=d.environment AND source.decision_id=d.decision_id
LEFT JOIN heimdall_capacity.reservations reservation ON reservation.product_id=d.product_id AND reservation.environment=d.environment AND reservation.decision_id=d.decision_id
CROSS JOIN LATERAL (SELECT CASE WHEN root.identity->>'kind'='ordinary' THEN ARRAY[$7::text] WHEN root.identity->>'kind'='request' THEN array_remove(ARRAY[d.request_event_id,d.terminal_event_id,d.recovery_event_id],NULL) ELSE ARRAY[]::text[] END AS ids) chosen
LEFT JOIN LATERAL (
 SELECT jsonb_agg(jsonb_build_object('eventId',member.event_id,'row',to_jsonb(history),'deliveries',coalesce(delivery.rows,'[]'::jsonb)) ORDER BY member.event_id COLLATE "C") AS members
 FROM unnest(chosen.ids) member(event_id)
 CROSS JOIN LATERAL (` + strings.ReplaceAll(eligibilityReadSQL, "$7", "member.event_id") + `) history
 LEFT JOIN LATERAL (SELECT jsonb_agg(jsonb_build_object('sinkId',sink_id,'state',state,'deliveredAt',delivered_at) ORDER BY sink_id COLLATE "C") AS rows FROM (SELECT sink_id,state,delivered_at FROM heimdall.audit_deliveries WHERE product_id=$5 AND environment=$6 AND event_id=member.event_id LIMIT 33) bounded) delivery ON true
) group_rows ON true
`
