package db

import (
	"strings"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_4_1_OptimizeIndexes adds optimized composite indexes for better query performance
func V1_4_1_OptimizeIndexes(db *gorm.DB) error {
	logrus.Info("Running migration v1.4.1: Adding optimized composite indexes for performance")

	// Define index configurations
	indexes := []struct {
		table     string
		indexName string
		ddlByDialect map[string]string
	}{
		{
			table:     "api_keys",
			indexName: "idx_api_keys_group_status_last_used",
			ddlByDialect: map[string]string{
				"sqlite":   "CREATE INDEX IF NOT EXISTS idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at DESC)",
				"mysql":    "CREATE INDEX idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at DESC)",
				"postgres": "CREATE INDEX IF NOT EXISTS idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at DESC NULLS LAST)",
				"default":  "CREATE INDEX IF NOT EXISTS idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at)",
			},
		},
		{
			table:     "request_logs",
			indexName: "idx_request_logs_group_time_status",
			ddlByDialect: map[string]string{
				"sqlite":   "CREATE INDEX IF NOT EXISTS idx_request_logs_group_time_status ON request_logs(group_id, timestamp DESC, is_success)",
				"mysql":    "CREATE INDEX idx_request_logs_group_time_status ON request_logs(group_id, timestamp DESC, is_success)",
				"postgres": "CREATE INDEX IF NOT EXISTS idx_request_logs_group_time_status ON request_logs(group_id, timestamp DESC, is_success)",
				"default":  "CREATE INDEX IF NOT EXISTS idx_request_logs_group_time_status ON request_logs(group_id, timestamp, is_success)",
			},
		},
		{
			table:     "request_logs",
			indexName: "idx_request_logs_key_hash_timestamp",
			ddlByDialect: map[string]string{
				"sqlite": "CREATE INDEX IF NOT EXISTS idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp DESC) WHERE key_hash IS NOT NULL AND key_hash != ''",
				// MySQL doesn't support partial indexes (WHERE clause), so it indexes all rows including NULL/empty values
				// This is a known MySQL limitation; partial index support was added only in MySQL 8.0.13+ with functional indexes
				"mysql":    "CREATE INDEX idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp DESC)",
				"postgres": "CREATE INDEX IF NOT EXISTS idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp DESC) WHERE key_hash IS NOT NULL AND key_hash != ''",
				"default":  "CREATE INDEX IF NOT EXISTS idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp)",
			},
		},
	}

	// Create each index using the helper function
	for _, idx := range indexes {
		if err := createIndexIfMissing(db, idx.table, idx.indexName, idx.ddlByDialect); err != nil {
			return err
		}
	}

	return nil
}

// createIndexIfMissing creates an index if it doesn't already exist
// This helper function reduces code duplication and improves maintainability
func createIndexIfMissing(db *gorm.DB, table, indexName string, ddlByDialect map[string]string) error {
	migrator := db.Migrator()
	dialectorName := db.Dialector.Name()

	// Check if index already exists
	if migrator.HasIndex(table, indexName) {
		logrus.Infof("Index %s already exists, skipping", indexName)
		return nil
	}

	// Get the appropriate DDL for the current database dialect
	sql, ok := ddlByDialect[dialectorName]
	if !ok {
		logrus.Warnf("Unsupported database dialect for custom index: %s, using basic index", dialectorName)
		sql = ddlByDialect["default"]
	}

	// Execute the DDL statement
	if err := db.Exec(sql).Error; err != nil {
		// MySQL-specific handling: Ignore duplicate key name error (error code 1061)
		// This can occur in concurrent migration scenarios where multiple instances
		// attempt to create the same index simultaneously
		if dialectorName == "mysql" && isMySQLDuplicateKeyError(err) {
			logrus.Warnf("Index %s already exists (concurrent creation detected), continuing", indexName)
			return nil
		}

		logrus.WithError(err).Errorf("Failed to create %s index", indexName)
		return err
	}

	logrus.Infof("Successfully added %s index", indexName)
	return nil
}

// isMySQLDuplicateKeyError checks if the error is a MySQL duplicate key name error (error code 1061)
func isMySQLDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}

	// Try to use type assertion for MySQL driver errors (more robust)
	// Note: We intentionally avoid importing MySQL driver directly to keep this package
	// database-agnostic. GORM wraps the underlying driver errors, so we use string matching
	// as a fallback. This is acceptable since error code 1061 has been stable since MySQL 3.23.
	errMsg := err.Error()
	return strings.Contains(errMsg, "Error 1061") || strings.Contains(errMsg, "Duplicate key name")
}
