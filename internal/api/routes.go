package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/auth"
	"github.com/techsavvyash/heimdall/internal/middleware"
)

// SetupRoutes configures all API routes
func SetupRoutes(app *fiber.App, authHandler *AuthHandler, userHandler *UserHandler, passwordHandler *PasswordHandler, tenantHandler *TenantHandler, jwtService *auth.JWTService) {
	// API v1 group
	v1 := app.Group("/v1")

	// Public routes (no authentication required)
	setupPublicRoutes(v1, authHandler)

	// Protected routes (authentication required)
	setupProtectedRoutes(v1, authHandler, userHandler, passwordHandler, tenantHandler, jwtService)
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
func setupProtectedRoutes(v1 fiber.Router, authHandler *AuthHandler, userHandler *UserHandler, passwordHandler *PasswordHandler, tenantHandler *TenantHandler, jwtService *auth.JWTService) {
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

	// Admin user routes (TODO: add permission checks)
	userRoutes.Get("/", userHandler.ListUsers)
	userRoutes.Get("/:userId", userHandler.GetUserByID)
	userRoutes.Post("/:userId/roles", userHandler.AssignRole)
	userRoutes.Delete("/:userId/roles/:roleId", userHandler.RemoveRole)

	// Tenant routes (TODO: add permission checks for admin-only operations)
	tenantRoutes := protected.Group("/tenants")
	tenantRoutes.Get("/", tenantHandler.ListTenants)
	tenantRoutes.Post("/", tenantHandler.CreateTenant)
	tenantRoutes.Get("/slug/:slug", tenantHandler.GetTenantBySlug)
	tenantRoutes.Get("/:tenantId", tenantHandler.GetTenant)
	tenantRoutes.Patch("/:tenantId", tenantHandler.UpdateTenant)
	tenantRoutes.Delete("/:tenantId", tenantHandler.DeleteTenant)
	tenantRoutes.Post("/:tenantId/suspend", tenantHandler.SuspendTenant)
	tenantRoutes.Post("/:tenantId/activate", tenantHandler.ActivateTenant)
	tenantRoutes.Get("/:tenantId/stats", tenantHandler.GetTenantStats)
}
