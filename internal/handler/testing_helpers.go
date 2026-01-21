package handler

import (
	"testing"

	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestDB creates an in-memory SQLite database for testing (pure Go, no CGO)
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	err = db.AutoMigrate(
		&models.APIKey{},
		&models.Group{},
		&models.RequestLog{},
		&models.GroupHourlyStat{},
	)
	require.NoError(t, err)

	return db
}

// setupTestServer creates a test server with minimal dependencies
func setupTestServer(t *testing.T) *Server {
	t.Helper()

	db := setupTestDB(t)

	mockConfig := &config.MockConfig{
		AuthKeyValue:       "test-auth-key-12345678",
		EncryptionKeyValue: "test-encryption-key-12345678",
	}

	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("")
	require.NoError(t, err)

	return &Server{
		DB:              db,
		config:          mockConfig,
		SettingsManager: settingsManager,
		EncryptionSvc:   encSvc,
	}
}
