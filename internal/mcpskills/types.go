package mcpskills

import "time"

// ServiceListParams defines pagination and filter parameters for service listing
type ServiceListParams struct {
	Page     int    // Page number (1-based)
	PageSize int    // Items per page (default 50, max 200)
	Search   string // Optional search term for name/description
	Category string // Optional filter by category
	Enabled  *bool  // Optional filter by enabled status
	Type     string // Optional filter by service type
}

// ServiceListResult contains paginated service list with metadata
type ServiceListResult struct {
	Services   []MCPServiceDTO `json:"services"`
	Total      int64           `json:"total"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	TotalPages int             `json:"total_pages"`
}

// MCPServiceDTO represents the data transfer object for MCP service
type MCPServiceDTO struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Sort        int    `json:"sort"`
	Enabled     bool   `json:"enabled"`
	Type        string `json:"type"`

	// For stdio/sse services
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"` // Working directory for stdio

	// For API bridge services
	APIEndpoint  string `json:"api_endpoint,omitempty"`
	APIKeyName   string `json:"api_key_name,omitempty"`
	HasAPIKey    bool   `json:"has_api_key"`
	APIKeyHeader string `json:"api_key_header,omitempty"`
	APIKeyPrefix string `json:"api_key_prefix,omitempty"`

	// Environment variables
	RequiredEnvVars []EnvVarDefinition `json:"required_env_vars,omitempty"`
	DefaultEnvs     map[string]string  `json:"default_envs,omitempty"`
	Headers         map[string]string  `json:"headers,omitempty"`

	// Tools
	Tools     []ToolDefinition `json:"tools,omitempty"`
	ToolCount int              `json:"tool_count"`

	// Rate limiting
	RPDLimit int `json:"rpd_limit"`

	// MCP endpoint exposure
	MCPEnabled     bool `json:"mcp_enabled"`
	HasAccessToken bool `json:"has_access_token"`

	// Status
	HealthStatus    string    `json:"health_status"`
	LastHealthCheck time.Time `json:"last_health_check,omitempty"`

	// Timestamps
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateServiceParams defines parameters for creating a new MCP service
type CreateServiceParams struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Sort        int    `json:"sort"`
	Enabled     bool   `json:"enabled"`
	Type        string `json:"type"`

	// For stdio/sse services
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"` // Working directory for stdio

	// For API bridge services
	APIEndpoint  string `json:"api_endpoint,omitempty"`
	APIKeyName   string `json:"api_key_name,omitempty"`
	APIKeyValue  string `json:"api_key_value,omitempty"`
	APIKeyHeader string `json:"api_key_header,omitempty"`
	APIKeyPrefix string `json:"api_key_prefix,omitempty"`

	// Environment variables
	RequiredEnvVars []EnvVarDefinition `json:"required_env_vars,omitempty"`
	DefaultEnvs     map[string]string  `json:"default_envs,omitempty"`
	Headers         map[string]string  `json:"headers,omitempty"`

	// Tools for API bridge
	Tools []ToolDefinition `json:"tools,omitempty"`

	// Rate limiting
	RPDLimit int `json:"rpd_limit"`

	// MCP endpoint exposure
	MCPEnabled  bool   `json:"mcp_enabled"`
	AccessToken string `json:"access_token,omitempty"`
}

// UpdateServiceParams defines parameters for updating an MCP service
type UpdateServiceParams struct {
	Name        *string `json:"name,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
	Category    *string `json:"category,omitempty"`
	Icon        *string `json:"icon,omitempty"`
	Sort        *int    `json:"sort,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	Type        *string `json:"type,omitempty"`

	// For stdio/sse services
	Command *string   `json:"command,omitempty"`
	Args    *[]string `json:"args,omitempty"`
	Cwd     *string   `json:"cwd,omitempty"` // Working directory for stdio

	// For API bridge services
	APIEndpoint  *string `json:"api_endpoint,omitempty"`
	APIKeyName   *string `json:"api_key_name,omitempty"`
	APIKeyValue  *string `json:"api_key_value,omitempty"`
	APIKeyHeader *string `json:"api_key_header,omitempty"`
	APIKeyPrefix *string `json:"api_key_prefix,omitempty"`

	// Environment variables
	RequiredEnvVars *[]EnvVarDefinition `json:"required_env_vars,omitempty"`
	DefaultEnvs     *map[string]string  `json:"default_envs,omitempty"`
	Headers         *map[string]string  `json:"headers,omitempty"`

	// Tools for API bridge
	Tools *[]ToolDefinition `json:"tools,omitempty"`

	// Rate limiting
	RPDLimit *int `json:"rpd_limit,omitempty"`

	// MCP endpoint exposure
	MCPEnabled  *bool   `json:"mcp_enabled,omitempty"`
	AccessToken *string `json:"access_token,omitempty"`
}

// GroupListParams defines pagination parameters for group listing
type GroupListParams struct {
	Page     int    // Page number (1-based)
	PageSize int    // Items per page (default 50, max 200)
	Search   string // Optional search term
	Enabled  *bool  // Optional filter by enabled status
}

// GroupListResult contains paginated group list with metadata
type GroupListResult struct {
	Groups     []MCPServiceGroupDTO `json:"groups"`
	Total      int64                `json:"total"`
	Page       int                  `json:"page"`
	PageSize   int                  `json:"page_size"`
	TotalPages int                  `json:"total_pages"`
}

// MCPServiceGroupDTO represents the data transfer object for MCP service group
type MCPServiceGroupDTO struct {
	ID           uint             `json:"id"`
	Name         string           `json:"name"`
	DisplayName  string           `json:"display_name"`
	Description  string           `json:"description"`
	ServiceIDs   []uint           `json:"service_ids"`
	ServiceCount int              `json:"service_count"`
	Services     []MCPServiceDTO  `json:"services,omitempty"` // Populated when needed
	Enabled      bool             `json:"enabled"`

	// MCP Aggregation settings
	AggregationEnabled  bool   `json:"aggregation_enabled"`
	AggregationEndpoint string `json:"aggregation_endpoint,omitempty"` // Generated endpoint URL
	HasAccessToken      bool   `json:"has_access_token"`

	// Skill export info
	SkillExportEndpoint string `json:"skill_export_endpoint,omitempty"`

	// Stats
	TotalToolCount int `json:"total_tool_count"`

	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

// CreateGroupParams defines parameters for creating a new MCP service group
type CreateGroupParams struct {
	Name               string `json:"name"`
	DisplayName        string `json:"display_name"`
	Description        string `json:"description"`
	ServiceIDs         []uint `json:"service_ids"`
	Enabled            bool   `json:"enabled"`
	AggregationEnabled bool   `json:"aggregation_enabled"`
	AccessToken        string `json:"access_token,omitempty"`
}

// UpdateGroupParams defines parameters for updating an MCP service group
type UpdateGroupParams struct {
	Name               *string `json:"name,omitempty"`
	DisplayName        *string `json:"display_name,omitempty"`
	Description        *string `json:"description,omitempty"`
	ServiceIDs         *[]uint `json:"service_ids,omitempty"`
	Enabled            *bool   `json:"enabled,omitempty"`
	AggregationEnabled *bool   `json:"aggregation_enabled,omitempty"`
	AccessToken        *string `json:"access_token,omitempty"`
}

// LogListParams defines pagination and filter parameters for log listing
type LogListParams struct {
	Page        int    // Page number (1-based)
	PageSize    int    // Items per page (default 50, max 200)
	ServiceID   *uint  // Optional filter by service ID
	ServiceName string // Optional filter by service name
	Phase       string // Optional filter by phase
	Level       string // Optional filter by level
}

// LogListResult contains paginated log list with metadata
type LogListResult struct {
	Logs       []MCPLogDTO `json:"logs"`
	Total      int64       `json:"total"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	TotalPages int         `json:"total_pages"`
}

// MCPLogDTO represents the data transfer object for MCP log
type MCPLogDTO struct {
	ID          uint      `json:"id"`
	ServiceID   uint      `json:"service_id"`
	ServiceName string    `json:"service_name"`
	Phase       string    `json:"phase"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"created_at"`
}

// SkillExportData represents the exported skill package data
type SkillExportData struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"display_name"`
	Description string           `json:"description"`
	Services    []MCPServiceDTO  `json:"services"`
	MCPConfig   map[string]any   `json:"mcp_config"`
	SkillMD     string           `json:"skill_md"`
	ToolsDocs   map[string]string `json:"tools_docs"` // service_name -> markdown
}

// APIBridgeTemplate represents a predefined template for API bridge services
type APIBridgeTemplate struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	DisplayName string           `json:"display_name"`
	Description string           `json:"description"`
	Category    string           `json:"category"`
	Icon        string           `json:"icon"`
	APIEndpoint string           `json:"api_endpoint"`
	APIKeyName  string           `json:"api_key_name"`
	APIKeyHeader string          `json:"api_key_header"`
	APIKeyPrefix string          `json:"api_key_prefix"`
	Tools       []ToolDefinition `json:"tools"`
}

// Predefined API bridge templates for common services
var APIBridgeTemplates = []APIBridgeTemplate{
	{
		ID:          "exa-search",
		Name:        "exa-search",
		DisplayName: "Exa Search",
		Description: "Exa AI search API - powerful web search for AI applications with neural search capabilities",
		Category:    string(CategorySearch),
		Icon:        "🔍",
		APIEndpoint: "https://api.exa.ai",
		APIKeyName:  "EXA_API_KEY",
		APIKeyHeader: "x-api-key",
		APIKeyPrefix: "",
		Tools: []ToolDefinition{
			{
				Name:        "search",
				Description: "Search the web using Exa AI's neural search engine. Returns relevant results with optional content extraction.",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query - can be a question or statement",
						},
						"num_results": map[string]interface{}{
							"type":        "integer",
							"description": "Number of results to return (default: 10, max: 100)",
							"default":     10,
						},
						"use_autoprompt": map[string]interface{}{
							"type":        "boolean",
							"description": "Whether to use autoprompt for better results",
							"default":     true,
						},
						"type": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"auto", "neural", "keyword"},
							"description": "Search type: auto (default), neural, or keyword",
							"default":     "auto",
						},
						"include_domains": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of domains to include in search results",
						},
						"exclude_domains": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of domains to exclude from search results",
						},
						"start_published_date": map[string]interface{}{
							"type":        "string",
							"description": "Filter results published after this date (ISO 8601 format)",
						},
						"end_published_date": map[string]interface{}{
							"type":        "string",
							"description": "Filter results published before this date (ISO 8601 format)",
						},
						"contents": map[string]interface{}{
							"type":        "object",
							"description": "Content extraction options",
							"properties": map[string]interface{}{
								"text": map[string]interface{}{
									"type":        "boolean",
									"description": "Include full text content",
								},
								"highlights": map[string]interface{}{
									"type":        "boolean",
									"description": "Include highlighted snippets",
								},
								"summary": map[string]interface{}{
									"type":        "boolean",
									"description": "Include AI-generated summary",
								},
							},
						},
					},
					"required": []string{"query"},
				},
			},
			{
				Name:        "find_similar",
				Description: "Find web pages similar to a given URL using Exa's neural similarity search",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"url": map[string]interface{}{
							"type":        "string",
							"description": "The URL to find similar content for",
						},
						"num_results": map[string]interface{}{
							"type":        "integer",
							"description": "Number of similar results to return (default: 10)",
							"default":     10,
						},
						"include_domains": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of domains to include in results",
						},
						"exclude_domains": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of domains to exclude from results",
						},
						"exclude_source_domain": map[string]interface{}{
							"type":        "boolean",
							"description": "Exclude results from the same domain as the source URL",
							"default":     true,
						},
					},
					"required": []string{"url"},
				},
			},
			{
				Name:        "get_contents",
				Description: "Get the full contents of web pages by their IDs or URLs",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"ids": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of document IDs or URLs to get contents for",
						},
						"text": map[string]interface{}{
							"type":        "boolean",
							"description": "Include full text content",
							"default":     true,
						},
						"highlights": map[string]interface{}{
							"type":        "boolean",
							"description": "Include highlighted snippets",
							"default":     false,
						},
						"summary": map[string]interface{}{
							"type":        "boolean",
							"description": "Include AI-generated summary",
							"default":     false,
						},
					},
					"required": []string{"ids"},
				},
			},
		},
	},
}

// AggregationMCPTool represents a tool in the MCP aggregation response
type AggregationMCPTool struct {
	ServiceName string `json:"service_name"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
}

// AggregationMCPSearchResult represents the result of search_tools
type AggregationMCPSearchResult struct {
	Tools []AggregationMCPTool `json:"tools"`
	Total int                  `json:"total"`
}

// AggregationMCPExecuteParams represents parameters for execute_tool
type AggregationMCPExecuteParams struct {
	ServiceName string                 `json:"service_name"`
	ToolName    string                 `json:"tool_name"`
	Arguments   map[string]interface{} `json:"arguments"`
}

// AggregationMCPExecuteResult represents the result of execute_tool
type AggregationMCPExecuteResult struct {
	Success bool        `json:"success"`
	Result  interface{} `json:"result,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// GroupEndpointInfo contains endpoint information for a group
type GroupEndpointInfo struct {
	GroupID             uint   `json:"group_id"`
	GroupName           string `json:"group_name"`
	AggregationEndpoint string `json:"aggregation_endpoint"`
	SkillExportURL      string `json:"skill_export_url"`
	MCPConfigJSON       string `json:"mcp_config_json"`
}

// ServiceEndpointInfo contains endpoint information for a single service
type ServiceEndpointInfo struct {
	ServiceID     uint   `json:"service_id"`
	ServiceName   string `json:"service_name"`
	ServiceType   string `json:"service_type"`
	MCPEndpoint   string `json:"mcp_endpoint"`
	APIEndpoint   string `json:"api_endpoint,omitempty"`
	MCPConfigJSON string `json:"mcp_config_json"`
}


// MCPSkillsExportData represents the export data structure for MCP Skills
type MCPSkillsExportData struct {
	Version    string                   `json:"version"`
	ExportedAt string                   `json:"exported_at"`
	Services   []MCPServiceExportInfo   `json:"services"`
	Groups     []MCPServiceGroupExportInfo `json:"groups"`
}

// MCPServiceExportInfo represents a single service in export data
// WARNING: When plainMode=true is used during export, APIKeyValue will contain
// decrypted secrets. Handle exported data with appropriate security measures.
type MCPServiceExportInfo struct {
	Name            string             `json:"name"`
	DisplayName     string             `json:"display_name"`
	Description     string             `json:"description"`
	Category        string             `json:"category"`
	Icon            string             `json:"icon"`
	Sort            int                `json:"sort"`
	Enabled         bool               `json:"enabled"`
	Type            string             `json:"type"`
	Command         string             `json:"command,omitempty"`
	Args            []string           `json:"args,omitempty"`
	Cwd             string             `json:"cwd,omitempty"` // Working directory for stdio
	APIEndpoint     string             `json:"api_endpoint,omitempty"`
	APIKeyName      string             `json:"api_key_name,omitempty"`
	APIKeyValue     string             `json:"api_key_value,omitempty"` // SECURITY: Encrypted or plain based on export mode
	APIKeyHeader    string             `json:"api_key_header,omitempty"`
	APIKeyPrefix    string             `json:"api_key_prefix,omitempty"`
	RequiredEnvVars []EnvVarDefinition `json:"required_env_vars,omitempty"`
	DefaultEnvs     map[string]string  `json:"default_envs,omitempty"`
	Headers         map[string]string  `json:"headers,omitempty"`
	Tools           []ToolDefinition   `json:"tools,omitempty"`
	RPDLimit        int                `json:"rpd_limit"`
}

// MCPServiceGroupExportInfo represents a single group in export data
type MCPServiceGroupExportInfo struct {
	Name               string   `json:"name"`
	DisplayName        string   `json:"display_name"`
	Description        string   `json:"description"`
	ServiceNames       []string `json:"service_names"` // Use names instead of IDs for portability
	Enabled            bool     `json:"enabled"`
	AggregationEnabled bool     `json:"aggregation_enabled"`
}

// MCPSkillsImportResult represents the result of an import operation
type MCPSkillsImportResult struct {
	ServicesImported int `json:"services_imported"`
	ServicesSkipped  int `json:"services_skipped"`
	GroupsImported   int `json:"groups_imported"`
	GroupsSkipped    int `json:"groups_skipped"`
}

// ServiceTestResult represents the result of testing an MCP service
type ServiceTestResult struct {
	ServiceID   uint                   `json:"service_id"`
	ServiceName string                 `json:"service_name"`
	ServiceType string                 `json:"service_type"`
	Success     bool                   `json:"success"`
	Message     string                 `json:"message,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Response    map[string]interface{} `json:"response,omitempty"`
	TestedAt    time.Time              `json:"tested_at"`
}

// ServiceToolsResult represents the result of fetching tools for a service
type ServiceToolsResult struct {
	ServiceID   uint             `json:"service_id"`
	ServiceName string           `json:"service_name"`
	ServerName  string           `json:"server_name,omitempty"`
	ServerVer   string           `json:"server_version,omitempty"`
	Description string           `json:"description,omitempty"`
	Tools       []ToolDefinition `json:"tools"`
	ToolCount   int              `json:"tool_count"`
	FromCache   bool             `json:"from_cache"`
	CachedAt    *time.Time       `json:"cached_at,omitempty"`
	ExpiresAt   *time.Time       `json:"expires_at,omitempty"`
}

// MCPServersConfig represents the standard MCP JSON configuration format
// This is the format used by Claude Desktop, Kiro, and other MCP clients
type MCPServersConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// MCPServerConfig represents a single MCP server configuration
type MCPServerConfig struct {
	// For local (stdio) servers
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"` // Working directory for stdio

	// For remote servers
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Common fields
	Type          string   `json:"type,omitempty"`          // stdio, sse, streamable_http
	Disabled      bool     `json:"disabled,omitempty"`      // Whether the server is disabled
	AutoApprove   []string `json:"autoApprove,omitempty"`   // Tool names to auto-approve (ignored)
	DisabledTools []string `json:"disabledTools,omitempty"` // Tool names to disable (ignored)
}

// MCPServersImportResult represents the result of importing MCP servers from JSON
type MCPServersImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}
