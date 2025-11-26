package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/techsavvyash/heimdall/internal/config"
	"github.com/techsavvyash/heimdall/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// BundleService handles policy bundle operations
type BundleService struct {
	db          *gorm.DB
	minioClient *minio.Client
	bucket      string
}

// NewBundleService creates a new bundle service
func NewBundleService(db *gorm.DB, cfg *config.MinIOConfig) (*BundleService, error) {
	// Initialize MinIO client
	minioClient, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create MinIO client: %w", err)
	}

	return &BundleService{
		db:          db,
		minioClient: minioClient,
		bucket:      cfg.Bucket,
	}, nil
}

// EnsureBucket ensures the MinIO bucket exists
func (s *BundleService) EnsureBucket(ctx context.Context) error {
	exists, err := s.minioClient.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("failed to check bucket existence: %w", err)
	}

	if !exists {
		if err := s.minioClient.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	return nil
}

// CreateBundleRequest represents a request to create a bundle
type CreateBundleRequest struct {
	TenantID    *uuid.UUID `json:"tenantId,omitempty"` // nil for global bundles
	Name        string     `json:"name" validate:"required"`
	Description string     `json:"description"`
	Version     string     `json:"version" validate:"required"`
	PolicyIDs   []uuid.UUID `json:"policyIds" validate:"required,min=1"`
	IsGlobal    bool       `json:"isGlobal"`
}

// CreateBundle creates a new policy bundle
func (s *BundleService) CreateBundle(ctx context.Context, userID uuid.UUID, req *CreateBundleRequest) (*models.PolicyBundle, error) {
	bundle := &models.PolicyBundle{
		Name:        req.Name,
		Description: req.Description,
		Version:     req.Version,
		Status:      models.BundleStatusBuilding,
		IsGlobal:    req.IsGlobal,
		CreatedBy:   userID,
		UpdatedBy:   userID,
		StorageBucket: s.bucket,
	}

	if req.TenantID != nil {
		bundle.TenantID = *req.TenantID
	}

	// Create bundle record
	if err := s.db.WithContext(ctx).Create(bundle).Error; err != nil {
		return nil, fmt.Errorf("failed to create bundle: %w", err)
	}

	// Associate policies with bundle
	now := time.Now()
	for _, policyID := range req.PolicyIDs {
		bundlePolicy := &models.BundlePolicy{
			BundleID: bundle.ID,
			PolicyID: policyID,
			AddedAt:  now,
			AddedBy:  userID,
		}
		if err := s.db.WithContext(ctx).Create(bundlePolicy).Error; err != nil {
			return nil, fmt.Errorf("failed to associate policy: %w", err)
		}
	}

	// Build the bundle asynchronously
	go s.buildBundle(context.Background(), bundle.ID, userID)

	return bundle, nil
}

// buildBundle builds the OPA bundle tar.gz file and uploads to MinIO
func (s *BundleService) buildBundle(ctx context.Context, bundleID, userID uuid.UUID) {
	// Update status
	now := time.Now()
	s.db.Model(&models.PolicyBundle{}).Where("id = ?", bundleID).Updates(map[string]interface{}{
		"build_started_at": now,
		"status":           models.BundleStatusBuilding,
	})

	// Get bundle with policies
	var bundle models.PolicyBundle
	if err := s.db.Preload("Policies").First(&bundle, "id = ?", bundleID).Error; err != nil {
		s.updateBundleError(ctx, bundleID, fmt.Sprintf("Failed to load bundle: %v", err))
		return
	}

	// Create bundle tar.gz
	bundleData, checksum, err := s.createBundleTarGz(&bundle)
	if err != nil {
		s.updateBundleError(ctx, bundleID, fmt.Sprintf("Failed to create bundle: %v", err))
		return
	}

	// Upload to MinIO
	storagePath := fmt.Sprintf("bundles/heimdall-%s.tar.gz", bundle.Version)
	_, err = s.minioClient.PutObject(
		ctx,
		s.bucket,
		storagePath,
		bytes.NewReader(bundleData),
		int64(len(bundleData)),
		minio.PutObjectOptions{ContentType: "application/gzip"},
	)
	if err != nil {
		s.updateBundleError(ctx, bundleID, fmt.Sprintf("Failed to upload bundle: %v", err))
		return
	}

	// Update bundle with success
	completedAt := time.Now()
	s.db.Model(&models.PolicyBundle{}).Where("id = ?", bundleID).Updates(map[string]interface{}{
		"status":              models.BundleStatusReady,
		"build_completed_at":  completedAt,
		"storage_path":        storagePath,
		"size":                len(bundleData),
		"checksum":            checksum,
	})
}

// createBundleTarGz creates a tar.gz bundle from policies
func (s *BundleService) createBundleTarGz(bundle *models.PolicyBundle) ([]byte, string, error) {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzWriter)

	// Create manifest
	manifest := map[string]interface{}{
		"name":     bundle.Name,
		"version":  bundle.Version,
		"policies": []string{},
	}

	// Add each policy to the bundle
	for _, policy := range bundle.Policies {
		// Add policy content
		content := []byte(policy.Content)
		fileName := fmt.Sprintf("%s.rego", policy.Path)

		header := &tar.Header{
			Name: fileName,
			Mode: 0644,
			Size: int64(len(content)),
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, "", fmt.Errorf("failed to write tar header: %w", err)
		}

		if _, err := tarWriter.Write(content); err != nil {
			return nil, "", fmt.Errorf("failed to write policy content: %w", err)
		}

		manifest["policies"] = append(manifest["policies"].([]string), fileName)
	}

	// Add manifest.json
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal manifest: %w", err)
	}

	manifestHeader := &tar.Header{
		Name: ".manifest",
		Mode: 0644,
		Size: int64(len(manifestData)),
	}

	if err := tarWriter.WriteHeader(manifestHeader); err != nil {
		return nil, "", fmt.Errorf("failed to write manifest header: %w", err)
	}

	if _, err := tarWriter.Write(manifestData); err != nil {
		return nil, "", fmt.Errorf("failed to write manifest: %w", err)
	}

	// Close writers
	if err := tarWriter.Close(); err != nil {
		return nil, "", fmt.Errorf("failed to close tar writer: %w", err)
	}

	if err := gzWriter.Close(); err != nil {
		return nil, "", fmt.Errorf("failed to close gzip writer: %w", err)
	}

	// Calculate checksum
	bundleData := buf.Bytes()
	hash := sha256.Sum256(bundleData)
	checksum := hex.EncodeToString(hash[:])

	return bundleData, checksum, nil
}

// updateBundleError updates bundle with error status
func (s *BundleService) updateBundleError(ctx context.Context, bundleID uuid.UUID, errorMsg string) {
	completedAt := time.Now()
	s.db.Model(&models.PolicyBundle{}).Where("id = ?", bundleID).Updates(map[string]interface{}{
		"status":              models.BundleStatusFailed,
		"build_completed_at":  completedAt,
		"build_error":         errorMsg,
	})
}

// GetBundle retrieves a bundle by ID
func (s *BundleService) GetBundle(ctx context.Context, bundleID uuid.UUID) (*models.PolicyBundle, error) {
	var bundle models.PolicyBundle
	if err := s.db.WithContext(ctx).Preload("Policies").First(&bundle, "id = ?", bundleID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("bundle not found")
		}
		return nil, fmt.Errorf("failed to get bundle: %w", err)
	}

	return &bundle, nil
}

// GetBundles retrieves all bundles, optionally filtered by tenant
func (s *BundleService) GetBundles(ctx context.Context, tenantID *uuid.UUID) ([]*models.PolicyBundle, error) {
	var bundles []*models.PolicyBundle
	query := s.db.WithContext(ctx)

	if tenantID != nil {
		query = query.Where("tenant_id = ? OR is_global = ?", *tenantID, true)
	}

	if err := query.Order("created_at DESC").Find(&bundles).Error; err != nil {
		return nil, fmt.Errorf("failed to get bundles: %w", err)
	}

	return bundles, nil
}

// ActivateBundle activates a bundle (sets it as the active bundle)
func (s *BundleService) ActivateBundle(ctx context.Context, bundleID, userID uuid.UUID) (*models.PolicyBundle, error) {
	bundle, err := s.GetBundle(ctx, bundleID)
	if err != nil {
		return nil, err
	}

	if bundle.Status != models.BundleStatusReady {
		return nil, fmt.Errorf("bundle must be in ready status to activate")
	}

	// Deactivate other active bundles for the same tenant
	if err := s.db.WithContext(ctx).Model(&models.PolicyBundle{}).
		Where("tenant_id = ? AND status = ?", bundle.TenantID, models.BundleStatusActive).
		Update("status", models.BundleStatusInactive).Error; err != nil {
		return nil, fmt.Errorf("failed to deactivate existing bundles: %w", err)
	}

	// Activate this bundle
	now := time.Now()
	bundle.Status = models.BundleStatusActive
	bundle.ActivatedAt = &now
	bundle.ActivatedBy = &userID

	if err := s.db.WithContext(ctx).Save(bundle).Error; err != nil {
		return nil, fmt.Errorf("failed to activate bundle: %w", err)
	}

	return bundle, nil
}

// DeployBundle creates a deployment record for a bundle
func (s *BundleService) DeployBundle(ctx context.Context, bundleID, userID uuid.UUID, environment string) (*models.BundleDeployment, error) {
	bundle, err := s.GetBundle(ctx, bundleID)
	if err != nil {
		return nil, err
	}

	if bundle.Status != models.BundleStatusActive && bundle.Status != models.BundleStatusReady {
		return nil, fmt.Errorf("bundle must be ready or active to deploy")
	}

	deployment := &models.BundleDeployment{
		BundleID:    bundleID,
		DeployedBy:  userID,
		Environment: environment,
		Status:      "success",
		DeployedAt:  time.Now(),
	}

	if err := s.db.WithContext(ctx).Create(deployment).Error; err != nil {
		return nil, fmt.Errorf("failed to create deployment: %w", err)
	}

	return deployment, nil
}

// RollbackBundle rolls back to a previous bundle
func (s *BundleService) RollbackBundle(ctx context.Context, bundleID, targetBundleID, userID uuid.UUID, reason string) error {
	// Deactivate current bundle
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.PolicyBundle{}).
		Where("id = ?", bundleID).
		Updates(map[string]interface{}{
			"status":         models.BundleStatusInactive,
			"deactivated_at": now,
			"deactivated_by": userID,
		}).Error; err != nil {
		return fmt.Errorf("failed to deactivate bundle: %w", err)
	}

	// Activate target bundle
	if _, err := s.ActivateBundle(ctx, targetBundleID, userID); err != nil {
		return fmt.Errorf("failed to activate target bundle: %w", err)
	}

	// Create deployment record with rollback info
	deployment := &models.BundleDeployment{
		BundleID:       targetBundleID,
		DeployedBy:     userID,
		Status:         "success",
		DeployedAt:     time.Now(),
		RollbackReason: reason,
	}

	if err := s.db.WithContext(ctx).Create(deployment).Error; err != nil {
		return fmt.Errorf("failed to create rollback deployment: %w", err)
	}

	return nil
}

// GetBundleDeployments retrieves deployment history for a bundle
func (s *BundleService) GetBundleDeployments(ctx context.Context, bundleID uuid.UUID) ([]*models.BundleDeployment, error) {
	var deployments []*models.BundleDeployment
	if err := s.db.WithContext(ctx).
		Where("bundle_id = ?", bundleID).
		Order("deployed_at DESC").
		Find(&deployments).Error; err != nil {
		return nil, fmt.Errorf("failed to get deployments: %w", err)
	}

	return deployments, nil
}

// DownloadBundle downloads a bundle from MinIO
func (s *BundleService) DownloadBundle(ctx context.Context, bundleID uuid.UUID) (io.Reader, error) {
	bundle, err := s.GetBundle(ctx, bundleID)
	if err != nil {
		return nil, err
	}

	if bundle.StoragePath == "" {
		return nil, fmt.Errorf("bundle has not been built yet")
	}

	object, err := s.minioClient.GetObject(ctx, s.bucket, bundle.StoragePath, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to download bundle: %w", err)
	}

	return object, nil
}

// DeleteBundle soft deletes a bundle
func (s *BundleService) DeleteBundle(ctx context.Context, bundleID uuid.UUID) error {
	bundle, err := s.GetBundle(ctx, bundleID)
	if err != nil {
		return err
	}

	if bundle.Status == models.BundleStatusActive {
		return fmt.Errorf("cannot delete active bundle")
	}

	if err := s.db.WithContext(ctx).Delete(bundle).Error; err != nil {
		return fmt.Errorf("failed to delete bundle: %w", err)
	}

	return nil
}

// UpdateBundleManifest updates the bundle manifest
func (s *BundleService) UpdateBundleManifest(ctx context.Context, bundleID uuid.UUID, manifest map[string]interface{}) error {
	manifestJSON, err := datatypes.NewJSONType(manifest).MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&models.PolicyBundle{}).
		Where("id = ?", bundleID).
		Update("manifest", manifestJSON).Error; err != nil {
		return fmt.Errorf("failed to update manifest: %w", err)
	}

	return nil
}
