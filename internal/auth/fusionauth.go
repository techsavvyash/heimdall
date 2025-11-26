package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/config"
)

// FusionAuthClient wraps FusionAuth API interactions
type FusionAuthClient struct {
	baseURL       string
	apiKey        string
	tenantID      string
	applicationID string
	httpClient    *http.Client
}

// NewFusionAuthClient creates a new FusionAuth client
func NewFusionAuthClient(cfg *config.AuthConfig) *FusionAuthClient {
	return &FusionAuthClient{
		baseURL:       cfg.URL,
		apiKey:        cfg.APIKey,
		tenantID:      cfg.TenantID,
		applicationID: cfg.ApplicationID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// RegisterRequest represents a user registration request
type RegisterRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// LoginRequest represents a login request
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// FusionAuthUser represents a FusionAuth user
type FusionAuthUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Active    bool   `json:"active"`
	Verified  bool   `json:"verified"`
}

// FusionAuthResponse represents a generic FusionAuth API response
type FusionAuthResponse struct {
	User  *FusionAuthUser `json:"user,omitempty"`
	Token string          `json:"token,omitempty"`
}

// Register creates a new user in FusionAuth
func (c *FusionAuthClient) Register(req *RegisterRequest) (*FusionAuthUser, error) {
	userID := uuid.New().String()

	payload := map[string]interface{}{
		"user": map[string]interface{}{
			"id":        userID,
			"email":     req.Email,
			"password":  req.Password,
			"firstName": req.FirstName,
			"lastName":  req.LastName,
		},
		"registration": map[string]interface{}{
			"applicationId": c.applicationID,
		},
		"sendSetPasswordEmail": false,
		"skipVerification":     false,
	}

	resp, err := c.doRequest("POST", "/api/user/registration", payload)
	if err != nil {
		return nil, err
	}

	var result FusionAuthResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.User, nil
}

// Login authenticates a user
func (c *FusionAuthClient) Login(req *LoginRequest) (*FusionAuthUser, error) {
	payload := map[string]interface{}{
		"loginId":       req.Email,
		"password":      req.Password,
		"applicationId": c.applicationID,
	}

	resp, err := c.doRequest("POST", "/api/login", payload)
	if err != nil {
		return nil, err
	}

	var result FusionAuthResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.User, nil
}

// GetUser retrieves a user by ID
func (c *FusionAuthClient) GetUser(userID string) (*FusionAuthUser, error) {
	resp, err := c.doRequest("GET", fmt.Sprintf("/api/user/%s", userID), nil)
	if err != nil {
		return nil, err
	}

	var result FusionAuthResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.User, nil
}

// UpdateUser updates a user's information using PATCH for partial updates
func (c *FusionAuthClient) UpdateUser(userID string, updates map[string]interface{}) (*FusionAuthUser, error) {
	payload := map[string]interface{}{
		"user": updates,
	}

	// Use PATCH for partial updates (PUT requires email/username)
	resp, err := c.doRequest("PATCH", fmt.Sprintf("/api/user/%s", userID), payload)
	if err != nil {
		return nil, err
	}

	var result FusionAuthResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.User, nil
}

// DeleteUser deletes a user
func (c *FusionAuthClient) DeleteUser(userID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/api/user/%s", userID), nil)
	return err
}

// ChangePassword changes a user's password
func (c *FusionAuthClient) ChangePassword(userID, currentPassword, newPassword string) error {
	payload := map[string]interface{}{
		"currentPassword": currentPassword,
		"password":        newPassword,
	}

	_, err := c.doRequest("POST", fmt.Sprintf("/api/user/change-password/%s", userID), payload)
	return err
}

// ForgotPassword initiates a password reset
func (c *FusionAuthClient) ForgotPassword(email string) error {
	payload := map[string]interface{}{
		"loginId":       email,
		"applicationId": c.applicationID,
		"sendForgotPasswordEmail": true,
	}

	_, err := c.doRequest("POST", "/api/user/forgot-password", payload)
	return err
}

// VerifyEmail sends a verification email
func (c *FusionAuthClient) VerifyEmail(email string) error {
	payload := map[string]interface{}{
		"email":         email,
		"applicationId": c.applicationID,
	}

	_, err := c.doRequest("PUT", "/api/user/verify-email", payload)
	return err
}

// DeactivateUser deactivates a user account
func (c *FusionAuthClient) DeactivateUser(userID string) error {
	payload := map[string]interface{}{
		"user": map[string]interface{}{
			"active": false,
		},
	}

	_, err := c.doRequest("PUT", fmt.Sprintf("/api/user/%s", userID), payload)
	return err
}

// ReactivateUser reactivates a user account
func (c *FusionAuthClient) ReactivateUser(userID string) error {
	payload := map[string]interface{}{
		"user": map[string]interface{}{
			"active": true,
		},
	}

	_, err := c.doRequest("PUT", fmt.Sprintf("/api/user/%s", userID), payload)
	return err
}

// doRequest performs an HTTP request to FusionAuth
func (c *FusionAuthClient) doRequest(method, path string, body interface{}) ([]byte, error) {
	url := c.baseURL + path

	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if c.tenantID != "" {
		req.Header.Set("X-FusionAuth-TenantId", c.tenantID)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("FusionAuth API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
