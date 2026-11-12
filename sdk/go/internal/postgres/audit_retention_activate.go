package postgres

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strings"
	"time"
)

// ActivateAuditRetentionUpgrade independently validates history and atomically
// publishes v4. External writers and maintenance must stay drained throughout.
func ActivateAuditRetentionUpgrade(ctx context.Context, db *sql.DB, writersDrained bool, validate func(map[string]any) error) error {
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
	var primary bool
	if e = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&primary); e != nil || !primary {
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
	if e = checkAuditRetentionSchema(ctx, tx); e != nil {
		return e
	}
	var ready bool
	if e = tx.QueryRowContext(ctx, retentionReadinessSQL, retentionReadinessArgs()...).Scan(&ready); e != nil {
		return databaseError(ctx)
	}
	if ready {
		// After activation, historical counts no longer describe live writes. The
		// immutable protocol receipt, not a second migration, resolves COMMIT loss.
		if e = checkAuditUpgradeReceipt(ctx, tx); e != nil {
			return e
		}
	} else {
		if e = checkPreparedAuditRetention(ctx, tx); e != nil {
			return e
		}
		var events, decisions int64
		if e = tx.QueryRowContext(ctx, "SELECT scanned_events,scanned_decisions FROM heimdall_retention.backfill WHERE singleton AND phase='complete' AND after_product IS NULL AND after_environment IS NULL AND after_id IS NULL").Scan(&events, &decisions); e != nil {
			return databaseError(ctx)
		}
		for _, q := range []string{retentionProgressSQL, retentionCoverageSQL} {
			var covered bool
			if e = tx.QueryRowContext(ctx, q, events, decisions).Scan(&covered); e != nil {
				return databaseError(ctx)
			}
			if !covered {
				return contract.ErrAuditUnavailable
			}
		}
		p := CapacityBackfillProgress{Phase: "ordinary"}
		for p.Phase != "complete" {
			if e = contract.CheckCancellation(ctx); e != nil {
				return e
			}
			if e = retentionGroupPage(ctx, db, tx, &p, 100, validate, false); e != nil {
				return e
			}
		}
		if p.ScannedEvents != events || p.ScannedDecisions != decisions {
			return contract.ErrAuditUnavailable
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall.migrations(version,checksum) VALUES(4,$1)", retentionUpgradeChecksums[0]); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "UPDATE heimdall.schema_metadata SET database_version=4,state='ready',reader_min=4,reader_max=4,writer_min=4,writer_max=4 WHERE singleton"); e != nil {
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
func retentionReadinessArgs() []any {
	return []any{sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1], pq.Array(retentionUpgradeChecksums[:]), retentionUpgradeChecksums[0]}
}
func CheckRetentionReadiness(ctx context.Context, db *sql.DB) error {
	return checkReadiness(ctx, db, retentionReadinessSQL, retentionReadinessArgs()...)
}

const retentionReadinessSQL = `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND (SELECT count(*) FROM heimdall.schema_metadata)=1
 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=4 AND state='ready' AND reader_min=4 AND reader_max=4 AND writer_min=4 AND writer_max=4)
 AND (SELECT count(*) FROM heimdall.migrations)=4
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=2 AND checksum=$2)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=3 AND checksum=$3)
 AND (SELECT count(*) FROM heimdall_capacity.policy)=1
 AND (SELECT count(*) FROM heimdall_capacity.upgrade)=1
 AND (SELECT count(*) FROM heimdall_capacity.backfill)=1
 AND EXISTS(SELECT 1 FROM heimdall_capacity.backfill WHERE singleton AND phase='complete' AND after_product IS NULL AND after_environment IS NULL AND after_id IS NULL)
 AND EXISTS(SELECT 1 FROM heimdall_capacity.policy p JOIN heimdall_capacity.upgrade u USING(singleton) WHERE p.singleton AND p.state='ready' AND u.capacity_checksum=$3 AND u.preparation_checksum=$4 AND p.event_limit=u.event_limit AND p.byte_limit=u.byte_limit AND p.delivery_limit=u.delivery_limit)
 AND (SELECT count(*) FROM heimdall_retention.capacity)=1
 AND (SELECT count(*) FROM heimdall_retention.upgrade)=1
 AND (SELECT count(*) FROM heimdall_retention.backfill)=1
 AND EXISTS(SELECT 1 FROM heimdall_retention.capacity c JOIN heimdall_retention.upgrade u USING(singleton)
 WHERE c.singleton AND c.row_limit=u.row_limit AND c.byte_limit=u.byte_limit AND u.checksums=$5::text[])
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=4 AND checksum=$6)
 AND EXISTS(SELECT 1 FROM heimdall_retention.backfill WHERE singleton AND phase='complete' AND after_product IS NULL AND after_environment IS NULL AND after_id IS NULL) AS ready`

var managedRetentionReadinessSQL = "SELECT base.ready AND managed.ready AS ready FROM (" + retentionReadinessSQL + ") base CROSS JOIN (" + strings.ReplaceAll(managedOnlyReadinessSQL, "$1", "$7") + ") managed"

func CheckManagedRetentionReadiness(ctx context.Context, db *sql.DB) error {
	return checkReadiness(ctx, db, managedRetentionReadinessSQL, append(retentionReadinessArgs(), managedChecksum)...)
}
