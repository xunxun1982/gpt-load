package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_3_3_AddCompositeIndexes adds composite indexes to optimize slow SQL queries
// - Adds composite index on groups(sort, id) for ORDER BY sort asc, id desc queries
// - Adds composite index on api_keys(group_id, status) for filtering by group and status
// - Adds composite index on group_hourly_stats(group_id, time) for time-range queries
// - Adds composite index on api_keys(group_id, last_used_at, updated_at) for ORDER BY queries
func V1_3_3_AddCompositeIndexes(db *gorm.DB) error {
	migrator := db.Migrator()
	dialectorName := db.Dialector.Name()

	logrus.Info("Running migration v1.3.3: Adding composite indexes to optimize slow SQL queries")

	// 1. Add composite index on groups(sort, id) for ORDER BY queries
	// This optimizes: SELECT * FROM groups ORDER BY sort asc, id desc
	indexNameGroupsSortID := "idx_groups_sort_id"
	hasGroupsSortIDIndex := migrator.HasIndex("groups", indexNameGroupsSortID)

	if !hasGroupsSortIDIndex {
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_groups_sort_id ON groups(sort, id)").Error; err != nil {
			// Fallback for databases that don't support IF NOT EXISTS
			var indexExists bool
			var checkErr error

			switch dialectorName {
			case "mysql":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*)
					FROM information_schema.STATISTICS
					WHERE TABLE_SCHEMA = DATABASE()
					AND TABLE_NAME = 'groups'
					AND INDEX_NAME = 'idx_groups_sort_id'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			case "sqlite":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM sqlite_master
					WHERE type = 'index' AND name = 'idx_groups_sort_id'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			case "postgres":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM pg_indexes
					WHERE tablename = 'groups' AND indexname = 'idx_groups_sort_id'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			default:
				indexExists = false
			}

			if checkErr != nil {
				logrus.WithError(checkErr).Warn("Failed to check if idx_groups_sort_id exists, attempting to create")
			}

			if !indexExists {
				if createErr := db.Exec("CREATE INDEX idx_groups_sort_id ON groups(sort, id)").Error; createErr != nil {
					logrus.WithError(createErr).Error("Failed to create idx_groups_sort_id index")
					return createErr
				}
			}
		}
		logrus.Info("Successfully added idx_groups_sort_id index")
	} else {
		logrus.Info("Index idx_groups_sort_id already exists, skipping")
	}

	// 2. Add composite index on api_keys(group_id, status) for filtering
	// This optimizes: SELECT count(*) FROM api_keys WHERE group_id = ? AND status = ?
	// Note: There's already idx_api_keys_group_status composite index, but we need to verify it covers both columns
	indexNameAPIKeysGroupStatus := "idx_api_keys_group_status"
	hasAPIKeysGroupStatusIndex := migrator.HasIndex("api_keys", indexNameAPIKeysGroupStatus)

	if !hasAPIKeysGroupStatusIndex {
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_api_keys_group_status ON api_keys(group_id, status)").Error; err != nil {
			var indexExists bool
			var checkErr error

			switch dialectorName {
			case "mysql":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*)
					FROM information_schema.STATISTICS
					WHERE TABLE_SCHEMA = DATABASE()
					AND TABLE_NAME = 'api_keys'
					AND INDEX_NAME = 'idx_api_keys_group_status'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			case "sqlite":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM sqlite_master
					WHERE type = 'index' AND name = 'idx_api_keys_group_status'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			case "postgres":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM pg_indexes
					WHERE tablename = 'api_keys' AND indexname = 'idx_api_keys_group_status'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			default:
				indexExists = false
			}

			if checkErr != nil {
				logrus.WithError(checkErr).Warn("Failed to check if idx_api_keys_group_status exists, attempting to create")
			}

			if !indexExists {
				if createErr := db.Exec("CREATE INDEX idx_api_keys_group_status ON api_keys(group_id, status)").Error; createErr != nil {
					logrus.WithError(createErr).Error("Failed to create idx_api_keys_group_status index")
					return createErr
				}
			}
		}
		logrus.Info("Successfully added idx_api_keys_group_status index")
	} else {
		logrus.Info("Index idx_api_keys_group_status already exists, skipping")
	}

	// 3. Verify group_hourly_stats has proper index for time-range queries
	// The existing idx_group_time unique index (group_id, time) should already optimize these queries
	// Most databases can use unique indexes for range queries efficiently
	// We just log that the index exists and is being used
	hasGroupHourlyStatsIndex := migrator.HasIndex("group_hourly_stats", "idx_group_time")
	if hasGroupHourlyStatsIndex {
		logrus.Info("Index idx_group_time on group_hourly_stats exists, range queries should be optimized")
	} else {
		logrus.Warn("Index idx_group_time on group_hourly_stats not found, time-range queries may be slow")
	}

	// 4. Add composite index on api_keys(group_id, last_used_at, updated_at) for ORDER BY queries
	// This optimizes: SELECT * FROM api_keys WHERE group_id = ? ORDER BY last_used_at desc, updated_at desc
	indexNameAPIKeysGroupOrder := "idx_api_keys_group_order"
	hasAPIKeysGroupOrderIndex := migrator.HasIndex("api_keys", indexNameAPIKeysGroupOrder)

	if !hasAPIKeysGroupOrderIndex {
		// For NULL values in last_used_at, we need to handle them properly
		// SQLite and PostgreSQL handle NULLs in indexes differently, but the index should still work
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_api_keys_group_order ON api_keys(group_id, last_used_at, updated_at)").Error; err != nil {
			var indexExists bool
			var checkErr error

			switch dialectorName {
			case "mysql":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*)
					FROM information_schema.STATISTICS
					WHERE TABLE_SCHEMA = DATABASE()
					AND TABLE_NAME = 'api_keys'
					AND INDEX_NAME = 'idx_api_keys_group_order'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			case "sqlite":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM sqlite_master
					WHERE type = 'index' AND name = 'idx_api_keys_group_order'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			case "postgres":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM pg_indexes
					WHERE tablename = 'api_keys' AND indexname = 'idx_api_keys_group_order'
				`).Count(&indexCount).Error
				indexExists = indexCount > 0
			default:
				indexExists = false
			}

			if checkErr != nil {
				logrus.WithError(checkErr).Warn("Failed to check if idx_api_keys_group_order exists, attempting to create")
			}

			if !indexExists {
				if createErr := db.Exec("CREATE INDEX idx_api_keys_group_order ON api_keys(group_id, last_used_at, updated_at)").Error; createErr != nil {
					logrus.WithError(createErr).Error("Failed to create idx_api_keys_group_order index")
					return createErr
				}
			}
		}
		logrus.Info("Successfully added idx_api_keys_group_order index")
	} else {
		logrus.Info("Index idx_api_keys_group_order already exists, skipping")
	}

	return nil
}
