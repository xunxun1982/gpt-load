package services

import (
	"testing"
	"time"

	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func setupKeyDeleteServiceTest(t *testing.T) (*gorm.DB, *KeyDeleteService) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		PrepareStmt: false,
	})
	require.NoError(t, err)

	// Limit SQLite connections to avoid separate in-memory databases
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	err = db.AutoMigrate(&models.Group{}, &models.APIKey{})
	require.NoError(t, err)

	settingsManager := config.NewSystemSettingsManager()
	memStore := store.NewMemoryStore()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	keyProvider := keypool.NewProvider(db, memStore, settingsManager, encSvc)
	keyValidator := keypool.NewKeyValidator(keypool.KeyValidatorParams{})
	keyService := NewKeyService(db, keyProvider, keyValidator, encSvc)

	taskService := NewTaskService(memStore)
	deleteService := NewKeyDeleteService(taskService, keyService)

	return db, deleteService
}

func createTestGroupWithKeys(t *testing.T, db *gorm.DB, encSvc encryption.Service, groupName string, keyCount int, status string) *models.Group {
	t.Helper()

	group := &models.Group{
		Name:        groupName,
		TestModel:   "test-model",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   datatypes.JSON([]byte("[]")), // Required field
	}
	require.NoError(t, db.Create(group).Error)

	for i := 0; i < keyCount; i++ {
		keyValue := "sk-test-key-" + groupName + "-" + string(rune('a'+i))
		encrypted, err := encSvc.Encrypt(keyValue)
		require.NoError(t, err)

		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: encrypted,
			KeyHash:  encSvc.Hash(keyValue),
			Status:   status,
		}
		require.NoError(t, db.Create(key).Error)
	}

	return group
}

// TestStartDeleteTask tests starting a delete task
func TestStartDeleteTask(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 5, models.KeyStatusActive)

	keysText := "sk-test-key-test-group-a\nsk-test-key-test-group-b\nsk-test-key-test-group-c"

	taskStatus, err := svc.StartDeleteTask(group, keysText)
	require.NoError(t, err)
	assert.NotNil(t, taskStatus)
	assert.Equal(t, TaskTypeKeyDelete, taskStatus.TaskType)
	assert.True(t, taskStatus.IsRunning)
	assert.Equal(t, 3, taskStatus.Total)

	// Wait for task to complete
	// Note: Fixed sleep is appropriate for async task testing. The task runs in a goroutine
	// with known execution time. Polling would add complexity without improving reliability.
	time.Sleep(100 * time.Millisecond)

	// Verify keys were deleted
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(2), count) // 5 - 3 = 2 remaining
}

// TestStartDeleteAllGroupKeys tests deleting all keys in a group
func TestStartDeleteAllGroupKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 10, models.KeyStatusActive)

	taskStatus, err := svc.StartDeleteAllGroupKeys(group, 10)
	require.NoError(t, err)
	assert.NotNil(t, taskStatus)
	assert.Equal(t, TaskTypeKeyDelete, taskStatus.TaskType)
	assert.True(t, taskStatus.IsRunning)
	assert.Equal(t, 10, taskStatus.Total)

	// Wait for task to complete
	time.Sleep(200 * time.Millisecond)

	// Verify all keys were deleted
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestStartDeleteInvalidGroupKeys tests deleting invalid keys in a group
func TestStartDeleteInvalidGroupKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group-delete-invalid", 5, models.KeyStatusActive)
	// Create invalid keys in the same group
	for i := 0; i < 3; i++ {
		keyValue := "sk-test-key-test-group-delete-invalid-invalid-" + string(rune('a'+i))
		encrypted, err := encSvc.Encrypt(keyValue)
		require.NoError(t, err)

		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: encrypted,
			KeyHash:  encSvc.Hash(keyValue),
			Status:   models.KeyStatusInvalid,
		}
		require.NoError(t, db.Create(key).Error)
	}

	taskStatus, err := svc.StartDeleteInvalidGroupKeys(group, 3)
	require.NoError(t, err)
	assert.NotNil(t, taskStatus)
	assert.Equal(t, TaskTypeKeyDelete, taskStatus.TaskType)
	assert.True(t, taskStatus.IsRunning)
	assert.Equal(t, 3, taskStatus.Total)

	// Wait for task to complete
	time.Sleep(200 * time.Millisecond)

	// Verify only invalid keys were deleted
	var activeCount, invalidCount int64
	err = db.Model(&models.APIKey{}).Where("group_id = ? AND status = ?", group.ID, models.KeyStatusActive).Count(&activeCount).Error
	require.NoError(t, err)
	assert.Equal(t, int64(5), activeCount)

	err = db.Model(&models.APIKey{}).Where("group_id = ? AND status = ?", group.ID, models.KeyStatusInvalid).Count(&invalidCount).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), invalidCount)
}

// TestStartRestoreInvalidGroupKeys tests restoring invalid keys in a group
func TestStartRestoreInvalidGroupKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group-restore", 5, models.KeyStatusActive)
	// Create invalid keys in the same group (don't create a new group)
	for i := 0; i < 3; i++ {
		keyValue := "sk-test-key-test-group-restore-invalid-" + string(rune('a'+i))
		encrypted, err := encSvc.Encrypt(keyValue)
		require.NoError(t, err)

		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: encrypted,
			KeyHash:  encSvc.Hash(keyValue),
			Status:   models.KeyStatusInvalid,
		}
		require.NoError(t, db.Create(key).Error)
	}

	taskStatus, err := svc.StartRestoreInvalidGroupKeys(group, 3)
	require.NoError(t, err)
	assert.NotNil(t, taskStatus)
	assert.Equal(t, TaskTypeKeyRestore, taskStatus.TaskType)
	assert.True(t, taskStatus.IsRunning)
	assert.Equal(t, 3, taskStatus.Total)

	// Wait for task to complete
	time.Sleep(200 * time.Millisecond)

	// Verify invalid keys were restored to active
	var activeCount, invalidCount int64
	err = db.Model(&models.APIKey{}).Where("group_id = ? AND status = ?", group.ID, models.KeyStatusActive).Count(&activeCount).Error
	require.NoError(t, err)
	assert.Equal(t, int64(8), activeCount) // 5 + 3 restored

	err = db.Model(&models.APIKey{}).Where("group_id = ? AND status = ?", group.ID, models.KeyStatusInvalid).Count(&invalidCount).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), invalidCount)
}

// TestStartDeleteTask_EmptyKeys tests starting a delete task with empty keys
func TestStartDeleteTask_EmptyKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 5, models.KeyStatusActive)

	keysText := ""

	_, err = svc.StartDeleteTask(group, keysText)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no valid keys found")
}

// TestStartDeleteTask_TaskAlreadyRunning tests starting a task when one is already running
func TestStartDeleteTask_TaskAlreadyRunning(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 10, models.KeyStatusActive)

	keysText := "sk-test-key-test-group-a\nsk-test-key-test-group-b"

	// Start first task
	_, err = svc.StartDeleteTask(group, keysText)
	require.NoError(t, err)

	// Try to start second task immediately
	_, err = svc.StartDeleteTask(group, keysText)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// Wait for first task to complete
	time.Sleep(200 * time.Millisecond)
}

// TestProcessAndDeleteKeys tests the core delete logic
func TestProcessAndDeleteKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 5, models.KeyStatusActive)

	keys := []string{
		"sk-test-key-test-group-a",
		"sk-test-key-test-group-b",
		"sk-test-key-test-group-c",
		"sk-nonexistent-key", // This key doesn't exist
	}

	deletedCount, ignoredCount, err := svc.processAndDeleteKeys(group.ID, keys, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, deletedCount)
	assert.Equal(t, 1, ignoredCount)

	// Verify keys were deleted
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(2), count) // 5 - 3 = 2 remaining
}

// TestDeleteService_CacheInvalidation tests cache invalidation after deletion
func TestDeleteService_CacheInvalidation(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 5, models.KeyStatusActive)

	// Set up cache invalidation callback
	cacheInvalidated := false
	svc.KeyService.CacheInvalidationCallback = func(groupID uint) {
		cacheInvalidated = true
		assert.Equal(t, group.ID, groupID)
	}

	keys := []string{"sk-test-key-test-group-a"}

	_, _, err = svc.processAndDeleteKeys(group.ID, keys, nil)
	require.NoError(t, err)
	assert.True(t, cacheInvalidated, "Cache should be invalidated after deletion")
}

// TestDeleteService_ProgressCallback tests progress tracking during deletion
func TestDeleteService_ProgressCallback(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 5, models.KeyStatusActive)

	keys := []string{
		"sk-test-key-test-group-a",
		"sk-test-key-test-group-b",
		"sk-test-key-test-group-c",
	}

	progressCalled := false
	progressCallback := func(processed int) {
		progressCalled = true
		assert.Greater(t, processed, 0)
	}

	_, _, err = svc.processAndDeleteKeys(group.ID, keys, progressCallback)
	require.NoError(t, err)
	assert.True(t, progressCalled, "Progress callback should be called")
}

// TestRunDeleteAllGroupKeys tests the async delete all keys method
func TestRunDeleteAllGroupKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 10, models.KeyStatusActive)

	// Start task manually
	_, err = svc.TaskService.StartTask(TaskTypeKeyDelete, group.Name, 10)
	require.NoError(t, err)

	// Run delete in goroutine
	go svc.runDeleteAllGroupKeys(group, 10)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	// Verify all keys deleted
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Verify task completed
	status, err := svc.TaskService.GetTaskStatus()
	require.NoError(t, err)
	assert.False(t, status.IsRunning)
	assert.NotNil(t, status.FinishedAt)
}

// TestRunRestoreInvalidGroupKeys tests the async restore invalid keys method
func TestRunRestoreInvalidGroupKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group-run-restore", 5, models.KeyStatusActive)
	// Create invalid keys in the same group
	for i := 0; i < 3; i++ {
		keyValue := "sk-test-key-test-group-run-restore-invalid-" + string(rune('a'+i))
		encrypted, err := encSvc.Encrypt(keyValue)
		require.NoError(t, err)

		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: encrypted,
			KeyHash:  encSvc.Hash(keyValue),
			Status:   models.KeyStatusInvalid,
		}
		require.NoError(t, db.Create(key).Error)
	}

	// Start task manually
	_, err = svc.TaskService.StartTask(TaskTypeKeyRestore, group.Name, 3)
	require.NoError(t, err)

	// Run restore in goroutine
	go svc.runRestoreInvalidGroupKeys(group, 3)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	// Verify keys restored
	var activeCount int64
	err = db.Model(&models.APIKey{}).Where("group_id = ? AND status = ?", group.ID, models.KeyStatusActive).Count(&activeCount).Error
	require.NoError(t, err)
	assert.Equal(t, int64(8), activeCount)

	// Verify task completed
	status, err := svc.TaskService.GetTaskStatus()
	require.NoError(t, err)
	assert.False(t, status.IsRunning)
	assert.NotNil(t, status.FinishedAt)
}
