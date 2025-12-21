package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_4_2_AddGroupSortNameIndex adds composite index on groups(sort, name) for ORDER BY queries.
// This optimizes: SELECT * FROM groups ORDER BY sort ASC, name ASC
func V1_4_2_AddGroupSortNameIndex(db *gorm.DB) error {
	migrator := db.Migrator()
	dialectorName := db.Dialector.Name()

	indexName := "idx_groups_sort_name"
	hasIndex := migrator.HasIndex("groups", indexName)

	if hasIndex {
		logrus.Infof("Index %s already exists, skipping", indexName)
		return nil
	}

	logrus.Info("Running migration v1.4.2: Adding idx_groups_sort_name index")

	// Try CREATE INDEX IF NOT EXISTS first
	if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_groups_sort_name ON groups(sort, name)").Error; err != nil {
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
				AND INDEX_NAME = 'idx_groups_sort_name'
			`).Scan(&indexCount).Error
			indexExists = indexCount > 0
		case "sqlite":
			var indexCount int64
			checkErr = db.Raw(`
				SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND name = 'idx_groups_sort_name'
			`).Scan(&indexCount).Error
			indexExists = indexCount > 0
		case "postgres":
			var indexCount int64
			checkErr = db.Raw(`
				SELECT COUNT(*) FROM pg_indexes
				WHERE tablename = 'groups' AND indexname = 'idx_groups_sort_name'
			`).Scan(&indexCount).Error
			indexExists = indexCount > 0
		default:
			indexExists = false
		}

		if checkErr != nil {
			logrus.WithError(checkErr).Warn("Failed to check if idx_groups_sort_name exists, attempting to create")
		}

		if !indexExists {
			if createErr := db.Exec("CREATE INDEX idx_groups_sort_name ON groups(sort, name)").Error; createErr != nil {
				logrus.WithError(createErr).Error("Failed to create idx_groups_sort_name index")
				return createErr
			}
		}
	}

	logrus.Info("Successfully added idx_groups_sort_name index")
	return nil
}
