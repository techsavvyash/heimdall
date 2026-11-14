package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type CapacityBackfillCursor struct {
	ProductID   string `json:"productId"`
	Environment string `json:"environment"`
	ID          string `json:"id"`
}
type CapacityBackfillProgress struct {
	Phase            string                  `json:"phase"`
	After            *CapacityBackfillCursor `json:"after"`
	ScannedEvents    int64                   `json:"scannedEvents"`
	ScannedDecisions int64                   `json:"scannedDecisions"`
	Processed        int                     `json:"processed"`
}

// AdvanceAuditCapacityUpgrade validates and accounts one bounded historical page.
// All writers remain externally drained. Coverage completion is not activation.
func AdvanceAuditCapacityUpgrade(ctx context.Context, db *sql.DB, limit int, writersDrained bool, validate func(map[string]any) error) (*CapacityBackfillProgress, error) {
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
	if e = checkPreparedAuditCapacity(ctx, tx); e != nil {
		return nil, e
	}
	var p CapacityBackfillProgress
	var product, environment, id *string
	if e = tx.QueryRowContext(ctx, "SELECT phase,after_product,after_environment,after_id,scanned_events,scanned_decisions FROM heimdall_capacity.backfill WHERE singleton FOR UPDATE").Scan(&p.Phase, &product, &environment, &id, &p.ScannedEvents, &p.ScannedDecisions); e != nil {
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
	switch p.Phase {
	case "events":
		if p.ScannedDecisions != 0 || ((id == nil) != (p.ScannedEvents == 0)) {
			return nil, contract.ErrAuditUnavailable
		}
		e = capacityEventPage(ctx, db, tx, &p, limit, validate)
	case "decisions":
		if (id == nil) != (p.ScannedDecisions == 0) {
			return nil, contract.ErrAuditUnavailable
		}
		e = capacityDecisionPage(ctx, db, tx, &p, limit, validate)
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
	if p.Phase == "complete" {
		var covered bool
		if e = tx.QueryRowContext(ctx, capacityCoverageSQL, p.ScannedEvents, p.ScannedDecisions).Scan(&covered); e != nil {
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
	if _, e = tx.ExecContext(ctx, "UPDATE heimdall_capacity.backfill SET phase=$1,after_product=$2,after_environment=$3,after_id=$4,scanned_events=$5,scanned_decisions=$6 WHERE singleton", p.Phase, product, environment, id, p.ScannedEvents, p.ScannedDecisions); e != nil {
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
func capacityCursorArgs(p *CapacityBackfillProgress, limit int) []any {
	c := CapacityBackfillCursor{}
	if p.After != nil {
		c = *p.After
	}
	return []any{c.ProductID, c.Environment, c.ID, limit}
}
func capacityAfter(p *CapacityBackfillProgress, product, environment, id string) bool {
	if !valid(product, 64) || !valid(environment, 64) || !valid(id, 128) {
		return false
	}
	if p.After == nil {
		return true
	}
	c := p.After
	return product > c.ProductID || (product == c.ProductID && (environment > c.Environment || (environment == c.Environment && id > c.ID)))
}
func capacityEventPage(ctx context.Context, db *sql.DB, tx *sql.Tx, p *CapacityBackfillProgress, limit int, validate func(map[string]any) error) error {
	var raw []byte
	var total int
	e := tx.QueryRowContext(ctx, `WITH selected AS MATERIALIZED (
 SELECT product_id,environment,event_id,recorded_at,payload_digest,event FROM heimdall.audit_events
 WHERE (product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C")>($1 COLLATE "C",$2 COLLATE "C",$3 COLLATE "C")
 ORDER BY product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C" LIMIT $4
 ), sized AS (SELECT *,sum(octet_length(event::text)) OVER(ORDER BY product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C") AS size FROM selected)
 SELECT (SELECT count(*)::int FROM selected),COALESCE(jsonb_agg(jsonb_build_object('product',product_id,'environment',environment,'event_id',event_id,'recorded_at',recorded_at::text,'payload_digest',encode(payload_digest,'hex'),'event',event) ORDER BY product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C"),'[]'::jsonb) FROM sized WHERE size<=4194304`, capacityCursorArgs(p, limit)...).Scan(&total, &raw)
	if e != nil {
		return databaseError(ctx)
	}
	var rows []activationEvent
	if json.Unmarshal(raw, &rows) != nil || rows == nil || total < 0 || total > limit || len(rows) > total || (total > 0 && len(rows) == 0) {
		return contract.ErrAuditUnavailable
	}
	for _, row := range rows {
		if !capacityAfter(p, row.Product, row.Environment, row.EventID) {
			return contract.ErrAuditUnavailable
		}
		a, e := NewAudit(db, row.Product, row.Environment, nil, validate)
		if e != nil {
			return contract.ErrAuditUnavailable
		}
		event, e := a.decodeCapacityEvent(&row.backfillEvent)
		if e != nil {
			return e
		}
		if event["eventType"] == "heimdall.authorization.requested" || event["eventType"] == "heimdall.authorization.completed" {
			// Correlation of the full request/outcome pair happens in the decision pass.
			// Reject orphan or additional authorization records without fetching more
			// payloads outside this page's byte budget.
			var bound bool
			if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2 AND decision_id=$3 AND (($4 AND request_event_id=$5) OR (NOT $4 AND terminal_event_id=$5)))", row.Product, row.Environment, event["decisionId"], event["eventType"] == "heimdall.authorization.requested", row.EventID).Scan(&bound); e != nil {
				return databaseError(ctx)
			}
			if !bound {
				return contract.ErrAuditUnavailable
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", row.Product, row.Environment, row.EventID); e != nil {
			return auditCapacityError(ctx, e)
		}
		if e = a.chargeAuditEvent(ctx, tx, row.EventID, false); e != nil {
			return e
		}
		p.After = &CapacityBackfillCursor{row.Product, row.Environment, row.EventID}
	}
	p.Processed = len(rows)
	p.ScannedEvents += int64(len(rows))
	if len(rows) == 0 {
		var covered bool
		if e = tx.QueryRowContext(ctx, capacityEventCoverageSQL, p.ScannedEvents).Scan(&covered); e != nil {
			return databaseError(ctx)
		}
		if !covered {
			return contract.ErrAuditUnavailable
		}
		p.Phase = "decisions"
		p.After = nil
	}
	return nil
}
func (a *Audit) decodeCapacityEvent(row *backfillEvent) (map[string]any, error) {
	if row == nil || !valid(row.EventID, 128) {
		return nil, contract.ErrAuditUnavailable
	}
	at, ok := workspaceDecimal(&row.RecordedAt, maxSafe, false)
	digest, e := hex.DecodeString(row.Digest)
	if !ok || e != nil || len(digest) != 32 || hex.EncodeToString(digest) != row.Digest {
		return nil, contract.ErrAuditUnavailable
	}
	return a.decodeStored(row.Event, row.EventID, at, row.Digest)
}

type capacityDecisionRow struct {
	Product     string         `json:"product"`
	Environment string         `json:"environment"`
	ID          string         `json:"id"`
	RequestedAt string         `json:"requestedAt"`
	Resolution  string         `json:"resolution"`
	TerminalID  *string        `json:"terminalId"`
	ResolvedAt  *string        `json:"resolvedAt"`
	Request     *backfillEvent `json:"request"`
	Terminal    *backfillEvent `json:"terminal"`
}

func capacityDecisionPage(ctx context.Context, db *sql.DB, tx *sql.Tx, p *CapacityBackfillProgress, limit int, validate func(map[string]any) error) error {
	var raw []byte
	var total int
	e := tx.QueryRowContext(ctx, `WITH selected AS MATERIALIZED (
 SELECT * FROM heimdall.audit_decisions
 WHERE (product_id COLLATE "C",environment COLLATE "C",decision_id COLLATE "C")>($1 COLLATE "C",$2 COLLATE "C",$3 COLLATE "C")
 ORDER BY product_id COLLATE "C",environment COLLATE "C",decision_id COLLATE "C" LIMIT $4
 ), encoded AS MATERIALIZED (
 SELECT d.product_id,d.environment,d.decision_id,
 coalesce(octet_length(r.event::text),0)+coalesce(octet_length(t.event::text),0)+CASE WHEN d.resolution='recovered' THEN 1500000 ELSE 0 END AS bytes,
 jsonb_build_object('product',d.product_id,'environment',d.environment,'id',d.decision_id,'requestedAt',d.requested_at::text,'resolution',d.resolution,'terminalId',d.terminal_event_id,'resolvedAt',d.resolved_at::text,
 'request',CASE WHEN r.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',r.event_id,'recorded_at',r.recorded_at::text,'payload_digest',encode(r.payload_digest,'hex'),'event',r.event) END,
 'terminal',CASE WHEN t.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',t.event_id,'recorded_at',t.recorded_at::text,'payload_digest',encode(t.payload_digest,'hex'),'event',t.event) END) AS record
 FROM selected d LEFT JOIN heimdall.audit_events r ON r.product_id=d.product_id AND r.environment=d.environment AND r.event_id=d.request_event_id
 LEFT JOIN heimdall.audit_events t ON t.product_id=d.product_id AND t.environment=d.environment AND t.event_id=d.terminal_event_id
 ), sized AS (SELECT *,sum(bytes) OVER(ORDER BY product_id COLLATE "C",environment COLLATE "C",decision_id COLLATE "C") AS size FROM encoded)
 SELECT (SELECT count(*)::int FROM selected),COALESCE(jsonb_agg(record ORDER BY product_id COLLATE "C",environment COLLATE "C",decision_id COLLATE "C"),'[]'::jsonb) FROM sized WHERE size<=6291456`, capacityCursorArgs(p, limit)...).Scan(&total, &raw)
	if e != nil {
		return databaseError(ctx)
	}
	var rows []capacityDecisionRow
	if json.Unmarshal(raw, &rows) != nil || rows == nil || total < 0 || total > limit || len(rows) > total || (total > 0 && len(rows) == 0) {
		return contract.ErrAuditUnavailable
	}
	for _, row := range rows {
		if !capacityAfter(p, row.Product, row.Environment, row.ID) {
			return contract.ErrAuditUnavailable
		}
		a, e := NewAudit(db, row.Product, row.Environment, nil, validate)
		if e != nil {
			return contract.ErrAuditUnavailable
		}
		request, e := a.decodeCapacityEvent(row.Request)
		if e != nil {
			return e
		}
		events := []map[string]any{request}
		var terminal map[string]any
		if row.TerminalID != nil {
			terminal, e = a.decodeCapacityEvent(row.Terminal)
			if e != nil {
				return e
			}
			events = append(events, terminal)
		} else if row.Terminal != nil {
			return contract.ErrAuditUnavailable
		}
		b, e := contract.BindAuditDecision(row.Product, row.Environment, events)
		requested, ok := workspaceDecimal(&row.RequestedAt, maxSafe, false)
		if e != nil || !ok || b.DecisionID != row.ID || b.RequestEventID != row.Request.EventID || b.RequestedAt != requested || b.Resolution != row.Resolution || (b.TerminalEventID == nil) != (row.TerminalID == nil) || (row.TerminalID != nil && *b.TerminalEventID != *row.TerminalID) || (b.ResolvedAt == nil) != (row.ResolvedAt == nil) {
			return contract.ErrAuditUnavailable
		}
		if row.ResolvedAt != nil {
			resolved, ok := workspaceDecimal(row.ResolvedAt, maxSafe, false)
			if !ok || resolved != *b.ResolvedAt {
				return contract.ErrAuditUnavailable
			}
		}
		var recoveryID *string
		if b.Resolution == "recovered" {
			id := contract.RecoveryEventID(row.Product, row.Environment, *b.TerminalEventID)
			recoveryID = &id
			admin := backfillEvent{EventID: id}
			if e = tx.QueryRowContext(ctx, "SELECT recorded_at::text,encode(payload_digest,'hex'),event FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=$3", row.Product, row.Environment, id).Scan(&admin.RecordedAt, &admin.Digest, &admin.Event); e != nil {
				if e == sql.ErrNoRows {
					return contract.ErrAuditUnavailable
				}
				return databaseError(ctx)
			}
			event, e := a.decodeCapacityEvent(&admin)
			if e != nil {
				return e
			}
			if e = contract.ValidateRecoveryPair(terminal, event, requested); e != nil {
				return contract.ErrAuditUnavailable
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_capacity.reservations(product_id,environment,decision_id,request_event_id,resolution,terminal_event_id,recovery_event_id) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING", row.Product, row.Environment, row.ID, b.RequestEventID, b.Resolution, b.TerminalEventID, recoveryID); e != nil {
			return auditCapacityError(ctx, e)
		}
		// Verify every nullable binding on a pre-existing import, not only its request.
		var matching bool
		if e = tx.QueryRowContext(ctx, "SELECT request_event_id=$4 AND resolution=$5 AND terminal_event_id IS NOT DISTINCT FROM $6::text AND recovery_event_id IS NOT DISTINCT FROM $7::text FROM heimdall_capacity.reservations WHERE product_id=$1 AND environment=$2 AND decision_id=$3", row.Product, row.Environment, row.ID, b.RequestEventID, b.Resolution, b.TerminalEventID, recoveryID).Scan(&matching); e != nil {
			return databaseError(ctx)
		}
		if !matching {
			return contract.ErrAuditUnavailable
		}
		p.After = &CapacityBackfillCursor{row.Product, row.Environment, row.ID}
	}
	p.Processed = len(rows)
	p.ScannedDecisions += int64(len(rows))
	if len(rows) == 0 {
		p.Phase = "complete"
		p.After = nil
	}
	return nil
}

const capacityEventCoverageSQL = `SELECT (SELECT count(*) FROM heimdall.audit_events)=$1
 AND (SELECT count(*) FROM heimdall_capacity.event_charges)=$1
 AND NOT EXISTS(SELECT 1 FROM heimdall.audit_events e LEFT JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id)
 WHERE c.event_id IS NULL OR c.envelope_bytes<>octet_length(e.event::text) OR c.delivery_rows<>(SELECT count(*) FROM heimdall.audit_deliveries d WHERE d.product_id=e.product_id AND d.environment=e.environment AND d.event_id=e.event_id))`
const capacityCoverageSQL = capacityEventCoverageSQL + `
 AND (SELECT count(*) FROM heimdall.audit_decisions)=$2 AND (SELECT count(*) FROM heimdall_capacity.reservations)=$2
 AND NOT EXISTS(SELECT 1 FROM heimdall.audit_decisions d LEFT JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id)
 WHERE r.decision_id IS NULL OR r.request_event_id<>d.request_event_id OR r.resolution<>d.resolution OR r.terminal_event_id IS DISTINCT FROM d.terminal_event_id)
 AND EXISTS(SELECT 1 FROM heimdall_capacity.policy WHERE singleton AND used_events=$1
 AND used_bytes=(SELECT coalesce(sum(envelope_bytes),0) FROM heimdall_capacity.event_charges)
 AND used_deliveries=(SELECT coalesce(sum(delivery_rows),0) FROM heimdall_capacity.event_charges)
 AND reserved_events=2*(SELECT count(*) FROM heimdall.audit_decisions WHERE resolution='pending')
 AND reserved_bytes=3000000::numeric*(SELECT count(*) FROM heimdall.audit_decisions WHERE resolution='pending')
 AND reserved_deliveries=64::numeric*(SELECT count(*) FROM heimdall.audit_decisions WHERE resolution='pending'))`
