package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/store"
	"gpt-load/internal/tokenusage"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testChannelProxy struct {
	client *http.Client
	url    string
}

func (p *testChannelProxy) SelectUpstreamWithClients(_ *url.URL, _ string) (*channel.UpstreamSelection, error) {
	return &channel.UpstreamSelection{
		URL:          p.url,
		HTTPClient:   p.client,
		StreamClient: p.client,
	}, nil
}

func (p *testChannelProxy) BuildUpstreamURL(_ *url.URL, _ string) (string, error) {
	return p.url, nil
}

func (p *testChannelProxy) IsConfigStale(_ *models.Group) bool {
	return false
}

func (p *testChannelProxy) GetHTTPClient() *http.Client {
	return p.client
}

func (p *testChannelProxy) GetStreamClient() *http.Client {
	return p.client
}

func (p *testChannelProxy) ModifyRequest(_ *http.Request, _ *models.APIKey, _ *models.Group) {}

func (p *testChannelProxy) IsStreamRequest(_ *gin.Context, _ []byte) bool {
	return false
}

func (p *testChannelProxy) ExtractModel(_ *gin.Context, _ []byte) string {
	return ""
}

func (p *testChannelProxy) ValidateKey(_ context.Context, _ *models.APIKey, _ *models.Group) (bool, error) {
	return true, nil
}

func (p *testChannelProxy) ApplyModelRedirect(_ *http.Request, bodyBytes []byte, _ *models.Group) ([]byte, string, error) {
	return bodyBytes, "", nil
}

func (p *testChannelProxy) ApplyModelRedirectWithIndex(_ *http.Request, bodyBytes []byte, _ *models.Group) ([]byte, string, int, error) {
	return bodyBytes, "", -1, nil
}

func (p *testChannelProxy) TransformModelList(_ *http.Request, _ []byte, _ *models.Group) (map[string]any, error) {
	return nil, nil
}

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
		&models.GroupSubGroup{},
		&models.RequestLog{},
		&models.GroupHourlyStat{},
	)
	require.NoError(t, err)

	return db
}

// setupTestProxyServer creates a test proxy server with minimal dependencies
func setupTestProxyServer(t *testing.T, db *gorm.DB) *ProxyServer {
	t.Helper()

	ps, _ := setupTestProxyServerWithStore(t, db)
	return ps
}

func setupTestProxyServerWithStore(t *testing.T, db *gorm.DB) (*ProxyServer, store.Store) {
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

	return ps, memStore
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
			expected: true,
		},
		{
			name: "openai_response_enabled",
			group: &models.Group{
				ChannelType: "openai-response",
				Config:      map[string]any{"force_function_call": true},
			},
			expected: true,
		},
		{
			name: "gemini_stays_disabled",
			group: &models.Group{
				ChannelType: "gemini",
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

func TestFormatUpstreamAddrForLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		upstreamAddr string
		proxyURL     *string
		gatewayProxy string
		expected     string
	}{
		{
			name:         "no_proxy",
			upstreamAddr: "https://api.example.com/v1/chat/completions",
			expected:     "https://api.example.com/v1/chat/completions",
		},
		{
			name:         "manual_proxy",
			upstreamAddr: "https://api.example.com/v1/chat/completions",
			proxyURL:     strPtr("http://proxy.example.com:8080"),
			expected:     "https://api.example.com/v1/chat/completions (manual proxy: http://proxy.example.com:8080)",
		},
		{
			name:         "manual_proxy_redacts_credentials",
			upstreamAddr: "https://api.example.com/v1/chat/completions",
			proxyURL:     strPtr("http://user:password@proxy.example.com:8080"),
			expected:     "https://api.example.com/v1/chat/completions (manual proxy: http://%2A%2A%2A@proxy.example.com:8080)",
		},
		{
			name:         "gateway_proxy",
			upstreamAddr: "https://betterclau.de/openai/api.example.com/v1/chat/completions",
			gatewayProxy: "betterclaude",
			expected:     "https://betterclau.de/openai/api.example.com/v1/chat/completions (gateway proxy: betterclaude)",
		},
		{
			name:         "gateway_takes_precedence_over_manual",
			upstreamAddr: "https://betterclau.de/openai/api.example.com/v1/chat/completions",
			proxyURL:     strPtr("http://proxy.example.com:8080"),
			gatewayProxy: "betterclaude",
			expected:     "https://betterclau.de/openai/api.example.com/v1/chat/completions (gateway proxy: betterclaude)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatUpstreamAddrForLog(tt.upstreamAddr, tt.proxyURL, tt.gatewayProxy)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLogRequestRecordsProxyInfoInUpstreamAddr(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		upstreamAddr string
		proxyURL     *string
		gatewayProxy string
		expected     string
	}{
		{
			name:         "no proxy",
			upstreamAddr: "https://api.example.com/v1/models",
			expected:     "https://api.example.com/v1/models",
		},
		{
			name:         "manual proxy",
			upstreamAddr: "https://api.example.com/v1/models",
			proxyURL:     strPtr("http://user:password@proxy.example.com:8080"),
			expected:     "https://api.example.com/v1/models (manual proxy: http://%2A%2A%2A@proxy.example.com:8080)",
		},
		{
			name:         "gateway proxy",
			upstreamAddr: "https://betterclau.de/openai/api.example.com/v1/models",
			gatewayProxy: "betterclaude",
			expected:     "https://betterclau.de/openai/api.example.com/v1/models (gateway proxy: betterclaude)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			memStore := store.NewMemoryStore()
			ps := &ProxyServer{
				requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
			}
			group := &models.Group{
				ID:              1,
				Name:            "proxy-log-group",
				GroupType:       "standard",
				EffectiveConfig: types.SystemSettings{},
			}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/models", nil)

			ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, tt.upstreamAddr, tt.proxyURL, tt.gatewayProxy, nil, nil, models.RequestTypeFinal)

			logEntry := popRecordedRequestLog(t, memStore)
			assert.Equal(t, tt.expected, logEntry.UpstreamAddr)
			assert.NotContains(t, logEntry.UpstreamAddr, "password")
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

func TestClearForceProtocolContextClearsToolState(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCCEnabled, true)
	c.Set(ctxKeyOpenAIResponseCC, true)
	c.Set(ctxKeyGeminiCC, true)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	c.Set(ctxKeyOpenAIToolNameReverseMap, map[string]string{"short": "original"})
	c.Set(ctxKeyCodexToolNameReverseMap, map[string]string{"short": "original"})
	c.Set(ctxKeyCodexToolContext, newCodexToolContext([]CodexTool{{Type: "custom", Name: "exec"}}))

	clearForceProtocolContext(c)

	for _, key := range []string{
		ctxKeyCCEnabled,
		ctxKeyOpenAIResponseCC,
		ctxKeyGeminiCC,
		ctxKeyCodexEnabled,
		ctxKeyCodexUpstreamFormat,
		ctxKeyOpenAIToolNameReverseMap,
		ctxKeyCodexToolNameReverseMap,
		ctxKeyCodexToolContext,
	} {
		_, exists := c.Get(key)
		assert.False(t, exists, "expected %s to be cleared", key)
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

func TestShouldAbortOnIgnorableErrorRetriesUpstreamTimeoutWhenClientAlive(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/test/v1/chat/completions", nil)

	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	// Keep the request context alive; this branch covers upstream timeouts, not client cancellation.
	require.NoError(t, c.Request.Context().Err())

	err := errors.New("net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers)")
	require.True(t, app_errors.IsIgnorableError(err))
	assert.False(t, ps.shouldAbortOnIgnorableError(c, err))
}

func TestShouldAbortOnIgnorableErrorStopsWhenClientCanceled(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	parentCtx, cancel := context.WithCancel(context.Background())
	cancel()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(parentCtx, http.MethodPost, "/proxy/test/v1/chat/completions", nil)

	err := errors.New("request canceled")
	assert.True(t, ps.shouldAbortOnIgnorableError(c, err))
}

func TestEffectiveNonStreamRequestContextFallsBackForNonPositiveTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		cfg               types.SystemSettings
		expectImmediate   bool
		expectHasDeadline bool
	}{
		{
			name: "positive non-stream timeout uses deadline",
			cfg: types.SystemSettings{
				NonStreamRequestTimeout: 1,
				RequestTimeout:          0,
			},
			expectImmediate:   false,
			expectHasDeadline: true,
		},
		{
			name: "zero non-stream timeout falls back to legacy request timeout",
			cfg: types.SystemSettings{
				NonStreamRequestTimeout: 0,
				RequestTimeout:          1,
			},
			expectImmediate:   false,
			expectHasDeadline: true,
		},
		{
			name: "zero non-stream and legacy timeouts uses cancelable context",
			cfg: types.SystemSettings{
				NonStreamRequestTimeout: 0,
				RequestTimeout:          0,
			},
			expectImmediate:   false,
			expectHasDeadline: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := effectiveNonStreamRequestContext(context.Background(), tt.cfg)
			defer cancel()

			_, hasDeadline := ctx.Deadline()
			assert.Equal(t, tt.expectHasDeadline, hasDeadline)
			select {
			case <-ctx.Done():
				assert.True(t, tt.expectImmediate, "context should not be canceled immediately")
			default:
				assert.False(t, tt.expectImmediate, "context should be canceled immediately")
			}
		})
	}
}

func TestExecuteRequestWithRetryStopsWhenNonStreamLifecycleTimeoutExpires(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	group := createTestGroup(t, db, "timeout-retry", "openai")
	group.EffectiveConfig = systemSettingsWithRetryTimeout(1, 1)
	createTestKey(t, db, group.ID, "sk-timeout-retry-1", ps.encryptionSvc)
	createTestKey(t, db, group.ID, "sk-timeout-retry-2", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			time.Sleep(1200 * time.Millisecond)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/timeout-retry/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-test"}`)))

	client := &http.Client{Timeout: 3 * time.Second}
	ps.executeRequestWithRetry(c, &testChannelProxy{client: client, url: upstream.URL}, group, group, []byte(`{"model":"gpt-test"}`), false, time.Now(), 0)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Equal(t, int32(1), atomic.LoadInt32(&attempts))
}

func TestRetryDelayForAttemptUsesExponentialBackoffWithJitter(t *testing.T) {
	t.Parallel()

	assert.Zero(t, retryDelayForAttempt(types.SystemSettings{}, 0))
	assert.Equal(t, 100*time.Millisecond, retryDelayForAttempt(types.SystemSettings{
		RetryDelayMs: 100,
	}, 99))

	cfg := types.SystemSettings{
		RetryDelayMs:           100,
		RetryBackoffEnabled:    true,
		RetryBackoffMaxPercent: 500,
	}
	maxExtra := retryBackoffMaxExtra(100*time.Millisecond, 500)
	assert.Equal(t, 500*time.Millisecond, maxExtra)
	assert.Equal(t, time.Second, retryBackoffMaxExtra(200*time.Millisecond, 500))
	assert.Greater(t, retryBackoffExtraForAttempt(0, maxExtra), time.Duration(0))
	assert.Less(t, retryBackoffExtraForAttempt(0, maxExtra), 10*time.Millisecond)
	assert.InDelta(t, 207*time.Millisecond, retryBackoffExtraForAttempt(49, maxExtra), float64(5*time.Millisecond))
	assert.Less(t, retryBackoffExtraForAttempt(98, maxExtra), maxExtra)
	assert.Equal(t, maxExtra, retryBackoffExtraForAttempt(99, maxExtra))
	assert.Equal(t, maxExtra, retryBackoffExtraForAttempt(150, maxExtra))

	for range 20 {
		delay := retryDelayForAttempt(cfg, 2)
		assert.GreaterOrEqual(t, delay, 70*time.Millisecond)
		assert.LessOrEqual(t, delay, 160*time.Millisecond)
	}

	cappedCfg := types.SystemSettings{
		RetryDelayMs:           1000,
		RetryBackoffEnabled:    true,
		RetryBackoffMaxPercent: 500,
	}
	for range 20 {
		assert.LessOrEqual(t, retryDelayForAttempt(cappedCfg, 150), 6*time.Second)
	}
}

func TestCodexAggregateAffinityKeyReadsExistingCodexContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	group := &models.Group{
		Name:        "codex-aggregate",
		GroupType:   "aggregate",
		ChannelType: "openai-response",
		Config: map[string]any{
			"codex_affinity_enabled": true,
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"body-cache-key"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex-aggregate/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Originator", "codex_cli_rs")
	c.Request.Header.Set("Session_ID", "header-session")
	c.Request.Header.Set("Conversation_ID", "header-conversation")

	assert.Equal(t, "header-session", codexAggregateAffinityKey(c, group, body))
	assert.Equal(t, "header-session", c.Request.Header.Get("Session_ID"))
	assert.JSONEq(t, `{"model":"gpt-5.4","prompt_cache_key":"body-cache-key"}`, string(body))
}

func TestCodexAggregateAffinityKeyFallsBackToPromptCacheKey(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	group := &models.Group{
		Name:        "codex-aggregate",
		GroupType:   "aggregate",
		ChannelType: "openai-response",
		Config: map[string]any{
			"codex_affinity_enabled": true,
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"body-cache-key"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex-aggregate/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", buildCodexUserAgent("0.150.1"))

	assert.Equal(t, "body-cache-key", codexAggregateAffinityKey(c, group, body))
}

func TestCodexAggregateAffinityKeyWithDegradationMitigationEnabled(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	group := &models.Group{
		Name:        "codex-aggregate",
		GroupType:   "aggregate",
		ChannelType: "openai-response",
		Config: map[string]any{
			"codex_affinity_enabled":               true,
			"codex_degradation_mitigation_enabled": true,
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-5.4","stream":true,"prompt_cache_key":"body-cache-key"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex-aggregate/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Originator", "codex_cli_rs")
	c.Request.Header.Set("Session_ID", "header-session")

	assert.Equal(t, "header-session", codexAggregateAffinityKey(c, group, body))
}

func TestCodexAggregateAffinityKeyDisabledForNonCodexAggregate(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	group := &models.Group{
		Name:        "plain-aggregate",
		GroupType:   "aggregate",
		ChannelType: "openai",
		Config: map[string]any{
			"codex_affinity_enabled": true,
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"prompt_cache_key":"body-cache-key"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/plain-aggregate/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Session_ID", "header-session")

	assert.Empty(t, codexAggregateAffinityKey(c, group, body))
}

func TestExecuteRequestWithRetryWaitsConfiguredDelayBeforeRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	group := createTestGroup(t, db, "retry-delay-standard", "openai")
	group.EffectiveConfig = systemSettingsWithRetryTimeout(1, 0)
	group.EffectiveConfig.RetryDelayMs = 120
	group.EffectiveConfig.RetryBackoffEnabled = false
	createTestKey(t, db, group.ID, "sk-retry-delay-1", ps.encryptionSvc)
	createTestKey(t, db, group.ID, "sk-retry-delay-2", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	var mu sync.Mutex
	attemptTimes := make([]time.Time, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptTimes = append(attemptTimes, time.Now())
		attempt := len(attemptTimes)
		mu.Unlock()

		if attempt == 1 {
			http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-test"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/retry-delay-standard/v1/chat/completions", bytes.NewReader(body))

	ps.executeRequestWithRetry(c, &testChannelProxy{client: upstream.Client(), url: upstream.URL}, group, group, body, false, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, attemptTimes, 2)
	assert.GreaterOrEqual(t, attemptTimes[1].Sub(attemptTimes[0]), 70*time.Millisecond)
}

func TestExecuteRequestWithRetryKeepsRetryDelayInsideNonStreamTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	group := createTestGroup(t, db, "retry-delay-timeout-standard", "openai")
	group.EffectiveConfig = systemSettingsWithRetryTimeout(1, 1)
	group.EffectiveConfig.RetryDelayMs = 1500
	createTestKey(t, db, group.ID, "sk-retry-delay-timeout-1", ps.encryptionSvc)
	createTestKey(t, db, group.ID, "sk-retry-delay-timeout-2", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
	}))
	t.Cleanup(upstream.Close)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-test"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/retry-delay-timeout-standard/v1/chat/completions", bytes.NewReader(body))

	start := time.Now()
	ps.executeRequestWithRetry(c, &testChannelProxy{client: upstream.Client(), url: upstream.URL}, group, group, body, false, start, 0)

	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Less(t, time.Since(start), 1400*time.Millisecond)
}

func TestExecuteRequestWithRetrySanitizesIgnorableAbortLogError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	memStore := store.NewMemoryStore()
	ps.requestLogService = services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager())

	group := createTestGroup(t, db, "abort-log-sanitize", "openai")
	group.Config = map[string]any{"max_retries": 0}
	group.EffectiveConfig = systemSettingsWithRetryTimeout(0, 0)
	createTestKey(t, db, group.ID, "sk-abort-log-sanitize", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("request should be canceled before reaching upstream")
	}))
	t.Cleanup(upstream.Close)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	parentCtx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/abort-log-sanitize/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-test"}`))).WithContext(parentCtx)

	upstreamURL := upstream.URL + "/v1/chat/completions?key=plain-secret&x-goog-api-key=goog-secret"
	ps.executeRequestWithRetry(c, &testChannelProxy{client: upstream.Client(), url: upstreamURL}, group, group, []byte(`{"model":"gpt-test"}`), false, time.Now(), 0)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, 499, logEntry.StatusCode)
	assert.Contains(t, logEntry.ErrorMessage, "key=[REDACTED]")
	assert.Contains(t, logEntry.ErrorMessage, "x-goog-api-key=[REDACTED]")
	assert.NotContains(t, logEntry.ErrorMessage, "plain-secret")
	assert.NotContains(t, logEntry.ErrorMessage, "goog-secret")
}

func TestExecuteRequestWithRetrySimulatedClientPreservesRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	group := createTestGroup(t, db, "simulate-body-passthrough", "anthropic")
	group.Config = map[string]any{"simulated_client": "claude_code"}
	createTestKey(t, db, group.ID, "sk-simulated-body-passthrough", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"client-owned"},"messages":[{"role":"user","content":"hello"}]}`)
	receivedBody := make(chan []byte, 1)
	receivedDangerousHeader := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedBody <- got
		receivedDangerousHeader <- r.Header.Get("Anthropic-Dangerous-Direct-Browser-Access")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/simulate-body-passthrough/v1/messages", bytes.NewReader(body))

	client := &http.Client{Timeout: 3 * time.Second}
	ps.executeRequestWithRetry(c, &testChannelProxy{client: client, url: upstream.URL}, group, group, body, false, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, body, <-receivedBody)
	assert.Equal(t, "true", <-receivedDangerousHeader)
}

func TestExecuteRequestWithRetrySimulatedCodexPreservesRequestModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	group := createTestGroup(t, db, "simulate-codex-model-passthrough", "openai-response")
	group.Config = map[string]any{
		"max_retries":             0,
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
	}
	createTestKey(t, db, group.ID, "sk-simulated-codex-model-passthrough", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	receivedBody := make(chan []byte, 1)
	receivedUserAgent := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedBody <- got
		receivedUserAgent <- r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/simulate-codex-model-passthrough/v1/responses", bytes.NewReader(body))

	client := &http.Client{Timeout: 3 * time.Second}
	ps.executeRequestWithRetry(c, &testChannelProxy{client: client, url: upstream.URL}, group, group, body, false, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, string(body), string(<-receivedBody))
	assert.Equal(t, buildCodexUserAgent("0.150.1"), <-receivedUserAgent)
}

func TestExecuteRequestWithRetryCodexCCModeUsesConfiguredSimulatedVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	group := createTestGroup(t, db, "codex-cc-version-sync", "openai-response")
	group.Config = map[string]any{
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
	}
	createTestKey(t, db, group.ID, "sk-codex-cc-version-sync", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	receivedUserAgent := make(chan string, 1)
	receivedVersion := make(chan string, 1)
	receivedOriginator := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent <- r.Header.Get("User-Agent")
		receivedVersion <- r.Header.Get("Version")
		receivedOriginator <- r.Header.Get("originator")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex-cc-version-sync/v1/responses", bytes.NewReader(body))
	c.Set(ctxKeyOpenAIResponseCC, true)

	client := &http.Client{Timeout: 3 * time.Second}
	ps.executeRequestWithRetry(c, &testChannelProxy{client: client, url: upstream.URL}, group, group, body, true, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, buildCodexUserAgent("0.150.1"), <-receivedUserAgent)
	assert.Equal(t, "0.150.1", <-receivedVersion)
	assert.Equal(t, "codex_cli_rs", <-receivedOriginator)
}

func TestExecuteRequestWithRetryForceStreamSendsStreamTrueToResponsesUpstream(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedBody <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"gpt-5\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "codex-force-stream", "openai-response")
	group.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	group.Config = map[string]any{
		"blacklist_threshold":                   100,
		"force_stream":                          true,
		"responses_include_encrypted_reasoning": true,
		"simulated_client":                      "codex",
		"simulated_codex_version":               "0.150.1",
	}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-codex-force-stream", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	body := []byte(`{"model":"gpt-5","input":"hello","stream":false}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex-force-stream/v1/responses", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: group.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusOK, w.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &payload))
	assert.Equal(t, true, payload["stream"])
}

func TestHandleProxyForceCodexCompactMarksOpenAIResponseMode(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "codex-compact-openai-response", "openai-response")
	group.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-codex-compact-openai-response", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	body := []byte(`{"model":"gpt-5","input":[],"prompt_cache_key":"compact-key"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+group.Name+"/codex/v1/responses/compact", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: group.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, isCodexEnabled(c))
	assert.Equal(t, codexUpstreamResponses, getCodexUpstreamFormat(c))
	assert.Equal(t, "/v1/responses/compact", <-receivedPath)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &payload))
	assert.NotContains(t, payload, "stream")
}

func TestHandleProxyAggregateForceCodexCompactMarksOpenAIResponseMode(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "codex-compact-aggregate-sub", "openai-response")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	require.NoError(t, db.Save(subGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "codex-compact-aggregate",
		ChannelType: "openai-response",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config:      map[string]any{"max_retries": 0},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      subGroup.ID,
		SubGroupName:    subGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-codex-compact-aggregate", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	body := []byte(`{"model":"gpt-5","input":[],"prompt_cache_key":"compact-key"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/codex/v1/responses/compact", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: aggregateGroup.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, isCodexEnabled(c))
	assert.Equal(t, codexUpstreamResponses, getCodexUpstreamFormat(c))
	assert.Equal(t, "/v1/responses/compact", <-receivedPath)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &payload))
	assert.NotContains(t, payload, "stream")
}

func TestExecuteRequestWithRetrySanitizesUpstreamHTTPError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid key x-goog-api-key=goog-secret-value","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "sanitize-upstream-http-error", "openai")
	group.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	group.Config = map[string]any{
		"max_retries":         0,
		"blacklist_threshold": 100,
	}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-sanitize-upstream-http-error", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+group.Name+"/v1/chat/completions", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: group.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.NotContains(t, w.Body.String(), "goog-secret-value")
	assert.Contains(t, w.Body.String(), "[REDACTED]")

	logEntry := popRecordedRequestLog(t, memStore)
	assert.NotContains(t, logEntry.ErrorMessage, "goog-secret-value")
	assert.Contains(t, logEntry.ErrorMessage, "[REDACTED]")
}

func TestExecuteRequestWithRetryLogsClientAndUpstreamUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	group := createTestGroup(t, db, "simulated-ua-log", "openai-response")
	group.Config = map[string]any{
		"max_retries":             0,
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
	}
	group.EffectiveConfig = types.SystemSettings{EnableRequestBodyLogging: true}
	createTestKey(t, db, group.ID, "test-key-simulated-ua-log", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/simulated-ua-log/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", "client-before/1.0")

	client := &http.Client{Timeout: 3 * time.Second}
	ps.executeRequestWithRetry(c, &testChannelProxy{client: client, url: upstream.URL}, group, group, body, false, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, "client-before/1.0", logEntry.UserAgent)
	assert.Equal(t, buildCodexUserAgent("0.150.1"), logEntry.UpstreamUserAgent)
	assert.True(t, logEntry.SimulatedClientEnabled)
}

func TestExecuteRequestWithRetryLogsUpstreamUserAgentWhenSimulatedClientAlreadyInbound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	group := createTestGroup(t, db, "simulated-ua-log-same", "openai-response")
	group.Config = map[string]any{
		"max_retries":             0,
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
	}
	group.EffectiveConfig = types.SystemSettings{EnableRequestBodyLogging: true}
	createTestKey(t, db, group.ID, "test-key-simulated-ua-log-same", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/simulated-ua-log-same/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", buildCodexUserAgent("0.150.1"))

	client := &http.Client{Timeout: 3 * time.Second}
	ps.executeRequestWithRetry(c, &testChannelProxy{client: client, url: upstream.URL}, group, group, body, false, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, buildCodexUserAgent("0.150.1"), logEntry.UserAgent)
	assert.Equal(t, buildCodexUserAgent("0.150.1"), logEntry.UpstreamUserAgent)
	assert.True(t, logEntry.SimulatedClientEnabled)
}

func TestLogRequestTruncatesUserAgentFieldsToColumnLimit(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{
		ID:   1,
		Name: "test-group",
		EffectiveConfig: types.SystemSettings{
			EnableRequestBodyLogging: true,
		},
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	clientSecret := strings.Repeat("b", 32)
	upstreamEmail := "operator@example.invalid"
	ctx.Request.Header.Set("User-Agent", "Bearer "+clientSecret+strings.Repeat("入", requestLogUserAgentMaxRunes+5))
	ctx.Set(ctxKeyUpstreamUserAgent, upstreamEmail+strings.Repeat("上", requestLogUserAgentMaxRunes+5))

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "", nil, "", nil, []byte(`{"model":"gpt-4o"}`), models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, requestLogUserAgentMaxRunes, utf8.RuneCountInString(logEntry.UserAgent))
	assert.Equal(t, requestLogUserAgentMaxRunes, utf8.RuneCountInString(logEntry.UpstreamUserAgent))
	assert.NotContains(t, logEntry.UserAgent, clientSecret)
	assert.NotContains(t, logEntry.UpstreamUserAgent, upstreamEmail)
	assert.Contains(t, logEntry.UserAgent, "Bearer [REDACTED]")
	assert.Contains(t, logEntry.UpstreamUserAgent, "[REDACTED_EMAIL]")
	assert.False(t, logEntry.SimulatedClientEnabled)
}

func TestLogRequestSanitizesCapturedResponseBodyBeforeTruncation(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{
		ID:   1,
		Name: "test-group",
		EffectiveConfig: types.SystemSettings{
			EnableRequestBodyLogging: true,
		},
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstreamKey := "s" + "k-" + strings.Repeat("c", 32)
	ctx.Set("response_body", `{"error":"bad upstream key","api_key":"`+upstreamKey+`"}`)

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusBadGateway, errors.New("upstream failed"), false, "", nil, "", nil, []byte(`{"model":"gpt-4o"}`), models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.NotContains(t, logEntry.ResponseBody, upstreamKey)
	assert.Contains(t, logEntry.ResponseBody, `"api_key": "[REDACTED]"`)
}

func TestExecuteRequestWithRetryPreservesCodexHeadersThroughTwoProxyLayers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	firstProxy := setupTestProxyServer(t, db)
	secondProxy := setupTestProxyServer(t, db)

	firstGroup := createTestGroup(t, db, "codex-layer-one", "openai-response")
	firstGroup.Config = map[string]any{"max_retries": 0}
	secondGroup := createTestGroup(t, db, "codex-layer-two", "openai-response")
	secondGroup.Config = map[string]any{"max_retries": 0}
	createTestKey(t, db, firstGroup.ID, "test-key-codex-layer-one", firstProxy.encryptionSvc)
	createTestKey(t, db, secondGroup.ID, "test-key-codex-layer-two", secondProxy.encryptionSvc)
	require.NoError(t, firstProxy.keyProvider.LoadKeysFromDB())
	require.NoError(t, secondProxy.keyProvider.LoadKeysFromDB())

	finalHeaders := make(chan http.Header, 1)
	finalUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(finalUpstream.Close)

	secondLayer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest(r.Method, r.URL.RequestURI(), bytes.NewReader(body))
		ctx.Request.Header = r.Header.Clone()
		secondProxy.executeRequestWithRetry(ctx, &testChannelProxy{
			client: &http.Client{Timeout: 3 * time.Second},
			url:    finalUpstream.URL,
		}, secondGroup, secondGroup, body, false, time.Now(), 0)
	}))
	t.Cleanup(secondLayer.Close)

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/codex-layer-one/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", buildCodexUserAgent("0.150.1"))
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Request.Header.Set("Version", "0.150.1")
	c.Request.Header.Set("originator", "codex_cli_rs")
	c.Request.Header.Set("Session_ID", "client-session")
	c.Request.Header.Set("Conversation_ID", "client-conversation")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"source":"client"}`)
	c.Request.Header.Set("X-Codex-Beta-Features", "client-beta")
	c.Request.Header.Set("x-client-request-id", "client-request")
	c.Request.Header.Set("x-codex-window-id", "client-window")

	firstProxy.executeRequestWithRetry(c, &testChannelProxy{
		client: &http.Client{Timeout: 3 * time.Second},
		url:    secondLayer.URL + "/proxy/codex-layer-two/v1/responses",
	}, firstGroup, firstGroup, body, false, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	var headers http.Header
	select {
	case headers = <-finalHeaders:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for final upstream headers")
	}
	assert.Equal(t, buildCodexUserAgent("0.150.1"), headers.Get("User-Agent"))
	assert.Equal(t, "responses=experimental", headers.Get("OpenAI-Beta"))
	assert.Equal(t, "0.150.1", headers.Get("Version"))
	assert.Equal(t, "codex_cli_rs", headers.Get("originator"))
	assert.Equal(t, "client-session", headers.Get("Session_ID"))
	assert.Equal(t, "client-conversation", headers.Get("Conversation_ID"))
	assert.Equal(t, `{"source":"client"}`, headers.Get("X-Codex-Turn-Metadata"))
	assert.Equal(t, "client-beta", headers.Get("X-Codex-Beta-Features"))
	assert.Equal(t, "client-request", headers.Get("x-client-request-id"))
	assert.Equal(t, "client-window", headers.Get("x-codex-window-id"))
}

func TestExecuteRequestWithRetrySimulatedCodexSurvivesTwoProxyLayers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	firstProxy := setupTestProxyServer(t, db)
	secondProxy := setupTestProxyServer(t, db)

	firstGroup := createTestGroup(t, db, "sim-codex-layer-one", "openai-response")
	firstGroup.Config = map[string]any{
		"max_retries":             0,
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
	}
	secondGroup := createTestGroup(t, db, "sim-codex-layer-two", "openai-response")
	secondGroup.Config = map[string]any{"max_retries": 0}
	createTestKey(t, db, firstGroup.ID, "test-key-sim-codex-layer-one", firstProxy.encryptionSvc)
	createTestKey(t, db, secondGroup.ID, "test-key-sim-codex-layer-two", secondProxy.encryptionSvc)
	require.NoError(t, firstProxy.keyProvider.LoadKeysFromDB())
	require.NoError(t, secondProxy.keyProvider.LoadKeysFromDB())

	finalHeaders := make(chan http.Header, 1)
	finalUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(finalUpstream.Close)

	secondLayer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest(r.Method, r.URL.RequestURI(), bytes.NewReader(body))
		ctx.Request.Header = r.Header.Clone()
		secondProxy.executeRequestWithRetry(ctx, &testChannelProxy{
			client: &http.Client{Timeout: 3 * time.Second},
			url:    finalUpstream.URL,
		}, secondGroup, secondGroup, body, false, time.Now(), 0)
	}))
	t.Cleanup(secondLayer.Close)

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/sim-codex-layer-one/v1/responses", bytes.NewReader(body))

	firstProxy.executeRequestWithRetry(c, &testChannelProxy{
		client: &http.Client{Timeout: 3 * time.Second},
		url:    secondLayer.URL + "/proxy/sim-codex-layer-two/v1/responses",
	}, firstGroup, firstGroup, body, false, time.Now(), 0)

	require.Equal(t, http.StatusOK, w.Code)
	var headers http.Header
	select {
	case headers = <-finalHeaders:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for final upstream headers")
	}
	assert.Equal(t, buildCodexUserAgent("0.150.1"), headers.Get("User-Agent"))
	assert.Equal(t, "responses=experimental", headers.Get("OpenAI-Beta"))
	assert.Equal(t, "0.150.1", headers.Get("Version"))
	assert.Equal(t, "codex_cli_rs", headers.Get("Originator"))
	assert.Equal(t, "terminal_resize_reflow", headers.Get("X-Codex-Beta-Features"))
	assert.NotEmpty(t, headers.Get("X-Codex-Turn-Metadata"))
	assert.NotEmpty(t, headers.Get("X-Codex-Window-Id"))
	assert.Empty(t, headers.Get("x-client-request-id"))
	assert.Empty(t, headers.Get("Session-Id"))
	assert.Empty(t, headers.Get("Thread-Id"))
}

func TestExecuteRequestWithAggregateRetryStopsWhenNonStreamLifecycleTimeoutExpires(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	var slowAttempts int32
	firstSlowAttemptStarted := make(chan struct{})
	var signalFirstSlowAttempt sync.Once
	t.Cleanup(func() {
		signalFirstSlowAttempt.Do(func() {
			close(firstSlowAttemptStarted)
		})
	})
	slowUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&slowAttempts, 1)
		signalFirstSlowAttempt.Do(func() {
			close(firstSlowAttemptStarted)
		})
		time.Sleep(1200 * time.Millisecond)
	}))
	t.Cleanup(slowUpstream.Close)

	var fastAttempts int32
	fastUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fastAttempts, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(fastUpstream.Close)

	slowGroup := createTestGroup(t, db, "agg-timeout-slow", "openai")
	slowGroup.Upstreams = []byte(`[{"url":"` + slowUpstream.URL + `","weight":100}]`)
	slowGroup.Config = map[string]any{
		"max_retries":                0,
		"non_stream_request_timeout": 1,
		"stream_request_timeout":     0,
		"blacklist_threshold":        100,
	}
	require.NoError(t, db.Save(slowGroup).Error)

	fastGroup := createTestGroup(t, db, "agg-timeout-fast", "openai")
	fastGroup.Upstreams = []byte(`[{"url":"` + fastUpstream.URL + `","weight":100}]`)
	fastGroup.Config = map[string]any{
		"max_retries":                0,
		"non_stream_request_timeout": 1,
		"stream_request_timeout":     0,
		"blacklist_threshold":        100,
	}
	require.NoError(t, db.Save(fastGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-timeout",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"max_retries": 1,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:    aggregateGroup.ID,
		SubGroupID: slowGroup.ID,
		Weight:     100,
	}).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:    aggregateGroup.ID,
		SubGroupID: fastGroup.ID,
		Weight:     100,
	}).Error)

	createTestKey(t, db, slowGroup.ID, "sk-agg-timeout-slow", ps.encryptionSvc)
	fastKey := createTestKey(t, db, fastGroup.ID, "sk-agg-timeout-fast", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, memStore.Delete(activeKeysListKeyForTest(uint64(fastGroup.ID))))
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName("agg-timeout")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/agg-timeout/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-test"}`)))

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   []byte(`{"model":"gpt-test"}`),
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}
	go func() {
		<-firstSlowAttemptStarted
		// Test scaffolding: keep fastGroup unavailable for the first selection, then restore fastKey outside upstream handlers.
		_ = memStore.LPush(activeKeysListKeyForTest(uint64(fastGroup.ID)), uint64(fastKey.ID))
	}()
	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, retryCtx.originalBodyBytes, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Equal(t, int32(1), atomic.LoadInt32(&slowAttempts))
	require.Equal(t, int32(0), atomic.LoadInt32(&fastAttempts))
}

func TestExecuteRequestWithAggregateRetryUsesEffectiveStreamModeForLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		time.Sleep(1200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "agg-effective-mode-sub", "openai")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	subGroup.Config = map[string]any{
		"max_retries":                0,
		"force_non_stream":           true,
		"stream_request_timeout":     1,
		"non_stream_request_timeout": 2,
		"blacklist_threshold":        100,
	}
	require.NoError(t, db.Save(subGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-effective-mode",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"max_retries":                0,
			"stream_request_timeout":     1,
			"non_stream_request_timeout": 2,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      subGroup.ID,
		SubGroupName:    subGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-agg-effective-mode", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-test","stream":true}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/chat/completions", bytes.NewReader(body))
	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, true, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts))
}

func TestExecuteRequestWithAggregateRetryAppliesOnlySelectedSubGroupSimulatedClient(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	receivedUserAgent := make(chan string, 1)
	receivedVersion := make(chan string, 1)
	receivedOriginator := make(chan string, 1)
	receivedAnthropicVersion := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent <- r.Header.Get("User-Agent")
		receivedVersion <- r.Header.Get("Version")
		receivedOriginator <- r.Header.Get("originator")
		receivedAnthropicVersion <- r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "agg-sim-sub", "openai-response")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	subGroup.Config = map[string]any{
		"max_retries":             0,
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
		"blacklist_threshold":     100,
	}
	require.NoError(t, db.Save(subGroup).Error)

	otherSubGroup := createTestGroup(t, db, "agg-sim-other", "openai-response")
	otherSubGroup.Upstreams = []byte(`[{"url":"https://placeholder-other.example","weight":100}]`)
	otherSubGroup.Config = map[string]any{
		"max_retries":                   0,
		"simulated_client":              "claude_code",
		"simulated_claude_code_version": "9.9.9",
		"blacklist_threshold":           100,
	}
	require.NoError(t, db.Save(otherSubGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-simulated-selected-only",
		ChannelType: "openai-response",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"simulated_client": "claude_code",
			"max_retries":      0,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      subGroup.ID,
		SubGroupName:    subGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      otherSubGroup.ID,
		SubGroupName:    otherSubGroup.Name,
		SubGroupEnabled: true,
		Weight:          1,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-agg-sim-sub", ps.encryptionSvc)
	createTestKey(t, db, otherSubGroup.ID, "sk-agg-sim-other", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, memStore.Delete(activeKeysListKeyForTest(uint64(otherSubGroup.ID))))
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/responses", bytes.NewReader(body))

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, buildCodexUserAgent("0.150.1"), <-receivedUserAgent)
	assert.Equal(t, "0.150.1", <-receivedVersion)
	assert.Equal(t, "codex_cli_rs", <-receivedOriginator)
	assert.Empty(t, <-receivedAnthropicVersion)
}

func TestExecuteRequestWithAggregateRetryWaitsBeforeSameSubGroupKeyRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	var mu sync.Mutex
	attemptTimes := make([]time.Time, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptTimes = append(attemptTimes, time.Now())
		attempt := len(attemptTimes)
		mu.Unlock()

		if attempt == 1 {
			http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "agg-retry-delay-sub", "openai")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	subGroup.Config = map[string]any{
		"max_retries":         1,
		"retry_delay_ms":      120,
		"blacklist_threshold": 100,
	}
	require.NoError(t, db.Save(subGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-retry-delay",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"max_retries": 0,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      subGroup.ID,
		SubGroupName:    subGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-agg-retry-delay-1", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-agg-retry-delay-2", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-test"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/chat/completions", bytes.NewReader(body))
	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, attemptTimes, 2)
	assert.GreaterOrEqual(t, attemptTimes[1].Sub(attemptTimes[0]), 70*time.Millisecond)
}

func TestExecuteRequestWithAggregateRetryKeepsSubGroupDelayInsideNonStreamTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "agg-retry-delay-timeout-sub", "openai")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	subGroup.Config = map[string]any{
		"max_retries":                1,
		"retry_delay_ms":             1500,
		"non_stream_request_timeout": 1,
		"blacklist_threshold":        100,
	}
	require.NoError(t, db.Save(subGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-retry-delay-timeout",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"max_retries": 0,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      subGroup.ID,
		SubGroupName:    subGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-agg-retry-delay-timeout-1", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-agg-retry-delay-timeout-2", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"gpt-test"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/chat/completions", bytes.NewReader(body))
	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	start := time.Now()
	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, start, retryCtx)

	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Less(t, time.Since(start), 1400*time.Millisecond)
}

func TestExecuteRequestWithAggregateRetryClearsSimulatedClientHeadersBetweenSubGroups(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	var attempts int32
	type receivedHeaders struct {
		userAgent        string
		version          string
		originator       string
		anthropicVersion string
	}
	receivedHeadersCh := make(chan receivedHeaders, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeadersCh <- receivedHeaders{
			userAgent:        r.Header.Get("User-Agent"),
			version:          r.Header.Get("Version"),
			originator:       r.Header.Get("originator"),
			anthropicVersion: r.Header.Get("anthropic-version"),
		}
		if atomic.AddInt32(&attempts, 1) == 1 {
			http.Error(w, `{"error":"fail first subgroup"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	failingSubGroup := createTestGroup(t, db, "agg-sim-fail", "openai-response")
	failingSubGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	failingSubGroup.Config = map[string]any{
		"max_retries":             0,
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
		"blacklist_threshold":     100,
	}
	require.NoError(t, db.Save(failingSubGroup).Error)

	successSubGroup := createTestGroup(t, db, "agg-sim-pass", "anthropic")
	successSubGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	successSubGroup.Config = map[string]any{
		"max_retries":                   0,
		"simulated_client":              "claude_code",
		"simulated_claude_code_version": "2.3.4",
		"blacklist_threshold":           100,
	}
	require.NoError(t, db.Save(successSubGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-simulated-switch",
		ChannelType: "openai-response",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"max_retries": 1,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      failingSubGroup.ID,
		SubGroupName:    failingSubGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      successSubGroup.ID,
		SubGroupName:    successSubGroup.Name,
		SubGroupEnabled: true,
		Weight:          1,
	}).Error)

	createTestKey(t, db, failingSubGroup.ID, "sk-agg-sim-fail", ps.encryptionSvc)
	createTestKey(t, db, successSubGroup.ID, "sk-agg-sim-pass", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/messages", bytes.NewReader(body))

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	got := []receivedHeaders{<-receivedHeadersCh, <-receivedHeadersCh}
	want := []receivedHeaders{
		{
			userAgent:  buildCodexUserAgent("0.150.1"),
			version:    "0.150.1",
			originator: "codex_cli_rs",
		},
		{
			userAgent:        buildClaudeCodeUserAgent("2.3.4"),
			anthropicVersion: "2023-06-01",
		},
	}
	assert.ElementsMatch(t, want, got)
}

func TestExecuteRequestWithAggregateRetryLogsSimulatedClientEnabledForSelectedSubGroupOnly(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	selectedSubGroup := createTestGroup(t, db, "agg-log-sim-selected", "openai-response")
	selectedSubGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	selectedSubGroup.Config = map[string]any{
		"max_retries": 0,
	}
	selectedSubGroup.EffectiveConfig = types.SystemSettings{EnableRequestBodyLogging: true}
	require.NoError(t, db.Save(selectedSubGroup).Error)

	otherSubGroup := createTestGroup(t, db, "agg-log-sim-other", "openai-response")
	otherSubGroup.Upstreams = []byte(`[{"url":"https://placeholder.example","weight":100}]`)
	otherSubGroup.Config = map[string]any{
		"max_retries":             0,
		"simulated_client":        "codex",
		"simulated_codex_version": "0.150.1",
	}
	otherSubGroup.EffectiveConfig = types.SystemSettings{EnableRequestBodyLogging: true}
	require.NoError(t, db.Save(otherSubGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-log-sim-parent",
		ChannelType: "openai-response",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"simulated_client": "claude_code",
			"max_retries":      0,
		},
		EffectiveConfig: types.SystemSettings{EnableRequestBodyLogging: true},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      selectedSubGroup.ID,
		SubGroupName:    selectedSubGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      otherSubGroup.ID,
		SubGroupName:    otherSubGroup.Name,
		SubGroupEnabled: true,
		Weight:          1,
	}).Error)

	createTestKey(t, db, selectedSubGroup.ID, "sk-agg-log-sim-selected", ps.encryptionSvc)
	createTestKey(t, db, otherSubGroup.ID, "sk-agg-log-sim-other", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, memStore.Delete(activeKeysListKeyForTest(uint64(otherSubGroup.ID))))
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", "client-before/1.0")

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, selectedSubGroup.ID, logEntry.GroupID)
	assert.Equal(t, selectedSubGroup.Name, logEntry.GroupName)
	assert.Equal(t, aggregateGroup.ID, logEntry.ParentGroupID)
	assert.Equal(t, aggregateGroup.Name, logEntry.ParentGroupName)
	assert.False(t, logEntry.SimulatedClientEnabled)
}

func TestExecuteRequestWithAggregateRetryUsesSelectedSubGroupMaxRetries(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "agg-sub-retry-selected", "openai")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	subGroup.Config = map[string]any{
		"max_retries":         2,
		"blacklist_threshold": 100,
	}
	subGroup.EffectiveConfig = systemSettingsWithRetryTimeout(2, 1)
	require.NoError(t, db.Save(subGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-sub-retry-parent",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"max_retries":     0,
			"sub_max_retries": 10,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      subGroup.ID,
		SubGroupName:    subGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-agg-sub-retry-a", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-agg-sub-retry-b", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-agg-sub-retry-c", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-test"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/chat/completions", bytes.NewReader(body))

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts))

	retryLogs := []models.RequestLog{
		popRecordedRequestLog(t, memStore),
		popRecordedRequestLog(t, memStore),
		popRecordedRequestLog(t, memStore),
	}
	require.Len(t, retryLogs, 3)
	requestTypes := map[string]int{}
	for _, logEntry := range retryLogs {
		requestTypes[logEntry.RequestType]++
	}
	assert.Equal(t, 2, requestTypes[models.RequestTypeRetry])
	assert.Equal(t, 1, requestTypes[models.RequestTypeFinal])
}

func TestExecuteRequestWithAggregateRetryPinsSubGroupDuringKeyRetries(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	var targetAttempts int32
	var backupAttempts int32
	var backupKey *models.APIKey
	targetUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&targetAttempts, 1) == 1 && backupKey != nil {
			_ = memStore.LPush(activeKeysListKeyForTest(uint64(backupKey.GroupID)), uint64(backupKey.ID))
		}
		if atomic.LoadInt32(&targetAttempts) <= 2 {
			http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(targetUpstream.Close)

	backupUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupAttempts, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"backup":true}`)
	}))
	t.Cleanup(backupUpstream.Close)

	targetGroup := createTestGroup(t, db, "agg-key-retry-target", "openai")
	targetGroup.Upstreams = []byte(`[{"url":"` + targetUpstream.URL + `","weight":100}]`)
	targetGroup.Config = map[string]any{
		"max_retries":         10,
		"blacklist_threshold": 100,
	}
	targetGroup.EffectiveConfig = systemSettingsWithRetryTimeout(10, 1)
	require.NoError(t, db.Save(targetGroup).Error)

	backupGroup := createTestGroup(t, db, "agg-key-retry-backup", "openai")
	backupGroup.Upstreams = []byte(`[{"url":"` + backupUpstream.URL + `","weight":100}]`)
	backupGroup.Config = map[string]any{
		"max_retries":         10,
		"blacklist_threshold": 100,
	}
	backupGroup.EffectiveConfig = systemSettingsWithRetryTimeout(10, 1)
	require.NoError(t, db.Save(backupGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-key-retry-parent",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{
			"max_retries":     8,
			"sub_max_retries": 10,
		},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      targetGroup.ID,
		SubGroupName:    targetGroup.Name,
		SubGroupEnabled: true,
		Weight:          1,
	}).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      backupGroup.ID,
		SubGroupName:    backupGroup.Name,
		SubGroupEnabled: true,
		Weight:          1_000_000,
	}).Error)

	createTestKey(t, db, targetGroup.ID, "sk-agg-key-retry-a", ps.encryptionSvc)
	createTestKey(t, db, targetGroup.ID, "sk-agg-key-retry-b", ps.encryptionSvc)
	createTestKey(t, db, targetGroup.ID, "sk-agg-key-retry-c", ps.encryptionSvc)
	backupKey = createTestKey(t, db, backupGroup.ID, "sk-agg-key-retry-backup", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, memStore.Delete(activeKeysListKeyForTest(uint64(backupGroup.ID))))
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() {
		ps.groupManager.Stop(context.Background())
	})

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-test"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/chat/completions", bytes.NewReader(body))

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int32(3), atomic.LoadInt32(&targetAttempts))
	assert.Equal(t, int32(0), atomic.LoadInt32(&backupAttempts))

	for i := 0; i < 3; i++ {
		logEntry := popRecordedRequestLog(t, memStore)
		assert.Equal(t, targetGroup.ID, logEntry.GroupID)
		assert.Equal(t, targetGroup.Name, logEntry.GroupName)
	}

	subGroupMetrics, err := dwm.GetSubGroupMetrics(aggregateGroup.ID, targetGroup.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), subGroupMetrics.Requests180d)
	assert.Equal(t, int64(1), subGroupMetrics.Successes180d)
	assert.Equal(t, int64(0), subGroupMetrics.ConsecutiveFailures)

	groupMetrics, err := dwm.GetGroupMetrics(targetGroup.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), groupMetrics.Requests180d)
	assert.Equal(t, int64(1), groupMetrics.Successes180d)
}

func TestMarkAggregateSubGroupFinalRestoresFalseValue(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	restore := markAggregateSubGroupFinal(c)
	require.True(t, isAggregateSubGroupFinal(c))

	restore()

	value, exists := c.Get(ctxKeyAggregateSubGroupFinal)
	require.True(t, exists)
	assert.Equal(t, false, value)
	assert.False(t, isAggregateSubGroupFinal(c))
}

func TestAggregateRetryAttemptsUpdateDynamicHealthAcrossChannels(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	aggregateGroup := &models.Group{
		ID:        9001,
		Name:      "agg-health",
		GroupType: "aggregate",
	}
	failedGroup := &models.Group{
		ID:          9002,
		Name:        "agg-health-openai",
		GroupType:   "standard",
		ChannelType: "openai",
	}
	successGroup := &models.Group{
		ID:          9003,
		Name:        "agg-health-gemini",
		GroupType:   "standard",
		ChannelType: "gemini",
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/agg-health/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-test"}`)))
	body := []byte(`{"model":"gpt-test"}`)

	clearAggregateSubGroupFinal := markAggregateSubGroupFinal(c)
	ps.logRequest(c, aggregateGroup, failedGroup, nil, time.Now().Add(-time.Millisecond), http.StatusBadGateway,
		errors.New("upstream failed"), false, "https://openai.example", nil, "", &testChannelProxy{}, body, models.RequestTypeRetry)
	clearAggregateSubGroupFinal()
	ps.logRequest(c, aggregateGroup, successGroup, nil, time.Now().Add(-time.Millisecond), http.StatusOK,
		nil, false, "https://gemini.example", nil, "", &testChannelProxy{}, body, models.RequestTypeFinal)

	failedMetrics, err := dwm.GetSubGroupMetrics(aggregateGroup.ID, failedGroup.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), failedMetrics.Requests180d)
	assert.Equal(t, int64(0), failedMetrics.Successes180d)
	assert.Equal(t, int64(1), failedMetrics.ConsecutiveFailures)

	successMetrics, err := dwm.GetSubGroupMetrics(aggregateGroup.ID, successGroup.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), successMetrics.Requests180d)
	assert.Equal(t, int64(1), successMetrics.Successes180d)
	assert.Equal(t, int64(0), successMetrics.ConsecutiveFailures)

	failedGroupMetrics, err := dwm.GetGroupMetrics(failedGroup.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), failedGroupMetrics.Requests180d)
	assert.Equal(t, int64(0), failedGroupMetrics.Successes180d)

	successGroupMetrics, err := dwm.GetGroupMetrics(successGroup.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), successGroupMetrics.Requests180d)
	assert.Equal(t, int64(1), successGroupMetrics.Successes180d)
}

func activeKeysListKeyForTest(groupID uint64) string {
	return "group:" + strconv.FormatUint(groupID, 10) + ":active_keys"
}

func compressBrotliForProxyTest(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := brotli.NewWriter(&buf)
	_, err := writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

func systemSettingsWithRetryTimeout(maxRetries, nonStreamTimeout int) types.SystemSettings {
	return types.SystemSettings{
		MaxRetries:                  maxRetries,
		RetryBackoffMaxPercent:      500,
		NonStreamRequestTimeout:     nonStreamTimeout,
		StreamRequestTimeout:        0,
		ConnectTimeout:              1,
		IdleConnTimeout:             30,
		MaxIdleConns:                10,
		MaxIdleConnsPerHost:         10,
		ResponseHeaderTimeout:       1,
		FailoverStatusCodes:         "400-403,405-999",
		BlacklistThreshold:          100,
		RequestLogRetentionDays:     7,
		KeyValidationConcurrency:    1,
		KeyValidationTimeoutSeconds: 1,
	}
}

func TestRetryAfterRateLimitPressureFromHeader(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 11, 12, 0, 0, 500*int(time.Millisecond), time.UTC)

	tests := []struct {
		name     string
		header   string
		expected int64
	}{
		{name: "empty", header: "", expected: 1},
		{name: "zero seconds", header: "0", expected: 1},
		{name: "short delta", header: "30", expected: 3},
		{name: "five minute delta", header: "300", expected: 4},
		{name: "one hour delta", header: "3600", expected: 5},
		{name: "future http date", header: now.Add(10 * time.Minute).Format(http.TimeFormat), expected: 4},
		// Retry-After dates are HTTP-date values, so the header intentionally uses
		// http.TimeFormat while now keeps subsecond precision to cover ceil boundaries.
		{name: "exact five minute http date with subsecond now", header: now.Add(5 * time.Minute).Format(http.TimeFormat), expected: 4},
		{name: "exact one hour http date with subsecond now", header: now.Add(time.Hour).Format(http.TimeFormat), expected: 5},
		{name: "past http date", header: now.Add(-time.Minute).Format(http.TimeFormat), expected: 1},
		{name: "invalid", header: "retry after 30 seconds", expected: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, retryAfterRateLimitPressureFromHeader(tt.header, now))
		})
	}
}

func TestSetRateLimitPressureContextForAttempt(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	now := time.Date(2026, 6, 11, 12, 0, 0, 500*int(time.Millisecond), time.UTC)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Retry-After": []string{now.Add(5 * time.Minute).Format(http.TimeFormat)},
		},
	}
	setRateLimitPressureContextForAttempt(ctx, resp, now)

	value, exists := ctx.Get(ctxKeyRateLimitPressure)
	require.True(t, exists)
	assert.Equal(t, int64(4), value)

	ctx.Set("response_body", `{"error":{"message":"api key quota exhausted"}}`)
	setRateLimitPressureContextForAttempt(ctx, resp, now)
	_, exists = ctx.Get("response_body")
	assert.False(t, exists)

	setRateLimitPressureContextForAttempt(ctx, &http.Response{StatusCode: http.StatusInternalServerError}, now)
	_, exists = ctx.Get(ctxKeyRateLimitPressure)
	assert.False(t, exists)

	ctx.Set(ctxKeyRateLimitPressure, int64(5))
	setRateLimitPressureContextForAttempt(ctx, nil, now)
	_, exists = ctx.Get(ctxKeyRateLimitPressure)
	assert.False(t, exists)
}

func TestRecordDynamicWeightMetricsUsesRetryAfterPressureAfterConsecutive429Threshold(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	group := &models.Group{ID: 79, GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set(ctxKeyRateLimitPressure, int64(3))

	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)

	metrics, err := dwm.GetGroupMetrics(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.ConsecutiveRateLimits)
	assert.InDelta(t, 1.0, dwm.CalculateHealthScore(metrics), 0.001)

	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)
	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)

	metrics, err = dwm.GetGroupMetrics(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(5), metrics.ConsecutiveRateLimits)
	assert.Less(t, dwm.CalculateHealthScore(metrics), 1.0)
}

func TestRecordDynamicWeightMetricsUsesQuotaExhaustedPressureFor429(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	group := &models.Group{ID: 81, GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("response_body", `{"error":{"message":"api key 5小时限额已用完","type":"rate_limit_exceeded"}}`)

	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)
	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)
	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)

	metrics, err := dwm.GetGroupMetrics(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(6), metrics.ConsecutiveRateLimits)
	assert.Less(t, dwm.CalculateHealthScore(metrics), 0.90)
	assert.Greater(t, dwm.CalculateHealthScore(metrics), 0.45)
}

func TestRecordDynamicWeightMetricsUsesQuotaExhaustedPressureFromCompressed429Body(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	rawBody := []byte(`{"error":{"message":"api key 日限额已用完","type":"rate_limit_exceeded"}}`)
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Encoding": []string{"br"},
		},
	}
	decodedBody := decompressUpstreamErrorBody(resp, compressBrotliForProxyTest(t, rawBody))
	require.Equal(t, rawBody, decodedBody)

	group := &models.Group{ID: 82, GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("response_body", utils.TruncateString(utils.SanitizeErrorBody(string(decodedBody)), maxResponseCaptureBytes))
	require.Equal(t, quotaExhaustedRatePressure, quotaExhaustedRateLimitPressureFromContext(ctx))

	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)
	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)
	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests, models.RequestTypeFinal)

	metrics, err := dwm.GetGroupMetrics(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(6), metrics.ConsecutiveRateLimits)
	assert.Less(t, dwm.CalculateHealthScore(metrics), 0.90)
}

func TestQuotaExhaustedRateLimitPressureMarkers(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		body     string
		expected int64
	}{
		{
			name:     "structured rate limit exceeded with json spacing",
			body:     `{"error":{"message":"api key quota exhausted","type": "rate_limit_exceeded"}}`,
			expected: quotaExhaustedRatePressure,
		},
		{
			name:     "structured api key quota exhausted code",
			body:     `{"code":"API_KEY_QUOTA_EXHAUSTED","message":"temporary quota block"}`,
			expected: quotaExhaustedRatePressure,
		},
		{
			name:     "chinese quota exhausted message",
			body:     `{"error":{"message":"api key 5小时限额已用完"}}`,
			expected: quotaExhaustedRatePressure,
		},
		{
			name:     "chinese daily quota exhausted message",
			body:     `{"error":{"message":"api key 日限额已用完","type":"rate_limit_exceeded"}}`,
			expected: quotaExhaustedRatePressure,
		},
		{
			name:     "plain too many requests remains light",
			body:     `{"error":{"message":"Too many requests"}}`,
			expected: 0,
		},
		{
			name:     "generic limit exceeded remains light",
			body:     `{"error":{"message":"request limit exceeded"}}`,
			expected: 0,
		},
		{
			name:     "generic rate limit type remains light",
			body:     `{"error":{"message":"Too many requests","type":"rate_limit_exceeded"}}`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("response_body", tt.body)
			assert.Equal(t, tt.expected, quotaExhaustedRateLimitPressureFromContext(ctx))
		})
	}
}

func TestRecordDynamicWeightMetricsForV2ModelRedirect(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	group := &models.Group{
		ID:        77,
		GroupType: "standard",
		ModelRedirectMapV2: map[string]*models.ModelRedirectRuleV2{
			"virtual-model": {
				Targets: []models.ModelRedirectTarget{
					{Model: "target-a", Weight: 100},
					{Model: "target-b", Weight: 100},
				},
			},
		},
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	setModelRedirectContext(ctx, "virtual-model", 1, false)

	ps.recordDynamicWeightMetrics(ctx, group, group, true, http.StatusOK, models.RequestTypeFinal)

	targetA, err := dwm.GetModelRedirectMetrics(group.ID, "virtual-model", "target-a")
	require.NoError(t, err)
	assert.Equal(t, int64(0), targetA.Requests180d)

	targetB, err := dwm.GetModelRedirectMetrics(group.ID, "virtual-model", "target-b")
	require.NoError(t, err)
	assert.Equal(t, int64(1), targetB.Requests180d)
	assert.Equal(t, int64(1), targetB.Successes180d)
}

func TestRecordDynamicWeightMetricsUsesRedirectSourceWhenModelMappingExists(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)
	ps.SetDynamicWeightManager(dwm)

	group := &models.Group{
		ID:        78,
		GroupType: "standard",
		ModelRedirectMapV2: map[string]*models.ModelRedirectRuleV2{
			"mapped-model": {
				Targets: []models.ModelRedirectTarget{
					{Model: "target-a", Weight: 100},
					{Model: "target-b", Weight: 100},
				},
			},
		},
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("original_model", "user-facing-alias")
	setModelRedirectContext(ctx, "mapped-model", 1, true)

	ps.recordDynamicWeightMetrics(ctx, group, group, true, http.StatusOK, models.RequestTypeFinal)

	mappedTarget, err := dwm.GetModelRedirectMetrics(group.ID, "mapped-model", "target-b")
	require.NoError(t, err)
	assert.Equal(t, int64(1), mappedTarget.Requests180d)

	aliasTarget, err := dwm.GetModelRedirectMetrics(group.ID, "user-facing-alias", "target-b")
	require.NoError(t, err)
	assert.Equal(t, int64(0), aliasTarget.Requests180d)

	originalModel, exists := ctx.Get("original_model")
	require.True(t, exists)
	assert.Equal(t, "user-facing-alias", originalModel)
}

func TestClearModelRedirectContextRemovesRetryAttemptState(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("original_model", "previous-alias")
	setModelRedirectContext(ctx, "previous-model", 1, true)

	clearModelRedirectContext(ctx)

	_, exists := ctx.Get("original_model")
	require.False(t, exists)
	_, exists = ctx.Get(ctxKeyModelRedirectSourceModel)
	require.False(t, exists)
	_, exists = ctx.Get(ctxKeyModelRedirectTargetIndex)
	require.False(t, exists)
}

func TestSanitizeInternalErrorMessageRedactsURLCredentials(t *testing.T) {
	t.Parallel()

	raw := "Post \"https://generativelanguage.googleapis.com/v1beta/models/gemini:generateContent?key=plain-secret&x-goog-api-key=goog-secret\": dial tcp failed"
	got := sanitizeInternalErrorMessage(raw)

	assert.Contains(t, got, "key=[REDACTED]")
	assert.Contains(t, got, "x-goog-api-key=[REDACTED]")
	assert.NotContains(t, got, "plain-secret")
	assert.NotContains(t, got, "goog-secret")
}

func TestSanitizeInternalErrorRedactsURLCredentials(t *testing.T) {
	t.Parallel()

	raw := errors.New("Post \"https://upstream.example/v1/messages?key=plain-secret&x-goog-api-key=goog-secret\": request canceled")
	got := sanitizeInternalError(raw)

	require.Error(t, got)
	assert.Contains(t, got.Error(), "key=[REDACTED]")
	assert.Contains(t, got.Error(), "x-goog-api-key=[REDACTED]")
	assert.NotContains(t, got.Error(), "plain-secret")
	assert.NotContains(t, got.Error(), "goog-secret")
	assert.Nil(t, sanitizeInternalError(nil))
}

func TestLogRequestUsesEstimatedTokenFallbackWhenUsageMissing(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{ID: 1, Name: "test-group", GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	setEstimatedOutputTokens(ctx, 3)

	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "", nil, "", nil, body, models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, models.TokenUsageSourceEstimated, logEntry.TokenUsageSource)
	assert.Greater(t, logEntry.InputTokens, int64(0))
	assert.Equal(t, int64(3), logEntry.OutputTokens)
	assert.Equal(t, logEntry.InputTokens+logEntry.OutputTokens, logEntry.TotalTokens)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(ctx))
}

func TestLogRequestKeepsEstimatedOutputTokensForLargeBody(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{ID: 1, Name: "test-group", GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	setEstimatedOutputTokens(ctx, 3)

	body := bytes.Repeat([]byte("x"), maxEstimatedTokenBodyBytes+1)
	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "", nil, "", nil, body, models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, models.TokenUsageSourceEstimated, logEntry.TokenUsageSource)
	assert.Equal(t, int64(0), logEntry.InputTokens)
	assert.Equal(t, int64(3), logEntry.OutputTokens)
	assert.Equal(t, int64(3), logEntry.TotalTokens)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(ctx))
}

func TestLogRequestPrefersUpstreamTokenUsageOverEstimate(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{ID: 1, Name: "test-group", GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	setEstimatedOutputTokens(ctx, 100)
	setTokenUsage(ctx, tokenusage.Usage{InputTokens: 2, OutputTokens: 4})

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "", nil, "", nil, []byte(`{"model":"gpt-4o"}`), models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, models.TokenUsageSourceUpstream, logEntry.TokenUsageSource)
	assert.Equal(t, int64(2), logEntry.InputTokens)
	assert.Equal(t, int64(4), logEntry.OutputTokens)
	assert.Equal(t, int64(6), logEntry.TotalTokens)
}

func TestLogRequestSkipsTokenUsageForFailedRequest(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{ID: 1, Name: "test-group", GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	setEstimatedOutputTokens(ctx, 100)
	setTokenUsage(ctx, tokenusage.Usage{InputTokens: 2, OutputTokens: 4})

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusTooManyRequests, errors.New("upstream rate limited"), false, "", nil, "", nil, []byte(`{"model":"gpt-4o"}`), models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Empty(t, logEntry.TokenUsageSource)
	assert.Equal(t, int64(0), logEntry.InputTokens)
	assert.Equal(t, int64(0), logEntry.OutputTokens)
	assert.Equal(t, int64(0), logEntry.TotalTokens)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(ctx))
}

func TestLogRequestSanitizesRequestBodyBeforePersisting(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{
		ID:        1,
		Name:      "request-body-log-group",
		GroupType: "standard",
		EffectiveConfig: types.SystemSettings{
			EnableRequestBodyLogging: true,
		},
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions?key=client-query-key", nil)

	body := []byte(`{"api_key":"sk-body-secret","authorization":"Bearer body-secret","x-goog-api-key":"goog-body-secret","encrypted_content":"gAAAA-request-reasoning","messages":[{"role":"user","content":"hello"}]}`)
	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "https://upstream.example", nil, "", nil, body, models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.NotContains(t, logEntry.RequestBody, "sk-body-secret")
	assert.NotContains(t, logEntry.RequestBody, "body-secret")
	assert.NotContains(t, logEntry.RequestBody, "goog-body-secret")
	assert.NotContains(t, logEntry.RequestBody, "gAAAA-request-reasoning")
	assert.Contains(t, logEntry.RequestBody, "[REDACTED]")
	assert.NotContains(t, logEntry.RequestPath, "client-query-key")
}

func TestLogRequestUsesLogicalStreamingFailureForHealthMetrics(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	dwm := services.NewDynamicWeightManager(memStore)
	ps := &ProxyServer{
		requestLogService:    services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
		dynamicWeightManager: dwm,
	}
	group := &models.Group{ID: 91, Name: "logical-failure-group", GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Set(ctxKeyUpstreamLogicalStatusCode, http.StatusTooManyRequests)
	ctx.Set(ctxKeyUpstreamLogicalErrorMessage, "Concurrency limit exceeded for user, please retry later")
	ctx.Set("response_body", `{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"Concurrency limit exceeded for user, please retry later"}}}`)

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, true, "", nil, "", nil, []byte(`{"model":"gpt-5.4"}`), models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.False(t, logEntry.IsSuccess)
	assert.Equal(t, http.StatusTooManyRequests, logEntry.StatusCode)
	assert.Contains(t, logEntry.ErrorMessage, "Concurrency limit exceeded")

	metrics, err := dwm.GetGroupMetrics(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.ConsecutiveRateLimits)
}

func TestLogRequestPreservesLogicalErrorMessageWhenFinalErrorExists(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	memStore := store.NewMemoryStore()
	ps := &ProxyServer{
		requestLogService: services.NewRequestLogService(nil, memStore, config.NewSystemSettingsManager()),
	}
	group := &models.Group{ID: 92, Name: "logical-error-message-group", GroupType: "standard"}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Set(ctxKeyUpstreamLogicalStatusCode, http.StatusTooManyRequests)
	ctx.Set(ctxKeyUpstreamLogicalErrorMessage, "Concurrency limit exceeded for user, please retry later")

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, errors.New("forced stream ended with logical failure"), true, "", nil, "", nil, []byte(`{"model":"gpt-5.4"}`), models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.False(t, logEntry.IsSuccess)
	assert.Equal(t, http.StatusTooManyRequests, logEntry.StatusCode)
	assert.Equal(t, "Concurrency limit exceeded for user, please retry later", logEntry.ErrorMessage)
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

func popRecordedRequestLog(t *testing.T, memStore store.Store) models.RequestLog {
	t.Helper()

	keys, err := memStore.SPopN(services.PendingLogKeysSet, 1)
	require.NoError(t, err)
	require.Len(t, keys, 1)

	logBytes, err := memStore.Get(keys[0])
	require.NoError(t, err)

	var logEntry models.RequestLog
	require.NoError(t, json.Unmarshal(logBytes, &logEntry))
	return logEntry
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
			name:          "json.Number value",
			preconditions: map[string]any{"max_request_size_kb": json.Number("1024")},
			expected:      1024,
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
