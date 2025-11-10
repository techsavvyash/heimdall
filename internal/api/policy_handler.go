package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/middleware"
	"github.com/techsavvyash/heimdall/internal/models"
	"github.com/techsavvyash/heimdall/internal/service"
)

// PolicyHandler handles policy-related endpoints
type PolicyHandler struct {
	policyService *service.PolicyService
	bundleService *service.BundleService
}

// NewPolicyHandler creates a new policy handler
func NewPolicyHandler(policyService *service.PolicyService, bundleService *service.BundleService) *PolicyHandler {
	return &PolicyHandler{
		policyService: policyService,
		bundleService: bundleService,
	}
}

// CreatePolicy creates a new policy
// POST /v1/policies
func (h *PolicyHandler) CreatePolicy(c *fiber.Ctx) error {
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

	var req service.CreatePolicyRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid request body",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid user ID",
				"code":    "INVALID_USER_ID",
			},
		})
	}

	policy, err := h.policyService.CreatePolicy(c.Context(), userUUID, &req)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to create policy",
				"code":    "POLICY_CREATION_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    policy,
	})
}

// ListPolicies lists all policies for a tenant
// GET /v1/policies
func (h *PolicyHandler) ListPolicies(c *fiber.Ctx) error {
	tenantID := middleware.GetTenantID(c)
	if tenantID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Tenant ID is required",
				"code":    "TENANT_REQUIRED",
			},
		})
	}

	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid tenant ID",
				"code":    "INVALID_TENANT_ID",
			},
		})
	}

	// Get status filter from query params
	var status *models.PolicyStatus
	if statusStr := c.Query("status"); statusStr != "" {
		s := models.PolicyStatus(statusStr)
		status = &s
	}

	policies, err := h.policyService.GetPoliciesByTenant(c.Context(), tenantUUID, status)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to list policies",
				"code":    "POLICY_LIST_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    policies,
		"count":   len(policies),
	})
}

// GetPolicy retrieves a specific policy
// GET /v1/policies/:id
func (h *PolicyHandler) GetPolicy(c *fiber.Ctx) error {
	policyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid policy ID",
				"code":    "INVALID_POLICY_ID",
			},
		})
	}

	policy, err := h.policyService.GetPolicy(c.Context(), policyID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Policy not found",
				"code":    "POLICY_NOT_FOUND",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    policy,
	})
}

// UpdatePolicy updates a policy
// PUT /v1/policies/:id
func (h *PolicyHandler) UpdatePolicy(c *fiber.Ctx) error {
	policyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid policy ID",
				"code":    "INVALID_POLICY_ID",
			},
		})
	}

	userID := middleware.GetUserID(c)
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid user ID",
				"code":    "INVALID_USER_ID",
			},
		})
	}

	var req service.UpdatePolicyRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid request body",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	policy, err := h.policyService.UpdatePolicy(c.Context(), policyID, userUUID, &req)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to update policy",
				"code":    "POLICY_UPDATE_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    policy,
	})
}

// DeletePolicy deletes a policy
// DELETE /v1/policies/:id
func (h *PolicyHandler) DeletePolicy(c *fiber.Ctx) error {
	policyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid policy ID",
				"code":    "INVALID_POLICY_ID",
			},
		})
	}

	if err := h.policyService.DeletePolicy(c.Context(), policyID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to delete policy",
				"code":    "POLICY_DELETE_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Policy deleted successfully",
	})
}

// PublishPolicy publishes a policy
// POST /v1/policies/:id/publish
func (h *PolicyHandler) PublishPolicy(c *fiber.Ctx) error {
	policyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid policy ID",
				"code":    "INVALID_POLICY_ID",
			},
		})
	}

	userID := middleware.GetUserID(c)
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid user ID",
				"code":    "INVALID_USER_ID",
			},
		})
	}

	policy, err := h.policyService.PublishPolicy(c.Context(), policyID, userUUID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to publish policy",
				"code":    "POLICY_PUBLISH_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    policy,
	})
}

// ValidatePolicy validates a policy
// POST /v1/policies/:id/validate
func (h *PolicyHandler) ValidatePolicy(c *fiber.Ctx) error {
	policyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid policy ID",
				"code":    "INVALID_POLICY_ID",
			},
		})
	}

	if err := h.policyService.ValidatePolicy(c.Context(), policyID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Policy validation failed",
				"code":    "POLICY_VALIDATION_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Policy is valid",
	})
}

// TestPolicy tests a policy
// POST /v1/policies/:id/test
func (h *PolicyHandler) TestPolicy(c *fiber.Ctx) error {
	policyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid policy ID",
				"code":    "INVALID_POLICY_ID",
			},
		})
	}

	results, err := h.policyService.TestPolicy(c.Context(), policyID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Policy test failed",
				"code":    "POLICY_TEST_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    results,
	})
}

// GetPolicyVersions retrieves all versions of a policy
// GET /v1/policies/:id/versions
func (h *PolicyHandler) GetPolicyVersions(c *fiber.Ctx) error {
	policyID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid policy ID",
				"code":    "INVALID_POLICY_ID",
			},
		})
	}

	versions, err := h.policyService.GetPolicyVersions(c.Context(), policyID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to get policy versions",
				"code":    "POLICY_VERSIONS_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    versions,
		"count":   len(versions),
	})
}

// --- Bundle Endpoints ---

// CreateBundle creates a new policy bundle
// POST /v1/bundles
func (h *PolicyHandler) CreateBundle(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid user ID",
				"code":    "INVALID_USER_ID",
			},
		})
	}

	var req service.CreateBundleRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid request body",
				"code":    "INVALID_REQUEST",
			},
		})
	}

	bundle, err := h.bundleService.CreateBundle(c.Context(), userUUID, &req)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to create bundle",
				"code":    "BUNDLE_CREATION_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    bundle,
		"message": "Bundle is being built",
	})
}

// ListBundles lists all bundles
// GET /v1/bundles
func (h *PolicyHandler) ListBundles(c *fiber.Ctx) error {
	tenantID := middleware.GetTenantID(c)
	var tenantUUID *uuid.UUID
	if tenantID != "" {
		parsed, err := uuid.Parse(tenantID)
		if err == nil {
			tenantUUID = &parsed
		}
	}

	bundles, err := h.bundleService.GetBundles(c.Context(), tenantUUID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to list bundles",
				"code":    "BUNDLE_LIST_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    bundles,
		"count":   len(bundles),
	})
}

// GetBundle retrieves a specific bundle
// GET /v1/bundles/:id
func (h *PolicyHandler) GetBundle(c *fiber.Ctx) error {
	bundleID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid bundle ID",
				"code":    "INVALID_BUNDLE_ID",
			},
		})
	}

	bundle, err := h.bundleService.GetBundle(c.Context(), bundleID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Bundle not found",
				"code":    "BUNDLE_NOT_FOUND",
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    bundle,
	})
}

// ActivateBundle activates a bundle
// POST /v1/bundles/:id/activate
func (h *PolicyHandler) ActivateBundle(c *fiber.Ctx) error {
	bundleID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid bundle ID",
				"code":    "INVALID_BUNDLE_ID",
			},
		})
	}

	userID := middleware.GetUserID(c)
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid user ID",
				"code":    "INVALID_USER_ID",
			},
		})
	}

	bundle, err := h.bundleService.ActivateBundle(c.Context(), bundleID, userUUID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to activate bundle",
				"code":    "BUNDLE_ACTIVATION_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    bundle,
	})
}

// DeployBundle deploys a bundle
// POST /v1/bundles/:id/deploy
func (h *PolicyHandler) DeployBundle(c *fiber.Ctx) error {
	bundleID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid bundle ID",
				"code":    "INVALID_BUNDLE_ID",
			},
		})
	}

	userID := middleware.GetUserID(c)
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid user ID",
				"code":    "INVALID_USER_ID",
			},
		})
	}

	var req struct {
		Environment string `json:"environment"`
	}
	if err := c.BodyParser(&req); err != nil {
		req.Environment = "production"
	}

	deployment, err := h.bundleService.DeployBundle(c.Context(), bundleID, userUUID, req.Environment)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to deploy bundle",
				"code":    "BUNDLE_DEPLOY_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    deployment,
	})
}

// DeleteBundle deletes a bundle
// DELETE /v1/bundles/:id
func (h *PolicyHandler) DeleteBundle(c *fiber.Ctx) error {
	bundleID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Invalid bundle ID",
				"code":    "INVALID_BUNDLE_ID",
			},
		})
	}

	if err := h.bundleService.DeleteBundle(c.Context(), bundleID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"success": false,
			"error": fiber.Map{
				"message": "Failed to delete bundle",
				"code":    "BUNDLE_DELETE_FAILED",
				"details": err.Error(),
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "Bundle deleted successfully",
	})
}
