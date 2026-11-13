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
