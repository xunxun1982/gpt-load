package services

import (
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupKeyImportServiceTest creates a test environment for KeyImportService
func setupKeyImportServiceTest(tb testing.TB) (*gorm.DB, *KeyImportService, *TaskService, *KeyService) {
	tb.Helper()
	// Use :memory: for isolated testing
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		PrepareStmt: false,
	})
	require.NoError(tb, err)

	// Limit SQLite connections to avoid separate in-memory databases
	sqlDB, err := db.DB()
	require.NoError(tb, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	tb.Cleanup(func() {
		_ = sqlDB.Close()
	})

	err = db.AutoMigrate(&models.APIKey{}, &models.Group{})
	require.NoError(tb, err)

	settingsManager := config.NewSystemSettingsManager()
	memStore := store.NewMemoryStore()
	encryptionSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(tb, err)

	keyProvider := keypool.NewProvider(db, memStore, settingsManager, encryptionSvc)
	tb.Cleanup(func() {
		keyProvider.Stop()
	})

	keyValidator := keypool.NewKeyValidator(keypool.KeyValidatorParams{
		DB:              db,
		SettingsManager: settingsManager,
	})
	keyService := NewKeyService(db, keyProvider, keyValidator, encryptionSvc)

	taskService := NewTaskService(memStore)
	bulkImportService := NewBulkImportService(db)
	keyImportService := NewKeyImportService(taskService, keyService, bulkImportService)

	return db, keyImportService, taskService, keyService
}

// createTestGroupForImport creates a minimal valid group for testing
func createTestGroupForImport(tb testing.TB, db *gorm.DB, name string) models.Group {
	tb.Helper()
	group := models.Group{
		Name:        name,
		ChannelType: "openai",
		GroupType:   "standard",
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	err := db.Create(&group).Error
	require.NoError(tb, err)
	return group
}

// TestKeyImportService_SmallBatch tests importing a small batch of keys
func TestKeyImportService_SmallBatch(t *testing.T) {
	t.Parallel()
	db, importSvc, taskSvc, _ := setupKeyImportServiceTest(t)

	group := createTestGroupForImport(t, db, "test-group-small")

	// Create 10 test keys
	keys := make([]string, 10)
	for i := 0; i < 10; i++ {
		keys[i] = fmt.Sprintf("sk-test-key-%d", i)
	}
	keysText := strings.Join(keys, "\n")

	// Start import task
	status, err := importSvc.StartImportTask(&group, keysText)
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.True(t, status.IsRunning)
	assert.Equal(t, group.Name, status.GroupName)

	// Wait for task to complete (with timeout)
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var finalStatus *TaskStatus
	for {
		select {
		case <-timeout:
			t.Fatal("Task did not complete within timeout")
		case <-ticker.C:
			status, err := taskSvc.GetTaskStatus()
			require.NoError(t, err)
			finalStatus = status
			if !finalStatus.IsRunning {
				goto TaskCompleted
			}
		}
	}

TaskCompleted:
	assert.False(t, finalStatus.IsRunning)
	assert.Empty(t, finalStatus.Error)

	// Verify keys were imported
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(10), count)
}

// TestKeyImportService_LargeBatch tests importing a large batch of keys (simulating file import)
func TestKeyImportService_LargeBatch(t *testing.T) {
	t.Parallel()
	db, importSvc, taskSvc, _ := setupKeyImportServiceTest(t)

	group := createTestGroupForImport(t, db, "test-group-large")

	// Create 1000 test keys (simulating a file with many keys)
	keys := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		keys[i] = fmt.Sprintf("sk-test-large-key-%d", i)
	}
	keysText := strings.Join(keys, "\n")

	// Start import task
	status, err := importSvc.StartImportTask(&group, keysText)
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.True(t, status.IsRunning)

	// Wait for task to complete (with longer timeout for large batch)
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var finalStatus *TaskStatus
	for {
		select {
		case <-timeout:
			t.Fatal("Task did not complete within timeout")
		case <-ticker.C:
			status, err := taskSvc.GetTaskStatus()
			require.NoError(t, err)
			finalStatus = status
			if !finalStatus.IsRunning {
				goto TaskCompleted
			}
		}
	}

TaskCompleted:
	assert.False(t, finalStatus.IsRunning)
	assert.Empty(t, finalStatus.Error)

	// Verify all keys were imported
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(1000), count)
}

// TestKeyImportService_DuplicateKeys tests that duplicate keys are ignored
func TestKeyImportService_DuplicateKeys(t *testing.T) {
	t.Parallel()
	db, importSvc, taskSvc, _ := setupKeyImportServiceTest(t)

	group := createTestGroupForImport(t, db, "test-group-dup")

	// Create keys with duplicates
	keysText := "sk-test-1\nsk-test-2\nsk-test-1\nsk-test-3\nsk-test-2"

	// Start import task
	status, err := importSvc.StartImportTask(&group, keysText)
	require.NoError(t, err)
	assert.NotNil(t, status)

	// Wait for task to complete
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var finalStatus *TaskStatus
	for {
		select {
		case <-timeout:
			t.Fatal("Task did not complete within timeout")
		case <-ticker.C:
			status, err := taskSvc.GetTaskStatus()
			require.NoError(t, err)
			finalStatus = status
			if !finalStatus.IsRunning {
				goto TaskCompleted
			}
		}
	}

TaskCompleted:
	assert.False(t, finalStatus.IsRunning)
	assert.Empty(t, finalStatus.Error)

	// Verify only unique keys were imported (3 unique keys)
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

// TestKeyImportService_EmptyInput tests handling of empty input
func TestKeyImportService_EmptyInput(t *testing.T) {
	t.Parallel()
	_, importSvc, _, _ := setupKeyImportServiceTest(t)

	group := models.Group{
		ID:   1,
		Name: "test-group-empty",
	}

	// Test with empty string
	_, err := importSvc.StartImportTask(&group, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no valid keys found")

	// Test with only whitespace
	_, err = importSvc.StartImportTask(&group, "   \n\n   ")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no valid keys found")
}

// TestKeyImportService_ConcurrentImport tests that only one import can run at a time
func TestKeyImportService_ConcurrentImport(t *testing.T) {
	t.Parallel()
	db, importSvc, _, _ := setupKeyImportServiceTest(t)

	group := createTestGroupForImport(t, db, "test-group-concurrent")

	// Start first import
	keysText1 := "sk-test-1\nsk-test-2\nsk-test-3"
	status1, err := importSvc.StartImportTask(&group, keysText1)
	require.NoError(t, err)
	assert.True(t, status1.IsRunning)

	// Try to start second import (should fail)
	keysText2 := "sk-test-4\nsk-test-5"
	_, err = importSvc.StartImportTask(&group, keysText2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task is already running")
}

// TestKeyImportService_MixedFormat tests importing keys with mixed formats
func TestKeyImportService_MixedFormat(t *testing.T) {
	t.Parallel()
	db, importSvc, taskSvc, _ := setupKeyImportServiceTest(t)

	group := createTestGroupForImport(t, db, "test-group-mixed")

	// Keys with various formats: newlines, spaces, empty lines
	keysText := `
sk-test-1

sk-test-2
  sk-test-3

sk-test-4
`

	// Start import task
	status, err := importSvc.StartImportTask(&group, keysText)
	require.NoError(t, err)
	assert.NotNil(t, status)

	// Wait for task to complete
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var finalStatus *TaskStatus
	for {
		select {
		case <-timeout:
			t.Fatal("Task did not complete within timeout")
		case <-ticker.C:
			status, err := taskSvc.GetTaskStatus()
			require.NoError(t, err)
			finalStatus = status
			if !finalStatus.IsRunning {
				goto TaskCompleted
			}
		}
	}

TaskCompleted:
	assert.False(t, finalStatus.IsRunning)
	assert.Empty(t, finalStatus.Error)

	// Verify 4 keys were imported (empty lines and spaces should be handled)
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(4), count)
}
