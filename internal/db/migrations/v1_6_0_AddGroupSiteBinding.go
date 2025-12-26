package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_6_0_AddGroupSiteBinding adds bound_site_id column to groups table
// and bound_group_id column to managed_sites table to support bidirectional binding
// between standard groups and managed sites.
//
// NOTE: We use raw SQL instead of GORM's migrator.AddColumn() because:
// 1. Raw SQL is more explicit and predictable across different database backends
// 2. "ALTER TABLE ... ADD COLUMN ... DEFAULT NULL" is standard SQL supported by SQLite/MySQL/PostgreSQL
// 3. GORM's AddColumn may have subtle differences in behavior across database drivers
func V1_6_0_AddGroupSiteBinding(db *gorm.DB) error {
	migrator := db.Migrator()

	// Add bound_site_id column to groups table if not exists
	if !migrator.HasColumn("groups", "bound_site_id") {
		if err := db.Exec("ALTER TABLE groups ADD COLUMN bound_site_id INTEGER DEFAULT NULL").Error; err != nil {
			logrus.WithError(err).Error("Failed to add bound_site_id column to groups table")
			return err
		}
		logrus.Info("Added bound_site_id column to groups table")
	}

	// Add bound_group_id column to managed_sites table if not exists
	if !migrator.HasColumn("managed_sites", "bound_group_id") {
		if err := db.Exec("ALTER TABLE managed_sites ADD COLUMN bound_group_id INTEGER DEFAULT NULL").Error; err != nil {
			logrus.WithError(err).Error("Failed to add bound_group_id column to managed_sites table")
			return err
		}
		logrus.Info("Added bound_group_id column to managed_sites table")
	}

	// Create index on bound_site_id for faster lookups
	indexName := "idx_groups_bound_site_id"
	if err := createIndexIfNotExists(db, "groups", indexName, "bound_site_id"); err != nil {
		logrus.WithError(err).Warnf("Failed to create %s index, continuing anyway", indexName)
	}

	// Create index on bound_group_id for faster lookups
	indexName = "idx_managed_sites_bound_group_id"
	if err := createIndexIfNotExists(db, "managed_sites", indexName, "bound_group_id"); err != nil {
		logrus.WithError(err).Warnf("Failed to create %s index, continuing anyway", indexName)
	}

	return nil
}
