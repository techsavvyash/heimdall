// Package provisioning contains operator-side product setup.
package provisioning

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type projectOptions struct {
	scope                                                           postgres.SetupScope
	issuer, organizationID, name, token, profile, existingProjectID string
}

const projectService = "/zitadel.project.v2.ProjectService/"

var providerID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var providerToken = regexp.MustCompile(`^[\x21-\x7e]+$`)

func provisionProject(ctx context.Context, db *sql.DB, o projectOptions) (string, error) {
	profile := o.profile
	if profile == "" {
		profile = "production"
	}
	if ctx == nil || contract.ValidateEndpoint(o.issuer, profile) != nil ||
		!providerID.MatchString(o.organizationID) || (o.existingProjectID != "" && !providerID.MatchString(o.existingProjectID)) ||
		!utf8.ValidString(o.name) || strings.TrimSpace(o.name) == "" || utf8.RuneCountInString(o.name) > 200 || len(o.token) > 4096 || !providerToken.MatchString(o.token) {
		return "", contract.ErrInvalidArgument
	}
	parsed, err := url.Parse(o.issuer)
	if err != nil || (parsed.Path != "" && parsed.Path != "/") {
		return "", contract.ErrInvalidArgument
	}
	dispatchID, err := newDispatchID()
	if err != nil {
		return "", err
	}
	step, err := postgres.ClaimSetupStep(ctx, db, o.scope, 1, dispatchID)
	if err != nil {
		return "", err
	}
	projectID := o.existingProjectID
	if projectID == "" {
		projectID = step.DispatchID
	}
	if step.State == "confirmed" && step.ResourceID != projectID {
		return "", contract.ErrConflict
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if step.State == "claimed" && o.existingProjectID == "" {
		var result struct{ ProjectID string }
		found, err := zitadelCall(ctx, client, o.issuer, o.token, projectService+"CreateProject", map[string]string{"projectId": projectID, "organizationId": o.organizationID, "name": o.name}, &result)
		if err != nil || !found || result.ProjectID != projectID {
			return "", contract.NewError(contract.OutcomeUnknown, step.DispatchID)
		}
	}
	var result struct {
		Project *struct {
			ProjectID, OrganizationID, Name, State                             string
			ProjectRoleAssertion, AuthorizationRequired, ProjectAccessRequired bool
		}
	}
	found, err := zitadelCall(ctx, client, o.issuer, o.token, projectService+"GetProject", map[string]string{"projectId": projectID}, &result)
	if err != nil {
		return "", err
	}
	if !found && step.State != "confirmed" && o.existingProjectID == "" {
		return "", contract.NewError(contract.OutcomeUnknown, step.DispatchID)
	}
	p := result.Project
	if !found || p == nil || p.ProjectID != projectID || p.OrganizationID != o.organizationID ||
		p.Name != o.name || p.State != "PROJECT_STATE_ACTIVE" ||
		p.ProjectRoleAssertion || p.AuthorizationRequired || p.ProjectAccessRequired {
		return "", contract.NewError(contract.Conflict, step.DispatchID)
	}
	if step.State != "confirmed" {
		if err := postgres.ConfirmSetupStep(ctx, db, o.scope, 1, step.DispatchID, projectID); err != nil {
			return "", err
		}
	}
	return projectID, nil
}
