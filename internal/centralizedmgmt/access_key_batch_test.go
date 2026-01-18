package centralizedmgmt

import (
	"context"
	"testing"
	"time"

	"gpt-load/internal/encryption"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestBatchDeleteAccessKeys tests batch deletion of access keys
func TestBatchDeleteAccessKeys(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	err = db.AutoMigrate(&HubAccessKey{})
	require.NoError(t, err)

	encSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)
	svc := NewHubAccessKeyService(db, encSvc)
	ctx := context.Background()

	// Create test keys
	key1, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-1",
		Enabled: true,
	})
	require.NoError(t, err)

	key2, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-2",
		Enabled: true,
	})
	require.NoError(t, err)

	key3, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-3",
		Enabled: true,
	})
	require.NoError(t, err)

	// Batch delete key1 and key2
	count, err := svc.BatchDeleteAccessKeys(ctx, []uint{key1.ID, key2.ID})
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Verify key1 and key2 are deleted
	_, err = svc.GetAccessKey(ctx, key1.ID)
	assert.Error(t, err)

	_, err = svc.GetAccessKey(ctx, key2.ID)
	assert.Error(t, err)

	// Verify key3 still exists
	key3Result, err := svc.GetAccessKey(ctx, key3.ID)
	require.NoError(t, err)
	assert.Equal(t, key3.ID, key3Result.ID)
}

// TestBatchUpdateAccessKeysEnabled tests batch enable/disable of access keys
func TestBatchUpdateAccessKeysEnabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	err = db.AutoMigrate(&HubAccessKey{})
	require.NoError(t, err)

	encSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)
	svc := NewHubAccessKeyService(db, encSvc)
	ctx := context.Background()

	// Create test keys (all enabled)
	key1, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-1",
		Enabled: true,
	})
	require.NoError(t, err)

	key2, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-2",
		Enabled: true,
	})
	require.NoError(t, err)

	key3, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-3",
		Enabled: true,
	})
	require.NoError(t, err)

	// Batch disable key1 and key2
	count, err := svc.BatchUpdateAccessKeysEnabled(ctx, []uint{key1.ID, key2.ID}, false)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Verify key1 and key2 are disabled
	key1Result, err := svc.GetAccessKey(ctx, key1.ID)
	require.NoError(t, err)
	assert.False(t, key1Result.Enabled)

	key2Result, err := svc.GetAccessKey(ctx, key2.ID)
	require.NoError(t, err)
	assert.False(t, key2Result.Enabled)

	// Verify key3 is still enabled
	key3Result, err := svc.GetAccessKey(ctx, key3.ID)
	require.NoError(t, err)
	assert.True(t, key3Result.Enabled)

	// Batch enable key1 and key2 again
	count, err = svc.BatchUpdateAccessKeysEnabled(ctx, []uint{key1.ID, key2.ID}, true)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Verify key1 and key2 are enabled
	key1Result, err = svc.GetAccessKey(ctx, key1.ID)
	require.NoError(t, err)
	assert.True(t, key1Result.Enabled)

	key2Result, err = svc.GetAccessKey(ctx, key2.ID)
	require.NoError(t, err)
	assert.True(t, key2Result.Enabled)
}

// TestRecordKeyUsage tests recording key usage statistics
func TestRecordKeyUsage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	err = db.AutoMigrate(&HubAccessKey{})
	require.NoError(t, err)

	encSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)
	svc := NewHubAccessKeyService(db, encSvc)
	ctx := context.Background()

	// Create test key
	key, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key",
		Enabled: true,
	})
	require.NoError(t, err)

	// Initial usage count should be 0
	assert.Equal(t, int64(0), key.UsageCount)
	assert.Nil(t, key.LastUsedAt)

	// Record usage
	err = svc.RecordKeyUsage(ctx, key.ID)
	require.NoError(t, err)

	// Wait a bit for async update
	time.Sleep(100 * time.Millisecond)

	// Get updated stats
	stats, err := svc.GetAccessKeyUsageStats(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stats.UsageCount)
	assert.NotNil(t, stats.LastUsedAt)

	// Record usage again
	err = svc.RecordKeyUsage(ctx, key.ID)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Get updated stats
	stats, err = svc.GetAccessKeyUsageStats(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), stats.UsageCount)
	assert.NotNil(t, stats.LastUsedAt)
}

// TestGetAccessKeyUsageStatsBatch tests batch retrieval of usage statistics
func TestGetAccessKeyUsageStatsBatch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	err = db.AutoMigrate(&HubAccessKey{})
	require.NoError(t, err)

	encSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)
	svc := NewHubAccessKeyService(db, encSvc)
	ctx := context.Background()

	// Create test keys
	key1, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-1",
		Enabled: true,
	})
	require.NoError(t, err)

	key2, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:    "test-key-2",
		Enabled: true,
	})
	require.NoError(t, err)

	// Record usage for key1
	err = svc.RecordKeyUsage(ctx, key1.ID)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Get batch stats
	stats, err := svc.GetAccessKeyUsageStatsBatch(ctx, []uint{key1.ID, key2.ID})
	require.NoError(t, err)
	assert.Len(t, stats, 2)

	// Find key1 stats
	var key1Stats *HubAccessKeyDTO
	for i := range stats {
		if stats[i].ID == key1.ID {
			key1Stats = &stats[i]
			break
		}
	}
	require.NotNil(t, key1Stats)
	assert.Equal(t, int64(1), key1Stats.UsageCount)
	assert.NotNil(t, key1Stats.LastUsedAt)

	// Find key2 stats
	var key2Stats *HubAccessKeyDTO
	for i := range stats {
		if stats[i].ID == key2.ID {
			key2Stats = &stats[i]
			break
		}
	}
	require.NotNil(t, key2Stats)
	assert.Equal(t, int64(0), key2Stats.UsageCount)
	assert.Nil(t, key2Stats.LastUsedAt)
}

// TestBatchOperationsEmptyIDs tests batch operations with empty ID list
func TestBatchOperationsEmptyIDs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	err = db.AutoMigrate(&HubAccessKey{})
	require.NoError(t, err)

	encSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)
	svc := NewHubAccessKeyService(db, encSvc)
	ctx := context.Background()

	// Test batch delete with empty IDs
	_, err = svc.BatchDeleteAccessKeys(ctx, []uint{})
	assert.Error(t, err)

	// Test batch update with empty IDs
	_, err = svc.BatchUpdateAccessKeysEnabled(ctx, []uint{}, true)
	assert.Error(t, err)

	// Test batch stats with empty IDs
	stats, err := svc.GetAccessKeyUsageStatsBatch(ctx, []uint{})
	require.NoError(t, err)
	assert.Empty(t, stats)
}

// TestBatchOperationsNonExistentIDs tests batch operations with non-existent IDs
func TestBatchOperationsNonExistentIDs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	err = db.AutoMigrate(&HubAccessKey{})
	require.NoError(t, err)

	encSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)
	svc := NewHubAccessKeyService(db, encSvc)
	ctx := context.Background()

	// Test batch delete with non-existent IDs
	count, err := svc.BatchDeleteAccessKeys(ctx, []uint{999, 1000})
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Test batch update with non-existent IDs
	count, err = svc.BatchUpdateAccessKeysEnabled(ctx, []uint{999, 1000}, false)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Test batch stats with non-existent IDs
	stats, err := svc.GetAccessKeyUsageStatsBatch(ctx, []uint{999, 1000})
	require.NoError(t, err)
	assert.Empty(t, stats)
}
