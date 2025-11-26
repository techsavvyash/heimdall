package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all application configuration
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Redis    RedisConfig
	JWT      JWTConfig
	Auth     AuthConfig
	SMTP     SMTPConfig
	OPA      OPAConfig
	MinIO    MinIOConfig
}

// ServerConfig holds server-related configuration
type ServerConfig struct {
	Port            string
	Environment     string
	AllowedOrigins  []string
	RateLimitPerMin int
}

// DatabaseConfig holds database connection configuration
type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
	SSLMode  string
	MaxConns int
	MaxIdle  int
}

// RedisConfig holds Redis connection configuration
type RedisConfig struct {
	Host     string
	Port     string
	Password string
	DB       int
}

// JWTConfig holds JWT configuration
type JWTConfig struct {
	PrivateKeyPath     string
	PublicKeyPath      string
	AccessTokenExpiry  time.Duration
	RefreshTokenExpiry time.Duration
	Issuer             string
}

// AuthConfig holds FusionAuth configuration
type AuthConfig struct {
	URL             string
	APIKey          string
	TenantID        string
	ApplicationID   string
	OAuthRedirectURL string
}

// SMTPConfig holds email configuration
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// OPAConfig holds Open Policy Agent configuration
type OPAConfig struct {
	URL         string
	PolicyPath  string
	Timeout     time.Duration
	EnableCache bool
}

// MinIOConfig holds MinIO configuration
type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	// Load .env file if it exists (ignore error if not found)
	_ = godotenv.Load()

	cfg := &Config{
		Server: ServerConfig{
			Port:            getEnv("PORT", "8080"),
			Environment:     getEnv("ENVIRONMENT", "development"),
			AllowedOrigins:  []string{getEnv("ALLOWED_ORIGINS", "*")},
			RateLimitPerMin: getEnvAsInt("RATE_LIMIT_PER_MIN", 100),
		},
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "heimdall"),
			Password: getEnv("DB_PASSWORD", ""),
			Database: getEnv("DB_NAME", "heimdall"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
			MaxConns: getEnvAsInt("DB_MAX_CONNS", 25),
			MaxIdle:  getEnvAsInt("DB_MAX_IDLE", 5),
		},
		Redis: RedisConfig{
			Host:     getEnv("REDIS_HOST", "localhost"),
			Port:     getEnv("REDIS_PORT", "6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvAsInt("REDIS_DB", 0),
		},
		JWT: JWTConfig{
			PrivateKeyPath:     getEnv("JWT_PRIVATE_KEY_PATH", "./keys/private.pem"),
			PublicKeyPath:      getEnv("JWT_PUBLIC_KEY_PATH", "./keys/public.pem"),
			AccessTokenExpiry:  time.Duration(getEnvAsInt("JWT_ACCESS_EXPIRY_MIN", 15)) * time.Minute,
			RefreshTokenExpiry: time.Duration(getEnvAsInt("JWT_REFRESH_EXPIRY_DAYS", 7)) * 24 * time.Hour,
			Issuer:             getEnv("JWT_ISSUER", "heimdall"),
		},
		Auth: AuthConfig{
			URL:              getEnv("FUSIONAUTH_URL", "http://localhost:9011"),
			APIKey:           getEnv("FUSIONAUTH_API_KEY", ""),
			TenantID:         getEnv("FUSIONAUTH_TENANT_ID", ""),
			ApplicationID:    getEnv("FUSIONAUTH_APPLICATION_ID", ""),
			OAuthRedirectURL: getEnv("OAUTH_REDIRECT_URL", "http://localhost:8080/v1/auth/oauth/callback"),
		},
		SMTP: SMTPConfig{
			Host:     getEnv("SMTP_HOST", "localhost"),
			Port:     getEnvAsInt("SMTP_PORT", 587),
			Username: getEnv("SMTP_USERNAME", ""),
			Password: getEnv("SMTP_PASSWORD", ""),
			From:     getEnv("SMTP_FROM", "noreply@heimdall.local"),
		},
		OPA: OPAConfig{
			URL:         getEnv("OPA_URL", "http://localhost:8181"),
			PolicyPath:  getEnv("OPA_POLICY_PATH", "heimdall/authz"),
			Timeout:     time.Duration(getEnvAsInt("OPA_TIMEOUT_SECONDS", 5)) * time.Second,
			EnableCache: getEnv("OPA_ENABLE_CACHE", "true") == "true",
		},
		MinIO: MinIOConfig{
			Endpoint:  getEnv("MINIO_ENDPOINT", "localhost:9000"),
			AccessKey: getEnv("MINIO_ACCESS_KEY", "minioadmin"),
			SecretKey: getEnv("MINIO_SECRET_KEY", "minioadmin"),
			Bucket:    getEnv("MINIO_BUCKET", "bundles"),
			UseSSL:    getEnv("MINIO_USE_SSL", "false") == "true",
		},
	}

	// Validate required configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks if required configuration is present
func (c *Config) Validate() error {
	if c.Server.Environment == "production" {
		if c.Database.Password == "" {
			return fmt.Errorf("DB_PASSWORD is required in production")
		}
		if c.Auth.APIKey == "" {
			return fmt.Errorf("FUSIONAUTH_API_KEY is required")
		}
	}
	return nil
}

// GetDatabaseDSN returns the PostgreSQL connection string
func (c *Config) GetDatabaseDSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.Database.Host,
		c.Database.Port,
		c.Database.User,
		c.Database.Password,
		c.Database.Database,
		c.Database.SSLMode,
	)
}

// GetRedisAddr returns the Redis address
func (c *Config) GetRedisAddr() string {
	return fmt.Sprintf("%s:%s", c.Redis.Host, c.Redis.Port)
}

// Helper functions
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}
