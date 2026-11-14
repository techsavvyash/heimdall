package provisioning

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

// ProductSetupPlan binds one product/environment to the selected provider resources.
type ProductSetupPlan struct {
	ProductID, Environment, Issuer, OrganizationID, ProjectName, OpenFGAURL, TransportProfile string
	ExistingProjectID, ExistingStoreID, ExistingModelID                                       string
	ExistingApplications                                                                      map[string]string
}

type ProductSetupOptions struct {
	ZitadelToken, OpenFGAToken     string
	SaveSecret                     func(ctx context.Context, applicationID, clientID, secret string) error
	HasSecret                      func(ctx context.Context, applicationID, clientID string) (bool, error)
	RecoverStoreID, RecoverModelID string
}

// SetupProduct emits runtime configuration JSON keyed by manifest client.
func SetupProduct(ctx context.Context, db *sql.DB, plan ProductSetupPlan, manifestJSON []byte, options ProductSetupOptions) (map[string]json.RawMessage, error) {
	profile := plan.TransportProfile
	if profile == "" {
		profile = "production"
	}
	invalid := contract.ErrInvalidArgument
	if ctx == nil || db == nil || len(manifestJSON) > 1048576 || !contract.ValidJSONUnicode(manifestJSON) ||
		!providerID.MatchString(plan.OrganizationID) || !utf8.ValidString(plan.ProjectName) ||
		strings.TrimSpace(plan.ProjectName) == "" || utf8.RuneCountInString(plan.ProjectName) > 200 ||
		options.SaveSecret == nil || options.HasSecret == nil ||
		(plan.ExistingModelID != "" && plan.ExistingStoreID == "") ||
		(plan.ExistingStoreID != "" && options.RecoverStoreID != "") ||
		(plan.ExistingModelID != "" && options.RecoverModelID != "") ||
		(len(plan.ExistingApplications) > 0 && plan.ExistingProjectID == "") {
		return nil, invalid
	}
	for _, endpoint := range []string{plan.Issuer, plan.OpenFGAURL} {
		if contract.ValidateEndpoint(endpoint, profile) != nil {
			return nil, invalid
		}
		u, err := url.Parse(endpoint)
		if err != nil || (u.Path != "" && u.Path != "/") {
			return nil, invalid
		}
	}
	for _, token := range []string{options.ZitadelToken, options.OpenFGAToken} {
		if len(token) > 4096 || !providerToken.MatchString(token) {
			return nil, invalid
		}
	}
	for _, id := range []string{plan.ExistingProjectID, plan.ExistingStoreID, plan.ExistingModelID, options.RecoverStoreID, options.RecoverModelID} {
		if id != "" && !providerID.MatchString(id) {
			return nil, invalid
		}
	}
	var raw struct {
		Clients map[string]json.RawMessage
		Version float64
	}
	if json.Unmarshal(manifestJSON, &raw) != nil || len(raw.Clients) == 0 {
		return nil, invalid
	}
	clients := slices.Sorted(maps.Keys(raw.Clients))
	seed := configuration.Config{SchemaVersion: 1, ProductID: plan.ProductID, Environment: plan.Environment,
		Issuer: plan.Issuer, Audience: "pending", ClientID: "pending", ZitadelProjectID: "pending", ClientKey: clients[0],
		ManifestVersion: int(raw.Version), TransportProfile: profile}
	seed.OpenFGA.URL, seed.OpenFGA.StoreID, seed.OpenFGA.ModelID = plan.OpenFGAURL, "pending", "pending"
	encoded, err := json.Marshal(seed)
	if err != nil {
		return nil, invalid
	}
	deployment, err := configuration.Parse(encoded, manifestJSON)
	if err != nil {
		return nil, err
	}
	artifact, err := json.Marshal(deployment.Artifact())
	if err != nil || len(artifact) > 1048576 {
		return nil, invalid
	}
	manifest := deployment.Manifest()
	seen := map[string]bool{}
	for key, id := range plan.ExistingApplications {
		if _, ok := manifest.Clients[key]; !ok || !providerID.MatchString(id) || seen[id] {
			return nil, invalid
		}
		seen[id] = true
	}
	applications := make([][2]string, 0, len(clients))
	for _, key := range clients {
		clientProfile := manifest.Clients[key].TransportProfile
		if clientProfile == "" {
			clientProfile = "production"
		}
		if clientProfile != profile {
			return nil, invalid
		}
		applications = append(applications, [2]string{key, plan.ExistingApplications[key]})
	}
	// Keep the fingerprint bytes compatible with JavaScript JSON.stringify.
	var fingerprint strings.Builder
	encoder := json.NewEncoder(&fingerprint)
	encoder.SetEscapeHTML(false)
	err = encoder.Encode([]any{"heimdall.setup.v1", plan.ProductID, plan.Environment, plan.Issuer, plan.OrganizationID,
		plan.ProjectName, plan.OpenFGAURL, profile, fmt.Sprintf("%x", sha256.Sum256(manifestJSON)),
		plan.ExistingProjectID, plan.ExistingStoreID, plan.ExistingModelID, applications})
	if err != nil {
		return nil, invalid
	}
	scope := postgres.SetupScope{ProductID: plan.ProductID, Environment: plan.Environment,
		Fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSuffix(fingerprint.String(), "\n")))), StepCount: len(clients) + 3}
	project, err := provisionProject(ctx, db, projectOptions{scope: scope, issuer: plan.Issuer, organizationID: plan.OrganizationID,
		name: plan.ProjectName, token: options.ZitadelToken, profile: profile, existingProjectID: plan.ExistingProjectID})
	if err != nil {
		return nil, err
	}
	seed.Audience, seed.ZitadelProjectID = project, project
	ids := map[string]string{}
	for i, key := range clients {
		app, err := provisionApplication(ctx, db, applicationOptions{scope: scope, ordinal: i + 2, issuer: plan.Issuer, token: options.ZitadelToken,
			projectID: project, name: key, profile: profile, existingApplicationID: applications[i][1], redirectURIs: manifest.Clients[key].RedirectURIs,
			saveSecret: options.SaveSecret, hasSecret: options.HasSecret})
		if err != nil {
			return nil, err
		}
		ids[key] = app.ClientID
	}
	seed.ClientID = ids[clients[0]]
	encoded, err = json.Marshal(seed)
	if err != nil {
		return nil, invalid
	}
	binding, err := provisionOpenFGA(ctx, db, openFGAOptions{scope: scope, configJSON: encoded, manifestJSON: manifestJSON,
		token: options.OpenFGAToken, existingStoreID: plan.ExistingStoreID, existingModelID: plan.ExistingModelID,
		recoverStoreID: options.RecoverStoreID, recoverModelID: options.RecoverModelID})
	if err != nil {
		return nil, err
	}
	seed.OpenFGA.StoreID, seed.OpenFGA.ModelID = binding.StoreID, binding.ModelID
	result := make(map[string]json.RawMessage, len(clients))
	for _, key := range clients {
		seed.ClientKey, seed.ClientID = key, ids[key]
		raw, err := json.Marshal(seed)
		if err != nil {
			return nil, invalid
		}
		result[key] = raw
	}
	return result, nil
}

// SetupStatus contains provider IDs and dispatch state without credentials.
type SetupStatus struct {
	ProductID   string            `json:"productId"`
	Environment string            `json:"environment"`
	StepCount   int               `json:"stepCount"`
	Steps       []SetupStatusStep `json:"steps"`
}

type SetupStatusStep struct {
	Ordinal    int     `json:"ordinal"`
	State      string  `json:"state"`
	DispatchID string  `json:"dispatchId"`
	ResourceID *string `json:"resourceId"`
}

var setupSlug = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ReadSetupStatus reports committed progress using only operator database access.
func ReadSetupStatus(ctx context.Context, db *sql.DB, product, environment string) (SetupStatus, error) {
	if ctx == nil || db == nil || !setupSlug.MatchString(product) || !setupSlug.MatchString(environment) {
		return SetupStatus{}, contract.ErrInvalidArgument
	}
	count, rows, err := postgres.ReadSetupStatus(ctx, db, product, environment)
	if err != nil {
		return SetupStatus{}, err
	}
	result := SetupStatus{ProductID: product, Environment: environment, StepCount: count, Steps: make([]SetupStatusStep, 0, len(rows))}
	for i, row := range rows {
		step := SetupStatusStep{Ordinal: i + 1, State: row.State, DispatchID: row.DispatchID}
		if row.ResourceID != "" {
			step.ResourceID = &row.ResourceID
		}
		result.Steps = append(result.Steps, step)
	}
	return result, nil
}
