package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type AuditUpgradeStep struct {
	Status      string                 `json:"status"`
	Registered  int                    `json:"registered,omitempty"`
	ProductID   string                 `json:"productId,omitempty"`
	Environment string                 `json:"environment,omitempty"`
	Progress    *AuditBackfillProgress `json:"progress,omitempty"`
}

// AdvanceAuditUpgrade makes bounded, restartable progress; coverage is not activation.
func AdvanceAuditUpgrade(ctx context.Context, db *sql.DB, limit int, writersDrained bool, validate func(map[string]any) error) (*AuditUpgradeStep, error) {
	if ctx == nil || db == nil || limit < 1 || limit > 100 || !writersDrained || validate == nil {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	step, e := auditUpgradeWork(ctx, db, limit)
	if e != nil {
		return nil, e
	}
	if step.Status != "backfill" {
		return step, nil
	}
	audit, e := NewAudit(db, step.ProductID, step.Environment, nil, validate)
	if e != nil {
		return nil, e
	}
	step.Progress, e = audit.BackfillDecisionPage(ctx, limit)
	if e != nil {
		step.Status = "backfill_failed"
		return step, e
	}
	step.Status = "backfilled"
	return step, nil
}

// Releases the registration connection before BackfillDecisionPage acquires one.
// This also supports a deployment pool restricted to one connection.
func auditUpgradeWork(ctx context.Context, db *sql.DB, limit int) (*AuditUpgradeStep, error) {
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		return nil, databaseError(ctx)
	}
	defer conn.Close()
	var writable bool
	if e = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&writable); e != nil || !writable {
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
	if e = checkPreparedAuditUpgrade(ctx, tx); e != nil {
		return nil, e
	}
	result, e := tx.ExecContext(ctx, `WITH missing AS MATERIALIZED (
 SELECT e.product_id,e.environment FROM heimdall.audit_events e
 WHERE NOT EXISTS(SELECT 1 FROM heimdall.audit_decision_backfill b WHERE b.product_id=e.product_id AND b.environment=e.environment)
 GROUP BY e.product_id,e.environment ORDER BY e.product_id COLLATE "C",e.environment COLLATE "C" LIMIT $1
) INSERT INTO heimdall.audit_decision_backfill(product_id,environment) SELECT product_id,environment FROM missing ON CONFLICT DO NOTHING`, limit)
	if e != nil {
		return nil, databaseError(ctx)
	}
	n, e := result.RowsAffected()
	if e != nil || n < 0 || n > int64(limit) {
		return nil, contract.ErrUnavailable
	}
	step := &AuditUpgradeStep{Status: "registered", Registered: int(n)}
	if n == 0 {
		e = tx.QueryRowContext(ctx, "SELECT product_id,environment FROM heimdall.audit_decision_backfill WHERE phase<>'complete' ORDER BY product_id COLLATE \"C\",environment COLLATE \"C\" LIMIT 1").Scan(&step.ProductID, &step.Environment)
		if e == sql.ErrNoRows {
			// Recount at the apparent end: a completed checkpoint is not proof against
			// new writes. Any drift refuses coverage; never rewind/rewrite history here.
			var valid bool
			if e = tx.QueryRowContext(ctx, auditCoverageSQL).Scan(&valid); e != nil {
				return nil, databaseError(ctx)
			}
			if !valid {
				return nil, contract.ErrAuditUnavailable
			}
			step.Status = "coverage_complete"
		} else if e != nil {
			return nil, databaseError(ctx)
		} else {
			if !valid(step.ProductID, 64) || !valid(step.Environment, 64) {
				return nil, contract.ErrAuditUnavailable
			}
			step.Status = "backfill"
		}
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, contract.ErrOutcomeUnknown
	}
	return step, nil
}

const auditCoverageSQL = `SELECT NOT EXISTS(
    SELECT 1 FROM heimdall.audit_decision_backfill b
    WHERE b.phase<>'complete' OR b.expected_count IS NULL OR b.scanned_count<>b.expected_count
     OR b.expected_count<>(SELECT count(*) FROM heimdall.audit_events e WHERE e.product_id=b.product_id AND e.environment=b.environment)
   ) AND NOT EXISTS(SELECT 1 FROM heimdall.audit_events e WHERE NOT EXISTS(SELECT 1 FROM heimdall.audit_decision_backfill b WHERE b.product_id=e.product_id AND b.environment=e.environment))`
