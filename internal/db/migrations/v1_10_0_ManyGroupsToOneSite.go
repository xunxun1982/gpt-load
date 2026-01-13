package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_10_0_ManyGroupsToOneSite migrates from one-to-one to many-to-one relationship
// between groups and sites. Multiple groups can now bind to the same site.
//
// Changes:
// 1. Clear bound_group_id in managed_sites table (no longer used for relationship tracking)
// 2. The relationship is now tracked only via groups.bound_site_id (many groups -> one site)
// 3. Site disable will cascade to all bound groups (handled in application layer)
//
// Note: We keep the bound_group_id column for backward compatibility but set all values to NULL.
// This allows rollback if needed and avoids complex ALTER TABLE operations.
func V1_10_0_ManyGroupsToOneSite(db *gorm.DB) error {
	logrus.Info("Running migration v1.10.0: Converting to many-groups-to-one-site relationship")

	// Clear bound_group_id in managed_sites table
	// The relationship is now tracked only via groups.bound_site_id
	if err := db.Exec("UPDATE managed_sites SET bound_group_id = NULL WHERE bound_group_id IS NOT NULL").Error; err != nil {
		logrus.WithError(err).Error("Failed to clear bound_group_id in managed_sites table")
		return err
	}

	logrus.Info("Migration v1.10.0 completed: Cleared bound_group_id in managed_sites table")
	return nil
}
