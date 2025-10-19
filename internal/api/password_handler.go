package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/middleware"
	"github.com/techsavvyash/heimdall/internal/service"
	"github.com/techsavvyash/heimdall/internal/utils"
)

// PasswordHandler handles password-related endpoints
type PasswordHandler struct {
	passwordService *service.PasswordService
}

// NewPasswordHandler creates a new password handler
func NewPasswordHandler(passwordService *service.PasswordService) *PasswordHandler {
	return &PasswordHandler{
		passwordService: passwordService,
	}
}

// ChangePassword handles password change for authenticated users
// POST /v1/auth/password/change
func (h *PasswordHandler) ChangePassword(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "User not authenticated",
				"code":    "UNAUTHORIZED",
			},
		})
	}

	var req service.ChangePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid request body",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	// Validate request
	if err := utils.ValidateStruct(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Validation failed",
				"code":    "VALIDATION_ERROR",
				"details": err,
			},
		})
	}

	// Change password
	if err := h.passwordService.ChangePassword(c.Context(), userID, &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "PASSWORD_CHANGE_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Password changed successfully",
	})
}
