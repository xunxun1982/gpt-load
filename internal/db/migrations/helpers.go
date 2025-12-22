package db

import (
	"fmt"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// checkIndexExists checks if an index exists using dialect-specific queries.
// Returns false if check fails or dialect is unknown.
func checkIndexExists(db *gorm.DB, dialectorName, tableName, indexName string) bool {
	var indexCount int64
	var err error

	switch dialectorName {
	case "mysql":
		err = db.Raw(`
			SELECT COUNT(*)
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = ?
			AND INDEX_NAME = ?
		`, tableName, indexName).Scan(&indexCount).Error
	case "sqlite":
		// Filter by tbl_name to ensure the index belongs to the specified table
		// Although index names are typically globally unique in this project,
		// adding table name filter is more robust
		err = db.Raw(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'index' AND tbl_name = ? AND name = ?
		`, tableName, indexName).Scan(&indexCount).Error
	case "postgres":
		err = db.Raw(`
			SELECT COUNT(*) FROM pg_indexes
			WHERE tablename = ? AND indexname = ?
		`, tableName, indexName).Scan(&indexCount).Error
	default:
		return false
	}

	if err != nil {
		logrus.WithError(err).Warnf("Failed to check if index %s exists", indexName)
		return false
	}

	return indexCount > 0
}

// createIndexIfNotExists creates an index if it doesn't exist.
// It first checks via GORM HasIndex, then tries CREATE INDEX IF NOT EXISTS,
// and falls back to dialect-specific checks for older databases.
// Returns nil if index already exists or was created successfully.
//
// AI suggestion (not adopted): Simplify fallback logic by checking dialect support upfront.
// Reason: Current flow is already reasonable - HasIndex check first, then CREATE INDEX IF NOT EXISTS,
// finally fallback to dialect-specific check. This approach is compatible with MySQL 8.0+,
// PostgreSQL, SQLite and other mainstream versions. Upfront dialect checking would add
// complexity without practical benefit, and error handling remains clear.
func createIndexIfNotExists(db *gorm.DB, tableName, indexName, columns string) error {
	migrator := db.Migrator()
	dialectorName := db.Dialector.Name()

	// Check if index already exists using GORM's HasIndex
	if migrator.HasIndex(tableName, indexName) {
		logrus.Infof("Index %s already exists, skipping", indexName)
		return nil
	}

	// Try CREATE INDEX IF NOT EXISTS first (supported by SQLite, PostgreSQL, MySQL 8.0+)
	createSQL := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s(%s)", indexName, tableName, columns)
	if err := db.Exec(createSQL).Error; err != nil {
		// Fallback: check if index exists using dialect-specific query
		if checkIndexExists(db, dialectorName, tableName, indexName) {
			logrus.Infof("Index %s already exists (detected via fallback), skipping", indexName)
			return nil
		}

		// Index doesn't exist, try to create without IF NOT EXISTS
		createSQL = fmt.Sprintf("CREATE INDEX %s ON %s(%s)", indexName, tableName, columns)
		if createErr := db.Exec(createSQL).Error; createErr != nil {
			logrus.WithError(createErr).Errorf("Failed to create %s index", indexName)
			return createErr
		}
		logrus.Infof("Index %s created successfully (via fallback)", indexName)
		return nil
	}

	// CREATE INDEX IF NOT EXISTS succeeded
	// Since HasIndex returned false above, the index was just created (not silently skipped)
	logrus.Infof("Index %s created successfully", indexName)
	return nil
}
