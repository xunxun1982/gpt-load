package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_7_0_AddSiteManagementIndexes adds indexes for site management performance optimization.
// These indexes target frequently executed queries identified through code analysis.
func V1_7_0_AddSiteManagementIndexes(db *gorm.DB) error {
	logrus.Info("Running migration v1.7.0: Adding site management performance indexes")

	// 1. Composite index for site listing ORDER BY (sort ASC, id ASC)
	// Optimizes: SELECT * FROM managed_sites ORDER BY sort ASC, id ASC
	if err := createIndexIfNotExists(db, "managed_sites", "idx_managed_sites_sort_id", "sort, id"); err != nil {
		logrus.WithError(err).Warn("Failed to create idx_managed_sites_sort_id")
	}

	// 2. Index for enabled status filter
	// Optimizes: SELECT * FROM managed_sites WHERE enabled = ?
	if err := createIndexIfNotExists(db, "managed_sites", "idx_managed_sites_enabled", "enabled"); err != nil {
		logrus.WithError(err).Warn("Failed to create idx_managed_sites_enabled")
	}

	// 3. Composite index for checkin log queries with pagination
	// Optimizes: SELECT * FROM managed_site_checkin_logs WHERE site_id = ? ORDER BY created_at DESC, id DESC
	if err := createIndexIfNotExists(db, "managed_site_checkin_logs", "idx_checkin_logs_site_created", "site_id, created_at DESC"); err != nil {
		logrus.WithError(err).Warn("Failed to create idx_checkin_logs_site_created")
	}

	logrus.Info("Migration v1.7.0 completed")
	return nil
}
