package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/auth"
	"github.com/techsavvyash/heimdall/internal/database"
	"github.com/techsavvyash/heimdall/internal/models"
	"gorm.io/gorm"
)

// AuthService handles authentication business logic
type AuthService struct {
	db             *gorm.DB
	fusionAuth     *auth.FusionAuthClient
	jwtService     *auth.JWTService
	redis          *database.RedisClient
	userRepository *UserRepository
}

// NewAuthService creates a new auth service
func NewAuthService(
	db *gorm.DB,
	fusionAuth *auth.FusionAuthClient,
	jwtService *auth.JWTService,
	redis *database.RedisClient,
) *AuthService {
	return &AuthService{
		db:             db,
		fusionAuth:     fusionAuth,
		jwtService:     jwtService,
		redis:          redis,
		userRepository: NewUserRepository(db),
	}
}

// RegisterRequest represents registration data
type RegisterRequest struct {
	Email     string `json:"email" validate:"required,email" example:"user@example.com"`
	Password  string `json:"password" validate:"required,min=8" example:"SecurePassword123!"`
	FirstName string `json:"firstName" validate:"required" example:"John"`
	LastName  string `json:"lastName" validate:"required" example:"Doe"`
	TenantID  string `json:"tenantId,omitempty" example:"550e8400-e29b-41d4-a716-446655440000"`
}

// LoginRequest represents login credentials
type LoginRequest struct {
	Email      string `json:"email" validate:"required,email" example:"user@example.com"`
	Password   string `json:"password" validate:"required" example:"SecurePassword123!"`
	RememberMe bool   `json:"rememberMe" example:"false"`
}

// AuthResponse represents authentication response
type AuthResponse struct {
	AccessToken  string    `json:"accessToken" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	RefreshToken string    `json:"refreshToken" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	TokenType    string    `json:"tokenType" example:"Bearer"`
	ExpiresIn    int64     `json:"expiresIn" example:"900"`
	User         *UserInfo `json:"user"`
}

// UserInfo represents basic user information
type UserInfo struct {
	ID        string `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email     string `json:"email" example:"user@example.com"`
	FirstName string `json:"firstName,omitempty" example:"John"`
	LastName  string `json:"lastName,omitempty" example:"Doe"`
	TenantID  string `json:"tenantId" example:"550e8400-e29b-41d4-a716-446655440000"`
}

// Register creates a new user account
func (s *AuthService) Register(ctx context.Context, req *RegisterRequest) (*AuthResponse, error) {
	// Create user in FusionAuth
	faUser, err := s.fusionAuth.Register(&auth.RegisterRequest{
		Email:     req.Email,
		Password:  req.Password,
		FirstName: req.FirstName,
		LastName:  req.LastName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create user in FusionAuth: %w", err)
	}

	// Parse tenant ID or get default tenant
	tenantID := req.TenantID
	var tenantUUID uuid.UUID

	if tenantID == "" {
		// Get default tenant
		var defaultTenant models.Tenant
		if err := s.db.Where("slug = ?", "default").First(&defaultTenant).Error; err != nil {
			return nil, fmt.Errorf("no tenant ID provided and default tenant not found: %w", err)
		}
		tenantUUID = defaultTenant.ID
		tenantID = defaultTenant.ID.String()
	} else {
		// Parse provided tenant ID
		var err error
		tenantUUID, err = uuid.Parse(tenantID)
		if err != nil {
			return nil, fmt.Errorf("invalid tenant ID: %w", err)
		}

		// Verify tenant exists and is active
		var tenant models.Tenant
		if err := s.db.Where("id = ? AND status = ?", tenantUUID, "active").First(&tenant).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, fmt.Errorf("tenant not found or inactive")
			}
			return nil, fmt.Errorf("failed to verify tenant: %w", err)
		}
	}

	userUUID, err := uuid.Parse(faUser.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID from FusionAuth: %w", err)
	}

	// Create user record in Heimdall database
	metadataMap := map[string]interface{}{
		"firstName": faUser.FirstName,
		"lastName":  faUser.LastName,
	}
	metadataJSON, err := json.Marshal(metadataMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	user := &models.User{
		ID:       userUUID,
		TenantID: tenantUUID,
		Email:    faUser.Email,
		Metadata: metadataJSON,
	}

	if err := s.userRepository.Create(ctx, user); err != nil {
		// Rollback: delete user from FusionAuth
		_ = s.fusionAuth.DeleteUser(faUser.ID)
		return nil, fmt.Errorf("failed to create user record: %w", err)
	}

	// Generate tokens
	tokens, err := s.jwtService.GenerateTokenPair(faUser.ID, tenantID, faUser.Email, []string{})
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// Store refresh token in Redis
	if s.redis != nil {
		tokenClaims, _ := s.jwtService.ValidateRefreshToken(tokens.RefreshToken)
		if tokenClaims != nil {
			_ = s.redis.StoreRefreshToken(ctx, faUser.ID, tokenClaims.ID, time.Duration(tokens.ExpiresIn)*time.Second)
		}
	}

	return &AuthResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    tokens.TokenType,
		ExpiresIn:    tokens.ExpiresIn,
		User: &UserInfo{
			ID:        faUser.ID,
			Email:     faUser.Email,
			FirstName: faUser.FirstName,
			LastName:  faUser.LastName,
			TenantID:  tenantID,
		},
	}, nil
}

// Login authenticates a user and returns tokens
func (s *AuthService) Login(ctx context.Context, req *LoginRequest) (*AuthResponse, error) {
	// Authenticate with FusionAuth
	faUser, err := s.fusionAuth.Login(&auth.LoginRequest{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	// Get user from database
	userUUID, _ := uuid.Parse(faUser.ID)
	user, err := s.userRepository.GetByID(ctx, userUUID)
	if err != nil {
		return nil, fmt.Errorf("user not found in database: %w", err)
	}

	// Update last login time
	now := time.Now()
	user.LastLoginAt = &now
	user.LoginCount++
	_ = s.userRepository.Update(ctx, user)

	// Get user roles
	roles, _ := s.userRepository.GetUserRoles(ctx, userUUID)
	roleNames := make([]string, len(roles))
	for i, role := range roles {
		roleNames[i] = role.Name
	}

	// Generate tokens
	tokens, err := s.jwtService.GenerateTokenPair(faUser.ID, user.TenantID.String(), faUser.Email, roleNames)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// Store refresh token in Redis
	if s.redis != nil {
		tokenClaims, _ := s.jwtService.ValidateRefreshToken(tokens.RefreshToken)
		if tokenClaims != nil {
			expiry := time.Duration(tokens.ExpiresIn) * time.Second
			if req.RememberMe {
				expiry = 30 * 24 * time.Hour // 30 days
			}
			_ = s.redis.StoreRefreshToken(ctx, faUser.ID, tokenClaims.ID, expiry)
		}
	}

	// Get user metadata
	var metadataMap map[string]interface{}
	var firstName, lastName string
	if len(user.Metadata) > 0 {
		if err := json.Unmarshal(user.Metadata, &metadataMap); err == nil {
			firstName, _ = metadataMap["firstName"].(string)
			lastName, _ = metadataMap["lastName"].(string)
		}
	}

	return &AuthResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    tokens.TokenType,
		ExpiresIn:    tokens.ExpiresIn,
		User: &UserInfo{
			ID:        faUser.ID,
			Email:     faUser.Email,
			FirstName: firstName,
			LastName:  lastName,
			TenantID:  user.TenantID.String(),
		},
	}, nil
}

// RefreshToken generates a new access token from a refresh token
func (s *AuthService) RefreshToken(ctx context.Context, refreshToken string) (*AuthResponse, error) {
	// Validate refresh token
	claims, err := s.jwtService.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}

	// Check if refresh token is valid in Redis
	if s.redis != nil {
		valid, err := s.redis.ValidateRefreshToken(ctx, claims.UserID, claims.ID)
		if err != nil || !valid {
			return nil, fmt.Errorf("refresh token not found or expired")
		}
	}

	// Get user from database
	userUUID, _ := uuid.Parse(claims.UserID)
	user, err := s.userRepository.GetByID(ctx, userUUID)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// Get user roles
	roles, _ := s.userRepository.GetUserRoles(ctx, userUUID)
	roleNames := make([]string, len(roles))
	for i, role := range roles {
		roleNames[i] = role.Name
	}

	// Generate new token pair
	tokens, err := s.jwtService.GenerateTokenPair(claims.UserID, claims.TenantID, claims.Email, roleNames)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// Revoke old refresh token and store new one
	if s.redis != nil {
		_ = s.redis.RevokeRefreshToken(ctx, claims.UserID, claims.ID)
		newClaims, _ := s.jwtService.ValidateRefreshToken(tokens.RefreshToken)
		if newClaims != nil {
			_ = s.redis.StoreRefreshToken(ctx, claims.UserID, newClaims.ID, time.Duration(tokens.ExpiresIn)*time.Second)
		}
	}

	// Get user metadata
	var metadataMap map[string]interface{}
	var firstName, lastName string
	if len(user.Metadata) > 0 {
		if err := json.Unmarshal(user.Metadata, &metadataMap); err == nil {
			firstName, _ = metadataMap["firstName"].(string)
			lastName, _ = metadataMap["lastName"].(string)
		}
	}

	return &AuthResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    tokens.TokenType,
		ExpiresIn:    tokens.ExpiresIn,
		User: &UserInfo{
			ID:        claims.UserID,
			Email:     claims.Email,
			FirstName: firstName,
			LastName:  lastName,
			TenantID:  claims.TenantID,
		},
	}, nil
}

// Logout revokes tokens for a user
func (s *AuthService) Logout(ctx context.Context, userID, tokenID string) error {
	// Blacklist the access token
	if s.redis != nil {
		// Blacklist for the remaining lifetime of the token
		_ = s.redis.BlacklistToken(ctx, tokenID, 15*time.Minute)

		// Revoke all refresh tokens for the user
		_ = s.redis.RevokeAllUserTokens(ctx, userID)
	}

	return nil
}

// LogoutEverywhere revokes all sessions for a user
func (s *AuthService) LogoutEverywhere(ctx context.Context, userID string) error {
	if s.redis != nil {
		return s.redis.RevokeAllUserTokens(ctx, userID)
	}
	return nil
}
