package contract

import "crypto/subtle"

type LoginPolicy struct {
	TenantID, OrganizationID, PolicyVersion string
	StrictSSO                               bool
}
type LoginScope struct {
	ProductID, Environment, Issuer, ClientID, RedirectURI, ReturnToKey, BrowserBindingDigest string
	TenantPolicy                                                                             *LoginPolicy
}
type LoginRecord struct {
	LoginScope
	CreatedAt, ExpiresAt int64
	State                string
	ClaimedAt            *int64
}

// ValidateLoginClaim follows decryption/schema validation; it does not claim storage.
func ValidateLoginClaim(record LoginRecord, scope LoginScope, now int64) error {
	if now < 0 || now > 9007199254740991 || record.CreatedAt > now || record.ExpiresAt <= record.CreatedAt || record.ExpiresAt-record.CreatedAt > 600000 || (record.ClaimedAt != nil && (*record.ClaimedAt < record.CreatedAt || *record.ClaimedAt > now)) {
		return ErrInvalidArgument
	}
	policyMatches := record.TenantPolicy == nil && scope.TenantPolicy == nil
	if record.TenantPolicy != nil && scope.TenantPolicy != nil {
		policyMatches = *record.TenantPolicy == *scope.TenantPolicy
	}
	if record.State != "pending" || now >= record.ExpiresAt || record.ProductID != scope.ProductID || record.Environment != scope.Environment || record.Issuer != scope.Issuer || record.ClientID != scope.ClientID || record.RedirectURI != scope.RedirectURI || record.ReturnToKey != scope.ReturnToKey || !policyMatches || subtle.ConstantTimeCompare([]byte(record.BrowserBindingDigest), []byte(scope.BrowserBindingDigest)) != 1 {
		return ErrUnauthenticated
	}
	return nil
}
