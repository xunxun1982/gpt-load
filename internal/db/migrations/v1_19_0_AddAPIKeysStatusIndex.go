package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_19_0_AddAPIKeysStatusIndex adds status index to api_keys table.
// This index optimizes queries filtering by status (e.g., RemoveInvalidKeys).
// Compatible with SQLite, MySQL 8.0+, and PostgreSQL 12+.
func V1_19_0_AddAPIKeysStatusIndex(db *gorm.DB) error {
	logrus.Info("Running migration v1.19.0: Adding api_keys status index")

	// Add status index for filtering by status
	// Optimizes: SELECT ... FROM api_keys WHERE status = ?
	// Optimizes: DELETE FROM api_keys WHERE status = ? (e.g., RemoveInvalidKeys)
	// Note: Queries with both group_id AND status use idx_api_keys_group_status
	if err := createIndexIfNotExists(db, "api_keys", "idx_api_keys_status", "status"); err != nil {
		logrus.WithError(err).Warn("Failed to create idx_api_keys_status")
		return err
	}

	logrus.Info("Migration v1.19.0 completed")
	return nil
}
