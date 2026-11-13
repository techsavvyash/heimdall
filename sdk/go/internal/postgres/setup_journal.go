package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type SetupScope struct {
	ProductID, Environment, Fingerprint string
	StepCount                           int
}

type SetupStep struct {
	State, DispatchID, ResourceID string
}

func ClaimSetupStep(ctx context.Context, db *sql.DB, scope SetupScope, ordinal int, dispatchID string) (SetupStep, error) {
	var step SetupStep
	err := setupTransaction(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT state,COALESCE(dispatch_id::text,''),COALESCE(resource_id,'') FROM heimdall_setup.claim($1,$2,$3,$4,$5,$6)",
			scope.ProductID, scope.Environment, scope.Fingerprint, scope.StepCount, ordinal, dispatchID).Scan(&step.State, &step.DispatchID, &step.ResourceID)
		if err != nil {
			return databaseError(ctx)
		}
		switch step.State {
		case "conflict":
			return contract.ErrConflict
		case "claimed":
			if step.DispatchID != dispatchID {
				return contract.ErrUnavailable
			}
		case "unresolved", "confirmed":
		default:
			return contract.ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return SetupStep{}, err
	}
	return step, nil
}

func ConfirmSetupStep(ctx context.Context, db *sql.DB, scope SetupScope, ordinal int, dispatchID, resourceID string) error {
	return setupTransaction(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		var confirmed bool
		err := tx.QueryRowContext(ctx, "SELECT heimdall_setup.confirm($1,$2,$3,$4,$5,$6)",
			scope.ProductID, scope.Environment, scope.Fingerprint, ordinal, dispatchID, resourceID).Scan(&confirmed)
		if err != nil {
			return databaseError(ctx)
		}
		if !confirmed {
			return contract.ErrConflict
		}
		return nil
	})
}

func setupTransaction(ctx context.Context, db *sql.DB, action func(context.Context, *sql.Tx) error) error {
	if ctx == nil || db == nil {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); err != nil {
		return databaseError(ctx)
	}
	var ready bool
	if err := tx.QueryRowContext(ctx, setupReadinessSQL, setupChecksum).Scan(&ready); err != nil || !ready {
		return databaseError(ctx)
	}
	if err := action(ctx, tx); err != nil {
		return err
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}

// RequireSetupDispatch rejects recovery input for a step that has never been dispatched.
func RequireSetupDispatch(ctx context.Context, db *sql.DB, scope SetupScope, ordinal int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var present bool
	err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM heimdall_setup.steps WHERE product_id=$1 AND environment=$2 AND ordinal=$3)",
		scope.ProductID, scope.Environment, ordinal).Scan(&present)
	if err != nil {
		return databaseError(ctx)
	}
	if !present {
		return contract.ErrConflict
	}
	return nil
}
