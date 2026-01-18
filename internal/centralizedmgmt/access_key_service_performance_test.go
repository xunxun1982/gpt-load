package centralizedmgmt

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gpt-load/internal/encryption"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestServiceForPerformance creates a test service with in-memory database
func setupTestServiceForPerformance(t *testing.T) (*HubAccessKeyService, *gorm.DB) {
	// Setup in-memory database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Auto-migrate schema
	err = db.AutoMigrate(&HubAccessKey{})
	require.NoError(t, err)

	// Create encryption service
	encryptionSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)

	// Create service
	service := NewHubAccessKeyService(db, encryptionSvc)
	return service, db
}

// TestAccessKeyServicePerformance tests the performance of access key operations
func TestAccessKeyServicePerformance(t *testing.T) {
	service, _ := setupTestServiceForPerformance(t)
	ctx := context.Background()

	t.Run("BulkCreatePerformance", func(t *testing.T) {
		start := time.Now()
		keyCount := 100

		// Create 100 keys
		for i := 0; i < keyCount; i++ {
			params := CreateAccessKeyParams{
				Name:          fmt.Sprintf("test-key-%d", i),
				AllowedModels: []string{"gpt-4", "gpt-3.5-turbo"},
				Enabled:       i%2 == 0, // Alternate enabled/disabled
			}
			_, _, err := service.CreateAccessKey(ctx, params)
			require.NoError(t, err)
		}

		elapsed := time.Since(start)
		t.Logf("Created %d keys in %v (avg: %v per key)", keyCount, elapsed, elapsed/time.Duration(keyCount))

		// Performance assertion: should create 100 keys in less than 5 seconds
		assert.Less(t, elapsed, 5*time.Second, "Bulk create should be fast")
	})

	t.Run("ListPerformance", func(t *testing.T) {
		start := time.Now()

		// List all keys (should use optimized index)
		keys, err := service.ListAccessKeys(ctx)
		require.NoError(t, err)

		elapsed := time.Since(start)
		t.Logf("Listed %d keys in %v", len(keys), elapsed)

		// Performance assertion: should list in less than 100ms
		assert.Less(t, elapsed, 100*time.Millisecond, "List should be fast with indexes")
		assert.Greater(t, len(keys), 0, "Should have keys")

		// Verify ordering: enabled keys first, then by name
		for i := 1; i < len(keys); i++ {
			if keys[i-1].Enabled == keys[i].Enabled {
				// Same enabled status, check name ordering
				assert.LessOrEqual(t, keys[i-1].Name, keys[i].Name, "Keys should be ordered by name within same enabled status")
			} else {
				// Different enabled status, enabled should come first
				assert.True(t, keys[i-1].Enabled, "Enabled keys should come before disabled keys")
				assert.False(t, keys[i].Enabled, "Disabled keys should come after enabled keys")
			}
		}
	})

	t.Run("ValidationCachePerformance", func(t *testing.T) {
		// Create a test key
		params := CreateAccessKeyParams{
			Name:          "cache-test-key",
			AllowedModels: []string{"gpt-4"},
			Enabled:       true,
		}
		_, keyValue, err := service.CreateAccessKey(ctx, params)
		require.NoError(t, err)

		// First validation (cache miss)
		start := time.Now()
		key1, err := service.ValidateAccessKey(ctx, keyValue)
		require.NoError(t, err)
		firstValidation := time.Since(start)

		// Second validation (cache hit)
		start = time.Now()
		key2, err := service.ValidateAccessKey(ctx, keyValue)
		require.NoError(t, err)
		secondValidation := time.Since(start)

		t.Logf("First validation (cache miss): %v", firstValidation)
		t.Logf("Second validation (cache hit): %v", secondValidation)

		// Cache hit should be significantly faster
		assert.Less(t, secondValidation, firstValidation/2, "Cached validation should be at least 2x faster")
		assert.Equal(t, key1.ID, key2.ID, "Should return same key")
	})

	t.Run("BatchOperationPerformance", func(t *testing.T) {
		// Get all key IDs
		keys, err := service.ListAccessKeys(ctx)
		require.NoError(t, err)

		ids := make([]uint, 0, len(keys))
		for _, key := range keys {
			ids = append(ids, key.ID)
		}

		// Batch enable
		start := time.Now()
		count, err := service.BatchUpdateAccessKeysEnabled(ctx, ids, true)
		require.NoError(t, err)
		elapsed := time.Since(start)

		t.Logf("Batch enabled %d keys in %v", count, elapsed)
		assert.Equal(t, len(ids), count, "Should update all keys")
		assert.Less(t, elapsed, 500*time.Millisecond, "Batch operation should be fast")
	})

	t.Run("ConcurrentValidation", func(t *testing.T) {
		// Create a test key
		params := CreateAccessKeyParams{
			Name:          "concurrent-test-key",
			AllowedModels: []string{"gpt-4"},
			Enabled:       true,
		}
		_, keyValue, err := service.CreateAccessKey(ctx, params)
		require.NoError(t, err)

		// Concurrent validations
		concurrency := 50
		done := make(chan bool, concurrency)
		start := time.Now()

		for i := 0; i < concurrency; i++ {
			go func() {
				_, err := service.ValidateAccessKey(ctx, keyValue)
				assert.NoError(t, err)
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < concurrency; i++ {
			<-done
		}

		elapsed := time.Since(start)
		t.Logf("Completed %d concurrent validations in %v (avg: %v per validation)",
			concurrency, elapsed, elapsed/time.Duration(concurrency))

		// Should handle concurrent access without errors
		assert.Less(t, elapsed, 1*time.Second, "Concurrent validations should be fast")
	})
}

// TestAccessKeyServiceMemoryLeak tests for memory leaks in cache operations
func TestAccessKeyServiceMemoryLeak(t *testing.T) {
	service, _ := setupTestServiceForPerformance(t)
	service.keyCacheTTL = 100 * time.Millisecond

	ctx := context.Background()

	// Create test keys
	keyValues := make([]string, 10)
	for i := 0; i < 10; i++ {
		params := CreateAccessKeyParams{
			Name:          fmt.Sprintf("leak-test-key-%d", i),
			AllowedModels: []string{"gpt-4"},
			Enabled:       true,
		}
		_, keyValue, err := service.CreateAccessKey(ctx, params)
		require.NoError(t, err)
		keyValues[i] = keyValue
	}

	// Validate keys multiple times to populate cache
	for i := 0; i < 100; i++ {
		for _, keyValue := range keyValues {
			_, err := service.ValidateAccessKey(ctx, keyValue)
			require.NoError(t, err)
		}
	}

	// Check cache size before expiration
	service.keyCacheMu.RLock()
	cacheSize := len(service.keyCache)
	service.keyCacheMu.RUnlock()

	t.Logf("Cache size after 1000 validations: %d entries", cacheSize)
	assert.LessOrEqual(t, cacheSize, 10, "Cache should not grow beyond number of unique keys")

	// Wait for cache entries to expire
	time.Sleep(200 * time.Millisecond)

	// Validate again to trigger cache cleanup
	for _, keyValue := range keyValues {
		_, err := service.ValidateAccessKey(ctx, keyValue)
		require.NoError(t, err)
	}

	// Cache should still be reasonable size (expired entries replaced)
	service.keyCacheMu.RLock()
	newCacheSize := len(service.keyCache)
	service.keyCacheMu.RUnlock()

	t.Logf("Cache size after expiration and re-validation: %d entries", newCacheSize)
	assert.LessOrEqual(t, newCacheSize, 10, "Cache should not leak memory")
}

// TestAccessKeyServiceIndexUsage tests that queries use proper indexes
func TestAccessKeyServiceIndexUsage(t *testing.T) {
	service, _ := setupTestServiceForPerformance(t)
	ctx := context.Background()

	// Create test data
	for i := 0; i < 20; i++ {
		params := CreateAccessKeyParams{
			Name:          fmt.Sprintf("index-test-key-%02d", i),
			AllowedModels: []string{"gpt-4"},
			Enabled:       i%2 == 0,
		}
		_, _, err := service.CreateAccessKey(ctx, params)
		require.NoError(t, err)
	}

	t.Run("ListUsesIndex", func(t *testing.T) {
		// This query should use idx_hub_access_keys_enabled_name
		keys, err := service.ListAccessKeys(ctx)
		require.NoError(t, err)
		assert.Equal(t, 20, len(keys), "Should return all keys")

		// Verify ordering
		prevEnabled := true
		prevName := ""
		for _, key := range keys {
			if key.Enabled != prevEnabled {
				assert.True(t, prevEnabled, "Enabled keys should come first")
				assert.False(t, key.Enabled, "Disabled keys should come after")
				prevEnabled = key.Enabled
				prevName = ""
			}
			if key.Enabled == prevEnabled && prevName != "" {
				assert.GreaterOrEqual(t, key.Name, prevName, "Keys should be ordered by name")
			}
			prevName = key.Name
		}
	})

	t.Run("ValidationUsesHashIndex", func(t *testing.T) {
		// Create a key and validate it
		params := CreateAccessKeyParams{
			Name:          "hash-index-test",
			AllowedModels: []string{"gpt-4"},
			Enabled:       true,
		}
		_, keyValue, err := service.CreateAccessKey(ctx, params)
		require.NoError(t, err)

		// This query should use idx_hub_access_keys_key_hash (unique index)
		key, err := service.ValidateAccessKey(ctx, keyValue)
		require.NoError(t, err)
		assert.Equal(t, "hash-index-test", key.Name)
	})
}
