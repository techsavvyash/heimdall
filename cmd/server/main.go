package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/techsavvyash/heimdall/internal/api"
	"github.com/techsavvyash/heimdall/internal/auth"
	"github.com/techsavvyash/heimdall/internal/config"
	"github.com/techsavvyash/heimdall/internal/database"
	"github.com/techsavvyash/heimdall/internal/middleware"
	"github.com/techsavvyash/heimdall/internal/opa"
	"github.com/techsavvyash/heimdall/internal/service"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Connect to PostgreSQL
	if err := database.Connect(cfg); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()
	log.Println("✅ Database connected")

	// Connect to Redis
	if err := database.ConnectRedis(cfg); err != nil {
		log.Printf("⚠️  Failed to connect to Redis: %v (continuing without cache)", err)
	} else {
		defer database.CloseRedis()
		log.Println("✅ Redis connected")
	}

	// Initialize JWT service
	jwtService, err := auth.NewJWTService(&cfg.JWT)
	if err != nil {
		log.Fatalf("Failed to initialize JWT service: %v", err)
	}
	log.Println("✅ JWT service initialized")

	// Initialize FusionAuth client
	fusionAuthClient := auth.NewFusionAuthClient(&cfg.Auth)
	log.Println("✅ FusionAuth client initialized")

	// Get database and redis clients
	db := database.GetDB()
	redis := database.GetRedis()

	// Initialize OPA client and evaluator
	opaClient := opa.NewClient(&cfg.OPA)
	opaEvaluator := opa.NewEvaluator(opaClient, redis, cfg.OPA.EnableCache)
	log.Println("✅ OPA client initialized")

	// Verify OPA is healthy
	if err := opaClient.HealthCheck(context.Background()); err != nil {
		log.Printf("⚠️  OPA health check failed: %v (policies may not be enforced properly)", err)
	} else {
		log.Println("✅ OPA is healthy")
	}

	// Initialize services
	authService := service.NewAuthService(db, fusionAuthClient, jwtService, redis)
	userService := service.NewUserService(db, fusionAuthClient)
	passwordService := service.NewPasswordService(fusionAuthClient)
	tenantService := service.NewTenantService(db)

	// Initialize policy and bundle services
	policyService := service.NewPolicyService(db, opaClient)
	bundleService, err := service.NewBundleService(db, &cfg.MinIO)
	if err != nil {
		log.Printf("⚠️  Failed to initialize bundle service: %v (bundle management will not work)", err)
	} else {
		// Ensure MinIO bucket exists
		if err := bundleService.EnsureBucket(context.Background()); err != nil {
			log.Printf("⚠️  Failed to ensure MinIO bucket: %v", err)
		} else {
			log.Println("✅ MinIO bucket ready")
		}
	}
	log.Println("✅ Services initialized")

	// Initialize handlers
	authHandler := api.NewAuthHandler(authService)
	userHandler := api.NewUserHandler(userService)
	passwordHandler := api.NewPasswordHandler(passwordService)
	tenantHandler := api.NewTenantHandler(tenantService)
	policyHandler := api.NewPolicyHandler(policyService, bundleService)
	log.Println("✅ Handlers initialized")

	// Initialize Fiber app
	app := fiber.New(fiber.Config{
		AppName: "Heimdall v1.0.0",
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"success": false,
				"error": fiber.Map{
					"message": err.Error(),
					"code":    "INTERNAL_ERROR",
				},
			})
		},
	})

	// Global middleware
	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${latency} ${method} ${path}\n",
	}))
	app.Use(middleware.CORS(cfg))
	app.Use(middleware.RateLimitMiddleware(cfg))
	app.Use(middleware.TenantMiddleware())

	// Health check endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "healthy",
			"service": "heimdall",
			"version": "1.0.0",
		})
	})

	// Setup API routes
	api.SetupRoutes(app, authHandler, userHandler, passwordHandler, tenantHandler, policyHandler, jwtService, opaEvaluator)
	log.Println("✅ Routes configured")

	// Get port from configuration
	port := cfg.Server.Port

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan

		log.Println("\n🛑 Shutting down server...")
		if err := app.Shutdown(); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
		database.Close()
		database.CloseRedis()
		log.Println("✅ Server stopped gracefully")
		os.Exit(0)
	}()

	// Start server
	log.Printf("🚀 Heimdall server starting on port %s", port)
	log.Printf("📡 Environment: %s", cfg.Server.Environment)
	log.Printf("🔗 API endpoint: http://localhost:%s/v1", port)
	log.Printf("❤️  Health check: http://localhost:%s/health", port)

	if err := app.Listen(":" + port); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}
}
