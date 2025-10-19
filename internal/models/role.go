package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Role represents a role within a tenant
type Role struct {
	ID           uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	TenantID     uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenantId"`
	Name         string         `gorm:"type:varchar(100);not null" json:"name"`
	Description  string         `gorm:"type:text" json:"description,omitempty"`

	// Role hierarchy
	ParentRoleID *uuid.UUID     `gorm:"type:uuid" json:"parentRoleId,omitempty"`
	ParentRole   *Role          `gorm:"foreignKey:ParentRoleID" json:"parentRole,omitempty"`

	// System roles cannot be deleted
	IsSystem     bool           `gorm:"default:false" json:"isSystem"`

	// Timestamps
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"deletedAt,omitempty"`

	// Relationships
	Tenant       Tenant         `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
	Permissions  []Permission   `gorm:"many2many:role_permissions;" json:"permissions,omitempty"`
	Users        []User         `gorm:"many2many:user_roles;" json:"users,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (r *Role) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for Role
func (Role) TableName() string {
	return "roles"
}
