package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

const capacityReadinessSQL = `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND (SELECT count(*) FROM heimdall.schema_metadata)=1
 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=3 AND state='ready' AND reader_min=3 AND reader_max=3 AND writer_min=3 AND writer_max=3)
 AND (SELECT count(*) FROM heimdall.migrations)=3
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=2 AND checksum=$2)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=3 AND checksum=$3)
 AND (SELECT count(*) FROM heimdall_capacity.policy)=1
 AND (SELECT count(*) FROM heimdall_capacity.upgrade)=1
 AND (SELECT count(*) FROM heimdall_capacity.backfill)=1
 AND EXISTS(SELECT 1 FROM heimdall_capacity.backfill WHERE singleton AND phase='complete' AND after_product IS NULL AND after_environment IS NULL AND after_id IS NULL)
 AND EXISTS(SELECT 1 FROM heimdall_capacity.policy p JOIN heimdall_capacity.upgrade u USING(singleton) WHERE p.singleton AND p.state='ready' AND u.capacity_checksum=$3 AND u.preparation_checksum=$4 AND p.event_limit=u.event_limit AND p.byte_limit=u.byte_limit AND p.delivery_limit=u.delivery_limit) AS ready`

// ActivateAuditCapacityUpgrade publishes capacity policy and v3 compatibility in
// one deployment transaction. Writers must remain externally drained throughout.
// validate must include every product's historical event definitions.
func ActivateAuditCapacityUpgrade(ctx context.Context, db *sql.DB, writersDrained bool, validate func(map[string]any) error) error {
	if ctx == nil || db == nil || !writersDrained || validate == nil {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return e
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	var primary bool
	if e = tx.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&primary); e != nil || !primary {
		return databaseError(ctx)
	}
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='20s'; SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		return databaseError(ctx)
	}
	if e = checkAuditCapacitySchema(ctx, tx); e != nil {
		return e
	}
	var ready bool
	if e = tx.QueryRowContext(ctx, capacityReadinessSQL, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1]).Scan(&ready); e != nil {
		return databaseError(ctx)
	}
	if ready {
		// Historical checkpoint counts stop describing live history after activation.
		// An ambiguous-COMMIT retry verifies the receipt, not those obsolete counts.
		if e = checkAuditUpgradeReceipt(ctx, tx); e != nil {
			return e
		}
	} else {
		if e = checkPreparedAuditCapacity(ctx, tx); e != nil {
			return e
		}
		var events, decisions int64
		if e = tx.QueryRowContext(ctx, "SELECT scanned_events,scanned_decisions FROM heimdall_capacity.backfill WHERE singleton AND phase='complete' AND after_product IS NULL AND after_environment IS NULL AND after_id IS NULL").Scan(&events, &decisions); e != nil {
			return databaseError(ctx)
		}
		var covered bool
		if e = tx.QueryRowContext(ctx, capacityCoverageSQL, events, decisions).Scan(&covered); e != nil {
			return databaseError(ctx)
		}
		if !covered {
			return contract.ErrAuditUnavailable
		}
		// Coverage must pass before the shared import helpers run: activation must
		// never repair missing charges or reservations as a side effect of validation.
		p := CapacityBackfillProgress{Phase: "events"}
		for p.Phase != "complete" {
			if e = contract.CheckCancellation(ctx); e != nil {
				return e
			}
			if p.Phase == "events" {
				e = capacityEventPage(ctx, db, tx, &p, 100, validate)
			} else {
				e = capacityDecisionPage(ctx, db, tx, &p, 100, validate)
			}
			if e != nil {
				return e
			}
		}
		if p.ScannedEvents != events || p.ScannedDecisions != decisions {
			return contract.ErrAuditUnavailable
		}
		if e = tx.QueryRowContext(ctx, capacityCoverageSQL, events, decisions).Scan(&covered); e != nil {
			return databaseError(ctx)
		}
		if !covered {
			return contract.ErrAuditUnavailable
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall.migrations(version,checksum) VALUES(3,$1)", capacityUpgradeChecksums[0]); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "UPDATE heimdall_capacity.policy SET state='ready' WHERE singleton; UPDATE heimdall.schema_metadata SET database_version=3,state='ready',reader_min=3,reader_max=3,writer_min=3,writer_max=3 WHERE singleton"); e != nil {
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
