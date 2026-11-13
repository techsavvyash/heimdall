package postgres

import (
	"context"
	"database/sql"

	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

const directoryScope = "product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4"

type DirectoryCheckpoint struct {
	StartIndex, Revision int64
}

func (s *IdentityRecords) DirectoryCheckpoint(ctx context.Context, issuer, org string) (DirectoryCheckpoint, error) {
	binding, err := identityBinding(issuer, org)
	if err != nil {
		return DirectoryCheckpoint{}, err
	}
	return identityTransaction(ctx, s.db, identityLock(s.product, s.environment, binding), true, func(ctx context.Context, tx *sql.Tx) (DirectoryCheckpoint, error) {
		var checkpoint DirectoryCheckpoint
		args := []any{s.product, s.environment, issuer, org}
		if _, err := tx.ExecContext(ctx, "INSERT INTO heimdall_directory.polling(product_id,environment,issuer,organization_id,start_index) VALUES($1,$2,$3,$4,1) ON CONFLICT DO NOTHING", args...); err != nil {
			return checkpoint, err
		}
		err := tx.QueryRowContext(ctx, "SELECT start_index,revision FROM heimdall_directory.polling WHERE "+directoryScope, args...).Scan(&checkpoint.StartIndex, &checkpoint.Revision)
		return checkpoint, err
	})
}

func (s *IdentityRecords) CommitDirectoryPage(ctx context.Context, issuer, org string, checkpoint DirectoryCheckpoint, next int64, subjects []string) error {
	binding, err := identityBinding(issuer, org)
	if err != nil {
		return err
	}
	_, err = identityTransaction(ctx, s.db, identityLock(s.product, s.environment, binding), true, func(ctx context.Context, tx *sql.Tx) (bool, error) {
		changed, err := tx.ExecContext(ctx, "UPDATE heimdall_directory.polling SET start_index=$5,revision=revision+1 WHERE "+directoryScope+" AND revision=$6", s.product, s.environment, issuer, org, next, checkpoint.Revision)
		if err != nil {
			return false, err
		}
		if count, err := changed.RowsAffected(); err != nil || count != 1 {
			return false, contract.ErrConflict
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO heimdall_directory.subjects(product_id,environment,issuer,organization_id,subject) SELECT $1,$2,$3,$4,subject FROM unnest($5::text[]) AS subject ON CONFLICT DO NOTHING", s.product, s.environment, issuer, org, pq.Array(subjects))
		return err == nil, err
	})
	return err
}

func (s *IdentityRecords) DirectorySubjects(ctx context.Context, issuer, org, after string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT subject FROM heimdall_directory.subjects WHERE "+directoryScope+" AND subject>$5 ORDER BY subject LIMIT $6", s.product, s.environment, issuer, org, after, limit)
	if err != nil {
		return nil, databaseError(ctx)
	}
	defer rows.Close()
	subjects := make([]string, 0)
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return nil, databaseError(ctx)
		}
		subjects = append(subjects, subject)
	}
	if rows.Err() != nil {
		return nil, databaseError(ctx)
	}
	return subjects, nil
}
