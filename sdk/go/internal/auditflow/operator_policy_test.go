package auditflow

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
)

func TestOperatorPolicy(t *testing.T) {
	var f struct {
		Config OperatorPolicyConfig
		Cases  []struct {
			Name, Issuer, Subject, ProductID, Environment, PolicyVersion, Action, Evidence, Error string
			TenantID                                                                              *string
			Grant                                                                                 int
			Allow                                                                                 bool
		}
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/operator-policy.json")
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("invalid operator policy fixture", err)
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			config := f.Config
			config.Grants = append([]OperatorGrant(nil), f.Config.Grants...)
			grant := config.Grants[c.Grant]
			calls := 0
			policy, err := NewOperatorPolicy(config, func(_ context.Context, s MaintenanceContext) (bool, error) {
				calls++
				if s.TargetEventID != "target" || s.OperationID != "hold" || c.Evidence == "unavailable" {
					return false, contract.ErrUnavailable
				}
				return c.Evidence == "ended", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			config.Grants[c.Grant].Subject = "modified"
			scope := MaintenanceContext{
				ProductID: config.ProductID, Environment: config.Environment, PolicyVersion: config.PolicyVersion,
				Actor:              identity.Identity{Issuer: grant.Issuer, Subject: grant.Subject},
				MaintenanceRequest: MaintenanceRequest{TenantID: grant.TenantID, Action: grant.Action, TargetEventID: "target", OperationID: "hold"},
			}
			for field, value := range map[*string]string{&scope.ProductID: c.ProductID, &scope.Environment: c.Environment, &scope.PolicyVersion: c.PolicyVersion, &scope.Actor.Issuer: c.Issuer, &scope.Actor.Subject: c.Subject, &scope.Action: c.Action} {
				if value != "" {
					*field = value
				}
			}
			if c.TenantID != nil {
				scope.TenantID = c.TenantID
			}
			allowed, err := policy(context.Background(), scope)
			errorCode := ""
			if err != nil {
				errorCode = err.Error()
			}
			if allowed != c.Allow || errorCode != c.Error || (calls == 1) != (c.Evidence != "") {
				t.Fatal(allowed, err, calls)
			}
		})
	}
	grant := f.Config.Grants[0]
	for _, action := range []string{"*", "managed.export.operations", "audit.hold.retry.release"} {
		config := f.Config
		bad := grant
		bad.Action = action
		config.Grants = []OperatorGrant{bad}
		if _, err := NewOperatorPolicy(config, nil); err != contract.ErrInvalidArgument {
			t.Fatal(action, err)
		}
	}
	f.Config.Grants = []OperatorGrant{grant, grant}
	if _, err := NewOperatorPolicy(f.Config, nil); err != contract.ErrInvalidArgument {
		t.Fatal("duplicate grant", err)
	}
}
