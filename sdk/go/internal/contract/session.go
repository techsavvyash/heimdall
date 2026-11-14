package contract

type SessionScope struct{ ProductID, Environment, Issuer, Subject, PolicyVersion string }
type SessionRecord struct {
	SessionScope
	EnterpriseEvidence                                                                                     *EnterpriseEvidence
	CreatedAt, LastActivityAt, AbsoluteExpiresAt, IdleExpiresAt, AccessTokenExpiresAt, IdentityValidatedAt int64
	State, RefreshToken                                                                                    string
	RefreshAttempt                                                                                         *struct {
		ID        string
		ExpiresAt int64
	}
}

// SessionStatus examines a schema-validated decrypted record, not authentication.
func SessionStatus(r SessionRecord, scope SessionScope, now, freshnessMs int64) (string, error) {
	if freshnessMs < 0 || freshnessMs > 60000 || now < 0 || now > 9007199254740991 ||
		r.CreatedAt > r.LastActivityAt || r.LastActivityAt > now ||
		r.LastActivityAt >= r.IdleExpiresAt || r.IdleExpiresAt > r.AbsoluteExpiresAt ||
		r.IdentityValidatedAt < r.CreatedAt || r.IdentityValidatedAt > now ||
		(r.RefreshAttempt != nil && (r.RefreshAttempt.ExpiresAt > r.AbsoluteExpiresAt || r.RefreshAttempt.ExpiresAt <= r.CreatedAt)) {
		return "", ErrInvalidArgument
	}
	if r.SessionScope != scope || now >= r.AbsoluteExpiresAt || now >= r.IdleExpiresAt {
		return "", ErrUnauthenticated
	}
	if r.State == "reauth_required" {
		return "reauth_required", nil
	}
	if r.State == "refreshing" {
		if now >= r.RefreshAttempt.ExpiresAt {
			return "reauth_required", nil
		}
		return "refreshing", nil
	}
	if now >= r.AccessTokenExpiresAt {
		if r.RefreshToken != "" {
			return "refresh_required", nil
		}
		return "reauth_required", nil
	}
	if now-r.IdentityValidatedAt >= freshnessMs {
		return "identity_revalidation_required", nil
	}
	return "active", nil
}
