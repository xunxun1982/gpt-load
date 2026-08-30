package store

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// Redis connection pool tuning. Without these, go-redis v9 defaults leave
// ConnMaxLifetime=0 (connections reused forever, risking half-dead connections
// after long runs) and a default PoolSize of 10×GOMAXPROCS even when most
// connections stay idle. Explicit limits keep the pool bounded and connections
// fresh; all values are overridable via environment variables.
const (
	defaultRedisMinIdleConns       = 1
	defaultRedisConnMaxIdleTime    = 30 * time.Minute
	defaultRedisConnMaxLifetime    = 30 * time.Minute
	envRedisPoolSize               = "REDIS_POOL_SIZE"
	envRedisMinIdleConns           = "REDIS_MIN_IDLE_CONNS"
	envRedisConnMaxIdleTimeSeconds = "REDIS_CONN_MAX_IDLE_TIME_SECONDS"
	envRedisConnMaxLifetimeSeconds = "REDIS_CONN_MAX_LIFETIME_SECONDS"
)

// buildRedisOptions parses the DSN and applies explicit pool limits.
// Extracted for testability: pool fields are asserted without a live server.
func buildRedisOptions(redisDSN string) (*redis.Options, error) {
	opts, err := redis.ParseURL(redisDSN)
	if err != nil {
		return nil, err
	}

	// PoolSize: 10×GOMAXPROCS is the go-redis default; allow override.
	opts.PoolSize = envIntOrDefault(envRedisPoolSize, 10*runtime.GOMAXPROCS(0))
	if opts.PoolSize < 1 {
		opts.PoolSize = 1
	}

	opts.MinIdleConns = envIntOrDefault(envRedisMinIdleConns, defaultRedisMinIdleConns)
	if opts.MinIdleConns < 0 {
		opts.MinIdleConns = 0
	}

	opts.ConnMaxIdleTime = time.Duration(envIntOrDefault(envRedisConnMaxIdleTimeSeconds, int(defaultRedisConnMaxIdleTime.Seconds()))) * time.Second
	if opts.ConnMaxIdleTime <= 0 {
		opts.ConnMaxIdleTime = defaultRedisConnMaxIdleTime
	}

	opts.ConnMaxLifetime = time.Duration(envIntOrDefault(envRedisConnMaxLifetimeSeconds, int(defaultRedisConnMaxLifetime.Seconds()))) * time.Second
	if opts.ConnMaxLifetime <= 0 {
		opts.ConnMaxLifetime = defaultRedisConnMaxLifetime
	}

	return opts, nil
}

// envIntOrDefault reads an int environment variable, falling back to def on
// unset or unparseable values.
func envIntOrDefault(key string, def int) int {
	raw := utils.GetEnvOrDefault(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		logrus.WithField("key", key).Warnf("Invalid integer env value %q, using default %d", raw, def)
		return def
	}
	return v
}

// NewStore creates a new store based on the application configuration.
func NewStore(cfg types.ConfigManager) (Store, error) {
	redisDSN := cfg.GetRedisDSN()
	if redisDSN != "" {
		opts, err := buildRedisOptions(redisDSN)
		if err != nil {
			return nil, fmt.Errorf("failed to parse redis DSN: %w", err)
		}

		client := redis.NewClient(opts)
		if err := client.Ping(context.Background()).Err(); err != nil {
			return nil, fmt.Errorf("failed to connect to redis: %w", err)
		}

		logrus.Debug("Successfully connected to Redis.")
		return NewRedisStore(client), nil
	}

	logrus.Info("Redis DSN not configured, falling back to in-memory store.")
	return NewMemoryStore(), nil
}
