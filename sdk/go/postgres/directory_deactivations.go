package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type DirectoryDeactivationResult struct {
	State               string
	SessionsInvalidated int64
}

// NewDirectoryDeactivation verifies a tracked subject before persisting denial and cleanup intent.
func NewDirectoryDeactivation(db *sql.DB, product, environment string, options provisioning.DirectoryReaderOptions) (func(context.Context, string) (DirectoryDeactivationResult, error), error) {
	reader, err := provisioning.NewDirectoryReader(options)
	if err != nil {
		return nil, err
	}
	records, err := postgres.NewRetainedRuntimeIdentityRecords(db, product, environment)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, subject string) (DirectoryDeactivationResult, error) {
		result, err := reader.Read(ctx, subject)
		if err != nil {
			return DirectoryDeactivationResult{}, err
		}
		switch result.State {
		case "inactive", "missing":
			removed, err := records.RecordDirectoryDeactivation(ctx, options.Issuer, subject, options.OrganizationID, result.State, time.Now().UnixMilli())
			if err != nil {
				return DirectoryDeactivationResult{}, err
			}
			return DirectoryDeactivationResult{State: result.State, SessionsInvalidated: removed}, nil
		default:
			return DirectoryDeactivationResult{State: result.State}, nil
		}
	}, nil
}
