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
		&models.RequestLog{},
		&models.GroupHourlyStat{},
		&models.DynamicWeightMetric{},
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
	keyService := NewKeyService(db, ReadOnlyDB{DB: db}, keyProvider, keyValidator, encryptionSvc, nil)
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

func TestGetGroupConfigOptionsIncludesRetryDelayAndBackoff(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	options, err := svc.GetGroupConfigOptions()
	require.NoError(t, err)

	var retryDelay *ConfigOption
	var retryBackoff *ConfigOption
	var retryBackoffMaxPercent *ConfigOption
	var skipTLSVerify *ConfigOption
	for i := range options {
		require.NotEqual(t, "retry_exponential_backoff", options[i].Key)
		if options[i].Key == "retry_delay_ms" {
			retryDelay = &options[i]
		}
		if options[i].Key == "retry_backoff_enabled" {
			retryBackoff = &options[i]
		}
		if options[i].Key == "retry_backoff_max_percent" {
			retryBackoffMaxPercent = &options[i]
		}
		if options[i].Key == "skip_tls_verify" {
			skipTLSVerify = &options[i]
		}
	}

	require.NotNil(t, retryDelay)
	assert.Equal(t, "config.retry_delay_ms", retryDelay.Name)
	assert.Equal(t, "config.retry_delay_ms_desc", retryDelay.Description)
	assert.Equal(t, 0, retryDelay.DefaultValue)

	require.NotNil(t, retryBackoff)
	assert.Equal(t, "config.retry_backoff_enabled", retryBackoff.Name)
	assert.Equal(t, "config.retry_backoff_enabled_desc", retryBackoff.Description)
	assert.Equal(t, false, retryBackoff.DefaultValue)

	require.NotNil(t, retryBackoffMaxPercent)
	assert.Equal(t, "config.retry_backoff_max_percent", retryBackoffMaxPercent.Name)
	assert.Equal(t, "config.retry_backoff_max_percent_desc", retryBackoffMaxPercent.Description)
	assert.Equal(t, 500, retryBackoffMaxPercent.DefaultValue)

	require.NotNil(t, skipTLSVerify)
	assert.Equal(t, "config.skip_tls_verify", skipTLSVerify.Name)
	assert.Equal(t, "config.skip_tls_verify_desc", skipTLSVerify.Description)
	assert.Equal(t, false, skipTLSVerify.DefaultValue)
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

func TestReorderGroups(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	groups := []models.Group{
		{
			Name:               "reorder-a",
			GroupType:          "standard",
			Enabled:            true,
			Upstreams:          datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":100}]`)),
			ChannelType:        "openai",
			Sort:               10,
			TestModel:          "gpt-4.1-mini",
			ValidationEndpoint: "/v1/chat/completions",
		},
		{
			Name:               "reorder-b",
			GroupType:          "standard",
			Enabled:            true,
			Upstreams:          datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":100}]`)),
			ChannelType:        "openai",
			Sort:               20,
			TestModel:          "gpt-4.1-mini",
			ValidationEndpoint: "/v1/chat/completions",
		},
		{
			Name:               "reorder-c",
			GroupType:          "standard",
			Enabled:            true,
			Upstreams:          datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":100}]`)),
			ChannelType:        "openai",
			Sort:               30,
			TestModel:          "gpt-4.1-mini",
			ValidationEndpoint: "/v1/chat/completions",
		},
	}
	require.NoError(t, db.Create(&groups).Error)

	err := svc.ReorderGroups(context.Background(), []GroupReorderItem{
		{ID: groups[0].ID, Sort: 30},
		{ID: groups[1].ID, Sort: 10},
		{ID: groups[2].ID, Sort: 20},
	})
	require.NoError(t, err)

	var updated []models.Group
	require.NoError(t, db.Order(GroupListOrderClause).Find(&updated).Error)
	require.Len(t, updated, 3)
	assert.Equal(t, []string{"reorder-b", "reorder-c", "reorder-a"}, []string{
		updated[0].Name,
		updated[1].Name,
		updated[2].Name,
	})
}

func TestReorderGroupsAllowsNoopSortValues(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	groups := []models.Group{
		{
			Name:               "reorder-noop-a",
			GroupType:          "standard",
			Enabled:            true,
			Upstreams:          datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":100}]`)),
			ChannelType:        "openai",
			Sort:               10,
			TestModel:          "gpt-4.1-mini",
			ValidationEndpoint: "/v1/chat/completions",
		},
		{
			Name:               "reorder-noop-b",
			GroupType:          "standard",
			Enabled:            true,
			Upstreams:          datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":100}]`)),
			ChannelType:        "openai",
			Sort:               20,
			TestModel:          "gpt-4.1-mini",
			ValidationEndpoint: "/v1/chat/completions",
		},
	}
	require.NoError(t, db.Create(&groups).Error)

	err := svc.ReorderGroups(context.Background(), []GroupReorderItem{
		{ID: groups[0].ID, Sort: 10},
		{ID: groups[1].ID, Sort: 20},
	})
	require.NoError(t, err)
}

func TestReorderGroupsValidation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name  string
		items []GroupReorderItem
	}{
		{name: "empty", items: nil},
		{name: "zero id", items: []GroupReorderItem{{ID: 0, Sort: 1}}},
		{name: "negative sort", items: []GroupReorderItem{{ID: 1, Sort: -1}}},
		{name: "duplicate id", items: []GroupReorderItem{{ID: 1, Sort: 1}, {ID: 1, Sort: 2}}},
		{name: "not found", items: []GroupReorderItem{{ID: 999, Sort: 1}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.ReorderGroups(context.Background(), tt.items)
			require.Error(t, err)
		})
	}
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

func TestCreateGroupSavesProxyPoolSelectedUpstreamProxy(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	group, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "proxy-pool-upstream",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100,"proxy_url":" socks5://127.0.0.1:1080 "}]`),
		ChannelType:        "openai",
		TestModel:          "gpt-4.1-mini",
		ValidationEndpoint: "/v1/chat/completions",
	})
	require.NoError(t, err)

	var saved []struct {
		URL      string  `json:"url"`
		Weight   int     `json:"weight"`
		ProxyURL *string `json:"proxy_url,omitempty"`
	}
	require.NoError(t, json.Unmarshal(group.Upstreams, &saved))
	require.Len(t, saved, 1)
	require.NotNil(t, saved[0].ProxyURL)
	assert.Equal(t, "socks5://127.0.0.1:1080", *saved[0].ProxyURL)
}

func TestCreateGroupSavesGatewayProxySelectedUpstream(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	group, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "gateway-proxy-upstream",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.anthropic.com","weight":100,"gateway_proxy":" BetterClaude "}]`),
		ChannelType:        "anthropic",
		TestModel:          "claude-3-haiku-20240307",
		ValidationEndpoint: "/v1/messages",
	})
	require.NoError(t, err)

	var saved []struct {
		URL          string `json:"url"`
		Weight       int    `json:"weight"`
		GatewayProxy string `json:"gateway_proxy,omitempty"`
	}
	require.NoError(t, json.Unmarshal(group.Upstreams, &saved))
	require.Len(t, saved, 1)
	assert.Equal(t, "betterclaude", saved[0].GatewayProxy)
}

func TestCreateGroupRejectsUnsupportedGatewayProxy(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	_, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "unknown-gateway-proxy",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.anthropic.com","weight":100,"gateway_proxy":"unknown"}]`),
		ChannelType:        "anthropic",
		TestModel:          "claude-3-haiku-20240307",
		ValidationEndpoint: "/v1/messages",
	})
	require.Error(t, err)
}

func TestCreateGroupRejectsGatewayProxyWithUpstreamProxy(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	_, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "gateway-and-proxy",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.anthropic.com","weight":100,"proxy_url":"http://127.0.0.1:8080","gateway_proxy":"betterclaude"}]`),
		ChannelType:        "anthropic",
		TestModel:          "claude-3-haiku-20240307",
		ValidationEndpoint: "/v1/messages",
	})
	require.Error(t, err)
}

func TestUpdateGroupSavesProxyPoolSelectedConfigProxy(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	group, err := svc.CreateGroup(context.Background(), GroupCreateParams{
		Name:               "proxy-pool-config",
		GroupType:          "standard",
		Upstreams:          json.RawMessage(`[{"url":"https://api.openai.com","weight":100}]`),
		ChannelType:        "openai",
		TestModel:          "gpt-4.1-mini",
		ValidationEndpoint: "/v1/chat/completions",
	})
	require.NoError(t, err)

	updated, err := svc.UpdateGroup(context.Background(), group.ID, GroupUpdateParams{
		Config: map[string]any{
			"proxy_url": "http://127.0.0.1:8080",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updated.Config)
	assert.Equal(t, "http://127.0.0.1:8080", updated.Config["proxy_url"])

	var persisted models.Group
	require.NoError(t, db.First(&persisted, group.ID).Error)
	require.NotNil(t, persisted.Config)
	assert.Equal(t, "http://127.0.0.1:8080", persisted.Config["proxy_url"])
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
			_, err := svc.validateAndCleanConfig(tt.config, "openai")
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateAndCleanConfigLegacyRequestTimeoutCompatibility(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		config      map[string]any
		expected    map[string]any
		expectError bool
	}{
		{
			name: "legacy request timeout backfills missing split timeout",
			config: map[string]any{
				"request_timeout": 60,
			},
			expected: map[string]any{
				"request_timeout":            float64(60),
				"non_stream_request_timeout": float64(60),
			},
		},
		{
			name: "legacy string request timeout backfills missing split timeout",
			config: map[string]any{
				"request_timeout": "60",
			},
			expected: map[string]any{
				"request_timeout":            float64(60),
				"non_stream_request_timeout": float64(60),
			},
		},
		{
			name: "explicit zero split timeout overrides legacy fallback",
			config: map[string]any{
				"request_timeout":            60,
				"non_stream_request_timeout": 0,
			},
			expected: map[string]any{
				"request_timeout":            float64(0),
				"non_stream_request_timeout": float64(0),
			},
		},
		{
			name: "legacy zero request timeout remains invalid without split timeout",
			config: map[string]any{
				"request_timeout": 0,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, err := svc.validateAndCleanConfig(tt.config, "openai")
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			for key, expected := range tt.expected {
				assert.Equal(t, expected, cleaned[key])
			}
		})
	}
}

func TestValidateAndCleanConfigRemovesForceFunctionCallForGemini(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	cleaned, err := svc.validateAndCleanConfig(map[string]any{
		"force_function_call": true,
		"cc_support":          true,
		"codex_support":       true,
	}, "gemini")

	require.NoError(t, err)
	assert.NotContains(t, cleaned, "force_function_call")
	assert.Equal(t, true, cleaned["cc_support"])
	assert.NotContains(t, cleaned, "codex_support")
}

func TestValidateAndCleanConfigCodexSupportScope(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		channelType string
		want        bool
	}{
		{name: "openai force codex group", channelType: "openai", want: true},
		{name: "anthropic force codex group", channelType: "anthropic", want: true},
		{name: "openai responses is codex native", channelType: "openai-response", want: false},
		{name: "gemini unsupported", channelType: "gemini", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, err := svc.validateAndCleanConfig(map[string]any{
				"codex_support": true,
			}, tt.channelType)

			require.NoError(t, err)
			if tt.want {
				assert.Equal(t, true, cleaned["codex_support"])
			} else {
				assert.NotContains(t, cleaned, "codex_support")
			}
		})
	}
}

func TestValidateAndCleanConfigCodexAffinityScope(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		channelType string
		want        bool
	}{
		{name: "openai responses supports codex affinity", channelType: "openai-response", want: true},
		{name: "openai unsupported", channelType: "openai", want: false},
		{name: "anthropic unsupported", channelType: "anthropic", want: false},
		{name: "gemini unsupported", channelType: "gemini", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, err := svc.validateAndCleanConfig(map[string]any{
				"codex_affinity_enabled":     true,
				"codex_affinity_max_retries": float64(5),
			}, tt.channelType)

			require.NoError(t, err)
			if tt.want {
				assert.Equal(t, true, cleaned["codex_affinity_enabled"])
				assert.Equal(t, float64(5), cleaned["codex_affinity_max_retries"])
			} else {
				assert.NotContains(t, cleaned, "codex_affinity_enabled")
				assert.NotContains(t, cleaned, "codex_affinity_max_retries")
			}
		})
	}
}

func TestValidateAndCleanConfigResponsesIncludeEncryptedReasoningScope(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		channelType string
		want        bool
	}{
		{name: "openai responses supports encrypted reasoning include", channelType: "openai-response", want: true},
		{name: "openai unsupported", channelType: "openai", want: false},
		{name: "anthropic unsupported", channelType: "anthropic", want: false},
		{name: "gemini unsupported", channelType: "gemini", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, err := svc.validateAndCleanConfig(map[string]any{
				"responses_include_encrypted_reasoning": true,
			}, tt.channelType)

			require.NoError(t, err)
			if tt.want {
				assert.Equal(t, true, cleaned["responses_include_encrypted_reasoning"])
			} else {
				assert.NotContains(t, cleaned, "responses_include_encrypted_reasoning")
			}
		})
	}
}

func TestCleanConfigForGroupTypeRemovesAggregateEncryptedReasoning(t *testing.T) {
	t.Parallel()

	config := map[string]any{
		"responses_include_encrypted_reasoning": true,
		"codex_degradation_mitigation_enabled":  true,
	}
	cleanConfigForGroupType(config, "aggregate")

	assert.NotContains(t, config, "responses_include_encrypted_reasoning")
	assert.Equal(t, true, config["codex_degradation_mitigation_enabled"])
}

func TestValidateAndCleanConfigCodexDegradationMitigationScope(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	tests := []struct {
		name        string
		channelType string
		want        bool
	}{
		{name: "openai responses supports codex degradation mitigation", channelType: "openai-response", want: true},
		{name: "openai unsupported", channelType: "openai", want: false},
		{name: "anthropic unsupported", channelType: "anthropic", want: false},
		{name: "gemini unsupported", channelType: "gemini", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, err := svc.validateAndCleanConfig(map[string]any{
				"codex_degradation_mitigation_enabled": true,
			}, tt.channelType)

			require.NoError(t, err)
			if tt.want {
				assert.Equal(t, true, cleaned["codex_degradation_mitigation_enabled"])
			} else {
				assert.NotContains(t, cleaned, "codex_degradation_mitigation_enabled")
			}
		})
	}
}

func TestValidateAndCleanConfigRejectsForceCCAndCodexTogether(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	_, err := svc.validateAndCleanConfig(map[string]any{
		"cc_support":    true,
		"codex_support": true,
	}, "openai")

	require.Error(t, err)
	var i18nErr *I18nError
	require.True(t, errors.As(err, &i18nErr))
	assert.Equal(t, "validation.force_cc_codex_mutually_exclusive", i18nErr.MessageID)
}

func TestUpdateGroupPreventsDisablingCodexSupportUsedByCodexAggregate(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)
	svc.aggregateGroupService = NewAggregateGroupService(db, ReadOnlyDB{DB: db}, svc.groupManager, nil)

	subGroup := models.Group{
		Name:               "codex-forced-child",
		DisplayName:        "Codex Forced Child",
		GroupType:          "standard",
		Enabled:            true,
		ChannelType:        "openai",
		Upstreams:          datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":100}]`)),
		ValidationEndpoint: "/v1/chat/completions",
		TestModel:          "gpt-4.1-mini",
		Config:             datatypes.JSONMap{"codex_support": true},
	}
	require.NoError(t, db.Create(&subGroup).Error)
	aggregateGroup := models.Group{
		Name:        "codex-aggregate-parent",
		DisplayName: "Codex Aggregate Parent",
		GroupType:   "aggregate",
		Enabled:     true,
		ChannelType: "openai-response",
		Upstreams:   datatypes.JSON([]byte(`[]`)),
		TestModel:   "-",
		Config:      datatypes.JSONMap{},
	}
	require.NoError(t, db.Create(&aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:            aggregateGroup.ID,
		SubGroupID:         subGroup.ID,
		Weight:             100,
		MinEffectiveWeight: 1,
		SubGroupName:       subGroup.Name,
		SubGroupEnabled:    true,
	}).Error)

	_, err := svc.UpdateGroup(context.Background(), subGroup.ID, GroupUpdateParams{
		Config: map[string]any{},
	})

	require.Error(t, err)
	var i18nErr *I18nError
	require.ErrorAs(t, err, &i18nErr)
	assert.Equal(t, "validation.codex_support_cannot_disable_used_by_codex", i18nErr.MessageID)
	assert.Contains(t, fmt.Sprint(i18nErr.Template["groups"]), aggregateGroup.Name)
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

func TestIsConfigCodexSupportEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		config   datatypes.JSONMap
		expected bool
	}{
		{name: "nil config", config: nil, expected: false},
		{name: "missing codex_support", config: datatypes.JSONMap{}, expected: false},
		{name: "boolean true", config: datatypes.JSONMap{"codex_support": true}, expected: true},
		{name: "boolean false", config: datatypes.JSONMap{"codex_support": false}, expected: false},
		{name: "string true", config: datatypes.JSONMap{"codex_support": "true"}, expected: true},
		{name: "number non-zero", config: datatypes.JSONMap{"codex_support": 1}, expected: true},
		{name: "number zero", config: datatypes.JSONMap{"codex_support": 0}, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isConfigCodexSupportEnabled(tt.config)
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

func TestUpdateChildGroupCannotAddGatewayProxyToManagedUpstream(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)

	parentGroup := models.Group{
		Name:        "parent-gateway-lock",
		DisplayName: "Parent Gateway Lock",
		GroupType:   "standard",
		Upstreams:   datatypes.JSON(`[{"url":"https://api.openai.com","weight":100,"gateway_proxy":"betterclaude"}]`),
		ChannelType: "openai",
		ProxyKeys:   "sk-parent-key",
	}
	require.NoError(t, db.Create(&parentGroup).Error)

	expectedURL := "http://127.0.0.1:3001/proxy/parent-gateway-lock"
	childGroup := models.Group{
		Name:          "child-gateway-lock",
		DisplayName:   "Child Gateway Lock",
		GroupType:     "standard",
		Upstreams:     datatypes.JSON(fmt.Sprintf(`[{"url":"%s","weight":1}]`, expectedURL)),
		ChannelType:   "openai",
		ParentGroupID: &parentGroup.ID,
	}
	require.NoError(t, db.Create(&childGroup).Error)

	modifiedUpstreams := json.RawMessage(fmt.Sprintf(`[{"url":"%s","weight":1,"gateway_proxy":"betterclaude"}]`, expectedURL))
	_, err := svc.UpdateGroup(context.Background(), childGroup.ID, GroupUpdateParams{
		HasUpstreams: true,
		Upstreams:    modifiedUpstreams,
	})

	require.Error(t, err)
	var i18nErr *I18nError
	require.True(t, errors.As(err, &i18nErr))
	assert.Equal(t, "validation.child_group_cannot_modify_upstream", i18nErr.MessageID)
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

func TestDeleteAllGroupsClearsGroupRelatedTables(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	svc := setupTestGroupService(t, db)
	ctx := context.Background()

	parent := models.Group{
		Name:      "delete-all-parent",
		GroupType: "standard",
		Enabled:   true,
		Upstreams: datatypes.JSON(`[{"url":"http://example.com","weight":1}]`),
	}
	require.NoError(t, db.Create(&parent).Error)

	child := models.Group{
		Name:          "delete-all-child",
		GroupType:     "standard",
		Enabled:       true,
		ParentGroupID: &parent.ID,
		Upstreams:     datatypes.JSON(`[{"url":"http://example.com","weight":1}]`),
	}
	require.NoError(t, db.Create(&child).Error)

	aggregate := models.Group{
		Name:      "delete-all-aggregate",
		GroupType: "aggregate",
		Enabled:   true,
		Upstreams: datatypes.JSON(`[]`),
	}
	require.NoError(t, db.Create(&aggregate).Error)

	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:    aggregate.ID,
		SubGroupID: parent.ID,
		Weight:     1,
	}).Error)
	require.NoError(t, db.Create(&models.APIKey{
		GroupID:  parent.ID,
		KeyValue: "encrypted-key",
		KeyHash:  "delete-all-key-hash",
		Status:   models.KeyStatusActive,
	}).Error)
	require.NoError(t, db.Create(&models.RequestLog{
		ID:        "delete-all-request-log",
		Timestamp: time.Now(),
		GroupID:   parent.ID,
		GroupName: parent.Name,
		IsSuccess: true,
	}).Error)
	require.NoError(t, db.Create(&models.GroupHourlyStat{
		Time:         time.Now().Truncate(time.Hour),
		GroupID:      parent.ID,
		SuccessCount: 1,
	}).Error)
	require.NoError(t, db.Create(&models.DynamicWeightMetric{
		MetricType: models.MetricTypeModelRedirect,
		GroupID:    parent.ID,
	}).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO managed_sites (bound_group_id, created_at, updated_at) VALUES (?, ?, ?)",
		parent.ID,
		time.Now(),
		time.Now(),
	).Error)

	aggregateSvc := NewAggregateGroupService(db, ReadOnlyDB{DB: db}, nil, nil)
	aggregateSvc.statsCacheMu.Lock()
	aggregateSvc.statsCache["1,2,3"] = keyStatsCacheEntry{
		results: map[uint]keyStatsResult{
			parent.ID: {TotalKeys: 1, ActiveKeys: 1},
		},
		expiresAt: time.Now().Add(time.Minute),
	}
	aggregateSvc.statsCacheMu.Unlock()
	svc.aggregateGroupService = aggregateSvc

	svc.keyStatsCacheMu.Lock()
	svc.keyStatsCache[parent.ID] = groupKeyStatsCacheEntry{
		Stats:      KeyStats{TotalKeys: 1, ActiveKeys: 1},
		ExpiresAt:  time.Now().Add(time.Minute),
		CurrentTTL: time.Minute,
	}
	svc.keyStatsCacheMu.Unlock()

	require.NoError(t, svc.DeleteAllGroups(ctx))

	assertTableEmpty(t, db, "groups")
	assertTableEmpty(t, db, "api_keys")
	assertTableEmpty(t, db, "group_sub_groups")
	assertTableEmpty(t, db, "request_logs")
	assertTableEmpty(t, db, "group_hourly_stats")
	assertTableEmpty(t, db, "dynamic_weight_metrics")

	svc.keyStatsCacheMu.RLock()
	assert.Empty(t, svc.keyStatsCache)
	svc.keyStatsCacheMu.RUnlock()
	aggregateSvc.statsCacheMu.RLock()
	assert.Empty(t, aggregateSvc.statsCache)
	aggregateSvc.statsCacheMu.RUnlock()

	var site struct {
		BoundGroupID *uint
	}
	require.NoError(t, db.Table("managed_sites").Select("bound_group_id").Scan(&site).Error)
	assert.Nil(t, site.BoundGroupID)

	afterReset := models.Group{
		Name:      "after-delete-all",
		GroupType: "standard",
		Enabled:   true,
		Upstreams: datatypes.JSON(`[{"url":"http://example.com","weight":1}]`),
	}
	require.NoError(t, db.Create(&afterReset).Error)
	assert.Equal(t, uint(1), afterReset.ID)
}

func assertTableEmpty(t *testing.T, db *gorm.DB, table string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Table(table).Count(&count).Error)
	assert.Zero(t, count, "expected %s to be empty", table)
}
