package testutil

import (
	"testing"

	"github.com/techsavvyash/heimdall/internal/auth"
)

// CreateTestJWTService creates a JWT service for testing
// This is a wrapper around auth.CreateTestJWTService to avoid import cycles
func CreateTestJWTService(t *testing.T) (*auth.JWTService, func()) {
	return auth.CreateTestJWTService(t)
}

// GenerateTestToken generates a test JWT token
// This is a wrapper around auth.GenerateTestToken to avoid import cycles
func GenerateTestToken(t *testing.T, jwtService *auth.JWTService, userID, tenantID, email string, roles []string) string {
	return auth.GenerateTestToken(t, jwtService, userID, tenantID, email, roles)
}
