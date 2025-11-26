package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PolicyType defines the type of policy
type PolicyType string

const (
	PolicyTypeRego    PolicyType = "rego"     // Rego policy code
	PolicyTypeJSON    PolicyType = "json"     // JSON data policy
	PolicyTypeWasm    PolicyType = "wasm"     // WebAssembly policy
)

// PolicyStatus defines the status of a policy
type PolicyStatus string

const (
	PolicyStatusDraft     PolicyStatus = "draft"      // Policy is being edited
	PolicyStatusActive    PolicyStatus = "active"     // Policy is active and in use
	PolicyStatusInactive  PolicyStatus = "inactive"   // Policy is inactive
	PolicyStatusArchived  PolicyStatus = "archived"   // Policy is archived
)

// Policy represents a policy document in the system
type Policy struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	TenantID    uuid.UUID      `gorm:"type:uuid;index;not null" json:"tenantId"`

	// Policy metadata
	Name        string         `gorm:"type:varchar(200);not null" json:"name"`
	Description string         `gorm:"type:text" json:"description,omitempty"`
	Version     int            `gorm:"default:1;not null" json:"version"`
	Path        string         `gorm:"type:varchar(500);uniqueIndex;not null" json:"path"` // Policy path in OPA (e.g., "heimdall/authz/users")

	// Policy content
	Type        PolicyType     `gorm:"type:varchar(50);default:'rego';not null" json:"type"`
	Content     string         `gorm:"type:text;not null" json:"content"` // Rego code or policy content

	// Status and metadata
	Status      PolicyStatus   `gorm:"type:varchar(50);default:'draft';not null" json:"status"`
	IsSystem    bool           `gorm:"default:false" json:"isSystem"` // System policies cannot be deleted

	// Validation
	IsValid     bool           `gorm:"default:false" json:"isValid"`
	ValidationError string      `gorm:"type:text" json:"validationError,omitempty"`
	ValidatedAt *time.Time     `json:"validatedAt,omitempty"`

	// Test cases (stored as JSON)
	TestCases   datatypes.JSON `gorm:"type:jsonb" json:"testCases,omitempty"`

	// Metadata (for additional custom fields)
	Metadata    datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`

	// Tags for categorization
	Tags        datatypes.JSON `gorm:"type:jsonb" json:"tags,omitempty"` // Array of strings

	// Publishing
	PublishedAt *time.Time     `json:"publishedAt,omitempty"`
	PublishedBy *uuid.UUID     `gorm:"type:uuid" json:"publishedBy,omitempty"`

	// Timestamps
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"deletedAt,omitempty"`
	CreatedBy   uuid.UUID      `gorm:"type:uuid" json:"createdBy"`
	UpdatedBy   uuid.UUID      `gorm:"type:uuid" json:"updatedBy"`

	// Relationships
	Tenant      *Tenant        `gorm:"foreignKey:TenantID;references:ID" json:"tenant,omitempty"`
	Bundles     []PolicyBundle `gorm:"many2many:bundle_policies;" json:"bundles,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (p *Policy) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for Policy
func (Policy) TableName() string {
	return "policies"
}

// PolicyTestCase represents a test case for a policy
type PolicyTestCase struct {
	Name     string                 `json:"name"`
	Input    map[string]interface{} `json:"input"`
	Expected map[string]interface{} `json:"expected"`
	Note     string                 `json:"note,omitempty"`
}

// PolicyVersion represents a historical version of a policy
type PolicyVersion struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	PolicyID    uuid.UUID      `gorm:"type:uuid;index;not null" json:"policyId"`
	Version     int            `gorm:"not null" json:"version"`
	Content     string         `gorm:"type:text;not null" json:"content"`
	ChangeNote  string         `gorm:"type:text" json:"changeNote,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	CreatedBy   uuid.UUID      `gorm:"type:uuid" json:"createdBy"`

	// Relationships
	Policy      *Policy        `gorm:"foreignKey:PolicyID;references:ID" json:"policy,omitempty"`
}

// BeforeCreate hook for PolicyVersion
func (pv *PolicyVersion) BeforeCreate(tx *gorm.DB) error {
	if pv.ID == uuid.Nil {
		pv.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for PolicyVersion
func (PolicyVersion) TableName() string {
	return "policy_versions"
}
