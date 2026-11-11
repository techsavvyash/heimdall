package contract

import (
	"encoding/json"
	"sort"
)

type RetentionSource struct {
	Event        map[string]any `json:"event"`
	Digest       string         `json:"digest"`
	Routing      []string       `json:"routing"`
	DeliveryRows int            `json:"deliveryRows"`
}
type RetainedIdentity struct {
	RetentionScope
	EventID    string   `json:"eventId"`
	RecordedAt int64    `json:"recordedAt"`
	Digest     string   `json:"digest"`
	Kind       string   `json:"kind"`
	DecisionID *string  `json:"decisionId"`
	Routing    []string `json:"routing"`
}

// BuildRetentionIdentities validates decoded built-in history, not purge authority.
func BuildRetentionIdentities(scope RetentionScope, resolution string, sources []RetentionSource) ([]RetainedIdentity, error) {
	if !auditScopeToken.MatchString(scope.ProductID) || !auditScopeToken.MatchString(scope.Environment) || (scope.TenantID != nil && !retentionToken.MatchString(*scope.TenantID)) {
		return nil, ErrInvalidArgument
	}
	bad := ErrAuditUnavailable
	size, ok := map[string]int{"ordinary": 1, "pending": 1, "completed": 2, "recovered": 3}[resolution]
	if !ok || len(sources) != size {
		return nil, bad
	}
	result := make([]RetainedIdentity, 0, size)
	ids := map[string]bool{}
	var decisionEvents []map[string]any
	var terminal, admin map[string]any
	for _, s := range sources {
		digest, err := AuditDigest(s.Event)
		if err != nil || digest != s.Digest {
			return nil, bad
		}
		raw, err := json.Marshal(s.Event)
		if err != nil {
			return nil, bad
		}
		var e map[string]any
		var typed AuditEvent
		if json.Unmarshal(raw, &e) != nil || ValidateAuditShape(e) != nil || json.Unmarshal(raw, &typed) != nil || ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false }) != nil {
			return nil, bad
		}
		sameTenant := (scope.TenantID == nil && typed.TenantID == nil) || (scope.TenantID != nil && typed.TenantID != nil && *scope.TenantID == *typed.TenantID)
		id := e["eventId"].(string)
		if e["productId"] != scope.ProductID || e["environment"] != scope.Environment || !sameTenant || ids[id] || s.Routing == nil || len(s.Routing) > 32 || s.DeliveryRows != len(s.Routing) {
			return nil, bad
		}
		ids[id] = true
		sinks := map[string]bool{}
		routing := make([]string, len(s.Routing))
		copy(routing, s.Routing)
		for _, sink := range routing {
			if !retentionToken.MatchString(sink) || sinks[sink] {
				return nil, bad
			}
			sinks[sink] = true
		}
		sort.Strings(routing)
		kind := "ordinary"
		switch e["eventType"] {
		case "heimdall.authorization.requested":
			kind = "request"
			decisionEvents = append(decisionEvents, e)
		case "heimdall.authorization.completed":
			kind = "terminal"
			terminal = e
			decisionEvents = append(decisionEvents, e)
		case "heimdall.recovery.completed":
			if e["action"] == "authorization.recover" {
				kind = "recovery"
				admin = e
			}
		}
		if (resolution == "ordinary") != (kind == "ordinary") {
			return nil, bad
		}
		var decisionID *string
		if kind != "ordinary" {
			value, ok := e["decisionId"].(string)
			if !ok {
				return nil, bad
			}
			decisionID = &value
		}
		snapshot := scope
		if scope.TenantID != nil {
			value := *scope.TenantID
			snapshot.TenantID = &value
		}
		result = append(result, RetainedIdentity{snapshot, id, typed.RecordedAt, digest, kind, decisionID, routing})
	}
	if resolution != "ordinary" {
		binding, err := BindAuditDecision(scope.ProductID, scope.Environment, decisionEvents)
		if err != nil || binding.Resolution != resolution {
			return nil, bad
		}
		if resolution == "recovered" && (terminal == nil || admin == nil || ValidateRecoveryPair(terminal, admin, binding.RequestedAt) != nil) {
			return nil, bad
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EventID < result[j].EventID })
	return result, nil
}
