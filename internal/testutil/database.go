package testutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/techsavvyash/heimdall/internal/database"
	"github.com/techsavvyash/heimdall/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SetupTestDB creates a test database connection
func SetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	// Use a test database
	dsn := "host=localhost port=5432 user=heimdall password=heimdall_password dbname=heimdall_test sslmode=disable"

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), // Silent during tests
	})
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Run migrations
	if err := database.RunMigrations(db); err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}

	return db
}

// CleanupTestDB cleans up the test database
func CleanupTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Drop all tables
	tables := []interface{}{
		&models.AuditLog{},
		&models.RolePermission{},
		&models.UserRole{},
		&models.Permission{},
		&models.Role{},
		&models.User{},
		&models.Tenant{},
	}

	for _, table := range tables {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Logf("Warning: Failed to drop table: %v", err)
		}
	}

	sqlDB, _ := db.DB()
	if sqlDB != nil {
		sqlDB.Close()
	}
}

// TruncateTables truncates all tables (faster than dropping)
func TruncateTables(t *testing.T, db *gorm.DB) {
	t.Helper()

	tables := []string{
		"audit_logs",
		"role_permissions",
		"user_roles",
		"permissions",
		"roles",
		"users",
		"tenants",
	}

	for _, table := range tables {
		db.Exec(fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
	}
}

// CreateTestTenant creates a test tenant
func CreateTestTenant(t *testing.T, db *gorm.DB, name, slug string) *models.Tenant {
	t.Helper()

	tenant := &models.Tenant{
		Name:     name,
		Slug:     slug,
		Status:   "active",
		MaxUsers: 1000,
		MaxRoles: 50,
		Settings: map[string]interface{}{
			"test": true,
		},
	}

	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("Failed to create test tenant: %v", err)
	}

	return tenant
}

// CreateTestUser creates a test user
func CreateTestUser(t *testing.T, db *gorm.DB, tenant *models.Tenant, email string) *models.User {
	t.Helper()

	user := &models.User{
		TenantID: tenant.ID,
		Email:    email,
		Metadata: map[string]interface{}{
			"firstName": "Test",
			"lastName":  "User",
		},
	}

	if err := db.Create(user).Error; err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	return user
}

// CreateTestRole creates a test role
func CreateTestRole(t *testing.T, db *gorm.DB, tenant *models.Tenant, name string) *models.Role {
	t.Helper()

	role := &models.Role{
		TenantID:    tenant.ID,
		Name:        name,
		Description: "Test role",
		IsSystem:    false,
	}

	if err := db.Create(role).Error; err != nil {
		t.Fatalf("Failed to create test role: %v", err)
	}

	return role
}

// CreateTestPermission creates a test permission
func CreateTestPermission(t *testing.T, db *gorm.DB, name, resource, action string) *models.Permission {
	t.Helper()

	permission := &models.Permission{
		Name:        name,
		Resource:    resource,
		Action:      action,
		Scope:       "tenant",
		IsSystem:    false,
		Description: "Test permission",
	}

	if err := db.Create(permission).Error; err != nil {
		t.Fatalf("Failed to create test permission: %v", err)
	}

	return permission
}

// AssignRoleToUser assigns a role to a user
func AssignRoleToUser(t *testing.T, db *gorm.DB, user *models.User, role *models.Role) {
	t.Helper()

	userRole := &models.UserRole{
		UserID: user.ID,
		RoleID: role.ID,
	}

	if err := db.Create(userRole).Error; err != nil {
		t.Fatalf("Failed to assign role to user: %v", err)
	}
}

// AssignPermissionToRole assigns a permission to a role
func AssignPermissionToRole(t *testing.T, db *gorm.DB, role *models.Role, permission *models.Permission) {
	t.Helper()

	rolePermission := &models.RolePermission{
		RoleID:       role.ID,
		PermissionID: permission.ID,
	}

	if err := db.Create(rolePermission).Error; err != nil {
		t.Fatalf("Failed to assign permission to role: %v", err)
	}
}

// WithTestDB wraps a test function with database setup and cleanup
func WithTestDB(t *testing.T, fn func(t *testing.T, db *gorm.DB)) {
	t.Helper()

	db := SetupTestDB(t)
	defer CleanupTestDB(t, db)

	fn(t, db)
}

// WithCleanDB runs a test with a clean database (truncated tables)
func WithCleanDB(t *testing.T, db *gorm.DB, fn func(t *testing.T)) {
	t.Helper()

	TruncateTables(t, db)
	fn(t)
}

// CreateTestContext creates a test context
func CreateTestContext(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}
