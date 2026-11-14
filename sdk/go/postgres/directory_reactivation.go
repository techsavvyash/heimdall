package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type DirectoryReactivation struct {
	Subject, Revision string
	StartedAt         int64
}

// NewDirectoryReactivation prepares a cleaned identity for explicit re-enrollment.
func NewDirectoryReactivation(db *sql.DB, product, environment string, options provisioning.DirectoryReaderOptions) (func(context.Context, string, string) (DirectoryReactivation, error), error) {
	reader, err := provisioning.NewDirectoryReader(options)
	if err != nil {
		return nil, err
	}
	records, err := internal.NewRetainedRuntimeIdentityRecords(db, product, environment)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, subject, revision string) (DirectoryReactivation, error) {
		empty := DirectoryReactivation{}
		if ctx == nil || !directoryID.MatchString(subject) {
			return empty, contract.ErrInvalidArgument
		}
		n, err := cleanupRevision(revision)
		if err != nil {
			return empty, err
		}
		startedAt := time.Now().UnixMilli()
		observed, err := reader.Read(ctx, subject)
		if err != nil {
			return empty, err
		}
		if observed.State != "active" || observed.ExternalID == "" {
			return empty, contract.ErrForbidden
		}
		started, err := records.PrepareDirectoryReactivation(ctx, options.Issuer, options.OrganizationID, subject, n, startedAt)
		if err != nil {
			return empty, err
		}
		return DirectoryReactivation{subject, revision, started}, nil
	}, nil
}

type DirectoryReactivationEnrollment struct {
	WorkspaceID, RuleID string
}

// NewDirectoryReactivationRelease releases a prepared identity after the exact approved enrollment set is confirmed.
func NewDirectoryReactivationRelease(db *sql.DB, product, environment string, options provisioning.DirectoryReaderOptions) (func(context.Context, string, string, []DirectoryReactivationEnrollment) error, error) {
	reader, err := provisioning.NewDirectoryReader(options)
	if err != nil {
		return nil, err
	}
	records, err := internal.NewRetainedRuntimeIdentityRecords(db, product, environment)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, subject, revision string, enrollments []DirectoryReactivationEnrollment) error {
		if ctx == nil || !directoryID.MatchString(subject) || len(enrollments) > 100 {
			return contract.ErrInvalidArgument
		}
		n, err := cleanupRevision(revision)
		if err != nil {
			return err
		}
		approvals := make(map[string]bool, len(enrollments))
		for _, item := range enrollments {
			key := item.WorkspaceID + ":" + item.RuleID
			if !directoryID.MatchString(item.WorkspaceID) || !directoryID.MatchString(item.RuleID) || approvals[key] {
				return contract.ErrInvalidArgument
			}
			approvals[key] = true
		}
		observed, err := reader.Read(ctx, subject)
		if err != nil {
			return err
		}
		if observed.State != "active" || observed.ExternalID == "" {
			return contract.ErrForbidden
		}
		return records.ReleaseDirectoryReactivation(ctx, options.Issuer, options.OrganizationID, subject, n, approvals)
	}, nil
}
