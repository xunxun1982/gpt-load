package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_3_3_AddCompositeIndexes adds composite indexes to optimize slow SQL queries
// - Adds composite index on groups(sort, id) for legacy ORDER BY sort ASC, id DESC queries
//   NOTE: idx_groups_sort_name on (sort, name) is added later in v1.4.2 for the new ORDER BY semantics
// - Adds composite index on api_keys(group_id, status) for filtering by group and status
// - Adds composite index on group_hourly_stats(group_id, time) for time-range queries
// - Adds composite index on api_keys(group_id, last_used_at, updated_at) for ORDER BY queries
func V1_3_3_AddCompositeIndexes(db *gorm.DB) error {
	logrus.Info("Running migration v1.3.3: Adding composite indexes to optimize slow SQL queries")

	// 1. Add composite index on groups(sort, id) for legacy ORDER BY queries
	// Note: idx_groups_sort_name on (sort, name) is added in v1.4.2 for the new ORDER BY semantics
	if err := createIndexIfNotExists(db, "groups", "idx_groups_sort_id", "sort, id"); err != nil {
		return err
	}

	// 2. Add composite index on api_keys(group_id, status) for filtering
	// This optimizes: SELECT count(*) FROM api_keys WHERE group_id = ? AND status = ?
	if err := createIndexIfNotExists(db, "api_keys", "idx_api_keys_group_status", "group_id, status"); err != nil {
		return err
	}

	// 3. Verify group_hourly_stats has proper index for time-range queries
	// The existing idx_group_time unique index (group_id, time) should already optimize these queries
	migrator := db.Migrator()
	if migrator.HasIndex("group_hourly_stats", "idx_group_time") {
		logrus.Info("Index idx_group_time on group_hourly_stats exists, range queries should be optimized")
	} else {
		logrus.Warn("Index idx_group_time on group_hourly_stats not found, time-range queries may be slow")
	}

	// 4. Add composite index on api_keys(group_id, last_used_at, updated_at) for ORDER BY queries
	// This optimizes: SELECT * FROM api_keys WHERE group_id = ? ORDER BY last_used_at desc, updated_at desc
	return createIndexIfNotExists(db, "api_keys", "idx_api_keys_group_order", "group_id, last_used_at, updated_at")
}
