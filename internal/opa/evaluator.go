package opa

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/database"
)

// Evaluator provides high-level authorization evaluation methods
type Evaluator struct {
	client      *Client
	cache       *database.RedisClient
	enableCache bool
	cacheTTL    time.Duration
}

// NewEvaluator creates a new OPA evaluator
func NewEvaluator(client *Client, cache *database.RedisClient, enableCache bool) *Evaluator {
	return &Evaluator{
		client:      client,
		cache:       cache,
		enableCache: enableCache,
		cacheTTL:    5 * time.Minute, // Default cache TTL
	}
}

// SetCacheTTL sets the cache TTL
func (e *Evaluator) SetCacheTTL(ttl time.Duration) {
	e.cacheTTL = ttl
}

// CanAccessResource checks if a user can perform an action on a resource
func (e *Evaluator) CanAccessResource(
	ctx context.Context,
	userID, tenantID string,
	roles []string,
	resource, resourceID string,
	action string,
) (bool, error) {
	input := BuildPermissionCheckInput(userID, tenantID, roles, resource, action)

	if resourceID != "" {
		inputMap := input
		if resourceMap, ok := inputMap["resource"].(map[string]interface{}); ok {
			resourceMap["id"] = resourceID
		}
	}

	// Check cache first if enabled
	if e.enableCache && e.cache != nil {
		cacheKey := buildCacheKey(userID, resource, resourceID, action)
		if cached, err := e.cache.Get(ctx, cacheKey); err == nil && cached == "1" {
			return true, nil
		} else if err == nil && cached == "0" {
			return false, nil
		}
	}

	// Evaluate with OPA
	allowed, err := e.client.CheckPermission(ctx, input)
	if err != nil {
		return false, err
	}

	// Cache the result if enabled
	if e.enableCache && e.cache != nil {
		cacheKey := buildCacheKey(userID, resource, resourceID, action)
		cacheValue := "0"
		if allowed {
			cacheValue = "1"
		}
		_ = e.cache.Set(ctx, cacheKey, cacheValue, e.cacheTTL)
	}

	return allowed, nil
}

// CanAccessOwnResource checks if a user can access their own resource
func (e *Evaluator) CanAccessOwnResource(
	ctx context.Context,
	userID, tenantID string,
	resourceType, resourceID, ownerID string,
	action string,
) (bool, error) {
	input := BuildOwnershipCheckInput(userID, tenantID, resourceType, resourceID, ownerID, action)
	return e.client.CheckPermission(ctx, input)
}

// EvaluateWithContext evaluates a policy with context from Fiber
func (e *Evaluator) EvaluateWithContext(
	ctx context.Context,
	c *fiber.Ctx,
	resource, action string,
) (bool, error) {
	builder := NewContextBuilderFromFiber(c)
	builder.WithResource(resource, "")
	builder.WithAction(action)

	input := builder.Build()
	return e.client.CheckPermission(ctx, input)
}

// EvaluateCustom evaluates a custom policy with full input control
func (e *Evaluator) EvaluateCustom(
	ctx context.Context,
	policyPath string,
	input map[string]interface{},
) (*DecisionResponse, error) {
	return e.client.EvaluatePolicy(ctx, policyPath, input)
}

// BatchCheckPermissions checks multiple permissions at once
func (e *Evaluator) BatchCheckPermissions(
	ctx context.Context,
	userID, tenantID string,
	roles []string,
	permissions []PermissionCheck,
) (map[string]bool, error) {
	results := make(map[string]bool, len(permissions))

	for _, perm := range permissions {
		key := fmt.Sprintf("%s:%s:%s", perm.Resource, perm.ResourceID, perm.Action)
		allowed, err := e.CanAccessResource(
			ctx,
			userID,
			tenantID,
			roles,
			perm.Resource,
			perm.ResourceID,
			perm.Action,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to check permission %s: %w", key, err)
		}
		results[key] = allowed
	}

	return results, nil
}

// InvalidateUserCache invalidates all cached permissions for a user
func (e *Evaluator) InvalidateUserCache(ctx context.Context, userID string) error {
	if !e.enableCache || e.cache == nil {
		return nil
	}

	// Delete all keys matching pattern user:{userID}:*
	pattern := fmt.Sprintf("opa:permission:%s:*", userID)
	return e.cache.DeletePattern(ctx, pattern)
}

// PermissionCheck represents a single permission check
type PermissionCheck struct {
	Resource   string
	ResourceID string
	Action     string
}

// buildCacheKey builds a cache key for a permission check
func buildCacheKey(userID, resource, resourceID, action string) string {
	if resourceID != "" {
		return fmt.Sprintf("opa:permission:%s:%s:%s:%s", userID, resource, resourceID, action)
	}
	return fmt.Sprintf("opa:permission:%s:%s:%s", userID, resource, action)
}

// FilterAllowed filters a list of resource IDs based on permissions
// This is useful for list endpoints where you want to filter results based on access
func (e *Evaluator) FilterAllowed(
	ctx context.Context,
	userID, tenantID string,
	roles []string,
	resourceType string,
	resourceIDs []string,
	action string,
) ([]string, error) {
	var allowed []string

	for _, resourceID := range resourceIDs {
		canAccess, err := e.CanAccessResource(
			ctx,
			userID,
			tenantID,
			roles,
			resourceType,
			resourceID,
			action,
		)
		if err != nil {
			// Log error but continue checking other resources
			continue
		}
		if canAccess {
			allowed = append(allowed, resourceID)
		}
	}

	return allowed, nil
}

// EnrichInputWithPermissions adds user permissions to the input
func (e *Evaluator) EnrichInputWithPermissions(
	ctx context.Context,
	input map[string]interface{},
	userID string,
) (map[string]interface{}, error) {
	// This could fetch permissions from database and add them to input
	// For now, we'll just return the input as-is
	// TODO: Implement permission enrichment
	return input, nil
}

// Health checks if the OPA service is healthy
func (e *Evaluator) Health(ctx context.Context) error {
	return e.client.HealthCheck(ctx)
}

// GetDecisionWithDetails returns the full decision response with metrics
func (e *Evaluator) GetDecisionWithDetails(
	ctx context.Context,
	policyPath string,
	input map[string]interface{},
) (*DecisionResponse, error) {
	return e.client.EvaluatePolicy(ctx, policyPath, input)
}

// EvaluateWithFullContext evaluates with complete context including all ABAC attributes
func (e *Evaluator) EvaluateWithFullContext(
	ctx context.Context,
	builder *ContextBuilder,
) (bool, error) {
	input := builder.Build()
	return e.client.CheckPermission(ctx, input)
}
