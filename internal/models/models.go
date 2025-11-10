package models

import "gorm.io/gorm"

// AllModels returns all models that need to be migrated
func AllModels() []interface{} {
	return []interface{}{
		&Tenant{},
		&User{},
		&Role{},
		&Permission{},
		&UserRole{},
		&RolePermission{},
		&AuditLog{},
		&Policy{},
		&PolicyVersion{},
		&PolicyBundle{},
		&BundleDeployment{},
		&BundlePolicy{},
	}
}

// AutoMigrate runs GORM's AutoMigrate on all models
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(AllModels()...)
}
