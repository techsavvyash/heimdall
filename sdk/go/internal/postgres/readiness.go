package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strings"
	"time"
)

const sessionChecksum = "f71db3eece51c3230c27c89a4e154bda18e15de3276b2e7eb0001a3e38c79cc4"
const readinessSQL = `SELECT NOT pg_is_in_recovery()
 AND current_setting('transaction_read_only') = 'off'
 AND (SELECT count(*) FROM heimdall.schema_metadata) = 1
 AND (SELECT count(*) FROM heimdall.schema_metadata WHERE singleton = true
   AND database_version = 1 AND state = 'ready'
   AND reader_min = 1 AND reader_max BETWEEN 1 AND 1000
   AND writer_min = 1 AND writer_max BETWEEN 1 AND 1000) = 1
 AND (SELECT count(*) FROM heimdall.migrations) = 1
 AND (SELECT count(*) FROM heimdall.migrations WHERE version = 1 AND checksum = $1) = 1 AS ready`

// CheckReadiness checks the bundled v1 policy in one primary database snapshot.
// It performs no DDL, initialization or recovery, and does not fence later migrations.
func CheckReadiness(ctx context.Context, db *sql.DB) error {
	return checkReadiness(ctx, db, readinessSQL, sessionChecksum)
}

const managedChecksum = "3df4dc62f6271691c56ac605c51012a97301b98c70454af62984cf253c066c55"

var managedOnlyReadinessSQL = strings.ReplaceAll(readinessSQL, "heimdall.", "heimdall_managed.")
var managedReadinessSQL = "SELECT base.ready AND managed.ready AS ready FROM (" + readinessSQL + ") base CROSS JOIN (" + strings.ReplaceAll(managedOnlyReadinessSQL, "$1", "$2") + ") managed"

// CheckManagedReadiness observes both families in one primary statement snapshot.
func CheckManagedReadiness(ctx context.Context, db *sql.DB) error {
	return checkReadiness(ctx, db, managedReadinessSQL, sessionChecksum, managedChecksum)
}
func checkReadiness(ctx context.Context, db *sql.DB, query string, args ...any) error {
	if db == nil || ctx == nil {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	var ready bool
	err := db.QueryRowContext(ctx, query, args...).Scan(&ready)
	if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
		return cancelled
	}
	if err != nil || !ready {
		return contract.ErrUnavailable
	}
	return nil
}

var managedAuditReadinessSQL = "SELECT base.ready AND managed.ready AS ready FROM (" + auditReadinessSQL + ") base CROSS JOIN (" + strings.ReplaceAll(managedOnlyReadinessSQL, "$1", "$3") + ") managed"

// CheckManagedAuditReadiness observes activated audit v2 and managed v1 together.
func CheckManagedAuditReadiness(ctx context.Context, db *sql.DB) error {
	return checkReadiness(ctx, db, managedAuditReadinessSQL, sessionChecksum, decisionChecksum, managedChecksum)
}
