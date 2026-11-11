package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

//go:embed audit_capacity_schema.sql
var auditCapacitySchemaSQL string

//go:embed audit_capacity_schema.json
var auditCapacitySchemaJSON string

var capacityUpgradeChecksums = [...]string{"15da3b2fa8a64b285516dc554a9c1cb7268fec5427046722ab9854cc0710e492", "60366da3390b8255dcabc6d1308a4fdfe22f012ee2e7e93a4434afb19c40ab86"}

// PrepareAuditCapacityUpgrade is deployment tooling, not a serving initializer.
// writersDrained asserts external quiescence; an advisory lock cannot prove it.
func PrepareAuditCapacityUpgrade(ctx context.Context, db *sql.DB, capacitySQL, preparationSQL []byte, limits contract.AuditCapacity, writersDrained bool) error {
	if ctx == nil || db == nil || !writersDrained {
		return contract.ErrInvalidArgument
	}
	for _, n := range []int64{limits.Events, limits.EnvelopeBytes, limits.Deliveries} {
		if n < 1 || n > maxSafe {
			return contract.ErrInvalidArgument
		}
	}
	for i, source := range [][]byte{capacitySQL, preparationSQL} {
		digest := sha256.Sum256(source)
		if hex.EncodeToString(digest[:]) != capacityUpgradeChecksums[i] {
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
	if e = tx.QueryRowContext(ctx, "SELECT to_regnamespace('heimdall_capacity') IS NOT NULL").Scan(&exists); e != nil {
		return databaseError(ctx)
	}
	if !exists {
		var ready bool
		if e = tx.QueryRowContext(ctx, auditReadinessSQL, sessionChecksum, decisionChecksum).Scan(&ready); e != nil || !ready {
			return databaseError(ctx)
		}
		if e = checkAuditUpgradeReceipt(ctx, tx); e != nil {
			return e
		}
		// Verify the exact old profile before adding FKs/triggers that change it.
		if e = checkAuditUpgradeSchema(ctx, tx); e != nil {
			return e
		}
		// Detect undersized operator limits before entering a serving outage. The
		// old-profile SHARE locks keep these local counts stable through preparation.
		var fits bool
		if e = tx.QueryRowContext(ctx, `WITH pending AS (SELECT count(*)::numeric AS n FROM heimdall.audit_decisions WHERE resolution='pending')
 SELECT (SELECT count(*)::numeric FROM heimdall.audit_events)+2*n<=$1::numeric
 AND (SELECT coalesce(sum(octet_length(event::text)::numeric),0) FROM heimdall.audit_events)+3000000*n<=$2::numeric
 AND (SELECT count(*)::numeric FROM heimdall.audit_deliveries)+64*n<=$3::numeric FROM pending`, limits.Events, limits.EnvelopeBytes, limits.Deliveries).Scan(&fits); e != nil {
			return databaseError(ctx)
		}
		if !fits {
			return contract.ErrAuditUnavailable
		}
		for _, source := range [][]byte{capacitySQL, preparationSQL} {
			if _, e = tx.ExecContext(ctx, string(source)); e != nil {
				return databaseError(ctx)
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_capacity.policy(event_limit,byte_limit,delivery_limit) VALUES($1,$2,$3)", limits.Events, limits.EnvelopeBytes, limits.Deliveries); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_capacity.upgrade(capacity_checksum,preparation_checksum,event_limit,byte_limit,delivery_limit) VALUES($1,$2,$3,$4,$5)", capacityUpgradeChecksums[0], capacityUpgradeChecksums[1], limits.Events, limits.EnvelopeBytes, limits.Deliveries); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall_capacity.backfill DEFAULT VALUES; UPDATE heimdall.schema_metadata SET state='migrating' WHERE singleton"); e != nil {
			return databaseError(ctx)
		}
	}
	if e = checkPreparedAuditCapacity(ctx, tx); e != nil {
		return e
	}
	var same bool
	if e = tx.QueryRowContext(ctx, "SELECT event_limit=$1 AND byte_limit=$2 AND delivery_limit=$3 FROM heimdall_capacity.upgrade WHERE singleton", limits.Events, limits.EnvelopeBytes, limits.Deliveries).Scan(&same); e != nil {
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
func checkPreparedAuditCapacity(ctx context.Context, tx *sql.Tx) error {
	if e := checkAuditCapacitySchema(ctx, tx); e != nil {
		return e
	}
	var prepared bool
	e := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM heimdall.schema_metadata)=1 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=2 AND state='migrating' AND reader_min=2 AND reader_max=2 AND writer_min=2 AND writer_max=2)
 AND (SELECT count(*) FROM heimdall.migrations)=2
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=2 AND checksum=$2)
 AND (SELECT count(*) FROM heimdall_capacity.upgrade)=1 AND (SELECT count(*) FROM heimdall_capacity.policy)=1 AND (SELECT count(*) FROM heimdall_capacity.backfill)=1
 AND EXISTS(SELECT 1 FROM heimdall_capacity.upgrade u JOIN heimdall_capacity.policy p USING(singleton) WHERE u.singleton AND u.capacity_checksum=$3 AND u.preparation_checksum=$4 AND p.state='preparing' AND u.event_limit=p.event_limit AND u.byte_limit=p.byte_limit AND u.delivery_limit=p.delivery_limit)`, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1]).Scan(&prepared)
	if e != nil {
		return databaseError(ctx)
	}
	if !prepared {
		return contract.ErrAuditUnavailable
	}
	return checkAuditUpgradeReceipt(ctx, tx)
}
func checkAuditCapacitySchema(ctx context.Context, tx *sql.Tx) error {
	if _, e := tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; LOCK TABLE heimdall.schema_metadata,heimdall.migrations,heimdall.records,heimdall.identity_denials,heimdall.audit_events,heimdall.audit_deliveries,heimdall.audit_decisions,heimdall.audit_decision_backfill,heimdall.audit_upgrade,heimdall_capacity.policy,heimdall_capacity.event_charges,heimdall_capacity.reservations,heimdall_capacity.upgrade,heimdall_capacity.backfill IN SHARE MODE"); e != nil {
		return databaseError(ctx)
	}
	var matches bool
	if e := tx.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::integer BETWEEN 170000 AND 179999 AND current_setting('session_replication_role')='origin' AND actual.value=$1::jsonb FROM ("+auditCapacitySchemaSQL+") actual", auditCapacitySchemaJSON).Scan(&matches); e != nil {
		return databaseError(ctx)
	}
	if !matches {
		return contract.ErrAuditUnavailable
	}
	return nil
}
