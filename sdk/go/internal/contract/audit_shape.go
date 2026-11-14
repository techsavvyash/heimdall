package contract

import (
	"math"
	"regexp"
	"unicode/utf8"
)

var auditToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var auditType = regexp.MustCompile(`^(heimdall|product)([.][a-z][a-z0-9_]*)+$`)

func auditObject(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok && m != nil }
func auditFields(v any, required, optional []string) (map[string]any, bool) {
	m, ok := auditObject(v)
	if !ok {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
		if _, ok := m[k]; !ok {
			return nil, false
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			return nil, false
		}
	}
	return m, true
}
func auditText(v any, max int, pattern *regexp.Regexp) bool {
	s, ok := v.(string)
	return ok && utf8.ValidString(s) && utf8.RuneCountInString(s) > 0 && utf8.RuneCountInString(s) <= max && (pattern == nil || pattern.MatchString(s))
}
func auditInteger(v any, min, max float64) bool {
	n, ok := v.(float64)
	return ok && math.Trunc(n) == n && n >= min && n <= max
}
func auditEnum(v any, values ...string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, allowed := range values {
		if s == allowed {
			return true
		}
	}
	return false
}

// ValidateAuditShape accepts the decoded JSON representation used by audit ports.
// Semantic origin and actor validation must still follow this structural check.
func ValidateAuditShape(value map[string]any) error {
	e, ok := auditFields(value, []string{"schemaVersion", "eventId", "eventType", "eventVersion", "occurredAt", "recordedAt", "productId", "environment", "tenantId", "actor", "target", "action", "outcome", "requestId", "source", "sdkVersion", "attributes"}, []string{"decisionId", "operationId", "parentDecisionId", "permission", "modelId", "manifestVersion", "policyVersion", "reasonCode"})
	if !ok || e["schemaVersion"] != float64(1) || !auditText(e["eventId"], 128, auditToken) || !auditText(e["eventType"], 128, auditType) || !auditInteger(e["eventVersion"], 1, 2147483647) || !auditInteger(e["occurredAt"], 0, 9007199254740991) || !auditInteger(e["recordedAt"], 0, 9007199254740991) || !auditText(e["productId"], 64, auditToken) || !auditText(e["environment"], 64, auditToken) {
		return ErrInvalidArgument
	}
	if e["tenantId"] != nil && !auditText(e["tenantId"], 128, auditToken) {
		return ErrInvalidArgument
	}
	actor, ok := auditObject(e["actor"])
	if !ok {
		return ErrInvalidArgument
	}
	switch actor["kind"] {
	case "user":
		_, ok = auditFields(actor, []string{"kind", "issuer", "subject"}, nil)
		if !ok || !auditText(actor["issuer"], 2048, nil) || !auditText(actor["subject"], 1024, nil) {
			return ErrInvalidArgument
		}
	case "system":
		_, ok = auditFields(actor, []string{"kind", "id"}, nil)
		if !ok || !auditText(actor["id"], 128, auditToken) {
			return ErrInvalidArgument
		}
	case "unknown":
		if _, ok = auditFields(actor, []string{"kind"}, nil); !ok {
			return ErrInvalidArgument
		}
	default:
		return ErrInvalidArgument
	}
	target, ok := auditFields(e["target"], []string{"type", "id"}, nil)
	if !ok || !auditText(target["type"], 64, auditToken) || !auditText(target["id"], 8192, nil) || !auditText(e["action"], 128, nil) || !auditText(e["requestId"], 128, auditToken) || !auditText(e["sdkVersion"], 64, nil) {
		return ErrInvalidArgument
	}
	if !auditEnum(e["outcome"], "requested", "attempted", "allow", "deny", "invalid", "unavailable", "cancelled", "outcome_unknown", "succeeded", "failed") || !auditEnum(e["source"], "sdk", "product", "operator", "zitadel") {
		return ErrInvalidArgument
	}
	attributes, ok := auditObject(e["attributes"])
	if !ok || len(attributes) > 32 {
		return ErrInvalidArgument
	}
	for _, k := range []string{"decisionId", "operationId", "parentDecisionId", "modelId", "policyVersion"} {
		if v, present := e[k]; present && !auditText(v, 128, auditToken) {
			return ErrInvalidArgument
		}
	}
	if v, present := e["permission"]; present && !auditText(v, 128, nil) {
		return ErrInvalidArgument
	}
	if v, present := e["manifestVersion"]; present && !auditInteger(v, 1, 2147483647) {
		return ErrInvalidArgument
	}
	if v, present := e["reasonCode"]; present && !auditEnum(v, "unauthenticated", "forbidden", "invalid_argument", "not_found", "conflict", "unavailable", "deadline_exceeded", "cancelled", "audit_unavailable", "outcome_unknown") {
		return ErrInvalidArgument
	}
	return nil
}
