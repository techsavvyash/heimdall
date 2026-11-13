package auditflow

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
)

type TenantSSOPolicy struct {
	OrganizationID, ProviderID, PolicyVersion, Protocol string
	MaxAgeMS                                            int64
}
type TenantSSOPolicySource func(context.Context, string) (*TenantSSOPolicy, error)

func (a *Authentication) permitsTenant(ctx context.Context, actor *identity.Principal, tenant string) (bool, error) {
	if a.tenantSSO == nil || tenant == "" {
		return true, nil
	}
	policy, err := a.tenantSSO(ctx, tenant)
	if cancellation := contract.CheckCancellation(ctx); cancellation != nil {
		return false, cancellation
	}
	if err != nil {
		return false, contract.ErrUnavailable
	}
	if policy == nil {
		return true, nil
	}
	p := *policy
	for _, value := range []string{p.OrganizationID, p.ProviderID, p.PolicyVersion} {
		if !utf8.ValidString(value) || value == "" || utf8.RuneCountInString(value) > 128 {
			return false, contract.ErrUnavailable
		}
	}
	if (p.Protocol != "saml" && p.Protocol != "oidc") || p.MaxAgeMS <= 0 || p.MaxAgeMS > 9007199254740991 {
		return false, contract.ErrUnavailable
	}
	id := actor.Identity()
	scope := contract.EnterpriseScope{
		ProductID: a.writer.product, Environment: a.writer.environment, ClientID: a.clientID,
		Issuer: id.Issuer, Subject: id.Subject, TenantID: tenant, OrganizationID: p.OrganizationID,
		PolicyVersion: p.PolicyVersion, ProviderID: p.ProviderID, Protocol: p.Protocol,
	}
	err = contract.ValidateEnterpriseEvidence(identity.EnterpriseEvidence(actor), scope, a.writer.now().UnixMilli(), p.MaxAgeMS)
	if errors.Is(err, contract.ErrUnauthenticated) {
		return false, nil
	}
	if err != nil {
		return false, contract.ErrUnavailable
	}
	return true, nil
}
