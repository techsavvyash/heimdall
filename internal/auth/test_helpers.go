package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/internal/config"
)

// GenerateTestJWTKeys generates RSA keys for testing
func GenerateTestJWTKeys(t *testing.T) (privateKeyPath, publicKeyPath string) {
	t.Helper()

	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	// Create temp files
	privateKeyFile, err := os.CreateTemp("", "test-private-*.pem")
	if err != nil {
		t.Fatalf("Failed to create temp private key file: %v", err)
	}
	defer privateKeyFile.Close()

	publicKeyFile, err := os.CreateTemp("", "test-public-*.pem")
	if err != nil {
		t.Fatalf("Failed to create temp public key file: %v", err)
	}
	defer publicKeyFile.Close()

	// Write private key
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})
	if _, err := privateKeyFile.Write(privateKeyPEM); err != nil {
		t.Fatalf("Failed to write private key: %v", err)
	}

	// Write public key
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal public key: %v", err)
	}
	publicKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	})
	if _, err := publicKeyFile.Write(publicKeyPEM); err != nil {
		t.Fatalf("Failed to write public key: %v", err)
	}

	return privateKeyFile.Name(), publicKeyFile.Name()
}

// CreateTestJWTService creates a JWT service for testing
func CreateTestJWTService(t *testing.T) (*JWTService, func()) {
	t.Helper()

	privateKeyPath, publicKeyPath := GenerateTestJWTKeys(t)

	cfg := &config.JWTConfig{
		PrivateKeyPath:     privateKeyPath,
		PublicKeyPath:      publicKeyPath,
		AccessTokenExpiry:  15 * time.Minute,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
		Issuer:             "heimdall-test",
	}

	jwtService, err := NewJWTService(cfg)
	if err != nil {
		t.Fatalf("Failed to create JWT service: %v", err)
	}

	cleanup := func() {
		os.Remove(privateKeyPath)
		os.Remove(publicKeyPath)
	}

	return jwtService, cleanup
}

// GenerateTestToken generates a test JWT token
func GenerateTestToken(t *testing.T, jwtService *JWTService, userID, tenantID, email string, roles []string) string {
	t.Helper()

	tokens, err := jwtService.GenerateTokenPair(userID, tenantID, email, roles)
	if err != nil {
		t.Fatalf("Failed to generate test token: %v", err)
	}

	return tokens.AccessToken
}
