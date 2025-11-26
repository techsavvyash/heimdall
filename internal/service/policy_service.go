package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/techsavvyash/heimdall/internal/models"
	"github.com/techsavvyash/heimdall/internal/opa"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PolicyService handles policy-related business logic
type PolicyService struct {
	db        *gorm.DB
	opaClient *opa.Client
}

// NewPolicyService creates a new policy service
func NewPolicyService(db *gorm.DB, opaClient *opa.Client) *PolicyService {
	return &PolicyService{
		db:        db,
		opaClient: opaClient,
	}
}

// CreatePolicyRequest represents a request to create a policy
type CreatePolicyRequest struct {
	TenantID    uuid.UUID              `json:"-"` // Set from authenticated user's context, not from request body
	Name        string                 `json:"name" validate:"required,min=3,max=200"`
	Description string                 `json:"description"`
	Path        string                 `json:"path"`
	Type        models.PolicyType      `json:"type" validate:"omitempty,oneof=rego json wasm"`
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

	// Set default path if not provided
	path := req.Path
	if path == "" {
		path = fmt.Sprintf("policies/%s", req.Name)
	}

	// Set default type if not provided
	policyType := req.Type
	if policyType == "" {
		policyType = models.PolicyTypeRego
	}

	policy := &models.Policy{
		TenantID:    req.TenantID,
		Name:        req.Name,
		Description: req.Description,
		Version:     1,
		Path:        path,
		Type:        policyType,
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

	// Basic checks
	if policy.Content == "" {
		policy.ValidationError = "Policy content is empty"
		policy.IsValid = false
		if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
			return fmt.Errorf("failed to save validation result: %w", err)
		}
		return nil
	}

	// Only validate Rego policies
	if policy.Type != models.PolicyTypeRego {
		policy.ValidationError = ""
		policy.IsValid = true
		now := time.Now()
		policy.ValidatedAt = &now
		if err := s.db.WithContext(ctx).Save(policy).Error; err != nil {
			return fmt.Errorf("failed to save validation result: %w", err)
		}
		return nil
	}

	// Validate Rego syntax by uploading to OPA
	// Use a temporary path for validation
	tempPath := fmt.Sprintf("temp/validation/%s", policyID.String())

	// Try to upload the policy to OPA - this will validate syntax
	if err := s.opaClient.UpsertPolicy(ctx, tempPath, policy.Content); err != nil {
		policy.ValidationError = fmt.Sprintf("Rego syntax error: %v", err)
		policy.IsValid = false
	} else {
		// Policy is valid, clean up the temporary policy
		_ = s.opaClient.DeletePolicy(ctx, tempPath)

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

	// If no test cases, return empty results
	if len(testCases) == 0 {
		return []PolicyTestResult{}, nil
	}

	// Only test Rego policies
	if policy.Type != models.PolicyTypeRego {
		return nil, fmt.Errorf("testing is only supported for Rego policies")
	}

	// Upload policy to OPA temporarily for testing
	tempPath := fmt.Sprintf("temp/testing/%s", policyID.String())
	if err := s.opaClient.UpsertPolicy(ctx, tempPath, policy.Content); err != nil {
		return nil, fmt.Errorf("failed to upload policy for testing: %w", err)
	}

	// Clean up after testing
	defer func() {
		_ = s.opaClient.DeletePolicy(ctx, tempPath)
	}()

	// Run each test case
	results := make([]PolicyTestResult, len(testCases))
	for i, tc := range testCases {
		result := PolicyTestResult{
			TestName: tc.Name,
		}

		// Evaluate the policy with the test input
		decision, err := s.opaClient.EvaluatePolicy(ctx, tempPath, tc.Input)
		if err != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("Failed to evaluate policy: %v", err)
			results[i] = result
			continue
		}

		// Compare the result with expected output
		passed, message := compareResults(decision.Result, tc.Expected)
		result.Passed = passed
		result.Message = message

		if tc.Note != "" && result.Passed {
			result.Message = tc.Note
		}

		results[i] = result
	}

	return results, nil
}

// compareResults compares the actual result with the expected result
func compareResults(actual interface{}, expected map[string]interface{}) (bool, string) {
	// Convert actual to map for comparison
	actualMap, ok := actual.(map[string]interface{})
	if !ok {
		// If the actual result is not a map, check if expected has a single key
		if len(expected) == 1 {
			for key, expectedValue := range expected {
				if compareValues(actual, expectedValue) {
					return true, "Test passed"
				}
				return false, fmt.Sprintf("Expected %s=%v, got %v", key, expectedValue, actual)
			}
		}
		return false, fmt.Sprintf("Expected map result, got %T", actual)
	}

	// Compare each expected key-value pair
	for key, expectedValue := range expected {
		actualValue, exists := actualMap[key]
		if !exists {
			return false, fmt.Sprintf("Expected key '%s' not found in result", key)
		}

		if !compareValues(actualValue, expectedValue) {
			return false, fmt.Sprintf("Mismatch for key '%s': expected %v, got %v", key, expectedValue, actualValue)
		}
	}

	return true, "Test passed"
}

// compareValues compares two values for equality, handling different types
func compareValues(actual, expected interface{}) bool {
	// Handle nil cases
	if actual == nil && expected == nil {
		return true
	}
	if actual == nil || expected == nil {
		return false
	}

	// Direct comparison for simple types
	if actual == expected {
		return true
	}

	// Handle boolean comparison
	actualBool, actualIsBool := actual.(bool)
	expectedBool, expectedIsBool := expected.(bool)
	if actualIsBool && expectedIsBool {
		return actualBool == expectedBool
	}

	// Handle numeric comparison (handle float64 and int conversion)
	actualFloat, actualIsFloat := toFloat64(actual)
	expectedFloat, expectedIsFloat := toFloat64(expected)
	if actualIsFloat && expectedIsFloat {
		return actualFloat == expectedFloat
	}

	// Handle string comparison
	actualStr, actualIsStr := actual.(string)
	expectedStr, expectedIsStr := expected.(string)
	if actualIsStr && expectedIsStr {
		return actualStr == expectedStr
	}

	// Handle map comparison
	actualMap, actualIsMap := actual.(map[string]interface{})
	expectedMap, expectedIsMap := expected.(map[string]interface{})
	if actualIsMap && expectedIsMap {
		if len(actualMap) != len(expectedMap) {
			return false
		}
		for key, expectedValue := range expectedMap {
			actualValue, exists := actualMap[key]
			if !exists || !compareValues(actualValue, expectedValue) {
				return false
			}
		}
		return true
	}

	// Handle slice comparison
	actualSlice, actualIsSlice := actual.([]interface{})
	expectedSlice, expectedIsSlice := expected.([]interface{})
	if actualIsSlice && expectedIsSlice {
		if len(actualSlice) != len(expectedSlice) {
			return false
		}
		for i := range expectedSlice {
			if !compareValues(actualSlice[i], expectedSlice[i]) {
				return false
			}
		}
		return true
	}

	return false
}

// toFloat64 converts numeric types to float64
func toFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int32:
		return float64(val), true
	case int64:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint32:
		return float64(val), true
	case uint64:
		return float64(val), true
	default:
		return 0, false
	}
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
