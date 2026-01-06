package mcpskills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// AggregationMCPHandler handles MCP aggregation requests
// MCP Aggregation exposes only two tools: search_tools and execute_tool
// This significantly reduces context usage from thousands of tokens to ~1000 tokens
type AggregationMCPHandler struct {
	db           *gorm.DB
	mcpService   *Service
	groupService *GroupService
	apiExecutor  *APIExecutor
}

// NewAggregationMCPHandler creates a new MCP aggregation handler
func NewAggregationMCPHandler(db *gorm.DB, mcpService *Service, groupService *GroupService, apiExecutor *APIExecutor) *AggregationMCPHandler {
	return &AggregationMCPHandler{
		db:           db,
		mcpService:   mcpService,
		groupService: groupService,
		apiExecutor:  apiExecutor,
	}
}

// MCPRequest represents a JSON-RPC 2.0 MCP request
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse represents a JSON-RPC 2.0 MCP response
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

// MCPError represents an MCP error
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// AggregationToolsListResult represents the tools/list response for MCP aggregation
type AggregationToolsListResult struct {
	Tools []AggregationToolDefinition `json:"tools"`
}

// AggregationToolDefinition represents a tool definition in MCP aggregation
type AggregationToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// GetAggregationTools returns the two aggregation tools: search_tools and execute_tool
// Exposes service names as enum for better LLM guidance and reduced hallucination
func (h *AggregationMCPHandler) GetAggregationTools(group *MCPServiceGroupDTO) AggregationToolsListResult {
	// Collect service names for enum
	serviceNames := make([]interface{}, 0, len(group.Services))
	for _, svc := range group.Services {
		if svc.Enabled {
			serviceNames = append(serviceNames, svc.Name)
		}
	}

	return AggregationToolsListResult{
		Tools: []AggregationToolDefinition{
			{
				Name:        "search_tools",
				Description: "STEP 1: Discover available tools in a service. You MUST call this first before execute_tool.",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"mcp_name": map[string]interface{}{
							"type":        "string",
							"enum":        serviceNames,
							"description": "MCP service name",
						},
					},
					"required": []string{"mcp_name"},
				},
			},
			{
				Name:        "execute_tool",
				Description: "STEP 2: Execute a tool found via search_tools. Pass arguments directly, do NOT nest.",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"mcp_name": map[string]interface{}{
							"type":        "string",
							"enum":        serviceNames,
							"description": "MCP service name",
						},
						"tool_name": map[string]interface{}{
							"type":        "string",
							"description": "Tool name from search_tools",
						},
						"arguments": map[string]interface{}{
							"type":        "object",
							"description": "Tool arguments. Example: {\"message\": \"hello\"} for a tool with message param",
						},
					},
					"required": []string{"mcp_name", "tool_name", "arguments"},
				},
			},
		},
	}
}

// searchToolsArgs represents parameters for search_tools
type searchToolsArgs struct {
	MCPName string
}

// executeToolArgs represents parameters for execute_tool
type executeToolArgs struct {
	MCPName   string
	ToolName  string
	Arguments map[string]interface{}
}

// yamlTool is a compact YAML-friendly tool representation
type yamlTool struct {
	Name   string                 `yaml:"name"`
	Desc   string                 `yaml:"desc,omitempty"`
	Params map[string]interface{} `yaml:"params,omitempty"`
}

// SearchTools searches for tools in a specific service
// Returns tools in YAML format for compact response and reduced token usage
func (h *AggregationMCPHandler) SearchTools(ctx context.Context, group *MCPServiceGroupDTO, args *searchToolsArgs) (map[string]interface{}, error) {
	// Find the service by iterating through group services
	var targetService *MCPServiceDTO
	for i := range group.Services {
		if group.Services[i].Name == args.MCPName {
			targetService = &group.Services[i]
			break
		}
	}

	if targetService == nil {
		return nil, fmt.Errorf("mcp_name not in group: %s", args.MCPName)
	}

	if !targetService.Enabled {
		return nil, fmt.Errorf("service '%s' is disabled", args.MCPName)
	}

	// Convert tools to YAML format for compact response
	yamlTools := make([]yamlTool, 0, len(targetService.Tools))
	for _, tool := range targetService.Tools {
		yt := yamlTool{
			Name: tool.Name,
			Desc: tool.Description,
		}
		// Extract just the properties from inputSchema for compactness
		if props, ok := tool.InputSchema["properties"].(map[string]interface{}); ok && len(props) > 0 {
			yt.Params = props
		}
		yamlTools = append(yamlTools, yt)
	}

	yamlBytes, err := yaml.Marshal(yamlTools)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize tools: %v", err)
	}

	toolsSummary := string(yamlBytes)

	return map[string]interface{}{
		"tools_yaml": toolsSummary,
		"tool_count": len(targetService.Tools),
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": toolsSummary,
			},
		},
	}, nil
}

// ExecuteTool executes a tool from a service
// For API bridge services, this performs actual API calls
// For stdio/sse services, returns tool info for client-side execution
func (h *AggregationMCPHandler) ExecuteTool(ctx context.Context, group *MCPServiceGroupDTO, args *executeToolArgs) (map[string]interface{}, error) {
	// Find the service by iterating through group services
	var targetService *MCPServiceDTO
	for i := range group.Services {
		if group.Services[i].Name == args.MCPName {
			targetService = &group.Services[i]
			break
		}
	}

	if targetService == nil {
		return nil, fmt.Errorf("mcp_name not in group: %s", args.MCPName)
	}

	if !targetService.Enabled {
		return nil, fmt.Errorf("service '%s' is disabled", args.MCPName)
	}

	// Find the tool by iterating through service tools
	var targetTool *ToolDefinition
	for i := range targetService.Tools {
		if targetService.Tools[i].Name == args.ToolName {
			targetTool = &targetService.Tools[i]
			break
		}
	}

	if targetTool == nil {
		return nil, fmt.Errorf("tool '%s' not found in service '%s'", args.ToolName, args.MCPName)
	}

	// For API bridge services, execute the actual API call
	if targetService.Type == string(ServiceTypeAPIBridge) && h.apiExecutor != nil {
		return h.apiExecutor.ExecuteAPIBridgeTool(ctx, targetService.ID, args.ToolName, args.Arguments)
	}

	// For other service types (stdio/sse/streamable_http), return execution info for client-side handling
	return map[string]interface{}{
		"service":   args.MCPName,
		"tool":      args.ToolName,
		"type":      targetService.Type,
		"arguments": args.Arguments,
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": fmt.Sprintf("Tool '%s' from service '%s' ready for execution with provided arguments.", args.ToolName, args.MCPName),
			},
		},
	}, nil
}

// HandleMCPRequest handles an MCP JSON-RPC request
func (h *AggregationMCPHandler) HandleMCPRequest(ctx context.Context, group *MCPServiceGroupDTO, req *MCPRequest) *MCPResponse {
	response := &MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
	}

	// Get service names for server info
	serviceNames := make([]string, 0, len(group.Services))
	for _, svc := range group.Services {
		if svc.Enabled {
			serviceNames = append(serviceNames, svc.Name)
		}
	}

	switch req.Method {
	case "initialize":
		response.Result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]interface{}{
				"name":     fmt.Sprintf("gpt-load-aggregation-%s", group.Name),
				"version":  "1.0.0",
				"services": serviceNames,
			},
			"instructions": group.Description,
		}

	case "tools/list":
		response.Result = h.GetAggregationTools(group)

	case "tools/call":
		var callParams struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			response.Error = &MCPError{Code: -32602, Message: "Invalid params: " + err.Error()}
			return response
		}

		switch callParams.Name {
		case "search_tools":
			args, err := h.parseSearchToolsArgs(callParams.Arguments)
			if err != nil {
				response.Error = &MCPError{Code: -32602, Message: err.Error()}
				return response
			}
			result, err := h.SearchTools(ctx, group, args)
			if err != nil {
				response.Error = &MCPError{Code: -32000, Message: err.Error()}
			} else {
				response.Result = result
			}

		case "execute_tool":
			args, err := h.parseExecuteToolArgs(callParams.Arguments)
			if err != nil {
				response.Error = &MCPError{Code: -32602, Message: err.Error()}
				return response
			}
			result, err := h.ExecuteTool(ctx, group, args)
			if err != nil {
				response.Error = &MCPError{Code: -32000, Message: err.Error()}
			} else {
				response.Result = result
			}

		default:
			response.Error = &MCPError{Code: -32601, Message: fmt.Sprintf("Unknown tool: %s", callParams.Name)}
		}

	default:
		response.Error = &MCPError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	return response
}

// parseSearchToolsArgs parses arguments for search_tools
func (h *AggregationMCPHandler) parseSearchToolsArgs(args map[string]interface{}) (*searchToolsArgs, error) {
	mcpName, _ := args["mcp_name"].(string)
	if strings.TrimSpace(mcpName) == "" {
		return nil, fmt.Errorf("mcp_name is required")
	}
	return &searchToolsArgs{
		MCPName: strings.TrimSpace(mcpName),
	}, nil
}

// parseExecuteToolArgs parses arguments for execute_tool
// Supports both "arguments" and "parameters" field names for client compatibility
func (h *AggregationMCPHandler) parseExecuteToolArgs(args map[string]interface{}) (*executeToolArgs, error) {
	mcpName, _ := args["mcp_name"].(string)
	toolName, _ := args["tool_name"].(string)
	if strings.TrimSpace(mcpName) == "" || strings.TrimSpace(toolName) == "" {
		return nil, fmt.Errorf("mcp_name and tool_name are required")
	}

	// Parse arguments - support both object and JSON string
	// Also supports "parameters" field name for client compatibility
	arguments := h.parseArgumentsValue(args)
	if arguments == nil {
		// Fallback: collect all other fields as arguments (for dumb LLMs)
		arguments = h.extractRemainingAsArguments(args)
	}
	if arguments == nil {
		arguments = map[string]interface{}{}
	}

	return &executeToolArgs{
		MCPName:   strings.TrimSpace(mcpName),
		ToolName:  strings.TrimSpace(toolName),
		Arguments: arguments,
	}, nil
}

// parseArgumentsValue parses arguments that could be either a map or a JSON string
// Supports field names: "arguments" or "parameters"
func (h *AggregationMCPHandler) parseArgumentsValue(args map[string]interface{}) map[string]interface{} {
	for _, fieldName := range []string{"arguments", "parameters"} {
		if v, ok := args[fieldName]; ok && v != nil {
			return h.parseAnyToMap(v)
		}
	}
	return nil
}

// parseAnyToMap converts a value to map[string]interface{}, supporting both object and JSON string
func (h *AggregationMCPHandler) parseAnyToMap(v interface{}) map[string]interface{} {
	if v == nil {
		return nil
	}
	// Try as map first
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	// Try as JSON string
	if s, ok := v.(string); ok && s != "" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			return m
		}
	}
	return nil
}

// extractRemainingAsArguments collects all fields except mcp_name/tool_name as arguments
// This handles cases where LLM puts tool params at top level instead of in arguments
func (h *AggregationMCPHandler) extractRemainingAsArguments(args map[string]interface{}) map[string]interface{} {
	reserved := map[string]bool{"mcp_name": true, "tool_name": true, "arguments": true, "parameters": true}
	result := make(map[string]interface{})
	for k, v := range args {
		if !reserved[k] {
			result[k] = v
		}
	}
	return result
}
