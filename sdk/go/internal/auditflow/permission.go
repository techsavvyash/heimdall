package auditflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
)

func (a *Authentication) CheckPermission(ctx context.Context, principal *identity.Principal, tenant, permission, model string, version int, evaluate func(context.Context) (bool, error)) (bool, error) {
	return a.CheckLinkedPermission(ctx, principal, tenant, permission, model, version, "", func(ctx context.Context, _ string) (bool, error) { return evaluate(ctx) })
}

func (a *Authentication) CheckLinkedPermission(ctx context.Context, principal *identity.Principal, tenant, permission, model string, version int, parent string, evaluate func(context.Context, string) (bool, error)) (bool, error) {
	if !a.Owns(principal) {
		return false, contract.ErrUnauthenticated
	}
	at := float64(a.writer.now().UnixMilli())
	var request [16]byte
	rand.Read(request[:])
	actor := principal.Identity()
	event := map[string]any{
		"schemaVersion": float64(1), "eventId": "pending", "eventType": "heimdall.authorization.requested", "eventVersion": float64(1),
		"occurredAt": at, "recordedAt": at, "productId": a.writer.product, "environment": a.writer.environment, "tenantId": tenant,
		"actor":  map[string]any{"kind": "user", "issuer": actor.Issuer, "subject": actor.Subject},
		"target": map[string]any{"type": "workspace", "id": tenant}, "action": permission, "permission": permission,
		"outcome": "requested", "requestId": hex.EncodeToString(request[:]), "source": "sdk", "sdkVersion": contract.SDKVersion,
		"attributes": map[string]any{}, "decisionId": "pending", "modelId": model, "manifestVersion": float64(version), "policyVersion": a.policy,
	}
	if parent != "" {
		event["parentDecisionId"] = parent
	}
	if tenant == "" {
		event["tenantId"] = nil
		event["target"] = map[string]any{"type": "product", "id": a.writer.product}
	}
	allowed, err := a.writer.run(ctx, event, func(ctx context.Context, decisionID string) (bool, error) {
		if err := a.verifier.CheckCurrent(ctx, principal); err != nil {
			return false, err
		}
		if allowed, err := a.permitsTenant(ctx, principal, tenant); err != nil || !allowed {
			return false, err
		}
		return evaluate(ctx, decisionID)
	})
	if err != nil || !allowed {
		return false, err
	}
	if err := a.verifier.CheckCurrent(ctx, principal); err != nil {
		return false, err
	}
	return true, nil
}

// LoadCredential bounds a deployment-owned credential callback for managed provisioning.
func LoadCredential(ctx context.Context, load func(context.Context) (string, error)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	value, err := bounded(ctx, load)
	if cancellation := contract.CheckCancellation(ctx); cancellation != nil {
		return "", cancellation
	}
	if err != nil {
		return "", contract.ErrUnavailable
	}
	return value, nil
}
