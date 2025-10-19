package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Tenant represents a tenant in the multi-tenant system
type Tenant struct {
	ID                uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	Name              string         `gorm:"type:varchar(255);not null" json:"name"`
	Slug              string         `gorm:"type:varchar(255);uniqueIndex;not null" json:"slug"`
	FusionAuthAppID   uuid.UUID      `gorm:"type:uuid" json:"fusionAuthAppId"`
	FusionAuthTenantID uuid.UUID     `gorm:"type:uuid" json:"fusionAuthTenantId"`

	// Configuration stored as JSONB
	Settings          datatypes.JSON `gorm:"type:jsonb" json:"settings,omitempty"`

	// Resource quotas
	MaxUsers          int            `gorm:"default:1000" json:"maxUsers"`
	MaxRoles          int            `gorm:"default:50" json:"maxRoles"`

	// Status
	Status            string         `gorm:"type:varchar(50);default:'active'" json:"status"` // active, suspended, deleted

	// Timestamps
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"deletedAt,omitempty"`

	// Relationships
	Roles             []Role         `gorm:"foreignKey:TenantID" json:"roles,omitempty"`
	AuditLogs         []AuditLog     `gorm:"foreignKey:TenantID" json:"auditLogs,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (t *Tenant) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for Tenant
func (Tenant) TableName() string {
	return "tenants"
}
