package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"time"

	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

//go:embed audit_retention_schema.sql
var auditRetentionSchemaSQL string

//go:embed audit_retention_schema.json
var auditRetentionSchemaJSON string

var retentionUpgradeChecksums = [...]string{"28a4d5070ee8b1ba0c68639838201a13082207ce144fd0f921d48320660675f9", "ff4c2d36f1c120f601de7addf5df4877c38fe1a5e4eae6ea93e0a9b0caeb4c33", "f9286fb217de9c86ecc5e525174d48698499c93498a6c9a8696247384e8280cd", "0b9ae1f226e71a0d8661b2deea928c39959871e545f4dd3164d53bb9939b61c0", "bd1388b8758ffbba64b8bc823383737f920ea1cc187aae6fd4c806f1947a62ef", "ef8d91118660fe24e0f0ad10bca5cdd4c9d8f02796eecf5263c9603bd8f22ad2"}

// PrepareAuditRetentionUpgrade is shared deployment tooling for both SDKs.
// writersDrained asserts external quiescence, which the advisory lock cannot prove.
func PrepareAuditRetentionUpgrade(ctx context.Context, db *sql.DB, sources [6][]byte, rowLimit, byteLimit int64, writersDrained bool) error {
	if ctx == nil || db == nil || !writersDrained || rowLimit < 1 || rowLimit > 1099511627775 || byteLimit < 8192 || byteLimit > maxSafe {
		return contract.ErrInvalidArgument
	}
	for i, source := range sources {
		digest := sha256.Sum256(source)
		if hex.EncodeToString(digest[:]) != retentionUpgradeChecksums[i] {
			return contract.ErrInvalidArgument
		}
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
	var exists bool
	if e = tx.QueryRowContext(ctx, "SELECT to_regnamespace('heimdall_retention') IS NOT NULL").Scan(&exists); e != nil {
		return databaseError(ctx)
	}
	if !exists {
		if e = checkAuditCapacitySchema(ctx, tx); e != nil {
			return e
		}
		var ready bool
		if e = tx.QueryRowContext(ctx, capacityReadinessSQL, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1]).Scan(&ready); e != nil || !ready {
			return databaseError(ctx)
		}
		if e = checkAuditUpgradeReceipt(ctx, tx); e != nil {
			return e
		}
		// Each event and decision consumes a slot. Pending decisions reserve two more.
		// Existing source-table SHARE locks make the preflight stable through commit.
		var fits bool
		if e = tx.QueryRowContext(ctx, `WITH required AS (
   SELECT (SELECT count(*)::numeric FROM heimdall.audit_events)
    +(SELECT count(*)::numeric FROM heimdall.audit_decisions)
    +2*(SELECT count(*)::numeric FROM heimdall.audit_decisions WHERE resolution='pending') AS n
  ) SELECT n<=$1::numeric AND n*8192<=$2::numeric FROM required`, rowLimit, byteLimit).Scan(&fits); e != nil {
			return databaseError(ctx)
		}
		if !fits {
			return contract.ErrAuditUnavailable
		}
		for _, source := range sources {
			if _, e = tx.ExecContext(ctx, string(source)); e != nil {
				return databaseError(ctx)
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_retention.capacity(row_limit,byte_limit) VALUES($1,$2)", rowLimit, byteLimit); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_retention.upgrade(checksums,row_limit,byte_limit) VALUES($1,$2,$3)", pq.Array(retentionUpgradeChecksums[:]), rowLimit, byteLimit); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_retention.backfill DEFAULT VALUES; UPDATE heimdall.schema_metadata SET state='migrating' WHERE singleton"); e != nil {
			return databaseError(ctx)
		}
	}
	if e = checkPreparedAuditRetention(ctx, tx); e != nil {
		return e
	}
	var same bool
	if e = tx.QueryRowContext(ctx, "SELECT row_limit=$1 AND byte_limit=$2 FROM heimdall_retention.upgrade WHERE singleton", rowLimit, byteLimit).Scan(&same); e != nil {
		return databaseError(ctx)
	}
	if !same {
		return contract.ErrConflict
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}

func checkAuditRetentionSchema(ctx context.Context, tx *sql.Tx) error {
	// The expanded catalog includes all v3 objects and the complete pinned retention
	// schema. Reject manually prepared or altered namespaces rather than adopting them.
	if e := lockAuditRetentionCatalog(ctx, tx); e != nil {
		return e
	}
	var matches bool
	if e := tx.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::integer BETWEEN 170000 AND 179999 AND current_setting('session_replication_role')='origin' AND actual.value=$1::jsonb FROM ("+auditRetentionSchemaSQL+") actual", auditRetentionSchemaJSON).Scan(&matches); e != nil {
		return databaseError(ctx)
	}
	if !matches {
		return contract.ErrAuditUnavailable
	}
	return nil
}
func lockAuditRetentionCatalog(ctx context.Context, tx *sql.Tx) error {
	if _, e := tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; LOCK TABLE heimdall.schema_metadata,heimdall.migrations,heimdall.records,heimdall.identity_denials,heimdall.audit_events,heimdall.audit_deliveries,heimdall.audit_decisions,heimdall.audit_decision_backfill,heimdall.audit_upgrade,heimdall_capacity.policy,heimdall_capacity.event_charges,heimdall_capacity.reservations,heimdall_capacity.upgrade,heimdall_capacity.backfill,heimdall_retention.event_identities,heimdall_retention.decision_identities,heimdall_retention.event_holds,heimdall_retention.capacity,heimdall_retention.retirements,heimdall_retention.upgrade,heimdall_retention.backfill IN SHARE MODE"); e != nil {
		return databaseError(ctx)
	}
	return nil
}
func checkPreparedAuditRetention(ctx context.Context, tx *sql.Tx) error {
	if e := checkAuditRetentionSchema(ctx, tx); e != nil {
		return e
	}
	var matches bool
	// Reuse all old readiness receipts while requiring the migration outage state.
	if e := tx.QueryRowContext(ctx, retentionPreparationReadinessSQL, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1], pq.Array(retentionUpgradeChecksums[:])).Scan(&matches); e != nil {
		return databaseError(ctx)
	}
	if !matches {
		return contract.ErrAuditUnavailable
	}
	return checkAuditUpgradeReceipt(ctx, tx)
}

const retentionPreparationReadinessSQL = `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND (SELECT count(*) FROM heimdall.schema_metadata)=1
 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=3 AND state='migrating' AND reader_min=3 AND reader_max=3 AND writer_min=3 AND writer_max=3)
 AND (SELECT count(*) FROM heimdall.migrations)=3
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
 WHERE c.singleton AND c.row_limit=u.row_limit AND c.byte_limit=u.byte_limit AND u.checksums=$5::text[])`
