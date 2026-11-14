package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
)

type AuditHoldRequest struct {
	ProductID     string  `json:"productId"`
	Environment   string  `json:"environment"`
	TenantID      *string `json:"tenantId"`
	HoldID        string  `json:"holdId"`
	EventID       string  `json:"eventId"`
	Purpose       string  `json:"purpose"`
	Operation     string  `json:"operation"`
	At            int64   `json:"at"`
	PolicyVersion string  `json:"policyVersion"`
}

func AuditHoldEventID(r AuditHoldRequest) string {
	h := sha256.Sum256([]byte("heimdall.audit-hold.v1\x00" + r.ProductID + "\x00" + r.Environment + "\x00" + r.HoldID + "\x00" + r.Operation))
	return "hold-" + hex.EncodeToString(h[:])
}

var holdToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidateAuditHold binds administration, not actor authority or the end of a retry obligation.
func ValidateAuditHold(r AuditHoldRequest, event map[string]any) error {
	if !auditScopeToken.MatchString(r.ProductID) || !auditScopeToken.MatchString(r.Environment) || (r.TenantID != nil && !holdToken.MatchString(*r.TenantID)) || !holdToken.MatchString(r.HoldID) || !holdToken.MatchString(r.EventID) || !holdToken.MatchString(r.PolicyVersion) || (r.Purpose != "administrative" && r.Purpose != "retry") || (r.Operation != "acquire" && r.Operation != "release") || r.At < 0 || r.At > 9007199254740991 {
		return ErrInvalidArgument
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return ErrInvalidArgument
	}
	var normalized map[string]any
	var typed AuditEvent
	if json.Unmarshal(raw, &normalized) != nil || ValidateAuditShape(normalized) != nil || json.Unmarshal(raw, &typed) != nil || ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false }) != nil {
		return ErrInvalidArgument
	}
	event = normalized
	target := event["target"].(map[string]any)
	actor := event["actor"].(map[string]any)
	var tenant any
	if r.TenantID != nil {
		tenant = *r.TenantID
	}
	_, decision := event["decisionId"]
	if event["eventId"] != AuditHoldEventID(r) || event["eventId"] == r.EventID || event["eventType"] != "heimdall.policy.completed" || event["source"] != "sdk" || event["outcome"] != "succeeded" || actor["kind"] == "unknown" || event["productId"] != r.ProductID || event["environment"] != r.Environment || event["tenantId"] != tenant || event["operationId"] != r.HoldID || event["policyVersion"] != r.PolicyVersion || event["action"] != "audit.hold."+r.Purpose+"."+r.Operation || target["type"] != "audit_event" || target["id"] != r.EventID || event["recordedAt"] != float64(r.At) || event["occurredAt"] != float64(r.At) || decision {
		return ErrInvalidArgument
	}
	return nil
}
