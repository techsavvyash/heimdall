package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type AuditBackfillProgress struct {
	Phase         string  `json:"phase"`
	After         *string `json:"after"`
	ScannedCount  int64   `json:"scannedCount"`
	ExpectedCount *int64  `json:"expectedCount"`
	Processed     int     `json:"processed"`
}
type backfillEvent struct {
	EventID    string          `json:"event_id"`
	RecordedAt string          `json:"recorded_at"`
	Digest     string          `json:"payload_digest"`
	Event      json.RawMessage `json:"event"`
}

// BackfillDecisionPage is deployment-only; it never installs schema or publishes readiness.
func (a *Audit) BackfillDecisionPage(ctx context.Context, limit int) (*AuditBackfillProgress, error) {
	if ctx == nil || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	conn, e := a.db.Conn(ctx)
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
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		return nil, databaseError(ctx)
	}
	var permitted bool
	if e = tx.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AND (SELECT count(*) FROM heimdall.schema_metadata)=1 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=1 AND state='migrating') AND (SELECT count(*) FROM heimdall.migrations)=1 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)", sessionChecksum).Scan(&permitted); e != nil || !permitted {
		return nil, databaseError(ctx)
	}
	var p AuditBackfillProgress
	var count string
	var expected *string
	e = tx.QueryRowContext(ctx, "SELECT phase,last_event_id,scanned_count::text,expected_count::text FROM heimdall.audit_decision_backfill WHERE product_id=$1 AND environment=$2 FOR UPDATE", a.product, a.environment).Scan(&p.Phase, &p.After, &count, &expected)
	if e != nil {
		return nil, databaseError(ctx)
	}
	n, ok := workspaceDecimal(&count, maxSafe, false)
	if !ok {
		return nil, contract.ErrAuditUnavailable
	}
	p.ScannedCount = n
	if expected != nil {
		v, ok := workspaceDecimal(expected, maxSafe, false)
		if !ok {
			return nil, contract.ErrAuditUnavailable
		}
		p.ExpectedCount = &v
	}
	if (p.Phase != "requests" && p.Phase != "outcomes" && p.Phase != "complete") || (p.After != nil && !valid(*p.After, 128)) || ((p.Phase == "requests") != (p.ExpectedCount == nil)) || (p.Phase == "complete" && (p.After != nil || p.ScannedCount != *p.ExpectedCount)) {
		return nil, contract.ErrAuditUnavailable
	}
	if p.Phase != "complete" && ((p.After == nil) != (p.ScannedCount == 0)) {
		return nil, contract.ErrAuditUnavailable
	}
	if p.Phase != "complete" {
		var raw []byte
		var total int
		e = tx.QueryRowContext(ctx, `WITH selected AS MATERIALIZED (
 SELECT event_id,recorded_at,payload_digest,event FROM heimdall.audit_events
 WHERE product_id=$1 AND environment=$2 AND ($3::text IS NULL OR event_id COLLATE "C">$3 COLLATE "C") ORDER BY event_id COLLATE "C" LIMIT $4
), sized AS (SELECT *,sum(octet_length(event::text)) OVER(ORDER BY event_id COLLATE "C") AS size FROM selected)
 SELECT (SELECT count(*)::int FROM selected) AS total,COALESCE(jsonb_agg(jsonb_build_object('event_id',event_id,'recorded_at',recorded_at::text,'payload_digest',encode(payload_digest,'hex'),'event',event) ORDER BY event_id COLLATE "C"),'[]'::jsonb) AS records FROM sized WHERE size<=4194304`, a.product, a.environment, p.After, limit).Scan(&total, &raw)
		if e != nil {
			return nil, databaseError(ctx)
		}
		var rows []backfillEvent
		if json.Unmarshal(raw, &rows) != nil || rows == nil || total < 0 || total > limit || len(rows) > total || (total > 0 && len(rows) == 0) {
			return nil, contract.ErrAuditUnavailable
		}
		for _, row := range rows {
			at, ok := workspaceDecimal(&row.RecordedAt, maxSafe, false)
			digest, err := hex.DecodeString(row.Digest)
			if !ok || !valid(row.EventID, 128) || (p.After != nil && row.EventID <= *p.After) || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != row.Digest {
				return nil, contract.ErrAuditUnavailable
			}
			event, err := a.decodeStored(row.Event, row.EventID, at, row.Digest)
			if err != nil {
				return nil, err
			}
			if p.Phase == "requests" && event["eventType"] == "heimdall.authorization.requested" {
				b, err := contract.BindAuditDecision(a.product, a.environment, []map[string]any{event})
				if err != nil {
					return nil, err
				}
				if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall.audit_decisions(product_id,environment,decision_id,request_event_id,requested_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", a.product, a.environment, b.DecisionID, b.RequestEventID, b.RequestedAt); e != nil {
					return nil, databaseError(ctx)
				}
				var requestID, requested, state string
				if e = tx.QueryRowContext(ctx, "SELECT request_event_id,requested_at::text,resolution FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2 AND decision_id=$3", a.product, a.environment, b.DecisionID).Scan(&requestID, &requested, &state); e != nil {
					if e == sql.ErrNoRows {
						return nil, contract.ErrAuditUnavailable
					}
					return nil, databaseError(ctx)
				}
				requestTime, ok := workspaceDecimal(&requested, maxSafe, false)
				if !ok || requestID != b.RequestEventID || requestTime != b.RequestedAt || state != "pending" {
					return nil, contract.ErrAuditUnavailable
				}
			}
			if p.Phase == "outcomes" && event["eventType"] == "heimdall.authorization.completed" {
				var requestID, requested, state, recorded string
				var terminal, resolved *string
				var storedDigest, storedEvent []byte
				e = tx.QueryRowContext(ctx, "SELECT d.request_event_id,d.requested_at::text,d.resolution,d.terminal_event_id,d.resolved_at::text,e.recorded_at::text,e.payload_digest,e.event FROM heimdall.audit_decisions d JOIN heimdall.audit_events e ON e.product_id=d.product_id AND e.environment=d.environment AND e.event_id=d.request_event_id WHERE d.product_id=$1 AND d.environment=$2 AND d.decision_id=$3 FOR UPDATE OF d", a.product, a.environment, event["decisionId"]).Scan(&requestID, &requested, &state, &terminal, &resolved, &recorded, &storedDigest, &storedEvent)
				if e == sql.ErrNoRows {
					return nil, contract.ErrAuditUnavailable
				}
				if e != nil {
					return nil, databaseError(ctx)
				}
				requestAt, ok := workspaceDecimal(&recorded, maxSafe, false)
				if !ok {
					return nil, contract.ErrAuditUnavailable
				}
				request, err := a.decodeStored(storedEvent, requestID, requestAt, hex.EncodeToString(storedDigest))
				if err != nil {
					return nil, err
				}
				b, err := contract.BindAuditDecision(a.product, a.environment, []map[string]any{request, event})
				if err != nil {
					return nil, err
				}
				requestTime, ok := workspaceDecimal(&requested, maxSafe, false)
				if !ok || requestTime != b.RequestedAt {
					return nil, contract.ErrAuditUnavailable
				}
				if state == "pending" {
					if terminal != nil || resolved != nil {
						return nil, contract.ErrAuditUnavailable
					}
					if _, e = tx.ExecContext(ctx, "UPDATE heimdall.audit_decisions SET resolution=$4,terminal_event_id=$5,resolved_at=$6 WHERE product_id=$1 AND environment=$2 AND decision_id=$3 AND resolution='pending'", a.product, a.environment, b.DecisionID, b.Resolution, b.TerminalEventID, b.ResolvedAt); e != nil {
						return nil, databaseError(ctx)
					}
				} else {
					resolvedAt, ok := workspaceDecimal(resolved, maxSafe, false)
					if !ok || terminal == nil || state != b.Resolution || *terminal != *b.TerminalEventID || resolvedAt != *b.ResolvedAt {
						return nil, contract.ErrAuditUnavailable
					}
				}
			}
			id := row.EventID
			p.After = &id
		}
		p.Processed = len(rows)
		p.ScannedCount += int64(p.Processed)
		if p.ScannedCount > maxSafe || (p.Phase == "outcomes" && p.ScannedCount > *p.ExpectedCount) {
			return nil, contract.ErrAuditUnavailable
		}
		if p.Processed == 0 {
			p.After = nil
			if p.Phase == "requests" {
				p.Phase = "outcomes"
				v := p.ScannedCount
				p.ExpectedCount = &v
				p.ScannedCount = 0
			} else {
				if p.ScannedCount != *p.ExpectedCount {
					return nil, contract.ErrAuditUnavailable
				}
				p.Phase = "complete"
			}
		}
		if _, e = tx.ExecContext(ctx, "UPDATE heimdall.audit_decision_backfill SET phase=$3,last_event_id=$4,scanned_count=$5,expected_count=$6 WHERE product_id=$1 AND environment=$2", a.product, a.environment, p.Phase, p.After, p.ScannedCount, p.ExpectedCount); e != nil {
			return nil, databaseError(ctx)
		}
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, contract.ErrOutcomeUnknown
	}
	return &p, nil
}
