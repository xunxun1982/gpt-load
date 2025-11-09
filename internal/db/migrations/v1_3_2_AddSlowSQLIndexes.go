package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_3_2_AddSlowSQLIndexes adds indexes to fix slow SQL queries
// - Adds index on group_sub_groups.sub_group_id for faster queries when finding parent aggregate groups
// - Adds index on groups.group_type for faster filtering in cron checker and other queries
func V1_3_2_AddSlowSQLIndexes(db *gorm.DB) error {
	migrator := db.Migrator()
	dialectorName := db.Dialector.Name()

	logrus.Info("Running migration v1.3.2: Adding indexes to fix slow SQL queries")

	// 1. Add index on group_sub_groups.sub_group_id
	// This fixes slow queries like:
	// - SELECT count(*) FROM group_sub_groups WHERE sub_group_id = ?
	// - SELECT * FROM group_sub_groups WHERE sub_group_id = ?
	indexNameSubGroupID := "idx_group_sub_groups_sub_group_id"
	hasSubGroupIDIndex := migrator.HasIndex("group_sub_groups", indexNameSubGroupID)

	if !hasSubGroupIDIndex {
		// Try CREATE INDEX IF NOT EXISTS first (works for SQLite, MySQL 5.7+, PostgreSQL)
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_group_sub_groups_sub_group_id ON group_sub_groups(sub_group_id)").Error; err != nil {
			// Fallback for databases that don't support IF NOT EXISTS (e.g., MySQL < 5.7)
			// Check if index exists using database-specific queries
			var indexExists bool
			var checkErr error

			switch dialectorName {
			case "mysql":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*)
					FROM information_schema.STATISTICS
					WHERE TABLE_SCHEMA = DATABASE()
					AND TABLE_NAME = 'group_sub_groups'
					AND INDEX_NAME = 'idx_group_sub_groups_sub_group_id'
				`).Scan(&indexCount).Error
				indexExists = indexCount > 0
			case "sqlite":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM sqlite_master
					WHERE type = 'index' AND name = 'idx_group_sub_groups_sub_group_id'
				`).Scan(&indexCount).Error
				indexExists = indexCount > 0
			case "postgres":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM pg_indexes
					WHERE tablename = 'group_sub_groups' AND indexname = 'idx_group_sub_groups_sub_group_id'
				`).Scan(&indexCount).Error
				indexExists = indexCount > 0
			default:
				// For other databases, assume index doesn't exist and try to create
				indexExists = false
			}

			if checkErr != nil {
				logrus.WithError(checkErr).Warn("Failed to check if sub_group_id index exists, attempting to create")
			}

			if !indexExists {
				// Index doesn't exist, create it without IF NOT EXISTS
				if createErr := db.Exec("CREATE INDEX idx_group_sub_groups_sub_group_id ON group_sub_groups(sub_group_id)").Error; createErr != nil {
					logrus.WithError(createErr).Error("Failed to create sub_group_id index on group_sub_groups")
					return createErr
				}
				logrus.Info("Successfully added idx_group_sub_groups_sub_group_id index")
			} else {
				logrus.Info("Index idx_group_sub_groups_sub_group_id already exists, skipping")
			}
		} else {
			logrus.Info("Successfully added idx_group_sub_groups_sub_group_id index")
		}
	} else {
		logrus.Info("Index idx_group_sub_groups_sub_group_id already exists, skipping")
	}

	// 2. Add index on groups.group_type
	// This fixes slow queries like:
	// - SELECT * FROM groups WHERE group_type != 'aggregate' OR group_type IS NULL
	indexNameGroupType := "idx_groups_group_type"
	hasGroupTypeIndex := migrator.HasIndex("groups", indexNameGroupType)

	if !hasGroupTypeIndex {
		// Try CREATE INDEX IF NOT EXISTS first (works for SQLite, MySQL 5.7+, PostgreSQL)
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_groups_group_type ON groups(group_type)").Error; err != nil {
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
					AND INDEX_NAME = 'idx_groups_group_type'
				`).Scan(&indexCount).Error
				indexExists = indexCount > 0
			case "sqlite":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM sqlite_master
					WHERE type = 'index' AND name = 'idx_groups_group_type'
				`).Scan(&indexCount).Error
				indexExists = indexCount > 0
			case "postgres":
				var indexCount int64
				checkErr = db.Raw(`
					SELECT COUNT(*) FROM pg_indexes
					WHERE tablename = 'groups' AND indexname = 'idx_groups_group_type'
				`).Scan(&indexCount).Error
				indexExists = indexCount > 0
			default:
				// For other databases, assume index doesn't exist and try to create
				indexExists = false
			}

			if checkErr != nil {
				logrus.WithError(checkErr).Warn("Failed to check if group_type index exists, attempting to create")
			}

			if !indexExists {
				// Index doesn't exist, create it without IF NOT EXISTS
				if createErr := db.Exec("CREATE INDEX idx_groups_group_type ON groups(group_type)").Error; createErr != nil {
					logrus.WithError(createErr).Error("Failed to create group_type index on groups")
					return createErr
				}
				logrus.Info("Successfully added idx_groups_group_type index")
			} else {
				logrus.Info("Index idx_groups_group_type already exists, skipping")
			}
		} else {
			logrus.Info("Successfully added idx_groups_group_type index")
		}
	} else {
		logrus.Info("Index idx_groups_group_type already exists, skipping")
	}

	return nil
}
