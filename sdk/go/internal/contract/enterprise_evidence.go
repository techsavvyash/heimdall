package contract

import "unicode/utf8"

type EnterpriseScope struct {
	ProductID, Environment, Issuer, Subject, ClientID, TenantID, OrganizationID, PolicyVersion, ProviderID, Protocol string
}
type EnterpriseEvidence struct {
	EnterpriseScope
	UpstreamSubject, ProviderSessionID string
	AuthTime, VerifiedAt               int64
}

// ValidateEnterpriseEvidence requires schema-validated authenticated stored evidence.
// Scope and maxAgeMs come from current trusted policy. This does not verify tokens.
func ValidateEnterpriseEvidence(e *EnterpriseEvidence, scope EnterpriseScope, now, maxAgeMs int64) error {
	if now < 0 || now > 9007199254740991 || maxAgeMs <= 0 || maxAgeMs > 9007199254740991 {
		return ErrInvalidArgument
	}
	if e == nil {
		return ErrUnauthenticated
	}
	if e.VerifiedAt > now || e.AuthTime != (e.VerifiedAt/1000)*1000 {
		return ErrInvalidArgument
	}
	if e.EnterpriseScope != scope || now-e.AuthTime >= maxAgeMs {
		return ErrUnauthenticated
	}
	return nil
}

// ReadEnterpriseEvidence validates the optional evidence object in a decrypted session.
func ReadEnterpriseEvidence(raw any) (*EnterpriseEvidence, error) {
	fields, ok := raw.(map[string]any)
	if !ok || len(fields) != 15 || fields["schemaVersion"] != float64(1) {
		return nil, ErrUnavailable
	}
	e := &EnterpriseEvidence{}
	for key, spec := range map[string]struct {
		out *string
		max int
	}{
		"productId": {&e.ProductID, 64}, "environment": {&e.Environment, 64},
		"issuer": {&e.Issuer, 2048}, "subject": {&e.Subject, 1024},
		"clientId": {&e.ClientID, 128}, "tenantId": {&e.TenantID, 128},
		"organizationId": {&e.OrganizationID, 128}, "policyVersion": {&e.PolicyVersion, 128},
		"providerId": {&e.ProviderID, 128}, "protocol": {&e.Protocol, 4},
		"upstreamSubject": {&e.UpstreamSubject, 1024}, "providerSessionId": {&e.ProviderSessionID, 128},
	} {
		text, ok := fields[key].(string)
		if !ok || text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > spec.max {
			return nil, ErrUnavailable
		}
		*spec.out = text
	}
	for key, out := range map[string]*int64{"authTime": &e.AuthTime, "verifiedAt": &e.VerifiedAt} {
		v, ok := fields[key].(float64)
		if !ok || v < 0 || v > 9007199254740991 || v != float64(int64(v)) {
			return nil, ErrUnavailable
		}
		*out = int64(v)
	}
	if e.Protocol != "saml" && e.Protocol != "oidc" {
		return nil, ErrUnavailable
	}
	return e, nil
}
