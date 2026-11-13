package contract

import (
	"math"
	"strings"
	"unicode/utf8"
)

var AuditTypes = map[string][]string{
	"heimdall.authorization.requested":  {"requested"},
	"heimdall.authorization.completed":  {"allow", "deny", "invalid", "unavailable", "cancelled", "outcome_unknown"},
	"heimdall.membership.intent":        {"requested"},
	"heimdall.membership.attempt":       {"attempted"},
	"heimdall.membership.completed":     {"succeeded", "failed", "outcome_unknown"},
	"heimdall.ownership.intent":         {"requested"},
	"heimdall.ownership.attempt":        {"attempted"},
	"heimdall.ownership.completed":      {"succeeded", "failed", "outcome_unknown"},
	"heimdall.authentication.completed": {"succeeded", "deny", "invalid", "unavailable", "cancelled"},
	"heimdall.recovery.completed":       {"succeeded", "failed", "outcome_unknown"},
	"heimdall.policy.completed":         {"succeeded", "failed"},
	"heimdall.export.completed":         {"succeeded", "failed"},
	"heimdall.identity.observed":        {"succeeded", "failed"},
}

type AuditEvent struct {
	ProductID                                                   string `json:"productId"`
	EventType                                                   string
	EventVersion                                                int
	OccurredAt, RecordedAt                                      int64
	Actor                                                       struct{ Kind string }
	TenantID                                                    *string
	Source, Outcome, Action                                     string
	Target                                                      struct{ Type, ID string }
	Attributes                                                  map[string]any
	DecisionID, OperationID, Permission, ModelID, PolicyVersion string
	ManifestVersion                                             int
}

var forbiddenAttributes = map[string]bool{
	"token": true, "accesstoken": true, "refreshtoken": true, "idtoken": true, "password": true, "secret": true,
	"clientsecret": true, "authorization": true, "cookie": true, "setcookie": true, "ciphertext": true,
	"actor": true, "productid": true, "environment": true, "tenantid": true, "outcome": true, "eventid": true,
	"__proto__": true, "constructor": true, "prototype": true,
}

func attributeWeight(value any, depth int) (int, error) {
	if depth > 8 {
		return 0, ErrInvalidArgument
	}
	switch v := value.(type) {
	case nil, bool:
		return 1, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 9007199254740991 {
			return 0, ErrInvalidArgument
		}
		return 9, nil
	case string:
		if !utf8.ValidString(v) || len(v) > 4096 {
			return 0, ErrInvalidArgument
		}
		return 1 + len(v), nil
	case []any:
		if len(v) > 32 {
			return 0, ErrInvalidArgument
		}
		size := 1
		for _, item := range v {
			weight, err := attributeWeight(item, depth+1)
			if err != nil {
				return 0, err
			}
			size += weight
			if size > 32768 {
				return 0, ErrInvalidArgument
			}
		}
		return size, nil
	case map[string]any:
		if len(v) > 32 {
			return 0, ErrInvalidArgument
		}
		size := 1
		for key, item := range v {
			normal := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			if !validToken(key, 64) || forbiddenAttributes[key] || forbiddenAttributes[normal] {
				return 0, ErrInvalidArgument
			}
			weight, err := attributeWeight(item, depth+1)
			if err != nil {
				return 0, err
			}
			size += len(key) + weight
			if size > 32768 {
				return 0, ErrInvalidArgument
			}
		}
		return size, nil
	default:
		return 0, ErrInvalidArgument
	}
}

// ValidateAuditEvent follows structural validation. Origin is trusted call context.
func ValidateAuditEvent(e AuditEvent, origin string, custom func(string, int, map[string]any) bool) error {
	weight, err := attributeWeight(e.Attributes, 0)
	if err != nil || weight > 32768 || e.OccurredAt > e.RecordedAt {
		return ErrInvalidArgument
	}
	if origin == "product" {
		if !strings.HasPrefix(e.EventType, "product.") || e.Source != "product" || custom == nil || !custom(e.EventType, e.EventVersion, e.Attributes) {
			return ErrInvalidArgument
		}
		return nil
	}
	if origin != "sdk" {
		return ErrInvalidArgument
	}
	outcomes, ok := AuditTypes[e.EventType]
	if !ok || e.EventVersion != 1 || e.Source == "product" || len(e.Attributes) != 0 {
		return ErrInvalidArgument
	}
	found := false
	for _, o := range outcomes {
		if o == e.Outcome {
			found = true
		}
	}
	if !found {
		return ErrInvalidArgument
	}
	if (e.Outcome == "allow" || (e.EventType == "heimdall.authentication.completed" && e.Outcome == "succeeded")) && e.Actor.Kind != "user" {
		return ErrInvalidArgument
	}
	hasTenant := e.TenantID != nil && *e.TenantID != ""
	if strings.HasPrefix(e.EventType, "heimdall.authorization.") && (e.DecisionID == "" || e.ModelID == "" || e.ManifestVersion == 0 || ((e.Outcome == "allow" || e.Outcome == "deny") && ((!hasTenant && !maintenanceAuditScope(e) && !discoveryAuditScope(e)) || e.Permission == ""))) {
		return ErrInvalidArgument
	}
	if MaintenanceTargetType(e.Action) == "managed_export" &&
		(strings.HasPrefix(e.EventType, "heimdall.authorization.") || e.EventType == "heimdall.export.completed") {
		target := "inventory"
		if hasTenant {
			target = *e.TenantID
		}
		if e.Source != "sdk" || e.Actor.Kind != "user" || e.OperationID == "" || e.PolicyVersion == "" ||
			e.Target.Type != "managed_export" || e.Target.ID != target || (!hasTenant && e.Action == "managed.export.operations") ||
			(strings.HasPrefix(e.EventType, "heimdall.authorization.") && e.Permission != e.Action) {
			return ErrInvalidArgument
		}
	}
	if (strings.HasPrefix(e.EventType, "heimdall.membership.") || strings.HasPrefix(e.EventType, "heimdall.ownership.")) &&
		(e.OperationID == "" || !hasTenant || e.ModelID == "" || e.ManifestVersion == 0 || e.Actor.Kind == "unknown") {
		return ErrInvalidArgument
	}
	if e.EventType == "heimdall.policy.completed" && e.PolicyVersion == "" {
		return ErrInvalidArgument
	}
	return nil
}

// MaintenanceTargetType binds reserved actions to their audit target type.
func MaintenanceTargetType(action string) string {
	switch action {
	case "audit.retention.retire", "audit.hold.administrative.acquire", "audit.hold.administrative.release", "audit.hold.retry.acquire", "audit.hold.retry.release":
		return "audit_event"
	case "managed.export.workspaces", "managed.export.operations":
		return "managed_export"
	}
	return ""
}

func maintenanceAuditScope(e AuditEvent) bool {
	return e.TenantID == nil && e.Source == "sdk" && e.Actor.Kind == "user" &&
		e.Target.Type != "" && e.Target.Type == MaintenanceTargetType(e.Action) && e.OperationID != "" && e.PolicyVersion != "" &&
		e.Permission == e.Action && e.Action != "managed.export.operations"
}

func discoveryAuditScope(e AuditEvent) bool {
	return e.TenantID == nil && e.Source == "sdk" && e.Actor.Kind == "user" &&
		e.Action == "workspaces.list" && e.Permission == e.Action && e.Target.Type == "product" && e.ProductID != "" && e.Target.ID == e.ProductID
}
