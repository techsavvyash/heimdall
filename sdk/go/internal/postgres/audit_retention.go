package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// RetainIdentityGroup is a prepared deployment port, not a runtime retention API.
// Callers drain writers and install the pinned identity schema before invoking it.
func (a *Audit) RetainIdentityGroup(ctx context.Context, tenant *string, resolution string, eventIDs []string) ([]contract.RetainedIdentity, error) {
	if ctx == nil || (tenant != nil && !valid(*tenant, 128)) || len(eventIDs) < 1 || len(eventIDs) > 3 {
		return nil, contract.ErrInvalidArgument
	}
	ids := append([]string(nil), eventIDs...)
	seen := map[string]bool{}
	for _, id := range ids {
		if !valid(id, 128) || seen[id] {
			return nil, contract.ErrInvalidArgument
		}
		seen[id] = true
	}
	var tenantCopy *string
	if tenant != nil {
		v := *tenant
		tenantCopy = &v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	tx, e := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		return nil, databaseError(ctx)
	}
	if e = lockRuntimeCapacity(ctx, tx); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, "LOCK TABLE heimdall.audit_events,heimdall.audit_deliveries,heimdall.audit_decisions,heimdall_capacity.event_charges,heimdall_capacity.reservations IN SHARE MODE"); e != nil {
		return nil, databaseError(ctx)
	}
	sources := []contract.RetentionSource{}
	events := []map[string]any{}
	for _, id := range ids {
		var raw []byte
		var at int64
		var digest string
		var count int
		var routing pq.StringArray
		e = tx.QueryRowContext(ctx, `SELECT e.event,e.recorded_at,encode(e.payload_digest,'hex'),c.delivery_rows,ARRAY(SELECT sink_id FROM heimdall.audit_deliveries d WHERE d.product_id=e.product_id AND d.environment=e.environment AND d.event_id=e.event_id ORDER BY sink_id COLLATE "C") FROM heimdall.audit_events e JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id) WHERE e.product_id=$1 AND e.environment=$2 AND e.event_id=$3 AND octet_length(e.event::text)<=1500000 AND c.envelope_bytes=octet_length(e.event::text)`, a.product, a.environment, id).Scan(&raw, &at, &digest, &count, &routing)
		if e == sql.ErrNoRows {
			return nil, contract.ErrAuditUnavailable
		}
		if e != nil {
			return nil, databaseError(ctx)
		}
		event, err := a.decodeStored(raw, id, at, digest)
		if err != nil {
			return nil, err
		}
		sources = append(sources, contract.RetentionSource{Event: event, Digest: digest, Routing: []string(routing), DeliveryRows: count})
		if event["eventType"] != "heimdall.recovery.completed" {
			events = append(events, event)
		}
	}
	identities, e := contract.BuildRetentionIdentities(contract.RetentionScope{ProductID: a.product, Environment: a.environment, TenantID: tenantCopy}, resolution, sources)
	if e != nil {
		return nil, e
	}
	var recovery *string
	for _, i := range identities {
		routing, _ := json.Marshal(i.Routing)
		_, e = tx.ExecContext(ctx, "INSERT INTO heimdall_retention.event_identities(product_id,environment,event_id,tenant_id,recorded_at,payload_digest,kind,decision_id,routing) VALUES($1,$2,$3,$4,$5,decode($6,'hex'),$7,$8,$9::jsonb) ON CONFLICT(product_id,environment,event_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id,recorded_at=EXCLUDED.recorded_at,payload_digest=EXCLUDED.payload_digest,kind=EXCLUDED.kind,decision_id=EXCLUDED.decision_id,routing=EXCLUDED.routing", i.ProductID, i.Environment, i.EventID, i.TenantID, i.RecordedAt, i.Digest, i.Kind, i.DecisionID, string(routing))
		if e != nil {
			return nil, auditCapacityError(ctx, e)
		}
		if i.Kind == "recovery" {
			id := i.EventID
			recovery = &id
		}
	}
	if resolution != "ordinary" {
		b, err := contract.BindAuditDecision(a.product, a.environment, events)
		if err != nil {
			return nil, err
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO heimdall_retention.decision_identities(product_id,environment,decision_id,tenant_id,request_event_id,requested_at,resolution,terminal_event_id,recovery_event_id,resolved_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(product_id,environment,decision_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id,request_event_id=EXCLUDED.request_event_id,requested_at=EXCLUDED.requested_at,resolution=EXCLUDED.resolution,terminal_event_id=EXCLUDED.terminal_event_id,recovery_event_id=EXCLUDED.recovery_event_id,resolved_at=EXCLUDED.resolved_at", a.product, a.environment, b.DecisionID, tenantCopy, b.RequestEventID, b.RequestedAt, b.Resolution, b.TerminalEventID, recovery, b.ResolvedAt)
		if e != nil {
			return nil, auditCapacityError(ctx, e)
		}
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, contract.ErrOutcomeUnknown
	}
	return identities, nil
}
