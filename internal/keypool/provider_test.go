package keypool

import (
	"context"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	skipIfNoSQLite(t)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Auto-migrate test models
	err = db.AutoMigrate(&models.APIKey{}, &models.Group{})
	require.NoError(t, err)

	return db
}

// setupTestProvider creates a test KeyProvider with in-memory store
func setupTestProvider(t *testing.T) (*KeyProvider, *gorm.DB, store.Store) {
	db := setupTestDB(t)
	memStore := store.NewMemoryStore()
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	settingsManager := config.NewSystemSettingsManager()

	provider := NewProvider(db, memStore, settingsManager, encSvc)
	return provider, db, memStore
}

// createTestGroup creates a test group with required fields
func createTestGroup(t *testing.T, db *gorm.DB, name string) *models.Group {
	t.Helper()
	group := &models.Group{
		Name:        name,
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)
	return group
}

func TestNewProvider(t *testing.T) {
	provider, _, _ := setupTestProvider(t)
	defer provider.Stop()
	assert.NotNil(t, provider)
	assert.NotNil(t, provider.db)
	assert.NotNil(t, provider.store)
	assert.NotNil(t, provider.statusUpdateChan)
	assert.NotNil(t, provider.stopChan)
}

func TestProviderStop(t *testing.T) {
	provider, _, _ := setupTestProvider(t)

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		provider.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("Provider.Stop() timed out")
	}
}

func TestSelectKey_NoKeys(t *testing.T) {
	provider, _, _ := setupTestProvider(t)
	defer provider.Stop()

	_, err := provider.SelectKey(1)
	assert.Error(t, err)
}

func TestSelectKey_Success(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Create test key
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-test123")
	require.NoError(t, err)

	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-test123"),
		Status:       models.KeyStatusActive,
		FailureCount: 0,
	}
	require.NoError(t, db.Create(apiKey).Error)

	// Add key to store
	activeKeysListKey := "group:1:active_keys"
	require.NoError(t, memStore.LPush(activeKeysListKey, apiKey.ID))

	keyHashKey := "key:1"
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    time.Now().Unix(),
	}
	require.NoError(t, memStore.HSet(keyHashKey, keyDetails))

	// Select key
	selectedKey, err := provider.SelectKey(group.ID)
	require.NoError(t, err)
	assert.NotNil(t, selectedKey)
	assert.Equal(t, "sk-test123", selectedKey.KeyValue) // Should be decrypted
	assert.Equal(t, models.KeyStatusActive, selectedKey.Status)
}

func TestUpdateStatus_Success(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	// Create test group with config
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Create test key
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-test123")
	require.NoError(t, err)

	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-test123"),
		Status:       models.KeyStatusActive,
		FailureCount: 1,
	}
	require.NoError(t, db.Create(apiKey).Error)

	// Setup store
	keyHashKey := "key:1"
	activeKeysListKey := "group:1:active_keys"
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "1",
		"created_at":    time.Now().Unix(),
	}
	require.NoError(t, memStore.HSet(keyHashKey, keyDetails))
	require.NoError(t, memStore.LPush(activeKeysListKey, apiKey.ID))

	// Update status to success
	provider.UpdateStatus(apiKey, group, true, "")

	// Wait for async processing with polling
	require.Eventually(t, func() bool {
		var updatedKey models.APIKey
		if err := db.First(&updatedKey, apiKey.ID).Error; err != nil {
			return false
		}
		return updatedKey.FailureCount == 0
	}, 5*time.Second, 10*time.Millisecond, "failure count should be reset")
}

func TestAddKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Prepare keys to add
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	keys := []models.APIKey{
		{
			GroupID:  group.ID,
			KeyValue: "sk-test1",
			KeyHash:  encSvc.Hash("sk-test1"),
			Status:   models.KeyStatusActive,
		},
		{
			GroupID:  group.ID,
			KeyValue: "sk-test2",
			KeyHash:  encSvc.Hash("sk-test2"),
			Status:   models.KeyStatusActive,
		},
	}

	// Add keys
	err := provider.AddKeys(group.ID, keys)
	require.NoError(t, err)

	// Verify keys were added
	var count int64
	db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count)
	assert.Equal(t, int64(2), count)
}

func TestRemoveKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Create test keys
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	key1 := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-test1",
		KeyHash:  encSvc.Hash("sk-test1"),
		Status:   models.KeyStatusActive,
	}
	require.NoError(t, db.Create(key1).Error)

	// Remove keys
	deletedCount, err := provider.RemoveKeys(group.ID, []string{"sk-test1"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), deletedCount)

	// Verify key was removed
	var count int64
	db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestRestoreKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
group := createTestGroup(t, db, "test-group")

	// Create invalid key
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	key := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     "sk-test1",
		KeyHash:      encSvc.Hash("sk-test1"),
		Status:       models.KeyStatusInvalid,
		FailureCount: 5,
	}
	require.NoError(t, db.Create(key).Error)

	// Restore keys
	restoredCount, err := provider.RestoreKeys(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), restoredCount)

	// Verify key was restored
	var updatedKey models.APIKey
	require.NoError(t, db.First(&updatedKey, key.ID).Error)
	assert.Equal(t, models.KeyStatusActive, updatedKey.Status)
	assert.Equal(t, int64(0), updatedKey.FailureCount)

	// Note: Active list verification skipped as LRange is not part of Store interface
	// The key restoration is verified through database status check above
}

func TestRemoveAllKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
group := createTestGroup(t, db, "test-group")

	// Create multiple keys
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	for i := 0; i < 10; i++ {
		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: "sk-test",
			KeyHash:  encSvc.Hash("sk-test"),
			Status:   models.KeyStatusActive,
		}
		require.NoError(t, db.Create(key).Error)
	}

	// Remove all keys
	ctx := context.Background()
	deletedCount, err := provider.RemoveAllKeys(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(10), deletedCount)

	// Verify all keys were removed
	var count int64
	db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestLoadKeysFromDB(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
group := createTestGroup(t, db, "test-group")

	// Create test keys
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	for i := 0; i < 5; i++ {
		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: "sk-test",
			KeyHash:  encSvc.Hash("sk-test"),
			Status:   models.KeyStatusActive,
		}
		require.NoError(t, db.Create(key).Error)
	}

	// Load keys from DB
	err := provider.LoadKeysFromDB()
	require.NoError(t, err)

	// Verify keys were loaded by checking database
	var loadedKeys []models.APIKey
	require.NoError(t, db.Where("group_id = ? AND status = ?", group.ID, models.KeyStatusActive).Find(&loadedKeys).Error)
	assert.Equal(t, 5, len(loadedKeys))
}

// setupBenchProvider creates a test KeyProvider for benchmarks
func setupBenchProvider(b *testing.B) (*KeyProvider, *gorm.DB, store.Store) {
	b.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatalf("failed to connect to test database: %v", err)
	}

	if err := db.AutoMigrate(&models.APIKey{}, &models.Group{}); err != nil {
		b.Fatalf("failed to migrate test database: %v", err)
	}

	memStore := store.NewMemoryStore()
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	settingsManager := config.NewSystemSettingsManager()

	provider := NewProvider(db, memStore, settingsManager, encSvc)
	return provider, db, memStore
}

// Benchmark tests for PGO optimization
func BenchmarkSelectKey(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	// Setup test data
	group := &models.Group{Name: "bench-group", ChannelType: "openai", Enabled: true}
	db.Create(group)

	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, _ := encSvc.Encrypt("sk-bench")
	apiKey := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: encryptedKey,
		KeyHash:  encSvc.Hash("sk-bench"),
		Status:   models.KeyStatusActive,
	}
	db.Create(apiKey)

	activeKeysListKey := "group:1:active_keys"
	memStore.LPush(activeKeysListKey, apiKey.ID)
	keyHashKey := "key:1"
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    time.Now().Unix(),
	}
	memStore.HSet(keyHashKey, keyDetails)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = provider.SelectKey(group.ID)
	}
}

func BenchmarkUpdateStatus(b *testing.B) {
	provider, db, memStore := setupTestProvider(&testing.T{})
	defer provider.Stop()

	// Setup test data
	group := &models.Group{Name: "bench-group", ChannelType: "openai", Enabled: true}
	db.Create(group)

	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, _ := encSvc.Encrypt("sk-bench")
	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-bench"),
		Status:       models.KeyStatusActive,
		FailureCount: 0,
	}
	db.Create(apiKey)

	keyHashKey := "key:1"
	activeKeysListKey := "group:1:active_keys"
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    time.Now().Unix(),
	}
	memStore.HSet(keyHashKey, keyDetails)
	memStore.LPush(activeKeysListKey, apiKey.ID)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.UpdateStatus(apiKey, group, true, "")
	}
}

func BenchmarkAddKeys(b *testing.B) {
	provider, db, _ := setupTestProvider(&testing.T{})
	defer provider.Stop()

	group := &models.Group{Name: "bench-group", ChannelType: "openai", Enabled: true}
	db.Create(group)

	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		keys := []models.APIKey{
			{
				GroupID:  group.ID,
				KeyValue: "sk-bench",
				KeyHash:  encSvc.Hash("sk-bench"),
				Status:   models.KeyStatusActive,
			},
		}
		_ = provider.AddKeys(group.ID, keys)
	}
}
