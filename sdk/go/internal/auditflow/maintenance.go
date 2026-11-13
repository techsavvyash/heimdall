package auditflow

import (
	"context"
	"regexp"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
)

// MaintenanceRequest is internal metadata supplied by the bound deployment.
type MaintenanceRequest struct {
	TenantID        *string `json:"tenantId"`
	Action          string  `json:"action"`
	TargetEventID   string  `json:"targetEventId"`
	OperationID     string  `json:"operationId"`
	ModelID         string  `json:"modelId"`
	ManifestVersion int     `json:"manifestVersion"`
}

type MaintenanceContext struct {
	MaintenanceRequest
	ProductID, Environment, PolicyVersion string
	Actor                                 identity.Identity
}

var maintenanceToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// RunMaintenance authenticates the caller and durably records the scoped
// decision before invoking the trusted maintenance port. Policy is read-only.
func (a *Authentication) RunMaintenance(
	ctx context.Context, raw string, request MaintenanceRequest,
	policy func(context.Context, MaintenanceContext) (bool, error),
	apply func(context.Context, MaintenanceContext) (contract.AuditReceipt, error),
) (contract.AuditReceipt, error) {
	if apply == nil {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	return runMaintenance(a, ctx, raw, request, policy, func(ctx context.Context, scope MaintenanceContext, _ func() error) (contract.AuditReceipt, error) {
		return apply(ctx, scope)
	}, "audit_event")
}

func runMaintenance[T any](
	a *Authentication, ctx context.Context, raw string, request MaintenanceRequest,
	policy func(context.Context, MaintenanceContext) (bool, error),
	apply func(context.Context, MaintenanceContext, func() error) (T, error),
	targetType string,
) (T, error) {
	var empty T
	if a == nil || ctx == nil || policy == nil || apply == nil ||
		contract.MaintenanceTargetType(request.Action) != targetType ||
		(request.TenantID != nil && !maintenanceToken.MatchString(*request.TenantID)) ||
		!maintenanceToken.MatchString(request.TargetEventID) || !maintenanceToken.MatchString(request.OperationID) ||
		!maintenanceToken.MatchString(request.ModelID) || request.ManifestVersion < 1 || request.ManifestVersion > 2147483647 {
		return empty, contract.ErrInvalidArgument
	}
	// Preserve the exact nullable scope across callbacks, including a policy
	// callback that retains or mutates its own input.
	clone := func(r MaintenanceRequest) MaintenanceRequest {
		if r.TenantID != nil {
			tenant := *r.TenantID
			r.TenantID = &tenant
		}
		return r
	}
	r := clone(request)
	if e := contract.CheckCancellation(ctx); e != nil {
		return empty, e
	}
	principal, e := a.Verify(ctx, raw, "bearer")
	if e != nil {
		return empty, e
	}
	actor := principal.Identity()
	contextFor := func() MaintenanceContext {
		return MaintenanceContext{clone(r), a.writer.product, a.writer.environment, a.policy, actor}
	}
	var tenant any
	if r.TenantID != nil {
		tenant = *r.TenantID
	}
	at := float64(a.writer.now().UnixMilli())
	event := map[string]any{
		"schemaVersion": float64(1), "eventId": "pending", "eventType": "heimdall.authorization.requested",
		"eventVersion": float64(1), "occurredAt": at, "recordedAt": at,
		"productId": a.writer.product, "environment": a.writer.environment, "tenantId": tenant,
		"actor":  map[string]any{"kind": "user", "issuer": actor.Issuer, "subject": actor.Subject},
		"target": map[string]any{"type": targetType, "id": r.TargetEventID},
		"action": r.Action, "permission": r.Action, "outcome": "requested", "requestId": r.OperationID,
		"source": "sdk", "sdkVersion": "0.0.0", "attributes": map[string]any{},
		"decisionId": "pending", "operationId": r.OperationID, "modelId": r.ModelID,
		"manifestVersion": float64(r.ManifestVersion), "policyVersion": a.policy,
	}
	allowed, e := a.writer.Run(ctx, event, func(ctx context.Context) (bool, error) { return policy(ctx, contextFor()) })
	if e != nil {
		return empty, e
	}
	if !allowed {
		return empty, contract.ErrForbidden
	}
	checkCurrent := func() error {
		if e := contract.CheckCancellation(ctx); e != nil {
			return e
		}
		return a.verifier.CheckCurrent(ctx, principal)
	}
	if e = checkCurrent(); e != nil {
		return empty, e
	}
	return apply(ctx, contextFor(), checkCurrent)
}
