package auditflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"time"
)

// Authentication releases identity results only after a matching durable receipt.
type Authentication struct {
	verifier      *identity.IdentityVerifier
	writer        *Decision
	policy        string
	releaseWindow int64
	clientID      string
	tenantSSO     TenantSSOPolicySource
}

func NewAuthentication(config identity.StatusConfig, secret func(context.Context) (string, error), gate identity.Gate, append Append, now func() time.Time, tenantSSO ...TenantSSOPolicySource) (*Authentication, error) {
	writer, err := New(config.ProductID, config.Environment, append, contract.ValidateAuditShape, now)
	if err != nil {
		return nil, err
	}
	verifier, err := identity.NewIdentityVerifier(config, secret, gate, now)
	if err != nil {
		return nil, err
	}
	window := int64(60000)
	if config.FreshnessMs != nil {
		window = *config.FreshnessMs
	}
	if window == 0 {
		window = 5000
	}
	a := &Authentication{verifier: verifier, writer: writer, policy: config.PolicyVersion, releaseWindow: window, clientID: config.ClientID}
	if len(tenantSSO) != 0 {
		a.tenantSSO = tenantSSO[0]
	}
	return a, nil
}
func (a *Authentication) Owns(p *identity.Principal) bool { return a.verifier.Owns(p) }
func (a *Authentication) Verify(ctx context.Context, raw, sessionContext string) (*identity.Principal, error) {
	operation, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	principal, failure := bounded(operation, func(ctx context.Context) (*identity.Principal, error) {
		return a.verifier.Verify(ctx, raw, sessionContext)
	})
	if failure == nil {
		failure = contract.CheckCancellation(operation)
	}
	outcome := "succeeded"
	var code contract.Code
	if failure != nil {
		var typed *contract.Error
		if errors.As(failure, &typed) {
			code = typed.Code
		} else {
			code = contract.Unavailable
		}
		failure = contract.NewError(code, "")
		switch code {
		case contract.Cancelled:
			outcome = "cancelled"
		case contract.InvalidArgument:
			outcome = "invalid"
		case contract.Unauthenticated, contract.Forbidden:
			outcome = "deny"
		default:
			outcome = "unavailable"
		}
	}
	ids := make([]byte, 32)
	if _, err := rand.Read(ids); err != nil {
		return nil, contract.ErrAuditUnavailable
	}
	at := float64(a.writer.now().UnixMilli())
	actor := map[string]any{"kind": "unknown"}
	if principal != nil {
		id := principal.Identity()
		actor = map[string]any{"kind": "user", "issuer": id.Issuer, "subject": id.Subject}
	}
	event := map[string]any{"schemaVersion": float64(1), "eventId": hex.EncodeToString(ids[:16]), "eventType": "heimdall.authentication.completed", "eventVersion": float64(1), "occurredAt": at, "recordedAt": at, "productId": a.writer.product, "environment": a.writer.environment, "tenantId": nil, "actor": actor, "target": map[string]any{"type": "product", "id": a.writer.product}, "action": "authenticate", "outcome": outcome, "requestId": hex.EncodeToString(ids[16:]), "source": "sdk", "sdkVersion": "0.0.0", "policyVersion": a.policy, "attributes": map[string]any{}}
	if failure != nil {
		event["reasonCode"] = string(code)
	}
	if a.writer.commit(event) != nil {
		return nil, contract.ErrAuditUnavailable
	}
	if failure != nil {
		return nil, failure
	}

	releaseError := contract.CheckCancellation(operation)
	releasedAt := a.writer.now().UnixMilli()
	if releaseError == nil {
		if releasedAt >= principal.ExpiresAt() {
			releaseError = contract.ErrUnauthenticated
		} else if releasedAt < principal.ValidatedAt() || releasedAt-principal.ValidatedAt() >= a.releaseWindow {
			releaseError = contract.ErrUnavailable
		}
	}
	if releaseError != nil {
		var rejected *contract.Error
		_ = errors.As(releaseError, &rejected)
		if _, err := rand.Read(ids[:16]); err != nil {
			return nil, contract.ErrAuditUnavailable
		}
		event["eventId"] = hex.EncodeToString(ids[:16])
		event["recordedAt"] = max(at, float64(a.writer.now().UnixMilli()))
		event["occurredAt"] = event["recordedAt"]
		event["reasonCode"] = string(rejected.Code)
		event["outcome"] = "unavailable"
		if rejected.Code == contract.Cancelled {
			event["outcome"] = "cancelled"
		} else if rejected.Code == contract.Unauthenticated {
			event["outcome"] = "deny"
		}
		if a.writer.commit(event) != nil {
			return nil, contract.ErrAuditUnavailable
		}
		return nil, releaseError
	}
	return principal, nil
}
