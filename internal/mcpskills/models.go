// Package mcpskills provides MCP (Model Context Protocol) service management,
// API bridge functionality, and Skills export for the application.
// It supports converting REST APIs to MCP services and provides a unified
// aggregation endpoint for reduced context usage in AI applications.
package mcpskills

import (
	"encoding/json"
	"time"
)

// ServiceCategory represents different categories of MCP services
// Categories are based on common MCP server classifications in the ecosystem
// Reference: glama.ai/mcp, mcpserve.com, one-mcp project
type ServiceCategory string

const (
	CategorySearch        ServiceCategory = "search"        // Web search, information retrieval
	CategoryFetch         ServiceCategory = "fetch"         // Web scraping, content extraction
	CategoryAI            ServiceCategory = "ai"            // AI/ML services, model inference
	CategoryUtil          ServiceCategory = "utility"       // General utilities, text processing
	CategoryStorage       ServiceCategory = "storage"       // Object storage, cloud storage (S3, R2, etc.)
	CategoryDatabase      ServiceCategory = "database"      // Database operations (SQL, NoSQL, vector DB)
	CategoryFilesystem    ServiceCategory = "filesystem"    // Local file system operations
	CategoryBrowser       ServiceCategory = "browser"       // Browser automation, web interaction
	CategoryCommunication ServiceCategory = "communication" // Email, messaging, notifications
	CategoryDevelopment   ServiceCategory = "development"   // Code tools, Git, CI/CD
	CategoryCloud         ServiceCategory = "cloud"         // Cloud platform services (AWS, GCP, Azure)
	CategoryMonitoring    ServiceCategory = "monitoring"    // Logging, metrics, observability
	CategoryProductivity  ServiceCategory = "productivity"  // Notion, calendar, task management
	CategoryCustom        ServiceCategory = "custom"        // User-defined custom services
)

// ServiceType represents the underlying type of an MCP service
type ServiceType string

const (
	ServiceTypeStdio          ServiceType = "stdio"
	ServiceTypeSSE            ServiceType = "sse"
	ServiceTypeStreamableHTTP ServiceType = "streamable_http"
	ServiceTypeAPIBridge      ServiceType = "api_bridge" // For converting REST APIs to MCP
)

// ServiceStatus represents the health status of an MCP service
type ServiceStatus string

const (
	StatusUnknown   ServiceStatus = "unknown"
	StatusHealthy   ServiceStatus = "healthy"
	StatusUnhealthy ServiceStatus = "unhealthy"
	StatusStarting  ServiceStatus = "starting"
	StatusStopped   ServiceStatus = "stopped"
)

// EnvVarDefinition defines a required environment variable for MCP service
type EnvVarDefinition struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	IsSecret     bool   `json:"is_secret"`
	Optional     bool   `json:"optional"`
	DefaultValue string `json:"default_value,omitempty"`
}

// ToolDefinition represents an MCP tool definition
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// MCPService represents an MCP service that can be enabled or configured
type MCPService struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	DisplayName string    `gorm:"type:varchar(255);not null" json:"display_name"`
	Description string    `gorm:"type:text" json:"description"`
	Category    string    `gorm:"type:varchar(50);not null;default:'custom'" json:"category"`
	Icon        string    `gorm:"type:varchar(255)" json:"icon"`
	Sort        int       `gorm:"default:0" json:"sort"`
	Enabled     bool      `gorm:"default:false" json:"enabled"`
	Type        string    `gorm:"type:varchar(50);not null;default:'api_bridge'" json:"type"`

	// For stdio/sse services
	Command  string `gorm:"type:varchar(512)" json:"command,omitempty"`
	ArgsJSON string `gorm:"type:text" json:"args_json,omitempty"`
	Cwd      string `gorm:"type:varchar(512)" json:"cwd,omitempty"` // Working directory for stdio

	// For API bridge services (converting REST APIs to MCP)
	APIEndpoint  string `gorm:"type:varchar(512)" json:"api_endpoint,omitempty"`
	APIKeyName   string `gorm:"type:varchar(255)" json:"api_key_name,omitempty"`
	APIKeyValue  string `gorm:"type:text" json:"-"` // Encrypted, never exposed
	APIKeyHeader string `gorm:"type:varchar(255)" json:"api_key_header,omitempty"`
	APIKeyPrefix string `gorm:"type:varchar(50)" json:"api_key_prefix,omitempty"`

	// Environment variables and configuration
	RequiredEnvVarsJSON string `gorm:"type:text" json:"required_env_vars_json,omitempty"`
	DefaultEnvsJSON     string `gorm:"type:text" json:"default_envs_json,omitempty"`
	HeadersJSON         string `gorm:"type:text" json:"headers_json,omitempty"`

	// Tools definition for API bridge services
	ToolsJSON string `gorm:"type:text" json:"tools_json,omitempty"`

	// Rate limiting
	RPDLimit int `gorm:"default:0" json:"rpd_limit"` // Requests per day limit (0 = unlimited)

	// MCP endpoint exposure settings
	MCPEnabled  bool   `gorm:"default:false" json:"mcp_enabled"`              // Enable MCP endpoint for this service
	AccessToken string `gorm:"type:varchar(255)" json:"-"`                    // Token for accessing MCP endpoint

	// Timestamps
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Runtime fields (not stored in DB)
	HealthStatus    ServiceStatus `gorm:"-" json:"health_status,omitempty"`
	LastHealthCheck time.Time     `gorm:"-" json:"last_health_check,omitempty"`
	ToolCount       int           `gorm:"-" json:"tool_count,omitempty"`
	HasAPIKey       bool          `gorm:"-" json:"has_api_key,omitempty"`
}

// TableName sets the table name for the MCPService model
func (s *MCPService) TableName() string {
	return "mcp_services"
}

// GetArgs returns the parsed arguments array
func (s *MCPService) GetArgs() ([]string, error) {
	if s.ArgsJSON == "" {
		return []string{}, nil
	}
	var args []string
	err := json.Unmarshal([]byte(s.ArgsJSON), &args)
	return args, err
}

// SetArgs sets the arguments as JSON
func (s *MCPService) SetArgs(args []string) error {
	if len(args) == 0 {
		s.ArgsJSON = ""
		return nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	s.ArgsJSON = string(data)
	return nil
}

// GetRequiredEnvVars returns the parsed environment variable definitions
func (s *MCPService) GetRequiredEnvVars() ([]EnvVarDefinition, error) {
	if s.RequiredEnvVarsJSON == "" {
		return []EnvVarDefinition{}, nil
	}
	var envVars []EnvVarDefinition
	err := json.Unmarshal([]byte(s.RequiredEnvVarsJSON), &envVars)
	return envVars, err
}

// SetRequiredEnvVars sets the environment variable definitions as JSON
func (s *MCPService) SetRequiredEnvVars(envVars []EnvVarDefinition) error {
	if len(envVars) == 0 {
		s.RequiredEnvVarsJSON = ""
		return nil
	}
	data, err := json.Marshal(envVars)
	if err != nil {
		return err
	}
	s.RequiredEnvVarsJSON = string(data)
	return nil
}

// GetDefaultEnvs returns the parsed default environment variables
func (s *MCPService) GetDefaultEnvs() (map[string]string, error) {
	if s.DefaultEnvsJSON == "" {
		return map[string]string{}, nil
	}
	var envs map[string]string
	err := json.Unmarshal([]byte(s.DefaultEnvsJSON), &envs)
	return envs, err
}

// SetDefaultEnvs sets the default environment variables as JSON
func (s *MCPService) SetDefaultEnvs(envs map[string]string) error {
	if len(envs) == 0 {
		s.DefaultEnvsJSON = ""
		return nil
	}
	data, err := json.Marshal(envs)
	if err != nil {
		return err
	}
	s.DefaultEnvsJSON = string(data)
	return nil
}

// GetHeaders returns the parsed custom headers
func (s *MCPService) GetHeaders() (map[string]string, error) {
	if s.HeadersJSON == "" {
		return map[string]string{}, nil
	}
	var headers map[string]string
	err := json.Unmarshal([]byte(s.HeadersJSON), &headers)
	return headers, err
}

// SetHeaders sets the custom headers as JSON
func (s *MCPService) SetHeaders(headers map[string]string) error {
	if len(headers) == 0 {
		s.HeadersJSON = ""
		return nil
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	s.HeadersJSON = string(data)
	return nil
}

// GetTools returns the parsed tool definitions
func (s *MCPService) GetTools() ([]ToolDefinition, error) {
	if s.ToolsJSON == "" {
		return []ToolDefinition{}, nil
	}
	var tools []ToolDefinition
	err := json.Unmarshal([]byte(s.ToolsJSON), &tools)
	return tools, err
}

// SetTools sets the tool definitions as JSON
func (s *MCPService) SetTools(tools []ToolDefinition) error {
	if len(tools) == 0 {
		s.ToolsJSON = ""
		return nil
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	s.ToolsJSON = string(data)
	return nil
}

// MCPServiceGroup represents a group of MCP services for Skills export and MCP Aggregation
type MCPServiceGroup struct {
	ID             uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name           string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	DisplayName    string    `gorm:"type:varchar(255);not null" json:"display_name"`
	Description    string    `gorm:"type:text" json:"description"`
	ServiceIDsJSON string    `gorm:"type:text" json:"service_ids_json"`
	Enabled        bool      `gorm:"default:true" json:"enabled"`

	// MCP Aggregation settings - expose unified endpoint with search_tools and execute_tool
	AggregationEnabled bool `gorm:"column:aggregation_enabled;default:false" json:"aggregation_enabled"`

	// Access control
	AccessToken string `gorm:"type:varchar(255)" json:"-"` // Token for accessing aggregation endpoint

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName sets the table name for the MCPServiceGroup model
func (g *MCPServiceGroup) TableName() string {
	return "mcp_service_groups"
}

// GetServiceIDs returns the parsed service IDs
// Returns empty slice if JSON is empty or invalid
func (g *MCPServiceGroup) GetServiceIDs() []uint {
	if g.ServiceIDsJSON == "" {
		return []uint{}
	}
	var ids []uint
	if err := json.Unmarshal([]byte(g.ServiceIDsJSON), &ids); err != nil {
		// Invalid JSON - return empty slice
		return []uint{}
	}
	return ids
}

// SetServiceIDs sets the service IDs as JSON
func (g *MCPServiceGroup) SetServiceIDs(ids []uint) {
	if len(ids) == 0 {
		g.ServiceIDsJSON = ""
		return
	}
	// json.Marshal for []uint never fails in practice
	data, err := json.Marshal(ids)
	if err != nil {
		g.ServiceIDsJSON = "[]"
		return
	}
	g.ServiceIDsJSON = string(data)
}

// MCPLog represents a log entry for MCP service operations
type MCPLog struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ServiceID   uint      `gorm:"not null;index:idx_mcp_logs_service_time" json:"service_id"`
	ServiceName string    `gorm:"type:varchar(255);index:idx_mcp_logs_name_time" json:"service_name"`
	Phase       string    `gorm:"type:varchar(50);index:idx_mcp_logs_phase_time" json:"phase"` // install, run, health_check
	Level       string    `gorm:"type:varchar(20)" json:"level"`                               // info, warn, error
	Message     string    `gorm:"type:text" json:"message"`
	CreatedAt   time.Time `gorm:"index:idx_mcp_logs_service_time,priority:2;index:idx_mcp_logs_name_time,priority:2;index:idx_mcp_logs_phase_time,priority:2" json:"created_at"`
}

// TableName sets the table name for the MCPLog model
func (l *MCPLog) TableName() string {
	return "mcp_logs"
}

// MCPToolCache stores discovered tools for MCP services with expiration
// This prevents frequent tool discovery requests and provides cache invalidation
// Uses Stale-While-Revalidate (SWR) strategy for optimal performance
type MCPToolCache struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	ServiceID   uint      `gorm:"not null;uniqueIndex" json:"service_id"`
	ToolsJSON   string    `gorm:"type:text" json:"tools_json"`
	ServerName  string    `gorm:"type:varchar(255)" json:"server_name"`
	ServerVer   string    `gorm:"type:varchar(100)" json:"server_version"`
	Description string    `gorm:"type:text" json:"description"`
	ToolCount   int       `gorm:"default:0" json:"tool_count"`
	// SoftExpiry: after this time, cache is stale but still usable, triggers background refresh
	SoftExpiry time.Time `gorm:"not null;index" json:"soft_expiry"`
	// HardExpiry: after this time, cache is invalid and must be refreshed synchronously
	HardExpiry time.Time `gorm:"not null;index" json:"hard_expiry"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName sets the table name for the MCPToolCache model
func (c *MCPToolCache) TableName() string {
	return "mcp_tool_cache"
}

// GetTools returns the parsed tool definitions from cache
func (c *MCPToolCache) GetTools() ([]ToolDefinition, error) {
	if c.ToolsJSON == "" {
		return []ToolDefinition{}, nil
	}
	var tools []ToolDefinition
	err := json.Unmarshal([]byte(c.ToolsJSON), &tools)
	return tools, err
}

// SetTools sets the tool definitions as JSON
func (c *MCPToolCache) SetTools(tools []ToolDefinition) error {
	if len(tools) == 0 {
		c.ToolsJSON = ""
		c.ToolCount = 0
		return nil
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	c.ToolsJSON = string(data)
	c.ToolCount = len(tools)
	return nil
}

// IsStale checks if the cache entry is stale (past soft expiry but before hard expiry)
// Stale cache can still be served while triggering background refresh
func (c *MCPToolCache) IsStale() bool {
	now := time.Now()
	return now.After(c.SoftExpiry) && now.Before(c.HardExpiry)
}

// IsExpired checks if the cache entry has hard expired (must refresh synchronously)
func (c *MCPToolCache) IsExpired() bool {
	return time.Now().After(c.HardExpiry)
}

// IsFresh checks if the cache entry is fresh (before soft expiry)
func (c *MCPToolCache) IsFresh() bool {
	return time.Now().Before(c.SoftExpiry)
}
