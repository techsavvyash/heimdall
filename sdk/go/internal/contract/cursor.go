package contract

type CursorScope struct{ ProductID, Environment, Principal, ModelID string }
type CursorPayload struct {
	CursorScope
	IssuedAt, ExpiresAt int64
	Position            string
}

// ValidateCursorScope follows signature and structural verification.
func ValidateCursorScope(p CursorPayload, scope CursorScope, now int64) error {
	if now < 0 || now > 9007199254740991 || p.IssuedAt > now ||
		p.ExpiresAt <= p.IssuedAt || now >= p.ExpiresAt || p.CursorScope != scope {
		return ErrInvalidArgument
	}
	return nil
}
