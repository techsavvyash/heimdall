package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/auth"
	"gorm.io/gorm"
)

// UserService handles user-related business logic
type UserService struct {
	db             *gorm.DB
	fusionAuth     *auth.FusionAuthClient
	userRepository *UserRepository
}

// NewUserService creates a new user service
func NewUserService(db *gorm.DB, fusionAuth *auth.FusionAuthClient) *UserService {
	return &UserService{
		db:             db,
		fusionAuth:     fusionAuth,
		userRepository: NewUserRepository(db),
	}
}

// UserProfile represents a user profile
type UserProfile struct {
	ID         string                 `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email      string                 `json:"email" example:"user@example.com"`
	FirstName  string                 `json:"firstName,omitempty" example:"John"`
	LastName   string                 `json:"lastName,omitempty" example:"Doe"`
	TenantID   string                 `json:"tenantId" example:"550e8400-e29b-41d4-a716-446655440000"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Roles      []string               `json:"roles,omitempty" example:"[\"user\",\"admin\"]"`
	LoginCount int                    `json:"loginCount" example:"42"`
	CreatedAt  string                 `json:"createdAt" example:"2024-01-15T10:30:00Z"`
}

// UpdateProfileRequest represents a profile update request
type UpdateProfileRequest struct {
	FirstName *string                `json:"firstName,omitempty" example:"Jane"`
	LastName  *string                `json:"lastName,omitempty" example:"Smith"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// GetUserProfile retrieves the user's profile
func (s *UserService) GetUserProfile(ctx context.Context, userID string) (*UserProfile, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID: %w", err)
	}

	// Get user from database
	user, err := s.userRepository.GetByID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// Get user from FusionAuth for additional details
	faUser, err := s.fusionAuth.GetUser(userID)
	if err != nil {
		// If FusionAuth fails, continue with database data
		faUser = &auth.FusionAuthUser{
			Email: user.Email,
		}
	}

	// Get user roles
	roles, _ := s.userRepository.GetUserRoles(ctx, uid)
	roleNames := make([]string, len(roles))
	for i, role := range roles {
		roleNames[i] = role.Name
	}

	// Unmarshal metadata from JSON
	var metadataMap map[string]interface{}
	var firstName, lastName string
	if len(user.Metadata) > 0 {
		if err := json.Unmarshal(user.Metadata, &metadataMap); err == nil {
			firstName, _ = metadataMap["firstName"].(string)
			lastName, _ = metadataMap["lastName"].(string)
		}
	}

	// Override with FusionAuth data if available
	if faUser.FirstName != "" {
		firstName = faUser.FirstName
	}
	if faUser.LastName != "" {
		lastName = faUser.LastName
	}

	return &UserProfile{
		ID:         user.ID.String(),
		Email:      user.Email,
		FirstName:  firstName,
		LastName:   lastName,
		TenantID:   user.TenantID.String(),
		Metadata:   metadataMap,
		Roles:      roleNames,
		LoginCount: user.LoginCount,
		CreatedAt:  user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}, nil
}

// UpdateUserProfile updates the user's profile
func (s *UserService) UpdateUserProfile(ctx context.Context, userID string, req *UpdateProfileRequest) (*UserProfile, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID: %w", err)
	}

	// Get current user
	user, err := s.userRepository.GetByID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// Unmarshal existing metadata
	var metadataMap map[string]interface{}
	if len(user.Metadata) > 0 {
		if err := json.Unmarshal(user.Metadata, &metadataMap); err != nil {
			metadataMap = make(map[string]interface{})
		}
	} else {
		metadataMap = make(map[string]interface{})
	}

	// Prepare updates for FusionAuth
	faUpdates := make(map[string]interface{})
	if req.FirstName != nil {
		faUpdates["firstName"] = *req.FirstName
		metadataMap["firstName"] = *req.FirstName
	}
	if req.LastName != nil {
		faUpdates["lastName"] = *req.LastName
		metadataMap["lastName"] = *req.LastName
	}

	// Update in FusionAuth if there are changes
	if len(faUpdates) > 0 {
		_, err = s.fusionAuth.UpdateUser(userID, faUpdates)
		if err != nil {
			return nil, fmt.Errorf("failed to update user in FusionAuth: %w", err)
		}
	}

	// Update metadata in database
	if req.Metadata != nil {
		for k, v := range req.Metadata {
			metadataMap[k] = v
		}
	}

	// Marshal metadata back to JSON
	metadataJSON, err := json.Marshal(metadataMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	user.Metadata = metadataJSON

	// Save to database
	if err := s.userRepository.Update(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to update user: %w", err)
	}

	// Return updated profile
	return s.GetUserProfile(ctx, userID)
}

// DeleteUser deletes a user account
func (s *UserService) DeleteUser(ctx context.Context, userID string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	// Delete from FusionAuth
	if err := s.fusionAuth.DeleteUser(userID); err != nil {
		return fmt.Errorf("failed to delete user from FusionAuth: %w", err)
	}

	// Soft delete from database
	if err := s.userRepository.Delete(ctx, uid); err != nil {
		return fmt.Errorf("failed to delete user from database: %w", err)
	}

	return nil
}

// ListUsers retrieves a list of users (admin function)
func (s *UserService) ListUsers(ctx context.Context, tenantID string, page, pageSize int) ([]UserProfile, int64, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid tenant ID: %w", err)
	}

	users, total, err := s.userRepository.ListUsers(ctx, tid, page, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list users: %w", err)
	}

	profiles := make([]UserProfile, len(users))
	for i, user := range users {
		var metadataMap map[string]interface{}
		var firstName, lastName string
		if len(user.Metadata) > 0 {
			if err := json.Unmarshal(user.Metadata, &metadataMap); err == nil {
				firstName, _ = metadataMap["firstName"].(string)
				lastName, _ = metadataMap["lastName"].(string)
			}
		}

		profiles[i] = UserProfile{
			ID:         user.ID.String(),
			Email:      user.Email,
			FirstName:  firstName,
			LastName:   lastName,
			TenantID:   user.TenantID.String(),
			LoginCount: user.LoginCount,
			CreatedAt:  user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
	}

	return profiles, total, nil
}

// GetUserPermissions retrieves all permissions for a user
func (s *UserService) GetUserPermissions(ctx context.Context, userID string) ([]string, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID: %w", err)
	}

	permissions, err := s.userRepository.GetUserPermissions(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("failed to get permissions: %w", err)
	}

	permissionNames := make([]string, len(permissions))
	for i, perm := range permissions {
		permissionNames[i] = perm.Name
	}

	return permissionNames, nil
}

// AssignRoleToUser assigns a role to a user
func (s *UserService) AssignRoleToUser(ctx context.Context, userID, roleID, assignedByID string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	rid, err := uuid.Parse(roleID)
	if err != nil {
		return fmt.Errorf("invalid role ID: %w", err)
	}

	aid, err := uuid.Parse(assignedByID)
	if err != nil {
		return fmt.Errorf("invalid assigned by ID: %w", err)
	}

	return s.userRepository.AssignRole(ctx, uid, rid, aid)
}

// RemoveRoleFromUser removes a role from a user
func (s *UserService) RemoveRoleFromUser(ctx context.Context, userID, roleID string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	rid, err := uuid.Parse(roleID)
	if err != nil {
		return fmt.Errorf("invalid role ID: %w", err)
	}

	return s.userRepository.RemoveRole(ctx, uid, rid)
}
