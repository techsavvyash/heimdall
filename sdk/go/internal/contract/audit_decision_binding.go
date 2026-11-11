package contract

import (
	"encoding/json"
	"regexp"
)

type AuditDecisionBinding struct {
	ProductID       string  `json:"productId"`
	Environment     string  `json:"environment"`
	DecisionID      string  `json:"decisionId"`
	RequestEventID  string  `json:"requestEventId"`
	RequestedAt     int64   `json:"requestedAt"`
	Resolution      string  `json:"resolution"`
	TerminalEventID *string `json:"terminalEventId"`
	ResolvedAt      *int64  `json:"resolvedAt"`
}

var auditScopeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// BindAuditDecision correlates decoded, digest-verified history, not raw storage or a migration.
func BindAuditDecision(product, environment string, events []map[string]any) (*AuditDecisionBinding, error) {
	if !auditScopeToken.MatchString(product) || !auditScopeToken.MatchString(environment) {
		return nil, ErrInvalidArgument
	}
	bad := ErrAuditUnavailable
	if len(events) < 1 || len(events) > 2 {
		return nil, bad
	}
	var request, terminal map[string]any
	for _, original := range events {
		if _, e := AuditDigest(original); e != nil {
			return nil, bad
		}
		raw, e := json.Marshal(original)
		if e != nil {
			return nil, bad
		}
		var event map[string]any
		if json.Unmarshal(raw, &event) != nil || ValidateAuditShape(event) != nil {
			return nil, bad
		}
		var typed AuditEvent
		if json.Unmarshal(raw, &typed) != nil || ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false }) != nil {
			return nil, bad
		}
		if event["productId"] != product || event["environment"] != environment || event["source"] != "sdk" {
			return nil, bad
		}
		if event["eventType"] == "heimdall.authorization.requested" && request == nil {
			request = event
		} else if event["eventType"] == "heimdall.authorization.completed" && terminal == nil {
			terminal = event
		} else {
			return nil, bad
		}
	}
	if request == nil {
		return nil, bad
	}
	contextDigest := func(event map[string]any) (string, error) {
		copy := map[string]any{}
		for key, value := range event {
			switch key {
			case "eventId", "eventType", "outcome", "occurredAt", "recordedAt", "reasonCode", "sdkVersion":
			default:
				copy[key] = value
			}
		}
		return AuditDigest(copy)
	}
	result := &AuditDecisionBinding{ProductID: product, Environment: environment, DecisionID: request["decisionId"].(string), RequestEventID: request["eventId"].(string), RequestedAt: int64(request["recordedAt"].(float64)), Resolution: "pending"}
	if terminal != nil {
		a, e := contextDigest(request)
		b, f := contextDigest(terminal)
		if e != nil || f != nil || a != b || terminal["eventId"] == request["eventId"] || terminal["recordedAt"].(float64) < request["recordedAt"].(float64) || terminal["occurredAt"].(float64) < request["occurredAt"].(float64) {
			return nil, bad
		}
		id := terminal["eventId"].(string)
		at := int64(terminal["recordedAt"].(float64))
		result.TerminalEventID = &id
		result.ResolvedAt = &at
		result.Resolution = "completed"
		if terminal["outcome"] == "outcome_unknown" {
			result.Resolution = "recovered"
		}
	}
	return result, nil
}
