package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuditLog represents an audit log entry
type AuditLog struct {
	ID         uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	TenantID   uuid.UUID      `gorm:"type:uuid;not null;index" json:"tenantId"`
	UserID     *uuid.UUID     `gorm:"type:uuid;index" json:"userId,omitempty"`

	// Event information
	EventType  string         `gorm:"type:varchar(100);not null;index" json:"eventType"` // login, logout, user.create, etc.
	Action     string         `gorm:"type:varchar(100);not null" json:"action"`
	Resource   string         `gorm:"type:varchar(100)" json:"resource,omitempty"`
	ResourceID *uuid.UUID     `gorm:"type:uuid" json:"resourceId,omitempty"`

	// Request context
	IPAddress  string         `gorm:"type:varchar(45)" json:"ipAddress,omitempty"`
	UserAgent  string         `gorm:"type:text" json:"userAgent,omitempty"`
	Method     string         `gorm:"type:varchar(10)" json:"method,omitempty"` // GET, POST, etc.
	Path       string         `gorm:"type:varchar(500)" json:"path,omitempty"`

	// Result
	Status     string         `gorm:"type:varchar(50);not null" json:"status"` // success, failure, error
	StatusCode int            `gorm:"type:integer" json:"statusCode,omitempty"`
	Message    string         `gorm:"type:text" json:"message,omitempty"`

	// Additional data
	Metadata   map[string]interface{} `gorm:"type:jsonb" json:"metadata,omitempty"`

	// Duration in milliseconds
	Duration   int64          `json:"duration,omitempty"`

	// Timestamp
	CreatedAt  time.Time      `gorm:"index" json:"createdAt"`

	// Relationships
	Tenant     Tenant         `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
	User       *User          `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for AuditLog
func (AuditLog) TableName() string {
	return "audit_logs"
}
