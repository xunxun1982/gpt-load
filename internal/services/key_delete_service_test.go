package services

import (
	"fmt"
	"sync/atomic"
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
	// Stop the provider's worker pool in cleanup to prevent goroutine leaks
	// This is important because all tests use t.Parallel()
	t.Cleanup(func() {
		keyProvider.Stop()
	})

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

	// Use numeric suffix for key generation to support any number of keys
	for i := 0; i < keyCount; i++ {
		keyValue := fmt.Sprintf("sk-test-key-%s-%d", groupName, i)
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

	keysText := "sk-test-key-test-group-0\nsk-test-key-test-group-1\nsk-test-key-test-group-2"

	taskStatus, err := svc.StartDeleteTask(group, keysText)
	require.NoError(t, err)
	assert.NotNil(t, taskStatus)
	assert.Equal(t, TaskTypeKeyDelete, taskStatus.TaskType)
	assert.True(t, taskStatus.IsRunning)
	assert.Equal(t, 3, taskStatus.Total)

	// Wait for task to complete using Eventually for more robust synchronization
	require.Eventually(t, func() bool {
		var count int64
		err := db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
		return err == nil && count == 2 // 5 - 3 = 2 remaining
	}, 2*time.Second, 10*time.Millisecond, "keys should be deleted")

	// Verify final count
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

	// Wait for task to complete using Eventually for more robust synchronization
	require.Eventually(t, func() bool {
		var count int64
		err := db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
		return err == nil && count == 0
	}, 2*time.Second, 10*time.Millisecond, "all keys should be deleted")
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
		keyValue := fmt.Sprintf("sk-test-key-test-group-delete-invalid-invalid-%d", i)
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

	// Wait for task to complete using Eventually for more robust synchronization
	require.Eventually(t, func() bool {
		var count int64
		err := db.Model(&models.APIKey{}).
			Where("group_id = ? AND status = ?", group.ID, models.KeyStatusInvalid).
			Count(&count).Error
		return err == nil && count == 0
	}, 2*time.Second, 10*time.Millisecond, "invalid keys should be deleted")

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
		keyValue := fmt.Sprintf("sk-test-key-test-group-restore-invalid-%d", i)
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

	// Wait for task to complete using Eventually for more robust synchronization
	require.Eventually(t, func() bool {
		var count int64
		err := db.Model(&models.APIKey{}).
			Where("group_id = ? AND status = ?", group.ID, models.KeyStatusInvalid).
			Count(&count).Error
		return err == nil && count == 0
	}, 2*time.Second, 10*time.Millisecond, "invalid keys should be restored to active")

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

	// Use actual key names created by createTestGroupWithKeys
	keysText := "sk-test-key-test-group-0\nsk-test-key-test-group-1"

	// Start first task
	_, err = svc.StartDeleteTask(group, keysText)
	require.NoError(t, err)

	// Ensure task is running before asserting "already running"
	// This prevents flakiness if the task completes very quickly
	require.Eventually(t, func() bool {
		status, err := svc.TaskService.GetTaskStatus()
		return err == nil && status.IsRunning
	}, 1*time.Second, 10*time.Millisecond, "task should be running")

	// Try to start second task while first is still running
	_, err = svc.StartDeleteTask(group, keysText)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// Wait for first task to complete using Eventually
	require.Eventually(t, func() bool {
		status, err := svc.TaskService.GetTaskStatus()
		return err == nil && !status.IsRunning
	}, 2*time.Second, 10*time.Millisecond, "first task should complete")

	// Start a new task after the previous one completes
	_, err = svc.StartDeleteTask(group, "sk-test-key-test-group-2")
	require.NoError(t, err)

	// Ensure the second task finishes before test cleanup closes the DB
	require.Eventually(t, func() bool {
		status, err := svc.TaskService.GetTaskStatus()
		return err == nil && !status.IsRunning
	}, 2*time.Second, 10*time.Millisecond, "second task should complete")
}

// TestProcessAndDeleteKeys tests the core delete logic
func TestProcessAndDeleteKeys(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 5, models.KeyStatusActive)

	keys := []string{
		"sk-test-key-test-group-0",
		"sk-test-key-test-group-1",
		"sk-test-key-test-group-2",
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
	var cacheInvalidated atomic.Bool
	svc.KeyService.CacheInvalidationCallback = func(groupID uint) {
		cacheInvalidated.Store(true)
		assert.Equal(t, group.ID, groupID)
	}

	keys := []string{"sk-test-key-test-group-0"}

	_, _, err = svc.processAndDeleteKeys(group.ID, keys, nil)
	require.NoError(t, err)
	assert.True(t, cacheInvalidated.Load(), "Cache should be invalidated after deletion")
}

// TestDeleteService_ProgressCallback tests progress tracking during deletion
func TestDeleteService_ProgressCallback(t *testing.T) {
	t.Parallel()

	db, svc := setupKeyDeleteServiceTest(t)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	group := createTestGroupWithKeys(t, db, encSvc, "test-group", 5, models.KeyStatusActive)

	// Use actual key names created by createTestGroupWithKeys
	keys := []string{
		"sk-test-key-test-group-0",
		"sk-test-key-test-group-1",
		"sk-test-key-test-group-2",
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
	go svc.runDeleteAllGroupKeys(group)

	// Wait for completion using Eventually for more robust synchronization
	require.Eventually(t, func() bool {
		var count int64
		err := db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
		return err == nil && count == 0
	}, 2*time.Second, 10*time.Millisecond, "all keys should be deleted")

	// Verify task completed
	require.Eventually(t, func() bool {
		status, err := svc.TaskService.GetTaskStatus()
		return err == nil && !status.IsRunning && status.FinishedAt != nil
	}, 2*time.Second, 10*time.Millisecond, "task should complete")
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
		keyValue := fmt.Sprintf("sk-test-key-test-group-run-restore-invalid-%d", i)
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
	go svc.runRestoreInvalidGroupKeys(group)

	// Wait for completion using Eventually for more robust synchronization
	require.Eventually(t, func() bool {
		var activeCount int64
		err := db.Model(&models.APIKey{}).
			Where("group_id = ? AND status = ?", group.ID, models.KeyStatusActive).
			Count(&activeCount).Error
		return err == nil && activeCount == 8 // 5 original + 3 restored
	}, 2*time.Second, 10*time.Millisecond, "invalid keys should be restored to active")

	// Verify task completed
	require.Eventually(t, func() bool {
		status, err := svc.TaskService.GetTaskStatus()
		return err == nil && !status.IsRunning && status.FinishedAt != nil
	}, 2*time.Second, 10*time.Millisecond, "task should complete")
}
