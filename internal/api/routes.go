package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/auth"
	"github.com/techsavvyash/heimdall/internal/middleware"
	"github.com/techsavvyash/heimdall/internal/opa"
)

// SetupRoutes configures all API routes
func SetupRoutes(app *fiber.App, authHandler *AuthHandler, userHandler *UserHandler, passwordHandler *PasswordHandler, tenantHandler *TenantHandler, policyHandler *PolicyHandler, jwtService *auth.JWTService, evaluator *opa.Evaluator) {
	// API v1 group
	v1 := app.Group("/v1")

	// Public routes (no authentication required)
	setupPublicRoutes(v1, authHandler)

	// Protected routes (authentication required)
	setupProtectedRoutes(v1, authHandler, userHandler, passwordHandler, tenantHandler, policyHandler, jwtService, evaluator)
}

// setupPublicRoutes configures public routes
func setupPublicRoutes(v1 fiber.Router, authHandler *AuthHandler) {
	auth := v1.Group("/auth")

	// Authentication endpoints
	auth.Post("/register", authHandler.Register)
	auth.Post("/login", authHandler.Login)
	auth.Post("/refresh", authHandler.RefreshToken)
}

// setupProtectedRoutes configures routes that require authentication
func setupProtectedRoutes(v1 fiber.Router, authHandler *AuthHandler, userHandler *UserHandler, passwordHandler *PasswordHandler, tenantHandler *TenantHandler, policyHandler *PolicyHandler, jwtService *auth.JWTService, evaluator *opa.Evaluator) {
	// Apply authentication middleware
	protected := v1.Use(middleware.AuthMiddleware(jwtService))

	// Auth routes (authenticated)
	authRoutes := protected.Group("/auth")
	authRoutes.Post("/logout", authHandler.Logout)
	authRoutes.Post("/logout-all", authHandler.LogoutAll)
	authRoutes.Post("/password/change", passwordHandler.ChangePassword)

	// User routes
	userRoutes := protected.Group("/users")
	userRoutes.Get("/me", userHandler.GetMe)
	userRoutes.Patch("/me", userHandler.UpdateMe)
	userRoutes.Delete("/me", userHandler.DeleteMe)
	userRoutes.Get("/me/permissions", userHandler.GetMyPermissions)

	// Admin user routes (OPA-protected)
	userRoutes.Get("/",
		middleware.RequirePermissionOPA(evaluator, "users", "read"),
		userHandler.ListUsers)
	userRoutes.Get("/:userId",
		middleware.RequirePermissionOPA(evaluator, "users", "read"),
		userHandler.GetUserByID)
	userRoutes.Post("/:userId/roles",
		middleware.RequirePermissionOPA(evaluator, "roles", "assign"),
		userHandler.AssignRole)
	userRoutes.Delete("/:userId/roles/:roleId",
		middleware.RequirePermissionOPA(evaluator, "roles", "assign"),
		userHandler.RemoveRole)

	// Tenant routes (OPA-protected)
	tenantRoutes := protected.Group("/tenants")
	tenantRoutes.Get("/",
		middleware.RequirePermissionOPA(evaluator, "tenants", "read"),
		tenantHandler.ListTenants)
	tenantRoutes.Post("/",
		middleware.RequirePermissionOPA(evaluator, "tenants", "create"),
		tenantHandler.CreateTenant)
	tenantRoutes.Get("/slug/:slug",
		middleware.RequirePermissionOPA(evaluator, "tenants", "read"),
		tenantHandler.GetTenantBySlug)
	tenantRoutes.Get("/:tenantId",
		middleware.RequirePermissionOPA(evaluator, "tenants", "read"),
		tenantHandler.GetTenant)
	tenantRoutes.Patch("/:tenantId",
		middleware.RequirePermissionOPA(evaluator, "tenants", "update"),
		tenantHandler.UpdateTenant)
	tenantRoutes.Delete("/:tenantId",
		middleware.RequirePermissionOPA(evaluator, "tenants", "delete"),
		tenantHandler.DeleteTenant)
	tenantRoutes.Post("/:tenantId/suspend",
		middleware.RequirePermissionOPA(evaluator, "tenants", "suspend"),
		tenantHandler.SuspendTenant)
	tenantRoutes.Post("/:tenantId/activate",
		middleware.RequirePermissionOPA(evaluator, "tenants", "activate"),
		tenantHandler.ActivateTenant)
	tenantRoutes.Get("/:tenantId/stats",
		middleware.RequirePermissionOPA(evaluator, "tenants", "read"),
		tenantHandler.GetTenantStats)

	// Policy routes (OPA-protected)
	policyRoutes := protected.Group("/policies")
	policyRoutes.Get("/",
		middleware.RequirePermissionOPA(evaluator, "policies", "read"),
		policyHandler.ListPolicies)
	policyRoutes.Post("/",
		middleware.RequirePermissionOPA(evaluator, "policies", "create"),
		policyHandler.CreatePolicy)
	policyRoutes.Get("/:id",
		middleware.RequirePermissionOPA(evaluator, "policies", "read"),
		policyHandler.GetPolicy)
	policyRoutes.Put("/:id",
		middleware.RequirePermissionOPA(evaluator, "policies", "update"),
		policyHandler.UpdatePolicy)
	policyRoutes.Delete("/:id",
		middleware.RequirePermissionOPA(evaluator, "policies", "delete"),
		policyHandler.DeletePolicy)
	policyRoutes.Post("/:id/publish",
		middleware.RequirePermissionOPA(evaluator, "policies", "publish"),
		policyHandler.PublishPolicy)
	policyRoutes.Post("/:id/validate",
		middleware.RequirePermissionOPA(evaluator, "policies", "test"),
		policyHandler.ValidatePolicy)
	policyRoutes.Post("/:id/test",
		middleware.RequirePermissionOPA(evaluator, "policies", "test"),
		policyHandler.TestPolicy)
	policyRoutes.Get("/:id/versions",
		middleware.RequirePermissionOPA(evaluator, "policies", "read"),
		policyHandler.GetPolicyVersions)

	// Bundle routes (OPA-protected)
	bundleRoutes := protected.Group("/bundles")
	bundleRoutes.Get("/",
		middleware.RequirePermissionOPA(evaluator, "bundles", "read"),
		policyHandler.ListBundles)
	bundleRoutes.Post("/",
		middleware.RequirePermissionOPA(evaluator, "bundles", "create"),
		policyHandler.CreateBundle)
	bundleRoutes.Get("/:id",
		middleware.RequirePermissionOPA(evaluator, "bundles", "read"),
		policyHandler.GetBundle)
	bundleRoutes.Post("/:id/activate",
		middleware.RequirePermissionOPA(evaluator, "bundles", "activate"),
		middleware.RequireMFA(evaluator, "bundles", "activate"),
		policyHandler.ActivateBundle)
	bundleRoutes.Post("/:id/deploy",
		middleware.RequirePermissionOPA(evaluator, "bundles", "deploy"),
		middleware.RequireMFA(evaluator, "bundles", "deploy"),
		policyHandler.DeployBundle)
	bundleRoutes.Delete("/:id",
		middleware.RequirePermissionOPA(evaluator, "bundles", "delete"),
		policyHandler.DeleteBundle)
}
