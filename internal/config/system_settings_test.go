package config

import (
	"testing"

	"gpt-load/internal/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// TestGetSettings tests getting system settings without initialization
func TestGetSettings(t *testing.T) {
	manager := NewSystemSettingsManager()

	// Should return default settings when not initialized
	settings := manager.GetSettings()
	assert.NotNil(t, settings)
	assert.Greater(t, settings.NonStreamRequestTimeout, 0)
	assert.Zero(t, settings.StreamRequestTimeout)
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
				"non_stream_request_timeout": float64(60),
			},
			expectError: false,
		},
		{
			name: "valid non-stream timeout disabled",
			settings: map[string]any{
				"non_stream_request_timeout": float64(0),
			},
			expectError: false,
		},
		{
			name: "valid stream timeout disabled",
			settings: map[string]any{
				"stream_request_timeout": float64(0),
			},
			expectError: false,
		},
		{
			name: "valid legacy request timeout",
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
				"non_stream_request_timeout": "not_a_number",
			},
			expectError: true,
			errorMsg:    "expected a number",
		},
		{
			name: "non-stream timeout below minimum",
			settings: map[string]any{
				"non_stream_request_timeout": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "stream timeout below minimum",
			settings: map[string]any{
				"stream_request_timeout": float64(-1),
			},
			expectError: true,
			errorMsg:    "below minimum value",
		},
		{
			name: "non-integer float value",
			settings: map[string]any{
				"non_stream_request_timeout": float64(30.5),
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
		name        string
		config      map[string]any
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid sub_max_retries",
			config: map[string]any{
				"sub_max_retries": float64(3),
			},
			expectError: false,
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
			name: "valid Responses encrypted reasoning include",
			config: map[string]any{
				"responses_include_encrypted_reasoning": true,
			},
			expectError: false,
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
			name: "valid split timeout overrides",
			config: map[string]any{
				"non_stream_request_timeout": float64(120),
				"stream_request_timeout":     float64(0),
			},
			expectError: false,
		},
		{
			name: "valid legacy timeout override",
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
			}
		})
	}
}

func TestGetEffectiveConfigSplitTimeouts(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"non_stream_request_timeout": float64(45),
		"stream_request_timeout":     float64(0),
	})

	assert.Equal(t, 45, cfg.NonStreamRequestTimeout)
	assert.Equal(t, 0, cfg.StreamRequestTimeout)
	assert.Equal(t, cfg.NonStreamRequestTimeout, cfg.RequestTimeout)
}

func TestGetEffectiveConfigSplitTimeoutsWithNonZeroStreamTimeout(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"non_stream_request_timeout": float64(45),
		"stream_request_timeout":     float64(30),
	})

	assert.Equal(t, 45, cfg.NonStreamRequestTimeout)
	assert.Equal(t, 30, cfg.StreamRequestTimeout)
	assert.Equal(t, cfg.NonStreamRequestTimeout, cfg.RequestTimeout)
}

func TestGetEffectiveConfigLegacyRequestTimeout(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"request_timeout": float64(75),
	})

	assert.Equal(t, 75, cfg.NonStreamRequestTimeout)
	assert.Equal(t, 0, cfg.StreamRequestTimeout)
	assert.Equal(t, cfg.NonStreamRequestTimeout, cfg.RequestTimeout)
}

func TestGetEffectiveConfigExplicitZeroNonStreamTimeoutDisablesLegacyFallback(t *testing.T) {
	manager := NewSystemSettingsManager()

	cfg := manager.GetEffectiveConfig(map[string]any{
		"request_timeout":            float64(75),
		"non_stream_request_timeout": float64(0),
	})

	assert.Equal(t, 0, cfg.NonStreamRequestTimeout)
	assert.Equal(t, 0, cfg.RequestTimeout)
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
