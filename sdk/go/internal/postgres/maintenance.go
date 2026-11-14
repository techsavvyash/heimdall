package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// RecordMaintenance deletes expired records without encryption keys or implicit workers.
type RecordMaintenance struct {
	db                   *sql.DB
	product, environment string
	now                  func() int64
}

func NewRecordMaintenance(db *sql.DB, product, environment string, now func() int64) (*RecordMaintenance, error) {
	if db == nil || !valid(product, 64) || !valid(environment, 64) {
		return nil, contract.ErrInvalidArgument
	}
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &RecordMaintenance{db, product, environment, now}, nil
}

// CleanupLag returns the age of the oldest expired record in the configured scope.
func (m *RecordMaintenance) CleanupLag(ctx context.Context) (*int64, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := m.now()
	var primary bool
	var oldest sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
       (SELECT expires_at::text FROM heimdall.records WHERE product_id=$1 AND environment=$2
        AND expires_at<=$3 ORDER BY expires_at LIMIT 1) AS oldest`, m.product, m.environment, now).Scan(&primary, &oldest)
	if err != nil || !primary {
		return nil, databaseError(ctx)
	}
	if !oldest.Valid {
		return nil, contract.CheckCancellation(ctx)
	}
	at, ok := workspaceDecimal(&oldest.String, maxSafe, false)
	if !ok || at > now {
		return nil, contract.ErrUnavailable
	}
	lag := now - at
	return &lag, contract.CheckCancellation(ctx)
}

func (m *RecordMaintenance) CleanupExpired(ctx context.Context, limit int) (int, error) {
	cutoff := m.now()
	if ctx == nil || cutoff < 0 || cutoff > maxSafe || limit < 1 || limit > 100 {
		return 0, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return 0, err
	}
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return 0, databaseError(ctx)
	}
	defer conn.Close()
	// lib/pq explicitly requests READ WRITE in BeginTx; preserve the connection policy first.
	var primary bool
	if err = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&primary); err != nil {
		return 0, databaseError(ctx)
	}
	if !primary {
		return 0, contract.ErrUnavailable
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); err != nil {
		return 0, databaseError(ctx)
	}
	var count int
	if err = tx.QueryRowContext(ctx, `WITH expired AS (
 SELECT product_id,environment,kind,key_hash FROM heimdall.records
 WHERE product_id=$1 AND environment=$2 AND expires_at<=$3
 ORDER BY expires_at,kind,key_hash LIMIT $4 FOR UPDATE SKIP LOCKED
), removed AS (
 DELETE FROM heimdall.records r USING expired e
 WHERE r.product_id=e.product_id AND r.environment=e.environment
 AND r.kind=e.kind AND r.key_hash=e.key_hash AND r.expires_at<=$3 RETURNING 1
) SELECT count(*)::int AS deleted FROM removed`, m.product, m.environment, cutoff, limit).Scan(&count); err != nil {
		return 0, databaseError(ctx)
	}
	if count < 0 || count > limit {
		return 0, contract.ErrUnavailable
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, contract.ErrOutcomeUnknown
	}
	return count, nil
}
