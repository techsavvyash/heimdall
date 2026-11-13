package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type DirectoryEnrollmentInput struct {
	Subject, WorkspaceID, RuleID string
	Roles                        []string
}

var directoryRole = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func NewDirectoryEnrollment(db *sql.DB, product, environment string, options provisioning.DirectoryReaderOptions) (func(context.Context, *heimdall.Client, *heimdall.Principal, DirectoryEnrollmentInput) (heimdall.MembershipStatus, error), error) {
	reader, err := provisioning.NewDirectoryReader(options)
	if err != nil {
		return nil, err
	}
	records, err := internal.NewRetainedRuntimeIdentityRecords(db, product, environment)
	if err != nil {
		return nil, err
	}
	issuer, org := options.Issuer, options.OrganizationID
	return func(ctx context.Context, runtime *heimdall.Client, actor *heimdall.Principal, input DirectoryEnrollmentInput) (heimdall.MembershipStatus, error) {
		empty := heimdall.MembershipStatus{}
		if ctx == nil || runtime == nil || !directoryID.MatchString(input.Subject) ||
			!directoryID.MatchString(input.WorkspaceID) || !directoryID.MatchString(input.RuleID) ||
			len(input.Roles) > 128 {
			return empty, contract.ErrInvalidArgument
		}
		roles := append([]string{}, input.Roles...)
		slices.Sort(roles)
		for i, role := range roles {
			if !directoryRole.MatchString(role) || (i > 0 && roles[i-1] == role) {
				return empty, contract.ErrInvalidArgument
			}
		}
		if actor == nil || actor.Identity().Issuer != issuer {
			return empty, contract.ErrForbidden
		}
		p, env, _ := actor.Scope()
		if p != product || env != environment {
			return empty, contract.ErrForbidden
		}
		memberships := runtime.Tenant(input.WorkspaceID).Memberships()
		if memberships == nil {
			return empty, contract.ErrUnavailable
		}
		actorKey, err := contract.EncodeIdentity(issuer, actor.Identity().Subject)
		if err != nil {
			return empty, err
		}
		var key bytes.Buffer
		encoder := json.NewEncoder(&key)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode([]string{product, environment, issuer, org, input.Subject, input.WorkspaceID, input.RuleID}); err != nil {
			return empty, contract.ErrInvalidArgument
		}
		digest := sha256.Sum256(bytes.TrimSuffix(key.Bytes(), []byte("\n")))
		observed, err := reader.Read(ctx, input.Subject)
		if err != nil {
			return empty, err
		}
		if observed.State != "active" || observed.ExternalID == "" {
			return empty, contract.ErrForbidden
		}
		if err := records.RecordDirectoryEnrollment(ctx, issuer, org, input.Subject, input.WorkspaceID, input.RuleID, append([]string{actorKey}, roles...)); err != nil {
			return empty, err
		}
		result, err := memberships.Add(ctx, actor, heimdall.MembershipRolesInput{
			MembershipInput: heimdall.MembershipInput{
				Target:         heimdall.Identity{Issuer: issuer, Subject: input.Subject},
				IdempotencyKey: "directory-enroll:" + hex.EncodeToString(digest[:]),
			}, Roles: roles,
		})
		if err != nil {
			return empty, err
		}
		if result.Status == "confirmed" {
			if err := records.ConfirmDirectoryEnrollment(ctx, issuer, org, input.Subject, input.WorkspaceID, input.RuleID, result.OperationID); err != nil {
				return empty, err
			}
		}
		return result, nil
	}, nil
}
