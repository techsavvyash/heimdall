package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
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
	if db == nil {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	var ready bool
	err := db.QueryRowContext(ctx, readinessSQL, sessionChecksum).Scan(&ready)
	if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
		return cancelled
	}
	if err != nil || !ready {
		return contract.ErrUnavailable
	}
	return nil
}
