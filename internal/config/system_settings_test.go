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
	assert.Greater(t, settings.RequestTimeout, 0)
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
			// Clear env vars first to ensure test isolation
			t.Setenv("HOST", "")
			t.Setenv("PORT", "")

			if tt.host != "" {
				t.Setenv("HOST", tt.host)
			}
			if tt.port != "" {
				t.Setenv("PORT", tt.port)
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
			name: "valid string setting",
			settings: map[string]any{
				"app_url": "http://localhost:3001",
			},
			expectError: false,
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
			name: "value below minimum",
			settings: map[string]any{
				"request_timeout": float64(0),
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
			name: "valid system setting override",
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
