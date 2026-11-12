package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

const holdReleaseMigrationChecksum = "b6f5f89911249318a85da849686db43d99db10e8c80d87c60ca67cd1091bb072"
const holdReleaseCatalogChecksum = "96846d4bd00508af14479471bb9e6e1f9ede25a66d44dc93b81aff44ee6846ac"

// PrepareHoldReleaseUpgrade installs reservations with writers drained and leaves
// the database in the migrating state until explicit activation.
func PrepareHoldReleaseUpgrade(ctx context.Context, db *sql.DB, source []byte, writersDrained bool) error {
	if ctx == nil || db == nil || !writersDrained {
		return contract.ErrInvalidArgument
	}
	digest := sha256.Sum256(source)
	if hex.EncodeToString(digest[:]) != holdReleaseMigrationChecksum {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, e := retainedConnection(ctx, db, true)
	if e != nil {
		return databaseError(ctx)
	}
	defer conn.Close()
	tx, e := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='20s'; SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		return databaseError(ctx)
	}
	var prepared bool
	if e = tx.QueryRowContext(ctx, "SELECT to_regclass('heimdall_retention.hold_release_reservations') IS NOT NULL").Scan(&prepared); e != nil {
		return databaseError(ctx)
	}
	gate := retentionReadinessSQL
	if prepared {
		gate = strings.Replace(gate, "state='ready' AND reader_min", "state='migrating' AND reader_min", 1)
	}
	var ready bool
	if e = tx.QueryRowContext(ctx, gate, retentionReadinessArgs()...).Scan(&ready); e != nil || !ready {
		return databaseError(ctx)
	}
	if !prepared {
		if e = checkAuditRetentionSchema(ctx, tx); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, string(source)); e != nil {
			return auditCapacityError(ctx, e)
		}
	}
	if e = checkPreparedHoldRelease(ctx, tx); e != nil {
		return e
	}
	if !prepared {
		if _, e = tx.ExecContext(ctx, "UPDATE heimdall.schema_metadata SET state='migrating' WHERE singleton"); e != nil {
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

func checkPreparedHoldRelease(ctx context.Context, tx *sql.Tx) error {
	if e := lockAuditRetentionCatalog(ctx, tx); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, "LOCK TABLE heimdall_retention.hold_release_reservations,heimdall_retention.hold_release_capacity IN SHARE MODE"); e != nil {
		return databaseError(ctx)
	}
	var actual string
	var supported bool
	if e := tx.QueryRowContext(ctx, "SELECT actual.value::text, current_setting('server_version_num')::integer BETWEEN 170000 AND 179999 AND current_setting('session_replication_role')='origin' FROM ("+auditRetentionSchemaSQL+") actual").Scan(&actual, &supported); e != nil {
		return databaseError(ctx)
	}
	digest := sha256.Sum256([]byte(actual))
	if !supported || hex.EncodeToString(digest[:]) != holdReleaseCatalogChecksum {
		return contract.ErrAuditUnavailable
	}
	var valid bool
	if e := tx.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM heimdall_retention.hold_release_capacity)=1
  AND EXISTS(SELECT 1 FROM heimdall_retention.hold_release_capacity c WHERE c.singleton
    AND c.active_count=(SELECT count(*) FROM heimdall_retention.hold_release_reservations WHERE release_event_id IS NULL))
  AND NOT EXISTS(
    SELECT 1 FROM heimdall_retention.event_holds h
    FULL JOIN heimdall_retention.hold_release_reservations r USING(product_id,environment,hold_id)
    WHERE h.hold_id IS NULL OR r.hold_id IS NULL OR h.release_event_id IS DISTINCT FROM r.release_event_id
       OR octet_length(to_jsonb(h)::text)+octet_length(to_jsonb(r)::text)>8192)`).Scan(&valid); e != nil {
		return databaseError(ctx)
	}
	if !valid {
		return contract.ErrAuditUnavailable
	}
	if _, e := tx.ExecContext(ctx, `SELECT heimdall_retention.check_hold_release_headroom(
   (SELECT active_count FROM heimdall_retention.hold_release_capacity WHERE singleton),
   (SELECT p FROM heimdall_capacity.policy p WHERE singleton),
   (SELECT c FROM heimdall_retention.capacity c WHERE singleton))`); e != nil {
		return auditCapacityError(ctx, e)
	}
	return nil
}
