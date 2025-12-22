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
		err = db.Raw(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'index' AND name = ?
		`, indexName).Scan(&indexCount).Error
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
// It first tries CREATE INDEX IF NOT EXISTS, then falls back to dialect-specific checks.
// Returns nil if index already exists or was created successfully.
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
	}

	logrus.Infof("Successfully added %s index", indexName)
	return nil
}
