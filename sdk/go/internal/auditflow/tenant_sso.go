package auditflow

import (
	"context"
	"errors"
	"regexp"
	"time"
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

var loginTenantID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (a *Authentication) VerifyEnterpriseProviderTest(ctx context.Context, actor *identity.Principal, tenant string, p TenantSSOPolicy) error {
	if ctx == nil || !loginTenantID.MatchString(tenant) || !loginTenantID.MatchString(p.OrganizationID) ||
		!loginTenantID.MatchString(p.ProviderID) || !loginTenantID.MatchString(p.PolicyVersion) ||
		(p.Protocol != "oidc" && p.Protocol != "saml") || p.MaxAgeMS <= 0 || p.MaxAgeMS > 9007199254740991 {
		return contract.ErrInvalidArgument
	}
	if !a.Owns(actor) {
		return contract.ErrUnauthenticated
	}
	if err := a.verifier.CheckCurrent(ctx, actor); err != nil {
		return err
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	id := actor.Identity()
	scope := contract.EnterpriseScope{
		ProductID: a.writer.product, Environment: a.writer.environment, ClientID: a.clientID,
		Issuer: id.Issuer, Subject: id.Subject, TenantID: tenant, OrganizationID: p.OrganizationID,
		PolicyVersion: p.PolicyVersion, ProviderID: p.ProviderID, Protocol: p.Protocol,
	}
	return contract.ValidateEnterpriseEvidence(identity.EnterpriseEvidence(actor), scope, a.writer.now().UnixMilli(), min(p.MaxAgeMS, 300000))
}

func LoadLoginPolicy(ctx context.Context, tenant string, source TenantSSOPolicySource) (contract.LoginPolicy, int64, error) {
	if !loginTenantID.MatchString(tenant) {
		return contract.LoginPolicy{}, 0, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	policy, err := bounded(ctx, func(ctx context.Context) (*TenantSSOPolicy, error) { return source(ctx, tenant) })
	if e := contract.CheckCancellation(ctx); e != nil {
		return contract.LoginPolicy{}, 0, e
	}
	if err != nil {
		return contract.LoginPolicy{}, 0, contract.ErrUnavailable
	}
	if policy == nil || policy.MaxAgeMS <= 0 || policy.MaxAgeMS > 9007199254740991 {
		return contract.LoginPolicy{}, 0, contract.ErrUnauthenticated
	}
	return contract.LoginPolicy{TenantID: tenant, OrganizationID: policy.OrganizationID, PolicyVersion: policy.PolicyVersion,
		ProviderID: policy.ProviderID, Protocol: policy.Protocol, StrictSSO: true}, policy.MaxAgeMS, nil
}
