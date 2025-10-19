package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Permission represents a permission in the system
type Permission struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	Name        string         `gorm:"type:varchar(100);uniqueIndex;not null" json:"name"`
	Resource    string         `gorm:"type:varchar(100);not null" json:"resource"`
	Action      string         `gorm:"type:varchar(50);not null" json:"action"` // create, read, update, delete, etc.
	Description string         `gorm:"type:text" json:"description,omitempty"`

	// Scope for granular control (e.g., "own", "tenant", "global")
	Scope       string         `gorm:"type:varchar(50);default:'tenant'" json:"scope"`

	// System permissions cannot be deleted
	IsSystem    bool           `gorm:"default:false" json:"isSystem"`

	// Timestamps
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"deletedAt,omitempty"`

	// Relationships
	Roles       []Role         `gorm:"many2many:role_permissions;" json:"roles,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (p *Permission) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for Permission
func (Permission) TableName() string {
	return "permissions"
}
