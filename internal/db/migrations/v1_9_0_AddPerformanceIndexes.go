package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_9_0_AddPerformanceIndexes adds additional indexes for performance optimization.
// These indexes target frequently executed queries identified through code analysis.
// Compatible with SQLite, MySQL 8.0+, and PostgreSQL 12+.
func V1_9_0_AddPerformanceIndexes(db *gorm.DB) error {
	logrus.Info("Running migration v1.9.0: Adding performance optimization indexes")

	dialect := db.Dialector.Name()

	// 1. Index for request_logs key_hash lookups
	// Optimizes: SELECT ... FROM request_logs WHERE key_hash = ?
	if err := createIndexIfNotExists(db, "request_logs", "idx_request_logs_key_hash", "key_hash"); err != nil {
		logrus.WithError(err).Warn("Failed to create idx_request_logs_key_hash")
	}

	// 2. Composite index for group_hourly_stats time range queries
	// Optimizes: SELECT ... FROM group_hourly_stats WHERE group_id = ? AND time >= ? AND time < ?
	if err := createIndexIfNotExists(db, "group_hourly_stats", "idx_hourly_stats_group_time", "group_id, time"); err != nil {
		logrus.WithError(err).Warn("Failed to create idx_hourly_stats_group_time")
	}

	// 3. Composite index for groups listing with enabled filter
	// Optimizes: SELECT * FROM groups WHERE enabled = true ORDER BY sort, name
	if err := createIndexIfNotExists(db, "groups", "idx_groups_enabled_sort", "enabled, sort, name"); err != nil {
		logrus.WithError(err).Warn("Failed to create idx_groups_enabled_sort")
	}

	// 4. PostgreSQL-specific partial indexes for better performance
	if dialect == "postgres" || dialect == "pgx" {
		// Partial index for active keys count
		// Optimizes: SELECT COUNT(*) FROM api_keys WHERE group_id = ? AND status = 'active'
		if err := db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_api_keys_active_by_group
			ON api_keys (group_id)
			WHERE status = 'active'
		`).Error; err != nil {
			logrus.WithError(err).Warn("Failed to create partial index idx_api_keys_active_by_group")
		} else {
			logrus.Info("Created partial index idx_api_keys_active_by_group")
		}

		// Partial index for failure_count queries (for blacklist threshold checks)
		// Optimizes: SELECT ... FROM api_keys WHERE group_id = ? AND failure_count > 0
		if err := db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_api_keys_failure_count
			ON api_keys (group_id, failure_count)
			WHERE failure_count > 0
		`).Error; err != nil {
			logrus.WithError(err).Warn("Failed to create partial index idx_api_keys_failure_count")
		} else {
			logrus.Info("Created partial index idx_api_keys_failure_count")
		}
	} else {
		// For MySQL and SQLite, create regular composite index for failure_count
		if err := createIndexIfNotExists(db, "api_keys", "idx_api_keys_group_failure", "group_id, failure_count"); err != nil {
			logrus.WithError(err).Warn("Failed to create idx_api_keys_group_failure")
		}
	}

	logrus.Info("Migration v1.9.0 completed")
	return nil
}
