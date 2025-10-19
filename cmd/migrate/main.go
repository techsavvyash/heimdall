package main

import (
	"fmt"
	"log"
	"os"

	"github.com/techsavvyash/heimdall/internal/config"
	"github.com/techsavvyash/heimdall/internal/database"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Connect to database
	if err := database.Connect(cfg); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	db := database.GetDB()

	// Execute command
	switch command {
	case "up", "migrate":
		if err := database.RunMigrations(db); err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		log.Println("✅ Migrations completed successfully")

	case "seed":
		if err := database.SeedDefaultData(db); err != nil {
			log.Fatalf("Seed failed: %v", err)
		}
		if err := database.SeedDefaultTenant(db); err != nil {
			log.Fatalf("Tenant seed failed: %v", err)
		}
		log.Println("✅ Seed completed successfully")

	case "fresh":
		// Run migrations and seed
		if err := database.RunMigrations(db); err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		if err := database.SeedDefaultData(db); err != nil {
			log.Fatalf("Seed failed: %v", err)
		}
		if err := database.SeedDefaultTenant(db); err != nil {
			log.Fatalf("Tenant seed failed: %v", err)
		}
		log.Println("✅ Fresh migration completed successfully")

	default:
		fmt.Printf("Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Heimdall Database Migration Tool")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go run cmd/migrate/main.go <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  up, migrate  Run database migrations")
	fmt.Println("  seed         Seed default data (permissions, etc.)")
	fmt.Println("  fresh        Run migrations and seed data")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go run cmd/migrate/main.go up")
	fmt.Println("  go run cmd/migrate/main.go seed")
	fmt.Println("  go run cmd/migrate/main.go fresh")
}
