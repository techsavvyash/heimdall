package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// User represents additional user data beyond FusionAuth
// Core auth data is stored in FusionAuth, this stores RBAC and custom attributes
type User struct {
	ID                uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"` // Same as FusionAuth user ID
	TenantID          uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenantId"`
	Email             string         `gorm:"type:varchar(255);not null;index" json:"email"`

	// Additional metadata
	Metadata          datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`

	// Account status tracking
	LastLoginAt       *time.Time     `json:"lastLoginAt,omitempty"`
	LoginCount        int            `gorm:"default:0" json:"loginCount"`

	// Timestamps
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"deletedAt,omitempty"`

	// Relationships
	Tenant            Tenant         `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
	Roles             []Role         `gorm:"many2many:user_roles;" json:"roles,omitempty"`
	AuditLogs         []AuditLog     `gorm:"foreignKey:UserID" json:"auditLogs,omitempty"`
}

// TableName specifies the table name for User
func (User) TableName() string {
	return "users"
}

// UserRole represents the many-to-many relationship between users and roles
type UserRole struct {
	ID         uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	UserID     uuid.UUID      `gorm:"type:uuid;not null;index" json:"userId"`
	RoleID     uuid.UUID      `gorm:"type:uuid;not null;index" json:"roleId"`
	AssignedBy uuid.UUID      `gorm:"type:uuid" json:"assignedBy"`
	AssignedAt time.Time      `gorm:"default:now()" json:"assignedAt"`
	ExpiresAt  *time.Time     `json:"expiresAt,omitempty"`

	// Relationships
	User       User           `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Role       Role           `gorm:"foreignKey:RoleID" json:"role,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (ur *UserRole) BeforeCreate(tx *gorm.DB) error {
	if ur.ID == uuid.Nil {
		ur.ID = uuid.New()
	}
	if ur.AssignedAt.IsZero() {
		ur.AssignedAt = time.Now()
	}
	return nil
}

// TableName specifies the table name for UserRole
func (UserRole) TableName() string {
	return "user_roles"
}

// RolePermission represents the many-to-many relationship between roles and permissions
type RolePermission struct {
	ID           uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	RoleID       uuid.UUID      `gorm:"type:uuid;not null;index" json:"roleId"`
	PermissionID uuid.UUID      `gorm:"type:uuid;not null;index" json:"permissionId"`
	GrantedBy    uuid.UUID      `gorm:"type:uuid" json:"grantedBy"`
	GrantedAt    time.Time      `gorm:"default:now()" json:"grantedAt"`

	// Relationships
	Role         Role           `gorm:"foreignKey:RoleID" json:"role,omitempty"`
	Permission   Permission     `gorm:"foreignKey:PermissionID" json:"permission,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (rp *RolePermission) BeforeCreate(tx *gorm.DB) error {
	if rp.ID == uuid.Nil {
		rp.ID = uuid.New()
	}
	if rp.GrantedAt.IsZero() {
		rp.GrantedAt = time.Now()
	}
	return nil
}

// TableName specifies the table name for RolePermission
func (RolePermission) TableName() string {
	return "role_permissions"
}
