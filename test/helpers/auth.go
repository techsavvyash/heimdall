package helpers

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/test/utils"
)

// RegisterRequest represents user registration request
type RegisterRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	TenantID  string `json:"tenantId,omitempty"`
}

// LoginRequest represents login request
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RefreshTokenRequest represents token refresh request
type RefreshTokenRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// AuthResponse represents authentication response
type AuthResponse struct {
	Success bool `json:"success"`
	Data    struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		TokenType    string `json:"tokenType"`
		ExpiresIn    int    `json:"expiresIn"`
		User         struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
			TenantID  string `json:"tenantId"`
		} `json:"user"`
	} `json:"data"`
	Error *ErrorResponse `json:"error,omitempty"`
}

// ErrorResponse represents error response
type ErrorResponse struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// RegisterUser registers a new user and returns the auth response
func RegisterUser(t *testing.T, client *utils.TestClient, req RegisterRequest) *AuthResponse {
	t.Helper()

	resp, err := client.Request(http.MethodPost, "/v1/auth/register", req, nil)
	utils.AssertNoError(t, err, "Failed to make registration request")

	var authResp AuthResponse
	err = client.DecodeResponse(resp, &authResp)
	utils.AssertNoError(t, err, "Failed to decode registration response")

	return &authResp
}

// LoginUser logs in a user and returns the auth response
func LoginUser(t *testing.T, client *utils.TestClient, req LoginRequest) *AuthResponse {
	t.Helper()

	resp, err := client.Request(http.MethodPost, "/v1/auth/login", req, nil)
	utils.AssertNoError(t, err, "Failed to make login request")

	var authResp AuthResponse
	err = client.DecodeResponse(resp, &authResp)
	utils.AssertNoError(t, err, "Failed to decode login response")

	return &authResp
}

// RefreshToken refreshes access token using refresh token
func RefreshToken(t *testing.T, client *utils.TestClient, refreshToken string) *AuthResponse {
	t.Helper()

	req := RefreshTokenRequest{
		RefreshToken: refreshToken,
	}

	resp, err := client.Request(http.MethodPost, "/v1/auth/refresh", req, nil)
	utils.AssertNoError(t, err, "Failed to make token refresh request")

	var authResp AuthResponse
	err = client.DecodeResponse(resp, &authResp)
	utils.AssertNoError(t, err, "Failed to decode refresh response")

	return &authResp
}

// Logout logs out the current user
func Logout(t *testing.T, client *utils.TestClient) *GenericResponse {
	t.Helper()

	resp, err := client.Request(http.MethodPost, "/v1/auth/logout", nil, nil)
	utils.AssertNoError(t, err, "Failed to make logout request")

	var logoutResp GenericResponse
	err = client.DecodeResponse(resp, &logoutResp)
	utils.AssertNoError(t, err, "Failed to decode logout response")

	return &logoutResp
}

// GenericResponse represents a generic API response
type GenericResponse struct {
	Success bool           `json:"success"`
	Data    interface{}    `json:"data,omitempty"`
	Error   *ErrorResponse `json:"error,omitempty"`
}

// CreateTestUser creates a test user with a unique email
func CreateTestUser(t *testing.T, client *utils.TestClient, prefix string) *AuthResponse {
	t.Helper()

	timestamp := time.Now().UnixNano()
	email := fmt.Sprintf("%s-%d@test.com", prefix, timestamp)

	req := RegisterRequest{
		Email:     email,
		Password:  "Test123456!",
		FirstName: "Test",
		LastName:  "User",
	}

	return RegisterUser(t, client, req)
}

// AssertAuthSuccess asserts authentication was successful
func AssertAuthSuccess(t *testing.T, resp *AuthResponse, context string) {
	t.Helper()

	utils.AssertTrue(t, resp.Success, fmt.Sprintf("%s: Expected success to be true", context))
	utils.AssertNotEmpty(t, resp.Data.AccessToken, fmt.Sprintf("%s: Access token should not be empty", context))
	utils.AssertNotEmpty(t, resp.Data.RefreshToken, fmt.Sprintf("%s: Refresh token should not be empty", context))
	utils.AssertEqual(t, "Bearer", resp.Data.TokenType, fmt.Sprintf("%s: Token type should be Bearer", context))
	utils.AssertTrue(t, resp.Data.ExpiresIn > 0, fmt.Sprintf("%s: ExpiresIn should be > 0", context))
	utils.AssertNotEmpty(t, resp.Data.User.ID, fmt.Sprintf("%s: User ID should not be empty", context))
	utils.AssertNotEmpty(t, resp.Data.User.Email, fmt.Sprintf("%s: User email should not be empty", context))
}

// AssertAuthFailure asserts authentication failed with expected error
func AssertAuthFailure(t *testing.T, resp *AuthResponse, expectedCode string, context string) {
	t.Helper()

	utils.AssertFalse(t, resp.Success, fmt.Sprintf("%s: Expected success to be false", context))
	utils.AssertTrue(t, resp.Error != nil, fmt.Sprintf("%s: Error should not be nil", context))
	utils.AssertEqual(t, expectedCode, resp.Error.Code, fmt.Sprintf("%s: Error code mismatch", context))
}
