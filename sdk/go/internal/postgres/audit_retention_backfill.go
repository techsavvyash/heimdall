package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// AdvanceAuditRetentionUpgrade validates and retains one bounded historical page.
// All writers remain externally drained. Coverage completion is not activation.
func AdvanceAuditRetentionUpgrade(ctx context.Context, db *sql.DB, limit int, writersDrained bool, validate func(map[string]any) error) (*CapacityBackfillProgress, error) {
	if ctx == nil || db == nil || limit < 1 || limit > 100 || !writersDrained || validate == nil {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		return nil, databaseError(ctx)
	}
	defer conn.Close()
	var primary bool
	if e = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&primary); e != nil || !primary {
		return nil, databaseError(ctx)
	}
	tx, e := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='5s'; SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		return nil, databaseError(ctx)
	}
	if e = checkPreparedAuditRetention(ctx, tx); e != nil {
		return nil, e
	}
	var p CapacityBackfillProgress
	var product, environment, id *string
	if e = tx.QueryRowContext(ctx, "SELECT phase,after_product,after_environment,after_id,scanned_events,scanned_decisions FROM heimdall_retention.backfill WHERE singleton FOR UPDATE").Scan(&p.Phase, &product, &environment, &id, &p.ScannedEvents, &p.ScannedDecisions); e != nil {
		return nil, databaseError(ctx)
	}
	if p.ScannedEvents < 0 || p.ScannedEvents > maxSafe || p.ScannedDecisions < 0 || p.ScannedDecisions > maxSafe || ((product == nil) != (id == nil)) || ((environment == nil) != (id == nil)) {
		return nil, contract.ErrAuditUnavailable
	}
	if id != nil {
		if !valid(*product, 64) || !valid(*environment, 64) || !valid(*id, 128) {
			return nil, contract.ErrAuditUnavailable
		}
		p.After = &CapacityBackfillCursor{*product, *environment, *id}
	}
	var accounted bool
	if e = tx.QueryRowContext(ctx, retentionProgressSQL, p.ScannedEvents, p.ScannedDecisions).Scan(&accounted); e != nil {
		return nil, databaseError(ctx)
	}
	if !accounted {
		return nil, contract.ErrAuditUnavailable
	}
	switch p.Phase {
	case "ordinary":
		if p.ScannedDecisions != 0 || ((id == nil) != (p.ScannedEvents == 0)) {
			return nil, contract.ErrAuditUnavailable
		}
		e = retentionGroupPage(ctx, db, tx, &p, limit, validate, true)
	case "decisions":
		if (id == nil) != (p.ScannedDecisions == 0) {
			return nil, contract.ErrAuditUnavailable
		}
		e = retentionGroupPage(ctx, db, tx, &p, limit, validate, true)
	case "complete":
		if id != nil {
			return nil, contract.ErrAuditUnavailable
		}
	default:
		return nil, contract.ErrAuditUnavailable
	}
	if e != nil {
		return nil, e
	}
	if p.ScannedEvents > maxSafe || p.ScannedDecisions > maxSafe {
		return nil, contract.ErrAuditUnavailable
	}
	if e = tx.QueryRowContext(ctx, retentionProgressSQL, p.ScannedEvents, p.ScannedDecisions).Scan(&accounted); e != nil {
		return nil, databaseError(ctx)
	}
	if !accounted {
		return nil, contract.ErrAuditUnavailable
	}
	if p.Phase == "complete" {
		var covered bool
		if e = tx.QueryRowContext(ctx, retentionCoverageSQL, p.ScannedEvents, p.ScannedDecisions).Scan(&covered); e != nil {
			return nil, databaseError(ctx)
		}
		if !covered {
			return nil, contract.ErrAuditUnavailable
		}
	}
	product, environment, id = nil, nil, nil
	if p.After != nil {
		product = &p.After.ProductID
		environment = &p.After.Environment
		id = &p.After.ID
	}
	if _, e = tx.ExecContext(ctx, "UPDATE heimdall_retention.backfill SET phase=$1,after_product=$2,after_environment=$3,after_id=$4,scanned_events=$5,scanned_decisions=$6 WHERE singleton", p.Phase, product, environment, id, p.ScannedEvents, p.ScannedDecisions); e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, contract.ErrOutcomeUnknown
	}
	return &p, nil
}

// Ordinary rows are independent groups; a decision always copies all its members.
const retentionOrdinarySQL = `SELECT e.product_id,e.environment,e.event_id AS id,'ordinary'::text AS resolution,ARRAY[e.event_id] AS ids
 FROM heimdall.audit_events e WHERE NOT EXISTS(SELECT 1 FROM heimdall.audit_decisions d
 WHERE d.product_id=e.product_id AND d.environment=e.environment AND e.event_id IN (d.request_event_id,d.terminal_event_id))
 AND NOT EXISTS(SELECT 1 FROM heimdall_capacity.reservations r WHERE r.product_id=e.product_id AND r.environment=e.environment
 AND e.event_id IN (r.request_event_id,r.terminal_event_id,r.recovery_event_id))`
const retentionDecisionSQL = `SELECT d.product_id,d.environment,d.decision_id AS id,d.resolution,
 array_remove(ARRAY[d.request_event_id,d.terminal_event_id,r.recovery_event_id],NULL) AS ids
 FROM heimdall.audit_decisions d LEFT JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id)`

type retentionGroupRow struct {
	Product     string   `json:"product"`
	Environment string   `json:"environment"`
	ID          string   `json:"id"`
	Resolution  string   `json:"resolution"`
	IDs         []string `json:"ids"`
}

func retentionGroupPage(ctx context.Context, db *sql.DB, tx *sql.Tx, p *CapacityBackfillProgress, limit int, validate func(map[string]any) error, write bool) error {
	source := retentionOrdinarySQL
	if p.Phase == "decisions" {
		source = retentionDecisionSQL
	}
	var raw []byte
	e := tx.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('product',product_id,'environment',environment,'id',id,'resolution',resolution,'ids',ids)
 ORDER BY product_id COLLATE "C",environment COLLATE "C",id COLLATE "C"),'[]'::jsonb) FROM (
 SELECT * FROM (`+source+`) source WHERE (product_id COLLATE "C",environment COLLATE "C",id COLLATE "C")>($1 COLLATE "C",$2 COLLATE "C",$3 COLLATE "C")
 ORDER BY product_id COLLATE "C",environment COLLATE "C",id COLLATE "C" LIMIT $4) selected`, capacityCursorArgs(p, limit)...).Scan(&raw)
	if e != nil {
		return databaseError(ctx)
	}
	var groups []retentionGroupRow
	if json.Unmarshal(raw, &groups) != nil || groups == nil || len(groups) > limit {
		return contract.ErrAuditUnavailable
	}
	bytes := int64(0)
	for _, g := range groups {
		if e = contract.CheckCancellation(ctx); e != nil {
			return e
		}
		if !capacityAfter(p, g.Product, g.Environment, g.ID) || len(g.IDs) < 1 || len(g.IDs) > 3 {
			return contract.ErrAuditUnavailable
		}
		seen := map[string]bool{}
		for _, id := range g.IDs {
			if !valid(id, 128) || seen[id] {
				return contract.ErrAuditUnavailable
			}
			seen[id] = true
		}
		// Count the representative twice because it is decoded to derive tenant scope
		// before the existing group writer reloads and validates every source member.
		var size int64
		var members int
		var bounded bool
		if e = tx.QueryRowContext(ctx, `SELECT count(*)::int,coalesce(sum(octet_length(event::text)),0)+coalesce(sum(CASE WHEN event_id=$4 THEN octet_length(event::text) ELSE 0 END),0),
 coalesce(bool_and(octet_length(event::text)<=1500000 AND (SELECT count(*) FROM heimdall.audit_deliveries d WHERE d.product_id=e.product_id AND d.environment=e.environment AND d.event_id=e.event_id)<=32),false)
 FROM heimdall.audit_events e WHERE product_id=$1 AND environment=$2 AND event_id=ANY($3::text[])`, g.Product, g.Environment, pq.Array(g.IDs), g.IDs[0]).Scan(&members, &size, &bounded); e != nil {
			return databaseError(ctx)
		}
		if members != len(g.IDs) || !bounded || size < 1 || size > 6000000 {
			return contract.ErrAuditUnavailable
		}
		if bytes+size > 8388608 {
			break
		}
		a, err := NewAudit(db, g.Product, g.Environment, nil, validate)
		if err != nil {
			return contract.ErrAuditUnavailable
		}
		row := backfillEvent{EventID: g.IDs[0]}
		if e = tx.QueryRowContext(ctx, "SELECT recorded_at::text,encode(payload_digest,'hex'),event FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=$3", g.Product, g.Environment, row.EventID).Scan(&row.RecordedAt, &row.Digest, &row.Event); e != nil {
			return databaseError(ctx)
		}
		event, err := a.decodeCapacityEvent(&row)
		if err != nil {
			return err
		}
		var tenant *string
		if v, ok := event["tenantId"].(string); ok {
			tenant = &v
		}
		identities, err := a.retentionIdentityGroupTx(ctx, tx, tenant, g.Resolution, g.IDs, write)
		if err != nil {
			return err
		}
		if len(identities) != len(g.IDs) {
			return contract.ErrAuditUnavailable
		}
		bytes += size
		p.Processed++
		p.ScannedEvents += int64(len(identities))
		if p.Phase == "decisions" {
			p.ScannedDecisions++
		}
		p.After = &CapacityBackfillCursor{g.Product, g.Environment, g.ID}
	}
	if len(groups) == 0 {
		if p.Phase == "ordinary" {
			var complete bool
			if e = tx.QueryRowContext(ctx, "SELECT count(*)=$1 FROM ("+retentionOrdinarySQL+") source", p.ScannedEvents).Scan(&complete); e != nil {
				return databaseError(ctx)
			}
			if !complete {
				return contract.ErrAuditUnavailable
			}
			p.Phase = "decisions"
		} else {
			p.Phase = "complete"
		}
		p.After = nil
	}
	return nil
}

const retentionProgressSQL = `SELECT
 (SELECT count(*) FROM heimdall_retention.event_identities)=$1
 AND (SELECT count(*) FROM heimdall_retention.decision_identities)=$2
 AND (SELECT count(*) FROM heimdall_retention.event_holds)=0
 AND (SELECT count(*) FROM heimdall_retention.retirements)=0
 AND EXISTS(SELECT 1 FROM heimdall_retention.capacity WHERE singleton AND used_rows=$1::numeric+$2::numeric
 AND reserved_rows=2*(SELECT count(*) FROM heimdall_retention.decision_identities WHERE resolution='pending'))`
const retentionCoverageSQL = capacityCoverageSQL + `
 AND (SELECT count(*) FROM heimdall_retention.event_identities)=$1
 AND (SELECT count(*) FROM heimdall_retention.decision_identities)=$2
 AND NOT EXISTS(SELECT 1 FROM heimdall.audit_events e LEFT JOIN heimdall_retention.event_identities i USING(product_id,environment,event_id)
 WHERE i.event_id IS NULL OR i.recorded_at<>e.recorded_at OR i.payload_digest<>e.payload_digest
 OR i.routing<>(SELECT coalesce(jsonb_agg(sink_id ORDER BY sink_id COLLATE "C"),'[]'::jsonb) FROM heimdall.audit_deliveries d WHERE d.product_id=e.product_id AND d.environment=e.environment AND d.event_id=e.event_id))
 AND NOT EXISTS(SELECT 1 FROM heimdall.audit_decisions d LEFT JOIN heimdall_retention.decision_identities i USING(product_id,environment,decision_id)
 LEFT JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id)
 WHERE i.decision_id IS NULL OR i.request_event_id<>d.request_event_id OR i.requested_at<>d.requested_at OR i.resolution<>d.resolution
 OR i.terminal_event_id IS DISTINCT FROM d.terminal_event_id OR i.resolved_at IS DISTINCT FROM d.resolved_at OR i.recovery_event_id IS DISTINCT FROM r.recovery_event_id)
 AND EXISTS(SELECT 1 FROM heimdall_retention.capacity WHERE singleton AND used_rows=$1::numeric+$2::numeric
 AND reserved_rows=2*(SELECT count(*) FROM heimdall.audit_decisions WHERE resolution='pending'))`
