package api

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/techsavvyash/heimdall/internal/service"
	"github.com/techsavvyash/heimdall/internal/utils"
)

// TenantHandler handles tenant-related endpoints
type TenantHandler struct {
	tenantService *service.TenantService
}

// NewTenantHandler creates a new tenant handler
func NewTenantHandler(tenantService *service.TenantService) *TenantHandler {
	return &TenantHandler{
		tenantService: tenantService,
	}
}

// CreateTenant creates a new tenant
// POST /v1/tenants
func (h *TenantHandler) CreateTenant(c *fiber.Ctx) error {
	var req service.CreateTenantRequest
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

	// Create tenant
	result, err := h.tenantService.CreateTenant(c.Context(), &req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "TENANT_CREATION_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    result,
	})
}

// GetTenant retrieves a tenant by ID
// GET /v1/tenants/:tenantId
func (h *TenantHandler) GetTenant(c *fiber.Ctx) error {
	tenantID := c.Params("tenantId")
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	tenant, err := h.tenantService.GetTenant(c.Context(), tenantID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "TENANT_NOT_FOUND",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    tenant,
	})
}

// GetTenantBySlug retrieves a tenant by slug
// GET /v1/tenants/slug/:slug
func (h *TenantHandler) GetTenantBySlug(c *fiber.Ctx) error {
	slug := c.Params("slug")
	if slug == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Slug is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	tenant, err := h.tenantService.GetTenantBySlug(c.Context(), slug)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "TENANT_NOT_FOUND",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    tenant,
	})
}

// ListTenants retrieves a paginated list of tenants
// GET /v1/tenants?page=1&pageSize=20
func (h *TenantHandler) ListTenants(c *fiber.Ctx) error {
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

	tenants, total, err := h.tenantService.ListTenants(c.Context(), page, pageSize)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to retrieve tenants",
				"code":    "TENANT_LIST_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"tenants": tenants,
			"pagination": fiber.Map{
				"page":       page,
				"pageSize":   pageSize,
				"total":      total,
				"totalPages": (total + int64(pageSize) - 1) / int64(pageSize),
			},
		},
	})
}

// UpdateTenant updates a tenant
// PATCH /v1/tenants/:tenantId
func (h *TenantHandler) UpdateTenant(c *fiber.Ctx) error {
	tenantID := c.Params("tenantId")
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	var req service.UpdateTenantRequest
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

	result, err := h.tenantService.UpdateTenant(c.Context(), tenantID, &req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "TENANT_UPDATE_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    result,
	})
}

// DeleteTenant deletes a tenant
// DELETE /v1/tenants/:tenantId
func (h *TenantHandler) DeleteTenant(c *fiber.Ctx) error {
	tenantID := c.Params("tenantId")
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	if err := h.tenantService.DeleteTenant(c.Context(), tenantID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "TENANT_DELETION_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Tenant deleted successfully",
	})
}

// SuspendTenant suspends a tenant
// POST /v1/tenants/:tenantId/suspend
func (h *TenantHandler) SuspendTenant(c *fiber.Ctx) error {
	tenantID := c.Params("tenantId")
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	if err := h.tenantService.SuspendTenant(c.Context(), tenantID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "TENANT_SUSPENSION_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Tenant suspended successfully",
	})
}

// ActivateTenant activates a suspended tenant
// POST /v1/tenants/:tenantId/activate
func (h *TenantHandler) ActivateTenant(c *fiber.Ctx) error {
	tenantID := c.Params("tenantId")
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	if err := h.tenantService.ActivateTenant(c.Context(), tenantID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": err.Error(),
				"code":    "TENANT_ACTIVATION_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Tenant activated successfully",
	})
}

// GetTenantStats retrieves statistics for a tenant
// GET /v1/tenants/:tenantId/stats
func (h *TenantHandler) GetTenantStats(c *fiber.Ctx) error {
	tenantID := c.Params("tenantId")
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	stats, err := h.tenantService.GetTenantStats(c.Context(), tenantID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to retrieve tenant stats",
				"code":    "STATS_RETRIEVAL_FAILED",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    stats,
	})
}
