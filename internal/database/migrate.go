package database

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/techsavvyash/heimdall/internal/models"
	"gorm.io/gorm"
)

// RunMigrations runs GORM AutoMigrate on all models
func RunMigrations(db *gorm.DB) error {
	log.Println("Running database migrations...")

	// Enable UUID extension for PostgreSQL
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS \"uuid-ossp\"").Error; err != nil {
		return fmt.Errorf("failed to create uuid extension: %w", err)
	}

	// Run AutoMigrate on all models
	if err := models.AutoMigrate(db); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	log.Println("Database migrations completed successfully")
	return nil
}

// SeedDefaultData seeds default data like system permissions
func SeedDefaultData(db *gorm.DB) error {
	log.Println("Seeding default data...")

	// Check if permissions already exist
	var count int64
	db.Model(&models.Permission{}).Count(&count)
	if count > 0 {
		log.Println("Default data already exists, skipping seed")
		return nil
	}

	// Default system permissions
	defaultPermissions := []models.Permission{
		// User permissions
		{Name: "users.create", Resource: "users", Action: "create", Scope: "tenant", IsSystem: true, Description: "Create users"},
		{Name: "users.read", Resource: "users", Action: "read", Scope: "tenant", IsSystem: true, Description: "Read user information"},
		{Name: "users.update", Resource: "users", Action: "update", Scope: "tenant", IsSystem: true, Description: "Update users"},
		{Name: "users.delete", Resource: "users", Action: "delete", Scope: "tenant", IsSystem: true, Description: "Delete users"},
		{Name: "users.read.own", Resource: "users", Action: "read", Scope: "own", IsSystem: true, Description: "Read own user information"},
		{Name: "users.update.own", Resource: "users", Action: "update", Scope: "own", IsSystem: true, Description: "Update own user information"},

		// Role permissions
		{Name: "roles.create", Resource: "roles", Action: "create", Scope: "tenant", IsSystem: true, Description: "Create roles"},
		{Name: "roles.read", Resource: "roles", Action: "read", Scope: "tenant", IsSystem: true, Description: "Read roles"},
		{Name: "roles.update", Resource: "roles", Action: "update", Scope: "tenant", IsSystem: true, Description: "Update roles"},
		{Name: "roles.delete", Resource: "roles", Action: "delete", Scope: "tenant", IsSystem: true, Description: "Delete roles"},

		// Permission permissions
		{Name: "permissions.read", Resource: "permissions", Action: "read", Scope: "tenant", IsSystem: true, Description: "Read permissions"},
		{Name: "permissions.assign", Resource: "permissions", Action: "assign", Scope: "tenant", IsSystem: true, Description: "Assign permissions to roles"},

		// Tenant permissions
		{Name: "tenants.read", Resource: "tenants", Action: "read", Scope: "tenant", IsSystem: true, Description: "Read tenant information"},
		{Name: "tenants.update", Resource: "tenants", Action: "update", Scope: "tenant", IsSystem: true, Description: "Update tenant information"},

		// Audit log permissions
		{Name: "audit.read", Resource: "audit", Action: "read", Scope: "tenant", IsSystem: true, Description: "Read audit logs"},
	}

	// Create permissions in transaction
	if err := db.Transaction(func(tx *gorm.DB) error {
		for i := range defaultPermissions {
			if err := tx.Create(&defaultPermissions[i]).Error; err != nil {
				return fmt.Errorf("failed to create permission %s: %w", defaultPermissions[i].Name, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to seed permissions: %w", err)
	}

	log.Printf("Successfully seeded %d default permissions", len(defaultPermissions))
	return nil
}

// SeedDefaultTenant seeds a default tenant if none exists
func SeedDefaultTenant(db *gorm.DB) error {
	log.Println("Seeding default tenant...")

	// Check if any tenants exist
	var count int64
	db.Model(&models.Tenant{}).Count(&count)
	if count > 0 {
		log.Println("Tenants already exist, skipping default tenant seed")
		return nil
	}

	// Create default tenant
	settingsMap := map[string]interface{}{
		"description": "Default tenant for Heimdall",
	}
	settingsJSON, err := json.Marshal(settingsMap)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	defaultTenant := models.Tenant{
		Name:     "Default Tenant",
		Slug:     "default",
		Status:   "active",
		MaxUsers: 1000,
		MaxRoles: 50,
		Settings: settingsJSON,
	}

	if err := db.Create(&defaultTenant).Error; err != nil {
		return fmt.Errorf("failed to create default tenant: %w", err)
	}

	log.Printf("Successfully created default tenant with ID: %s", defaultTenant.ID.String())
	return nil
}
