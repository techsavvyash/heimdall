package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

var auditUpgradeChecksums = [...]string{"f7e859c78a6d099ad4a4d35c96e553ba10a53e3ae7e23440237c769efcd50722", "d2f4d3969cf3df68395db98219e0c209d79ce8de8c1486e6aac12de6bdd4f12b", "37db57363148bb86a8140576f727b628ec9a19ace68e464992f1f3f7226c22b0"}

// PrepareAuditUpgrade is an offline deployment step, never a runtime initializer.
// writersDrained is an operator assertion; the database cannot prove quiescence.
func PrepareAuditUpgrade(ctx context.Context, db *sql.DB, decisionSQL, backfillSQL, preparationSQL []byte, writersDrained bool) error {
	if ctx == nil || db == nil || !writersDrained {
		return contract.ErrInvalidArgument
	}
	for i, source := range [][]byte{decisionSQL, backfillSQL, preparationSQL} {
		digest := sha256.Sum256(source)
		if hex.EncodeToString(digest[:]) != auditUpgradeChecksums[i] {
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
	var receiptExists bool
	if e = tx.QueryRowContext(ctx, "SELECT to_regclass('heimdall.audit_upgrade') IS NOT NULL").Scan(&receiptExists); e != nil {
		return databaseError(ctx)
	}
	if receiptExists {
		if e = checkPreparedAuditUpgrade(ctx, tx); e != nil {
			return e
		}
	} else {
		var ready bool
		if e = tx.QueryRowContext(ctx, readinessSQL, sessionChecksum).Scan(&ready); e != nil || !ready {
			return databaseError(ctx)
		}
		// No adoption of manually installed/partial upgrade objects.
		var clean bool
		if e = tx.QueryRowContext(ctx, "SELECT to_regclass('heimdall.audit_decisions') IS NULL AND to_regclass('heimdall.audit_decision_backfill') IS NULL").Scan(&clean); e != nil || !clean {
			return databaseError(ctx)
		}
		for _, source := range [][]byte{decisionSQL, backfillSQL, preparationSQL} {
			if _, e = tx.ExecContext(ctx, string(source)); e != nil {
				return databaseError(ctx)
			}
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO heimdall.audit_upgrade(base_checksum,decision_checksum,backfill_checksum,preparation_checksum) VALUES($1,$2,$3,$4)", sessionChecksum, auditUpgradeChecksums[0], auditUpgradeChecksums[1], auditUpgradeChecksums[2]); e != nil {
			return databaseError(ctx)
		}
		if _, e = tx.ExecContext(ctx, "UPDATE heimdall.schema_metadata SET state='migrating',reader_min=1,reader_max=1,writer_min=1,writer_max=1 WHERE singleton"); e != nil {
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

const auditUpgradePreparedSQL = `SELECT (SELECT count(*) FROM heimdall.schema_metadata)=1 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=1 AND state='migrating' AND reader_min=1 AND reader_max=1 AND writer_min=1 AND writer_max=1)
   AND (SELECT count(*) FROM heimdall.migrations)=1 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)
   AND (SELECT count(*) FROM heimdall.audit_upgrade)=1 AND EXISTS(SELECT 1 FROM heimdall.audit_upgrade WHERE singleton AND base_checksum=$1 AND decision_checksum=$2 AND backfill_checksum=$3 AND preparation_checksum=$4)
   AND to_regclass('heimdall.audit_decisions') IS NOT NULL AND to_regclass('heimdall.audit_decision_backfill') IS NOT NULL`

func checkPreparedAuditUpgrade(ctx context.Context, tx *sql.Tx) error {
	var valid bool
	if e := tx.QueryRowContext(ctx, auditUpgradePreparedSQL, sessionChecksum, auditUpgradeChecksums[0], auditUpgradeChecksums[1], auditUpgradeChecksums[2]).Scan(&valid); e != nil || !valid {
		return databaseError(ctx)
	}
	return nil
}
