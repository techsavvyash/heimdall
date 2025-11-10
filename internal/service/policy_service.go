package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PolicyService handles policy-related business logic
type PolicyService struct {
	db *gorm.DB
}

// NewPolicyService creates a new policy service
func NewPolicyService(db *gorm.DB) *PolicyService {
	return &PolicyService{
		db: db,
	}
}

// CreatePolicyRequest represents a request to create a policy
type CreatePolicyRequest struct {
	TenantID    uuid.UUID              `json:"tenantId" validate:"required"`
	Name        string                 `json:"name" validate:"required,min=3,max=200"`
	Description string                 `json:"description"`
	Path        string                 `json:"path" validate:"required"`
	Type        models.PolicyType      `json:"type" validate:"required,oneof=rego json wasm"`
	Content     string                 `json:"content" validate:"required"`
	Tags        []string               `json:"tags"`
	Metadata    map[string]interface{} `json:"metadata"`
	TestCases   []models.PolicyTestCase `json:"testCases"`
}

// UpdatePolicyRequest represents a request to update a policy
type UpdatePolicyRequest struct {
	Name        *string                 `json:"name,omitempty"`
	Description *string                 `json:"description,omitempty"`
	Content     *string                 `json:"content,omitempty"`
	Status      *models.PolicyStatus    `json:"status,omitempty"`
	Tags        []string                `json:"tags,omitempty"`
	Metadata    map[string]interface{}  `json:"metadata,omitempty"`
	TestCases   []models.PolicyTestCase `json:"testCases,omitempty"`
}

// CreatePolicy creates a new policy
func (s *PolicyService) CreatePolicy(ctx context.Context, userID uuid.UUID, req *CreatePolicyRequest) (*models.Policy, error) {
	// Convert tags to JSON
	tagsJSON, err := convertToJSON(req.Tags)
	if err != nil {
		return nil, fmt.Errorf("failed to convert tags: %w", err)
	}

	// Convert metadata to JSON
	metadataJSON, err := convertToJSON(req.Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to convert metadata: %w", err)
	}

	// Convert test cases to JSON
	testCasesJSON, err := convertToJSON(req.TestCases)
	if err != nil {
		return nil, fmt.Errorf("failed to convert test cases: %w", err)
	}

	policy := &models.Policy{
		TenantID:    req.TenantID,
		Name:        req.Name,
		Description: req.Description,
		Version:     1,
		Path:        req.Path,
		Type:        req.Type,
		Content:     req.Content,
		Status:      models.PolicyStatusDraft,
		IsValid:     false,
		Tags:        tagsJSON,
		Metadata:    metadataJSON,
		TestCases:   testCasesJSON,
		CreatedBy:   userID,
		UpdatedBy:   userID,
	}

	if err := s.db.WithContext(ctx).Create(policy).Error; err != nil {
		return nil, fmt.Errorf("failed to create policy: %w", err)
	}

	return policy, nil
}

// GetPolicy retrieves a policy by ID
func (s *PolicyService) GetPolicy(ctx context.Context, policyID uuid.UUID) (*models.Policy, error) {
	var policy models.Policy
	if err := s.db.WithContext(ctx).First(&policy, "id = ?", policyID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("policy not found")
		}
		return nil, fmt.Errorf("failed to get policy: %w", err)
	}

	return &policy, nil
}

// GetPoliciesByTenant retrieves all policies for a tenant
func (s *PolicyService) GetPoliciesByTenant(ctx context.Context, tenantID uuid.UUID, status *models.PolicyStatus) ([]*models.Policy, error) {
	var policies []*models.Policy
	query := s.db.WithContext(ctx).Where("tenant_id = ?", tenantID)

	if status != nil {
		query = query.Where("status = ?", *status)
	}

	if err := query.Order("created_at DESC").Find(&policies).Error; err != nil {
		return nil, fmt.Errorf("failed to get policies: %w", err)
	}

	return policies, nil
}

// UpdatePolicy updates an existing policy
func (s *PolicyService) UpdatePolicy(ctx context.Context, policyID, userID uuid.UUID, req *UpdatePolicyRequest) (*models.Policy, error) {
	policy, err := s.GetPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}

	// Create version before update if content changed
	if req.Content != nil && *req.Content != policy.Content {
		if err := s.createPolicyVersion(ctx, policy, userID, "Content updated"); err != nil {
			return nil, fmt.Errorf("failed to create policy version: %w", err)
		}
		policy.Version++
		policy.Content = *req.Content
		policy.IsValid = false // Mark as invalid when content changes
	}

	// Update fields
	if req.Name != nil {
		policy.Name = *req.Name
	}
	if req.Description != nil {
		policy.Description = *req.Description
	}
	if req.Status != nil {
		policy.Status = *req.Status
	}
	if req.Tags != nil {
		tagsJSON, err := convertToJSON(req.Tags)
		if err != nil {
			return nil, fmt.Errorf("failed to convert tags: %w", err)
		}
		policy.Tags = tagsJSON
	}
	if req.Metadata != nil {
		metadataJSON, err := convertToJSON(req.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to convert metadata: %w", err)
		}
		policy.Metadata = metadataJSON
	}
	if req.TestCases != nil {
		testCasesJSON, err := convertToJSON(req.TestCases)
		if err != nil {
			return nil, fmt.Errorf("failed to convert test cases: %w", err)
		}
		policy.TestCases = testCasesJSON
	}

	policy.UpdatedBy = userID

	if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
		return nil, fmt.Errorf("failed to update policy: %w", err)
	}

	return policy, nil
}

// DeletePolicy soft deletes a policy
func (s *PolicyService) DeletePolicy(ctx context.Context, policyID uuid.UUID) error {
	policy, err := s.GetPolicy(ctx, policyID)
	if err != nil {
		return err
	}

	if policy.IsSystem {
		return fmt.Errorf("cannot delete system policy")
	}

	if err := s.db.WithContext(ctx).Delete(policy).Error; err != nil {
		return fmt.Errorf("failed to delete policy: %w", err)
	}

	return nil
}

// ValidatePolicy validates a policy's Rego syntax
func (s *PolicyService) ValidatePolicy(ctx context.Context, policyID uuid.UUID) error {
	policy, err := s.GetPolicy(ctx, policyID)
	if err != nil {
		return err
	}

	// TODO: Implement actual Rego validation using OPA
	// For now, just basic checks
	if policy.Content == "" {
		policy.ValidationError = "Policy content is empty"
		policy.IsValid = false
	} else {
		policy.ValidationError = ""
		policy.IsValid = true
		now := time.Now()
		policy.ValidatedAt = &now
	}

	if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
		return fmt.Errorf("failed to save validation result: %w", err)
	}

	return nil
}

// PublishPolicy publishes a policy (marks it as active)
func (s *PolicyService) PublishPolicy(ctx context.Context, policyID, userID uuid.UUID) (*models.Policy, error) {
	policy, err := s.GetPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}

	if !policy.IsValid {
		return nil, fmt.Errorf("cannot publish invalid policy")
	}

	policy.Status = models.PolicyStatusActive
	now := time.Now()
	policy.PublishedAt = &now
	policy.PublishedBy = &userID
	policy.UpdatedBy = userID

	if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
		return nil, fmt.Errorf("failed to publish policy: %w", err)
	}

	return policy, nil
}

// ArchivePolicy archives a policy
func (s *PolicyService) ArchivePolicy(ctx context.Context, policyID, userID uuid.UUID) (*models.Policy, error) {
	policy, err := s.GetPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}

	if policy.IsSystem {
		return nil, fmt.Errorf("cannot archive system policy")
	}

	policy.Status = models.PolicyStatusArchived
	policy.UpdatedBy = userID

	if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
		return nil, fmt.Errorf("failed to archive policy: %w", err)
	}

	return policy, nil
}

// GetPolicyVersions retrieves all versions of a policy
func (s *PolicyService) GetPolicyVersions(ctx context.Context, policyID uuid.UUID) ([]*models.PolicyVersion, error) {
	var versions []*models.PolicyVersion
	if err := s.db.WithContext(ctx).
		Where("policy_id = ?", policyID).
		Order("version DESC").
		Find(&versions).Error; err != nil {
		return nil, fmt.Errorf("failed to get policy versions: %w", err)
	}

	return versions, nil
}

// RollbackToVersion rolls back a policy to a specific version
func (s *PolicyService) RollbackToVersion(ctx context.Context, policyID uuid.UUID, version int, userID uuid.UUID) (*models.Policy, error) {
	// Get the target version
	var targetVersion models.PolicyVersion
	if err := s.db.WithContext(ctx).
		Where("policy_id = ? AND version = ?", policyID, version).
		First(&targetVersion).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("version not found")
		}
		return nil, fmt.Errorf("failed to get policy version: %w", err)
	}

	// Get the current policy
	policy, err := s.GetPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}

	// Create a new version with current content before rollback
	if err := s.createPolicyVersion(ctx, policy, userID, fmt.Sprintf("Rollback from version %d to version %d", policy.Version, version)); err != nil {
		return nil, fmt.Errorf("failed to create version before rollback: %w", err)
	}

	// Update policy with target version content
	policy.Content = targetVersion.Content
	policy.Version++
	policy.IsValid = false
	policy.UpdatedBy = userID

	if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
		return nil, fmt.Errorf("failed to rollback policy: %w", err)
	}

	return policy, nil
}

// createPolicyVersion creates a new policy version
func (s *PolicyService) createPolicyVersion(ctx context.Context, policy *models.Policy, userID uuid.UUID, changeNote string) error {
	version := &models.PolicyVersion{
		PolicyID:   policy.ID,
		Version:    policy.Version,
		Content:    policy.Content,
		ChangeNote: changeNote,
		CreatedBy:  userID,
	}

	if err := s.db.WithContext(ctx).Create(version).Error; err != nil {
		return fmt.Errorf("failed to create policy version: %w", err)
	}

	return nil
}

// TestPolicy tests a policy against its test cases
func (s *PolicyService) TestPolicy(ctx context.Context, policyID uuid.UUID) ([]PolicyTestResult, error) {
	policy, err := s.GetPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}

	var testCases []models.PolicyTestCase
	if len(policy.TestCases) > 0 {
		if err := json.Unmarshal(policy.TestCases, &testCases); err != nil {
			return nil, fmt.Errorf("failed to unmarshal test cases: %w", err)
		}
	}

	// TODO: Implement actual policy testing using OPA
	results := make([]PolicyTestResult, len(testCases))
	for i, tc := range testCases {
		results[i] = PolicyTestResult{
			TestName: tc.Name,
			Passed:   true, // Placeholder
			Message:  "Test not implemented yet",
		}
	}

	return results, nil
}

// PolicyTestResult represents the result of a policy test
type PolicyTestResult struct {
	TestName string `json:"testName"`
	Passed   bool   `json:"passed"`
	Message  string `json:"message,omitempty"`
}

// SearchPolicies searches policies by name, description, or tags
func (s *PolicyService) SearchPolicies(ctx context.Context, tenantID uuid.UUID, query string) ([]*models.Policy, error) {
	var policies []*models.Policy
	searchPattern := "%" + query + "%"

	if err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND (name ILIKE ? OR description ILIKE ?)", tenantID, searchPattern, searchPattern).
		Order("created_at DESC").
		Find(&policies).Error; err != nil {
		return nil, fmt.Errorf("failed to search policies: %w", err)
	}

	return policies, nil
}

// Helper function to convert to JSON
func convertToJSON(v interface{}) (datatypes.JSON, error) {
	if v == nil {
		return nil, nil
	}
	return datatypes.NewJSONType(v).MarshalJSON()
}
