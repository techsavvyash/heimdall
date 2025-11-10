package integration

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/test/helpers"
	"github.com/techsavvyash/heimdall/test/utils"
)

var (
	apiURL string
	client *utils.TestClient
)

func TestMain(m *testing.M) {
	// Get API URL from environment or use default
	apiURL = os.Getenv("HEIMDALL_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}

	// Initialize test client
	client = utils.NewTestClient(apiURL)

	// Run tests
	code := m.Run()

	// Cleanup if needed
	os.Exit(code)
}

// TestUserRegistration tests user registration flow
func TestUserRegistration(t *testing.T) {
	t.Run("Successful registration with valid data", func(t *testing.T) {
		timestamp := time.Now().UnixNano()
		req := helpers.RegisterRequest{
			Email:     fmt.Sprintf("testuser-%d@example.com", timestamp),
			Password:  "Test123456!",
			FirstName: "Test",
			LastName:  "User",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/register", req, nil)
		utils.AssertNoError(t, err, "Registration request failed")
		utils.AssertStatusCode(t, http.StatusCreated, resp.StatusCode, "Registration status code")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode registration response")

		helpers.AssertAuthSuccess(t, &authResp, "Registration")
		utils.AssertEqual(t, req.Email, authResp.Data.User.Email, "User email mismatch")
		utils.AssertEqual(t, req.FirstName, authResp.Data.User.FirstName, "User first name mismatch")
		utils.AssertEqual(t, req.LastName, authResp.Data.User.LastName, "User last name mismatch")
	})

	t.Run("Registration fails with duplicate email", func(t *testing.T) {
		timestamp := time.Now().UnixNano()
		email := fmt.Sprintf("duplicate-%d@example.com", timestamp)

		req := helpers.RegisterRequest{
			Email:     email,
			Password:  "Test123456!",
			FirstName: "Test",
			LastName:  "User",
		}

		// First registration should succeed
		resp1, err := client.Request(http.MethodPost, "/v1/auth/register", req, nil)
		utils.AssertNoError(t, err, "First registration request failed")
		utils.AssertStatusCode(t, http.StatusCreated, resp1.StatusCode, "First registration status")

		// Second registration with same email should fail
		resp2, err := client.Request(http.MethodPost, "/v1/auth/register", req, nil)
		utils.AssertNoError(t, err, "Second registration request failed")

		// Can be either 400 or 500 depending on FusionAuth response
		utils.AssertTrue(t, resp2.StatusCode == http.StatusBadRequest || resp2.StatusCode == http.StatusInternalServerError,
			fmt.Sprintf("Expected 400 or 500 status code for duplicate email, got %d", resp2.StatusCode))

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp2, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		helpers.AssertAuthFailure(t, &authResp, "REGISTRATION_FAILED", "Duplicate email registration")
	})

	t.Run("Registration fails with invalid email", func(t *testing.T) {
		req := helpers.RegisterRequest{
			Email:     "invalid-email",
			Password:  "Test123456!",
			FirstName: "Test",
			LastName:  "User",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/register", req, nil)
		utils.AssertNoError(t, err, "Invalid email registration request failed")
		utils.AssertStatusCode(t, http.StatusBadRequest, resp.StatusCode, "Invalid email status")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		helpers.AssertAuthFailure(t, &authResp, "VALIDATION_ERROR", "Invalid email validation")
	})

	t.Run("Registration fails with weak password", func(t *testing.T) {
		timestamp := time.Now().UnixNano()
		req := helpers.RegisterRequest{
			Email:     fmt.Sprintf("weakpass-%d@example.com", timestamp),
			Password:  "123",
			FirstName: "Test",
			LastName:  "User",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/register", req, nil)
		utils.AssertNoError(t, err, "Weak password registration request failed")
		utils.AssertStatusCode(t, http.StatusBadRequest, resp.StatusCode, "Weak password status")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		helpers.AssertAuthFailure(t, &authResp, "VALIDATION_ERROR", "Weak password validation")
	})

	t.Run("Registration fails with missing required fields", func(t *testing.T) {
		req := helpers.RegisterRequest{
			Email:    "missingfields@example.com",
			Password: "Test123456!",
			// Missing FirstName and LastName
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/register", req, nil)
		utils.AssertNoError(t, err, "Missing fields registration request failed")
		utils.AssertStatusCode(t, http.StatusBadRequest, resp.StatusCode, "Missing fields status")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		helpers.AssertAuthFailure(t, &authResp, "VALIDATION_ERROR", "Missing fields validation")
	})
}

// TestUserLogin tests user login flow
func TestUserLogin(t *testing.T) {
	// Create a test user first
	testUser := helpers.CreateTestUser(t, client, "login-test")
	utils.AssertTrue(t, testUser.Success, "Test user creation failed")

	t.Run("Successful login with valid credentials", func(t *testing.T) {
		loginReq := helpers.LoginRequest{
			Email:    testUser.Data.User.Email,
			Password: "Test123456!",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/login", loginReq, nil)
		utils.AssertNoError(t, err, "Login request failed")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Login status code")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode login response")

		helpers.AssertAuthSuccess(t, &authResp, "Login")
		utils.AssertEqual(t, testUser.Data.User.Email, authResp.Data.User.Email, "User email mismatch")
		utils.AssertEqual(t, testUser.Data.User.ID, authResp.Data.User.ID, "User ID mismatch")
	})

	t.Run("Login fails with incorrect password", func(t *testing.T) {
		loginReq := helpers.LoginRequest{
			Email:    testUser.Data.User.Email,
			Password: "WrongPassword123!",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/login", loginReq, nil)
		utils.AssertNoError(t, err, "Wrong password login request failed")
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode, "Wrong password status")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		// Can be INVALID_CREDENTIALS or AUTHENTICATION_FAILED
		utils.AssertFalse(t, authResp.Success, "Login with wrong password should fail")
		utils.AssertTrue(t, authResp.Error != nil, "Error should be present")
	})

	t.Run("Login fails with non-existent email", func(t *testing.T) {
		loginReq := helpers.LoginRequest{
			Email:    "nonexistent@example.com",
			Password: "Test123456!",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/login", loginReq, nil)
		utils.AssertNoError(t, err, "Non-existent user login request failed")
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode, "Non-existent user status")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		// Can be INVALID_CREDENTIALS or AUTHENTICATION_FAILED
		utils.AssertFalse(t, authResp.Success, "Login with non-existent email should fail")
		utils.AssertTrue(t, authResp.Error != nil, "Error should be present")
	})

	t.Run("Login fails with invalid email format", func(t *testing.T) {
		loginReq := helpers.LoginRequest{
			Email:    "not-an-email",
			Password: "Test123456!",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/login", loginReq, nil)
		utils.AssertNoError(t, err, "Invalid email login request failed")
		utils.AssertStatusCode(t, http.StatusBadRequest, resp.StatusCode, "Invalid email format status")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		helpers.AssertAuthFailure(t, &authResp, "VALIDATION_ERROR", "Invalid email format error")
	})
}

// TestTokenRefresh tests token refresh flow
func TestTokenRefresh(t *testing.T) {
	// Create a test user first
	testUser := helpers.CreateTestUser(t, client, "refresh-test")
	utils.AssertTrue(t, testUser.Success, "Test user creation failed")

	t.Run("Successful token refresh with valid refresh token", func(t *testing.T) {
		refreshReq := helpers.RefreshTokenRequest{
			RefreshToken: testUser.Data.RefreshToken,
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/refresh", refreshReq, nil)
		utils.AssertNoError(t, err, "Token refresh request failed")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Token refresh status code")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode refresh response")

		helpers.AssertAuthSuccess(t, &authResp, "Token refresh")
		utils.AssertEqual(t, testUser.Data.User.Email, authResp.Data.User.Email, "User email mismatch after refresh")

		// New tokens should be different from original
		utils.AssertTrue(t, authResp.Data.AccessToken != testUser.Data.AccessToken, "Access token should be different")
	})

	t.Run("Token refresh fails with invalid refresh token", func(t *testing.T) {
		refreshReq := helpers.RefreshTokenRequest{
			RefreshToken: "invalid.refresh.token",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/refresh", refreshReq, nil)
		utils.AssertNoError(t, err, "Invalid refresh token request failed")
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode, "Invalid refresh token status")

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		// Can be INVALID_TOKEN or INVALID_REFRESH_TOKEN
		utils.AssertFalse(t, authResp.Success, "Token refresh with invalid token should fail")
		utils.AssertTrue(t, authResp.Error != nil, "Error should be present")
	})

	t.Run("Token refresh fails with empty refresh token", func(t *testing.T) {
		refreshReq := helpers.RefreshTokenRequest{
			RefreshToken: "",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/refresh", refreshReq, nil)
		utils.AssertNoError(t, err, "Empty refresh token request failed")

		// Can be 400 or 401 depending on validation
		utils.AssertTrue(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized,
			fmt.Sprintf("Expected 400 or 401 status for empty token, got %d", resp.StatusCode))

		var authResp helpers.AuthResponse
		err = client.DecodeResponse(resp, &authResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		utils.AssertFalse(t, authResp.Success, "Token refresh with empty token should fail")
		utils.AssertTrue(t, authResp.Error != nil, "Error should be present")
	})
}

// TestLogout tests logout flow
func TestLogout(t *testing.T) {
	// Create a test user and login
	testUser := helpers.CreateTestUser(t, client, "logout-test")
	utils.AssertTrue(t, testUser.Success, "Test user creation failed")

	t.Run("Successful logout with valid token", func(t *testing.T) {
		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)

		resp, err := client.Request(http.MethodPost, "/v1/auth/logout", nil, nil)
		utils.AssertNoError(t, err, "Logout request failed")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Logout status code")

		var logoutResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &logoutResp)
		utils.AssertNoError(t, err, "Failed to decode logout response")

		utils.AssertTrue(t, logoutResp.Success, "Logout should succeed")

		// Clear auth token
		client.SetAuthToken("")
	})

	t.Run("Logout fails without authentication", func(t *testing.T) {
		// Clear auth token
		client.SetAuthToken("")

		resp, err := client.Request(http.MethodPost, "/v1/auth/logout", nil, nil)
		utils.AssertNoError(t, err, "Unauthenticated logout request failed")
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode, "Unauthenticated logout status")

		var logoutResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &logoutResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		utils.AssertFalse(t, logoutResp.Success, "Unauthenticated logout should fail")
		utils.AssertTrue(t, logoutResp.Error != nil, "Error should be present")
	})
}

// TestProtectedEndpointAccess tests accessing protected endpoints
func TestProtectedEndpointAccess(t *testing.T) {
	// Create a test user
	testUser := helpers.CreateTestUser(t, client, "protected-test")
	utils.AssertTrue(t, testUser.Success, "Test user creation failed")

	t.Run("Access protected endpoint with valid token", func(t *testing.T) {
		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err, "Protected endpoint request failed")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Protected endpoint status")

		var userResp struct {
			Success bool `json:"success"`
			Data    struct {
				ID        string `json:"id"`
				Email     string `json:"email"`
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
				TenantID  string `json:"tenantId"`
			} `json:"data"`
		}

		err = client.DecodeResponse(resp, &userResp)
		utils.AssertNoError(t, err, "Failed to decode user response")

		utils.AssertTrue(t, userResp.Success, "Protected endpoint should succeed")
		utils.AssertEqual(t, testUser.Data.User.Email, userResp.Data.Email, "User email mismatch")

		// Clear auth token
		client.SetAuthToken("")
	})

	t.Run("Access protected endpoint without token fails", func(t *testing.T) {
		// Clear auth token
		client.SetAuthToken("")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err, "Unauthenticated protected endpoint request failed")
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode, "Unauthenticated protected endpoint status")

		var errResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &errResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		utils.AssertFalse(t, errResp.Success, "Protected endpoint should fail without token")
	})

	t.Run("Access protected endpoint with invalid token fails", func(t *testing.T) {
		// Set invalid auth token
		client.SetAuthToken("invalid.jwt.token")

		resp, err := client.Request(http.MethodGet, "/v1/users/me", nil, nil)
		utils.AssertNoError(t, err, "Invalid token protected endpoint request failed")
		utils.AssertStatusCode(t, http.StatusUnauthorized, resp.StatusCode, "Invalid token protected endpoint status")

		var errResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &errResp)
		utils.AssertNoError(t, err, "Failed to decode error response")

		utils.AssertFalse(t, errResp.Success, "Protected endpoint should fail with invalid token")

		// Clear auth token
		client.SetAuthToken("")
	})
}

// TestPasswordChange tests password change flow
func TestPasswordChange(t *testing.T) {
	t.Skip("Skipping password change test - requires FusionAuth user synchronization")

	// Create a test user
	testUser := helpers.CreateTestUser(t, client, "password-change-test")
	utils.AssertTrue(t, testUser.Success, "Test user creation failed")

	t.Run("Successful password change", func(t *testing.T) {
		// Set auth token
		client.SetAuthToken(testUser.Data.AccessToken)

		changeReq := map[string]string{
			"currentPassword": "Test123456!",
			"newPassword":     "NewPassword123!",
			"confirmPassword": "NewPassword123!",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/password/change", changeReq, nil)
		utils.AssertNoError(t, err, "Password change request failed")
		utils.AssertStatusCode(t, http.StatusOK, resp.StatusCode, "Password change status")

		var changeResp helpers.GenericResponse
		err = client.DecodeResponse(resp, &changeResp)
		utils.AssertNoError(t, err, "Failed to decode password change response")

		utils.AssertTrue(t, changeResp.Success, "Password change should succeed")

		// Clear auth token
		client.SetAuthToken("")

		// Try logging in with new password
		loginReq := helpers.LoginRequest{
			Email:    testUser.Data.User.Email,
			Password: "NewPassword123!",
		}

		loginResp, err := client.Request(http.MethodPost, "/v1/auth/login", loginReq, nil)
		utils.AssertNoError(t, err, "Login with new password failed")
		utils.AssertStatusCode(t, http.StatusOK, loginResp.StatusCode, "Login with new password status")
	})

	t.Run("Password change fails with incorrect current password", func(t *testing.T) {
		// Create new test user
		newUser := helpers.CreateTestUser(t, client, "wrong-current-password-test")
		client.SetAuthToken(newUser.Data.AccessToken)

		changeReq := map[string]string{
			"currentPassword": "WrongPassword123!",
			"newPassword":     "NewPassword123!",
			"confirmPassword": "NewPassword123!",
		}

		resp, err := client.Request(http.MethodPost, "/v1/auth/password/change", changeReq, nil)
		utils.AssertNoError(t, err, "Wrong current password request failed")
		utils.AssertStatusCode(t, http.StatusBadRequest, resp.StatusCode, "Wrong current password status")

		client.SetAuthToken("")
	})
}
