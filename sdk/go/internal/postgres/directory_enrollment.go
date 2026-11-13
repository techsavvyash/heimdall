package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func (s *IdentityRecords) RecordDirectoryEnrollment(ctx context.Context, issuer, org, subject, workspace, rule string, plan []string) error {
	binding, err := identityBinding(issuer, subject)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return contract.ErrInvalidArgument
	}
	_, err = identityTransaction(ctx, s.db, identityLock(s.product, s.environment, binding), true, func(ctx context.Context, tx *sql.Tx) (bool, error) {
		result, err := tx.ExecContext(ctx, `INSERT INTO heimdall_directory.enrollments(product_id,environment,issuer,organization_id,subject,workspace_id,rule_id,plan)
SELECT $1,$2,$3,$4,$5,$6,$7,$8::jsonb WHERE NOT EXISTS (
 SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$9)
ON CONFLICT(product_id,environment,issuer,organization_id,subject,workspace_id,rule_id)
DO UPDATE SET plan=EXCLUDED.plan WHERE enrollments.plan=EXCLUDED.plan`,
			s.product, s.environment, issuer, org, subject, workspace, rule, string(raw), binding)
		return directoryEnrollmentWrite(result, err)
	})
	return err
}

func (s *IdentityRecords) ConfirmDirectoryEnrollment(ctx context.Context, issuer, org, subject, workspace, rule, operation string) error {
	binding, err := identityBinding(issuer, subject)
	if err != nil {
		return err
	}
	_, err = identityTransaction(ctx, s.db, identityLock(s.product, s.environment, binding), true, func(ctx context.Context, tx *sql.Tx) (bool, error) {
		result, err := tx.ExecContext(ctx, `UPDATE heimdall_directory.enrollments SET operation_id=$8
WHERE product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4
 AND subject=$5 AND workspace_id=$6 AND rule_id=$7 AND (operation_id IS NULL OR operation_id=$8)`,
			s.product, s.environment, issuer, org, subject, workspace, rule, operation)
		return directoryEnrollmentWrite(result, err)
	})
	return err
}

func directoryEnrollmentWrite(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return false, contract.ErrConflict
	}
	return true, nil
}
