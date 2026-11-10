package contract

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
