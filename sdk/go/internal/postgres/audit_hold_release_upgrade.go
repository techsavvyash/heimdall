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
	gate := retentionV4ReadinessSQL
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
	if _, e := checkHoldReleaseCatalog(ctx, tx, false); e != nil {
		return e
	}
	return checkHoldReleaseReservations(ctx, tx)
}

func checkHoldReleaseCatalog(ctx context.Context, tx *sql.Tx, allowIndex bool) (bool, error) {
	if e := lockAuditRetentionCatalog(ctx, tx); e != nil {
		return false, e
	}
	if _, e := tx.ExecContext(ctx, "LOCK TABLE heimdall_retention.hold_release_reservations,heimdall_retention.hold_release_capacity IN SHARE MODE"); e != nil {
		return false, databaseError(ctx)
	}
	var actual string
	var supported, indexed bool
	if e := tx.QueryRowContext(ctx, retirementIndexCatalogSQL, allowIndex).Scan(&actual, &supported, &indexed); e != nil {
		return false, databaseError(ctx)
	}
	digest := sha256.Sum256([]byte(actual))
	if !supported || hex.EncodeToString(digest[:]) != holdReleaseCatalogChecksum {
		return false, contract.ErrAuditUnavailable
	}
	return indexed, nil
}

func checkHoldReleaseReservations(ctx context.Context, tx *sql.Tx) error {
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

// ActivateHoldReleaseUpgrade publishes the verified reservation protocol.
func ActivateHoldReleaseUpgrade(ctx context.Context, db *sql.DB, writersDrained bool) error {
	if ctx == nil || db == nil || !writersDrained {
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
	var active bool
	if e = tx.QueryRowContext(ctx, retentionV5ReadinessSQL, retentionReadinessArgs()...).Scan(&active); e != nil {
		return databaseError(ctx)
	}
	if !active {
		gate := strings.Replace(retentionV4ReadinessSQL, "state='ready' AND reader_min", "state='migrating' AND reader_min", 1)
		var ready bool
		if e = tx.QueryRowContext(ctx, gate, retentionReadinessArgs()...).Scan(&ready); e != nil || !ready {
			return databaseError(ctx)
		}
	}
	if _, e = checkHoldReleaseCatalog(ctx, tx, active); e != nil {
		return e
	}
	if e = checkHoldReleaseReservations(ctx, tx); e != nil {
		return e
	}
	if !active {
		var bound bool
		if e = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(
   SELECT 1 FROM heimdall_retention.event_holds h
   LEFT JOIN heimdall_retention.event_identities t ON (t.product_id,t.environment,t.event_id)=(h.product_id,h.environment,h.event_id)
   LEFT JOIN heimdall_retention.event_identities p ON (p.product_id,p.environment,p.event_id)=(h.product_id,h.environment,h.placement_event_id)
   LEFT JOIN heimdall_retention.event_identities r ON (r.product_id,r.environment,r.event_id)=(h.product_id,h.environment,h.release_event_id)
   WHERE t.event_id IS NULL OR t.tenant_id IS DISTINCT FROM h.tenant_id OR t.recorded_at>h.placed_at
     OR p.event_id IS NULL OR p.tenant_id IS DISTINCT FROM h.tenant_id OR p.recorded_at<>h.placed_at
     OR (h.release_event_id IS NOT NULL AND (r.event_id IS NULL OR r.tenant_id IS DISTINCT FROM h.tenant_id OR r.recorded_at<>h.released_at))
     OR (h.release_event_id IS NULL AND NOT EXISTS (
       SELECT 1 FROM heimdall.audit_events e JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id)
       WHERE (e.product_id,e.environment,e.event_id)=(t.product_id,t.environment,t.event_id)
         AND e.recorded_at=t.recorded_at AND e.payload_digest=t.payload_digest
         AND c.envelope_bytes=octet_length(e.event::text)
     ))
  )`).Scan(&bound); e != nil {
			return databaseError(ctx)
		}
		if !bound {
			return contract.ErrAuditUnavailable
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall.migrations(version,checksum) VALUES(5,$1)", holdReleaseMigrationChecksum); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "UPDATE heimdall.schema_metadata SET database_version=5,state='ready',reader_min=5,reader_max=5,writer_min=5,writer_max=5 WHERE singleton"); e != nil {
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
