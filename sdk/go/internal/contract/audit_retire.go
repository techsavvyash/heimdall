package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

type AuditRetirementRequest struct {
	ProductID     string  `json:"productId"`
	Environment   string  `json:"environment"`
	TenantID      *string `json:"tenantId"`
	RetirementID  string  `json:"retirementId"`
	EventID       string  `json:"eventId"`
	At            int64   `json:"at"`
	RetainForMS   int64   `json:"retainForMs"`
	PolicyVersion string  `json:"policyVersion"`
}

func AuditRetirementEventID(r AuditRetirementRequest) string {
	h := sha256.Sum256([]byte("heimdall.audit-retirement.v1\x00" + r.ProductID + "\x00" + r.Environment + "\x00" + r.RetirementID + "\x00" + strconv.FormatInt(r.RetainForMS, 10)))
	return "retirement-" + hex.EncodeToString(h[:])
}

// ValidateAuditRetirement binds an already validated event to its retirement.
func ValidateAuditRetirement(r AuditRetirementRequest, event map[string]any) error {
	if !auditScopeToken.MatchString(r.ProductID) || !auditScopeToken.MatchString(r.Environment) ||
		(r.TenantID != nil && !holdToken.MatchString(*r.TenantID)) ||
		!holdToken.MatchString(r.RetirementID) || !holdToken.MatchString(r.EventID) ||
		!holdToken.MatchString(r.PolicyVersion) || r.At < 0 || r.At > 9007199254740991 ||
		r.RetainForMS < 1 || r.RetainForMS > 9007199254740991 {
		return ErrInvalidArgument
	}
	id := AuditRetirementEventID(r)
	target, _ := event["target"].(map[string]any)
	actor, _ := event["actor"].(map[string]any)
	var tenant any
	if r.TenantID != nil {
		tenant = *r.TenantID
	}
	_, decision := event["decisionId"]
	if event["eventId"] != id || id == r.EventID || event["eventType"] != "heimdall.policy.completed" ||
		event["source"] != "sdk" || event["outcome"] != "succeeded" ||
		(actor["kind"] != "system" && actor["kind"] != "user") || event["productId"] != r.ProductID ||
		event["environment"] != r.Environment || event["tenantId"] != tenant ||
		event["operationId"] != r.RetirementID || event["policyVersion"] != r.PolicyVersion ||
		event["action"] != "audit.retention.retire" || target["type"] != "audit_event" ||
		target["id"] != r.EventID || event["recordedAt"] != float64(r.At) ||
		event["occurredAt"] != float64(r.At) || decision {
		return ErrInvalidArgument
	}
	return nil
}
