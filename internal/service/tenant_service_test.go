package service

import (
	"testing"

	"github.com/techsavvyash/heimdall/internal/testutil"
	"gorm.io/gorm"
)

func TestTenantService_CreateTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		req := &CreateTenantRequest{
			Name:     "Test Corporation",
			Slug:     "test-corp",
			MaxUsers: 500,
			MaxRoles: 25,
			Settings: map[string]interface{}{
				"theme": "blue",
			},
		}

		tenant, err := tenantService.CreateTenant(ctx, req)
		if err != nil {
			t.Fatalf("Failed to create tenant: %v", err)
		}

		// Verify tenant fields
		if tenant.Name != req.Name {
			t.Errorf("Expected name '%s', got '%s'", req.Name, tenant.Name)
		}
		if tenant.Slug != req.Slug {
			t.Errorf("Expected slug '%s', got '%s'", req.Slug, tenant.Slug)
		}
		if tenant.MaxUsers != req.MaxUsers {
			t.Errorf("Expected maxUsers %d, got %d", req.MaxUsers, tenant.MaxUsers)
		}
		if tenant.Status != "active" {
			t.Errorf("Expected status 'active', got '%s'", tenant.Status)
		}
		if tenant.ID == "" {
			t.Error("Tenant ID should not be empty")
		}
	})
}

func TestTenantService_CreateTenant_DuplicateSlug(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		req := &CreateTenantRequest{
			Name: "First Tenant",
			Slug: "test-slug",
		}

		// Create first tenant
		_, err := tenantService.CreateTenant(ctx, req)
		if err != nil {
			t.Fatalf("Failed to create first tenant: %v", err)
		}

		// Try to create second tenant with same slug
		req.Name = "Second Tenant"
		_, err = tenantService.CreateTenant(ctx, req)
		if err == nil {
			t.Error("Expected error for duplicate slug, got nil")
		}
	})
}

func TestTenantService_CreateTenant_InvalidSlug(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		invalidSlugs := []string{
			"Test Slug",     // spaces
			"test_slug_",    // trailing underscore
			"Test-Slug",     // uppercase
			"test@slug",     // special chars
			"-test-slug",    // leading hyphen
			"test--slug",    // double hyphen
		}

		for _, slug := range invalidSlugs {
			req := &CreateTenantRequest{
				Name: "Test",
				Slug: slug,
			}

			_, err := tenantService.CreateTenant(ctx, req)
			if err == nil {
				t.Errorf("Expected error for invalid slug '%s', got nil", slug)
			}
		}
	})
}

func TestTenantService_CreateTenant_SlugNormalization(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		tests := []struct {
			input    string
			expected string
		}{
			{"Test Corp", "test-corp"},
			{"Test_Corp", "test-corp"},
			{"  test corp  ", "test-corp"},
			{"TEST-CORP", "test-corp"},
		}

		for _, tt := range tests {
			req := &CreateTenantRequest{
				Name: "Test",
				Slug: tt.input,
			}

			tenant, err := tenantService.CreateTenant(ctx, req)
			if err != nil {
				t.Errorf("Failed to create tenant with slug '%s': %v", tt.input, err)
				continue
			}

			if tenant.Slug != tt.expected {
				t.Errorf("Input '%s': expected slug '%s', got '%s'", tt.input, tt.expected, tenant.Slug)
			}

			// Clean up for next test
			testutil.TruncateTables(t, db)
		}
	})
}

func TestTenantService_GetTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		// Create test tenant
		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")
		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// Get tenant
		tenant, err := tenantService.GetTenant(ctx, testTenant.ID.String())
		if err != nil {
			t.Fatalf("Failed to get tenant: %v", err)
		}

		// Verify
		if tenant.ID != testTenant.ID.String() {
			t.Errorf("Expected ID '%s', got '%s'", testTenant.ID.String(), tenant.ID)
		}
		if tenant.Name != testTenant.Name {
			t.Errorf("Expected name '%s', got '%s'", testTenant.Name, tenant.Name)
		}
	})
}

func TestTenantService_GetTenantBySlug(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-slug")
		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// Get tenant by slug
		tenant, err := tenantService.GetTenantBySlug(ctx, "test-slug")
		if err != nil {
			t.Fatalf("Failed to get tenant by slug: %v", err)
		}

		// Verify
		if tenant.Slug != "test-slug" {
			t.Errorf("Expected slug 'test-slug', got '%s'", tenant.Slug)
		}
		if tenant.ID != testTenant.ID.String() {
			t.Errorf("Expected ID '%s', got '%s'", testTenant.ID.String(), tenant.ID)
		}
	})
}

func TestTenantService_UpdateTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Original Name", "test-tenant")
		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		newName := "Updated Name"
		newMaxUsers := 2000
		req := &UpdateTenantRequest{
			Name:     &newName,
			MaxUsers: &newMaxUsers,
			Settings: map[string]interface{}{
				"newKey": "newValue",
			},
		}

		// Update tenant
		updated, err := tenantService.UpdateTenant(ctx, testTenant.ID.String(), req)
		if err != nil {
			t.Fatalf("Failed to update tenant: %v", err)
		}

		// Verify updates
		if updated.Name != newName {
			t.Errorf("Expected name '%s', got '%s'", newName, updated.Name)
		}
		if updated.MaxUsers != newMaxUsers {
			t.Errorf("Expected maxUsers %d, got %d", newMaxUsers, updated.MaxUsers)
		}
		if updated.Settings["newKey"] != "newValue" {
			t.Error("Settings not updated correctly")
		}
	})
}

func TestTenantService_DeleteTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")
		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// Delete tenant (should succeed - no users)
		err := tenantService.DeleteTenant(ctx, testTenant.ID.String())
		if err != nil {
			t.Fatalf("Failed to delete tenant: %v", err)
		}

		// Verify tenant is deleted
		_, err = tenantService.GetTenant(ctx, testTenant.ID.String())
		if err == nil {
			t.Error("Expected error when getting deleted tenant, got nil")
		}
	})
}

func TestTenantService_DeleteTenant_WithUsers(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")
		// Create a user in the tenant
		testutil.CreateTestUser(t, db, testTenant, "user@example.com")

		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// Try to delete tenant (should fail - has users)
		err := tenantService.DeleteTenant(ctx, testTenant.ID.String())
		if err == nil {
			t.Error("Expected error when deleting tenant with users, got nil")
		}
	})
}

func TestTenantService_SuspendTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")
		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// Suspend tenant
		err := tenantService.SuspendTenant(ctx, testTenant.ID.String())
		if err != nil {
			t.Fatalf("Failed to suspend tenant: %v", err)
		}

		// Verify status changed
		tenant, _ := tenantService.GetTenant(ctx, testTenant.ID.String())
		if tenant.Status != "suspended" {
			t.Errorf("Expected status 'suspended', got '%s'", tenant.Status)
		}
	})
}

func TestTenantService_ActivateTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")
		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// First suspend
		tenantService.SuspendTenant(ctx, testTenant.ID.String())

		// Then activate
		err := tenantService.ActivateTenant(ctx, testTenant.ID.String())
		if err != nil {
			t.Fatalf("Failed to activate tenant: %v", err)
		}

		// Verify status changed
		tenant, _ := tenantService.GetTenant(ctx, testTenant.ID.String())
		if tenant.Status != "active" {
			t.Errorf("Expected status 'active', got '%s'", tenant.Status)
		}
	})
}

func TestTenantService_ListTenants(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		// Create multiple tenants
		testutil.CreateTestTenant(t, db, "Tenant 1", "tenant-1")
		testutil.CreateTestTenant(t, db, "Tenant 2", "tenant-2")
		testutil.CreateTestTenant(t, db, "Tenant 3", "tenant-3")

		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// List tenants
		tenants, total, err := tenantService.ListTenants(ctx, 1, 10)
		if err != nil {
			t.Fatalf("Failed to list tenants: %v", err)
		}

		// Verify
		if total != 3 {
			t.Errorf("Expected 3 tenants, got %d", total)
		}
		if len(tenants) != 3 {
			t.Errorf("Expected 3 tenants in response, got %d", len(tenants))
		}
	})
}

func TestTenantService_GetTenantStats(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")
		// Create users and roles
		testutil.CreateTestUser(t, db, testTenant, "user1@example.com")
		testutil.CreateTestUser(t, db, testTenant, "user2@example.com")
		testutil.CreateTestRole(t, db, testTenant, "Admin")
		testutil.CreateTestRole(t, db, testTenant, "User")

		tenantService := NewTenantService(db)
		ctx := testutil.CreateTestContext(t)

		// Get stats
		stats, err := tenantService.GetTenantStats(ctx, testTenant.ID.String())
		if err != nil {
			t.Fatalf("Failed to get tenant stats: %v", err)
		}

		// Verify stats
		userCount, ok := stats["userCount"].(int64)
		if !ok || userCount != 2 {
			t.Errorf("Expected userCount 2, got %v", stats["userCount"])
		}

		roleCount, ok := stats["roleCount"].(int64)
		if !ok || roleCount != 2 {
			t.Errorf("Expected roleCount 2, got %v", stats["roleCount"])
		}
	})
}
