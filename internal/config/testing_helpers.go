package config

import (
	"gpt-load/internal/types"
)

// MockConfig implements types.ConfigManager for testing
type MockConfig struct {
	AuthKeyValue       string
	EncryptionKeyValue string
}

// GetServerConfig returns mock server configuration
func (m *MockConfig) GetServerConfig() types.ServerConfig {
	return types.ServerConfig{
		IsMaster:                true,
		Port:                    3001,
		Host:                    "0.0.0.0",
		ReadTimeout:             300,
		WriteTimeout:            600,
		IdleTimeout:             120,
		GracefulShutdownTimeout: 10,
	}
}

// GetAuthConfig returns mock auth configuration
func (m *MockConfig) GetAuthConfig() types.AuthConfig {
	return types.AuthConfig{
		Key: m.AuthKeyValue,
	}
}

// GetCORSConfig returns mock CORS configuration
func (m *MockConfig) GetCORSConfig() types.CORSConfig {
	return types.CORSConfig{
		Enabled:          false,
		AllowedOrigins:   []string{},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: false,
	}
}

// GetPerformanceConfig returns mock performance configuration
func (m *MockConfig) GetPerformanceConfig() types.PerformanceConfig {
	return types.PerformanceConfig{
		MaxConcurrentRequests: 100,
	}
}

// GetLogConfig returns mock log configuration
func (m *MockConfig) GetLogConfig() types.LogConfig {
	return types.LogConfig{
		Level:      "info",
		Format:     "text",
		EnableFile: false,
		FilePath:   "./data/logs/app.log",
	}
}

// GetDatabaseConfig returns mock database configuration
func (m *MockConfig) GetDatabaseConfig() types.DatabaseConfig {
	return types.DatabaseConfig{
		DSN: ":memory:",
	}
}

// GetRedisDSN returns mock Redis DSN
func (m *MockConfig) GetRedisDSN() string {
	return ""
}

// GetEncryptionKey returns mock encryption key
func (m *MockConfig) GetEncryptionKey() string {
	return m.EncryptionKeyValue
}

// IsDebugMode returns mock debug mode
func (m *MockConfig) IsDebugMode() bool {
	return false
}

// GetEffectiveServerConfig returns effective server configuration
func (m *MockConfig) GetEffectiveServerConfig() types.ServerConfig {
	return m.GetServerConfig()
}

// GetEffectivePerformanceConfig returns effective performance configuration
func (m *MockConfig) GetEffectivePerformanceConfig() types.PerformanceConfig {
	return m.GetPerformanceConfig()
}

// GetEffectiveLogConfig returns effective log configuration
func (m *MockConfig) GetEffectiveLogConfig() types.LogConfig {
	return m.GetLogConfig()
}

// ReloadConfig reloads configuration (no-op for mock)
func (m *MockConfig) ReloadConfig() error {
	return nil
}

// IsMaster returns whether this is a master instance
func (m *MockConfig) IsMaster() bool {
	return true
}

// Validate validates the configuration
func (m *MockConfig) Validate() error {
	return nil
}

// DisplayServerConfig displays server configuration (no-op for mock)
func (m *MockConfig) DisplayServerConfig() {
	// No-op for testing
}

// MockSystemSettingsManager implements SystemSettingsManagerInterface for testing
type MockSystemSettingsManager struct {
	Settings *types.SystemSettings
}

// GetSettings returns mock system settings
func (m *MockSystemSettingsManager) GetSettings() *types.SystemSettings {
	if m.Settings == nil {
		return &types.SystemSettings{}
	}
	return m.Settings
}

// UpdateSettings updates mock system settings
func (m *MockSystemSettingsManager) UpdateSettings(settings *types.SystemSettings) error {
	m.Settings = settings
	return nil
}

// ReloadSettings reloads mock system settings (no-op)
func (m *MockSystemSettingsManager) ReloadSettings() error {
	return nil
}
