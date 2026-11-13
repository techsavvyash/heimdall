package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func (s *IdentityRecords) PrepareDirectoryReactivation(ctx context.Context, issuer, org, subject string, revision, startedAt int64) (int64, error) {
	binding, err := identityBinding(issuer, subject)
	if err != nil {
		return 0, err
	}
	return identityTransaction(ctx, s.db, identityLock(s.product, s.environment, binding), true, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		var started int64
		err := tx.QueryRowContext(ctx, `UPDATE heimdall_directory.deactivations d
SET reactivation_started_at=COALESCE(d.reactivation_started_at,$8)
WHERE d.product_id=$1 AND d.environment=$2 AND d.issuer=$3 AND d.organization_id=$4
 AND d.identity_hash=$5 AND d.subject=$6 AND d.revision=$7
 AND d.completed AND d.reason='inactive' AND d.observed_at<$8
 AND EXISTS(SELECT 1 FROM heimdall.identity_denials n WHERE n.product_id=$1
   AND n.environment=$2 AND n.identity_hash=$5 AND n.reason='inactive' AND n.observed_at<COALESCE(d.reactivation_started_at,$8))
RETURNING d.reactivation_started_at`, s.product, s.environment, issuer, org, binding, subject, revision, startedAt).Scan(&started)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, contract.ErrConflict
		}
		return started, err
	})
}

func (s *IdentityRecords) ReleaseDirectoryReactivation(ctx context.Context, issuer, org, subject string, revision int64, approvals map[string]bool) error {
	hash, err := identityBinding(issuer, subject)
	if err != nil {
		return err
	}
	_, err = identityTransaction(ctx, s.db, identityLock(s.product, s.environment, hash), true, func(ctx context.Context, tx *sql.Tx) (bool, error) {
		scope := []any{s.product, s.environment, issuer, org, subject, revision, hash}
		rows, err := tx.QueryContext(ctx, `SELECT e.workspace_id,e.rule_id,EXISTS (
 SELECT 1 FROM heimdall_managed.operations o WHERE o.product_id=e.product_id AND o.environment=e.environment
  AND o.operation_id=e.operation_id AND o.target_hash=$7 AND o.workspace_id=e.workspace_id
  AND o.state='completed' AND o.kind IN ('add_member','assign_roles'))
FROM heimdall_directory.enrollments e WHERE product_id=$1 AND environment=$2 AND issuer=$3
 AND organization_id=$4 AND subject=$5 AND reactivation_revision=$6 LIMIT 101`, scope...)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var workspace, rule string
			var confirmed bool
			if err := rows.Scan(&workspace, &rule, &confirmed); err != nil {
				return false, err
			}
			if !confirmed || !approvals[workspace+":"+rule] {
				return false, contract.ErrConflict
			}
			count++
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		if count != len(approvals) {
			return false, contract.ErrConflict
		}

		var released, deniedAgain bool
		var startedAt int64
		err = tx.QueryRowContext(ctx, `SELECT d.reactivation_released,d.reactivation_started_at,
 EXISTS(SELECT 1 FROM heimdall.identity_denials n WHERE n.product_id=$1 AND n.environment=$2 AND n.identity_hash=$7)
FROM heimdall_directory.deactivations d WHERE d.product_id=$1 AND d.environment=$2 AND d.issuer=$3
 AND d.organization_id=$4 AND d.subject=$5 AND d.revision=$6 AND d.identity_hash=$7
 AND d.completed AND d.reason='inactive' AND d.reactivation_started_at IS NOT NULL`, scope...).Scan(&released, &startedAt, &deniedAgain)
		if errors.Is(err, sql.ErrNoRows) {
			return false, contract.ErrConflict
		}
		if err != nil {
			return false, err
		}
		if released {
			if deniedAgain {
				return false, contract.ErrConflict
			}
			return true, nil
		}
		denied, err := tx.ExecContext(ctx, `DELETE FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2
 AND identity_hash=$3 AND reason='inactive' AND observed_at<$4`, s.product, s.environment, hash, startedAt)
		if err != nil {
			return false, err
		}
		if count, err := denied.RowsAffected(); err != nil || count != 1 {
			return false, contract.ErrConflict
		}
		_, err = tx.ExecContext(ctx, "UPDATE heimdall_directory.deactivations SET reactivation_released=true WHERE product_id=$1 AND environment=$2 AND identity_hash=$3", s.product, s.environment, hash)
		return err == nil, err
	})
	return err
}
