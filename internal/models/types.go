package models

import (
	"encoding/json"
	"fmt"
	"gpt-load/internal/types"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

// Key status constants
const (
	KeyStatusActive  = "active"
	KeyStatusInvalid = "invalid"
)

// SystemSetting corresponds to the system_settings table.
type SystemSetting struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SettingKey   string    `gorm:"type:varchar(255);not null;unique" json:"setting_key"`
	SettingValue string    `gorm:"type:text;not null" json:"setting_value"`
	Description  string    `gorm:"type:varchar(512)" json:"description"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GroupConfig stores group-specific configuration.
type GroupConfig struct {
	RequestTimeout               *int    `json:"request_timeout,omitempty"`
	IdleConnTimeout              *int    `json:"idle_conn_timeout,omitempty"`
	ConnectTimeout               *int    `json:"connect_timeout,omitempty"`
	MaxIdleConns                 *int    `json:"max_idle_conns,omitempty"`
	MaxIdleConnsPerHost          *int    `json:"max_idle_conns_per_host,omitempty"`
	ResponseHeaderTimeout        *int    `json:"response_header_timeout,omitempty"`
	ProxyURL                     *string `json:"proxy_url,omitempty"`
	MaxRetries                   *int    `json:"max_retries,omitempty"`
	SubMaxRetries                *int    `json:"sub_max_retries,omitempty"`
	BlacklistThreshold           *int    `json:"blacklist_threshold,omitempty"`
	KeyValidationIntervalMinutes *int    `json:"key_validation_interval_minutes,omitempty"`
	KeyValidationConcurrency     *int    `json:"key_validation_concurrency,omitempty"`
	KeyValidationTimeoutSeconds  *int    `json:"key_validation_timeout_seconds,omitempty"`
	EnableRequestBodyLogging     *bool   `json:"enable_request_body_logging,omitempty"`
	// ForceFunctionCall enables experimental function call middleware for this group.
	// This flag is stored in the group-level config JSON and is optional.
	ForceFunctionCall *bool `json:"force_function_call,omitempty"`
	// CCSupport enables Claude Code compatibility mode for this group.
	// When enabled, clients can connect via /claude endpoint and requests will be
	// converted from Claude format to OpenAI format before forwarding to upstream.
	CCSupport *bool `json:"cc_support,omitempty"`
	// ThinkingModel specifies the model to use when Claude Code enables extended thinking.
	// When a request has thinking.type="enabled", the model will be automatically
	// switched to this model. Leave empty to use the original model from request.
	ThinkingModel *string `json:"thinking_model,omitempty"`
	// CodexInstructions specifies custom instructions for Codex API requests.
	// Some providers (like 88code.ai) validate this field strictly.
	// Leave empty to use default instructions.
	CodexInstructions *string `json:"codex_instructions,omitempty"`
	// CodexInstructionsMode controls how Codex instructions are handled.
	// Values: "auto" (default, use codexDefaultInstructions), "official" (use official Codex CLI instructions),
	// "custom" (use CodexInstructions field value).
	CodexInstructionsMode *string `json:"codex_instructions_mode,omitempty"`
	// InterceptEventLog enables interception of Claude Code event logging endpoint.
	// Only applies to Anthropic channel groups. When enabled, /api/event_logging/batch
	// requests are intercepted and not forwarded to upstream.
	InterceptEventLog *bool `json:"intercept_event_log,omitempty"`
}

// HeaderRule defines a single rule for header manipulation.
type HeaderRule struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Action string `json:"action"` // "set" or "remove"
}

// PathRedirectRule defines a single URL path rewrite rule.
// Only applied to OpenAI channel groups at request time.
type PathRedirectRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// GroupSubGroup is the association table for aggregate groups and sub-groups.
type GroupSubGroup struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	GroupID    uint      `gorm:"not null;uniqueIndex:idx_group_sub" json:"group_id"`
	SubGroupID uint      `gorm:"not null;uniqueIndex:idx_group_sub" json:"sub_group_id"`
	Weight     int       `gorm:"default:0" json:"weight"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	// Lightweight association - only store necessary info for performance
	SubGroupName    string `gorm:"-" json:"sub_group_name,omitempty"`
	SubGroupEnabled bool   `gorm:"-" json:"sub_group_enabled,omitempty"`
}

// SubGroupInfo represents sub-group information for API responses.
type SubGroupInfo struct {
	Group         Group              `json:"group"`
	Weight        int                `json:"weight"`
	TotalKeys     int64              `json:"total_keys"`
	ActiveKeys    int64              `json:"active_keys"`
	InvalidKeys   int64              `json:"invalid_keys"`
	DynamicWeight *DynamicWeightInfo `json:"dynamic_weight,omitempty"` // Dynamic weight info (nil if not enabled)
}

// DynamicWeightInfo represents dynamic weight information for display.
// This is returned by API endpoints for frontend display.
type DynamicWeightInfo struct {
	BaseWeight      int     `json:"base_weight"`      // Original configured weight
	HealthScore     float64 `json:"health_score"`     // Health score (0.0 - 1.0)
	EffectiveWeight int     `json:"effective_weight"` // Calculated effective weight
	SuccessRate     float64 `json:"success_rate"`     // Success rate percentage (0-100)
	RequestCount    int64   `json:"request_count"`    // Total request count
	LastFailureAt   *string `json:"last_failure_at"`  // Last failure timestamp (ISO8601, nil if never failed)
	LastSuccessAt   *string `json:"last_success_at"`  // Last success timestamp (ISO8601, nil if never succeeded)
}

// ParentAggregateGroupInfo represents parent aggregate group information for API responses.
type ParentAggregateGroupInfo struct {
	GroupID     uint   `json:"group_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Weight      int    `json:"weight"`
}

// ChildGroupInfo represents child group information for API responses.
type ChildGroupInfo struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
}

// Group corresponds to the groups table.
type Group struct {
	ID                   uint                 `gorm:"primaryKey;autoIncrement" json:"id"`
	EffectiveConfig      types.SystemSettings `gorm:"-" json:"effective_config,omitempty"`
	Name                 string               `gorm:"type:varchar(255);not null;unique" json:"name"`
	Endpoint             string               `gorm:"-" json:"endpoint"`
	DisplayName          string               `gorm:"type:varchar(255)" json:"display_name"`
	ProxyKeys            string               `gorm:"type:text" json:"proxy_keys"`
	Description          string               `gorm:"type:varchar(512)" json:"description"`
	GroupType            string               `gorm:"type:varchar(50);default:'standard'" json:"group_type"` // 'standard' or 'aggregate'
	Enabled              bool                 `gorm:"default:true;not null" json:"enabled"`                  // Group enabled status
	Upstreams            datatypes.JSON       `gorm:"type:json;not null" json:"upstreams"`
	ValidationEndpoint   string               `gorm:"type:varchar(255)" json:"validation_endpoint"`
	ChannelType          string               `gorm:"type:varchar(50);not null" json:"channel_type"`
	Sort                 int                  `gorm:"default:0" json:"sort"`
	TestModel            string               `gorm:"type:varchar(255);not null" json:"test_model"`
	ParamOverrides       datatypes.JSONMap    `gorm:"type:json" json:"param_overrides"`
	Config               datatypes.JSONMap    `gorm:"type:json" json:"config"`
	HeaderRules          datatypes.JSON       `gorm:"type:json" json:"header_rules"`
	ModelMapping         string               `gorm:"type:text" json:"model_mapping"`             // Deprecated: use ModelRedirectRules instead
	ModelRedirectRules   datatypes.JSONMap    `gorm:"type:json" json:"model_redirect_rules"`      // Model redirect rules (one-to-one)
	ModelRedirectRulesV2 datatypes.JSON       `gorm:"type:json" json:"model_redirect_rules_v2"`   // Enhanced redirect rules (one-to-many)
	ModelRedirectStrict  bool                 `gorm:"default:false" json:"model_redirect_strict"` // Strict mode for model redirect
	CustomModelNames     datatypes.JSON       `gorm:"type:json" json:"custom_model_names"`        // Custom model names for aggregate groups (JSON array)
	Preconditions        datatypes.JSONMap    `gorm:"type:json" json:"preconditions"`             // Preconditions for aggregate groups (e.g., max_request_size_kb)
	PathRedirects        datatypes.JSON       `gorm:"type:json" json:"path_redirects"`            // JSON array of {from,to} rules (OpenAI only)
	ParentGroupID        *uint                `gorm:"index" json:"parent_group_id"`               // Parent group ID for child groups
	BoundSiteID          *uint                `gorm:"index" json:"bound_site_id"`                 // Bound managed site ID for standard groups
	APIKeys              []APIKey             `gorm:"foreignKey:GroupID" json:"api_keys"`
	SubGroups            []GroupSubGroup      `gorm:"-" json:"sub_groups,omitempty"`
	ChildGroups          []Group              `gorm:"-" json:"child_groups,omitempty"` // Child groups derived from this group
	LastValidatedAt      *time.Time           `json:"last_validated_at"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`

	// For cache
	ProxyKeysMap         map[string]struct{}             `gorm:"-" json:"-"`
	HeaderRuleList       []HeaderRule                    `gorm:"-" json:"-"`
	ModelMappingCache    map[string]string               `gorm:"-" json:"-"` // Deprecated: for backward compatibility
	ModelRedirectMap     map[string]string               `gorm:"-" json:"-"` // Parsed model redirect rules (one-to-one)
	ModelRedirectMapV2   map[string]*ModelRedirectRuleV2 `gorm:"-" json:"-"` // Parsed V2 rules (one-to-many)
	PathRedirectRuleList []PathRedirectRule              `gorm:"-" json:"-"` // Parsed path redirect rules (OpenAI)
}

// APIKey corresponds to the api_keys table.
type APIKey struct {
	ID           uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	KeyValue     string     `gorm:"type:text;not null" json:"key_value"`
	KeyHash      string     `gorm:"type:varchar(128);index" json:"key_hash"`
	GroupID      uint       `gorm:"not null;index:idx_api_keys_group_status" json:"group_id"`
	Status       string     `gorm:"type:varchar(50);not null;default:'active';index:idx_api_keys_status;index:idx_api_keys_group_status" json:"status"`
	Notes        string     `gorm:"type:varchar(255);default:''" json:"notes"`
	RequestCount int64      `gorm:"not null;default:0" json:"request_count"`
	FailureCount int64      `gorm:"not null;default:0" json:"failure_count"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// RequestType request type constants
const (
	RequestTypeRetry = "retry"
	RequestTypeFinal = "final"
)

// RequestLog corresponds to the request_logs table.
type RequestLog struct {
	ID              string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Timestamp       time.Time `gorm:"not null;index:idx_request_logs_group_timestamp;index:idx_request_logs_success_timestamp" json:"timestamp"`
	GroupID         uint      `gorm:"not null;index:idx_request_logs_group_timestamp" json:"group_id"`
	GroupName       string    `gorm:"type:varchar(255);index" json:"group_name"`
	ParentGroupID   uint      `gorm:"index" json:"parent_group_id"`
	ParentGroupName string    `gorm:"type:varchar(255);index" json:"parent_group_name"`
	KeyValue        string    `gorm:"type:text" json:"key_value"`
	KeyHash         string    `gorm:"type:varchar(128);index" json:"key_hash"`
	Model           string    `gorm:"type:varchar(255);index" json:"model"`
	MappedModel     string    `gorm:"type:varchar(255)" json:"mapped_model"` // Model name after mapping/redirect
	IsSuccess       bool      `gorm:"not null;index:idx_request_logs_success_timestamp" json:"is_success"`
	SourceIP        string    `gorm:"type:varchar(64)" json:"source_ip"`
	StatusCode      int       `gorm:"not null" json:"status_code"`
	RequestPath     string    `gorm:"type:varchar(500)" json:"request_path"`
	Duration        int64     `gorm:"not null" json:"duration_ms"`
	ErrorMessage    string    `gorm:"type:text" json:"error_message"`
	UserAgent       string    `gorm:"type:varchar(512)" json:"user_agent"`
	RequestType     string    `gorm:"type:varchar(20);not null;default:'final';index" json:"request_type"`
	UpstreamAddr    string    `gorm:"type:varchar(500)" json:"upstream_addr"`
	IsStream        bool      `gorm:"not null" json:"is_stream"`
	RequestBody     string    `gorm:"type:text" json:"request_body"`
	ResponseBody    string    `gorm:"type:text" json:"response_body"` // Response body for debugging (only stored when logging is enabled)
}

// StatCard represents a single statistics card data for the dashboard.
type StatCard struct {
	Value         float64 `json:"value"`
	SubValue      int64   `json:"sub_value,omitempty"`
	SubValueTip   string  `json:"sub_value_tip,omitempty"`
	Trend         float64 `json:"trend"`
	TrendIsGrowth bool    `json:"trend_is_growth"`
}

// SecurityWarning represents security warning information.
type SecurityWarning struct {
	Type       string `json:"type"`       // Warning type: auth_key, encryption_key, etc.
	Message    string `json:"message"`    // Warning message
	Severity   string `json:"severity"`   // Severity level: low, medium, high
	Suggestion string `json:"suggestion"` // Suggested solution
}

// DashboardStatsResponse represents the API response for dashboard basic statistics.
type DashboardStatsResponse struct {
	KeyCount         StatCard          `json:"key_count"`
	RPM              StatCard          `json:"rpm"`
	RequestCount     StatCard          `json:"request_count"`
	ErrorRate        StatCard          `json:"error_rate"`
	SecurityWarnings []SecurityWarning `json:"security_warnings"`
}

// ChartDataset represents a dataset for charts.
type ChartDataset struct {
	Label string  `json:"label"`
	Data  []int64 `json:"data"`
	Color string  `json:"color"`
}

// ChartData represents the API response for charts.
type ChartData struct {
	Labels   []string       `json:"labels"`
	Datasets []ChartDataset `json:"datasets"`
}

// GroupHourlyStat corresponds to the group_hourly_stats table, used to store hourly request statistics for each group.
type GroupHourlyStat struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Time         time.Time `gorm:"not null;index:idx_group_hourly_stats_time;uniqueIndex:idx_group_time" json:"time"` // Hourly timestamp
	GroupID      uint      `gorm:"not null;uniqueIndex:idx_group_time" json:"group_id"`
	SuccessCount int64     `gorm:"not null;default:0" json:"success_count"`
	FailureCount int64     `gorm:"not null;default:0" json:"failure_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GetMaxRequestSizeKB returns the max_request_size_kb precondition value for the group.
// Returns 0 if not configured (meaning no size limit).
// Negative values are normalized to 0 (no limit).
func (g *Group) GetMaxRequestSizeKB() int {
	if g.Preconditions == nil {
		return 0
	}

	val, ok := g.Preconditions["max_request_size_kb"]
	if !ok {
		return 0
	}

	var result int

	// Handle different numeric types from JSON unmarshaling
	switch v := val.(type) {
	case json.Number:
		intVal, err := v.Int64()
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"group_id": g.ID,
				"value":    v,
				"error":    err,
			}).Warn("Failed to parse json.Number for max_request_size_kb")
			return 0
		}
		result = int(intVal)
	case float64:
		result = int(v)
	case int:
		result = v
	case int64:
		result = int(v)
	default:
		logrus.WithFields(logrus.Fields{
			"group_id":   g.ID,
			"value_type": fmt.Sprintf("%T", val),
		}).Warn("Unexpected value type for max_request_size_kb")
		return 0
	}

	// Normalize negative values to 0 (no limit)
	if result < 0 {
		return 0
	}
	return result
}
