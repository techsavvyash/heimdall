package api

import (
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/service"
	"github.com/techsavvyash/heimdall/internal/testutil"
	"gorm.io/gorm"
)

func setupTenantTestApp(t *testing.T, db *gorm.DB) (*fiber.App, string) {
	t.Helper()

	// Create services
	tenantService := service.NewTenantService(db)

	// Create handler
	tenantHandler := NewTenantHandler(tenantService)

	// Create app
	app := testutil.CreateTestApp()

	// Setup routes
	v1 := app.Group("/v1")
	tenantRoutes := v1.Group("/tenants")
	tenantRoutes.Post("/", tenantHandler.CreateTenant)
	tenantRoutes.Get("/", tenantHandler.ListTenants)
	tenantRoutes.Get("/slug/:slug", tenantHandler.GetTenantBySlug)
	tenantRoutes.Get("/:tenantId", tenantHandler.GetTenant)
	tenantRoutes.Patch("/:tenantId", tenantHandler.UpdateTenant)
	tenantRoutes.Delete("/:tenantId", tenantHandler.DeleteTenant)
	tenantRoutes.Post("/:tenantId/suspend", tenantHandler.SuspendTenant)
	tenantRoutes.Post("/:tenantId/activate", tenantHandler.ActivateTenant)
	tenantRoutes.Get("/:tenantId/stats", tenantHandler.GetTenantStats)

	// Generate test token (for auth)
	jwtService, cleanup := testutil.CreateTestJWTService(t)
	t.Cleanup(cleanup)

	token := testutil.GenerateTestToken(t, jwtService, "test-user-id", "test-tenant-id", "test@example.com", []string{"admin"})

	return app, token
}

func TestTenantHandler_CreateTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		app, token := setupTenantTestApp(t, db)

		body := map[string]interface{}{
			"name":     "Test Corporation",
			"slug":     "test-corp",
			"maxUsers": 500,
			"maxRoles": 25,
			"settings": map[string]interface{}{
				"theme": "blue",
			},
		}

		resp := testutil.MakeRequest(t, app, "POST", "/v1/tenants", body, testutil.WithAuthHeader(token))

		// Assert status
		testutil.AssertStatusCode(t, http.StatusCreated, resp.Code)

		// Parse response
		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		// Verify data
		data := testutil.GetDataField(t, jsonResp)
		if data["name"] != "Test Corporation" {
			t.Errorf("Expected name 'Test Corporation', got '%v'", data["name"])
		}
		if data["slug"] != "test-corp" {
			t.Errorf("Expected slug 'test-corp', got '%v'", data["slug"])
		}
		if data["status"] != "active" {
			t.Errorf("Expected status 'active', got '%v'", data["status"])
		}
	})
}

func TestTenantHandler_CreateTenant_ValidationError(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		app, token := setupTenantTestApp(t, db)

		// Missing required fields
		body := map[string]interface{}{
			"name": "Test",
			// slug is missing
		}

		resp := testutil.MakeRequest(t, app, "POST", "/v1/tenants", body, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusBadRequest, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONError(t, jsonResp, "VALIDATION_ERROR")
	})
}

func TestTenantHandler_GetTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		// Create test tenant
		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")

		app, token := setupTenantTestApp(t, db)

		resp := testutil.MakeRequest(t, app, "GET", "/v1/tenants/"+testTenant.ID.String(), nil, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		data := testutil.GetDataField(t, jsonResp)
		if data["name"] != "Test Tenant" {
			t.Errorf("Expected name 'Test Tenant', got '%v'", data["name"])
		}
		if data["slug"] != "test-tenant" {
			t.Errorf("Expected slug 'test-tenant', got '%v'", data["slug"])
		}
	})
}

func TestTenantHandler_GetTenantBySlug(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testutil.CreateTestTenant(t, db, "Test Tenant", "test-slug")

		app, token := setupTenantTestApp(t, db)

		resp := testutil.MakeRequest(t, app, "GET", "/v1/tenants/slug/test-slug", nil, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		data := testutil.GetDataField(t, jsonResp)
		if data["slug"] != "test-slug" {
			t.Errorf("Expected slug 'test-slug', got '%v'", data["slug"])
		}
	})
}

func TestTenantHandler_ListTenants(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		// Create multiple tenants
		testutil.CreateTestTenant(t, db, "Tenant 1", "tenant-1")
		testutil.CreateTestTenant(t, db, "Tenant 2", "tenant-2")
		testutil.CreateTestTenant(t, db, "Tenant 3", "tenant-3")

		app, token := setupTenantTestApp(t, db)

		resp := testutil.MakeRequest(t, app, "GET", "/v1/tenants?page=1&pageSize=10", nil, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		data := testutil.GetDataField(t, jsonResp)
		tenants, ok := data["tenants"].([]interface{})
		if !ok {
			t.Fatal("tenants field is not an array")
		}

		if len(tenants) != 3 {
			t.Errorf("Expected 3 tenants, got %d", len(tenants))
		}

		pagination, ok := data["pagination"].(map[string]interface{})
		if !ok {
			t.Fatal("pagination field is missing or not a map")
		}

		if pagination["total"] != float64(3) {
			t.Errorf("Expected total 3, got %v", pagination["total"])
		}
	})
}

func TestTenantHandler_UpdateTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Original Name", "test-tenant")

		app, token := setupTenantTestApp(t, db)

		body := map[string]interface{}{
			"name":     "Updated Name",
			"maxUsers": 2000,
		}

		resp := testutil.MakeRequest(t, app, "PATCH", "/v1/tenants/"+testTenant.ID.String(), body, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		data := testutil.GetDataField(t, jsonResp)
		if data["name"] != "Updated Name" {
			t.Errorf("Expected name 'Updated Name', got '%v'", data["name"])
		}
		if data["maxUsers"] != float64(2000) {
			t.Errorf("Expected maxUsers 2000, got %v", data["maxUsers"])
		}
	})
}

func TestTenantHandler_DeleteTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")

		app, token := setupTenantTestApp(t, db)

		resp := testutil.MakeRequest(t, app, "DELETE", "/v1/tenants/"+testTenant.ID.String(), nil, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)
	})
}

func TestTenantHandler_SuspendTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")

		app, token := setupTenantTestApp(t, db)

		resp := testutil.MakeRequest(t, app, "POST", "/v1/tenants/"+testTenant.ID.String()+"/suspend", nil, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		// Verify status changed
		getResp := testutil.MakeRequest(t, app, "GET", "/v1/tenants/"+testTenant.ID.String(), nil, testutil.WithAuthHeader(token))
		getData := testutil.GetDataField(t, testutil.ParseJSONResponse(t, getResp))
		if getData["status"] != "suspended" {
			t.Errorf("Expected status 'suspended', got '%v'", getData["status"])
		}
	})
}

func TestTenantHandler_ActivateTenant(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")

		app, token := setupTenantTestApp(t, db)

		// First suspend
		testutil.MakeRequest(t, app, "POST", "/v1/tenants/"+testTenant.ID.String()+"/suspend", nil, testutil.WithAuthHeader(token))

		// Then activate
		resp := testutil.MakeRequest(t, app, "POST", "/v1/tenants/"+testTenant.ID.String()+"/activate", nil, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		// Verify status changed
		getResp := testutil.MakeRequest(t, app, "GET", "/v1/tenants/"+testTenant.ID.String(), nil, testutil.WithAuthHeader(token))
		getData := testutil.GetDataField(t, testutil.ParseJSONResponse(t, getResp))
		if getData["status"] != "active" {
			t.Errorf("Expected status 'active', got '%v'", getData["status"])
		}
	})
}

func TestTenantHandler_GetTenantStats(t *testing.T) {
	testutil.WithTestDB(t, func(t *testing.T, db *gorm.DB) {
		testutil.TruncateTables(t, db)

		testTenant := testutil.CreateTestTenant(t, db, "Test Tenant", "test-tenant")
		// Create users and roles
		testutil.CreateTestUser(t, db, testTenant, "user1@example.com")
		testutil.CreateTestUser(t, db, testTenant, "user2@example.com")
		testutil.CreateTestRole(t, db, testTenant, "Admin")

		app, token := setupTenantTestApp(t, db)

		resp := testutil.MakeRequest(t, app, "GET", "/v1/tenants/"+testTenant.ID.String()+"/stats", nil, testutil.WithAuthHeader(token))

		testutil.AssertStatusCode(t, http.StatusOK, resp.Code)

		jsonResp := testutil.ParseJSONResponse(t, resp)
		testutil.AssertJSONSuccess(t, jsonResp)

		data := testutil.GetDataField(t, jsonResp)
		if data["userCount"] != float64(2) {
			t.Errorf("Expected userCount 2, got %v", data["userCount"])
		}
		if data["roleCount"] != float64(1) {
			t.Errorf("Expected roleCount 1, got %v", data["roleCount"])
		}
	})
}
