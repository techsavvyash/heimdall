package main

import (
	"context"
	"encoding/json"
	"os"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
)

func tenantSSOPolicy() (heimdall.TenantSSOPolicySource, error) {
	federationPolicies := os.Getenv("HEIMDALL_TENANT_SSO_POLICIES")
	if federationPolicies != "" {
		var policies map[string]heimdall.TenantSSOPolicy
		if json.Unmarshal([]byte(federationPolicies), &policies) != nil || policies == nil {
			return nil, heimdall.ErrInvalidArgument
		}
		return func(_ context.Context, workspace string) (*heimdall.TenantSSOPolicy, error) {
			policy, found := policies[workspace]
			if !found {
				return nil, nil
			}
			return &policy, nil
		}, nil
	} else if os.Getenv("HEIMDALL_REFERENCE_STRICT_SSO") == "1" {
		return func(_ context.Context, workspaceID string) (*heimdall.TenantSSOPolicy, error) {
			if workspaceID != "globex" {
				return nil, nil
			}
			return &heimdall.TenantSSOPolicy{
				OrganizationID: "reference-globex", ProviderID: "reference-globex-oidc",
				PolicyVersion: "reference-sso-v1", Protocol: "oidc", MaxAgeMS: 900_000,
			}, nil
		}, nil
	}
	return nil, nil
}
