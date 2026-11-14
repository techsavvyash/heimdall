package contract

import "regexp"

const DefaultAuditRetentionMS int64 = 2592000000

type RetentionScope struct {
	ProductID   string  `json:"productId"`
	Environment string  `json:"environment"`
	TenantID    *string `json:"tenantId"`
}
type RetentionDelivery struct {
	SinkID      string `json:"sinkId"`
	State       string `json:"state"`
	DeliveredAt *int64 `json:"deliveredAt"`
}
type RetentionEvent struct {
	RetentionScope
	EventID    string              `json:"eventId"`
	RecordedAt int64               `json:"recordedAt"`
	Deliveries []RetentionDelivery `json:"deliveries"`
}
type RetentionGroup struct {
	Resolution string           `json:"resolution"`
	HoldState  string           `json:"holdState"`
	RetryState string           `json:"retryState"`
	Events     []RetentionEvent `json:"events"`
}

var retentionToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ClassifyAuditRetention plans only. Storage must validate full history and
// preserve immutable receipts in the deletion transaction; this grants no access.
func ClassifyAuditRetention(scope RetentionScope, group RetentionGroup, now, retainForMS int64) (string, error) {
	const max int64 = 9007199254740991
	if !auditScopeToken.MatchString(scope.ProductID) || !auditScopeToken.MatchString(scope.Environment) || (scope.TenantID != nil && !retentionToken.MatchString(*scope.TenantID)) || now < 0 || now > max || retainForMS < 1 || retainForMS > max {
		return "", ErrInvalidArgument
	}
	sizes := map[string]int{"ordinary": 1, "pending": 1, "completed": 2, "recovered": 3}
	size, known := sizes[group.Resolution]
	if !known || (group.HoldState != "clear" && group.HoldState != "held") || (group.RetryState != "clear" && group.RetryState != "protected") || len(group.Events) != size {
		return "", ErrAuditUnavailable
	}
	ids := map[string]bool{}
	var anchor int64
	undelivered := false
	for _, event := range group.Events {
		sameTenant := (scope.TenantID == nil && event.TenantID == nil) || (scope.TenantID != nil && event.TenantID != nil && *scope.TenantID == *event.TenantID)
		if event.ProductID != scope.ProductID || event.Environment != scope.Environment || !sameTenant || !retentionToken.MatchString(event.EventID) || ids[event.EventID] || event.RecordedAt < 0 || event.RecordedAt > max || event.Deliveries == nil || len(event.Deliveries) > 32 {
			return "", ErrAuditUnavailable
		}
		ids[event.EventID] = true
		if event.RecordedAt > anchor {
			anchor = event.RecordedAt
		}
		sinks := map[string]bool{}
		for _, delivery := range event.Deliveries {
			if !retentionToken.MatchString(delivery.SinkID) || sinks[delivery.SinkID] || (delivery.State != "pending" && delivery.State != "delivered" && delivery.State != "quarantined") || (delivery.State == "delivered" && (delivery.DeliveredAt == nil || *delivery.DeliveredAt < 0 || *delivery.DeliveredAt > max)) || (delivery.State != "delivered" && delivery.DeliveredAt != nil) {
				return "", ErrAuditUnavailable
			}
			sinks[delivery.SinkID] = true
			if delivery.State == "delivered" {
				if *delivery.DeliveredAt > anchor {
					anchor = *delivery.DeliveredAt
				}
			} else {
				undelivered = true
			}
		}
	}
	if group.HoldState == "held" {
		return "retain_held", nil
	}
	if group.Resolution == "pending" {
		return "retain_unresolved", nil
	}
	if undelivered {
		return "retain_undelivered", nil
	}
	if group.RetryState == "protected" {
		return "retain_retry", nil
	}
	if anchor > now || now-anchor < retainForMS {
		return "retain_age", nil
	}
	return "candidate", nil
}
