package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewManager tests the creation of a new configuration manager
func TestNewManager(t *testing.T) {
	// Setup test environment
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)

	require.NoError(t, err)
	require.NotNil(t, manager)

	// Verify default values
	assert.Equal(t, 3001, manager.GetEffectiveServerConfig().Port)
	assert.Equal(t, "0.0.0.0", manager.GetEffectiveServerConfig().Host)
	assert.True(t, manager.IsMaster())
}

// TestManagerReloadConfig tests configuration reloading
func TestManagerReloadConfig(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	settingsManager := &SystemSettingsManager{}
	manager := &Manager{settingsManager: settingsManager}

	// Set custom environment variables
	t.Setenv("PORT", "8080")
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("MAX_CONCURRENT_REQUESTS", "200")

	err := manager.ReloadConfig()
	require.NoError(t, err)

	assert.Equal(t, 8080, manager.GetEffectiveServerConfig().Port)
	assert.Equal(t, "127.0.0.1", manager.GetEffectiveServerConfig().Host)
	assert.Equal(t, 200, manager.GetPerformanceConfig().MaxConcurrentRequests)
}

// TestManagerValidation tests configuration validation
func TestManagerValidation(t *testing.T) {
	tests := []struct {
		name        string
		setupEnv    func(*testing.T)
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid configuration",
			setupEnv: func(t *testing.T) {
				setupTestEnv(t)
			},
			expectError: false,
		},
		{
			name: "invalid port - too low",
			setupEnv: func(t *testing.T) {
				setupTestEnv(t)
				t.Setenv("PORT", "0")
			},
			expectError: true,
			errorMsg:    "port must be between",
		},
		{
			name: "invalid port - too high",
			setupEnv: func(t *testing.T) {
				setupTestEnv(t)
				t.Setenv("PORT", "70000")
			},
			expectError: true,
			errorMsg:    "port must be between",
		},
		{
			name: "missing auth key",
			setupEnv: func(t *testing.T) {
				setupTestEnv(t)
				os.Unsetenv("AUTH_KEY")
			},
			expectError: true,
			errorMsg:    "AUTH_KEY is required",
		},
		{
			name: "invalid max concurrent requests",
			setupEnv: func(t *testing.T) {
				setupTestEnv(t)
				t.Setenv("MAX_CONCURRENT_REQUESTS", "0")
			},
			expectError: true,
			errorMsg:    "max concurrent requests cannot be less than 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv(t)
			defer cleanupTestEnv(t)

			settingsManager := &SystemSettingsManager{}
			manager := &Manager{settingsManager: settingsManager}
			err := manager.ReloadConfig()

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestManagerGetters tests all getter methods
func TestManagerGetters(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("REDIS_DSN", "redis://localhost:6379")
	os.Setenv("ENCRYPTION_KEY", "test-encryption-key-32-bytes!!")
	os.Setenv("DEBUG_MODE", "true")
	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:8080")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	// Test IsMaster
	assert.True(t, manager.IsMaster())

	// Test GetAuthConfig
	authConfig := manager.GetAuthConfig()
	assert.NotEmpty(t, authConfig.Key)

	// Test GetCORSConfig
	corsConfig := manager.GetCORSConfig()
	assert.True(t, corsConfig.Enabled)
	assert.Len(t, corsConfig.AllowedOrigins, 2)

	// Test GetPerformanceConfig
	perfConfig := manager.GetPerformanceConfig()
	assert.Greater(t, perfConfig.MaxConcurrentRequests, 0)

	// Test GetLogConfig
	logConfig := manager.GetLogConfig()
	assert.NotEmpty(t, logConfig.Level)

	// Test GetRedisDSN
	redisDSN := manager.GetRedisDSN()
	assert.Equal(t, "redis://localhost:6379", redisDSN)

	// Test GetEncryptionKey
	encKey := manager.GetEncryptionKey()
	assert.Equal(t, "test-encryption-key-32-bytes!!", encKey)

	// Test IsDebugMode
	assert.True(t, manager.IsDebugMode())

	// Test GetDatabaseConfig
	dbConfig := manager.GetDatabaseConfig()
	assert.NotEmpty(t, dbConfig.DSN)
}

// TestManagerCORSValidation tests CORS configuration validation
func TestManagerCORSValidation(t *testing.T) {
	tests := []struct {
		name         string
		enableCORS   string
		origins      string
		expectError  bool
		expectWarn   bool
	}{
		{
			name:        "CORS disabled",
			enableCORS:  "false",
			origins:     "",
			expectError: false,
			expectWarn:  false,
		},
		{
			name:        "CORS enabled with valid origins",
			enableCORS:  "true",
			origins:     "http://localhost:3000",
			expectError: false,
			expectWarn:  false,
		},
		{
			name:        "CORS enabled without origins",
			enableCORS:  "true",
			origins:     "",
			expectError: true,
			expectWarn:  false,
		},
		{
			name:        "CORS enabled with wildcard",
			enableCORS:  "true",
			origins:     "*",
			expectError: false,
			expectWarn:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			os.Setenv("ENABLE_CORS", tt.enableCORS)
			if tt.origins != "" {
				os.Setenv("ALLOWED_ORIGINS", tt.origins)
			} else {
				os.Unsetenv("ALLOWED_ORIGINS")
			}

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, manager)
			}
		})
	}
}

// TestManagerTimeoutValidation tests timeout configuration validation
func TestManagerTimeoutValidation(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	// Test graceful shutdown timeout minimum
	os.Setenv("SERVER_GRACEFUL_SHUTDOWN_TIMEOUT", "5")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	// Should be reset to minimum 10 seconds
	assert.Equal(t, 10, manager.GetEffectiveServerConfig().GracefulShutdownTimeout)
}

// setupTestEnv sets up a test environment with required variables
func setupTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AUTH_KEY", "test-auth-key-minimum-16-chars")
	t.Setenv("PORT", "3001")
	t.Setenv("DATABASE_DSN", ":memory:")
}

// setupBenchEnv sets up environment for benchmarks (no testing.T required)
func setupBenchEnv() {
	os.Setenv("AUTH_KEY", "test-auth-key-minimum-16-chars")
	os.Setenv("PORT", "3001")
	os.Setenv("DATABASE_DSN", ":memory:")
}

// cleanupTestEnv cleans up test environment variables
func cleanupTestEnv(t *testing.T) {
	os.Unsetenv("AUTH_KEY")
	os.Unsetenv("PORT")
	os.Unsetenv("HOST")
	os.Unsetenv("DATABASE_DSN")
	os.Unsetenv("REDIS_DSN")
	os.Unsetenv("ENCRYPTION_KEY")
	os.Unsetenv("DEBUG_MODE")
	os.Unsetenv("ENABLE_CORS")
	os.Unsetenv("ALLOWED_ORIGINS")
	os.Unsetenv("MAX_CONCURRENT_REQUESTS")
	os.Unsetenv("SERVER_GRACEFUL_SHUTDOWN_TIMEOUT")
	os.Unsetenv("LOG_LEVEL")
}

// cleanupBenchEnv cleans up environment for benchmarks
func cleanupBenchEnv() {
	os.Unsetenv("AUTH_KEY")
	os.Unsetenv("PORT")
	os.Unsetenv("HOST")
	os.Unsetenv("DATABASE_DSN")
	os.Unsetenv("REDIS_DSN")
	os.Unsetenv("ENCRYPTION_KEY")
	os.Unsetenv("DEBUG_MODE")
	os.Unsetenv("ENABLE_CORS")
	os.Unsetenv("ALLOWED_ORIGINS")
	os.Unsetenv("MAX_CONCURRENT_REQUESTS")
	os.Unsetenv("SERVER_GRACEFUL_SHUTDOWN_TIMEOUT")
	os.Unsetenv("LOG_LEVEL")
}

// BenchmarkNewManager benchmarks configuration manager creation
func BenchmarkNewManager(b *testing.B) {
	setupBenchEnv()
	defer cleanupBenchEnv()

	settingsManager := &SystemSettingsManager{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = NewManager(settingsManager)
	}
}

// BenchmarkReloadConfig benchmarks configuration reloading
func BenchmarkReloadConfig(b *testing.B) {
	setupBenchEnv()
	defer cleanupBenchEnv()

	settingsManager := &SystemSettingsManager{}
	manager := &Manager{settingsManager: settingsManager}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.ReloadConfig()
	}
}

// BenchmarkValidate benchmarks configuration validation
func BenchmarkValidate(b *testing.B) {
	setupBenchEnv()
	defer cleanupBenchEnv()

	settingsManager := &SystemSettingsManager{}
	manager, _ := NewManager(settingsManager)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.Validate()
	}
}

// TestDisplayServerConfig tests the display of server configuration
func TestDisplayServerConfig(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("REDIS_DSN", "redis://localhost:6379")
	os.Setenv("ENCRYPTION_KEY", "test-encryption-key-32-bytes!!")
	os.Setenv("LOG_ENABLE_FILE", "true")
	os.Setenv("LOG_FILE_PATH", "./test.log")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	// Should not panic
	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})
}

// TestManagerSlaveMode tests slave mode configuration
func TestManagerSlaveMode(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("IS_SLAVE", "true")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.False(t, manager.IsMaster())
}

// TestManagerLogConfig tests log configuration
func TestManagerLogConfig(t *testing.T) {
	tests := []struct {
		name       string
		logLevel   string
		logFormat  string
		enableFile string
		filePath   string
	}{
		{
			name:       "default log config",
			logLevel:   "",
			logFormat:  "",
			enableFile: "",
			filePath:   "",
		},
		{
			name:       "custom log config",
			logLevel:   "debug",
			logFormat:  "json",
			enableFile: "true",
			filePath:   "/var/log/app.log",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			if tt.logLevel != "" {
				os.Setenv("LOG_LEVEL", tt.logLevel)
			}
			if tt.logFormat != "" {
				os.Setenv("LOG_FORMAT", tt.logFormat)
			}
			if tt.enableFile != "" {
				os.Setenv("LOG_ENABLE_FILE", tt.enableFile)
			}
			if tt.filePath != "" {
				os.Setenv("LOG_FILE_PATH", tt.filePath)
			}

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)
			require.NoError(t, err)

			logConfig := manager.GetLogConfig()
			if tt.logLevel != "" {
				assert.Equal(t, tt.logLevel, logConfig.Level)
			} else {
				assert.Equal(t, "info", logConfig.Level)
			}
			if tt.logFormat != "" {
				assert.Equal(t, tt.logFormat, logConfig.Format)
			} else {
				assert.Equal(t, "text", logConfig.Format)
			}
		})
	}
}

// TestManagerServerTimeouts tests server timeout configurations
func TestManagerServerTimeouts(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("SERVER_READ_TIMEOUT", "30")
	os.Setenv("SERVER_WRITE_TIMEOUT", "300")
	os.Setenv("SERVER_IDLE_TIMEOUT", "60")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	serverConfig := manager.GetEffectiveServerConfig()
	assert.Equal(t, 30, serverConfig.ReadTimeout)
	assert.Equal(t, 300, serverConfig.WriteTimeout)
	assert.Equal(t, 60, serverConfig.IdleTimeout)
}

// TestManagerCORSMethods tests CORS methods configuration
func TestManagerCORSMethods(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")
	os.Setenv("ALLOWED_METHODS", "GET,POST,PUT")
	os.Setenv("ALLOWED_HEADERS", "Content-Type,Authorization")
	os.Setenv("ALLOW_CREDENTIALS", "true")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	corsConfig := manager.GetCORSConfig()
	assert.True(t, corsConfig.Enabled)
	assert.Len(t, corsConfig.AllowedMethods, 3)
	assert.Contains(t, corsConfig.AllowedMethods, "GET")
	assert.Contains(t, corsConfig.AllowedMethods, "POST")
	assert.Contains(t, corsConfig.AllowedMethods, "PUT")
	assert.Len(t, corsConfig.AllowedHeaders, 2)
	assert.True(t, corsConfig.AllowCredentials)
}

// TestManagerWithoutEncryption tests configuration without encryption key
func TestManagerWithoutEncryption(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Unsetenv("ENCRYPTION_KEY")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.Empty(t, manager.GetEncryptionKey())
}

// BenchmarkGetters benchmarks all getter methods
func BenchmarkGetters(b *testing.B) {
	setupBenchEnv()
	defer cleanupBenchEnv()

	settingsManager := &SystemSettingsManager{}
	manager, _ := NewManager(settingsManager)

	b.Run("IsMaster", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = manager.IsMaster()
		}
	})

	b.Run("GetAuthConfig", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = manager.GetAuthConfig()
		}
	})

	b.Run("GetCORSConfig", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = manager.GetCORSConfig()
		}
	})

	b.Run("GetPerformanceConfig", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = manager.GetPerformanceConfig()
		}
	})

	b.Run("GetLogConfig", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = manager.GetLogConfig()
		}
	})

	b.Run("GetEffectiveServerConfig", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = manager.GetEffectiveServerConfig()
		}
	})
}

// TestManagerDatabaseConfig tests database configuration
func TestManagerDatabaseConfig(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("DATABASE_DSN", "./test.db")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	dbConfig := manager.GetDatabaseConfig()
	assert.Equal(t, "./test.db", dbConfig.DSN)
}

// TestManagerRedisDSN tests Redis DSN configuration
func TestManagerRedisDSN(t *testing.T) {
	tests := []struct {
		name     string
		redisDSN string
	}{
		{"with Redis", "redis://localhost:6379"},
		{"without Redis", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			if tt.redisDSN != "" {
				os.Setenv("REDIS_DSN", tt.redisDSN)
			}

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)
			require.NoError(t, err)

			assert.Equal(t, tt.redisDSN, manager.GetRedisDSN())
		})
	}
}

// TestManagerDebugMode tests debug mode configuration
func TestManagerDebugMode(t *testing.T) {
	tests := []struct {
		name      string
		debugMode string
		expected  bool
	}{
		{"debug enabled", "true", true},
		{"debug disabled", "false", false},
		{"debug not set", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			if tt.debugMode != "" {
				os.Setenv("DEBUG_MODE", tt.debugMode)
			}

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, manager.IsDebugMode())
		})
	}
}

// TestManagerAllTimeouts tests all timeout configurations
func TestManagerAllTimeouts(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("SERVER_READ_TIMEOUT", "45")
	os.Setenv("SERVER_WRITE_TIMEOUT", "450")
	os.Setenv("SERVER_IDLE_TIMEOUT", "90")
	os.Setenv("SERVER_GRACEFUL_SHUTDOWN_TIMEOUT", "15")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	serverConfig := manager.GetEffectiveServerConfig()
	assert.Equal(t, 45, serverConfig.ReadTimeout)
	assert.Equal(t, 450, serverConfig.WriteTimeout)
	assert.Equal(t, 90, serverConfig.IdleTimeout)
	assert.Equal(t, 15, serverConfig.GracefulShutdownTimeout)
}

// TestManagerCORSAllOptions tests all CORS options
func TestManagerCORSAllOptions(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:8080")
	os.Setenv("ALLOWED_METHODS", "GET,POST,PUT,DELETE,PATCH")
	os.Setenv("ALLOWED_HEADERS", "Content-Type,Authorization,X-Custom-Header")
	os.Setenv("ALLOW_CREDENTIALS", "true")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	corsConfig := manager.GetCORSConfig()
	assert.True(t, corsConfig.Enabled)
	assert.Len(t, corsConfig.AllowedOrigins, 2)
	assert.Len(t, corsConfig.AllowedMethods, 5)
	assert.Len(t, corsConfig.AllowedHeaders, 3)
	assert.True(t, corsConfig.AllowCredentials)
}

// TestManagerLogConfigAllOptions tests all log configuration options
func TestManagerLogConfigAllOptions(t *testing.T) {
	tests := []struct {
		name       string
		level      string
		format     string
		enableFile string
		filePath   string
	}{
		{"debug json with file", "debug", "json", "true", "/var/log/app.log"},
		{"info text without file", "info", "text", "false", ""},
		{"warn json without file", "warn", "json", "false", ""},
		{"error text with file", "error", "text", "true", "./logs/error.log"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			os.Setenv("LOG_LEVEL", tt.level)
			os.Setenv("LOG_FORMAT", tt.format)
			os.Setenv("LOG_ENABLE_FILE", tt.enableFile)
			if tt.filePath != "" {
				os.Setenv("LOG_FILE_PATH", tt.filePath)
			}

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)
			require.NoError(t, err)

			logConfig := manager.GetLogConfig()
			assert.Equal(t, tt.level, logConfig.Level)
			assert.Equal(t, tt.format, logConfig.Format)
			if tt.enableFile == "true" {
				assert.True(t, logConfig.EnableFile)
				if tt.filePath != "" {
					assert.Equal(t, tt.filePath, logConfig.FilePath)
				}
			}
		})
	}
}

// TestManagerPerformanceConfig tests performance configuration
func TestManagerPerformanceConfig(t *testing.T) {
	tests := []struct {
		name           string
		maxConcurrent  string
		expectedValue  int
	}{
		{"default", "", 100},
		{"custom low", "50", 50},
		{"custom high", "500", 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			if tt.maxConcurrent != "" {
				os.Setenv("MAX_CONCURRENT_REQUESTS", tt.maxConcurrent)
			}

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)
			require.NoError(t, err)

			perfConfig := manager.GetPerformanceConfig()
			assert.Equal(t, tt.expectedValue, perfConfig.MaxConcurrentRequests)
		})
	}
}

// TestManagerValidationMultipleErrors tests validation with multiple errors
func TestManagerValidationMultipleErrors(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("PORT", "0")
	os.Setenv("MAX_CONCURRENT_REQUESTS", "0")
	os.Unsetenv("AUTH_KEY")

	settingsManager := &SystemSettingsManager{}
	manager := &Manager{settingsManager: settingsManager}
	err := manager.ReloadConfig()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "port must be between")
	assert.Contains(t, err.Error(), "max concurrent requests")
	assert.Contains(t, err.Error(), "AUTH_KEY is required")
}

// TestManagerDisplayServerConfigWithAllOptions tests display with all options
func TestManagerDisplayServerConfigWithAllOptions(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("HOST", "127.0.0.1")
	os.Setenv("PORT", "8080")
	os.Setenv("REDIS_DSN", "redis://localhost:6379")
	os.Setenv("ENCRYPTION_KEY", "test-encryption-key-32-bytes!!")
	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")
	os.Setenv("LOG_ENABLE_FILE", "true")
	os.Setenv("LOG_FILE_PATH", "./test.log")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})
}

// TestManagerReloadConfigMultipleTimes tests reloading config multiple times
func TestManagerReloadConfigMultipleTimes(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	settingsManager := &SystemSettingsManager{}
	manager := &Manager{settingsManager: settingsManager}

	// First reload
	err := manager.ReloadConfig()
	require.NoError(t, err)

	// Change config
	os.Setenv("PORT", "8080")

	// Second reload
	err = manager.ReloadConfig()
	require.NoError(t, err)
	assert.Equal(t, 8080, manager.GetEffectiveServerConfig().Port)

	// Third reload
	os.Setenv("PORT", "9090")
	err = manager.ReloadConfig()
	require.NoError(t, err)
	assert.Equal(t, 9090, manager.GetEffectiveServerConfig().Port)
}

// TestManagerHostVariants tests different host configurations
func TestManagerHostVariants(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"localhost", "localhost"},
		{"0.0.0.0", "0.0.0.0"},
		{"127.0.0.1", "127.0.0.1"},
		{"custom domain", "api.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			os.Setenv("HOST", tt.host)

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)
			require.NoError(t, err)

			assert.Equal(t, tt.host, manager.GetEffectiveServerConfig().Host)
		})
	}
}

// TestManagerPortBoundaries tests port boundary values
func TestManagerPortBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		port        string
		expectError bool
	}{
		{"minimum valid port", "1", false},
		{"maximum valid port", "65535", false},
		{"below minimum", "0", true},
		{"above maximum", "65536", true},
		{"negative", "-1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			os.Setenv("PORT", tt.port)

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, manager)
			}
		})
	}
}

// BenchmarkReloadConfigMultiple benchmarks multiple config reloads
func BenchmarkReloadConfigMultiple(b *testing.B) {
	setupBenchEnv()
	defer cleanupBenchEnv()

	settingsManager := &SystemSettingsManager{}
	manager := &Manager{settingsManager: settingsManager}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.ReloadConfig()
	}
}

// BenchmarkDisplayServerConfig benchmarks config display
func BenchmarkDisplayServerConfig(b *testing.B) {
	setupBenchEnv()
	defer cleanupBenchEnv()

	settingsManager := &SystemSettingsManager{}
	manager, _ := NewManager(settingsManager)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.DisplayServerConfig()
	}
}

// TestManagerValidationAuthKeyStrength tests AUTH_KEY strength validation
func TestManagerValidationAuthKeyStrength(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	// Test with weak key (should still work but log warning)
	os.Setenv("AUTH_KEY", "weak")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)
	assert.NotNil(t, manager)
}

// TestManagerValidationGracefulShutdownTimeout tests graceful shutdown timeout validation
func TestManagerValidationGracefulShutdownTimeout(t *testing.T) {
	tests := []struct {
		name     string
		timeout  string
		expected int
	}{
		{"below minimum", "5", 10},
		{"at minimum", "10", 10},
		{"above minimum", "30", 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			defer cleanupTestEnv(t)

			os.Setenv("SERVER_GRACEFUL_SHUTDOWN_TIMEOUT", tt.timeout)

			settingsManager := &SystemSettingsManager{}
			manager, err := NewManager(settingsManager)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, manager.GetEffectiveServerConfig().GracefulShutdownTimeout)
		})
	}
}

// TestManagerCORSWildcardWarning tests CORS wildcard warning
func TestManagerCORSWildcardWarning(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "*")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	corsConfig := manager.GetCORSConfig()
	assert.True(t, corsConfig.Enabled)
	assert.Len(t, corsConfig.AllowedOrigins, 1)
	assert.Equal(t, "*", corsConfig.AllowedOrigins[0])
}

// TestManagerDisplayServerConfigWithoutEncryption tests display without encryption
func TestManagerDisplayServerConfigWithoutEncryption(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Unsetenv("ENCRYPTION_KEY")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})
}

// TestManagerDisplayServerConfigWithoutDatabase tests display without database
func TestManagerDisplayServerConfigWithoutDatabase(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("DATABASE_DSN", "")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})
}

// TestManagerDisplayServerConfigWithoutRedis tests display without Redis
func TestManagerDisplayServerConfigWithoutRedis(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Unsetenv("REDIS_DSN")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})
}

// TestManagerDisplayServerConfigWithFileLogging tests display with file logging
func TestManagerDisplayServerConfigWithFileLogging(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("LOG_ENABLE_FILE", "true")
	os.Setenv("LOG_FILE_PATH", "/var/log/app.log")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})

	logConfig := manager.GetLogConfig()
	assert.True(t, logConfig.EnableFile)
	assert.Equal(t, "/var/log/app.log", logConfig.FilePath)
}

// TestManagerDisplayServerConfigWithCORS tests display with CORS enabled
func TestManagerDisplayServerConfigWithCORS(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:8080")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})
}

// TestManagerDisplayServerConfigComplete tests display with all options
func TestManagerDisplayServerConfigComplete(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("HOST", "127.0.0.1")
	os.Setenv("PORT", "8080")
	os.Setenv("SERVER_READ_TIMEOUT", "45")
	os.Setenv("SERVER_WRITE_TIMEOUT", "450")
	os.Setenv("SERVER_IDLE_TIMEOUT", "90")
	os.Setenv("SERVER_GRACEFUL_SHUTDOWN_TIMEOUT", "15")
	os.Setenv("MAX_CONCURRENT_REQUESTS", "200")
	os.Setenv("ENCRYPTION_KEY", "test-encryption-key-32-bytes!!")
	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("LOG_FORMAT", "json")
	os.Setenv("LOG_ENABLE_FILE", "true")
	os.Setenv("LOG_FILE_PATH", "/var/log/app.log")
	os.Setenv("DATABASE_DSN", "./test.db")
	os.Setenv("REDIS_DSN", "redis://localhost:6379")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.DisplayServerConfig()
	})
}

// TestManagerDefaultValues tests default configuration values
func TestManagerDefaultValues(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	// Unset optional variables to test defaults
	os.Unsetenv("HOST")
	os.Unsetenv("PORT")
	os.Unsetenv("SERVER_READ_TIMEOUT")
	os.Unsetenv("SERVER_WRITE_TIMEOUT")
	os.Unsetenv("SERVER_IDLE_TIMEOUT")
	os.Unsetenv("MAX_CONCURRENT_REQUESTS")
	os.Unsetenv("LOG_LEVEL")
	os.Unsetenv("LOG_FORMAT")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	serverConfig := manager.GetEffectiveServerConfig()
	assert.Equal(t, "0.0.0.0", serverConfig.Host)
	assert.Equal(t, 3001, serverConfig.Port)
	assert.Equal(t, 60, serverConfig.ReadTimeout)
	assert.Equal(t, 600, serverConfig.WriteTimeout)
	assert.Equal(t, 120, serverConfig.IdleTimeout)

	perfConfig := manager.GetPerformanceConfig()
	assert.Equal(t, 100, perfConfig.MaxConcurrentRequests)

	logConfig := manager.GetLogConfig()
	assert.Equal(t, "info", logConfig.Level)
	assert.Equal(t, "text", logConfig.Format)
}

// TestManagerCORSDefaultMethods tests default CORS methods
func TestManagerCORSDefaultMethods(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")
	os.Unsetenv("ALLOWED_METHODS")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	corsConfig := manager.GetCORSConfig()
	assert.True(t, corsConfig.Enabled)
	assert.Contains(t, corsConfig.AllowedMethods, "GET")
	assert.Contains(t, corsConfig.AllowedMethods, "POST")
	assert.Contains(t, corsConfig.AllowedMethods, "PUT")
	assert.Contains(t, corsConfig.AllowedMethods, "DELETE")
	assert.Contains(t, corsConfig.AllowedMethods, "OPTIONS")
}

// TestManagerCORSDefaultHeaders tests default CORS headers
func TestManagerCORSDefaultHeaders(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Setenv("ENABLE_CORS", "true")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")
	os.Unsetenv("ALLOWED_HEADERS")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	corsConfig := manager.GetCORSConfig()
	assert.True(t, corsConfig.Enabled)
	assert.Len(t, corsConfig.AllowedHeaders, 1)
	assert.Equal(t, "*", corsConfig.AllowedHeaders[0])
}

// TestManagerDatabaseDefaultPath tests default database path
func TestManagerDatabaseDefaultPath(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Unsetenv("DATABASE_DSN")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	dbConfig := manager.GetDatabaseConfig()
	assert.Equal(t, "./data/gpt-load.db", dbConfig.DSN)
}

// TestManagerLogDefaultPath tests default log file path
func TestManagerLogDefaultPath(t *testing.T) {
	setupTestEnv(t)
	defer cleanupTestEnv(t)

	os.Unsetenv("LOG_FILE_PATH")

	settingsManager := &SystemSettingsManager{}
	manager, err := NewManager(settingsManager)
	require.NoError(t, err)

	logConfig := manager.GetLogConfig()
	assert.Equal(t, "./data/logs/app.log", logConfig.FilePath)
}

// TestManagerConstants tests configuration constants
func TestManagerConstants(t *testing.T) {
	assert.Equal(t, 1, DefaultConstants.MinPort)
	assert.Equal(t, 65535, DefaultConstants.MaxPort)
	assert.Equal(t, 1, DefaultConstants.MinTimeout)
	assert.Equal(t, 30, DefaultConstants.DefaultTimeout)
	assert.Equal(t, 50, DefaultConstants.DefaultMaxSockets)
	assert.Equal(t, 10, DefaultConstants.DefaultMaxFreeSockets)
}

// BenchmarkGetAllConfigs benchmarks getting all configuration values
func BenchmarkGetAllConfigs(b *testing.B) {
	setupBenchEnv()
	defer cleanupBenchEnv()

	settingsManager := &SystemSettingsManager{}
	manager, _ := NewManager(settingsManager)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.IsMaster()
		_ = manager.GetAuthConfig()
		_ = manager.GetCORSConfig()
		_ = manager.GetPerformanceConfig()
		_ = manager.GetLogConfig()
		_ = manager.GetRedisDSN()
		_ = manager.GetDatabaseConfig()
		_ = manager.GetEncryptionKey()
		_ = manager.IsDebugMode()
		_ = manager.GetEffectiveServerConfig()
	}
}
