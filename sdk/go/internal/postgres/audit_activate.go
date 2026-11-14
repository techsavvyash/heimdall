package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

const decisionChecksum = "f7e859c78a6d099ad4a4d35c96e553ba10a53e3ae7e23440237c769efcd50722"
const auditReadinessSQL = `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND (SELECT count(*) FROM heimdall.schema_metadata)=1
 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=2 AND state='ready' AND reader_min=2 AND reader_max=2 AND writer_min=2 AND writer_max=2)
 AND (SELECT count(*) FROM heimdall.migrations)=2
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=2 AND checksum=$2)
 AND to_regclass('heimdall.audit_decisions') IS NOT NULL AS ready`

// CheckAuditReadiness checks only the activated v2 protocol, without DDL.
func CheckAuditReadiness(ctx context.Context, db *sql.DB) error {
	return checkReadiness(ctx, db, auditReadinessSQL, sessionChecksum, decisionChecksum)
}

// ActivateAuditUpgrade is deployment-only. The caller keeps all writers and
// schema changes stopped. A deadline leaves migrating state intact; no partial
// readiness is published. validate must include the complete product registry.
func ActivateAuditUpgrade(ctx context.Context, db *sql.DB, writersDrained bool, validate func(map[string]any) error) error {
	if ctx == nil || db == nil || !writersDrained || validate == nil {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return e
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		return databaseError(ctx)
	}
	defer conn.Close()
	var writable bool
	if e = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&writable); e != nil || !writable {
		return databaseError(ctx)
	}
	tx, e := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='20s'; SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		return databaseError(ctx)
	}
	if e = checkAuditUpgradeSchema(ctx, tx); e != nil {
		return e
	}
	var ready bool
	if e = tx.QueryRowContext(ctx, auditReadinessSQL, sessionChecksum, decisionChecksum).Scan(&ready); e != nil {
		return databaseError(ctx)
	}
	if ready {
		// Retry after ambiguous COMMIT does not reapply DDL or historical counts:
		// new v2 events may legitimately exist after the original activation.
		if e = checkAuditUpgradeReceipt(ctx, tx); e != nil {
			return e
		}
	} else {
		if e = checkPreparedAuditUpgrade(ctx, tx); e != nil {
			return e
		}
		var complete bool
		if e = tx.QueryRowContext(ctx, auditCoverageSQL).Scan(&complete); e != nil {
			return databaseError(ctx)
		}
		if !complete {
			return contract.ErrAuditUnavailable
		}
		if e = validateUpgradeHistory(ctx, db, tx, validate); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall.migrations(version,checksum) VALUES(2,$1)", decisionChecksum); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "UPDATE heimdall.schema_metadata SET database_version=2,state='ready',reader_min=2,reader_max=2,writer_min=2,writer_max=2 WHERE singleton"); e != nil {
			return databaseError(ctx)
		}
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}

func checkAuditUpgradeReceipt(ctx context.Context, tx *sql.Tx) error {
	var valid bool
	if e := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM heimdall.audit_upgrade)=1 AND EXISTS(SELECT 1 FROM heimdall.audit_upgrade WHERE singleton AND base_checksum=$1 AND decision_checksum=$2 AND backfill_checksum=$3 AND preparation_checksum=$4)`, sessionChecksum, auditUpgradeChecksums[0], auditUpgradeChecksums[1], auditUpgradeChecksums[2]).Scan(&valid); e != nil || !valid {
		return databaseError(ctx)
	}
	return nil
}

type activationEvent struct {
	Product     string `json:"product"`
	Environment string `json:"environment"`
	backfillEvent
}

func validateUpgradeHistory(ctx context.Context, db *sql.DB, tx *sql.Tx, validate func(map[string]any) error) error {
	product, environment, eventID := "", "", ""
	var requests int64
	for {
		var raw []byte
		var total int
		e := tx.QueryRowContext(ctx, `WITH selected AS MATERIALIZED (
   SELECT product_id,environment,event_id,recorded_at,payload_digest,event FROM heimdall.audit_events
   WHERE (product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C")>($1 COLLATE "C",$2 COLLATE "C",$3 COLLATE "C")
   ORDER BY product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C" LIMIT 100
  ), sized AS (SELECT *,sum(octet_length(event::text)) OVER(ORDER BY product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C") AS size FROM selected)
  SELECT (SELECT count(*)::int FROM selected),COALESCE(jsonb_agg(jsonb_build_object('product',product_id,'environment',environment,'event_id',event_id,'recorded_at',recorded_at::text,'payload_digest',encode(payload_digest,'hex'),'event',event) ORDER BY product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C"),'[]'::jsonb) FROM sized WHERE size<=4194304`, product, environment, eventID).Scan(&total, &raw)
		if e != nil {
			return databaseError(ctx)
		}
		var rows []activationEvent
		if json.Unmarshal(raw, &rows) != nil || rows == nil || total < 0 || total > 100 || len(rows) > total || (total > 0 && len(rows) == 0) {
			return contract.ErrAuditUnavailable
		}
		if total == 0 {
			break
		}
		for _, row := range rows {
			a, e := NewAudit(db, row.Product, row.Environment, nil, validate)
			if e != nil {
				return contract.ErrAuditUnavailable
			}
			at, ok := workspaceDecimal(&row.RecordedAt, maxSafe, false)
			if !ok {
				return contract.ErrAuditUnavailable
			}
			event, e := a.decodeStored(row.Event, row.EventID, at, row.Digest)
			if e != nil {
				return e
			}
			kind := event["eventType"]
			if kind == "heimdall.authorization.requested" || kind == "heimdall.authorization.completed" {
				if kind == "heimdall.authorization.requested" {
					requests++
				}
				if e = a.verifyHistoricalSlot(ctx, tx, event); e != nil {
					return e
				}
			}
			product, environment, eventID = row.Product, row.Environment, row.EventID
		}
	}
	var slots int64
	if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM heimdall.audit_decisions").Scan(&slots); e != nil {
		return databaseError(ctx)
	}
	if slots != requests {
		return contract.ErrAuditUnavailable
	}
	return nil
}

func (a *Audit) verifyHistoricalSlot(ctx context.Context, tx *sql.Tx, event map[string]any) error {
	var requestID, state string
	var requested int64
	var terminal *string
	var resolved *int64
	e := tx.QueryRowContext(ctx, "SELECT request_event_id,requested_at,resolution,terminal_event_id,resolved_at FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2 AND decision_id=$3", a.product, a.environment, event["decisionId"]).Scan(&requestID, &requested, &state, &terminal, &resolved)
	if e == sql.ErrNoRows {
		return contract.ErrAuditUnavailable
	}
	if e != nil {
		return databaseError(ctx)
	}
	if (event["eventType"] == "heimdall.authorization.requested" && event["eventId"] != requestID) || (event["eventType"] == "heimdall.authorization.completed" && (terminal == nil || event["eventId"] != *terminal)) {
		return contract.ErrAuditUnavailable
	}
	load := func(id string) (map[string]any, error) {
		var raw, digest []byte
		var at int64
		e := tx.QueryRowContext(ctx, "SELECT recorded_at,payload_digest,event FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=$3", a.product, a.environment, id).Scan(&at, &digest, &raw)
		if e == sql.ErrNoRows {
			return nil, contract.ErrAuditUnavailable
		}
		if e != nil {
			return nil, databaseError(ctx)
		}
		return a.decodeStored(raw, id, at, hex.EncodeToString(digest))
	}
	request, e := load(requestID)
	if e != nil {
		return e
	}
	events := []map[string]any{request}
	if terminal != nil {
		outcome, e := load(*terminal)
		if e != nil {
			return e
		}
		events = append(events, outcome)
	}
	binding, e := contract.BindAuditDecision(a.product, a.environment, events)
	if e != nil {
		return e
	}
	if binding.DecisionID != event["decisionId"] || binding.RequestEventID != requestID || binding.RequestedAt != requested || binding.Resolution != state || (resolved == nil) != (binding.ResolvedAt == nil) || (resolved != nil && *resolved != *binding.ResolvedAt) {
		return contract.ErrAuditUnavailable
	}
	return nil
}
