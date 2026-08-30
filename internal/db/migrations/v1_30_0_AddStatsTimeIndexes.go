package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	// groupHourlyStatsTimeIndex is a single-column index on group_hourly_stats.time.
	// The retention cleanup deletes rows where time < ?; without a time-prefixed index
	// such deletes would fall back to a full table scan because the existing composite index
	// (group_id, time) and the unique index cannot serve a time-only predicate efficiently.
	groupHourlyStatsTimeIndex = "idx_group_hourly_stats_time"

	// modelTokenHourlyStatsTimeIndex is a single-column index on model_token_hourly_stats.time
	// with the same retention-cleanup rationale as groupHourlyStatsTimeIndex. The name matches
	// the model gorm tag (index:idx_model_token_hourly_time) so the migration reuses the index
	// AutoMigrate would create instead of duplicating it.
	modelTokenHourlyStatsTimeIndex = "idx_model_token_hourly_time"
)

// V1_30_0_AddStatsTimeIndexes adds single-column time indexes to the two hourly stats tables.
// Retention cleanup (StatsRetentionDays) deletes rows with time < cutoff; those deletes need
// a time-prefixed index to avoid full-table scans on large stats tables. Compatible with
// SQLite 3.24+, MySQL 8.0+ and PostgreSQL 12+. Note: MySQL does not support
// CREATE INDEX IF NOT EXISTS (MariaDB does); createIndexIfNotExists falls back to a
// dialect index-existence check for MySQL, so this migration is safe on all supported DBs.
// The migration is idempotent: it is a no-op when the indexes already exist (e.g. created by
// AutoMigrate from the model gorm tags).
func V1_30_0_AddStatsTimeIndexes(db *gorm.DB) error {
	logrus.Info("Running migration v1.30.0: Adding time indexes to hourly stats tables")

	if err := createIndexIfNotExists(db, "group_hourly_stats", groupHourlyStatsTimeIndex, "time"); err != nil {
		return err
	}
	return createIndexIfNotExists(db, "model_token_hourly_stats", modelTokenHourlyStatsTimeIndex, "time")
}
