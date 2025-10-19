package auth

import (
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/internal/config"
)

func TestJWTService_GenerateTokenPair(t *testing.T) {
	jwtService, cleanup := CreateTestJWTService(t)
	defer cleanup()

	userID := "550e8400-e29b-41d4-a716-446655440000"
	tenantID := "660e8400-e29b-41d4-a716-446655440000"
	email := "test@example.com"
	roles := []string{"user", "admin"}

	tokens, err := jwtService.GenerateTokenPair(userID, tenantID, email, roles)
	if err != nil {
		t.Fatalf("Failed to generate token pair: %v", err)
	}

	// Verify tokens are not empty
	if tokens.AccessToken == "" {
		t.Error("Access token is empty")
	}
	if tokens.RefreshToken == "" {
		t.Error("Refresh token is empty")
	}
	if tokens.TokenType != "Bearer" {
		t.Errorf("Expected token type 'Bearer', got '%s'", tokens.TokenType)
	}
	if tokens.ExpiresIn <= 0 {
		t.Errorf("Expected positive expiresIn, got %d", tokens.ExpiresIn)
	}
}

func TestJWTService_ValidateAccessToken(t *testing.T) {
	jwtService, cleanup := CreateTestJWTService(t)
	defer cleanup()

	userID := "550e8400-e29b-41d4-a716-446655440000"
	tenantID := "660e8400-e29b-41d4-a716-446655440000"
	email := "test@example.com"
	roles := []string{"user"}

	// Generate token
	tokens, err := jwtService.GenerateTokenPair(userID, tenantID, email, roles)
	if err != nil {
		t.Fatalf("Failed to generate token pair: %v", err)
	}

	// Validate access token
	claims, err := jwtService.ValidateAccessToken(tokens.AccessToken)
	if err != nil {
		t.Fatalf("Failed to validate access token: %v", err)
	}

	// Verify claims
	if claims.UserID != userID {
		t.Errorf("Expected userID '%s', got '%s'", userID, claims.UserID)
	}
	if claims.TenantID != tenantID {
		t.Errorf("Expected tenantID '%s', got '%s'", tenantID, claims.TenantID)
	}
	if claims.Email != email {
		t.Errorf("Expected email '%s', got '%s'", email, claims.Email)
	}
	if claims.Type != "access" {
		t.Errorf("Expected type 'access', got '%s'", claims.Type)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "user" {
		t.Errorf("Expected roles ['user'], got %v", claims.Roles)
	}
}

func TestJWTService_ValidateRefreshToken(t *testing.T) {
	jwtService, cleanup := CreateTestJWTService(t)
	defer cleanup()

	userID := "550e8400-e29b-41d4-a716-446655440000"
	tenantID := "660e8400-e29b-41d4-a716-446655440000"
	email := "test@example.com"
	roles := []string{"user"}

	// Generate token
	tokens, err := jwtService.GenerateTokenPair(userID, tenantID, email, roles)
	if err != nil {
		t.Fatalf("Failed to generate token pair: %v", err)
	}

	// Validate refresh token
	claims, err := jwtService.ValidateRefreshToken(tokens.RefreshToken)
	if err != nil {
		t.Fatalf("Failed to validate refresh token: %v", err)
	}

	// Verify claims
	if claims.UserID != userID {
		t.Errorf("Expected userID '%s', got '%s'", userID, claims.UserID)
	}
	if claims.Type != "refresh" {
		t.Errorf("Expected type 'refresh', got '%s'", claims.Type)
	}
}

func TestJWTService_ValidateInvalidToken(t *testing.T) {
	jwtService, cleanup := CreateTestJWTService(t)
	defer cleanup()

	invalidToken := "invalid.token.here"

	_, err := jwtService.ValidateAccessToken(invalidToken)
	if err == nil {
		t.Error("Expected error for invalid token, got nil")
	}
}

func TestJWTService_ValidateWrongTokenType(t *testing.T) {
	jwtService, cleanup := CreateTestJWTService(t)
	defer cleanup()

	userID := "550e8400-e29b-41d4-a716-446655440000"
	tenantID := "660e8400-e29b-41d4-a716-446655440000"
	email := "test@example.com"

	tokens, err := jwtService.GenerateTokenPair(userID, tenantID, email, nil)
	if err != nil {
		t.Fatalf("Failed to generate token pair: %v", err)
	}

	// Try to validate refresh token as access token
	_, err = jwtService.ValidateAccessToken(tokens.RefreshToken)
	if err == nil {
		t.Error("Expected error when validating refresh token as access token")
	}

	// Try to validate access token as refresh token
	_, err = jwtService.ValidateRefreshToken(tokens.AccessToken)
	if err == nil {
		t.Error("Expected error when validating access token as refresh token")
	}
}

func TestExtractTokenFromHeader(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		expectToken string
		expectError bool
	}{
		{
			name:        "Valid Bearer token",
			header:      "Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9",
			expectToken: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9",
			expectError: false,
		},
		{
			name:        "Empty header",
			header:      "",
			expectToken: "",
			expectError: true,
		},
		{
			name:        "Missing Bearer prefix",
			header:      "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9",
			expectToken: "",
			expectError: true,
		},
		{
			name:        "Only Bearer",
			header:      "Bearer ",
			expectToken: "",
			expectError: true,
		},
		{
			name:        "Wrong prefix",
			header:      "Basic eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9",
			expectToken: "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := ExtractTokenFromHeader(tt.header)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if token != tt.expectToken {
					t.Errorf("Expected token '%s', got '%s'", tt.expectToken, token)
				}
			}
		})
	}
}

func TestJWTService_TokenExpiry(t *testing.T) {
	// Create JWT service with very short expiry
	privateKeyPath, publicKeyPath := GenerateTestJWTKeys(t)
	defer func() {
		// Clean up temp files
		os.Remove(privateKeyPath)
		os.Remove(publicKeyPath)
	}()

	cfg := &config.JWTConfig{
		PrivateKeyPath:     privateKeyPath,
		PublicKeyPath:      publicKeyPath,
		AccessTokenExpiry:  1 * time.Second, // 1 second expiry
		RefreshTokenExpiry: 2 * time.Second,
		Issuer:             "heimdall-test",
	}

	jwtService, err := NewJWTService(cfg)
	if err != nil {
		t.Fatalf("Failed to create JWT service: %v", err)
	}

	// Generate token
	tokens, err := jwtService.GenerateTokenPair("user-id", "tenant-id", "test@example.com", nil)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Token should be valid immediately
	_, err = jwtService.ValidateAccessToken(tokens.AccessToken)
	if err != nil {
		t.Errorf("Token should be valid immediately: %v", err)
	}

	// Wait for token to expire
	time.Sleep(2 * time.Second)

	// Token should now be expired
	_, err = jwtService.ValidateAccessToken(tokens.AccessToken)
	if err == nil {
		t.Error("Expected error for expired token, got nil")
	}
}
