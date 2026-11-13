package postgres

import (
	"context"
	"database/sql"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type DirectoryCleanupItem struct {
	Subject, Revision, Reason string
	ObservedAt                int64
}

func (s *IdentityRecords) PendingDirectoryCleanup(ctx context.Context, issuer, org string, after int64, limit int) ([]DirectoryCleanupItem, error) {
	args := append(retentionReadinessArgs(), s.product, s.environment, issuer, org, after, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT readiness.ready,d.subject,d.revision::text,d.observed_at,d.reason
FROM (`+retentionReadinessSQL+`) readiness LEFT JOIN LATERAL (
 SELECT subject,revision,observed_at,reason FROM heimdall_directory.deactivations
 WHERE readiness.ready AND product_id=$7 AND environment=$8 AND issuer=$9 AND organization_id=$10
   AND NOT completed AND revision>$11::bigint ORDER BY revision LIMIT $12
) d ON true ORDER BY d.revision`, args...)
	if err != nil {
		return nil, databaseError(ctx)
	}
	defer rows.Close()
	items := make([]DirectoryCleanupItem, 0)
	ready := false
	for rows.Next() {
		var subject, revision, reason sql.NullString
		var observed sql.NullInt64
		if err := rows.Scan(&ready, &subject, &revision, &observed, &reason); err != nil || !ready {
			return nil, databaseError(ctx)
		}
		if subject.Valid {
			items = append(items, DirectoryCleanupItem{subject.String, revision.String, reason.String, observed.Int64})
		}
	}
	if rows.Err() != nil || !ready {
		return nil, databaseError(ctx)
	}
	return items, nil
}

func (s *IdentityRecords) CompleteDirectoryCleanup(ctx context.Context, issuer, org, subject string, revision int64) error {
	binding, err := identityBinding(issuer, subject)
	if err != nil {
		return err
	}
	_, err = identityTransaction(ctx, s.db, identityLock(s.product, s.environment, binding), true, func(ctx context.Context, tx *sql.Tx) (bool, error) {
		result, err := tx.ExecContext(ctx, `UPDATE heimdall_directory.deactivations SET completed=true
WHERE product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4 AND identity_hash=$5 AND subject=$6 AND revision=$7`,
			s.product, s.environment, issuer, org, binding, subject, revision)
		if err != nil {
			return false, err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return false, contract.ErrConflict
		}
		return true, nil
	})
	return err
}
