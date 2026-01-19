package keypool

import (
	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestValidator(tb testing.TB) (*KeyValidator, *gorm.DB, *KeyProvider) {
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
	// Register cleanup to stop provider
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

	return validator, db, provider
}

func TestNewKeyValidator(t *testing.T) {
	validator, _, _ := setupTestValidator(t)
	assert.NotNil(t, validator)
	assert.NotNil(t, validator.DB)
	assert.NotNil(t, validator.channelFactory)
	assert.NotNil(t, validator.SettingsManager)
}

func TestValidateSingleKey_InvalidChannel(t *testing.T) {
	validator, db, _ := setupTestValidator(t)

	// Create group with invalid channel type
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "invalid-channel",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Create test key
	apiKey := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-test123",
		Status:   models.KeyStatusActive,
	}

	// Validate should fail due to invalid channel
	isValid, err := validator.ValidateSingleKey(apiKey, group)
	assert.False(t, isValid)
	assert.Error(t, err)
}

func TestTestMultipleKeys_NonExistentKeys(t *testing.T) {
	validator, db, _ := setupTestValidator(t)

	// Create test group
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Test non-existent keys
	keyValues := []string{"sk-nonexistent1", "sk-nonexistent2"}
	results, err := validator.TestMultipleKeys(group, keyValues)
	require.NoError(t, err)
	assert.Equal(t, 2, len(results))

	// All keys should be marked as non-existent
	for _, result := range results {
		assert.False(t, result.IsValid)
		assert.Contains(t, result.Error, "does not exist")
	}
}

func TestTestMultipleKeys_EmptyList(t *testing.T) {
	validator, db, _ := setupTestValidator(t)

	// Create test group
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Test empty key list
	results, err := validator.TestMultipleKeys(group, []string{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(results))
}

func TestTestMultipleKeys_ExistingKeys(t *testing.T) {
	validator, db, _ := setupTestValidator(t)

	// Create test group
	group := &models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Create test keys - reuse encryption service from provider
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	key1 := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-test1",
		KeyHash:  encSvc.Hash("sk-test1"),
		Status:   models.KeyStatusActive,
	}
	require.NoError(t, db.Create(key1).Error)

	// Test existing keys (will fail validation due to invalid API key format)
	keyValues := []string{"sk-test1"}
	results, err := validator.TestMultipleKeys(group, keyValues)
	require.NoError(t, err)
	assert.Equal(t, 1, len(results))
	assert.Equal(t, "sk-test1", results[0].KeyValue)
}

// Benchmark tests for PGO optimization
func BenchmarkValidateSingleKey(b *testing.B) {
	validator, db, _ := setupTestValidator(b)

	group := &models.Group{
		Name:        "bench-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	db.Create(group)

	apiKey := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-bench",
		Status:   models.KeyStatusActive,
	}

	// Set timeout to avoid long waits
	group.EffectiveConfig.KeyValidationTimeoutSeconds = 1

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = validator.ValidateSingleKey(apiKey, group)
	}
}

func BenchmarkTestMultipleKeys(b *testing.B) {
	validator, db, _ := setupTestValidator(b)

	group := &models.Group{
		Name:        "bench-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	db.Create(group)

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: "sk-bench",
			KeyHash:  encSvc.Hash("sk-bench"),
			Status:   models.KeyStatusActive,
		}
		db.Create(key)
	}

	keyValues := []string{"sk-bench"}
	group.EffectiveConfig.KeyValidationTimeoutSeconds = 1

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = validator.TestMultipleKeys(group, keyValues)
	}
}
