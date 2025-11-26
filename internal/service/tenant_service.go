package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/models"
	"gorm.io/gorm"
)

// TenantService handles tenant-related business logic
type TenantService struct {
	db               *gorm.DB
	tenantRepository *TenantRepository
}

// NewTenantService creates a new tenant service
func NewTenantService(db *gorm.DB) *TenantService {
	return &TenantService{
		db:               db,
		tenantRepository: NewTenantRepository(db),
	}
}

// CreateTenantRequest represents a tenant creation request
type CreateTenantRequest struct {
	Name     string                 `json:"name" validate:"required,min=2,max=255" example:"Acme Corporation"`
	Slug     string                 `json:"slug" validate:"required,min=2,max=255" example:"acme-corp"`
	Settings map[string]interface{} `json:"settings,omitempty"`
	MaxUsers int                    `json:"maxUsers,omitempty" example:"1000"`
	MaxRoles int                    `json:"maxRoles,omitempty" example:"50"`
}

// UpdateTenantRequest represents a tenant update request
type UpdateTenantRequest struct {
	Name     *string                `json:"name,omitempty" validate:"omitempty,min=2,max=255" example:"Acme Inc."`
	Settings map[string]interface{} `json:"settings,omitempty"`
	MaxUsers *int                   `json:"maxUsers,omitempty" example:"2000"`
	MaxRoles *int                   `json:"maxRoles,omitempty" example:"100"`
}

// TenantResponse represents a tenant response
type TenantResponse struct {
	ID        string                 `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string                 `json:"name" example:"Acme Corporation"`
	Slug      string                 `json:"slug" example:"acme-corp"`
	Settings  map[string]interface{} `json:"settings,omitempty"`
	MaxUsers  int                    `json:"maxUsers" example:"1000"`
	MaxRoles  int                    `json:"maxRoles" example:"50"`
	Status    string                 `json:"status" example:"active"`
	CreatedAt string                 `json:"createdAt" example:"2024-01-15T10:30:00Z"`
	UpdatedAt string                 `json:"updatedAt" example:"2024-01-20T14:45:00Z"`
	Stats     map[string]interface{} `json:"stats,omitempty"`
}

// CreateTenant creates a new tenant
func (s *TenantService) CreateTenant(ctx context.Context, req *CreateTenantRequest) (*TenantResponse, error) {
	// Validate and normalize slug
	slug := normalizeSlug(req.Slug)
	if !isValidSlug(slug) {
		return nil, fmt.Errorf("invalid slug: must contain only lowercase letters, numbers, and hyphens")
	}

	// Check if slug already exists
	exists, err := s.tenantRepository.CheckSlugExists(ctx, slug, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to check slug: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("tenant with slug '%s' already exists", slug)
	}

	// Set defaults
	maxUsers := req.MaxUsers
	if maxUsers == 0 {
		maxUsers = 1000
	}

	maxRoles := req.MaxRoles
	if maxRoles == 0 {
		maxRoles = 50
	}

	// Create tenant
	tenant := &models.Tenant{
		Name:     req.Name,
		Slug:     slug,
		MaxUsers: maxUsers,
		MaxRoles: maxRoles,
		Status:   "active",
	}

	// Marshal settings to JSON
	if req.Settings != nil {
		settingsJSON, err := json.Marshal(req.Settings)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal settings: %w", err)
		}
		tenant.Settings = settingsJSON
	}

	if err := s.tenantRepository.Create(ctx, tenant); err != nil {
		return nil, fmt.Errorf("failed to create tenant: %w", err)
	}

	return s.toTenantResponse(tenant, nil), nil
}

// GetTenant retrieves a tenant by ID
func (s *TenantService) GetTenant(ctx context.Context, tenantID string) (*TenantResponse, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("invalid tenant ID: %w", err)
	}

	tenant, err := s.tenantRepository.GetByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("tenant not found")
		}
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	// Get tenant stats
	stats, _ := s.tenantRepository.GetTenantStats(ctx, id)

	return s.toTenantResponse(tenant, stats), nil
}

// GetTenantBySlug retrieves a tenant by slug
func (s *TenantService) GetTenantBySlug(ctx context.Context, slug string) (*TenantResponse, error) {
	tenant, err := s.tenantRepository.GetBySlug(ctx, slug)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("tenant not found")
		}
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	stats, _ := s.tenantRepository.GetTenantStats(ctx, tenant.ID)

	return s.toTenantResponse(tenant, stats), nil
}

// UpdateTenant updates a tenant
func (s *TenantService) UpdateTenant(ctx context.Context, tenantID string, req *UpdateTenantRequest) (*TenantResponse, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("invalid tenant ID: %w", err)
	}

	tenant, err := s.tenantRepository.GetByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("tenant not found")
		}
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	// Update fields
	if req.Name != nil {
		tenant.Name = *req.Name
	}

	if req.Settings != nil {
		// Parse existing settings
		var existingSettings map[string]interface{}
		if len(tenant.Settings) > 0 {
			if err := json.Unmarshal(tenant.Settings, &existingSettings); err != nil {
				existingSettings = make(map[string]interface{})
			}
		} else {
			existingSettings = make(map[string]interface{})
		}

		// Merge new settings
		for k, v := range req.Settings {
			existingSettings[k] = v
		}

		// Marshal back to JSON
		settingsJSON, err := json.Marshal(existingSettings)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal settings: %w", err)
		}
		tenant.Settings = settingsJSON
	}

	if req.MaxUsers != nil {
		tenant.MaxUsers = *req.MaxUsers
	}

	if req.MaxRoles != nil {
		tenant.MaxRoles = *req.MaxRoles
	}

	if err := s.tenantRepository.Update(ctx, tenant); err != nil {
		return nil, fmt.Errorf("failed to update tenant: %w", err)
	}

	stats, _ := s.tenantRepository.GetTenantStats(ctx, id)

	return s.toTenantResponse(tenant, stats), nil
}

// DeleteTenant deletes a tenant
func (s *TenantService) DeleteTenant(ctx context.Context, tenantID string) error {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("invalid tenant ID: %w", err)
	}

	// Check if tenant exists
	tenant, err := s.tenantRepository.GetByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("tenant not found")
		}
		return fmt.Errorf("failed to get tenant: %w", err)
	}

	// Check if tenant has users
	userCount, err := s.tenantRepository.GetUserCount(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to check user count: %w", err)
	}

	if userCount > 0 {
		return fmt.Errorf("cannot delete tenant with existing users (count: %d)", userCount)
	}

	// Soft delete tenant
	if err := s.tenantRepository.Delete(ctx, id); err != nil {
		return fmt.Errorf("failed to delete tenant: %w", err)
	}

	// Update status to deleted
	_ = s.tenantRepository.UpdateStatus(ctx, tenant.ID, "deleted")

	return nil
}

// ListTenants retrieves a paginated list of tenants
func (s *TenantService) ListTenants(ctx context.Context, page, pageSize int) ([]TenantResponse, int64, error) {
	tenants, total, err := s.tenantRepository.List(ctx, page, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list tenants: %w", err)
	}

	responses := make([]TenantResponse, len(tenants))
	for i, tenant := range tenants {
		responses[i] = *s.toTenantResponse(&tenant, nil)
	}

	return responses, total, nil
}

// SuspendTenant suspends a tenant
func (s *TenantService) SuspendTenant(ctx context.Context, tenantID string) error {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("invalid tenant ID: %w", err)
	}

	return s.tenantRepository.UpdateStatus(ctx, id, "suspended")
}

// ActivateTenant activates a suspended tenant
func (s *TenantService) ActivateTenant(ctx context.Context, tenantID string) error {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("invalid tenant ID: %w", err)
	}

	return s.tenantRepository.UpdateStatus(ctx, id, "active")
}

// GetTenantStats retrieves statistics for a tenant
func (s *TenantService) GetTenantStats(ctx context.Context, tenantID string) (map[string]interface{}, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("invalid tenant ID: %w", err)
	}

	return s.tenantRepository.GetTenantStats(ctx, id)
}

// Helper functions

func (s *TenantService) toTenantResponse(tenant *models.Tenant, stats map[string]interface{}) *TenantResponse {
	// Unmarshal settings from JSON
	var settings map[string]interface{}
	if len(tenant.Settings) > 0 {
		if err := json.Unmarshal(tenant.Settings, &settings); err != nil {
			settings = nil
		}
	}

	return &TenantResponse{
		ID:        tenant.ID.String(),
		Name:      tenant.Name,
		Slug:      tenant.Slug,
		Settings:  settings,
		MaxUsers:  tenant.MaxUsers,
		MaxRoles:  tenant.MaxRoles,
		Status:    tenant.Status,
		CreatedAt: tenant.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: tenant.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Stats:     stats,
	}
}

// normalizeSlug normalizes a slug to lowercase and replaces spaces with hyphens
func normalizeSlug(slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	return slug
}

// isValidSlug checks if a slug is valid (lowercase letters, numbers, hyphens only)
func isValidSlug(slug string) bool {
	validSlug := regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	return validSlug.MatchString(slug)
}
