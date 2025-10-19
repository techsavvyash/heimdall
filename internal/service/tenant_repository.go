package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/models"
	"gorm.io/gorm"
)

// TenantRepository handles tenant data access
type TenantRepository struct {
	db *gorm.DB
}

// NewTenantRepository creates a new tenant repository
func NewTenantRepository(db *gorm.DB) *TenantRepository {
	return &TenantRepository{db: db}
}

// Create creates a new tenant
func (r *TenantRepository) Create(ctx context.Context, tenant *models.Tenant) error {
	return r.db.WithContext(ctx).Create(tenant).Error
}

// GetByID retrieves a tenant by ID
func (r *TenantRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Tenant, error) {
	var tenant models.Tenant
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&tenant).Error
	if err != nil {
		return nil, err
	}
	return &tenant, nil
}

// GetBySlug retrieves a tenant by slug
func (r *TenantRepository) GetBySlug(ctx context.Context, slug string) (*models.Tenant, error) {
	var tenant models.Tenant
	err := r.db.WithContext(ctx).Where("slug = ?", slug).First(&tenant).Error
	if err != nil {
		return nil, err
	}
	return &tenant, nil
}

// Update updates a tenant
func (r *TenantRepository) Update(ctx context.Context, tenant *models.Tenant) error {
	return r.db.WithContext(ctx).Save(tenant).Error
}

// Delete soft deletes a tenant
func (r *TenantRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Tenant{}, id).Error
}

// List retrieves a paginated list of tenants
func (r *TenantRepository) List(ctx context.Context, page, pageSize int) ([]models.Tenant, int64, error) {
	var tenants []models.Tenant
	var total int64

	// Count total
	if err := r.db.WithContext(ctx).Model(&models.Tenant{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Get paginated results
	offset := (page - 1) * pageSize
	err := r.db.WithContext(ctx).
		Offset(offset).
		Limit(pageSize).
		Order("created_at DESC").
		Find(&tenants).Error

	if err != nil {
		return nil, 0, err
	}

	return tenants, total, nil
}

// UpdateStatus updates a tenant's status
func (r *TenantRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	return r.db.WithContext(ctx).
		Model(&models.Tenant{}).
		Where("id = ?", id).
		Update("status", status).Error
}

// UpdateSettings updates tenant settings
func (r *TenantRepository) UpdateSettings(ctx context.Context, id uuid.UUID, settings map[string]interface{}) error {
	return r.db.WithContext(ctx).
		Model(&models.Tenant{}).
		Where("id = ?", id).
		Update("settings", settings).Error
}

// GetUserCount returns the number of users in a tenant
func (r *TenantRepository) GetUserCount(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.User{}).
		Where("tenant_id = ?", tenantID).
		Count(&count).Error
	return count, err
}

// GetRoleCount returns the number of roles in a tenant
func (r *TenantRepository) GetRoleCount(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.Role{}).
		Where("tenant_id = ?", tenantID).
		Count(&count).Error
	return count, err
}

// CheckSlugExists checks if a slug already exists
func (r *TenantRepository) CheckSlugExists(ctx context.Context, slug string, excludeID *uuid.UUID) (bool, error) {
	var count int64
	query := r.db.WithContext(ctx).Model(&models.Tenant{}).Where("slug = ?", slug)

	if excludeID != nil {
		query = query.Where("id != ?", *excludeID)
	}

	err := query.Count(&count).Error
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// BulkCreate creates multiple tenants
func (r *TenantRepository) BulkCreate(ctx context.Context, tenants []models.Tenant) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range tenants {
			if err := tx.Create(&tenants[i]).Error; err != nil {
				return fmt.Errorf("failed to create tenant %s: %w", tenants[i].Name, err)
			}
		}
		return nil
	})
}

// GetTenantStats retrieves statistics for a tenant
func (r *TenantRepository) GetTenantStats(ctx context.Context, tenantID uuid.UUID) (map[string]interface{}, error) {
	userCount, err := r.GetUserCount(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	roleCount, err := r.GetRoleCount(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"userCount": userCount,
		"roleCount": roleCount,
	}, nil
}
