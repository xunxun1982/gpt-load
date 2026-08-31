package store

import (
	"testing"
	"time"

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

// clearRedisPoolEnv unsets all four Redis pool environment variables so tests
// that assert DSN/default precedence are isolated from the ambient process
// environment. Uses t.Setenv which automatically restores the original value.
func clearRedisPoolEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envRedisPoolSize, "")
	t.Setenv(envRedisMinIdleConns, "")
	t.Setenv(envRedisConnMaxIdleTimeSeconds, "")
	t.Setenv(envRedisConnMaxLifetimeSeconds, "")
}

func TestBuildRedisOptions_ExplicitPoolLimits(t *testing.T) {
	clearRedisPoolEnv(t)

	opts, err := buildRedisOptions("redis://localhost:6379/0")
	require.NoError(t, err)

	// Pool limits must be explicit so long-running deployments do not rely on
	// go-redis defaults (ConnMaxLifetime=0 reuses connections forever).
	assert.GreaterOrEqual(t, opts.PoolSize, int(1))
	assert.GreaterOrEqual(t, opts.MinIdleConns, int(0))
	assert.Greater(t, opts.ConnMaxIdleTime, time.Duration(0))
	assert.Greater(t, opts.ConnMaxLifetime, time.Duration(0))
	// The hard cap must follow the effective pool size so the pool stays bounded.
	assert.Equal(t, opts.PoolSize, opts.MaxActiveConns)
}

func TestBuildRedisOptions_DSNParams(t *testing.T) {
	// DSN pool_size=5, min_idle_conns=2, no env overrides.
	// MaxActiveConns must follow the final PoolSize.
	clearRedisPoolEnv(t)
	opts, err := buildRedisOptions("redis://localhost:6379/0?pool_size=5&min_idle_conns=2")
	require.NoError(t, err)
	assert.Equal(t, 5, opts.PoolSize)
	assert.Equal(t, 2, opts.MinIdleConns)
	assert.Equal(t, 5, opts.MaxActiveConns)
	assert.Equal(t, 30*time.Minute, opts.ConnMaxIdleTime)
}

func TestBuildRedisOptions_DSNDisableDuration(t *testing.T) {
	// DSN conn_max_idle_time=0 must produce -1 (sentinel), not the default 30m.
	// conn_max_lifetime=0 must also produce -1.
	clearRedisPoolEnv(t)
	t.Run("conn_max_idle_time=0", func(t *testing.T) {
		opts, err := buildRedisOptions("redis://localhost:6379/0?conn_max_idle_time=0")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(-1), opts.ConnMaxIdleTime)
	})
	t.Run("conn_max_idle_time=0s", func(t *testing.T) {
		// go-redis ParseURL parses "0s" via time.ParseDuration to 0, unlike plain
		// "0" which short-circuits to -1 (options.go duration()). NewClient would
		// then rewrite that 0 to 30m, so the factory must normalize the unit-form
		// zero to -1 here to preserve the explicit-disable intent.
		opts, err := buildRedisOptions("redis://localhost:6379/0?conn_max_idle_time=0s")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(-1), opts.ConnMaxIdleTime)
	})
	t.Run("idle_timeout=0s", func(t *testing.T) {
		// Same unit-form zero via the idle_timeout alias, which go-redis falls
		// back to when conn_max_idle_time is absent (options.go); must also
		// normalize 0 to -1 instead of letting NewClient rewrite it to 30m.
		opts, err := buildRedisOptions("redis://localhost:6379/0?idle_timeout=0s")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(-1), opts.ConnMaxIdleTime)
	})
	t.Run("conn_max_lifetime=0", func(t *testing.T) {
		opts, err := buildRedisOptions("redis://localhost:6379/0?conn_max_lifetime=0")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(-1), opts.ConnMaxLifetime)
	})
}

func TestBuildRedisOptions_EnvOverridePoolSize(t *testing.T) {
	// env REDIS_POOL_SIZE=7 overrides DSN pool_size=5 and max_active_conns=9.
	// MaxActiveConns must be set to the final PoolSize (7).
	clearRedisPoolEnv(t)
	t.Setenv(envRedisPoolSize, "7")
	opts, err := buildRedisOptions("redis://localhost:6379/0?pool_size=5&max_active_conns=9")
	require.NoError(t, err)
	assert.Equal(t, 7, opts.PoolSize)
	assert.Equal(t, 7, opts.MaxActiveConns)
}

func TestBuildRedisOptions_EnvDisableIdleTime(t *testing.T) {
	// env REDIS_CONN_MAX_IDLE_TIME_SECONDS=0 must produce -1 (explicit disable sentinel).
	clearRedisPoolEnv(t)
	t.Setenv(envRedisConnMaxIdleTimeSeconds, "0")
	opts, err := buildRedisOptions("redis://localhost:6379/0")
	require.NoError(t, err)
	assert.Equal(t, time.Duration(-1), opts.ConnMaxIdleTime)
}

func TestBuildRedisOptions_DSNMaxActiveConns(t *testing.T) {
	// DSN max_active_conns=9 with no env: must be preserved.
	clearRedisPoolEnv(t)
	opts, err := buildRedisOptions("redis://localhost:6379/0?max_active_conns=9")
	require.NoError(t, err)
	assert.Equal(t, 9, opts.MaxActiveConns)
}

func TestBuildRedisOptions_InvalidEnvIgnored(t *testing.T) {
	// DSN pool_size=5, but env REDIS_POOL_SIZE="abc" is invalid.
	// The invalid env must be logged and ignored, falling back to DSN value.
	clearRedisPoolEnv(t)
	t.Setenv(envRedisPoolSize, "abc")
	opts, err := buildRedisOptions("redis://localhost:6379/0?pool_size=5")
	require.NoError(t, err)
	assert.Equal(t, 5, opts.PoolSize)
}

func TestBuildRedisOptions_InvalidDSN(t *testing.T) {
	t.Parallel()

	_, err := buildRedisOptions("not-a-url")
	require.Error(t, err)
}

func TestBuildRedisOptions_ConnectionCountsAboveMaxInt32(t *testing.T) {
	// go-redis v9 newConnPool calls SafeIntToInt32 on PoolSize, MinIdleConns,
	// MaxIdleConns, and MaxActiveConns; values above math.MaxInt32 cause a panic
	// instead of returning a descriptive error. Must reject them at the
	// validation layer.
	maxInt32 := int64(^uint32(0) >> 1) // math.MaxInt32

	clearRedisPoolEnv(t)

	t.Run("PoolSize env above MaxInt32", func(t *testing.T) {
		t.Setenv(envRedisPoolSize, "2147483648") // MaxInt32 + 1
		_, err := buildRedisOptions("redis://localhost:6379/0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PoolSize")
		assert.Contains(t, err.Error(), "safe limit")
	})
	t.Run("MinIdleConns env above MaxInt32", func(t *testing.T) {
		t.Setenv(envRedisMinIdleConns, "2147483648")
		_, err := buildRedisOptions("redis://localhost:6379/0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MinIdleConns")
	})
	t.Run("MaxActiveConns DSN above MaxInt32", func(t *testing.T) {
		_, err := buildRedisOptions("redis://localhost:6379/0?max_active_conns=2147483648")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MaxActiveConns")
	})
	t.Run("MaxIdleConns DSN above MaxInt32", func(t *testing.T) {
		_, err := buildRedisOptions("redis://localhost:6379/0?max_idle_conns=2147483648")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MaxIdleConns")
	})
	t.Run("MaxIdleConns DSN at MaxInt32 boundary", func(t *testing.T) {
		opts, err := buildRedisOptions("redis://localhost:6379/0?max_idle_conns=2147483647")
		require.NoError(t, err)
		assert.Equal(t, int(maxInt32), opts.MaxIdleConns)
	})
	t.Run("PoolSize env at MaxInt32 boundary", func(t *testing.T) {
		t.Setenv(envRedisPoolSize, "2147483647") // MaxInt32
		_, err := buildRedisOptions("redis://localhost:6379/0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PoolSize")
	})
	t.Run("PoolSize env at limit", func(t *testing.T) {
		t.Setenv(envRedisPoolSize, "10000")
		_, err := buildRedisOptions("redis://localhost:6379/0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PoolSize")
	})
	t.Run("PoolSize env one over limit", func(t *testing.T) {
		t.Setenv(envRedisPoolSize, "10001")
		_, err := buildRedisOptions("redis://localhost:6379/0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PoolSize")
	})
	t.Run("PoolSize env below limit", func(t *testing.T) {
		t.Setenv(envRedisPoolSize, "9999")
		opts, err := buildRedisOptions("redis://localhost:6379/0")
		require.NoError(t, err)
		assert.Equal(t, 9999, opts.PoolSize)
	})
}

func TestRedisDSNHasQueryParam_EmptyAndRepeated(t *testing.T) {
	// Empty values must be treated as unset (go-redis parses them to 0, and
	// they must not suppress the factory default).
	t.Run("empty value returns false", func(t *testing.T) {
		assert.False(t, redisDSNHasQueryParam("redis://localhost:6379/0?conn_max_lifetime=", "conn_max_lifetime"))
		assert.False(t, redisDSNHasQueryParam("redis://localhost:6379/0?min_idle_conns=", "min_idle_conns"))
	})
	t.Run("repeated param last value empty", func(t *testing.T) {
		assert.False(t, redisDSNHasQueryParam("redis://localhost:6379/0?conn_max_lifetime=1h&conn_max_lifetime=", "conn_max_lifetime"))
	})
	t.Run("repeated param last value non-empty", func(t *testing.T) {
		assert.True(t, redisDSNHasQueryParam("redis://localhost:6379/0?conn_max_lifetime=&conn_max_lifetime=2h", "conn_max_lifetime"))
	})
	t.Run("non-empty value returns true", func(t *testing.T) {
		assert.True(t, redisDSNHasQueryParam("redis://localhost:6379/0?min_idle_conns=3", "min_idle_conns"))
	})
	t.Run("zero is non-empty string", func(t *testing.T) {
		assert.True(t, redisDSNHasQueryParam("redis://localhost:6379/0?pool_size=0", "pool_size"))
	})
}

func TestBuildRedisOptions_EmptyDSNParamSuppressesDefault(t *testing.T) {
	// An empty DSN query parameter value must NOT suppress the factory default.
	// go-redis parses an empty value to 0 (duration/int), and the factory
	// default should apply.
	clearRedisPoolEnv(t)
	t.Run("empty conn_max_lifetime falls back to default", func(t *testing.T) {
		opts, err := buildRedisOptions("redis://localhost:6379/0?conn_max_lifetime=")
		require.NoError(t, err)
		assert.Equal(t, defaultRedisConnMaxLifetime, opts.ConnMaxLifetime)
	})
	t.Run("empty min_idle_conns falls back to default", func(t *testing.T) {
		opts, err := buildRedisOptions("redis://localhost:6379/0?min_idle_conns=")
		require.NoError(t, err)
		assert.Equal(t, defaultRedisMinIdleConns, opts.MinIdleConns)
	})
}

func TestBuildRedisOptions_EnvDurationOverflow(t *testing.T) {
	// Values at or below maxDurationSeconds (math.MaxInt64/1e9) must apply;
	// larger values would overflow time.Duration into a negative duration and
	// must be ignored so the DSN/default wins instead of a broken sentinel.
	clearRedisPoolEnv(t)
	t.Run("conn_max_idle_time at max boundary", func(t *testing.T) {
		t.Setenv(envRedisConnMaxIdleTimeSeconds, "9223372036")
		opts, err := buildRedisOptions("redis://localhost:6379/0")
		require.NoError(t, err)
		assert.Equal(t, 9223372036*time.Second, opts.ConnMaxIdleTime)
	})
	t.Run("conn_max_idle_time overflow ignored", func(t *testing.T) {
		t.Setenv(envRedisConnMaxIdleTimeSeconds, "9223372037")
		opts, err := buildRedisOptions("redis://localhost:6379/0")
		require.NoError(t, err)
		assert.Equal(t, defaultRedisConnMaxIdleTime, opts.ConnMaxIdleTime)
	})
	t.Run("conn_max_lifetime at max boundary", func(t *testing.T) {
		t.Setenv(envRedisConnMaxLifetimeSeconds, "9223372036")
		opts, err := buildRedisOptions("redis://localhost:6379/0")
		require.NoError(t, err)
		assert.Equal(t, 9223372036*time.Second, opts.ConnMaxLifetime)
	})
	t.Run("conn_max_lifetime overflow ignored", func(t *testing.T) {
		t.Setenv(envRedisConnMaxLifetimeSeconds, "9223372037")
		opts, err := buildRedisOptions("redis://localhost:6379/0")
		require.NoError(t, err)
		assert.Equal(t, defaultRedisConnMaxLifetime, opts.ConnMaxLifetime)
	})
}
