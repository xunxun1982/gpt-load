package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_5_0_AddChildGroupSupport adds parent_group_id column to groups table
// to support child group (derived group) functionality.
// Child groups inherit proxy keys from parent and use parent's external endpoint as upstream.
func V1_5_0_AddChildGroupSupport(db *gorm.DB) error {
	migrator := db.Migrator()

	// Add parent_group_id column if not exists
	if !migrator.HasColumn("groups", "parent_group_id") {
		if err := db.Exec("ALTER TABLE groups ADD COLUMN parent_group_id INTEGER DEFAULT NULL").Error; err != nil {
			logrus.WithError(err).Error("Failed to add parent_group_id column")
			return err
		}
		logrus.Info("Added parent_group_id column to groups table")
	}

	// Create index on parent_group_id for faster child group queries
	indexName := "idx_groups_parent_group_id"
	if err := createIndexIfNotExists(db, "groups", indexName, "parent_group_id"); err != nil {
		logrus.WithError(err).Warnf("Failed to create %s index, continuing anyway", indexName)
	}

	return nil
}
