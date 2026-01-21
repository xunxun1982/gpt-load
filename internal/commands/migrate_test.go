package commands

import (
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMigrationBatchSize(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1000, migrationBatchSize)
	assert.Greater(t, migrationBatchSize, 0)
}

func TestValidateAndGetScenario(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		fromKey     string
		toKey       string
		expected    string
		expectError bool
	}{
		{
			name:        "enable encryption",
			fromKey:     "",
			toKey:       "new-strong-key-12345",
			expected:    "enable encryption",
			expectError: false,
		},
		{
			name:        "disable encryption",
			fromKey:     "old-key",
			toKey:       "",
			expected:    "disable encryption",
			expectError: false,
		},
		{
			name:        "change encryption key",
			fromKey:     "old-key",
			toKey:       "new-strong-key-12345",
			expected:    "change encryption key",
			expectError: false,
		},
		{
			name:        "same keys",
			fromKey:     "same-key",
			toKey:       "same-key",
			expected:    "",
			expectError: true,
		},
		{
			name:        "no keys provided",
			fromKey:     "",
			toKey:       "",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &MigrateKeysCommand{
				fromKey: tt.fromKey,
				toKey:   tt.toKey,
			}

			scenario, err := cmd.validateAndGetScenario()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, scenario)
			}
		})
	}
}

func TestNewMigrateKeysCommand(t *testing.T) {
	t.Parallel()
	cmd := NewMigrateKeysCommand(nil, nil, nil, "from-key", "to-key")

	assert.NotNil(t, cmd)
	assert.Equal(t, "from-key", cmd.fromKey)
	assert.Equal(t, "to-key", cmd.toKey)
}

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Auto-migrate the APIKey model
	err = db.AutoMigrate(&models.APIKey{})
	require.NoError(t, err)

	return db
}

func TestCreateMigrationServices(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		fromKey     string
		toKey       string
		expectError bool
	}{
		{
			name:        "enable encryption",
			fromKey:     "",
			toKey:       "test-key-12345",
			expectError: false,
		},
		{
			name:        "disable encryption",
			fromKey:     "test-key-12345",
			toKey:       "",
			expectError: false,
		},
		{
			name:        "change key",
			fromKey:     "old-key-12345",
			toKey:       "new-key-12345",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &MigrateKeysCommand{
				fromKey: tt.fromKey,
				toKey:   tt.toKey,
			}

			oldService, newService, err := cmd.createMigrationServices()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, oldService)
				assert.NotNil(t, newService)
			}
		})
	}
}

func TestPreCheck_EmptyDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db:      db,
		fromKey: "",
		toKey:   "test-key-12345",
	}

	err := cmd.preCheck()
	assert.NoError(t, err)
}

func TestPreCheck_WithUnencryptedData(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)

	// Create unencrypted test data
	noopService, err := encryption.NewService("")
	require.NoError(t, err)

	testKey := "sk-test-key-123"
	hash := noopService.Hash(testKey)

	key := models.APIKey{
		KeyValue: testKey,
		KeyHash:  hash,
	}
	err = db.Create(&key).Error
	require.NoError(t, err)

	cmd := &MigrateKeysCommand{
		db:      db,
		fromKey: "",
		toKey:   "test-key-12345",
	}

	err = cmd.preCheck()
	assert.NoError(t, err)
}

func TestCreateTempTable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db: db,
	}

	err := cmd.createTempTable()
	assert.NoError(t, err)

	// Verify table exists
	var count int64
	err = db.Table("temp_migration").Count(&count).Error
	assert.NoError(t, err)
}

func TestDropTempTable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db: db,
	}

	// Create temp table first
	err := cmd.createTempTable()
	require.NoError(t, err)

	// Drop it
	err = cmd.dropTempTable()
	assert.NoError(t, err)

	// Verify table doesn't exist (should error)
	var count int64
	err = db.Table("temp_migration").Count(&count).Error
	assert.Error(t, err)
}

func TestProcessBatchToTempTable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db:      db,
		fromKey: "",
		toKey:   "test-key-12345",
	}

	// Create temp table
	err := cmd.createTempTable()
	require.NoError(t, err)

	// Create services
	oldService, err := encryption.NewService("")
	require.NoError(t, err)
	newService, err := encryption.NewService("test-key-12345")
	require.NoError(t, err)

	// Create test keys
	testKey := "sk-test-key-123"
	hash := oldService.Hash(testKey)

	keys := []models.APIKey{
		{ID: 1, KeyValue: testKey, KeyHash: hash},
		{ID: 2, KeyValue: testKey, KeyHash: hash},
	}

	err = cmd.processBatchToTempTable(keys, oldService, newService)
	assert.NoError(t, err)

	// Verify data in temp table
	var count int64
	err = db.Table("temp_migration").Count(&count).Error
	assert.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestVerifyTempColumns_EmptyDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db:    db,
		toKey: "test-key-12345",
	}

	err := cmd.verifyTempColumns()
	assert.NoError(t, err)
}

func TestClearCache_NilStore(t *testing.T) {
	t.Parallel()
	cmd := &MigrateKeysCommand{
		cacheStore: nil,
	}

	err := cmd.clearCache()
	assert.NoError(t, err)
}

func TestClearCache_WithStore(t *testing.T) {
	t.Parallel()
	mockStore := store.NewMemoryStore()
	cmd := &MigrateKeysCommand{
		cacheStore: mockStore,
	}

	err := cmd.clearCache()
	assert.NoError(t, err)
}

func TestDetectIfAlreadyEncrypted_EmptyDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db:      db,
		fromKey: "",
		toKey:   "test-key-12345",
	}

	err := cmd.detectIfAlreadyEncrypted()
	assert.NoError(t, err)
}

func TestDetectIfAlreadyEncrypted_WithUnencryptedData(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)

	// Create unencrypted test data
	noopService, err := encryption.NewService("")
	require.NoError(t, err)

	testKey := "sk-test-key-123"
	hash := noopService.Hash(testKey)

	key := models.APIKey{
		KeyValue: testKey,
		KeyHash:  hash,
	}
	err = db.Create(&key).Error
	require.NoError(t, err)

	cmd := &MigrateKeysCommand{
		db:      db,
		fromKey: "",
		toKey:   "test-key-12345",
	}

	err = cmd.detectIfAlreadyEncrypted()
	assert.NoError(t, err)
}

func TestSwitchColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db:      db,
		fromKey: "",
		toKey:   "test-key-12345",
	}

	// Create temp table
	err := cmd.createTempTable()
	require.NoError(t, err)

	// Create original data
	noopService, err := encryption.NewService("")
	require.NoError(t, err)

	testKey := "sk-test-key-123"
	hash := noopService.Hash(testKey)

	key := models.APIKey{
		KeyValue: testKey,
		KeyHash:  hash,
	}
	err = db.Create(&key).Error
	require.NoError(t, err)

	// Create services and process to temp table
	newService, err := encryption.NewService("test-key-12345")
	require.NoError(t, err)

	keys := []models.APIKey{key}
	err = cmd.processBatchToTempTable(keys, noopService, newService)
	require.NoError(t, err)

	// Switch columns
	err = cmd.switchColumns()
	assert.NoError(t, err)

	// Verify data was updated
	var updatedKey models.APIKey
	err = db.First(&updatedKey, key.ID).Error
	assert.NoError(t, err)
	assert.NotEqual(t, testKey, updatedKey.KeyValue) // Should be encrypted now
}

func TestCreateBackupTableAndMigrate_EmptyDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db := setupTestDB(t)
	cmd := &MigrateKeysCommand{
		db:      db,
		fromKey: "",
		toKey:   "test-key-12345",
	}

	err := cmd.createBackupTableAndMigrate()
	assert.NoError(t, err)
}

func TestValidateAndGetScenario_WeakPassword(t *testing.T) {
	t.Parallel()
	cmd := &MigrateKeysCommand{
		fromKey: "",
		toKey:   "weak", // Too short - will generate warning but not error
	}

	scenario, err := cmd.validateAndGetScenario()
	assert.NoError(t, err) // Weak passwords generate warnings, not errors
	assert.Equal(t, "enable encryption", scenario)
}

func TestMigrateKeysCommand_Fields(t *testing.T) {
	t.Parallel()
	db := &gorm.DB{}
	mockStore := store.NewMemoryStore()

	cmd := NewMigrateKeysCommand(db, nil, mockStore, "from", "to")

	assert.Equal(t, db, cmd.db)
	assert.Equal(t, mockStore, cmd.cacheStore)
	assert.Equal(t, "from", cmd.fromKey)
	assert.Equal(t, "to", cmd.toKey)
}
