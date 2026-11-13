package auditflow

import (
	"context"
	"regexp"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type OperatorGrant struct {
	Issuer, Subject, Action string
	TenantID                *string
}

type OperatorPolicyConfig struct {
	ProductID, Environment, PolicyVersion string
	Grants                                []OperatorGrant
}

type operatorGrantKey struct {
	issuer, subject, action, tenant string
}

var operatorScopeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewOperatorPolicy snapshots a deployment's exact grants. Retry hold release
// also requires evidence about the individual obligation from the deployment.
func NewOperatorPolicy(config OperatorPolicyConfig, retryEnded func(context.Context, MaintenanceContext) (bool, error)) (func(context.Context, MaintenanceContext) (bool, error), error) {
	if !operatorScopeToken.MatchString(config.ProductID) || !operatorScopeToken.MatchString(config.Environment) || !maintenanceToken.MatchString(config.PolicyVersion) {
		return nil, contract.ErrInvalidArgument
	}
	grants := make(map[operatorGrantKey]struct{}, len(config.Grants))
	for _, grant := range config.Grants {
		if _, err := contract.EncodeIdentity(grant.Issuer, grant.Subject); err != nil ||
			contract.MaintenanceTargetType(grant.Action) == "" ||
			(grant.TenantID != nil && !maintenanceToken.MatchString(*grant.TenantID)) ||
			(grant.Action == "managed.export.operations" && grant.TenantID == nil) ||
			(grant.Action == "audit.hold.retry.release" && retryEnded == nil) {
			return nil, contract.ErrInvalidArgument
		}
		tenant := ""
		if grant.TenantID != nil {
			tenant = *grant.TenantID
		}
		key := operatorGrantKey{grant.Issuer, grant.Subject, grant.Action, tenant}
		if _, duplicate := grants[key]; duplicate {
			return nil, contract.ErrInvalidArgument
		}
		grants[key] = struct{}{}
	}
	product, environment, version := config.ProductID, config.Environment, config.PolicyVersion
	return func(ctx context.Context, scope MaintenanceContext) (bool, error) {
		if ctx == nil {
			return false, contract.ErrInvalidArgument
		}
		if err := contract.CheckCancellation(ctx); err != nil {
			return false, err
		}
		if scope.ProductID != product || scope.Environment != environment || scope.PolicyVersion != version ||
			(scope.TenantID != nil && !maintenanceToken.MatchString(*scope.TenantID)) {
			return false, nil
		}
		tenant := ""
		if scope.TenantID != nil {
			tenant = *scope.TenantID
		}
		if _, allowed := grants[operatorGrantKey{scope.Actor.Issuer, scope.Actor.Subject, scope.Action, tenant}]; !allowed {
			return false, nil
		}
		if scope.Action == "audit.hold.retry.release" {
			return retryEnded(ctx, scope)
		}
		return true, nil
	}, nil
}
