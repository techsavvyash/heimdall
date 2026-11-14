package provisioning

import (
	"context"
	"maps"
	"net/url"
	"slices"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
)

type ModelRolloutDeployment struct {
	ConfigJSON, ManifestJSON []byte
}

type ModelRolloutReport struct {
	Compatible               bool     `json:"compatible"`
	OwnerChanged             bool     `json:"ownerChanged"`
	RoleMappingChanged       []string `json:"roleMappingChanged"`
	PermissionMappingChanged []string `json:"permissionMappingChanged"`
	PermissionPolicyChanged  []string `json:"permissionPolicyChanged"`
	DelegationChanged        []string `json:"delegationChanged"`
}

// InspectModelRollout compares two pinned, published models for the same product store.
// It reads models only. Operators must still drain old writers before activating new roles.
func InspectModelRollout(ctx context.Context, current, candidate ModelRolloutDeployment, token string) (ModelRolloutReport, error) {
	var report ModelRolloutReport
	if ctx == nil || len(token) > 4096 || !providerToken.MatchString(token) {
		return report, contract.ErrInvalidArgument
	}
	old, err := configuration.Parse(current.ConfigJSON, current.ManifestJSON)
	if err != nil {
		return report, err
	}
	next, err := configuration.Parse(candidate.ConfigJSON, candidate.ManifestJSON)
	if err != nil {
		return report, err
	}
	a, b := old.Config(), next.Config()
	profile := a.TransportProfile
	if profile == "" {
		profile = "production"
	}
	nextProfile := b.TransportProfile
	if nextProfile == "" {
		nextProfile = "production"
	}
	endpoint, err := url.Parse(a.OpenFGA.URL)
	if err != nil || (endpoint.Path != "" && endpoint.Path != "/") ||
		a.ProductID != b.ProductID || a.Environment != b.Environment || a.Issuer != b.Issuer || a.Audience != b.Audience ||
		a.ZitadelProjectID != b.ZitadelProjectID || a.OpenFGA.URL != b.OpenFGA.URL ||
		a.OpenFGA.StoreID != b.OpenFGA.StoreID || a.OpenFGA.ModelID == b.OpenFGA.ModelID ||
		b.ManifestVersion <= a.ManifestVersion || profile != nextProfile {
		return report, contract.ErrInvalidArgument
	}
	for _, deployment := range []*configuration.Deployment{old, next} {
		c := deployment.Config()
		pinned, err := modelbinding.New(
			modelbinding.Config{URL: c.OpenFGA.URL, StoreID: c.OpenFGA.StoreID, ModelID: c.OpenFGA.ModelID, Profile: profile},
			deployment.Manifest(),
			contract.Binding{ProductID: c.ProductID, ManifestVersion: c.ManifestVersion, ClientKey: c.ClientKey, TransportProfile: profile},
			deployment.Artifact(),
			func(context.Context) (string, error) { return token, nil },
		)
		if err != nil {
			return report, err
		}
		if _, err := pinned.Verify(ctx); err != nil {
			return report, err
		}
	}

	previous, proposed := old.Manifest(), next.Manifest()
	priorModel, nextModel := old.Artifact(), next.Artifact()
	report = ModelRolloutReport{
		OwnerChanged:       previous.OwnerRole != proposed.OwnerRole,
		RoleMappingChanged: []string{}, PermissionMappingChanged: []string{},
		PermissionPolicyChanged: []string{}, DelegationChanged: []string{},
	}
	for _, name := range slices.Sorted(maps.Keys(previous.Roles)) {
		if priorModel.Roles[name] != nextModel.Roles[name] {
			report.RoleMappingChanged = append(report.RoleMappingChanged, name)
		}
		if !slices.Equal(rolesContaining(previous.Delegation, name), rolesContaining(proposed.Delegation, name)) {
			report.DelegationChanged = append(report.DelegationChanged, name)
		}
	}
	for _, name := range slices.Sorted(slices.Values(previous.Permissions)) {
		if priorModel.Permissions[name] != nextModel.Permissions[name] {
			report.PermissionMappingChanged = append(report.PermissionMappingChanged, name)
		}
		if !slices.Equal(rolesContaining(previous.Roles, name), rolesContaining(proposed.Roles, name)) {
			report.PermissionPolicyChanged = append(report.PermissionPolicyChanged, name)
		}
	}
	report.Compatible = !report.OwnerChanged && len(report.RoleMappingChanged)+len(report.PermissionMappingChanged)+len(report.PermissionPolicyChanged)+len(report.DelegationChanged) == 0
	return report, nil
}

func rolesContaining(values map[string][]string, name string) []string {
	var roles []string
	for role, entries := range values {
		if slices.Contains(entries, name) {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	return roles
}
