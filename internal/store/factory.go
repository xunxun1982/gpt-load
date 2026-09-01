package store

import (
	"context"
	"fmt"
	"net/url"
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
// fresh. Precedence for every field: environment variable > DSN query parameter
// > built-in default. ParseURL parses a DSN duration of 0 or negative to -1, the
// explicit-disable sentinel; env duration values <= 0 are normalized to the same
// sentinel, remembering that 0 would be rewritten to 30m by go-redis runtime
// normalization. MaxActiveConns is the hard pool cap: it follows the final
// PoolSize unless the DSN explicitly configures it (0 = no hard cap).
const (
	defaultRedisMinIdleConns       = 1
	defaultRedisConnMaxIdleTime    = 30 * time.Minute
	defaultRedisConnMaxLifetime    = 30 * time.Minute
	envRedisPoolSize               = "REDIS_POOL_SIZE"
	envRedisMinIdleConns           = "REDIS_MIN_IDLE_CONNS"
	envRedisConnMaxIdleTimeSeconds = "REDIS_CONN_MAX_IDLE_TIME_SECONDS"
	envRedisConnMaxLifetimeSeconds = "REDIS_CONN_MAX_LIFETIME_SECONDS"

	// maxRedisPoolSize is the application-defined safe upper limit for the Redis
	// connection pool. go-redis v9 pre-allocates an idleConns slice with capacity
	// equal to PoolSize during NewClient (internal/pool/pool.go:83); values at or
	// above this limit would needlessly risk memory exhaustion long before the
	// int32 overflow check (SafeIntToInt32) catches them. 10000 is well above any
	// reasonable deployment and still well below the server's default maxclients.
	maxRedisPoolSize = 10000
)

// maxDurationSeconds is math.MaxInt64 / 1e9: multiplying a larger seconds
// value overflows time.Duration into a negative duration, which go-redis
// would interpret as "timeout disabled" instead of the configured value.
const maxDurationSeconds = int64(^uint64(0)>>1) / int64(time.Second)

// maxInt32 is math.MaxInt32. go-redis v9 narrows connection-count options
// (PoolSize, MinIdleConns, MaxIdleConns, MaxActiveConns) to int32 when
// creating the pool (util.SafeIntToInt32) and panics on overflow, so we
// reject out-of-range values here with a descriptive error instead of
// crashing the process.
const maxInt32 = int64(^uint32(0) >> 1)

// buildRedisOptions parses the DSN and applies explicit pool limits.
// Extracted for testability: pool fields are asserted without a live server.
func buildRedisOptions(redisDSN string) (*redis.Options, error) {
	opts, err := redis.ParseURL(redisDSN)
	if err != nil {
		return nil, err
	}

	// PoolSize: env > DSN > default. ParseURL already resolves the DSN pool_size
	// into opts.PoolSize (0 when absent or explicit zero). A non-positive value
	// equals "unconfigured": go-redis treats pool_size=0 identically to a missing
	// parameter, so it falls through to the default below.
	poolV, poolOK := envRedisInt(envRedisPoolSize)
	if poolOK {
		opts.PoolSize = poolV
	} else if opts.PoolSize <= 0 {
		opts.PoolSize = 10 * runtime.GOMAXPROCS(0)
	}
	if opts.PoolSize < 1 {
		opts.PoolSize = 1
	}

	// MaxActiveConns: hard cap on the pool, guarding against unbounded
	// over-allocation. env REDIS_POOL_SIZE also drives this cap; otherwise an
	// explicit DSN value is preserved (0 = user opted for no hard cap); otherwise
	// the cap follows the final PoolSize.
	if poolOK {
		opts.MaxActiveConns = opts.PoolSize
	} else if !redisDSNHasQueryParam(redisDSN, "max_active_conns") {
		opts.MaxActiveConns = opts.PoolSize
	}

	// MinIdleConns: env > DSN > default. An explicit DSN 0 meaning "no pre-warmed
	// connections" is preserved; only absence falls back to the default.
	if v, ok := envRedisInt(envRedisMinIdleConns); ok {
		opts.MinIdleConns = v
	} else if !redisDSNHasQueryParam(redisDSN, "min_idle_conns") {
		opts.MinIdleConns = defaultRedisMinIdleConns
	}
	if opts.MinIdleConns < 0 {
		opts.MinIdleConns = 0
	}

	// ConnMaxIdleTime: env > DSN > default. env <= 0 and the DSN's -1 sentinel
	// both mean "explicitly disabled" and must survive: 0 would be normalized to
	// 30m by go-redis, so it cannot express "disabled". Values above
	// maxDurationSeconds would overflow time.Duration into a negative duration
	// (interpreted as "disabled" by go-redis), so the env is treated as unset to
	// keep it from silently corrupting the effective config: DSN or default win.
	v, ok := envRedisInt(envRedisConnMaxIdleTimeSeconds)
	if ok && int64(v) > maxDurationSeconds {
		logrus.WithFields(logrus.Fields{"key": envRedisConnMaxIdleTimeSeconds, "value": v}).
			Warnf("Ignoring value above %d seconds: it would overflow time.Duration", maxDurationSeconds)
		ok = false
	}
	if ok {
		if v <= 0 {
			opts.ConnMaxIdleTime = -1
		} else {
			opts.ConnMaxIdleTime = time.Duration(v) * time.Second
		}
	} else if !redisDSNHasQueryParam(redisDSN, "conn_max_idle_time", "idle_timeout") {
		opts.ConnMaxIdleTime = defaultRedisConnMaxIdleTime
	} else {
		// go-redis ParseURL parses a DSN duration in unit form ("0s") to 0,
		// whereas a plain "0" already becomes the -1 disable sentinel
		// (options.go duration(): Atoi <= 0 short-circuits before
		// ParseDuration). NewClient then rewrites ConnMaxIdleTime == 0 to 30m
		// (options.go), silently losing the explicit "disable idle timeout"
		// intent. This branch runs only when the DSN explicitly configured the
		// parameter (redisDSNHasQueryParam), so a parsed 0 can only be a
		// unit-form zero like "0s": normalize it to -1 to keep the intent.
		if opts.ConnMaxIdleTime == 0 {
			opts.ConnMaxIdleTime = -1
		}
	}

	// ConnMaxLifetime: same precedence and sentinel semantics as ConnMaxIdleTime.
	v, ok = envRedisInt(envRedisConnMaxLifetimeSeconds)
	if ok && int64(v) > maxDurationSeconds {
		logrus.WithFields(logrus.Fields{"key": envRedisConnMaxLifetimeSeconds, "value": v}).
			Warnf("Ignoring value above %d seconds: it would overflow time.Duration", maxDurationSeconds)
		ok = false
	}
	if ok {
		if v <= 0 {
			opts.ConnMaxLifetime = -1
		} else {
			opts.ConnMaxLifetime = time.Duration(v) * time.Second
		}
	} else if !redisDSNHasQueryParam(redisDSN, "conn_max_lifetime", "max_conn_age") {
		opts.ConnMaxLifetime = defaultRedisConnMaxLifetime
	}

	// Connection-count options are narrowed to int32 by go-redis's
	// util.SafeIntToInt32 when the pool is created; a value above math.MaxInt32
	// makes redis.NewClient panic, so reject it here with a descriptive error
	// instead of crashing the process.
	if int64(opts.PoolSize) >= maxRedisPoolSize {
		return nil, fmt.Errorf("redis: PoolSize %d exceeds application-defined safe limit %d", opts.PoolSize, maxRedisPoolSize)
	}
	if int64(opts.MaxActiveConns) > maxInt32 {
		return nil, fmt.Errorf("redis: MaxActiveConns %d exceeds max supported value %d", opts.MaxActiveConns, maxInt32)
	}
	if int64(opts.MinIdleConns) > maxInt32 {
		return nil, fmt.Errorf("redis: MinIdleConns %d exceeds max supported value %d", opts.MinIdleConns, maxInt32)
	}
	// MaxIdleConns is also converted to int32 by SafeIntToInt32 in newConnPool
	// (go-redis v9 options.go), so an oversized DSN value would panic in
	// redis.NewClient just like the other count options above.
	if int64(opts.MaxIdleConns) > maxInt32 {
		return nil, fmt.Errorf("redis: MaxIdleConns %d exceeds max supported value %d", opts.MaxIdleConns, maxInt32)
	}

	// Clamp MinIdleConns to a positive MaxActiveConns. go-redis v9's
	// checkMinIdleConns pre-warm loop (pool.go) only compares MinIdleConns
	// against PoolSize, never MaxActiveConns, while newConn still enforces the
	// hard cap and returns ErrPoolExhausted on exceeding it. An idle target
	// above the cap would start one failing pre-warm goroutine per extra idle
	// slot, wasting resources and churning goroutines, so clamp it to the cap.
	// This runs after the int32 overflow guard above: out-of-range MinIdleConns
	// is still rejected with an error instead of being silently clamped.
	if opts.MaxActiveConns > 0 && opts.MinIdleConns > opts.MaxActiveConns {
		opts.MinIdleConns = opts.MaxActiveConns
	}

	return opts, nil
}

// redisDSNHasQueryParam reports whether the DSN query string explicitly
// configures one of the given parameters, following go-redis ParseURL's
// canonical-first precedence (options.go). The parameters are consulted in
// order: the first parameter whose key is present in the query decides, even
// when its final value is empty (go-redis q.has() checks key presence only, and
// duration("") parses to 0). A present-but-empty canonical key therefore
// suppresses a non-empty legacy alias and yields false — the factory default
// applies, matching go-redis's behavior of ignoring the alias once the
// canonical key exists. Only a parameter whose key is entirely absent falls
// through to the next name. The last occurrence wins, matching go-redis's
// last-value semantics (url.Values.Get would return the first value, not the
// last, so it cannot be used here).
func redisDSNHasQueryParam(redisDSN string, params ...string) bool {
	u, err := url.Parse(redisDSN)
	if err != nil {
		return false
	}
	query := u.Query()
	for _, p := range params {
		vs, ok := query[p]
		if !ok || len(vs) == 0 {
			continue // key absent: try the next (alias) name
		}
		return vs[len(vs)-1] != ""
	}
	return false
}

// envRedisInt reads an integer environment variable. ok reports whether the
// variable held a valid integer; invalid values are logged and ignored (treated
// as unset, so they never override DSN or default values).
func envRedisInt(key string) (int, bool) {
	raw := utils.GetEnvOrDefault(key, "")
	if raw == "" {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		logrus.WithField("key", key).Warnf("Invalid integer env value %q, ignoring", raw)
		return 0, false
	}
	return v, true
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
