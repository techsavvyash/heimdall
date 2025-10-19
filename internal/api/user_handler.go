package api

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/middleware"
	"github.com/techsavvyash/heimdall/internal/service"
)

// UserHandler handles user-related endpoints
type UserHandler struct {
	userService *service.UserService
}

// NewUserHandler creates a new user handler
func NewUserHandler(userService *service.UserService) *UserHandler {
	return &UserHandler{
		userService: userService,
	}
}

// GetMe retrieves the current user's profile
// GET /v1/users/me
func (h *UserHandler) GetMe(c *fiber.Ctx) error {
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

	profile, err := h.userService.GetUserProfile(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to retrieve user profile",
				"code":    "PROFILE_RETRIEVAL_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    profile,
	})
}

// UpdateMe updates the current user's profile
// PATCH /v1/users/me
func (h *UserHandler) UpdateMe(c *fiber.Ctx) error {
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

	var req service.UpdateProfileRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid request body",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	profile, err := h.userService.UpdateUserProfile(c.Context(), userID, &req)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to update profile",
				"code":    "PROFILE_UPDATE_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    profile,
	})
}

// DeleteMe deletes the current user's account
// DELETE /v1/users/me
func (h *UserHandler) DeleteMe(c *fiber.Ctx) error {
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

	if err := h.userService.DeleteUser(c.Context(), userID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to delete account",
				"code":    "ACCOUNT_DELETION_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Account deleted successfully",
	})
}

// GetUserByID retrieves a user by ID (admin endpoint)
// GET /v1/users/:userId
func (h *UserHandler) GetUserByID(c *fiber.Ctx) error {
	userID := c.Params("userId")
	if userID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "User ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	profile, err := h.userService.GetUserProfile(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "User not found",
				"code":    "USER_NOT_FOUND",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    profile,
	})
}

// ListUsers retrieves a paginated list of users (admin endpoint)
// GET /v1/users?page=1&pageSize=20
func (h *UserHandler) ListUsers(c *fiber.Ctx) error {
	tenantID := middleware.GetTenantID(c)
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	// Parse pagination params
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize", "20"))

	// Validate pagination
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	users, total, err := h.userService.ListUsers(c.Context(), tenantID, page, pageSize)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to retrieve users",
				"code":    "USER_LIST_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"users": users,
			"pagination": fiber.Map{
				"page":      page,
				"pageSize":  pageSize,
				"total":     total,
				"totalPages": (total + int64(pageSize) - 1) / int64(pageSize),
			},
		},
	})
}

// GetMyPermissions retrieves the current user's permissions
// GET /v1/users/me/permissions
func (h *UserHandler) GetMyPermissions(c *fiber.Ctx) error {
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

	permissions, err := h.userService.GetUserPermissions(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to retrieve permissions",
				"code":    "PERMISSIONS_RETRIEVAL_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"permissions": permissions,
		},
	})
}

// AssignRole assigns a role to a user (admin endpoint)
// POST /v1/users/:userId/roles
func (h *UserHandler) AssignRole(c *fiber.Ctx) error {
	userID := c.Params("userId")
	if userID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "User ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	var req struct {
		RoleID string `json:"roleId" validate:"required"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid request body",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	assignedByID := middleware.GetUserID(c)
	if err := h.userService.AssignRoleToUser(c.Context(), userID, req.RoleID, assignedByID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to assign role",
				"code":    "ROLE_ASSIGNMENT_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Role assigned successfully",
	})
}

// RemoveRole removes a role from a user (admin endpoint)
// DELETE /v1/users/:userId/roles/:roleId
func (h *UserHandler) RemoveRole(c *fiber.Ctx) error {
	userID := c.Params("userId")
	roleID := c.Params("roleId")

	if userID == "" || roleID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "User ID and Role ID are required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	if err := h.userService.RemoveRoleFromUser(c.Context(), userID, roleID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to remove role",
				"code":    "ROLE_REMOVAL_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Role removed successfully",
	})
}
