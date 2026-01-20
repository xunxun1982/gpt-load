package container

import (
	"testing"

	"gpt-load/internal/config"
	"gpt-load/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestEnv sets up test environment variables
func setupTestEnv(t testing.TB) {
	t.Helper()
	t.Setenv("AUTH_KEY", "test-auth-key-minimum-16-chars")
	t.Setenv("DATABASE_DSN", ":memory:")
	t.Setenv("PORT", "3001")
}

// TestBuildContainer tests container creation
func TestBuildContainer(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)
}

// TestBuildContainer_ConfigManagerResolution tests config manager resolution
func TestBuildContainer_ConfigManagerResolution(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.NotNil(t, configManager)
}

// TestBuildContainer_App tests basic container functionality
// Note: Full app testing requires embed.FS which can only be created via //go:embed
// This test verifies the container can be built and basic services resolved
func TestBuildContainer_App(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)
	assert.NotNil(t, container)

	// Verify we can resolve basic services
	err = container.Invoke(func(cm types.ConfigManager) {
		assert.NotNil(t, cm)
	})
	require.NoError(t, err)
}

// TestBuildContainer_SystemSettingsManager tests system settings manager resolution
func TestBuildContainer_SystemSettingsManager(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	var settingsManager *config.SystemSettingsManager
	err = container.Invoke(func(sm *config.SystemSettingsManager) {
		settingsManager = sm
	})
	require.NoError(t, err)
	assert.NotNil(t, settingsManager)
}

// TestBuildContainer_ConfigManagerInvoke tests that config manager can be invoked
func TestBuildContainer_ConfigManagerInvoke(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	// Test that we can invoke a function that requires config manager
	err = container.Invoke(func(cm types.ConfigManager) {
		assert.NotNil(t, cm)
	})
	require.NoError(t, err)
}

// BenchmarkBuildContainer benchmarks container creation
func BenchmarkBuildContainer(b *testing.B) {
	setupTestEnv(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		container, err := BuildContainer()
		if err != nil {
			b.Fatal(err)
		}
		_ = container
	}
}

// BenchmarkContainerInvoke benchmarks dependency resolution
func BenchmarkContainerInvoke(b *testing.B) {
	setupTestEnv(b)

	container, err := BuildContainer()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err = container.Invoke(func(cm types.ConfigManager) {
			_ = cm
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestBuildContainer_MultipleInvocations tests multiple container invocations
func TestBuildContainer_MultipleInvocations(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	// First invocation
	var cm1 types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		cm1 = cm
	})
	require.NoError(t, err)
	assert.NotNil(t, cm1)

	// Second invocation should return same instance (singleton)
	var cm2 types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		cm2 = cm
	})
	require.NoError(t, err)
	assert.NotNil(t, cm2)
	assert.Same(t, cm1, cm2)
}

// TestBuildContainer_EmptyInvoke tests invoking with empty function
func TestBuildContainer_EmptyInvoke(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	// Test invoking with simple function
	err = container.Invoke(func() {
		// Simple function
	})
	assert.NoError(t, err)
}

// TestBuildContainer_WithEncryptionKey tests container with encryption key
func TestBuildContainer_WithEncryptionKey(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-32-bytes!!")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.NotNil(t, configManager)
	assert.Equal(t, "test-encryption-key-32-bytes!!", configManager.GetEncryptionKey())
}

// TestBuildContainer_WithDebugMode tests container with debug mode enabled
func TestBuildContainer_WithDebugMode(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("DEBUG_MODE", "true")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.True(t, configManager.IsDebugMode())
}

// TestBuildContainer_WithCORSEnabled tests container with CORS enabled
func TestBuildContainer_WithCORSEnabled(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("ENABLE_CORS", "true")
	t.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	corsConfig := configManager.GetCORSConfig()
	assert.True(t, corsConfig.Enabled)
}

// TestBuildContainer_WithRedis tests container with Redis DSN
func TestBuildContainer_WithRedis(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("REDIS_DSN", "redis://localhost:6379")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.Equal(t, "redis://localhost:6379", configManager.GetRedisDSN())
}

// TestBuildContainer_WithCustomPort tests container with custom port
func TestBuildContainer_WithCustomPort(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("PORT", "8080")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.Equal(t, 8080, configManager.GetEffectiveServerConfig().Port)
}

// TestBuildContainer_WithCustomHost tests container with custom host
func TestBuildContainer_WithCustomHost(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("HOST", "127.0.0.1")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", configManager.GetEffectiveServerConfig().Host)
}

// TestBuildContainer_WithMaxConcurrentRequests tests container with custom max concurrent requests
func TestBuildContainer_WithMaxConcurrentRequests(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("MAX_CONCURRENT_REQUESTS", "200")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.Equal(t, 200, configManager.GetPerformanceConfig().MaxConcurrentRequests)
}

// TestBuildContainer_WithLogLevel tests container with custom log level
func TestBuildContainer_WithLogLevel(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("LOG_LEVEL", "debug")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.Equal(t, "debug", configManager.GetLogConfig().Level)
}

// TestBuildContainer_WithSlaveMode tests container with slave mode
func TestBuildContainer_WithSlaveMode(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("IS_SLAVE", "true")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.False(t, configManager.IsMaster())
}

// TestBuildContainer_WithAllConfigs tests container with all configuration options
func TestBuildContainer_WithAllConfigs(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-32-bytes!!")
	t.Setenv("DEBUG_MODE", "true")
	t.Setenv("ENABLE_CORS", "true")
	t.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")
	t.Setenv("REDIS_DSN", "redis://localhost:6379")
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("MAX_CONCURRENT_REQUESTS", "200")
	t.Setenv("LOG_LEVEL", "debug")

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	var configManager types.ConfigManager
	err = container.Invoke(func(cm types.ConfigManager) {
		configManager = cm
	})
	require.NoError(t, err)
	assert.NotNil(t, configManager)
	assert.True(t, configManager.IsDebugMode())
	assert.Equal(t, "test-encryption-key-32-bytes!!", configManager.GetEncryptionKey())
}

// TestBuildContainer_MultipleServices tests resolving multiple services
func TestBuildContainer_MultipleServices(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	// Resolve multiple services
	err = container.Invoke(func(
		cm types.ConfigManager,
		sm *config.SystemSettingsManager,
	) {
		assert.NotNil(t, cm)
		assert.NotNil(t, sm)
	})
	require.NoError(t, err)
}

// TestBuildContainer_ServiceSingleton tests that services are singletons
func TestBuildContainer_ServiceSingleton(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	var cm1 types.ConfigManager
	var cm2 types.ConfigManager

	err = container.Invoke(func(cm types.ConfigManager) {
		cm1 = cm
	})
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		cm2 = cm
	})
	require.NoError(t, err)

	// Should be same instance
	assert.Same(t, cm1, cm2)
}

// BenchmarkBuildContainerWithConfigs benchmarks container creation with all configs
func BenchmarkBuildContainerWithConfigs(b *testing.B) {
	setupTestEnv(b)
	b.Setenv("ENCRYPTION_KEY", "test-encryption-key-32-bytes!!")
	b.Setenv("DEBUG_MODE", "true")
	b.Setenv("ENABLE_CORS", "true")
	b.Setenv("ALLOWED_ORIGINS", "http://localhost:3000")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		container, err := BuildContainer()
		if err != nil {
			b.Fatal(err)
		}
		_ = container
	}
}

// BenchmarkContainerInvokeMultiple benchmarks multiple service resolutions
func BenchmarkContainerInvokeMultiple(b *testing.B) {
	setupTestEnv(b)

	container, err := BuildContainer()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err = container.Invoke(func(
			cm types.ConfigManager,
			sm *config.SystemSettingsManager,
		) {
			_ = cm
			_ = sm
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestBuildContainer_CoreProviders tests that core providers are registered correctly
func TestBuildContainer_CoreProviders(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)
	require.NotNil(t, container)

	// Test that core configuration providers can be resolved
	tests := []struct {
		name string
		fn   any
	}{
		{"ConfigManager", func(cm types.ConfigManager) { assert.NotNil(t, cm) }},
		{"SystemSettingsManager", func(sm *config.SystemSettingsManager) { assert.NotNil(t, sm) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := container.Invoke(tt.fn)
			assert.NoError(t, err)
		})
	}
}

// TestBuildContainer_InfrastructureServices tests infrastructure service resolution
func TestBuildContainer_InfrastructureServices(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("IS_SLAVE", "false") // Explicitly set to master mode

	container, err := BuildContainer()
	require.NoError(t, err)

	// Verify infrastructure services can be resolved
	err = container.Invoke(func(
		cm types.ConfigManager,
		sm *config.SystemSettingsManager,
	) {
		assert.NotNil(t, cm)
		assert.NotNil(t, sm)
		assert.True(t, cm.IsMaster())
	})
	require.NoError(t, err)
}

// TestBuildContainer_ConfigManagerProperties tests config manager properties
func TestBuildContainer_ConfigManagerProperties(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("DEBUG_MODE", "true")
	t.Setenv("LOG_LEVEL", "debug")

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		assert.True(t, cm.IsDebugMode())
		assert.Equal(t, "debug", cm.GetLogConfig().Level)
		assert.NotEmpty(t, cm.GetAuthConfig().Key)
	})
	require.NoError(t, err)
}

// TestBuildContainer_PerformanceConfig tests performance configuration
func TestBuildContainer_PerformanceConfig(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("MAX_CONCURRENT_REQUESTS", "250")

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		perfConfig := cm.GetPerformanceConfig()
		assert.Equal(t, 250, perfConfig.MaxConcurrentRequests)
	})
	require.NoError(t, err)
}

// TestBuildContainer_ServerConfig tests server configuration
func TestBuildContainer_ServerConfig(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("HOST", "localhost")
	t.Setenv("PORT", "9090")

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		serverConfig := cm.GetEffectiveServerConfig()
		assert.Equal(t, "localhost", serverConfig.Host)
		assert.Equal(t, 9090, serverConfig.Port)
	})
	require.NoError(t, err)
}

// TestBuildContainer_CORSConfig tests CORS configuration
func TestBuildContainer_CORSConfig(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("ENABLE_CORS", "true")
	t.Setenv("ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:8080")
	t.Setenv("ALLOW_CREDENTIALS", "true")

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		corsConfig := cm.GetCORSConfig()
		assert.True(t, corsConfig.Enabled)
		assert.Len(t, corsConfig.AllowedOrigins, 2)
		assert.True(t, corsConfig.AllowCredentials)
	})
	require.NoError(t, err)
}

// TestBuildContainer_LogConfig tests log configuration
func TestBuildContainer_LogConfig(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("LOG_ENABLE_FILE", "true")

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		logConfig := cm.GetLogConfig()
		assert.Equal(t, "warn", logConfig.Level)
		assert.Equal(t, "json", logConfig.Format)
		assert.True(t, logConfig.EnableFile)
	})
	require.NoError(t, err)
}

// TestBuildContainer_DatabaseConfig tests database configuration
func TestBuildContainer_DatabaseConfig(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		dbConfig := cm.GetDatabaseConfig()
		assert.NotEmpty(t, dbConfig.DSN)
	})
	require.NoError(t, err)
}

// TestBuildContainer_RedisConfig tests Redis configuration
func TestBuildContainer_RedisConfig(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		redisDSN := cm.GetRedisDSN()
		assert.Equal(t, "redis://localhost:6379/0", redisDSN)
	})
	require.NoError(t, err)
}

// TestBuildContainer_EncryptionConfig tests encryption configuration
func TestBuildContainer_EncryptionConfig(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("ENCRYPTION_KEY", "my-secret-encryption-key-32b!!")

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		encKey := cm.GetEncryptionKey()
		assert.Equal(t, "my-secret-encryption-key-32b!!", encKey)
	})
	require.NoError(t, err)
}

// TestBuildContainer_MasterSlaveMode tests master/slave mode
func TestBuildContainer_MasterSlaveMode(t *testing.T) {
	tests := []struct {
		name     string
		isSlave  string
		expected bool
	}{
		{"master mode", "false", true},
		{"slave mode", "true", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTestEnv(t)
			t.Setenv("IS_SLAVE", tt.isSlave)

			container, err := BuildContainer()
			require.NoError(t, err)

			err = container.Invoke(func(cm types.ConfigManager) {
				assert.Equal(t, tt.expected, cm.IsMaster())
			})
			require.NoError(t, err)
		})
	}
}

// TestBuildContainer_ValidationSuccess tests successful validation
func TestBuildContainer_ValidationSuccess(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		validateErr := cm.Validate()
		assert.NoError(t, validateErr)
	})
	require.NoError(t, err)
}

// TestBuildContainer_ReloadConfig tests config reloading
func TestBuildContainer_ReloadConfig(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		reloadErr := cm.ReloadConfig()
		assert.NoError(t, reloadErr)
	})
	require.NoError(t, err)
}

// TestBuildContainer_DisplayConfig tests config display
func TestBuildContainer_DisplayConfig(t *testing.T) {
	setupTestEnv(t)

	container, err := BuildContainer()
	require.NoError(t, err)

	err = container.Invoke(func(cm types.ConfigManager) {
		assert.NotPanics(t, func() {
			cm.DisplayServerConfig()
		})
	})
	require.NoError(t, err)
}

// BenchmarkBuildContainerComplete benchmarks complete container build
func BenchmarkBuildContainerComplete(b *testing.B) {
	setupTestEnv(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		container, err := BuildContainer()
		if err != nil {
			b.Fatal(err)
		}
		// Resolve a service to ensure full initialization
		err = container.Invoke(func(cm types.ConfigManager) {
			_ = cm.IsMaster()
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkContainerResolveConfigManager benchmarks config manager resolution
func BenchmarkContainerResolveConfigManager(b *testing.B) {
	setupTestEnv(b)

	container, err := BuildContainer()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err = container.Invoke(func(cm types.ConfigManager) {
			_ = cm.IsMaster()
			_ = cm.GetAuthConfig()
			_ = cm.GetCORSConfig()
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
