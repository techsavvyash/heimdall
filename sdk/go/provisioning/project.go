// Package provisioning contains operator-side product setup.
package provisioning

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", contract.ErrUnavailable
	}
	random[6] = random[6]&0x0f | 0x40
	random[8] = random[8]&0x3f | 0x80
	dispatchID := fmt.Sprintf("%x-%x-%x-%x-%x", random[:4], random[4:6], random[6:8], random[8:10], random[10:])
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
		found, err := projectCall(ctx, client, o, "CreateProject", map[string]string{"projectId": projectID, "organizationId": o.organizationID, "name": o.name}, &result)
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
	found, err := projectCall(ctx, client, o, "GetProject", map[string]string{"projectId": projectID}, &result)
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

func projectCall(ctx context.Context, client *http.Client, o projectOptions, method string, payload map[string]string, result any) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, contract.ErrInvalidArgument
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(o.issuer, "/")+"/zitadel.project.v2.ProjectService/"+method, bytes.NewReader(raw))
	if err != nil {
		return false, contract.ErrInvalidArgument
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+o.token)
	resp, err := client.Do(req)
	if err != nil {
		if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
			return false, cancelled
		}
		return false, contract.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, contract.ErrUnavailable
	}
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) > limit || json.Unmarshal(data, result) != nil {
		return false, contract.ErrUnavailable
	}
	return true, nil
}
