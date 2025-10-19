package service

import (
	"context"
	"fmt"

	"github.com/techsavvyash/heimdall/internal/auth"
)

// PasswordService handles password-related operations
type PasswordService struct {
	fusionAuth *auth.FusionAuthClient
}

// NewPasswordService creates a new password service
func NewPasswordService(fusionAuth *auth.FusionAuthClient) *PasswordService {
	return &PasswordService{
		fusionAuth: fusionAuth,
	}
}

// ChangePasswordRequest represents a password change request
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword" validate:"required"`
	NewPassword     string `json:"newPassword" validate:"required,min=8"`
	ConfirmPassword string `json:"confirmPassword" validate:"required"`
}

// ChangePassword changes a user's password
func (s *PasswordService) ChangePassword(ctx context.Context, userID string, req *ChangePasswordRequest) error {
	// Validate that new password and confirm password match
	if req.NewPassword != req.ConfirmPassword {
		return fmt.Errorf("new password and confirm password do not match")
	}

	// Validate that new password is different from current password
	if req.CurrentPassword == req.NewPassword {
		return fmt.Errorf("new password must be different from current password")
	}

	// Change password in FusionAuth
	if err := s.fusionAuth.ChangePassword(userID, req.CurrentPassword, req.NewPassword); err != nil {
		return fmt.Errorf("failed to change password: %w", err)
	}

	return nil
}
