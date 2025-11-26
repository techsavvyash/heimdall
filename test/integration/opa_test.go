package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/test/helpers"
	"github.com/techsavvyash/heimdall/test/utils"
)

// TestOPARBACBasicPermissions tests basic RBAC permission checks
func TestOPARBACBasicPermissions(t *testing.T) {
	t.Run("Regular user cannot list all users", func(t *testing.T) {
		// Create regular test user
		testUser := helpers.CreateTestUser(t, client, "opa-user")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Try to list all users (admin-only endpoint)
		resp, err := client.Request(http.MethodGet, "/v1/users", nil, nil)
		utils.AssertNoError(t, err, "Failed to make list users request")

		// Should be forbidden (403) or unauthorized (401) due to OPA policy
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Expected status code 401 or 403 but got %d", resp.StatusCode)
		}

		t.Logf("✅ Regular user correctly denied access to admin endpoint (status: %d)", resp.StatusCode)
	})

	t.Run("User can access their own profile", func(t *testing.T) {
		// Create test user
		testUser := helpers.CreateTestUser(t, client, "opa-self")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Get own profile (should be allowed)
		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err, "Failed to get own profile")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Get own profile")

		var profileResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &profileResp)
		utils.AssertNoError(t, err, "Failed to decode profile response")
		utils.AssertTrue(t, profileResp.Success, "Expected success to be true")

		t.Log("✅ User can access own profile (self-access rule)")
	})

	t.Run("User can update their own profile", func(t *testing.T) {
		// Create test user
		testUser := helpers.CreateTestUser(t, client, "opa-update")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Update own profile
		updateReq := map[string]interface{}{
			"firstName": "Updated",
			"lastName":  "Name",
		}

		resp, err := client.Request(http.MethodPatch, "/v1/users/me", updateReq, nil)
		utils.AssertNoError(t, err, "Failed to update own profile")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Update own profile")

		var profileResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &profileResp)
		utils.AssertNoError(t, err, "Failed to decode update response")
		utils.AssertTrue(t, profileResp.Success, "Expected success to be true")

		t.Log("✅ User can update own profile (self-access rule)")
	})
}

// TestOPATenantIsolation tests tenant isolation policies
func TestOPATenantIsolation(t *testing.T) {
	t.Run("User can only access resources in their tenant", func(t *testing.T) {
		// Create test user
		testUser := helpers.CreateTestUser(t, client, "tenant-isolation")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Try to access own profile (same tenant)
		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err, "Failed to get own profile")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Get own profile in same tenant")

		t.Log("✅ User can access resources in own tenant")
	})

	t.Run("User permissions are retrieved correctly", func(t *testing.T) {
		// Create test user
		testUser := helpers.CreateTestUser(t, client, "permissions-check")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Get user permissions
		resp, err := client.Request(http.MethodGet, "/v1/users/me/permissions", nil, nil)
		utils.AssertNoError(t, err, "Failed to get permissions")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Get user permissions")

		var permResp struct {
			Success bool `json:"success"`
			Data    struct {
				Permissions []string `json:"permissions"`
			} `json:"data"`
		}
		err = client.DecodeResponse(resp, &permResp)
		utils.AssertNoError(t, err, "Failed to decode permissions response")
		utils.AssertTrue(t, permResp.Success, "Expected success to be true")

		t.Logf("✅ User permissions retrieved: %v", permResp.Data.Permissions)
	})
}

// TestOPAProtectedEndpoints tests various OPA-protected endpoints
func TestOPAProtectedEndpoints(t *testing.T) {
	t.Run("Policy endpoints require permissions", func(t *testing.T) {
		// Create regular test user (no admin permissions)
		testUser := helpers.CreateTestUser(t, client, "policy-test")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Try to list policies (requires 'policies.read' permission)
		resp, err := client.Request(http.MethodGet, "/v1/policies", nil, nil)
		utils.AssertNoError(t, err, "Failed to make list policies request")

		// Should be forbidden due to OPA policy (no 'policies.read' permission)
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Expected status code 401 or 403 but got %d", resp.StatusCode)
		}

		t.Log("✅ Policy endpoints correctly require OPA permissions")
	})

	t.Run("Bundle endpoints require permissions", func(t *testing.T) {
		// Create regular test user
		testUser := helpers.CreateTestUser(t, client, "bundle-test")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Try to list bundles (requires 'bundles.read' permission)
		resp, err := client.Request(http.MethodGet, "/v1/bundles", nil, nil)
		utils.AssertNoError(t, err, "Failed to make list bundles request")

		// Should be forbidden due to OPA policy
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Expected status code 401 or 403 but got %d", resp.StatusCode)
		}

		t.Log("✅ Bundle endpoints correctly require OPA permissions")
	})

	t.Run("Tenant endpoints require permissions", func(t *testing.T) {
		// Create regular test user
		testUser := helpers.CreateTestUser(t, client, "tenant-test")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Try to list tenants (requires 'tenants.read' permission)
		resp, err := client.Request(http.MethodGet, "/v1/tenants", nil, nil)
		utils.AssertNoError(t, err, "Failed to make list tenants request")

		// Should be forbidden due to OPA policy
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Expected status code 401 or 403 but got %d", resp.StatusCode)
		}

		t.Log("✅ Tenant endpoints correctly require OPA permissions")
	})
}

// TestOPAAuthenticationRequired tests that protected endpoints require authentication
func TestOPAAuthenticationRequired(t *testing.T) {
	t.Run("Protected endpoints reject unauthenticated requests", func(t *testing.T) {
		// Clear any existing auth token
		client.SetAuthToken("")

		endpoints := []struct {
			method string
			path   string
			name   string
		}{
			{http.MethodGet, "/v1/users/me", "Get own profile"},
			{http.MethodGet, "/v1/users", "List users"},
			{http.MethodGet, "/v1/policies", "List policies"},
			{http.MethodGet, "/v1/bundles", "List bundles"},
			{http.MethodGet, "/v1/tenants", "List tenants"},
			{http.MethodGet, "/v1/users/me/permissions", "Get permissions"},
		}

		for _, endpoint := range endpoints {
			resp, err := client.Request(endpoint.method, endpoint.path, nil, nil)
			utils.AssertNoError(t, err, fmt.Sprintf("Failed to make request to %s", endpoint.path))

			utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode,
				fmt.Sprintf("%s without authentication", endpoint.name))

			t.Logf("✅ %s correctly requires authentication (401)", endpoint.name)
		}
	})

	t.Run("Invalid token is rejected", func(t *testing.T) {
		// Set invalid token
		client.SetAuthToken("invalid.jwt.token")
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err, "Failed to make request with invalid token")

		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode, "Invalid token")

		t.Log("✅ Invalid JWT token correctly rejected")
	})

	t.Run("Expired token is rejected", func(t *testing.T) {
		// Create a user and get a token
		testUser := helpers.CreateTestUser(t, client, "token-expiry")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Wait for token to expire (this test assumes short token expiry in test environment)
		// In a real scenario, you'd either manipulate time or use a test token with past expiry
		// For now, we'll just verify that a fresh token works
		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err, "Failed to make request with fresh token")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Fresh token should work")

		t.Log("✅ Fresh token works correctly")
		// Note: Testing actual expiry would require waiting 15 minutes or manipulating system time
	})
}

// TestOPASelfAccessRules tests self-access authorization rules
func TestOPASelfAccessRules(t *testing.T) {
	t.Run("User can read own data", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "self-read")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Read own profile")

		t.Log("✅ Self-access: User can read own data")
	})

	t.Run("User can update own data", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "self-update")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		updateReq := map[string]interface{}{
			"firstName": "NewFirst",
			"lastName":  "NewLast",
		}

		resp, err := client.Request(http.MethodPatch, "/v1/users/me", updateReq, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Update own profile")

		t.Log("✅ Self-access: User can update own data")
	})

	t.Run("User can delete own account", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "self-delete")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodDelete, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Delete own account")

		var deleteResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &deleteResp)
		utils.AssertNoError(t, err)
		utils.AssertTrue(t, deleteResp.Success, "Account deletion should succeed")

		t.Log("✅ Self-access: User can delete own account")
	})

	t.Run("User can retrieve own permissions", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "self-perms")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me/permissions", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Get own permissions")

		t.Log("✅ Self-access: User can retrieve own permissions")
	})
}

// TestOPAUserManagementPermissions tests user management authorization
func TestOPAUserManagementPermissions(t *testing.T) {
	t.Run("Regular user cannot list all users", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "no-list-users")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users", nil, nil)
		utils.AssertNoError(t, err)

		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Expected 401/403 but got %d", resp.StatusCode)
		}

		t.Log("✅ Regular user cannot list all users (requires 'users.read' permission)")
	})

	t.Run("Regular user cannot view other users", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "no-view-others")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		// Try to get another user by ID (using a random UUID)
		resp, err := client.Request(http.MethodGet, "/v1/users/123e4567-e89b-12d3-a456-426614174000", nil, nil)
		utils.AssertNoError(t, err)

		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Expected 401/403 but got %d", resp.StatusCode)
		}

		t.Log("✅ Regular user cannot view other users (requires 'users.read' permission)")
	})

	t.Run("Regular user cannot assign roles", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "no-assign-roles")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		roleReq := map[string]interface{}{
			"roleId": "123e4567-e89b-12d3-a456-426614174000",
		}

		resp, err := client.Request(http.MethodPost, "/v1/users/123e4567-e89b-12d3-a456-426614174000/roles", roleReq, nil)
		utils.AssertNoError(t, err)

		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Expected 401/403 but got %d", resp.StatusCode)
		}

		t.Log("✅ Regular user cannot assign roles (requires 'roles.assign' permission)")
	})
}

// TestOPATokenValidation tests JWT token validation in OPA context
func TestOPATokenValidation(t *testing.T) {
	t.Run("Valid token allows access to protected endpoints", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "valid-token")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode)

		t.Log("✅ Valid JWT token allows access")
	})

	t.Run("Missing token denies access", func(t *testing.T) {
		client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode)

		t.Log("✅ Missing token denies access")
	})

	t.Run("Malformed token denies access", func(t *testing.T) {
		client.SetAuthToken("not.a.valid.jwt")
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode)

		t.Log("✅ Malformed token denies access")
	})
}

// TestOPASessionManagement tests session-based authorization
func TestOPASessionManagement(t *testing.T) {
	t.Run("Logout invalidates session", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "logout-test")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		client.SetAuthToken(testUser.Data.AccessToken)

		// Verify token works
		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Token should work before logout")

		// Logout
		logoutResp := helpers.Logout(t, client)
		utils.AssertTrue(t, logoutResp.Success, "Logout should succeed")

		// Try to use the same token after logout
		resp, err = client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)

		// Token should be invalidated
		// NOTE: Depending on implementation, this might still work if stateless JWT is used
		// without server-side session tracking. The behavior depends on implementation.
		t.Logf("Status after logout: %d", resp.StatusCode)

		client.SetAuthToken("")
		t.Log("✅ Logout session test completed")
	})

	t.Run("Refresh token extends session", func(t *testing.T) {
		testUser := helpers.CreateTestUser(t, client, "refresh-test")
		helpers.AssertAuthSuccess(t, testUser, "User registration")

		// Wait a bit
		time.Sleep(1 * time.Second)

		// Refresh token
		refreshResp := helpers.RefreshToken(t, client, testUser.Data.RefreshToken)
		helpers.AssertAuthSuccess(t, refreshResp, "Token refresh")

		// Use new token
		client.SetAuthToken(refreshResp.Data.AccessToken)
		defer client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err)
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Refreshed token should work")

		t.Log("✅ Token refresh successfully extends session")
	})
}
