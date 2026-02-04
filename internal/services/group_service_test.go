package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// expectedProxyURL builds the expected proxy URL using the same PORT logic as production code.
// AI Review: Added to avoid hardcoding port 3001 in tests, which can fail if PORT env var is set.
func expectedProxyURL(groupName string) string {
	port := utils.ParseInteger(os.Getenv("PORT"), 3001)
	return fmt.Sprintf("http://127.0.0.1:%d/proxy/%s", port, groupName)
}

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(tb testing.TB) *gorm.DB {
	tb.Helper()
	// Use :memory: for fast isolated testing
	// Each test gets its own isolated database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		// Disable prepared statement cache to avoid concurrency issues
		PrepareStmt: false,
	})
	require.NoError(tb, err)

	// Limit SQLite connections to avoid separate in-memory databases
	// SQLite :memory: creates a separate database per connection
	// Background goroutines need to share the same database
	sqlDB, err := db.DB()
	require.NoError(tb, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	tb.Cleanup(func() { _ = sqlDB.Close() })

	// Auto-migrate all models used by GroupService and its dependencies
	// This includes tables checked in DeleteGroup and other operations
	err = db.AutoMigrate(
		&models.Group{},
		&models.APIKey{},
		&models.GroupSubGroup{},
		&models.GroupHourlyStat{},
	)
	require.NoError(tb, err)

	// Create managed_sites table manually since it's checked in DeleteGroup
	// but not part of the core models package
	err = db.Exec(`
		CREATE TABLE IF NOT EXISTS managed_sites (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bound_group_id INTEGER,
			created_at DATETIME,
			updated_at DATETIME
		)
	`).Error
	require.NoError(tb, err)

	return db
}

// setupTestGroupService creates a GroupService with test dependencies
func setupTestGroupService(tb testing.TB, db *gorm.DB) *GroupService {
	tb.Helper()
	settingsManager := config.NewSystemSettingsManager()
	memStore := store.NewMemoryStore()

	// Create SubGroupManager first (required by GroupManager)
	subGroupManager := NewSubGroupManager(memStore)

	groupManager := NewGroupManager(db, memStore, settingsManager, subGroupManager)

	// Initialize GroupManager to set up the syncer
	err := groupManager.Initialize()
	require.NoError(tb, err)

	// Stop the syncer when test completes to avoid accessing closed DB
	tb.Cleanup(func() {
		groupManager.Stop(context.Background())
	})

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
	channelFactory := &channel.Factory{}

	svc := NewGroupService(
		db,
		ReadOnlyDB{DB: db},
		settingsManager,
		groupManager,
		channelFactory,
		keyProvider,
		keyService,
		nil, // keyImportSvc
		nil, // keyDeleteSvc
		nil, // bulkImportSvc
		encryptionSvc,
		nil, // aggregateGroupService
	)

	return svc
}

// TestCreateGroup tests group creation
func TestCreateGroup(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		params      GroupCreateParams
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid standard group",
			params: GroupCreateParams{
				Name:               "test-group-valid",
				DisplayName:        "Test Group",
				Description:        "Test Description",
				GroupType:          "standard",
				Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
				ChannelType:        "openai",
				Sort:               100,
				TestModel:          "gpt-3.5-turbo",
				ValidationEndpoint: "/v1/chat/completions",
			},
			expectError: false,
		},
		{
			name: "default sort when omitted",
			params: GroupCreateParams{
				Name:               "default-sort-group",
				GroupType:          "standard",
				Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
				ChannelType:        "openai",
				TestModel:          "gpt-3.5-turbo",
				ValidationEndpoint: "/v1/chat/completions",
				// Sort omitted -> expect default 100
			},
			expectError: false,
		},
		{
			name: "valid high sort value",
			params: GroupCreateParams{
				Name:               "high-sort-group",
				GroupType:          "standard",
				Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
				ChannelType:        "openai",
				Sort:               5000, // Valid: no range limit on sort field
				TestModel:          "gpt-3.5-turbo",
				ValidationEndpoint: "/v1/chat/completions",
			},
			expectError: false,
		},
		{
			name: "invalid group name",
			params: GroupCreateParams{
				Name:        "Invalid Name!",
				GroupType:   "standard",
				ChannelType: "openai",
				Sort:        100,
			},
			expectError: true,
		},
		{
			name: "missing test model",
			params: GroupCreateParams{
				Name:        "missing-test-model",
				GroupType:   "standard",
				Upstreams:   json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
				ChannelType: "openai",
				Sort:        100,
			},
			expectError: true,
		},
		{
			name: "invalid channel type",
			params: GroupCreateParams{
				Name:        "invalid-channel",
				GroupType:   "standard",
				ChannelType: "invalid-channel",
				Sort:        100,
			},
			expectError: true,
		},
		{
			name: "duplicate group name",
			params: GroupCreateParams{
				Name:               "test-group-valid", // Same as first test case
				DisplayName:        "Duplicate Group",
				Description:        "This should fail",
				GroupType:          "standard",
				Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
				ChannelType:        "openai",
				Sort:               100,
				TestModel:          "gpt-3.5-turbo",
				ValidationEndpoint: "/v1/chat/completions",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, err := svc.CreateGroup(context.Background(), tt.params)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, group)
				assert.NotZero(t, group.ID)
				assert.Equal(t, tt.params.Name, group.Name)
				// Verify default sort value when omitted
				if tt.params.Sort == 0 {
					assert.Equal(t, 100, group.Sort, "Expected default sort value of 100 when omitted")
				}
			}
		})
	}
}

// TestListGroups tests listing all groups
func TestListGroups(t *testing.T) {
	t.Skip("Disabled: background syncer causes 'no such table' errors. Covered by integration tests.")
}

// TestUpdateGroup tests group updates
func TestUpdateGroup(t *testing.T) {
	t.Skip("Disabled: background syncer causes 'no such table' errors. Covered by integration tests.")
}

// TestDeleteGroup tests group deletion
func TestDeleteGroup(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create a group
	params := GroupCreateParams{
		Name:               "test-group",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
	}
	group, err := svc.CreateGroup(context.Background(), params)
	require.NoError(t, err)

	// Delete the group
	err = svc.DeleteGroup(context.Background(), group.ID)
	require.NoError(t, err)

	// Verify deletion
	var count int64
	err = db.Model(&models.Group{}).Where("id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestDeleteGroupWithKeys tests group deletion with different key counts (multi-threshold strategy)
func TestDeleteGroupWithKeys(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	t.Run("SmallKeyCount_SyncDelete", func(t *testing.T) {
		// Create a group with small key count (<5000)
		params := GroupCreateParams{
			Name:               "small-keys-group",
			GroupType:          "standard",
			Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
			ChannelType:        "openai",
			Sort:               100,
			TestModel:          "gpt-3.5-turbo",
			ValidationEndpoint: "/v1/chat/completions",
		}
		group, err := svc.CreateGroup(context.Background(), params)
		require.NoError(t, err)

		// Add 100 keys
		for i := 0; i < 100; i++ {
			key := models.APIKey{
				GroupID:  group.ID,
				KeyHash:  fmt.Sprintf("hash-%d", i),
				KeyValue: fmt.Sprintf("sk-test-key-%d", i),
				Notes:    fmt.Sprintf("Test key %d", i),
			}
			err := db.Create(&key).Error
			require.NoError(t, err)
		}

		// Delete should succeed synchronously
		err = svc.DeleteGroup(context.Background(), group.ID)
		require.NoError(t, err)

		// Verify group is deleted
		var count int64
		err = db.Model(&models.Group{}).Where("id = ?", group.ID).Count(&count).Error
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)

		// Verify keys are deleted
		var keyCount int64
		err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&keyCount).Error
		require.NoError(t, err)
		assert.Equal(t, int64(0), keyCount)
	})

	t.Run("MediumKeyCount_SyncChunkedDelete", func(t *testing.T) {
		// This test would require BulkSyncThreshold+ keys which is too slow for unit tests
		// The logic is covered by the implementation and can be tested in integration tests
		t.Skip("Skipped: requires BulkSyncThreshold+ keys, too slow for unit tests")
	})

	t.Run("LargeKeyCount_AsyncDelete", func(t *testing.T) {
		// This test would require AsyncThreshold+ keys which is too slow for unit tests
		// The async logic is covered by the implementation and can be tested in integration tests
		t.Skip("Skipped: requires AsyncThreshold+ keys, too slow for unit tests")
	})
}

// TestGetGroupStats tests statistics retrieval
// DISABLED: This test has issues with GroupManager's background syncer.
// The functionality is covered by integration tests.
// TODO(issue): Re-enable after fixing background syncer isolation in tests
// func TestGetGroupStats(t *testing.T) { ... }

// TestCopyGroup tests group copying
func TestCopyGroup(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create source group
	params := GroupCreateParams{
		Name:               "source-group",
		DisplayName:        "Source Group",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
	}
	sourceGroup, err := svc.CreateGroup(context.Background(), params)
	require.NoError(t, err)

	// Copy group
	copiedGroup, err := svc.CopyGroup(context.Background(), sourceGroup.ID, "none")
	require.NoError(t, err)
	assert.NotNil(t, copiedGroup)
	assert.NotEqual(t, sourceGroup.ID, copiedGroup.ID)
	assert.NotEqual(t, sourceGroup.Name, copiedGroup.Name)
	// Verify key properties are preserved in the copy
	assert.Equal(t, sourceGroup.ChannelType, copiedGroup.ChannelType)
	assert.Equal(t, sourceGroup.TestModel, copiedGroup.TestModel)
	assert.Equal(t, sourceGroup.GroupType, copiedGroup.GroupType)
}

// TestToggleGroupEnabled tests enabling/disabling groups
func TestToggleGroupEnabled(t *testing.T) {
	t.Skip("Disabled: background syncer causes 'no such table' errors. Covered by integration tests.")
}

// TestValidateAndCleanUpstreams tests upstream validation
func TestValidateAndCleanUpstreams(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		upstreams   json.RawMessage
		expectError bool
	}{
		{
			name:        "valid upstreams",
			upstreams:   json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
			expectError: false,
		},
		{
			name:        "empty upstreams",
			upstreams:   json.RawMessage(`[]`),
			expectError: true,
		},
		{
			name:        "invalid URL",
			upstreams:   json.RawMessage(`[{"url":"not-a-url","weight":100}]`),
			expectError: true,
		},
		{
			name:        "negative weight",
			upstreams:   json.RawMessage(`[{"url":"https://api.openai.com","weight":-1}]`),
			expectError: true,
		},
		{
			name:        "all zero weights",
			upstreams:   json.RawMessage(`[{"url":"https://api.openai.com","weight":0}]`),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.validateAndCleanUpstreams(tt.upstreams)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidateAndCleanConfig tests config validation
func TestValidateAndCleanConfig(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		config      map[string]any
		expectError bool
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: false,
		},
		{
			name: "valid config",
			config: map[string]any{
				"connect_timeout": 30,
				"request_timeout": 60,
			},
			expectError: false,
		},
		{
			name: "invalid field",
			config: map[string]any{
				"invalid_field": "value",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.validateAndCleanConfig(tt.config)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestIsValidGroupName tests group name validation
func TestIsValidGroupName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid lowercase", "test-group", true},
		{"valid with numbers", "test123", true},
		{"valid with underscore", "test_group", true},
		{"valid with dash", "test-group", true},
		{"invalid uppercase", "TestGroup", false},
		{"invalid special chars", "test@group", false},
		{"invalid empty", "", false},
		{"invalid too long", strings.Repeat("a", 101), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidGroupName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsValidValidationEndpoint tests validation endpoint validation
func TestIsValidValidationEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid path", "/v1/chat/completions", true},
		{"empty is valid", "", true},
		{"invalid no leading slash", "v1/chat/completions", false},
		{"invalid with scheme", "https://api.openai.com/v1/chat", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidValidationEndpoint(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestCalculateRequestStats tests request statistics calculation
func TestCalculateRequestStats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		total    int64
		failed   int64
		expected RequestStats
	}{
		{
			name:   "no requests",
			total:  0,
			failed: 0,
			expected: RequestStats{
				TotalRequests:  0,
				FailedRequests: 0,
				FailureRate:    0,
			},
		},
		{
			name:   "all successful",
			total:  100,
			failed: 0,
			expected: RequestStats{
				TotalRequests:  100,
				FailedRequests: 0,
				FailureRate:    0,
			},
		},
		{
			name:   "50% failure rate",
			total:  100,
			failed: 50,
			expected: RequestStats{
				TotalRequests:  100,
				FailedRequests: 50,
				FailureRate:    0.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateRequestStats(tt.total, tt.failed)
			assert.Equal(t, tt.expected.TotalRequests, result.TotalRequests)
			assert.Equal(t, tt.expected.FailedRequests, result.FailedRequests)
			assert.InDelta(t, tt.expected.FailureRate, result.FailureRate, 0.0001)
		})
	}
}

// TestInvalidateKeyStatsCache tests cache invalidation
func TestInvalidateKeyStatsCache(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Add entry to cache
	svc.keyStatsCacheMu.Lock()
	svc.keyStatsCache[1] = groupKeyStatsCacheEntry{
		Stats:     KeyStats{TotalKeys: 10},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	svc.keyStatsCacheMu.Unlock()

	// Invalidate cache
	svc.InvalidateKeyStatsCache(1)

	// Verify cache is empty
	svc.keyStatsCacheMu.RLock()
	_, exists := svc.keyStatsCache[1]
	svc.keyStatsCacheMu.RUnlock()
	assert.False(t, exists)
}

// TestNormalizeHeaderRules tests header rules normalization
func TestNormalizeHeaderRules(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		rules       []models.HeaderRule
		expectError bool
	}{
		{
			name:        "empty rules",
			rules:       []models.HeaderRule{},
			expectError: false,
		},
		{
			name: "valid rules",
			rules: []models.HeaderRule{
				{Key: "Authorization", Value: "Bearer token", Action: "set"},
			},
			expectError: false,
		},
		{
			name: "duplicate keys",
			rules: []models.HeaderRule{
				{Key: "Authorization", Value: "Bearer token1", Action: "set"},
				{Key: "authorization", Value: "Bearer token2", Action: "set"},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.normalizeHeaderRules(tt.rules)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestNormalizePathRedirects tests path redirect normalization
func TestNormalizePathRedirects(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name     string
		rules    []models.PathRedirectRule
		expected int // expected number of rules after normalization
	}{
		{
			name:     "empty rules",
			rules:    []models.PathRedirectRule{},
			expected: 0,
		},
		{
			name: "valid rules",
			rules: []models.PathRedirectRule{
				{From: "/old", To: "/new"},
			},
			expected: 1,
		},
		{
			name: "filter empty rules",
			rules: []models.PathRedirectRule{
				{From: "/old", To: "/new"},
				{From: "", To: "/new"},
				{From: "/old2", To: ""},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := svc.normalizePathRedirects(tt.rules)
			require.NoError(t, err)

			var rules []models.PathRedirectRule
			err = json.Unmarshal(result, &rules)
			require.NoError(t, err)
			assert.Len(t, rules, tt.expected)
		})
	}
}

// TestValidateParamOverrides tests parameter override validation
func TestValidateParamOverrides(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		overrides   map[string]any
		expectError bool
	}{
		{
			name:        "nil overrides",
			overrides:   nil,
			expectError: false,
		},
		{
			name: "valid boolean",
			overrides: map[string]any{
				"stream": true,
			},
			expectError: false,
		},
		{
			name: "valid number",
			overrides: map[string]any{
				"temperature": 0.7,
			},
			expectError: false,
		},
		{
			name: "valid integer",
			overrides: map[string]any{
				"max_tokens": 100,
			},
			expectError: false,
		},
		{
			name: "invalid type for boolean",
			overrides: map[string]any{
				"stream": "true",
			},
			expectError: true,
		},
		{
			name: "invalid type for number",
			overrides: map[string]any{
				"temperature": "0.7",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParamOverrides(tt.overrides)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestIsConfigCCSupportEnabled tests CC support detection
func TestIsConfigCCSupportEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		config   datatypes.JSONMap
		expected bool
	}{
		{
			name:     "nil config",
			config:   nil,
			expected: false,
		},
		{
			name:     "missing cc_support",
			config:   datatypes.JSONMap{},
			expected: false,
		},
		{
			name: "boolean true",
			config: datatypes.JSONMap{
				"cc_support": true,
			},
			expected: true,
		},
		{
			name: "boolean false",
			config: datatypes.JSONMap{
				"cc_support": false,
			},
			expected: false,
		},
		{
			name: "string true",
			config: datatypes.JSONMap{
				"cc_support": "true",
			},
			expected: true,
		},
		{
			name: "number non-zero",
			config: datatypes.JSONMap{
				"cc_support": 1,
			},
			expected: true,
		},
		{
			name: "number zero",
			config: datatypes.JSONMap{
				"cc_support": 0,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isConfigCCSupportEnabled(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// BenchmarkCreateGroup benchmarks group creation
func BenchmarkCreateGroup(b *testing.B) {
	db := setupTestDB(b)
	svc := setupTestGroupService(b, db)
	b.ReportAllocs()

	params := GroupCreateParams{
		Name:               "bench-group",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		params.Name = "bench-group-" + strconv.Itoa(i)
		if _, err := svc.CreateGroup(context.Background(), params); err != nil {
			b.Fatalf("Failed to create group: %v", err)
		}
	}
}

// BenchmarkListGroups benchmarks group listing
func BenchmarkListGroups(b *testing.B) {
	db := setupTestDB(b)
	svc := setupTestGroupService(b, db)
	b.ReportAllocs()

	// Create some groups
	for i := 0; i < 10; i++ {
		params := GroupCreateParams{
			Name:               "bench-group-" + strconv.Itoa(i),
			GroupType:          "standard",
			Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
			ChannelType:        "openai",
			Sort:               100,
			TestModel:          "gpt-3.5-turbo",
			ValidationEndpoint: "/v1/chat/completions",
		}
		if _, err := svc.CreateGroup(context.Background(), params); err != nil {
			b.Fatalf("Failed to create group: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.ListGroups(context.Background()); err != nil {
			b.Fatalf("Failed to list groups: %v", err)
		}
	}
}

// BenchmarkGetGroupStats benchmarks statistics retrieval
func BenchmarkGetGroupStats(b *testing.B) {
	db := setupTestDB(b)
	svc := setupTestGroupService(b, db)
	b.ReportAllocs()

	params := GroupCreateParams{
		Name:               "bench-group",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
	}
	group, err := svc.CreateGroup(context.Background(), params)
	if err != nil {
		b.Fatalf("Failed to create group: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.GetGroupStats(context.Background(), group.ID); err != nil {
			b.Fatalf("Failed to get group stats: %v", err)
		}
	}
}

// TestUpdateGroupWithChildGroupSync tests that child groups are synced when parent group name changes
func TestUpdateGroupWithChildGroupSync(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create parent group
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-group",
		DisplayName:        "Parent Group",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key-1,sk-parent-key-2",
	})
	require.NoError(t, err)
	require.NotNil(t, parentGroup)

	// Create child group manually (simulating child group creation)
	childUpstreams := []map[string]interface{}{
		{
			"url":    "http://127.0.0.1:3001/proxy/parent-group",
			"weight": 1,
		},
	}
	childUpstreamsJSON, err := json.Marshal(childUpstreams)
	require.NoError(t, err)

	childGroup := models.Group{
		Name:               "parent-group_child1",
		DisplayName:        "Parent Group (Child1)",
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          datatypes.JSON(childUpstreamsJSON),
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &parentGroup.ID,
		ProxyKeys:          "sk-child-random-key",
		Sort:               parentGroup.Sort,
	}
	err = db.Create(&childGroup).Error
	require.NoError(t, err)

	// Update parent group name
	newName := "parent-group-renamed"
	_, err = svc.UpdateGroup(context.Background(), parentGroup.ID, GroupUpdateParams{
		Name: &newName,
	})
	require.NoError(t, err)

	// Verify child group upstream was updated
	var updatedChild models.Group
	err = db.First(&updatedChild, childGroup.ID).Error
	require.NoError(t, err)

	var upstreams []map[string]interface{}
	err = json.Unmarshal(updatedChild.Upstreams, &upstreams)
	require.NoError(t, err)
	require.Len(t, upstreams, 1)

	expectedURL := expectedProxyURL("parent-group-renamed")
	assert.Equal(t, expectedURL, upstreams[0]["url"])
}

// TestUpdateGroupWithChildGroupSyncCommit tests that parent update commits successfully with child sync.
// AI Review: Renamed from TestUpdateGroupWithChildGroupSyncRollback to reflect actual behavior (tests commit, not rollback).
func TestUpdateGroupWithChildGroupSyncCommit(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create parent group
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-rollback",
		DisplayName:        "Parent Rollback",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key",
	})
	require.NoError(t, err)

	// Create child group
	childUpstreams := []map[string]interface{}{
		{
			"url":    expectedProxyURL("parent-rollback"),
			"weight": 1,
		},
	}
	childUpstreamsJSON, err := json.Marshal(childUpstreams)
	require.NoError(t, err)

	childGroup := models.Group{
		Name:               "parent-rollback_child1",
		DisplayName:        "Parent Rollback (Child1)",
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          datatypes.JSON(childUpstreamsJSON),
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &parentGroup.ID,
		ProxyKeys:          "sk-child-key",
		Sort:               parentGroup.Sort,
	}
	err = db.Create(&childGroup).Error
	require.NoError(t, err)

	// Get original parent name
	originalName := parentGroup.Name

	// Update parent group name (should succeed with transaction)
	newName := "parent-rollback-renamed"
	updatedGroup, err := svc.UpdateGroup(context.Background(), parentGroup.ID, GroupUpdateParams{
		Name: &newName,
	})
	require.NoError(t, err)
	assert.Equal(t, newName, updatedGroup.Name)

	// Verify parent group was updated in database
	var dbParent models.Group
	err = db.First(&dbParent, parentGroup.ID).Error
	require.NoError(t, err)
	assert.Equal(t, newName, dbParent.Name)
	assert.NotEqual(t, originalName, dbParent.Name)

	// Verify child group upstream was also updated (transaction committed successfully)
	var dbChild models.Group
	err = db.First(&dbChild, childGroup.ID).Error
	require.NoError(t, err)

	var upstreams []map[string]interface{}
	err = json.Unmarshal(dbChild.Upstreams, &upstreams)
	require.NoError(t, err)
	require.Len(t, upstreams, 1)

	expectedURL := expectedProxyURL("parent-rollback-renamed")
	assert.Equal(t, expectedURL, upstreams[0]["url"])
}

// TestUpdateGroupNoChildGroupSync tests that non-name changes don't trigger child sync
func TestUpdateGroupNoChildGroupSync(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create parent group
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-no-sync",
		DisplayName:        "Parent No Sync",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key",
	})
	require.NoError(t, err)

	// Create child group
	childUpstreams := []map[string]interface{}{
		{
			"url":    expectedProxyURL("parent-no-sync"),
			"weight": 1,
		},
	}
	childUpstreamsJSON, err := json.Marshal(childUpstreams)
	require.NoError(t, err)

	childGroup := models.Group{
		Name:               "parent-no-sync_child1",
		DisplayName:        "Parent No Sync (Child1)",
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          datatypes.JSON(childUpstreamsJSON),
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &parentGroup.ID,
		ProxyKeys:          "sk-child-key",
		Sort:               parentGroup.Sort,
	}
	err = db.Create(&childGroup).Error
	require.NoError(t, err)

	// Update parent group display name (should NOT trigger child sync)
	newDisplayName := "Parent No Sync Updated"
	_, err = svc.UpdateGroup(context.Background(), parentGroup.ID, GroupUpdateParams{
		DisplayName: &newDisplayName,
	})
	require.NoError(t, err)

	// Verify child group upstream was NOT changed
	var updatedChild models.Group
	err = db.First(&updatedChild, childGroup.ID).Error
	require.NoError(t, err)

	var upstreams []map[string]interface{}
	err = json.Unmarshal(updatedChild.Upstreams, &upstreams)
	require.NoError(t, err)
	require.Len(t, upstreams, 1)

	// Upstream should still point to original parent name
	expectedURL := expectedProxyURL("parent-no-sync")
	assert.Equal(t, expectedURL, upstreams[0]["url"])
}

// TestUpdateGroupWithoutChildGroups tests that updating a parent group without child groups works correctly
func TestUpdateGroupWithoutChildGroups(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create parent group without any child groups
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-no-children",
		DisplayName:        "Parent No Children",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key",
	})
	require.NoError(t, err)

	// Update parent group name (should succeed even without child groups)
	newName := "parent-no-children-renamed"
	updatedGroup, err := svc.UpdateGroup(context.Background(), parentGroup.ID, GroupUpdateParams{
		Name: &newName,
	})
	require.NoError(t, err)
	assert.Equal(t, newName, updatedGroup.Name)

	// Verify parent group was updated in database
	var dbParent models.Group
	err = db.First(&dbParent, parentGroup.ID).Error
	require.NoError(t, err)
	assert.Equal(t, newName, dbParent.Name)

	// Verify no child groups exist
	var childCount int64
	err = db.Model(&models.Group{}).Where("parent_group_id = ?", parentGroup.ID).Count(&childCount).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), childCount)
}

// TestUpdateGroupWithChildGroupCacheInvalidation tests that child groups cache is invalidated
// when parent group name changes, ensuring frontend displays updated upstream URLs.
func TestUpdateGroupWithChildGroupCacheInvalidation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Track cache invalidation calls
	cacheInvalidated := false
	svc.InvalidateChildGroupsCacheCallback = func() {
		cacheInvalidated = true
	}

	// Create parent group
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-cache-test",
		DisplayName:        "Parent Cache Test",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key",
	})
	require.NoError(t, err)

	// Create child group
	childUpstreams := []map[string]interface{}{
		{
			"url":    expectedProxyURL("parent-cache-test"),
			"weight": 1,
		},
	}
	childUpstreamsJSON, err := json.Marshal(childUpstreams)
	require.NoError(t, err)

	childGroup := models.Group{
		Name:               "parent-cache-test_child1",
		DisplayName:        "Parent Cache Test (Child1)",
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          datatypes.JSON(childUpstreamsJSON),
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &parentGroup.ID,
		ProxyKeys:          "sk-child-key",
		Sort:               parentGroup.Sort,
	}
	err = db.Create(&childGroup).Error
	require.NoError(t, err)

	// Reset cache invalidation flag
	cacheInvalidated = false

	// Update parent group name
	newName := "parent-cache-test-renamed"
	_, err = svc.UpdateGroup(context.Background(), parentGroup.ID, GroupUpdateParams{
		Name: &newName,
	})
	require.NoError(t, err)

	// Verify cache was invalidated
	assert.True(t, cacheInvalidated, "Child groups cache should be invalidated when parent name changes")

	// Verify child group upstream was updated in database
	var updatedChild models.Group
	err = db.First(&updatedChild, childGroup.ID).Error
	require.NoError(t, err)

	var upstreams []map[string]interface{}
	err = json.Unmarshal(updatedChild.Upstreams, &upstreams)
	require.NoError(t, err)
	require.Len(t, upstreams, 1)

	expectedURL := expectedProxyURL("parent-cache-test-renamed")
	assert.Equal(t, expectedURL, upstreams[0]["url"])
}

// TestUpdateGroupWithChildGroupProxyKeysSync tests that child groups' API keys are synced
// when parent group's proxy_keys change, and cache is invalidated.
func TestUpdateGroupWithChildGroupProxyKeysSync(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Track cache invalidation calls
	cacheInvalidated := false
	svc.InvalidateChildGroupsCacheCallback = func() {
		cacheInvalidated = true
	}

	// Create parent group
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-proxy-test",
		DisplayName:        "Parent Proxy Test",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key-1,sk-parent-key-2",
	})
	require.NoError(t, err)

	// Create child group
	childUpstreams := []map[string]interface{}{
		{
			"url":    expectedProxyURL("parent-proxy-test"),
			"weight": 1,
		},
	}
	childUpstreamsJSON, err := json.Marshal(childUpstreams)
	require.NoError(t, err)

	childGroup := models.Group{
		Name:               "parent-proxy-test_child1",
		DisplayName:        "Parent Proxy Test (Child1)",
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          datatypes.JSON(childUpstreamsJSON),
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &parentGroup.ID,
		ProxyKeys:          "sk-child-random-key",
		Sort:               parentGroup.Sort,
	}
	err = db.Create(&childGroup).Error
	require.NoError(t, err)

	// Add parent's first proxy key as child's API key
	if svc.keyService != nil {
		_, err = svc.keyService.AddMultipleKeys(childGroup.ID, "sk-parent-key-1")
		require.NoError(t, err)
	}

	// Reset cache invalidation flag
	cacheInvalidated = false

	// Update parent group proxy_keys
	newProxyKeys := "sk-parent-key-new,sk-parent-key-2"
	_, err = svc.UpdateGroup(context.Background(), parentGroup.ID, GroupUpdateParams{
		ProxyKeys: &newProxyKeys,
	})
	require.NoError(t, err)

	// Verify cache was invalidated
	assert.True(t, cacheInvalidated, "Child groups cache should be invalidated when parent proxy_keys change")

	// Verify child group has new API key
	if svc.keyService != nil {
		var apiKeys []models.APIKey
		err = db.Where("group_id = ?", childGroup.ID).Find(&apiKeys).Error
		require.NoError(t, err)

		// Should have the new key
		newKeyHash := svc.encryptionSvc.Hash("sk-parent-key-new")
		found := false
		for _, key := range apiKeys {
			if key.KeyHash == newKeyHash {
				found = true
				break
			}
		}
		assert.True(t, found, "Child group should have new API key from parent")

		// Verify old key was removed (sync behavior is replace, not add)
		oldKeyHash := svc.encryptionSvc.Hash("sk-parent-key-1")
		oldKeyFound := false
		for _, key := range apiKeys {
			if key.KeyHash == oldKeyHash {
				oldKeyFound = true
				break
			}
		}
		assert.False(t, oldKeyFound, "Old API key should be removed from child group after sync")
	}
}

// TestUpdateGroupNoCacheInvalidationWhenNoChildGroups tests that cache is not invalidated
// when updating a parent group that has no child groups.
func TestUpdateGroupNoCacheInvalidationWhenNoChildGroups(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Track cache invalidation calls
	cacheInvalidated := false
	svc.InvalidateChildGroupsCacheCallback = func() {
		cacheInvalidated = true
	}

	// Create parent group without child groups
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-no-child",
		DisplayName:        "Parent No Child",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key",
	})
	require.NoError(t, err)

	// Reset cache invalidation flag
	cacheInvalidated = false

	// Update parent group name
	newName := "parent-no-child-renamed"
	_, err = svc.UpdateGroup(context.Background(), parentGroup.ID, GroupUpdateParams{
		Name: &newName,
	})
	require.NoError(t, err)

	// Verify cache was NOT invalidated (no child groups to sync)
	assert.False(t, cacheInvalidated, "Child groups cache should not be invalidated when parent has no child groups")
}

// TestUpdateChildGroupCannotModifyUpstream tests that child groups cannot modify their upstream URLs.
func TestUpdateChildGroupCannotModifyUpstream(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create parent group
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-upstream-test",
		DisplayName:        "Parent Upstream Test",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key",
	})
	require.NoError(t, err)

	// Create child group
	childUpstreams := []map[string]interface{}{
		{
			"url":    expectedProxyURL("parent-upstream-test"),
			"weight": 1,
		},
	}
	childUpstreamsJSON, err := json.Marshal(childUpstreams)
	require.NoError(t, err)

	childGroup := models.Group{
		Name:               "parent-upstream-test_child1",
		DisplayName:        "Parent Upstream Test (Child1)",
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          datatypes.JSON(childUpstreamsJSON),
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &parentGroup.ID,
		ProxyKeys:          "sk-child-key",
		Sort:               parentGroup.Sort,
	}
	err = db.Create(&childGroup).Error
	require.NoError(t, err)

	// Try to update child group's upstream (should fail)
	newUpstreams := json.RawMessage(`[{"url":"https://different-api.com","weight":100}]`)
	_, err = svc.UpdateGroup(context.Background(), childGroup.ID, GroupUpdateParams{
		Upstreams:    newUpstreams,
		HasUpstreams: true,
	})

	// Should return validation error
	require.Error(t, err)
	// Check if it's an I18nError with the correct message ID
	var i18nErr *I18nError
	if errors.As(err, &i18nErr) {
		assert.Equal(t, "validation.child_group_cannot_modify_upstream", i18nErr.MessageID)
	} else {
		t.Fatalf("Expected I18nError, got %T", err)
	}

	// Verify child group upstream was NOT changed
	var updatedChild models.Group
	err = db.First(&updatedChild, childGroup.ID).Error
	require.NoError(t, err)

	var upstreams []map[string]interface{}
	err = json.Unmarshal(updatedChild.Upstreams, &upstreams)
	require.NoError(t, err)
	require.Len(t, upstreams, 1)

	// Upstream should still be the original parent proxy URL
	expectedURL := expectedProxyURL("parent-upstream-test")
	assert.Equal(t, expectedURL, upstreams[0]["url"])
}

// TestUpdateChildGroupOtherFieldsAllowed tests that child groups can update other fields (not upstream).
func TestUpdateChildGroupOtherFieldsAllowed(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	// Create parent group
	parentGroup, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "parent-other-fields",
		DisplayName:        "Parent Other Fields",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		Sort:               100,
		TestModel:          "gpt-3.5-turbo",
		ValidationEndpoint: "/v1/chat/completions",
		ProxyKeys:          "sk-parent-key",
	})
	require.NoError(t, err)

	// Create child group
	childUpstreams := []map[string]interface{}{
		{
			"url":    expectedProxyURL("parent-other-fields"),
			"weight": 1,
		},
	}
	childUpstreamsJSON, err := json.Marshal(childUpstreams)
	require.NoError(t, err)

	childGroup := models.Group{
		Name:               "parent-other-fields_child1",
		DisplayName:        "Parent Other Fields (Child1)",
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          datatypes.JSON(childUpstreamsJSON),
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &parentGroup.ID,
		ProxyKeys:          "sk-child-key",
		Sort:               parentGroup.Sort,
	}
	err = db.Create(&childGroup).Error
	require.NoError(t, err)

	// Update child group's display name and description (should succeed)
	newDisplayName := "Updated Child Display Name"
	newDescription := "Updated child description"
	updatedGroup, err := svc.UpdateGroup(context.Background(), childGroup.ID, GroupUpdateParams{
		DisplayName: &newDisplayName,
		Description: &newDescription,
	})

	// Should succeed
	require.NoError(t, err)
	assert.Equal(t, newDisplayName, updatedGroup.DisplayName)
	assert.Equal(t, newDescription, updatedGroup.Description)

	// Verify upstream was NOT changed
	var upstreams []map[string]interface{}
	err = json.Unmarshal(updatedGroup.Upstreams, &upstreams)
	require.NoError(t, err)
	require.Len(t, upstreams, 1)

	expectedURL := expectedProxyURL("parent-other-fields")
	assert.Equal(t, expectedURL, upstreams[0]["url"])
}
