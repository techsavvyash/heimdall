package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// MigrateSession installs the exact shared v1 schema, or verifies an existing v1
// installation. Deployment-only: never call from runtime constructors. It cannot
// upgrade or repair an incompatible installation and never runs caller-defined SQL.
func MigrateSession(ctx context.Context, db *sql.DB, migration []byte) error {
	return migrate(ctx, db, migration, false)
}

// MigrateManaged requires a ready session/audit installation. It never installs or
// repairs the prerequisite implicitly and never changes the session ledger.
func MigrateManaged(ctx context.Context, db *sql.DB, migration []byte) error {
	return migrate(ctx, db, migration, true)
}
func migrate(ctx context.Context, db *sql.DB, migration []byte, managed bool) error {
	schema, checksum, readySQL := "heimdall", sessionChecksum, readinessSQL
	if managed {
		schema, checksum, readySQL = "heimdall_managed", managedChecksum, managedOnlyReadinessSQL
	}
	digest := sha256.Sum256(migration)
	if db == nil || ctx == nil || hex.EncodeToString(digest[:]) != checksum {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return databaseError(ctx)
	}
	defer conn.Close()
	var writable bool
	if err = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&writable); err != nil || !writable {
		return databaseError(ctx)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	// READ COMMITTED is deliberate: a runner waiting on the lock must see the
	// previous runner's committed installation in its next statement snapshot.
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='20s'"); err != nil {
		return databaseError(ctx)
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(74162401)"); err != nil {
		return databaseError(ctx)
	}
	var primary bool
	if err = tx.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&primary); err != nil || !primary {
		return databaseError(ctx)
	}
	if managed {
		var baseReady bool
		if err = tx.QueryRowContext(ctx, "SELECT old.ready OR upgraded.ready FROM ("+readinessSQL+") old CROSS JOIN ("+auditReadinessSQL+") upgraded", sessionChecksum, decisionChecksum).Scan(&baseReady); err != nil || !baseReady {
			return databaseError(ctx)
		}
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)", schema).Scan(&exists); err != nil {
		return databaseError(ctx)
	}
	if exists {
		var ready bool
		if err = tx.QueryRowContext(ctx, readySQL, checksum).Scan(&ready); err != nil || !ready {
			return databaseError(ctx)
		}
	} else {
		if _, err = tx.ExecContext(ctx, string(migration)); err != nil {
			return databaseError(ctx)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+schema+".migrations(version,checksum) VALUES(1,$1)", checksum); err != nil {
			return databaseError(ctx)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+schema+".schema_metadata(database_version,state,reader_min,reader_max,writer_min,writer_max) VALUES(1,'ready',1,1,1,1)"); err != nil {
			return databaseError(ctx)
		}
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	// A lost COMMIT response cannot safely be reported as rollback. An operator
	// may rerun this exact command to inspect the ledger under the same lock.
	if err = tx.Commit(); err != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}
func databaseError(ctx context.Context) error {
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	return contract.ErrUnavailable
}
