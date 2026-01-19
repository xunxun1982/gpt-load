package keypool

import (
	"context"
	"encoding/json"
	"fmt"
	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestCronChecker(tb testing.TB) (*CronChecker, *gorm.DB, *KeyValidator) {
	tb.Helper()
	skipIfNoSQLite(tb)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(tb, err)

	// Limit SQLite connections to avoid separate in-memory databases
	// NewProvider spawns background workers that need to share the same database
	sqlDB, err := db.DB()
	require.NoError(tb, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	err = db.AutoMigrate(&models.APIKey{}, &models.Group{})
	require.NoError(tb, err)

	memStore := store.NewMemoryStore()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(tb, err)

	settingsManager := config.NewSystemSettingsManager()

	provider := NewProvider(db, memStore, settingsManager, encSvc)
	tb.Cleanup(func() {
		provider.Stop()
	})

	httpClientManager := httpclient.NewHTTPClientManager()
	channelFactory := channel.NewFactory(settingsManager, httpClientManager)

	validator := NewKeyValidator(KeyValidatorParams{
		DB:              db,
		ChannelFactory:  channelFactory,
		SettingsManager: settingsManager,
		KeypoolProvider: provider,
		EncryptionSvc:   encSvc,
	})

	cronChecker := NewCronChecker(db, settingsManager, validator, encSvc, memStore)

	return cronChecker, db, validator
}

func TestNewCronChecker(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)
	assert.NotNil(t, cronChecker)
	assert.NotNil(t, cronChecker.DB)
	assert.NotNil(t, cronChecker.SettingsManager)
	assert.NotNil(t, cronChecker.Validator)
	assert.NotNil(t, cronChecker.stopChan)
}

func TestCronCheckerStartStop(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)

	// Start cron checker
	cronChecker.Start()

	// Wait a bit to ensure it's running
	time.Sleep(100 * time.Millisecond)

	// Stop with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		cronChecker.Stop(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("CronChecker.Stop() timed out")
	}
}

func TestCronCheckerStopTimeout(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)

	// Start cron checker
	cronChecker.Start()

	// Stop with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Should handle timeout gracefully
	cronChecker.Stop(ctx)
}

func TestValidateGroupKeys_NoInvalidKeys(t *testing.T) {
	cronChecker, db, _ := setupTestCronChecker(t)

	// Create test group
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Create only active keys
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	key := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-test",
		KeyHash:  encSvc.Hash("sk-test"),
		Status:   models.KeyStatusActive,
	}
	require.NoError(t, db.Create(key).Error)

	// Validate group keys (should complete quickly with no invalid keys)
	groupsToUpdate := make(map[uint]struct{})
	var mu sync.Mutex

	cronChecker.validateGroupKeys(group, &mu, groupsToUpdate)

	// Group should be marked for update
	assert.Contains(t, groupsToUpdate, group.ID)
}

func TestValidateGroupKeys_DisabledGroup(t *testing.T) {
	cronChecker, db, _ := setupTestCronChecker(t)

	// Create disabled group
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		Enabled:     false,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Create invalid key
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	key := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-test",
		KeyHash:  encSvc.Hash("sk-test"),
		Status:   models.KeyStatusInvalid,
	}
	require.NoError(t, db.Create(key).Error)

	// Note: validateGroupKeys itself doesn't check if group is enabled
	// The filtering happens in ValidateAllGroups before calling this function
	// This test verifies that validateGroupKeys works correctly when called directly
	groupsToUpdate := make(map[uint]struct{})
	var mu sync.Mutex

	cronChecker.validateGroupKeys(group, &mu, groupsToUpdate)

	// Group will be marked for update even if disabled (when called directly)
	// In production, disabled groups are filtered out before calling validateGroupKeys
	assert.Contains(t, groupsToUpdate, group.ID)
}

func TestBatchUpdateLastValidatedAt_EmptyMap(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)

	// Should handle empty map gracefully
	groupsToUpdate := make(map[uint]struct{})
	cronChecker.batchUpdateLastValidatedAt(groupsToUpdate)
}

func TestBatchUpdateLastValidatedAt_SingleGroup(t *testing.T) {
	cronChecker, db, _ := setupTestCronChecker(t)

	// Create test group
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Update last validated at
	groupsToUpdate := map[uint]struct{}{
		group.ID: {},
	}
	cronChecker.batchUpdateLastValidatedAt(groupsToUpdate)

	// Verify last_validated_at was updated
	var updatedGroup models.Group
	require.NoError(t, db.First(&updatedGroup, group.ID).Error)
	assert.NotNil(t, updatedGroup.LastValidatedAt)
}

func TestBatchUpdateLastValidatedAt_MultipleGroups(t *testing.T) {
	cronChecker, db, _ := setupTestCronChecker(t)

	// Create multiple test groups
	var groupIDs []uint
	for i := 0; i < 5; i++ {
		group := &models.Group{
			Name:        fmt.Sprintf("test-group-%d", i),
			ChannelType: "openai",
			Enabled:     true,
			Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
		}
		require.NoError(t, db.Create(group).Error)
		groupIDs = append(groupIDs, group.ID)
	}

	// Update last validated at for all groups
	groupsToUpdate := make(map[uint]struct{})
	for _, id := range groupIDs {
		groupsToUpdate[id] = struct{}{}
	}
	cronChecker.batchUpdateLastValidatedAt(groupsToUpdate)

	// Verify all groups were updated
	for _, id := range groupIDs {
		var updatedGroup models.Group
		require.NoError(t, db.First(&updatedGroup, id).Error)
		assert.NotNil(t, updatedGroup.LastValidatedAt)
	}
}

func TestBatchUpdateLastValidatedAt_LargeBatch(t *testing.T) {
	cronChecker, db, _ := setupTestCronChecker(t)

	// Create many groups to test chunking (SQLite has 999 parameter limit)
	var groupIDs []uint
	for i := 0; i < 1500; i++ {
		group := &models.Group{
			Name:        fmt.Sprintf("test-group-%d", i),
			ChannelType: "openai",
			Enabled:     true,
			Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
		}
		require.NoError(t, db.Create(group).Error)
		groupIDs = append(groupIDs, group.ID)
	}

	// Update last validated at for all groups
	groupsToUpdate := make(map[uint]struct{})
	for _, id := range groupIDs {
		groupsToUpdate[id] = struct{}{}
	}
	cronChecker.batchUpdateLastValidatedAt(groupsToUpdate)

	// Verify all groups were updated
	var count int64
	db.Model(&models.Group{}).Where("last_validated_at IS NOT NULL").Count(&count)
	assert.Equal(t, int64(len(groupIDs)), count)
}

func TestIsBusy_NoTask(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)

	// Should return false when no task is running
	assert.False(t, cronChecker.isBusy())
}

func TestIsBusy_ImportRunning(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)

	// Set import task as running
	taskData := map[string]any{
		"task_type":  "KEY_IMPORT",
		"is_running": true,
	}
	taskDataBytes, err := json.Marshal(taskData)
	require.NoError(t, err)
	cronChecker.Store.Set("global_task", taskDataBytes, 0)

	// Should return true when import is running
	assert.True(t, cronChecker.isBusy())
}

func TestIsBusy_DeleteRunning(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)

	// Set delete task as running
	taskData := map[string]any{
		"task_type":  "KEY_DELETE",
		"is_running": true,
	}
	taskDataBytes, err := json.Marshal(taskData)
	require.NoError(t, err)
	cronChecker.Store.Set("global_task", taskDataBytes, 0)

	// Should return true when delete is running
	assert.True(t, cronChecker.isBusy())
}

func TestIsBusy_OtherTaskRunning(t *testing.T) {
	cronChecker, _, _ := setupTestCronChecker(t)

	// Set other task as running
	taskData := map[string]any{
		"task_type":  "OTHER_TASK",
		"is_running": true,
	}
	taskDataBytes, err := json.Marshal(taskData)
	require.NoError(t, err)
	cronChecker.Store.Set("global_task", taskDataBytes, 0)

	// Should return false for non-import/delete tasks
	assert.False(t, cronChecker.isBusy())
}

// Benchmark tests for PGO optimization
func BenchmarkValidateGroupKeys(b *testing.B) {
	cronChecker, db, _ := setupTestCronChecker(b)

	// Create test group
	group := &models.Group{
		Name:        "bench-group",
		ChannelType: "openai",
		Enabled:     true,
	}
	db.Create(group)

	// Create invalid keys
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: "sk-bench",
			KeyHash:  encSvc.Hash("sk-bench"),
			Status:   models.KeyStatusInvalid,
		}
		db.Create(key)
	}

	groupsToUpdate := make(map[uint]struct{})
	var mu sync.Mutex

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cronChecker.validateGroupKeys(group, &mu, groupsToUpdate)
	}
}

func BenchmarkBatchUpdateLastValidatedAt(b *testing.B) {
	cronChecker, db, _ := setupTestCronChecker(b)

	// Create test groups
	var groupIDs []uint
	for i := 0; i < 100; i++ {
		group := &models.Group{
			Name:        "bench-group",
			ChannelType: "openai",
			Enabled:     true,
		}
		db.Create(group)
		groupIDs = append(groupIDs, group.ID)
	}

	groupsToUpdate := make(map[uint]struct{})
	for _, id := range groupIDs {
		groupsToUpdate[id] = struct{}{}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cronChecker.batchUpdateLastValidatedAt(groupsToUpdate)
	}
}
