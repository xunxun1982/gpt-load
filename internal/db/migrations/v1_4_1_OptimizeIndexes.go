package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_4_1_OptimizeIndexes adds optimized composite indexes for better query performance
func V1_4_1_OptimizeIndexes(db *gorm.DB) error {
	migrator := db.Migrator()
	dialectorName := db.Dialector.Name()

	logrus.Info("Running migration v1.4.1: Adding optimized composite indexes for performance")

	// Composite index for api_keys table - optimizes key listing and filtering queries
	// This index covers (group_id, status, last_used_at) for efficient queries
	indexNameAPIKeysGroupStatusLastUsed := "idx_api_keys_group_status_last_used"
	if !migrator.HasIndex("api_keys", indexNameAPIKeysGroupStatusLastUsed) {
		var sql string
		switch dialectorName {
		case "sqlite":
			sql = "CREATE INDEX IF NOT EXISTS idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at DESC)"
		case "mysql":
			sql = "CREATE INDEX idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at DESC)"
		case "postgres":
			sql = "CREATE INDEX IF NOT EXISTS idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at DESC NULLS LAST)"
		default:
			logrus.Warnf("Unsupported database dialect for custom index: %s, using basic index", dialectorName)
			sql = "CREATE INDEX IF NOT EXISTS idx_api_keys_group_status_last_used ON api_keys(group_id, status, last_used_at)"
		}

		if err := db.Exec(sql).Error; err != nil {
			logrus.WithError(err).Error("Failed to create idx_api_keys_group_status_last_used index")
			return err
		}
		logrus.Info("Successfully added idx_api_keys_group_status_last_used index")
	} else {
		logrus.Info("Index idx_api_keys_group_status_last_used already exists, skipping")
	}

	// Composite index for request_logs - optimizes log filtering by group and time
	indexNameRequestLogsGroupTimeStatus := "idx_request_logs_group_time_status"
	if !migrator.HasIndex("request_logs", indexNameRequestLogsGroupTimeStatus) {
		var sql string
		switch dialectorName {
		case "sqlite":
			sql = "CREATE INDEX IF NOT EXISTS idx_request_logs_group_time_status ON request_logs(group_id, timestamp DESC, is_success)"
		case "mysql":
			sql = "CREATE INDEX idx_request_logs_group_time_status ON request_logs(group_id, timestamp DESC, is_success)"
		case "postgres":
			sql = "CREATE INDEX IF NOT EXISTS idx_request_logs_group_time_status ON request_logs(group_id, timestamp DESC, is_success)"
		default:
			logrus.Warnf("Unsupported database dialect for custom index: %s, using basic index", dialectorName)
			sql = "CREATE INDEX IF NOT EXISTS idx_request_logs_group_time_status ON request_logs(group_id, timestamp, is_success)"
		}

		if err := db.Exec(sql).Error; err != nil {
			logrus.WithError(err).Error("Failed to create idx_request_logs_group_time_status index")
			return err
		}
		logrus.Info("Successfully added idx_request_logs_group_time_status index")
	} else {
		logrus.Info("Index idx_request_logs_group_time_status already exists, skipping")
	}

	// Index for key_hash lookups in request_logs
	indexNameRequestLogsKeyHashTimestamp := "idx_request_logs_key_hash_timestamp"
	if !migrator.HasIndex("request_logs", indexNameRequestLogsKeyHashTimestamp) {
		var sql string
		switch dialectorName {
		case "sqlite":
			sql = "CREATE INDEX IF NOT EXISTS idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp DESC) WHERE key_hash IS NOT NULL AND key_hash != ''"
		case "mysql":
			sql = "CREATE INDEX idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp DESC)"
		case "postgres":
			sql = "CREATE INDEX IF NOT EXISTS idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp DESC) WHERE key_hash IS NOT NULL AND key_hash != ''"
		default:
			logrus.Warnf("Unsupported database dialect for custom index: %s, using basic index", dialectorName)
			sql = "CREATE INDEX IF NOT EXISTS idx_request_logs_key_hash_timestamp ON request_logs(key_hash, timestamp)"
		}

		if err := db.Exec(sql).Error; err != nil {
			logrus.WithError(err).Error("Failed to create idx_request_logs_key_hash_timestamp index")
			return err
		}
		logrus.Info("Successfully added idx_request_logs_key_hash_timestamp index")
	} else {
		logrus.Info("Index idx_request_logs_key_hash_timestamp already exists, skipping")
	}

	return nil
}
