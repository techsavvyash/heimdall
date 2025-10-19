package middleware

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/auth"
	"github.com/techsavvyash/heimdall/internal/database"
)

// AuthMiddleware validates JWT tokens and sets user context
func AuthMiddleware(jwtService *auth.JWTService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Get Authorization header
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Authorization header is required",
					"code":    "UNAUTHORIZED",
				},
			})
		}

		// Extract token
		tokenString, err := auth.ExtractTokenFromHeader(authHeader)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": err.Error(),
					"code":    "INVALID_TOKEN",
				},
			})
		}

		// Validate token
		claims, err := jwtService.ValidateAccessToken(tokenString)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Invalid or expired token",
					"code":    "INVALID_TOKEN",
				},
			})
		}

		// Check if token is blacklisted
		redis := database.GetRedis()
		if redis != nil {
			blacklisted, err := redis.IsTokenBlacklisted(context.Background(), claims.ID)
			if err == nil && blacklisted {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
					"success": false,
					"error": fiber.Map{
						"message": "Token has been revoked",
						"code":    "TOKEN_REVOKED",
					},
				})
			}
		}

		// Set user info in context
		c.Locals("userID", claims.UserID)
		c.Locals("tenantID", claims.TenantID)
		c.Locals("email", claims.Email)
		c.Locals("roles", claims.Roles)
		c.Locals("tokenID", claims.ID)

		return c.Next()
	}
}

// OptionalAuthMiddleware validates JWT if present but doesn't require it
func OptionalAuthMiddleware(jwtService *auth.JWTService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Next()
		}

		tokenString, err := auth.ExtractTokenFromHeader(authHeader)
		if err != nil {
			return c.Next()
		}

		claims, err := jwtService.ValidateAccessToken(tokenString)
		if err != nil {
			return c.Next()
		}

		// Check if token is blacklisted
		redis := database.GetRedis()
		if redis != nil {
			blacklisted, err := redis.IsTokenBlacklisted(context.Background(), claims.ID)
			if err == nil && !blacklisted {
				c.Locals("userID", claims.UserID)
				c.Locals("tenantID", claims.TenantID)
				c.Locals("email", claims.Email)
				c.Locals("roles", claims.Roles)
			}
		}

		return c.Next()
	}
}

// RequireRole middleware checks if user has a specific role
func RequireRole(requiredRole string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		roles, ok := c.Locals("roles").([]string)
		if !ok {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied: insufficient permissions",
					"code":    "FORBIDDEN",
				},
			})
		}

		hasRole := false
		for _, role := range roles {
			if role == requiredRole {
				hasRole = true
				break
			}
		}

		if !hasRole {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied: required role not found",
					"code":    "FORBIDDEN",
				},
			})
		}

		return c.Next()
	}
}

// TenantMiddleware validates and sets tenant context
func TenantMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Get tenant ID from header or subdomain
		tenantID := c.Get("X-Tenant-ID")

		// If not in header, try to get from subdomain
		if tenantID == "" {
			host := c.Hostname()
			parts := strings.Split(host, ".")
			if len(parts) > 2 {
				tenantID = parts[0]
			}
		}

		// If still no tenant ID, check if user is authenticated
		if tenantID == "" {
			userTenantID, ok := c.Locals("tenantID").(string)
			if ok {
				tenantID = userTenantID
			}
		}

		if tenantID != "" {
			c.Locals("requestTenantID", tenantID)
		}

		return c.Next()
	}
}

// RequireTenant middleware ensures a tenant context exists
func RequireTenant() fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenantID := GetTenantID(c)
		if tenantID == "" {
			tenantID, _ = c.Locals("requestTenantID").(string)
		}

		if tenantID == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Tenant context is required",
					"code":    "TENANT_REQUIRED",
				},
			})
		}

		return c.Next()
	}
}

// IsolateTenant middleware ensures users can only access resources in their tenant
func IsolateTenant() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Get user's tenant ID from JWT claims
		userTenantID := GetTenantID(c)
		if userTenantID == "" {
			// If no user tenant, allow (might be super admin)
			return c.Next()
		}

		// Get requested tenant ID from URL params or body
		requestedTenantID := c.Params("tenantId")

		// If tenant ID is in the request, verify it matches user's tenant
		if requestedTenantID != "" && requestedTenantID != userTenantID {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": "Access denied: cannot access resources from another tenant",
					"code":    "TENANT_ISOLATION_VIOLATION",
				},
			})
		}

		return c.Next()
	}
}

// GetUserID helper to extract user ID from context
func GetUserID(c *fiber.Ctx) string {
	userID, _ := c.Locals("userID").(string)
	return userID
}

// GetTenantID helper to extract tenant ID from context
func GetTenantID(c *fiber.Ctx) string {
	tenantID, _ := c.Locals("tenantID").(string)
	return tenantID
}

// GetEmail helper to extract email from context
func GetEmail(c *fiber.Ctx) string {
	email, _ := c.Locals("email").(string)
	return email
}

// GetRoles helper to extract roles from context
func GetRoles(c *fiber.Ctx) []string {
	roles, _ := c.Locals("roles").([]string)
	return roles
}

// GetTokenID helper to extract token ID from context
func GetTokenID(c *fiber.Ctx) string {
	tokenID, _ := c.Locals("tokenID").(string)
	return tokenID
}
