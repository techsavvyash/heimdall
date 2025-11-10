package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// BundleStatus defines the status of a policy bundle
type BundleStatus string

const (
	BundleStatusBuilding  BundleStatus = "building"   // Bundle is being built
	BundleStatusReady     BundleStatus = "ready"      // Bundle is ready to deploy
	BundleStatusActive    BundleStatus = "active"     // Bundle is currently active
	BundleStatusInactive  BundleStatus = "inactive"   // Bundle is inactive
	BundleStatusFailed    BundleStatus = "failed"     // Bundle build failed
)

// PolicyBundle represents a bundle of policies for deployment to OPA
type PolicyBundle struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	TenantID    uuid.UUID      `gorm:"type:uuid;index" json:"tenantId,omitempty"` // Null for global bundles

	// Bundle metadata
	Name        string         `gorm:"type:varchar(200);not null" json:"name"`
	Description string         `gorm:"type:text" json:"description,omitempty"`
	Version     string         `gorm:"type:varchar(100);not null" json:"version"` // Semantic version (e.g., "1.0.0")

	// Bundle status
	Status      BundleStatus   `gorm:"type:varchar(50);default:'building';not null" json:"status"`
	IsGlobal    bool           `gorm:"default:false" json:"isGlobal"` // Global bundles apply to all tenants

	// Build information
	BuildStartedAt  *time.Time  `json:"buildStartedAt,omitempty"`
	BuildCompletedAt *time.Time `json:"buildCompletedAt,omitempty"`
	BuildError      string      `gorm:"type:text" json:"buildError,omitempty"`
	BuildLog        string      `gorm:"type:text" json:"buildLog,omitempty"`

	// Storage information
	StoragePath     string      `gorm:"type:varchar(500)" json:"storagePath,omitempty"` // Path in MinIO
	StorageBucket   string      `gorm:"type:varchar(200)" json:"storageBucket,omitempty"`
	Size            int64       `json:"size,omitempty"` // Size in bytes
	Checksum        string      `gorm:"type:varchar(256)" json:"checksum,omitempty"` // SHA256 checksum

	// Activation tracking
	ActivatedAt     *time.Time  `json:"activatedAt,omitempty"`
	ActivatedBy     *uuid.UUID  `gorm:"type:uuid" json:"activatedBy,omitempty"`
	DeactivatedAt   *time.Time  `json:"deactivatedAt,omitempty"`
	DeactivatedBy   *uuid.UUID  `gorm:"type:uuid" json:"deactivatedBy,omitempty"`

	// Bundle manifest (stores metadata about included policies)
	Manifest        datatypes.JSON `gorm:"type:jsonb" json:"manifest,omitempty"`

	// Timestamps
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"deletedAt,omitempty"`
	CreatedBy   uuid.UUID      `gorm:"type:uuid" json:"createdBy"`
	UpdatedBy   uuid.UUID      `gorm:"type:uuid" json:"updatedBy"`

	// Relationships
	Tenant      *Tenant        `gorm:"foreignKey:TenantID;references:ID" json:"tenant,omitempty"`
	Policies    []Policy       `gorm:"many2many:bundle_policies;" json:"policies,omitempty"`
	Deployments []BundleDeployment `gorm:"foreignKey:BundleID;references:ID" json:"deployments,omitempty"`
}

// BeforeCreate hook to set UUID if not provided
func (pb *PolicyBundle) BeforeCreate(tx *gorm.DB) error {
	if pb.ID == uuid.Nil {
		pb.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for PolicyBundle
func (PolicyBundle) TableName() string {
	return "policy_bundles"
}

// BundleDeployment represents a deployment of a bundle to OPA
type BundleDeployment struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	BundleID    uuid.UUID      `gorm:"type:uuid;index;not null" json:"bundleId"`

	// Deployment metadata
	DeployedAt  time.Time      `json:"deployedAt"`
	DeployedBy  uuid.UUID      `gorm:"type:uuid" json:"deployedBy"`
	Environment string         `gorm:"type:varchar(100)" json:"environment,omitempty"` // e.g., "production", "staging"

	// Deployment status
	Status      string         `gorm:"type:varchar(50);not null" json:"status"` // "success", "failed", "rolling_back"
	ErrorMessage string        `gorm:"type:text" json:"errorMessage,omitempty"`

	// Rollback information
	RolledBackAt *time.Time    `json:"rolledBackAt,omitempty"`
	RolledBackBy *uuid.UUID    `gorm:"type:uuid" json:"rolledBackBy,omitempty"`
	RollbackReason string      `gorm:"type:text" json:"rollbackReason,omitempty"`

	// Timestamps
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`

	// Relationships
	Bundle      *PolicyBundle  `gorm:"foreignKey:BundleID;references:ID" json:"bundle,omitempty"`
}

// BeforeCreate hook for BundleDeployment
func (bd *BundleDeployment) BeforeCreate(tx *gorm.DB) error {
	if bd.ID == uuid.Nil {
		bd.ID = uuid.New()
	}
	return nil
}

// TableName specifies the table name for BundleDeployment
func (BundleDeployment) TableName() string {
	return "bundle_deployments"
}

// BundlePolicy is the join table between bundles and policies
type BundlePolicy struct {
	BundleID  uuid.UUID `gorm:"type:uuid;primaryKey" json:"bundleId"`
	PolicyID  uuid.UUID `gorm:"type:uuid;primaryKey" json:"policyId"`
	AddedAt   time.Time `json:"addedAt"`
	AddedBy   uuid.UUID `gorm:"type:uuid" json:"addedBy"`

	// Relationships
	Bundle    *PolicyBundle `gorm:"foreignKey:BundleID;references:ID" json:"bundle,omitempty"`
	Policy    *Policy       `gorm:"foreignKey:PolicyID;references:ID" json:"policy,omitempty"`
}

// TableName specifies the table name for BundlePolicy
func (BundlePolicy) TableName() string {
	return "bundle_policies"
}
