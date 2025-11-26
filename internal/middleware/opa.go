package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/opa"
)

// RequirePermissionOPA middleware checks if the user has permission using OPA
func RequirePermissionOPA(evaluator *opa.Evaluator, resource, action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := GetUserID(c)
		tenantID := GetTenantID(c)
		roles := GetRoles(c)

		if userID == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "User not authenticated",
					"code":    "UNAUTHORIZED",
				},
			})
		}

		// Get resource ID from route params if available
		resourceID := c.Params("id")
		if resourceID == "" {
			resourceID = c.Params(resource + "Id")
		}

		allowed, err := evaluator.CanAccessResource(
			c.Context(),
			userID,
			tenantID,
			roles,
			resource,
			resourceID,
			action,
		)

		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Failed to evaluate authorization policy",
					"code":    "AUTHZ_EVALUATION_FAILED",
					"details": err.Error(),
				},
			})
		}

		if !allowed {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied: insufficient permissions",
					"code":    "FORBIDDEN",
					"required": fiber.Map{
						"resource": resource,
						"action":   action,
					},
				},
			})
		}

		return c.Next()
	}
}

// RequireDecisionOPA evaluates a custom policy path
func RequireDecisionOPA(evaluator *opa.Evaluator, policyPath string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		builder := opa.NewContextBuilderFromFiber(c)

		// Extract resource info from route if available
		if resourceType := c.Params("resourceType"); resourceType != "" {
			builder.WithResource(resourceType, c.Params("id"))
		}

		// Set action based on HTTP method
		action := getActionFromMethod(c.Method())
		builder.WithAction(action)

		input := builder.Build()

		decision, err := evaluator.EvaluateCustom(c.Context(), policyPath, input)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Failed to evaluate authorization policy",
					"code":    "AUTHZ_EVALUATION_FAILED",
					"details": err.Error(),
				},
			})
		}

		// Check if allowed
		allowed := false
		if result, ok := decision.Result.(bool); ok {
			allowed = result
		} else if resultMap, ok := decision.Result.(map[string]interface{}); ok {
			if allow, ok := resultMap["allow"].(bool); ok {
				allowed = allow
			}
		}

		if !allowed {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied by policy",
					"code":    "FORBIDDEN",
					"policy":  policyPath,
				},
			})
		}

		// Store decision metadata in context for audit logging
		c.Locals("opaDecisionID", decision.DecisionID)
		if decision.Metrics != nil {
			c.Locals("opaMetrics", decision.Metrics)
		}

		return c.Next()
	}
}

// RequireOwnership checks if the user owns the resource or has admin rights
func RequireOwnership(evaluator *opa.Evaluator, resourceType, ownerIDParam string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := GetUserID(c)
		tenantID := GetTenantID(c)
		roles := GetRoles(c)

		if userID == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "User not authenticated",
					"code":    "UNAUTHORIZED",
				},
			})
		}

		// Get resource ID and owner ID from params
		resourceID := c.Params("id")
		ownerID := c.Params(ownerIDParam)

		// If owner ID is not in params, it might need to be fetched from the database
		// For now, we'll check if the resource ID matches the user ID for "own" resources
		if ownerID == "" {
			ownerID = resourceID
		}

		action := getActionFromMethod(c.Method())

		// Build context with ownership info
		builder := opa.NewContextBuilder()
		builder.WithUser(userID, "", roles)
		builder.WithResource(resourceType, resourceID)
		builder.WithResourceOwner(ownerID)
		builder.WithAction(action)
		builder.WithTenant(tenantID, "", nil)

		allowed, err := evaluator.EvaluateWithFullContext(c.Context(), builder)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Failed to evaluate ownership policy",
					"code":    "AUTHZ_EVALUATION_FAILED",
					"details": err.Error(),
				},
			})
		}

		if !allowed {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied: you don't own this resource",
					"code":    "FORBIDDEN",
				},
			})
		}

		return c.Next()
	}
}

// RequireAnyPermission checks if the user has any of the specified permissions
func RequireAnyPermission(evaluator *opa.Evaluator, resource string, actions []string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := GetUserID(c)
		tenantID := GetTenantID(c)
		roles := GetRoles(c)

		if userID == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "User not authenticated",
					"code":    "UNAUTHORIZED",
				},
			})
		}

		resourceID := c.Params("id")

		// Check each action until one is allowed
		for _, action := range actions {
			allowed, err := evaluator.CanAccessResource(
				c.Context(),
				userID,
				tenantID,
				roles,
				resource,
				resourceID,
				action,
			)

			if err != nil {
				continue
			}

			if allowed {
				return c.Next()
			}
		}

		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Access denied: insufficient permissions",
				"code":    "FORBIDDEN",
				"required": fiber.Map{
					"resource": resource,
					"actions":  actions,
				},
			},
		})
	}
}

// RequireAllPermissions checks if the user has all of the specified permissions
func RequireAllPermissions(evaluator *opa.Evaluator, permissions []PermissionRequirement) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := GetUserID(c)
		tenantID := GetTenantID(c)
		roles := GetRoles(c)

		if userID == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "User not authenticated",
					"code":    "UNAUTHORIZED",
				},
			})
		}

		// Check all permissions
		for _, perm := range permissions {
			resourceID := c.Params("id")
			if perm.ResourceIDParam != "" {
				resourceID = c.Params(perm.ResourceIDParam)
			}

			allowed, err := evaluator.CanAccessResource(
				c.Context(),
				userID,
				tenantID,
				roles,
				perm.Resource,
				resourceID,
				perm.Action,
			)

			if err != nil || !allowed {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
					"success": false,
					"error": fiber.Map{
						"message": "Access denied: insufficient permissions",
						"code":    "FORBIDDEN",
						"required": fiber.Map{
							"resource": perm.Resource,
							"action":   perm.Action,
						},
					},
				})
			}
		}

		return c.Next()
	}
}

// PermissionRequirement represents a permission requirement
type PermissionRequirement struct {
	Resource        string
	Action          string
	ResourceIDParam string // Optional: param name for resource ID (defaults to "id")
}

// getActionFromMethod maps HTTP methods to actions
func getActionFromMethod(method string) string {
	switch method {
	case "GET":
		return "read"
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	default:
		return "access"
	}
}

// RequireBusinessHours restricts access to business hours only
func RequireBusinessHours(evaluator *opa.Evaluator, resource, action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		builder := opa.NewContextBuilderFromFiber(c)
		builder.WithResource(resource, c.Params("id"))
		builder.WithAction(action)

		// The time context is automatically added by the builder
		// The policy will check if time.isBusinessHours == true

		allowed, err := evaluator.EvaluateWithFullContext(c.Context(), builder)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Failed to evaluate time-based policy",
					"code":    "AUTHZ_EVALUATION_FAILED",
					"details": err.Error(),
				},
			})
		}

		if !allowed {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied: this action is only allowed during business hours (9 AM - 5 PM weekdays)",
					"code":    "OUTSIDE_BUSINESS_HOURS",
				},
			})
		}

		return c.Next()
	}
}

// RequireMFA requires MFA verification for sensitive operations
func RequireMFA(evaluator *opa.Evaluator, resource, action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		mfaVerified, ok := c.Locals("mfaVerified").(bool)
		if !ok || !mfaVerified {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "MFA verification required for this operation",
					"code":    "MFA_REQUIRED",
				},
			})
		}

		// Continue with normal permission check
		userID := GetUserID(c)
		tenantID := GetTenantID(c)
		roles := GetRoles(c)

		allowed, err := evaluator.CanAccessResource(
			c.Context(),
			userID,
			tenantID,
			roles,
			resource,
			c.Params("id"),
			action,
		)

		if err != nil || !allowed {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied",
					"code":    "FORBIDDEN",
				},
			})
		}

		return c.Next()
	}
}
