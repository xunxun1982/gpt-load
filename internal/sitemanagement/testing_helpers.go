package sitemanagement

import (
	"testing"

	"gpt-load/internal/encryption"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestDB creates an in-memory SQLite database for testing
// Optimized for speed with disabled logging and prepared statement cache
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use pure Go SQLite implementation (glebarez/sqlite) to avoid CGO dependency
	// Use unique in-memory database for each test to avoid conflicts
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true, // Enable prepared statement cache for speed
		Logger:                 logger.Default.LogMode(logger.Silent), // Disable logging for speed
	})
	require.NoError(t, err, "Failed to create test database")
	return db
}

// setupTestEncryption creates a test encryption service
func setupTestEncryption(t *testing.T) encryption.Service {
	t.Helper()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err, "Failed to create encryption service")
	return encSvc
}
