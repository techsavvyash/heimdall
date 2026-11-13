package provisioning

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

const applicationService = "/zitadel.application.v2.ApplicationService/"

type applicationOptions struct {
	scope                                                          postgres.SetupScope
	ordinal                                                        int
	issuer, token, projectID, name, profile, existingApplicationID string
	redirectURIs                                                   []string
	saveSecret                                                     func(context.Context, string, string, string) error
	hasSecret                                                      func(context.Context, string, string) (bool, error)
}

type applicationResult struct{ ApplicationID, ClientID string }

type oidcApplication struct {
	RedirectURIs    []string `json:"redirectUris"`
	ResponseTypes   []string `json:"responseTypes"`
	GrantTypes      []string `json:"grantTypes"`
	ApplicationType string   `json:"applicationType"`
	AuthMethodType  string   `json:"authMethodType"`
	AccessTokenType string   `json:"accessTokenType"`
	ClientID        string   `json:"clientId,omitempty"`
	ClientSecret    string   `json:"clientSecret,omitempty"`
	NonCompliant    bool     `json:"nonCompliant,omitempty"`
	DevelopmentMode bool     `json:"developmentMode,omitempty"`
}

func provisionApplication(ctx context.Context, db *sql.DB, o applicationOptions) (applicationResult, error) {
	empty := applicationResult{}
	profile := o.profile
	if profile == "" {
		profile = "production"
	}
	parsed, parseErr := url.Parse(o.issuer)
	if ctx == nil || parseErr != nil || contract.ValidateEndpoint(o.issuer, profile) != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		len(o.token) > 4096 || !providerToken.MatchString(o.token) ||
		!providerID.MatchString(o.projectID) || !providerID.MatchString(o.name) ||
		(o.existingApplicationID != "" && !providerID.MatchString(o.existingApplicationID)) ||
		o.ordinal < 2 || o.ordinal > o.scope.StepCount || len(o.redirectURIs) < 1 || len(o.redirectURIs) > 32 ||
		o.saveSecret == nil || o.hasSecret == nil {
		return empty, contract.ErrInvalidArgument
	}
	seen := make(map[string]bool)
	for _, uri := range o.redirectURIs {
		if seen[uri] || contract.ValidateEndpoint(uri, profile) != nil || !callbackPattern.MatchString(uri) {
			return empty, contract.ErrInvalidArgument
		}
		seen[uri] = true
	}
	expected := oidcApplication{
		RedirectURIs: slices.Clone(o.redirectURIs), ResponseTypes: []string{"OIDC_RESPONSE_TYPE_CODE"},
		GrantTypes:      []string{"OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"},
		ApplicationType: "OIDC_APP_TYPE_WEB", AuthMethodType: "OIDC_AUTH_METHOD_TYPE_BASIC", AccessTokenType: "OIDC_TOKEN_TYPE_JWT",
	}
	id, err := newDispatchID()
	if err != nil {
		return empty, err
	}
	step, err := postgres.ClaimSetupStep(ctx, db, o.scope, o.ordinal, id)
	if err != nil {
		return empty, err
	}
	id = o.existingApplicationID
	if id == "" {
		id = step.DispatchID
	}
	conflict := contract.NewError(contract.Conflict, step.DispatchID)
	unknown := contract.NewError(contract.OutcomeUnknown, step.DispatchID)
	if step.State == "confirmed" && step.ResourceID != id {
		return empty, conflict
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var createdClientID string
	if step.State == "claimed" && o.existingApplicationID == "" {
		var created struct {
			ApplicationID     string
			OIDCConfiguration oidcApplication
		}
		found, err := zitadelCall(ctx, client, o.issuer, o.token, applicationService+"CreateApplication",
			map[string]any{"projectId": o.projectID, "applicationId": id, "name": o.name, "oidcConfiguration": expected}, &created)
		oidc := created.OIDCConfiguration
		if err != nil || !found || created.ApplicationID != id || !contract.ValidClientID(oidc.ClientID) ||
			oidc.ClientSecret == "" || len(oidc.ClientSecret) > 4096 {
			return empty, unknown
		}
		createdClientID = oidc.ClientID
		if err = secretOperation(ctx, func(ctx context.Context) error { return o.saveSecret(ctx, id, oidc.ClientID, oidc.ClientSecret) }); err != nil {
			return empty, unknown
		}
	}
	var observed struct {
		Application *struct {
			ApplicationID, ProjectID, Name, State string
			OIDCConfiguration                     *oidcApplication
		}
	}
	found, err := zitadelCall(ctx, client, o.issuer, o.token, applicationService+"GetApplication", map[string]string{"applicationId": id}, &observed)
	if err != nil {
		return empty, err
	}
	if !found || observed.Application == nil {
		if step.State == "confirmed" || o.existingApplicationID != "" {
			return empty, conflict
		}
		return empty, unknown
	}
	app := observed.Application
	oidc := app.OIDCConfiguration
	if app.ApplicationID != id || app.ProjectID != o.projectID || app.Name != o.name || app.State != "APPLICATION_STATE_ACTIVE" ||
		oidc == nil || !contract.ValidClientID(oidc.ClientID) || (createdClientID != "" && oidc.ClientID != createdClientID) ||
		(oidc.ApplicationType != "" && oidc.ApplicationType != expected.ApplicationType) ||
		(oidc.AuthMethodType != "" && oidc.AuthMethodType != expected.AuthMethodType) ||
		oidc.AccessTokenType != expected.AccessTokenType || oidc.NonCompliant || oidc.DevelopmentMode ||
		!sameValues(oidc.RedirectURIs, expected.RedirectURIs) || !sameValues(oidc.ResponseTypes, expected.ResponseTypes) ||
		!sameValues(oidc.GrantTypes, expected.GrantTypes) {
		return empty, conflict
	}
	if err := secretOperation(ctx, func(ctx context.Context) error {
		ok, err := o.hasSecret(ctx, id, oidc.ClientID)
		if err != nil || !ok {
			return unknown
		}
		return nil
	}); err != nil {
		return empty, unknown
	}
	if step.State != "confirmed" {
		if err := postgres.ConfirmSetupStep(ctx, db, o.scope, o.ordinal, step.DispatchID, id); err != nil {
			return empty, err
		}
	}
	return applicationResult{id, oidc.ClientID}, nil
}

var callbackPattern = regexp.MustCompile(`^https?://[^/]+/`)

func sameValues(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, value := range expected {
		if !slices.Contains(actual, value) {
			return false
		}
	}
	return true
}

func secretOperation(ctx context.Context, run func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		if err := contract.CheckCancellation(ctx); err != nil {
			done <- err
			return
		}
		done <- run(ctx)
	}()
	select {
	case <-ctx.Done():
		return contract.CheckCancellation(ctx)
	case err := <-done:
		return err
	}
}
