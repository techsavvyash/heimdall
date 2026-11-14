package contract

import (
	"crypto/sha256"
	"encoding/hex"
)

// RecoveryEventID binds one administration receipt to one scoped terminal event.
func RecoveryEventID(product, environment, terminal string) string {
	hash := sha256.Sum256([]byte("heimdall.recovery.v1\x00" + product + "\x00" + environment + "\x00" + terminal))
	return "recovery-" + hex.EncodeToString(hash[:])
}

// ValidateRecoveryPair requires full shape/semantic validation before this call.
func ValidateRecoveryPair(terminal, administration map[string]any, cutoff int64) error {
	target, ok := administration["target"].(map[string]any)
	actor, actorOK := administration["actor"].(map[string]any)
	at, atOK := terminal["recordedAt"].(float64)
	product, pOK := terminal["productId"].(string)
	environment, eOK := terminal["environment"].(string)
	id, idOK := terminal["eventId"].(string)
	if !ok || !actorOK || !atOK || !pOK || !eOK || !idOK || cutoff < 0 || cutoff > 9007199254740991 || terminal["eventType"] != "heimdall.authorization.completed" || terminal["outcome"] != "outcome_unknown" || terminal["source"] != "sdk" || administration["source"] != "sdk" || administration["eventType"] != "heimdall.recovery.completed" || administration["outcome"] != "succeeded" || actor["kind"] == "unknown" || administration["productId"] != terminal["productId"] || administration["environment"] != terminal["environment"] || administration["tenantId"] != terminal["tenantId"] || administration["decisionId"] != terminal["decisionId"] || target["type"] != "authorization_decision" || target["id"] != terminal["decisionId"] || administration["action"] != "authorization.recover" || terminal["occurredAt"] != at || administration["recordedAt"] != at || administration["occurredAt"] != at || at < float64(cutoff) || administration["eventId"] == id || administration["eventId"] != RecoveryEventID(product, environment, id) {
		return ErrInvalidArgument
	}
	return nil
}
