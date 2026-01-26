package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_18_0_OptimizeDynamicWeightQueries adds indexes to optimize dynamic weight metrics queries.
// Addresses slow SQL issues identified in production logs (47+ second queries).
// These indexes significantly improve LoadFromDatabase and cleanup operations.
// Compatible with SQLite 3.8+, MySQL 8.0+, and PostgreSQL 12+.
func V1_18_0_OptimizeDynamicWeightQueries(db *gorm.DB) error {
	logrus.Info("Running migration v1.18.0: Optimizing dynamic weight metrics queries")

	hadError := false // Track if any index creation failed
	dialect := db.Dialector.Name()

	// 1. Composite index for deleted_at filtering (most critical optimization)
	// Optimizes: SELECT * FROM dynamic_weight_metrics WHERE deleted_at IS NULL
	// This query was taking 47+ seconds without proper indexing
	// The composite index allows efficient filtering of non-deleted records
	if err := createIndexIfNotExists(db, "dynamic_weight_metrics", "idx_dw_metrics_deleted_type", "deleted_at, metric_type"); err != nil {
		hadError = true
		logrus.WithError(err).Warn("Failed to create idx_dw_metrics_deleted_type")
	}

	// 2. Composite index for metric lookup by type and group
	// Optimizes: SELECT * FROM dynamic_weight_metrics WHERE metric_type = ? AND group_id = ?
	// Used during metric updates and queries
	if err := createIndexIfNotExists(db, "dynamic_weight_metrics", "idx_dw_metrics_type_group", "metric_type, group_id"); err != nil {
		hadError = true
		logrus.WithError(err).Warn("Failed to create idx_dw_metrics_type_group")
	}

	// 3. Index for rollover operations
	// Optimizes: SELECT * FROM dynamic_weight_metrics WHERE deleted_at IS NULL AND last_rollover_at < ?
	// Used during daily rollover maintenance
	if err := createIndexIfNotExists(db, "dynamic_weight_metrics", "idx_dw_metrics_rollover", "deleted_at, last_rollover_at"); err != nil {
		hadError = true
		logrus.WithError(err).Warn("Failed to create idx_dw_metrics_rollover")
	}

	// 4. PostgreSQL-specific partial index for active metrics
	// Partial indexes are more efficient for frequently filtered columns
	if dialect == "postgres" || dialect == "pgx" {
		if err := db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_dw_metrics_active
			ON dynamic_weight_metrics (metric_type, group_id, updated_at)
			WHERE deleted_at IS NULL
		`).Error; err != nil {
			hadError = true
			logrus.WithError(err).Warn("Failed to create partial index idx_dw_metrics_active")
		} else {
			logrus.Info("Created partial index idx_dw_metrics_active for PostgreSQL")
		}
	}

	// 5. Ensure request_logs timestamp index exists (may already exist from v1.6.1)
	// This is critical for log cleanup operations to avoid timeouts
	// Optimizes: DELETE FROM request_logs WHERE timestamp < ?
	if err := createIndexIfNotExists(db, "request_logs", "idx_request_logs_timestamp", "timestamp"); err != nil {
		hadError = true
		logrus.WithError(err).Warn("Failed to create idx_request_logs_timestamp (may already exist)")
	}

	if hadError {
		logrus.Warn("Migration v1.18.0 completed with warnings; some indexes may be missing")
	} else {
		logrus.Info("Migration v1.18.0 completed successfully")
	}
	return nil
}
