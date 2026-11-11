package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// ReserveRecordKeyUsage durably burns one allowance before encryption. A failed
// or uncertain reservation never authorizes encryption; later failures burn usage.
func ReserveRecordKeyUsage(ctx context.Context, db *sql.DB, fingerprint string) error {
	return changeRecordKeyUsage(ctx, db, fingerprint, "reserve", 1)
}

// RegisterRecordKeyUsage requires an operator assertion that previously unseen material
// has never encrypted records. Retrying an existing identical policy never resets usage.
func RegisterRecordKeyUsage(ctx context.Context, db *sql.DB, fingerprint string, limit int64, freshKey bool) error {
	if !freshKey {
		return contract.ErrInvalidArgument
	}
	return changeRecordKeyUsage(ctx, db, fingerprint, "register", limit)
}

// RetireRecordKeyUsage permanently prevents new reservations; decryption is unaffected.
func RetireRecordKeyUsage(ctx context.Context, db *sql.DB, fingerprint string) error {
	return changeRecordKeyUsage(ctx, db, fingerprint, "retire", 1)
}

func changeRecordKeyUsage(ctx context.Context, db *sql.DB, fingerprint, operation string, limit int64) error {
	if ctx == nil || db == nil {
		return contract.ErrInvalidArgument
	}
	if err := contract.ValidateKeyUsage(fingerprint, limit); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return databaseError(ctx)
	}
	defer conn.Close()
	var primary bool
	if err = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&primary); err != nil || !primary {
		return databaseError(ctx)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'; SET LOCAL lock_timeout='4s'; SET LOCAL statement_timeout='4s'"); err != nil {
		return databaseError(ctx)
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared(74162401)"); err != nil {
		return databaseError(ctx)
	}
	var ready bool
	if err = tx.QueryRowContext(ctx, keyUsageReadinessSQL, keyUsageChecksum).Scan(&ready); err != nil || !ready {
		return databaseError(ctx)
	}
	switch operation {
	case "register":
		if _, err = tx.ExecContext(ctx, "INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode($1,'hex'),$2) ON CONFLICT(fingerprint) DO NOTHING", fingerprint, limit); err != nil {
			return databaseError(ctx)
		}
		var existing int64
		var state string
		if err = tx.QueryRowContext(ctx, "SELECT encryption_limit,state FROM heimdall_keys.record_key_usage WHERE fingerprint=decode($1,'hex') FOR UPDATE", fingerprint).Scan(&existing, &state); err != nil {
			return databaseError(ctx)
		}
		if existing != limit || state != "active" {
			return contract.ErrConflict
		}
	case "retire":
		var state string
		if err = tx.QueryRowContext(ctx, "UPDATE heimdall_keys.record_key_usage SET state='retired' WHERE fingerprint=decode($1,'hex') RETURNING state", fingerprint).Scan(&state); err != nil {
			return databaseError(ctx)
		}
	case "reserve":
		var used int64
		if err = tx.QueryRowContext(ctx, "UPDATE heimdall_keys.record_key_usage SET used=used+1 WHERE fingerprint=decode($1,'hex') AND state='active' AND used<encryption_limit RETURNING used", fingerprint).Scan(&used); err != nil {
			return databaseError(ctx)
		}
	default:
		return contract.ErrInvalidArgument
	}

	if err = contract.CheckCancellation(ctx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}
