package main

import (
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
	"github.com/techsavvyash/heimdall/internal/openapi"
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

	// Initialize services
	db := database.GetDB()
	redis := database.GetRedis()
	authService := service.NewAuthService(db, fusionAuthClient, jwtService, redis)
	userService := service.NewUserService(db, fusionAuthClient)
	passwordService := service.NewPasswordService(fusionAuthClient)
	tenantService := service.NewTenantService(db)
	log.Println("✅ Services initialized")

	// Initialize handlers
	authHandler := api.NewAuthHandler(authService)
	userHandler := api.NewUserHandler(userService)
	passwordHandler := api.NewPasswordHandler(passwordService)
	tenantHandler := api.NewTenantHandler(tenantService)
	log.Println("✅ Handlers initialized")

	// Initialize OpenAPI handler
	openapiHandler := openapi.NewHandler()
	if err := openapiHandler.Initialize(); err != nil {
		log.Fatalf("Failed to initialize OpenAPI handler: %v", err)
	}
	log.Println("✅ OpenAPI specification generated")

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
	api.SetupRoutes(app, authHandler, userHandler, passwordHandler, tenantHandler, jwtService)
	log.Println("✅ Routes configured")

	// Setup OpenAPI/Swagger routes
	openapiHandler.RegisterRoutes(app)
	log.Println("✅ Swagger UI configured")

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
	log.Printf("📚 Swagger UI: http://localhost:%s/swagger/", port)
	log.Printf("📄 OpenAPI spec: http://localhost:%s/swagger/spec", port)

	if err := app.Listen(":" + port); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}
}
