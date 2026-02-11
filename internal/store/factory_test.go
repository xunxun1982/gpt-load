package store

import (
	"testing"

	"gpt-load/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConfigManager implements types.ConfigManager for testing
type mockConfigManager struct {
	redisDSN string
}

func (m *mockConfigManager) IsMaster() bool {
	return true
}

func (m *mockConfigManager) GetAuthConfig() types.AuthConfig {
	return types.AuthConfig{}
}

func (m *mockConfigManager) GetCORSConfig() types.CORSConfig {
	return types.CORSConfig{}
}

func (m *mockConfigManager) GetPerformanceConfig() types.PerformanceConfig {
	return types.PerformanceConfig{}
}

func (m *mockConfigManager) GetLogConfig() types.LogConfig {
	return types.LogConfig{}
}

func (m *mockConfigManager) GetDatabaseConfig() types.DatabaseConfig {
	return types.DatabaseConfig{}
}

func (m *mockConfigManager) GetEncryptionKey() string {
	return ""
}

func (m *mockConfigManager) GetEffectiveServerConfig() types.ServerConfig {
	return types.ServerConfig{}
}

func (m *mockConfigManager) GetRedisDSN() string {
	return m.redisDSN
}

func (m *mockConfigManager) IsDebugMode() bool {
	return false
}

func (m *mockConfigManager) Validate() error {
	return nil
}

func (m *mockConfigManager) DisplayServerConfig() {
	// No-op for testing
}

func (m *mockConfigManager) ReloadConfig() error {
	return nil
}

func TestNewStore_MemoryStore(t *testing.T) {
	t.Parallel()
	cfg := &mockConfigManager{
		redisDSN: "",
	}

	store, err := NewStore(cfg)
	require.NoError(t, err)
	require.NotNil(t, store)
	defer store.Close()

	// Verify it's a memory store by checking type
	_, ok := store.(*MemoryStore)
	assert.True(t, ok, "Expected MemoryStore when Redis DSN is empty")
}

func TestNewStore_InvalidRedisDSN(t *testing.T) {
	t.Parallel()
	cfg := &mockConfigManager{
		redisDSN: "invalid://dsn",
	}

	store, err := NewStore(cfg)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "failed to parse redis DSN")
}

func TestNewStore_RedisConnectionFailed(t *testing.T) {
	t.Parallel()
	// Use a valid DSN format but with a non-existent server
	cfg := &mockConfigManager{
		redisDSN: "redis://localhost:9999",
	}

	store, err := NewStore(cfg)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "failed to connect to redis")
}
