package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type CustomAuditValidator func(string, int, map[string]any) bool

func auditEventValidator(custom CustomAuditValidator, customOnly bool) func(map[string]any) error {
	return func(event map[string]any) error {
		if err := contract.ValidateAuditShape(event); err != nil {
			return err
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return contract.ErrInvalidArgument
		}
		var typed contract.AuditEvent
		if json.Unmarshal(raw, &typed) != nil {
			return contract.ErrInvalidArgument
		}
		origin := "sdk"
		if customOnly || typed.Source == "product" {
			origin = "product"
		}
		return contract.ValidateAuditEvent(typed, origin, custom)
	}
}

type CustomAuditOptions struct {
	ProductID, Environment string
	AuditSinks             []string
	ValidateCustom         CustomAuditValidator
}

type CustomAuditEvent struct {
	EventID, EventType         string
	EventVersion               int
	OccurredAt, RecordedAt     int64
	TenantID                   *string
	Actor                      map[string]any
	Target                     map[string]any
	Action, Outcome, RequestID string
	Attributes                 map[string]any
}

type CustomAuditWriter struct {
	audit                *internal.RetainedRuntimeAudit
	product, environment string
}

// NewCustomAuditWriter is for trusted product code after resolving actor, tenant and enabled schema.
func NewCustomAuditWriter(db *sql.DB, options CustomAuditOptions) (*CustomAuditWriter, error) {
	if options.ValidateCustom == nil {
		return nil, contract.ErrInvalidArgument
	}
	audit, err := internal.NewRetainedRuntimeAudit(db, options.ProductID, options.Environment, options.AuditSinks, auditEventValidator(options.ValidateCustom, true))
	if err != nil {
		return nil, err
	}
	return &CustomAuditWriter{audit, options.ProductID, options.Environment}, nil
}

func (w *CustomAuditWriter) Record(ctx context.Context, e CustomAuditEvent) (heimdall.AuditReceipt, error) {
	empty := heimdall.AuditReceipt{}
	kind, _ := e.Actor["kind"].(string)
	if w == nil || w.audit == nil || ctx == nil ||
		(e.Outcome != "succeeded" && e.Outcome != "failed") ||
		(kind != "user" && kind != "system") {
		return empty, contract.ErrInvalidArgument
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	var tenant any
	if e.TenantID != nil {
		tenant = *e.TenantID
	}
	return w.audit.Append(ctx, map[string]any{
		"schemaVersion": float64(1), "productId": w.product, "environment": w.environment,
		"source": "product", "sdkVersion": "0.0.0", "eventId": e.EventID, "eventType": e.EventType,
		"eventVersion": float64(e.EventVersion), "occurredAt": float64(e.OccurredAt), "recordedAt": float64(e.RecordedAt),
		"tenantId": tenant, "actor": e.Actor, "target": e.Target, "action": e.Action,
		"outcome": e.Outcome, "requestId": e.RequestID, "attributes": e.Attributes,
	})
}
