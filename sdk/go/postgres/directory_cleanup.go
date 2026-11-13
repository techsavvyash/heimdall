package postgres

import (
	"context"
	"database/sql"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type DirectoryCleanupItem = internal.DirectoryCleanupItem

type DirectoryCleanup struct {
	records              *internal.IdentityRecords
	issuer, organization string
}

var directoryID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func NewDirectoryCleanup(db *sql.DB, product, environment, issuer, organization, transportProfile string) (*DirectoryCleanup, error) {
	if transportProfile == "" {
		transportProfile = "production"
	}
	if !directoryID.MatchString(organization) || contract.ValidateEndpoint(issuer, transportProfile) != nil {
		return nil, contract.ErrInvalidArgument
	}
	parsed, _ := url.Parse(issuer)
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, contract.ErrInvalidArgument
	}
	records, err := internal.NewRetainedRuntimeIdentityRecords(db, product, environment)
	if err != nil {
		return nil, err
	}
	return &DirectoryCleanup{records, issuer, organization}, nil
}

func cleanupRevision(value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
		return 0, contract.ErrInvalidArgument
	}
	return n, nil
}

// Pending scans a bounded page. Use an empty after value to restart from the oldest pending item.
func (d *DirectoryCleanup) Pending(ctx context.Context, after string, limit int) ([]DirectoryCleanupItem, error) {
	if d == nil || d.records == nil || ctx == nil || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidArgument
	}
	var cursor int64
	if after != "" {
		n, err := cleanupRevision(after)
		if err != nil {
			return nil, err
		}
		cursor = n
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return d.records.PendingDirectoryCleanup(ctx, d.issuer, d.organization, cursor, limit)
}

// Complete acknowledges a revision after audited, recoverable membership operations are confirmed.
func (d *DirectoryCleanup) Complete(ctx context.Context, subject, revision string) error {
	if d == nil || d.records == nil || ctx == nil || !directoryID.MatchString(subject) {
		return contract.ErrInvalidArgument
	}
	n, err := cleanupRevision(revision)
	if err != nil {
		return err
	}
	return d.records.CompleteDirectoryCleanup(ctx, d.issuer, d.organization, subject, n)
}
