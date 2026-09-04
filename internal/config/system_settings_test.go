package config

import (
	"context"
	"errors"
	"testing"

	appdb "gpt-load/internal/db"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/syncer"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type staticProxyURLResolver struct {
	resolved string
	err      error
}

type sequenceProxyURLResolver struct {
	values    map[string]string
	errors    map[string]error
	calls     []string
	afterCall func(string)
}

func (r *sequenceProxyURLResolver) ResolveProxyURL(_ context.Context, ref string) (string, error) {
	r.calls = append(r.calls, ref)
	if r.afterCall != nil {
		r.afterCall(ref)
	}
	return r.values[ref], r.errors[ref]
}

type batchProxyURLResolver struct {
	values map[string]string
	err    error
	calls  [][]string
}

func (r *batchProxyURLResolver) ResolveProxyURL(_ context.Context, _ string) (string, error) {
	return "", errors.New("unexpected single resolution")
}

func (r *batchProxyURLResolver) ResolveProxyURLs(_ context.Context, refs []string) (map[string]string, error) {
	r.calls = append(r.calls, append([]string(nil), refs...))
	return r.values, r.err
}

type noopSystemSettingsGroupManager struct{}

func (noopSystemSettingsGroupManager) Invalidate() error {
	return nil
}

func (r staticProxyURLResolver) ResolveProxyURL(_ context.Context, _ string) (string, error) {
	return r.resolved, r.err
}

func TestResolveRuntimeProxyURLsDeduplicatesAndPreservesPlainURL(t *testing.T) {
	t.Parallel()

	resolver := &batchProxyURLResolver{values: map[string]string{"proxy-pool:1": "http://proxy.example.com:8080"}}
	manager := NewSystemSettingsManager()
	manager.SetProxyURLResolver(resolver)

	resolved := manager.ResolveRuntimeProxyURLs(context.Background(), []string{" proxy-pool:1 ", "proxy-pool:1", "http://direct.example.com"})

	require.Len(t, resolver.calls, 1)
	assert.Equal(t, []string{"proxy-pool:1"}, resolver.calls[0])
	assert.Equal(t, "http://proxy.example.com:8080", resolved["proxy-pool:1"])
	assert.Equal(t, "http://direct.example.com", resolved["http://direct.example.com"])
}

func TestResolveRuntimeProxyURLsBatchFailureKeepsReferences(t *testing.T) {
	t.Parallel()

	manager := NewSystemSettingsManager()
	manager.SetProxyURLResolver(&batchProxyURLResolver{
		values: map[string]string{"proxy-pool:1": "http://partial.example.com:8080"},
		err:    errors.New("temporary database failure"),
	})

	resolved := manager.ResolveRuntimeProxyURLs(context.Background(), []string{"proxy-pool:1"})

	assert.Equal(t, "proxy-pool:1", resolved["proxy-pool:1"])
}

func TestResolveRuntimeProxyURLsNonBatchFailureKeepsPartialSuccess(t *testing.T) {
	t.Parallel()

	resolver := &sequenceProxyURLResolver{
		values: map[string]string{"proxy-pool:1": "http://proxy.example.com:8080"},
		errors: map[string]error{"proxy-pool:2": errors.New("missing proxy")},
	}
	manager := NewSystemSettingsManager()
	manager.SetProxyURLResolver(resolver)

	resolved := manager.ResolveRuntimeProxyURLs(context.Background(), []string{"proxy-pool:1", "proxy-pool:2"})

	assert.Equal(t, []string{"proxy-pool:1", "proxy-pool:2"}, resolver.calls)
	assert.Equal(t, "http://proxy.example.com:8080", resolved["proxy-pool:1"])
	assert.Equal(t, "proxy-pool:2", resolved["proxy-pool:2"])
}

func TestResolveRuntimeProxyURLsContinuesAfterFailure(t *testing.T) {
	t.Parallel()

	resolver := &sequenceProxyURLResolver{
		values: map[string]string{"proxy-pool:2": "http://proxy.example.com:8080"},
		errors: map[string]error{"proxy-pool:1": errors.New("missing proxy")},
	}
	manager := NewSystemSettingsManager()
	manager.SetProxyURLResolver(resolver)

	resolved := manager.ResolveRuntimeProxyURLs(context.Background(), []string{"proxy-pool:1", "proxy-pool:2"})

	assert.Equal(t, []string{"proxy-pool:1", "proxy-pool:2"}, resolver.calls)
	assert.Equal(t, "proxy-pool:1", resolved["proxy-pool:1"])
	assert.Equal(t, "http://proxy.example.com:8080", resolved["proxy-pool:2"])
}

func TestResolveRuntimeProxyURLsStopsAfterContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	resolver := &sequenceProxyURLResolver{
		values: map[string]string{
			"proxy-pool:1": "http://first.example.com:8080",
			"proxy-pool:2": "http://second.example.com:8080",
		},
		afterCall: func(ref string) {
			if ref == "proxy-pool:1" {
				cancel()
			}
		},
	}
	manager := NewSystemSettingsManager()
	manager.SetProxyURLResolver(resolver)

	resolved := manager.ResolveRuntimeProxyURLs(ctx, []string{"proxy-pool:1", "proxy-pool:2"})

	assert.Equal(t, []string{"proxy-pool:1"}, resolver.calls)
	assert.Equal(t, "http://first.example.com:8080", resolved["proxy-pool:1"])
	assert.Equal(t, "proxy-pool:2", resolved["proxy-pool:2"])
}

func setupSystemSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := appdb.DB
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	require.NoError(t, testDB.AutoMigrate(&models.SystemSetting{}))
	appdb.DB = testDB
	t.Cleanup(func() {
		appdb.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})

	return testDB
}

func assertSystemSettingValue(t *testing.T, db *gorm.DB, key, want string) {
	t.Helper()

	var setting models.SystemSetting
	require.NoError(t, db.Where("setting_key = ?", key).First(&setting).Error)
	assert.Equal(t, want, setting.SettingValue)
}

func setupSystemSettingsManagerWithSettings(t *testing.T, settings types.SystemSettings) *SystemSettingsManager {
	t.Helper()

	memStore := store.NewMemoryStore()
	t.Cleanup(func() {
		require.NoError(t, memStore.Close())
	})

	cache, err := syncer.NewCacheSyncer(
		func() (types.SystemSettings, error) {
			return settings, nil
		},
		memStore,
		"system-settings-test",
		logrus.WithField("test", t.Name()),
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(cache.Stop)

	manager := NewSystemSettingsManager()
	manager.syncer = cache
	return manager
}

// TestSystemSettingsManager tests the system settings manager
func TestSystemSettingsManager(t *testing.T) {
	manager := NewSystemSettingsManager()
	assert.NotNil(t, manager)
}

// TestDefaultConstants tests default configuration constants
func TestDefaultConstants(t *testing.T) {
	assert.Equal(t, 1, DefaultConstants.MinPort)
	assert.Equal(t, 65535, DefaultConstants.MaxPort)
	assert.Equal(t, 1, DefaultConstants.MinTimeout)
	assert.Equal(t, 30, DefaultConstants.DefaultTimeout)
	assert.Equal(t, 50, DefaultConstants.DefaultMaxSockets)
	assert.Equal(t, 10, DefaultConstants.DefaultMaxFreeSockets)
}

func TestStreamFirstByteTimeoutDefaultsToDisabled(t *testing.T) {
	settings := utils.DefaultSystemSettings()

	assert.Equal(t, 0, settings.StreamFirstByteTimeout)
}

// TestGetSettings tests getting system settings without initialization
func TestGetSettings(t *testing.T) {
	manager := NewSystemSettingsManager()

	// Should return default settings when not initialized
	settings := manager.GetSettings()
	assert.NotNil(t, settings)
	assert.Equal(t, 0, settings.RequestTimeout)
	assert.Equal(t, 0, settings.StreamFirstByteTimeout)
	assert.Equal(t, 30, settings.ConnectTimeout)
}

// TestGetAppUrl tests getting app URL
func TestGetAppUrl(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		port     string
		expected string
	}{
		{
			name:     "default values",
			host:     "",
			port:     "",
			expected: "http://localhost:3001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables for this test
			if tt.host != "" {
				t.Setenv("HOST", tt.host)
			} else {
				// Ensure HOST is not set or set to empty
				t.Setenv("HOST", "")
			}
			if tt.port != "" {
				t.Setenv("PORT", tt.port)
			} else {
				// Ensure PORT is not set or set to empty
				t.Setenv("PORT", "")
			}

			manager := NewSystemSettingsManager()
			appUrl := manager.GetAppUrl()
			assert.Equal(t, tt.expected, appUrl)
		})
	}
}

// TestValidateSettings tests settings validation
func TestValidateSettings(t *testing.T) {
	manager := NewSystemSettingsManager()

	tests := []struct {
		name        string
		settings    map[string]any
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid integer setting",
			settings: map[string]any{
				"request_timeout": float64(60),
			},
			expectError: false,
		},
		{
			name: "valid retry delay disabled",
			settings: map[string]any{
				"retry_delay_ms": float64(0),
			},
			expectError: false,
		},
		{
			name: "valid retry delay",
			settings: map[string]any{
				"retry_delay_ms": float64(1000),
			},
			expectError: false,
		},
		{
			name: "valid retry backoff enabled",
			settings: map[string]any{
				"retry_backoff_enabled": true,
			},
			expectError: false,
		},
		{
			name: "valid retry backoff max percent",
			settings: map[string]any{
				"retry_backoff_max_percent": float64(500),
			},
			expectError: false,
		},
		{
			name: "valid request timeout disabled",
			settings: map[string]any{
				"request_timeout": float64(0),
			},
			expectError: false,
		},
		{
			name: "valid stream timeout disabled",
			settings: map[string]any{
				"stream_first_byte_timeout": float64(0),
			},
			expectError: false,
		},
		{
			name: "valid request timeout",
			settings: map[string]any{
				"request_timeout": float64(60),
			},
			expectError: false,
		},
		{
			name: "valid string setting",
			settings: map[string]any{
				"app_url": "http://localhost:3001",
			},
			expectError: false,
		},
		{
			name: "valid proxy pool selected setting",
			settings: map[string]any{
				"proxy_url": "socks5://127.0.0.1:1080",
			},
			expectError: false,
		},
		{
			name: "valid empty proxy pool setting",
			settings: map[string]any{
				"proxy_url": "",
			},
			expectError: false,
		},
		{
			name: "invalid proxy_url unsupported scheme",
			settings: map[string]any{
				"proxy_url": "ftp://proxy.example.com",
			},
			expectError: true,
			errorMsg:    "invalid value for proxy_url",
		},
		{
			name: "invalid proxy_url missing scheme",
			settings: map[string]any{
				"proxy_url": "proxy.example.com:8080",
			},
			expectError: true,
			errorMsg:    "invalid value for proxy_url",
		},
		{
			name: "invalid proxy_url missing host",
			settings: map[string]any{
				"proxy_url": "http://",
			},
			expectError: true,
			errorMsg:    "invalid value for proxy_url",
		},
		{
			name: "invalid proxy_url malformed URL",
			settings: map[string]any{
				"proxy_url": "http://[invalid",
			},
			expectError: true,
			errorMsg:    "invalid value for proxy_url",
		},
		{
			name: "invalid setting key",
			settings: map[string]any{
				"invalid_key": "value",
			},
			expectError: true,
			errorMsg:    "invalid setting key",
		},
		{
			name: "invalid type for integer",
			settings: map[string]any{
				"request_timeout": "not_a_number",
			},
			expectError: true,
			errorMsg:    "expected a number",
		},
		{
			name: "request timeout below minimum",
			settings: map[string]any{
				"request_timeout": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "retry delay below minimum",
			settings: map[string]any{
				"retry_delay_ms": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "stream timeout below minimum",
			settings: map[string]any{
				"stream_first_byte_timeout": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "non-integer float value",
			settings: map[string]any{
				"request_timeout": float64(30.5),
			},
			expectError: true,
			errorMsg:    "must be an integer",
		},
		{
			name: "valid proxy pool test target URL",
			settings: map[string]any{
				"proxy_pool_test_target_url": "https://www.gstatic.com/generate_204",
			},
			expectError: false,
		},
		{
			name: "invalid proxy pool test target URL scheme",
			settings: map[string]any{
				"proxy_pool_test_target_url": "ftp://example.com/health",
			},
			expectError: true,
			errorMsg:    "invalid value for proxy_pool_test_target_url",
		},
		{
			name: "invalid proxy pool test target URL host",
			settings: map[string]any{
				"proxy_pool_test_target_url": "https:///health",
			},
			expectError: true,
			errorMsg:    "invalid value for proxy_pool_test_target_url",
		},
		{
			name: "invalid proxy pool test timeout below minimum",
			settings: map[string]any{
				"proxy_pool_test_timeout_seconds": float64(0),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "valid proxy pool auto test interval",
			settings: map[string]any{
				"proxy_pool_auto_test_interval_minutes": float64(60),
			},
			expectError: false,
		},
		{
			name: "invalid gateway proxy test timeout below minimum",
			settings: map[string]any{
				"gateway_proxy_test_timeout_seconds": float64(0),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "valid gateway proxy auto test interval",
			settings: map[string]any{
				"gateway_proxy_auto_test_interval_minutes": float64(60),
			},
			expectError: false,
		},
		{
			name: "required string empty",
			settings: map[string]any{
				"app_url": "",
			},
			expectError: true,
			errorMsg:    "is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.ValidateSettings(tt.settings)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateGroupConfigOverrides tests group config override validation
func TestValidateGroupConfigOverrides(t *testing.T) {
	manager := NewSystemSettingsManager()

	tests := []struct {
		name         string
		config       map[string]any
		expectError  bool
		errorMsg     string
		assertConfig func(t *testing.T, config map[string]any)
	}{
		{
			name: "valid sub_max_retries",
			config: map[string]any{
				"sub_max_retries": float64(3),
			},
			expectError: false,
		},
		{
			name: "valid high sub_max_retries",
			config: map[string]any{
				"sub_max_retries": float64(500),
			},
			expectError: false,
		},
		{
			name: "valid minimum codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": float64(1),
			},
			expectError: false,
		},
		{
			name: "valid default codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": float64(5),
			},
			expectError: false,
		},
		{
			name: "valid maximum codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": float64(MaxCodexAffinityAttempts),
			},
			expectError: false,
		},
		{
			name: "zero codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": float64(0),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "negative codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "codex_affinity_max_retries above maximum",
			config: map[string]any{
				"codex_affinity_max_retries": float64(MaxCodexAffinityAttempts + 1),
			},
			expectError: true,
			errorMsg:    "exceeds maximum value",
		},
		{
			name: "fractional codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": float64(1.5),
			},
			expectError: true,
			errorMsg:    "must be an integer",
		},
		{
			name: "string codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": "5",
			},
			expectError: true,
			errorMsg:    "expected a number",
		},
		{
			name: "boolean codex_affinity_max_retries",
			config: map[string]any{
				"codex_affinity_max_retries": true,
			},
			expectError: true,
			errorMsg:    "expected a number",
		},
		{
			name: "valid retry_delay_ms",
			config: map[string]any{
				"retry_delay_ms": float64(1000),
			},
			expectError: false,
		},
		{
			name: "valid retry_backoff_enabled",
			config: map[string]any{
				"retry_backoff_enabled": true,
			},
			expectError: false,
		},
		{
			name: "valid retry_backoff_max_percent",
			config: map[string]any{
				"retry_backoff_max_percent": float64(500),
			},
			expectError: false,
		},
		{
			name: "negative retry_delay_ms",
			config: map[string]any{
				"retry_delay_ms": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "invalid sub_max_retries type",
			config: map[string]any{
				"sub_max_retries": "not_a_number",
			},
			expectError: true,
			errorMsg:    "expected a number",
		},
		{
			name: "negative sub_max_retries",
			config: map[string]any{
				"sub_max_retries": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "valid force_function_call",
			config: map[string]any{
				"force_function_call": true,
			},
			expectError: false,
		},
		{
			name: "invalid force_function_call type",
			config: map[string]any{
				"force_function_call": "not_a_bool",
			},
			expectError: true,
			errorMsg:    "expected a boolean",
		},
		{
			name: "valid cc_support",
			config: map[string]any{
				"cc_support": true,
			},
			expectError: false,
		},
		{
			name: "valid codex_support",
			config: map[string]any{
				"codex_support": true,
			},
			expectError: false,
		},
		{
			name: "valid codex_affinity_enabled",
			config: map[string]any{
				"codex_affinity_enabled": true,
			},
			expectError: false,
		},
		{
			name: "valid codex_degradation_mitigation_enabled",
			config: map[string]any{
				"codex_degradation_mitigation_enabled": true,
			},
			expectError: false,
		},
		{
			name: "invalid codex_affinity_enabled type",
			config: map[string]any{
				"codex_affinity_enabled": "true",
			},
			expectError: true,
			errorMsg:    "expected a boolean",
		},
		{
			name: "invalid codex_degradation_mitigation_enabled type",
			config: map[string]any{
				"codex_degradation_mitigation_enabled": "true",
			},
			expectError: true,
			errorMsg:    "expected a boolean",
		},
		{
			name: "invalid codex_support type",
			config: map[string]any{
				"codex_support": "true",
			},
			expectError: true,
			errorMsg:    "expected a boolean",
		},
		{
			name: "valid thinking_model with cc_support",
			config: map[string]any{
				"cc_support":     true,
				"thinking_model": "claude-3-opus",
			},
			expectError: false,
		},
		{
			name: "thinking_model without cc_support",
			config: map[string]any{
				"thinking_model": "claude-3-opus",
			},
			expectError: true,
			errorMsg:    "can only be set when cc_support is enabled",
		},
		{
			name: "valid codex_instructions",
			config: map[string]any{
				"codex_instructions": "custom instructions",
			},
			expectError: false,
		},
		{
			name: "valid codex_instructions_mode auto",
			config: map[string]any{
				"codex_instructions_mode": "auto",
			},
			expectError: false,
		},
		{
			name: "valid codex_instructions_mode official",
			config: map[string]any{
				"codex_instructions_mode": "official",
			},
			expectError: false,
		},
		{
			name: "valid codex_instructions_mode custom",
			config: map[string]any{
				"codex_instructions_mode": "custom",
			},
			expectError: false,
		},
		{
			name: "invalid codex_instructions_mode",
			config: map[string]any{
				"codex_instructions_mode": "invalid",
			},
			expectError: true,
			errorMsg:    "must be 'auto', 'official', or 'custom'",
		},
		{
			name: "codex_instructions_mode case insensitive",
			config: map[string]any{
				"codex_instructions_mode": "AUTO",
			},
			expectError: false,
		},
		{
			name: "nil value skipped",
			config: map[string]any{
				"force_function_call": nil,
			},
			expectError: false,
		},
		{
			name: "valid intercept_event_log",
			config: map[string]any{
				"intercept_event_log": true,
			},
			expectError: false,
		},
		{
			name: "valid validation stream",
			config: map[string]any{
				"validation_stream": true,
			},
			expectError: false,
		},
		{
			name: "valid validation prompt mode",
			config: map[string]any{
				"validation_prompt_mode": "random_queue",
			},
			expectError: false,
		},
		{
			name: "invalid validation prompt mode",
			config: map[string]any{
				"validation_prompt_mode": "random",
			},
			expectError: true,
			errorMsg:    "must be 'default' or 'random_queue'",
		},
		{
			name: "valid stream override",
			config: map[string]any{
				"force_stream": true,
			},
			expectError: false,
		},
		{
			name: "conflicting stream override",
			config: map[string]any{
				"force_stream":     true,
				"force_non_stream": true,
			},
			expectError: true,
			errorMsg:    "cannot both be enabled",
		},
		{
			name: "valid Responses compatibility flags",
			config: map[string]any{
				"responses_include_encrypted_reasoning": true,
				"responses_legacy_user_role":            true,
			},
			expectError: false,
		},
		{
			name: "invalid Responses legacy user role type",
			config: map[string]any{
				"responses_legacy_user_role": "true",
			},
			expectError: true,
			errorMsg:    "expected a boolean",
		},
		{
			name: "valid simulated codex client",
			config: map[string]any{
				"simulated_client": "codex",
			},
			expectError: false,
		},
		{
			name: "valid simulated claude code client",
			config: map[string]any{
				"simulated_client": "claude_code",
			},
			expectError: false,
		},
		{
			name: "simulated client trims and normalizes case",
			config: map[string]any{
				"simulated_client": "  CODEX  ",
			},
			expectError: false,
			assertConfig: func(t *testing.T, config map[string]any) {
				assert.Equal(t, "codex", config["simulated_client"])
			},
		},
		{
			name: "invalid simulated client type",
			config: map[string]any{
				"simulated_client": true,
			},
			expectError: true,
			errorMsg:    "expected a string",
		},
		{
			name: "invalid simulated client value",
			config: map[string]any{
				"simulated_client": "browser",
			},
			expectError: true,
			errorMsg:    "must be 'off', 'codex', or 'claude_code'",
		},
		{
			name: "valid simulated codex version",
			config: map[string]any{
				"simulated_codex_version": "0.150.1",
			},
			expectError: false,
		},
		{
			name: "valid simulated codex version with two segments",
			config: map[string]any{
				"simulated_codex_version": "1.32",
			},
			expectError: false,
		},
		{
			name: "valid simulated claude code version",
			config: map[string]any{
				"simulated_claude_code_version": "2.2.0",
			},
			expectError: false,
		},
		{
			name: "valid simulated claude code version with many segments",
			config: map[string]any{
				"simulated_claude_code_version": "1.32.6.9.8",
			},
			expectError: false,
		},
		{
			name: "blank simulated codex version clears override",
			config: map[string]any{
				"simulated_codex_version": "   ",
			},
			expectError: false,
			assertConfig: func(t *testing.T, config map[string]any) {
				_, exists := config["simulated_codex_version"]
				assert.False(t, exists)
			},
		},
		{
			name: "blank simulated claude code version clears override",
			config: map[string]any{
				"simulated_claude_code_version": "",
			},
			expectError: false,
			assertConfig: func(t *testing.T, config map[string]any) {
				_, exists := config["simulated_claude_code_version"]
				assert.False(t, exists)
			},
		},
		{
			name: "invalid simulated codex version type",
			config: map[string]any{
				"simulated_codex_version": 1,
			},
			expectError: true,
			errorMsg:    "expected a string",
		},
		{
			name: "invalid simulated claude code version format",
			config: map[string]any{
				"simulated_claude_code_version": "2..1",
			},
			expectError: true,
			errorMsg:    "must be a dotted numeric version",
		},
		{
			name: "invalid simulated codex version non ascii digits",
			config: map[string]any{
				"simulated_codex_version": "١.٢.٣",
			},
			expectError: true,
			errorMsg:    "must be a dotted numeric version",
		},
		{
			name: "invalid simulated codex version single segment",
			config: map[string]any{
				"simulated_codex_version": "1",
			},
			expectError: true,
			errorMsg:    "must be a dotted numeric version",
		},
		{
			name: "valid health reset interval disabled",
			config: map[string]any{
				"health_reset_interval_seconds": float64(0),
			},
			expectError: false,
		},
		{
			name: "invalid health reset interval below enabled minimum",
			config: map[string]any{
				"health_reset_interval_seconds": float64(1799),
			},
			expectError: true,
			errorMsg:    "below minimum enabled value",
		},
		{
			name: "valid health reset interval thirty minute boundary",
			config: map[string]any{
				"health_reset_interval_seconds": float64(1800),
			},
			expectError: false,
		},
		{
			name: "valid health reset interval hour boundary",
			config: map[string]any{
				"health_reset_interval_seconds": float64(3600),
			},
			expectError: false,
		},
		{
			name: "valid health reset interval max boundary",
			config: map[string]any{
				"health_reset_interval_seconds": float64(31536000),
			},
			expectError: false,
		},
		{
			name: "invalid health reset interval over max",
			config: map[string]any{
				"health_reset_interval_seconds": float64(31536001),
			},
			expectError: true,
			errorMsg:    "exceeds maximum value",
		},
		{
			name: "valid health reset interval int64",
			config: map[string]any{
				"health_reset_interval_seconds": int64(1800),
			},
			expectError: false,
		},
		{
			name: "valid request timeout override",
			config: map[string]any{
				"request_timeout":           float64(120),
				"stream_first_byte_timeout": float64(0),
			},
			expectError: false,
		},
		{
			name: "valid request timeout only",
			config: map[string]any{
				"request_timeout": float64(120),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.ValidateGroupConfigOverrides(tt.config)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				if tt.assertConfig != nil {
					tt.assertConfig(t, tt.config)
				}
			}
		})
	}
}

func TestGetEffectiveConfigRequestTimeout(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"request_timeout":           float64(45),
		"stream_first_byte_timeout": float64(0),
	})

	assert.Equal(t, 45, cfg.RequestTimeout)
	assert.Equal(t, 0, cfg.StreamFirstByteTimeout)
}

func TestGetEffectiveConfigRequestTimeoutWithNonZeroStreamTimeout(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"request_timeout":           float64(45),
		"stream_first_byte_timeout": float64(30),
	})

	assert.Equal(t, 45, cfg.RequestTimeout)
	assert.Equal(t, 30, cfg.StreamFirstByteTimeout)
}

func TestGetEffectiveConfigRequestTimeoutDoesNotBackfillStreamFirstByte(t *testing.T) {
	manager := NewSystemSettingsManager()

	// request_timeout no longer backfills stream_first_byte_timeout (normalizeSplitRequestTimeouts is removed).
	cfg := manager.GetEffectiveConfig(map[string]any{
		"request_timeout": float64(75),
	})

	assert.Equal(t, 75, cfg.RequestTimeout)
	assert.Equal(t, 0, cfg.StreamFirstByteTimeout)
}

func TestGetEffectiveConfigKeepsExplicitStreamOverride(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"request_timeout":           float64(75),
		"stream_first_byte_timeout": float64(30),
	})

	assert.Equal(t, 75, cfg.RequestTimeout)
	assert.Equal(t, 30, cfg.StreamFirstByteTimeout)
}

func TestRequestTimeoutPersistsAcrossReload(t *testing.T) {
	testDB := setupSystemSettingsTestDB(t)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "request_timeout",
		SettingValue: "75",
	}).Error)

	memStore := store.NewMemoryStore()
	t.Cleanup(func() {
		require.NoError(t, memStore.Close())
	})

	manager := NewSystemSettingsManager()
	require.NoError(t, manager.Initialize(memStore, noopSystemSettingsGroupManager{}, false))
	t.Cleanup(func() {
		manager.Stop(context.Background())
	})

	settings := manager.GetSettings()
	assert.Equal(t, 75, settings.RequestTimeout)
	assert.Equal(t, 0, settings.StreamFirstByteTimeout)

	require.NoError(t, manager.UpdateSettings(map[string]any{
		"request_timeout": float64(90),
	}))
	require.NoError(t, manager.ReloadSettings())

	settings = manager.GetSettings()
	assert.Equal(t, 90, settings.RequestTimeout)
	assert.Equal(t, 0, settings.StreamFirstByteTimeout)
	assertSystemSettingValue(t, testDB, "request_timeout", "90")
}

// TestLegacyStreamRequestTimeoutMigrationKeepsExistingValue pins the "both keys
// present" case: stream_request_timeout must be dropped while the already-stored
// stream_first_byte_timeout value survives untouched. It deterministically mirrors
// the concurrent-write scenario — the real race protection (conflict updates only
// updated_at plus an in-transaction re-read) is enforced at the database layer, so
// no flaky multi-goroutine timing test is used.

func TestLegacyStreamRequestTimeoutMigrationKeepsExistingValue(t *testing.T) {
	testDB := setupSystemSettingsTestDB(t)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "stream_request_timeout",
		SettingValue: "5",
	}).Error)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "stream_first_byte_timeout",
		SettingValue: "60",
	}).Error)

	memStore := store.NewMemoryStore()
	t.Cleanup(func() {
		require.NoError(t, memStore.Close())
	})

	manager := NewSystemSettingsManager()
	require.NoError(t, manager.Initialize(memStore, noopSystemSettingsGroupManager{}, false))
	t.Cleanup(func() {
		manager.Stop(context.Background())
	})

	assert.Equal(t, 60, manager.GetSettings().StreamFirstByteTimeout)
	assertSystemSettingValue(t, testDB, "stream_first_byte_timeout", "60")

	var legacy models.SystemSetting
	require.ErrorIs(t, testDB.Where("setting_key = ?", "stream_request_timeout").First(&legacy).Error, gorm.ErrRecordNotFound)

}

// TestLegacyStreamRequestTimeoutMigrationBackfillsNewKey pins the normal one-time
// migration: only the legacy key exists, so its value must be copied to
// stream_first_byte_timeout and the legacy key removed.

func TestLegacyStreamRequestTimeoutMigrationBackfillsNewKey(t *testing.T) {
	testDB := setupSystemSettingsTestDB(t)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "stream_request_timeout",
		SettingValue: "5",
	}).Error)

	memStore := store.NewMemoryStore()
	t.Cleanup(func() {
		require.NoError(t, memStore.Close())
	})

	manager := NewSystemSettingsManager()
	require.NoError(t, manager.Initialize(memStore, noopSystemSettingsGroupManager{}, false))
	t.Cleanup(func() {
		manager.Stop(context.Background())
	})

	assert.Equal(t, 5, manager.GetSettings().StreamFirstByteTimeout)
	assertSystemSettingValue(t, testDB, "stream_first_byte_timeout", "5")

	var legacy models.SystemSetting
	require.ErrorIs(t, testDB.Where("setting_key = ?", "stream_request_timeout").First(&legacy).Error, gorm.ErrRecordNotFound)

}

func TestGetEffectiveConfigExplicitZeroRequestTimeout(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"request_timeout": float64(0),
	})

	assert.Equal(t, 0, cfg.RequestTimeout)
	assert.Equal(t, 0, cfg.StreamFirstByteTimeout)
}

// TestLegacyNonStreamRequestTimeoutMigrationBackfillsRequestTimeout pins the
// one-time non-stream key rename: request_timeout now bounds both stream and
// non-stream request lifecycles, so the stored non_stream value must be copied
// over and the legacy key removed.

func TestLegacyNonStreamRequestTimeoutMigrationBackfillsRequestTimeout(t *testing.T) {
	testDB := setupSystemSettingsTestDB(t)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "non_stream_request_timeout",
		SettingValue: "45",
	}).Error)

	memStore := store.NewMemoryStore()
	t.Cleanup(func() {
		require.NoError(t, memStore.Close())
	})

	manager := NewSystemSettingsManager()
	require.NoError(t, manager.Initialize(memStore, noopSystemSettingsGroupManager{}, false))
	t.Cleanup(func() {
		manager.Stop(context.Background())
	})

	assert.Equal(t, 45, manager.GetSettings().RequestTimeout)
	assertSystemSettingValue(t, testDB, "request_timeout", "45")

	var legacy models.SystemSetting
	require.ErrorIs(t, testDB.Where("setting_key = ?", "non_stream_request_timeout").First(&legacy).Error, gorm.ErrRecordNotFound)
}

// TestLegacyNonStreamRequestTimeoutMigrationKeepsExistingValue pins the "both keys
// present" case: request_timeout (already written by the legacy split-timeout sync)
// is authoritative, the legacy non_stream key is dropped and the stored value survives.

// TestEnsureSettingsInitializedMigratesLegacyTimeoutKeys pins the startup ordering
// (CodeRabbit): EnsureSettingsInitialized runs before the SystemSettingsManager
// loader and inserts default rows for request_timeout/stream_first_byte_timeout.
// Without migrating legacy keys first, the loader would see both key forms and
// delete the legacy key, losing the user's custom values to the defaults. Both
// legacy keys must be migrated (value preserved), not deleted by a conflict.
func TestEnsureSettingsInitializedMigratesLegacyTimeoutKeys(t *testing.T) {
	testDB := setupSystemSettingsTestDB(t)

	// Simulate a pre-upgrade database holding only legacy custom values.
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "non_stream_request_timeout",
		SettingValue: "300",
	}).Error)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "stream_request_timeout",
		SettingValue: "120",
	}).Error)

	manager := NewSystemSettingsManager()
	require.NoError(t, manager.EnsureSettingsInitialized(types.AuthConfig{}))

	// Custom legacy values must survive as the replacement keys.
	assertSystemSettingValue(t, testDB, "request_timeout", "300")
	assertSystemSettingValue(t, testDB, "stream_first_byte_timeout", "120")

	// Legacy keys must be gone.
	for _, legacyKey := range []string{"non_stream_request_timeout", "stream_request_timeout"} {
		var legacy models.SystemSetting
		require.ErrorIs(t, testDB.Where("setting_key = ?", legacyKey).First(&legacy).Error, gorm.ErrRecordNotFound,
			"legacy key %s must be removed after migration", legacyKey)
	}
}

// TestEnsureSettingsInitializedLegacyConflictKeepsReplacement pins the "both key
// forms exist" case during initialization: when the replacement key already holds
// a value, EnsureSettingsInitialized must drop the legacy key and keep the
// replacement value (no unconditional legacy migration overwriting it).
func TestEnsureSettingsInitializedLegacyConflictKeepsReplacement(t *testing.T) {
	testDB := setupSystemSettingsTestDB(t)

	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "request_timeout",
		SettingValue: "60",
	}).Error)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "non_stream_request_timeout",
		SettingValue: "300",
	}).Error)

	manager := NewSystemSettingsManager()
	require.NoError(t, manager.EnsureSettingsInitialized(types.AuthConfig{}))

	assertSystemSettingValue(t, testDB, "request_timeout", "60")
	var legacy models.SystemSetting
	require.ErrorIs(t, testDB.Where("setting_key = ?", "non_stream_request_timeout").First(&legacy).Error, gorm.ErrRecordNotFound)
}

// TestMigrateSettingKeyInTx pins the transactional rename helper semantics
// (CodeRabbit race fix): the legacy value is read with a locking SELECT inside
// the transaction (never from a pre-transaction snapshot), so the migrated value
// is whatever the row holds when the transaction starts; a source row that has
// already vanished is a no-op instead of a spurious error.
func TestMigrateSettingKeyInTx(t *testing.T) {
	t.Run("migrates the value committed before the locking read", func(t *testing.T) {
		testDB := setupSystemSettingsTestDB(t)
		require.NoError(t, testDB.Create(&models.SystemSetting{
			SettingKey:   "non_stream_request_timeout",
			SettingValue: "300",
		}).Error)

		// A concurrent writer lands BEFORE the migration transaction starts:
		// its value must be the one migrated (proving the snapshot from the
		// pre-transaction read is no longer used).
		require.NoError(t, testDB.Model(&models.SystemSetting{}).
			Where("setting_key = ?", "non_stream_request_timeout").
			Update("setting_value", "999").Error)

		var value string
		err := testDB.Transaction(func(tx *gorm.DB) error {
			var innerErr error
			value, _, innerErr = migrateSettingKeyInTx(tx, "non_stream_request_timeout", "request_timeout")
			return innerErr
		})
		require.NoError(t, err)
		assert.Equal(t, "999", value)
		assertSystemSettingValue(t, testDB, "request_timeout", "999")
		var legacy models.SystemSetting
		require.ErrorIs(t, testDB.Where("setting_key = ?", "non_stream_request_timeout").First(&legacy).Error, gorm.ErrRecordNotFound)
	})

	t.Run("missing source key is a no-op", func(t *testing.T) {
		testDB := setupSystemSettingsTestDB(t)

		var migrated bool
		err := testDB.Transaction(func(tx *gorm.DB) error {
			var innerErr error
			_, migrated, innerErr = migrateSettingKeyInTx(tx, "non_stream_request_timeout", "request_timeout")
			return innerErr
		})
		require.NoError(t, err)
		assert.False(t, migrated, "a missing source key must not be reported as migrated")
		var count int64
		require.NoError(t, testDB.Model(&models.SystemSetting{}).Count(&count).Error)
		assert.Zero(t, count, "a missing source key must not create any row")
	})

	t.Run("conflicting replacement keeps its value and drops the legacy row", func(t *testing.T) {
		testDB := setupSystemSettingsTestDB(t)
		require.NoError(t, testDB.Create(&models.SystemSetting{
			SettingKey:   "request_timeout",
			SettingValue: "60",
		}).Error)
		require.NoError(t, testDB.Create(&models.SystemSetting{
			SettingKey:   "non_stream_request_timeout",
			SettingValue: "300",
		}).Error)

		var value string
		err := testDB.Transaction(func(tx *gorm.DB) error {
			var innerErr error
			value, _, innerErr = migrateSettingKeyInTx(tx, "non_stream_request_timeout", "request_timeout")
			return innerErr
		})
		require.NoError(t, err)
		assert.Equal(t, "60", value, "the existing replacement value stays authoritative on conflict")
		assertSystemSettingValue(t, testDB, "request_timeout", "60")
		var legacy models.SystemSetting
		require.ErrorIs(t, testDB.Where("setting_key = ?", "non_stream_request_timeout").First(&legacy).Error, gorm.ErrRecordNotFound)
	})
}

func TestLegacyNonStreamRequestTimeoutMigrationKeepsExistingValue(t *testing.T) {
	testDB := setupSystemSettingsTestDB(t)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "request_timeout",
		SettingValue: "60",
	}).Error)
	require.NoError(t, testDB.Create(&models.SystemSetting{
		SettingKey:   "non_stream_request_timeout",
		SettingValue: "45",
	}).Error)

	memStore := store.NewMemoryStore()
	t.Cleanup(func() {
		require.NoError(t, memStore.Close())
	})

	manager := NewSystemSettingsManager()
	require.NoError(t, manager.Initialize(memStore, noopSystemSettingsGroupManager{}, false))
	t.Cleanup(func() {
		manager.Stop(context.Background())
	})

	assert.Equal(t, 60, manager.GetSettings().RequestTimeout)
	assertSystemSettingValue(t, testDB, "request_timeout", "60")

	var legacy models.SystemSetting
	require.ErrorIs(t, testDB.Where("setting_key = ?", "non_stream_request_timeout").First(&legacy).Error, gorm.ErrRecordNotFound)
}

func TestGetEffectiveConfigRetryDelayOverride(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"retry_delay_ms":            float64(1500),
		"retry_backoff_enabled":     true,
		"retry_backoff_max_percent": float64(300),
	})

	assert.Equal(t, 1500, cfg.RetryDelayMs)
	assert.True(t, cfg.RetryBackoffEnabled)
	assert.Equal(t, 300, cfg.RetryBackoffMaxPercent)
}

func TestGetEffectiveConfigResolvesSystemProxyWhenGroupConfigMarshalFails(t *testing.T) {
	manager := setupSystemSettingsManagerWithSettings(t, types.SystemSettings{
		ProxyURL: utils.BuildProxyPoolItemRef(10),
	})
	manager.SetProxyURLResolver(staticProxyURLResolver{resolved: "http://proxy.example.com:8080"})

	cfg := manager.GetEffectiveConfig(datatypes.JSONMap{
		"invalid": func() {},
	})

	assert.Equal(t, "http://proxy.example.com:8080", cfg.ProxyURL)
}

func TestGetEffectiveConfigResolvesSystemProxyWhenGroupConfigUnmarshalFails(t *testing.T) {
	manager := setupSystemSettingsManagerWithSettings(t, types.SystemSettings{
		ProxyURL: utils.BuildProxyPoolItemRef(11),
	})
	manager.SetProxyURLResolver(staticProxyURLResolver{resolved: "http://proxy.example.com:8080"})

	cfg := manager.GetEffectiveConfig(datatypes.JSONMap{
		"proxy_url": []string{"invalid"},
	})

	assert.Equal(t, "http://proxy.example.com:8080", cfg.ProxyURL)
}

func TestGetEffectiveConfigAllowsGroupSkipTLSVerifyOverride(t *testing.T) {
	manager := setupSystemSettingsManagerWithSettings(t, types.SystemSettings{
		SkipTLSVerify: true,
	})

	inherited := manager.GetEffectiveConfig(datatypes.JSONMap{})
	assert.True(t, inherited.SkipTLSVerify)

	overridden := manager.GetEffectiveConfig(datatypes.JSONMap{
		"skip_tls_verify": false,
	})
	assert.False(t, overridden.SkipTLSVerify)
}

func TestResolveRuntimeProxyURLKeepsReferenceWhenResolverUnavailable(t *testing.T) {
	manager := NewSystemSettingsManager()
	ref := utils.BuildProxyPoolItemRef(12)

	resolved := manager.ResolveRuntimeProxyURL(context.Background(), " "+ref+" ")

	assert.Equal(t, ref, resolved)
}

func TestResolveRuntimeProxyURLKeepsReferenceWhenResolverFails(t *testing.T) {
	manager := NewSystemSettingsManager()
	manager.SetProxyURLResolver(staticProxyURLResolver{err: errors.New("missing proxy")})
	ref := utils.BuildProxyPoolItemRef(13)

	resolved := manager.ResolveRuntimeProxyURL(context.Background(), ref)

	assert.Equal(t, ref, resolved)
}

func TestResolveRuntimeProxyURLKeepsReferenceWhenResolverReturnsBlank(t *testing.T) {
	manager := NewSystemSettingsManager()
	manager.SetProxyURLResolver(staticProxyURLResolver{resolved: " \t "})
	ref := utils.BuildProxyPoolItemRef(14)

	resolved := manager.ResolveRuntimeProxyURL(context.Background(), ref)

	assert.Equal(t, ref, resolved)
}

// TestDisplaySystemConfig tests displaying system configuration
func TestDisplaySystemConfig(t *testing.T) {
	manager := NewSystemSettingsManager()
	settings := utils.DefaultSystemSettings()

	// Should not panic
	assert.NotPanics(t, func() {
		manager.DisplaySystemConfig(settings)
	})
}

// BenchmarkSystemSettingsManager benchmarks system settings manager creation
func BenchmarkSystemSettingsManager(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewSystemSettingsManager()
	}
}

// BenchmarkGetSettings benchmarks getting settings
func BenchmarkGetSettings(b *testing.B) {
	manager := NewSystemSettingsManager()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.GetSettings()
	}
}

// BenchmarkValidateSettings benchmarks settings validation
func BenchmarkValidateSettings(b *testing.B) {
	manager := NewSystemSettingsManager()
	settings := map[string]any{
		"request_timeout": float64(60),
		"max_retries":     float64(3),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.ValidateSettings(settings)
	}
}

// BenchmarkValidateGroupConfigOverrides benchmarks group config validation
func BenchmarkValidateGroupConfigOverrides(b *testing.B) {
	manager := NewSystemSettingsManager()
	config := map[string]any{
		"sub_max_retries":     float64(3),
		"force_function_call": true,
		"cc_support":          true,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.ValidateGroupConfigOverrides(config)
	}
}
