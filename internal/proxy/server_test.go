package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestDB creates an in-memory SQLite database for testing (pure Go, no CGO)
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger:      logger.Default.LogMode(logger.Silent),
		PrepareStmt: false,
	})
	require.NoError(t, err)

	// Limit connections for in-memory database
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	err = db.AutoMigrate(
		&models.APIKey{},
		&models.Group{},
		&models.RequestLog{},
		&models.GroupHourlyStat{},
	)
	require.NoError(t, err)

	return db
}

// setupTestProxyServer creates a test proxy server with minimal dependencies
func setupTestProxyServer(t *testing.T, db *gorm.DB) *ProxyServer {
	t.Helper()

	settingsManager := config.NewSystemSettingsManager()
	memStore := store.NewMemoryStore()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)

	keyProvider := keypool.NewProvider(db, memStore, settingsManager, encSvc)
	t.Cleanup(func() {
		keyProvider.Stop()
	})

	subGroupManager := services.NewSubGroupManager(memStore)
	groupManager := services.NewGroupManager(db, memStore, settingsManager, subGroupManager)

	clientManager := httpclient.NewHTTPClientManager()
	channelFactory := channel.NewFactory(settingsManager, clientManager)
	requestLogService := services.NewRequestLogService(db, memStore, settingsManager)

	ps, err := NewProxyServer(
		keyProvider,
		groupManager,
		subGroupManager,
		settingsManager,
		channelFactory,
		requestLogService,
		encSvc,
	)
	require.NoError(t, err)

	return ps
}

// createTestGroup creates a minimal valid group for testing
func createTestGroup(t *testing.T, db *gorm.DB, name string, channelType string) *models.Group {
	t.Helper()

	group := &models.Group{
		Name:        name,
		ChannelType: channelType,
		GroupType:   "standard",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
		Config:      map[string]any{"max_retries": 2, "request_timeout": 30},
	}
	err := db.Create(group).Error
	require.NoError(t, err)

	return group
}

// createTestKey creates a test API key for a group
func createTestKey(t *testing.T, db *gorm.DB, groupID uint, keyValue string, encSvc encryption.Service) *models.APIKey {
	t.Helper()

	encryptedKey, err := encSvc.Encrypt(keyValue)
	require.NoError(t, err)

	apiKey := &models.APIKey{
		GroupID:  groupID,
		KeyValue: encryptedKey,
		KeyHash:  encSvc.Hash(keyValue),
		Status:   models.KeyStatusActive,
	}
	err = db.Create(apiKey).Error
	require.NoError(t, err)

	return apiKey
}

func TestNewProxyServer(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	assert.NotNil(t, ps)
	assert.NotNil(t, ps.keyProvider)
	assert.NotNil(t, ps.groupManager)
	assert.NotNil(t, ps.subGroupManager)
	assert.NotNil(t, ps.settingsManager)
	assert.NotNil(t, ps.channelFactory)
	assert.NotNil(t, ps.requestLogService)
	assert.NotNil(t, ps.encryptionSvc)
}

func TestSetDynamicWeightManager(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)

	ps.SetDynamicWeightManager(dwm)

	assert.NotNil(t, ps.GetDynamicWeightManager())
	assert.Equal(t, dwm, ps.GetDynamicWeightManager())
}

func TestParseRetryConfigInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   map[string]any
		key      string
		expected int
	}{
		{
			name:     "nil_config",
			config:   nil,
			key:      "max_retries",
			expected: 0,
		},
		{
			name:     "missing_key",
			config:   map[string]any{},
			key:      "max_retries",
			expected: 0,
		},
		{
			name:     "float64_value",
			config:   map[string]any{"max_retries": float64(5)},
			key:      "max_retries",
			expected: 5,
		},
		{
			name:     "int_value",
			config:   map[string]any{"max_retries": 3},
			key:      "max_retries",
			expected: 3,
		},
		{
			name:     "string_value",
			config:   map[string]any{"max_retries": "7"},
			key:      "max_retries",
			expected: 7,
		},
		{
			name:     "negative_clamped",
			config:   map[string]any{"max_retries": -5},
			key:      "max_retries",
			expected: 0,
		},
		{
			name:     "over_100_clamped",
			config:   map[string]any{"max_retries": 150},
			key:      "max_retries",
			expected: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseRetryConfigInt(tt.config, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseMaxRetries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   map[string]any
		expected int
	}{
		{
			name:     "default_zero",
			config:   map[string]any{},
			expected: 0,
		},
		{
			name:     "configured_value",
			config:   map[string]any{"max_retries": 5},
			expected: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseMaxRetries(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsForceFunctionCallEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		group    *models.Group
		expected bool
	}{
		{
			name:     "nil_group",
			group:    nil,
			expected: false,
		},
		{
			name: "non_openai_channel",
			group: &models.Group{
				ChannelType: "anthropic",
				Config:      map[string]any{"force_function_call": true},
			},
			expected: false,
		},
		{
			name: "openai_enabled",
			group: &models.Group{
				ChannelType: "openai",
				Config:      map[string]any{"force_function_call": true},
			},
			expected: true,
		},
		{
			name: "openai_disabled",
			group: &models.Group{
				ChannelType: "openai",
				Config:      map[string]any{"force_function_call": false},
			},
			expected: false,
		},
		{
			name: "openai_legacy_key",
			group: &models.Group{
				ChannelType: "openai",
				Config:      map[string]any{"force_function_calling": true},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isForceFunctionCallEnabled(tt.group)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetParallelToolCallsConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		group    *models.Group
		expected *bool
	}{
		{
			name:     "nil_group",
			group:    nil,
			expected: nil,
		},
		{
			name: "not_configured",
			group: &models.Group{
				Config: map[string]any{},
			},
			expected: nil,
		},
		{
			name: "bool_true",
			group: &models.Group{
				Config: map[string]any{"parallel_tool_calls": true},
			},
			expected: boolPtr(true),
		},
		{
			name: "bool_false",
			group: &models.Group{
				Config: map[string]any{"parallel_tool_calls": false},
			},
			expected: boolPtr(false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := getParallelToolCallsConfig(tt.group)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestIsChatCompletionsEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		method   string
		expected bool
	}{
		{
			name:     "valid_endpoint",
			path:     "/v1/chat/completions",
			method:   http.MethodPost,
			expected: true,
		},
		{
			name:     "with_prefix",
			path:     "/proxy/test-group/v1/chat/completions",
			method:   http.MethodPost,
			expected: true,
		},
		{
			name:     "wrong_method",
			path:     "/v1/chat/completions",
			method:   http.MethodGet,
			expected: false,
		},
		{
			name:     "wrong_path",
			path:     "/v1/completions",
			method:   http.MethodPost,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isChatCompletionsEndpoint(tt.path, tt.method)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeProxyURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		proxyURL *string
		expected string
	}{
		{
			name:     "nil_pointer",
			proxyURL: nil,
			expected: "none",
		},
		{
			name:     "empty_string",
			proxyURL: strPtr(""),
			expected: "none",
		},
		{
			name:     "no_credentials",
			proxyURL: strPtr("http://proxy.example.com:8080"),
			expected: "http://proxy.example.com:8080",
		},
		{
			name:     "with_credentials",
			proxyURL: strPtr("http://user:password@proxy.example.com:8080"),
			expected: "http://%2A%2A%2A@proxy.example.com:8080", // URL-encoded by Go's URL parser
		},
		{
			name:     "invalid_url",
			proxyURL: strPtr("://invalid"),
			expected: "[invalid-url]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := safeProxyURL(tt.proxyURL)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRestoreOriginalPath(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		originalPath string
		currentPath  string
		expectedPath string
	}{
		{
			name:         "restore_needed",
			originalPath: "/v1/messages",
			currentPath:  "/v1/chat/completions",
			expectedPath: "/v1/messages",
		},
		{
			name:         "no_restore_needed",
			originalPath: "/v1/messages",
			currentPath:  "/v1/messages",
			expectedPath: "/v1/messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", tt.currentPath, nil)

			retryCtx := &retryContext{
				originalPath: tt.originalPath,
			}

			restoreOriginalPath(c, retryCtx)

			assert.Equal(t, tt.expectedPath, c.Request.URL.Path)
		})
	}
}

func TestCountAvailableSubGroups(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	// Create sub-groups
	subGroup1 := createTestGroup(t, db, "sub1", "openai")
	subGroup2 := createTestGroup(t, db, "sub2", "openai")
	subGroup3 := createTestGroup(t, db, "sub3", "openai")

	// Create aggregate group
	aggregateGroup := &models.Group{
		Name:        "aggregate",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`), // Required field
		SubGroups: []models.GroupSubGroup{
			{SubGroupID: subGroup1.ID, SubGroupEnabled: true, Weight: 100},
			{SubGroupID: subGroup2.ID, SubGroupEnabled: true, Weight: 100},
			{SubGroupID: subGroup3.ID, SubGroupEnabled: false, Weight: 100}, // Disabled
		},
	}
	err := db.Create(aggregateGroup).Error
	require.NoError(t, err)

	tests := []struct {
		name        string
		excludedIDs map[uint]bool
		expected    int
	}{
		{
			name:        "no_exclusions",
			excludedIDs: make(map[uint]bool),
			expected:    2, // Only enabled sub-groups
		},
		{
			name:        "one_excluded",
			excludedIDs: map[uint]bool{subGroup1.ID: true},
			expected:    1,
		},
		{
			name:        "all_excluded",
			excludedIDs: map[uint]bool{subGroup1.ID: true, subGroup2.ID: true},
			expected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := ps.countAvailableSubGroups(aggregateGroup, tt.excludedIDs)
			assert.Equal(t, tt.expected, count)
		})
	}
}

func TestEstimateTokensForClaudeCountTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     []byte
		minToken int
	}{
		{
			name:     "empty_body",
			body:     []byte{},
			minToken: 0,
		},
		{
			name:     "simple_message",
			body:     []byte(`{"messages":[{"role":"user","content":"Hello"}]}`),
			minToken: 1,
		},
		{
			name:     "with_system",
			body:     []byte(`{"system":"You are helpful","messages":[{"role":"user","content":"Hello"}]}`),
			minToken: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := estimateTokensForClaudeCountTokens(tt.body)
			assert.GreaterOrEqual(t, result, tt.minToken)
		})
	}
}

// Benchmark tests for hot paths
func BenchmarkParseRetryConfigInt(b *testing.B) {
	config := map[string]any{"max_retries": float64(5)}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = parseRetryConfigInt(config, "max_retries")
	}
}

func BenchmarkIsForceFunctionCallEnabled(b *testing.B) {
	group := &models.Group{
		ChannelType: "openai",
		Config:      map[string]any{"force_function_call": true},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = isForceFunctionCallEnabled(group)
	}
}

func BenchmarkSafeProxyURL(b *testing.B) {
	proxyURL := strPtr("http://user:password@proxy.example.com:8080")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = safeProxyURL(proxyURL)
	}
}

func BenchmarkEstimateTokensForClaudeCountTokens(b *testing.B) {
	body := []byte(`{"system":"You are a helpful assistant","messages":[{"role":"user","content":"Hello, how are you?"}],"tools":[{"name":"get_weather","description":"Get weather"}]}`)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = estimateTokensForClaudeCountTokens(body)
	}
}

func BenchmarkCountAvailableSubGroups(b *testing.B) {
	group := &models.Group{
		SubGroups: []models.GroupSubGroup{
			{SubGroupID: 1, SubGroupEnabled: true, Weight: 100},
			{SubGroupID: 2, SubGroupEnabled: true, Weight: 100},
			{SubGroupID: 3, SubGroupEnabled: false, Weight: 100},
			{SubGroupID: 4, SubGroupEnabled: true, Weight: 100},
		},
	}
	excludedIDs := map[uint]bool{1: true}

	// Create a minimal proxy server for the benchmark
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		b.Fatal(err)
	}
	settingsManager := config.NewSystemSettingsManager()
	memStore := store.NewMemoryStore()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	keyProvider := keypool.NewProvider(db, memStore, settingsManager, encSvc)
	defer keyProvider.Stop()

	subGroupManager := services.NewSubGroupManager(memStore)
	groupManager := services.NewGroupManager(db, memStore, settingsManager, subGroupManager)

	clientManager := httpclient.NewHTTPClientManager()
	channelFactory := channel.NewFactory(settingsManager, clientManager)
	requestLogService := services.NewRequestLogService(db, memStore, settingsManager)
	ps, err := NewProxyServer(keyProvider, groupManager, subGroupManager, settingsManager, channelFactory, requestLogService, encSvc)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ps.countAvailableSubGroups(group, excludedIDs)
	}
}

// Helper functions
func strPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

// TestGetMaxRequestSizeKB tests the GetMaxRequestSizeKB method
func TestGetMaxRequestSizeKB(t *testing.T) {
	tests := []struct {
		name          string
		preconditions map[string]any
		expected      int
	}{
		{
			name:          "nil preconditions",
			preconditions: nil,
			expected:      0,
		},
		{
			name:          "empty preconditions",
			preconditions: map[string]any{},
			expected:      0,
		},
		{
			name:          "missing max_request_size_kb",
			preconditions: map[string]any{"other_key": 100},
			expected:      0,
		},
		{
			name:          "float64 value",
			preconditions: map[string]any{"max_request_size_kb": float64(128)},
			expected:      128,
		},
		{
			name:          "int value",
			preconditions: map[string]any{"max_request_size_kb": 256},
			expected:      256,
		},
		{
			name:          "int64 value",
			preconditions: map[string]any{"max_request_size_kb": int64(512)},
			expected:      512,
		},
		{
			name:          "zero value",
			preconditions: map[string]any{"max_request_size_kb": 0},
			expected:      0,
		},
		{
			name:          "negative value (normalized to 0)",
			preconditions: map[string]any{"max_request_size_kb": -100},
			expected:      0,
		},
		{
			name:          "negative float64 (normalized to 0)",
			preconditions: map[string]any{"max_request_size_kb": float64(-50)},
			expected:      0,
		},
		{
			name:          "invalid type (string)",
			preconditions: map[string]any{"max_request_size_kb": "128"},
			expected:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := &models.Group{
				Preconditions: tt.preconditions,
			}
			result := group.GetMaxRequestSizeKB()
			assert.Equal(t, tt.expected, result)
		})
	}
}
