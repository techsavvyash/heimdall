package contract

import "crypto/subtle"

type LoginPolicy struct {
	TenantID       string `json:"tenantId"`
	OrganizationID string `json:"organizationId"`
	PolicyVersion  string `json:"policyVersion"`
	ProviderID     string `json:"providerId,omitempty"`
	Protocol       string `json:"protocol,omitempty"`
	StrictSSO      bool   `json:"strictSSO"`
}
type LoginScope struct {
	ProductID            string       `json:"productId"`
	Environment          string       `json:"environment"`
	Issuer               string       `json:"issuer"`
	ClientID             string       `json:"clientId"`
	RedirectURI          string       `json:"redirectUri"`
	ReturnToKey          string       `json:"returnToKey"`
	BrowserBindingDigest string       `json:"browserBindingDigest"`
	TenantPolicy         *LoginPolicy `json:"tenantPolicy"`
}
type LoginRecord struct {
	LoginScope
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
	State     string `json:"state"`
	ClaimedAt *int64 `json:"claimedAt,omitempty"`
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
