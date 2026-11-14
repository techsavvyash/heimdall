package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type DirectoryPolling struct {
	reader               *provisioning.DirectoryReader
	records              *internal.IdentityRecords
	observe              func(context.Context, string) (DirectoryDeactivationResult, error)
	issuer, organization string
}

type DirectoryDiscoveryResult struct {
	Discovered    int
	CycleComplete bool
}

type DirectoryObservationResult struct {
	Observed            int
	SessionsInvalidated int64
	NextSubject         string
}

// NewDirectoryPolling composes explicit, bounded discovery and known-subject polling.
func NewDirectoryPolling(db *sql.DB, product, environment string, options provisioning.DirectoryReaderOptions) (*DirectoryPolling, error) {
	reader, err := provisioning.NewDirectoryReader(options)
	if err != nil {
		return nil, err
	}
	records, err := internal.NewRetainedRuntimeIdentityRecords(db, product, environment)
	if err != nil {
		return nil, err
	}
	observe, err := NewDirectoryDeactivation(db, product, environment, options)
	if err != nil {
		return nil, err
	}
	return &DirectoryPolling{reader, records, observe, options.Issuer, options.OrganizationID}, nil
}

func (d *DirectoryPolling) Discover(ctx context.Context, limit int) (DirectoryDiscoveryResult, error) {
	empty := DirectoryDiscoveryResult{}
	if d == nil || d.reader == nil || ctx == nil || limit < 1 || limit > 50 {
		return empty, contract.ErrInvalidArgument
	}
	checkpoint, err := d.records.DirectoryCheckpoint(ctx, d.issuer, d.organization)
	if err != nil {
		return empty, err
	}
	page, err := d.reader.List(ctx, checkpoint.StartIndex, int64(limit))
	if err != nil {
		return empty, err
	}
	subjects := make([]string, 0, len(page.Users))
	for _, user := range page.Users {
		if user.ExternalID != "" {
			subjects = append(subjects, user.ID)
		}
	}
	next := page.NextStartIndex
	if next == 0 {
		next = 1
	}
	if err := d.records.CommitDirectoryPage(ctx, d.issuer, d.organization, checkpoint, next, subjects); err != nil {
		return empty, err
	}
	return DirectoryDiscoveryResult{len(subjects), page.NextStartIndex == 0}, nil
}

func (d *DirectoryPolling) ObserveKnown(ctx context.Context, after string, limit int) (DirectoryObservationResult, error) {
	empty := DirectoryObservationResult{}
	if d == nil || d.records == nil || ctx == nil || limit < 1 || limit > 50 || (after != "" && !directoryID.MatchString(after)) {
		return empty, contract.ErrInvalidArgument
	}
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	subjects, err := d.records.DirectorySubjects(read, d.issuer, d.organization, after, limit)
	cancel()
	if err != nil {
		return empty, err
	}
	result := DirectoryObservationResult{Observed: len(subjects)}
	for _, subject := range subjects {
		observed, err := d.observe(ctx, subject)
		if err != nil {
			return empty, err
		}
		result.SessionsInvalidated += observed.SessionsInvalidated
	}
	if len(subjects) == limit {
		result.NextSubject = subjects[len(subjects)-1]
	}
	return result, nil
}
