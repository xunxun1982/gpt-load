package types

// ConfigManager defines the interface for configuration management
type ConfigManager interface {
	IsMaster() bool
	GetAuthConfig() AuthConfig
	GetCORSConfig() CORSConfig
	GetPerformanceConfig() PerformanceConfig
	GetLogConfig() LogConfig
	GetDatabaseConfig() DatabaseConfig
	GetEncryptionKey() string
	GetEffectiveServerConfig() ServerConfig
	GetRedisDSN() string
	IsDebugMode() bool
	Validate() error
	DisplayServerConfig()
	ReloadConfig() error
}

// SystemSettings defines all system configuration items.
type SystemSettings struct {
	// Basic parameters
	AppUrl                         string `json:"app_url" default:"http://localhost:3001" name:"config.app_url" category:"config.category.basic" desc:"config.app_url_desc" validate:"required"`
	ProxyKeys                      string `json:"proxy_keys" name:"config.proxy_keys" category:"config.category.basic" desc:"config.proxy_keys_desc" validate:"required"`
	RequestLogRetentionDays        int    `json:"request_log_retention_days" default:"7" name:"config.log_retention_days" category:"config.category.basic" desc:"config.log_retention_days_desc" validate:"required,min=0"`
	RequestLogWriteIntervalMinutes int    `json:"request_log_write_interval_minutes" default:"1" name:"config.log_write_interval" category:"config.category.basic" desc:"config.log_write_interval_desc" validate:"required,min=0"`
	EnableRequestBodyLogging       bool   `json:"enable_request_body_logging" default:"false" name:"config.enable_request_body_logging" category:"config.category.basic" desc:"config.enable_request_body_logging_desc"`

	// Request settings
	RequestTimeout          int    `json:"request_timeout" default:"1200" name:"config.request_timeout" category:"-" desc:"config.request_timeout_desc" validate:"required,min=1"`
	NonStreamRequestTimeout int    `json:"non_stream_request_timeout" default:"1200" name:"config.non_stream_request_timeout" category:"config.category.request" desc:"config.non_stream_request_timeout_desc" validate:"required,min=0"`
	StreamRequestTimeout    int    `json:"stream_request_timeout" default:"600" name:"config.stream_request_timeout" category:"config.category.request" desc:"config.stream_request_timeout_desc" validate:"required,min=0"`
	ConnectTimeout          int    `json:"connect_timeout" default:"30" name:"config.connect_timeout" category:"config.category.request" desc:"config.connect_timeout_desc" validate:"required,min=1"`
	IdleConnTimeout         int    `json:"idle_conn_timeout" default:"120" name:"config.idle_conn_timeout" category:"config.category.request" desc:"config.idle_conn_timeout_desc" validate:"required,min=1"`
	ResponseHeaderTimeout   int    `json:"response_header_timeout" default:"600" name:"config.response_header_timeout" category:"config.category.request" desc:"config.response_header_timeout_desc" validate:"required,min=1"`
	MaxIdleConns            int    `json:"max_idle_conns" default:"100" name:"config.max_idle_conns" category:"config.category.request" desc:"config.max_idle_conns_desc" validate:"required,min=1"`
	MaxIdleConnsPerHost     int    `json:"max_idle_conns_per_host" default:"50" name:"config.max_idle_conns_per_host" category:"config.category.request" desc:"config.max_idle_conns_per_host_desc" validate:"required,min=1,ltecsfield=MaxIdleConns"`
	ProxyURL                string `json:"proxy_url" name:"config.proxy_url" category:"config.category.request" desc:"config.proxy_url_desc"`

	// Proxy pool health-check settings are managed from More > Proxy Pool only.
	ProxyPoolTestTargetURL              string `json:"proxy_pool_test_target_url" default:"https://www.gstatic.com/generate_204" name:"config.proxy_pool_test_target_url" category:"-" desc:"config.proxy_pool_test_target_url_desc" validate:"required"`
	ProxyPoolTestTimeoutSeconds         int    `json:"proxy_pool_test_timeout_seconds" default:"10" name:"config.proxy_pool_test_timeout_seconds" category:"-" desc:"config.proxy_pool_test_timeout_seconds_desc" validate:"required,min=1"`
	ProxyPoolAutoTestIntervalMinutes    int    `json:"proxy_pool_auto_test_interval_minutes" default:"60" name:"config.proxy_pool_auto_test_interval_minutes" category:"-" desc:"config.proxy_pool_auto_test_interval_minutes_desc" validate:"required,min=1"`
	GatewayProxyTestTimeoutSeconds      int    `json:"gateway_proxy_test_timeout_seconds" default:"10" name:"config.gateway_proxy_test_timeout_seconds" category:"-" desc:"config.gateway_proxy_test_timeout_seconds_desc" validate:"required,min=1"`
	GatewayProxyAutoTestIntervalMinutes int    `json:"gateway_proxy_auto_test_interval_minutes" default:"60" name:"config.gateway_proxy_auto_test_interval_minutes" category:"-" desc:"config.gateway_proxy_auto_test_interval_minutes_desc" validate:"required,min=1"`

	// Key configuration
	MaxRetries                   int    `json:"max_retries" default:"3" name:"config.max_retries" category:"config.category.key" desc:"config.max_retries_desc" validate:"required,min=0"`
	RetryDelayMs                 int    `json:"retry_delay_ms" default:"0" name:"config.retry_delay_ms" category:"config.category.key" desc:"config.retry_delay_ms_desc" validate:"required,min=0"`
	RetryBackoffEnabled          bool   `json:"retry_backoff_enabled" default:"false" name:"config.retry_backoff_enabled" category:"config.category.key" desc:"config.retry_backoff_enabled_desc"`
	RetryBackoffMaxPercent       int    `json:"retry_backoff_max_percent" default:"500" name:"config.retry_backoff_max_percent" category:"config.category.key" desc:"config.retry_backoff_max_percent_desc" validate:"required,min=0"`
	BlacklistThreshold           int    `json:"blacklist_threshold" default:"3" name:"config.blacklist_threshold" category:"config.category.key" desc:"config.blacklist_threshold_desc" validate:"required,min=0"`
	FailoverStatusCodes          string `json:"failover_status_codes" default:"400-403,405-999" name:"config.failover_status_codes" category:"config.category.key" desc:"config.failover_status_codes_desc" validate:"required"`
	KeyValidationIntervalMinutes int    `json:"key_validation_interval_minutes" default:"60" name:"config.key_validation_interval" category:"config.category.key" desc:"config.key_validation_interval_desc" validate:"required,min=1"`
	KeyValidationConcurrency     int    `json:"key_validation_concurrency" default:"10" name:"config.key_validation_concurrency" category:"config.category.key" desc:"config.key_validation_concurrency_desc" validate:"required,min=1"`
	KeyValidationTimeoutSeconds  int    `json:"key_validation_timeout_seconds" default:"20" name:"config.key_validation_timeout" category:"config.category.key" desc:"config.key_validation_timeout_desc" validate:"required,min=1"`

	// For cache
	ProxyKeysMap map[string]struct{} `json:"-"`
}

// ServerConfig represents server configuration
type ServerConfig struct {
	Port                    int    `json:"port"`
	Host                    string `json:"host"`
	IsMaster                bool   `json:"is_master"`
	ReadTimeout             int    `json:"read_timeout"`
	WriteTimeout            int    `json:"write_timeout"`
	IdleTimeout             int    `json:"idle_timeout"`
	GracefulShutdownTimeout int    `json:"graceful_shutdown_timeout"`
}

// AuthConfig represents authentication configuration
type AuthConfig struct {
	Key string `json:"key"`
}

// CORSConfig represents CORS configuration
type CORSConfig struct {
	Enabled          bool     `json:"enabled"`
	AllowedOrigins   []string `json:"allowed_origins"`
	AllowedMethods   []string `json:"allowed_methods"`
	AllowedHeaders   []string `json:"allowed_headers"`
	AllowCredentials bool     `json:"allow_credentials"`
}

// PerformanceConfig represents performance configuration
type PerformanceConfig struct {
	MaxConcurrentRequests int `json:"max_concurrent_requests"`
}

// LogConfig represents logging configuration
type LogConfig struct {
	Level      string `json:"level"`
	Format     string `json:"format"`
	EnableFile bool   `json:"enable_file"`
	FilePath   string `json:"file_path"`
}

// DatabaseConfig represents database configuration
type DatabaseConfig struct {
	DSN string `json:"dsn"`
}

type RetryError struct {
	StatusCode         int    `json:"status_code"`
	ErrorMessage       string `json:"error_message"`
	ParsedErrorMessage string `json:"-"`
	KeyValue           string `json:"key_value"`
	Attempt            int    `json:"attempt"`
	UpstreamAddr       string `json:"-"`
}
