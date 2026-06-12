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
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestExecuteRequestWithRetryRetriesAfterNonStreamTimeout(t *testing.T) {
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

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int32(2), atomic.LoadInt32(&attempts))
}

func TestExecuteRequestWithAggregateRetryRetriesAfterNonStreamTimeout(t *testing.T) {
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

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int32(1), atomic.LoadInt32(&slowAttempts))
	require.Equal(t, int32(1), atomic.LoadInt32(&fastAttempts))
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

	ps.logRequest(c, aggregateGroup, failedGroup, nil, time.Now().Add(-time.Millisecond), http.StatusBadGateway,
		errors.New("upstream failed"), false, "https://openai.example", nil, &testChannelProxy{}, body, models.RequestTypeRetry)
	ps.logRequest(c, aggregateGroup, successGroup, nil, time.Now().Add(-time.Millisecond), http.StatusOK,
		nil, false, "https://gemini.example", nil, &testChannelProxy{}, body, models.RequestTypeFinal)

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

func systemSettingsWithRetryTimeout(maxRetries, nonStreamTimeout int) types.SystemSettings {
	return types.SystemSettings{
		MaxRetries:                  maxRetries,
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

	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests)

	metrics, err := dwm.GetGroupMetrics(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.ConsecutiveRateLimits)
	assert.InDelta(t, 1.0, dwm.CalculateHealthScore(metrics), 0.001)

	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests)
	ps.recordDynamicWeightMetrics(ctx, group, group, false, http.StatusTooManyRequests)

	metrics, err = dwm.GetGroupMetrics(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(5), metrics.ConsecutiveRateLimits)
	assert.Less(t, dwm.CalculateHealthScore(metrics), 1.0)
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

	ps.recordDynamicWeightMetrics(ctx, group, group, true, http.StatusOK)

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

	ps.recordDynamicWeightMetrics(ctx, group, group, true, http.StatusOK)

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
	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "", nil, nil, body, models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Equal(t, models.TokenUsageSourceEstimated, logEntry.TokenUsageSource)
	assert.Greater(t, logEntry.InputTokens, int64(0))
	assert.Equal(t, int64(3), logEntry.OutputTokens)
	assert.Equal(t, logEntry.InputTokens+logEntry.OutputTokens, logEntry.TotalTokens)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(ctx))
}

func TestLogRequestSkipsEstimatedTokenFallbackForLargeBody(t *testing.T) {
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
	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "", nil, nil, body, models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Empty(t, logEntry.TokenUsageSource)
	assert.Equal(t, int64(0), logEntry.InputTokens)
	assert.Equal(t, int64(0), logEntry.OutputTokens)
	assert.Equal(t, int64(0), logEntry.TotalTokens)
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

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusOK, nil, false, "", nil, nil, []byte(`{"model":"gpt-4o"}`), models.RequestTypeFinal)

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

	ps.logRequest(ctx, nil, group, nil, time.Now().Add(-time.Millisecond), http.StatusTooManyRequests, errors.New("upstream rate limited"), false, "", nil, nil, []byte(`{"model":"gpt-4o"}`), models.RequestTypeFinal)

	logEntry := popRecordedRequestLog(t, memStore)
	assert.Empty(t, logEntry.TokenUsageSource)
	assert.Equal(t, int64(0), logEntry.InputTokens)
	assert.Equal(t, int64(0), logEntry.OutputTokens)
	assert.Equal(t, int64(0), logEntry.TotalTokens)
	assert.Equal(t, int64(0), getEstimatedOutputTokens(ctx))
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
