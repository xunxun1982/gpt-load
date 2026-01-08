package mcpskills

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// AggregationMCPHandler handles MCP aggregation requests
type AggregationMCPHandler struct {
	db           *gorm.DB
	mcpService   *Service
	groupService *GroupService
	apiExecutor  *APIExecutor

	// Service error tracking for smart routing
	serviceErrors   map[uint]*serviceErrorStats
	serviceErrorsMu sync.RWMutex
}

// serviceErrorStats tracks error statistics for a service
type serviceErrorStats struct {
	TotalCalls   int64
	ErrorCalls   int64
	LastError    time.Time
	LastErrorMsg string
	RecentCalls  []bool // sliding window: true = success, false = error
	RecentIndex  int
}

// NewAggregationMCPHandler creates a new MCP aggregation handler
func NewAggregationMCPHandler(db *gorm.DB, mcpService *Service, groupService *GroupService, apiExecutor *APIExecutor) *AggregationMCPHandler {
	return &AggregationMCPHandler{
		db:            db,
		mcpService:    mcpService,
		groupService:  groupService,
		apiExecutor:   apiExecutor,
		serviceErrors: make(map[uint]*serviceErrorStats),
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

// AggregationToolsListResult represents the tools/list response
type AggregationToolsListResult struct {
	Tools []AggregationToolDefinition `json:"tools"`
}

// AggregationToolDefinition represents a tool definition
type AggregationToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// Argument types
type searchToolsArgs struct {
	MCPName string
}

type executeToolArgs struct {
	MCPName   string
	ToolName  string
	Arguments map[string]interface{}
}

type smartExecuteArgs struct {
	ToolName   string
	Arguments  map[string]interface{}
	MaxRetries int
}

type listSimilarToolsArgs struct {
	ToolName string
}

type serviceWithTool struct {
	Service    *MCPServiceDTO
	Tool       *ToolDefinition
	Weight     int
	ErrorRate  float64
	TotalCalls int64
}

type yamlTool struct {
	Name   string                 `yaml:"name"`
	Desc   string                 `yaml:"desc,omitempty"`
	Params map[string]interface{} `yaml:"params,omitempty"`
}


// GetAggregationTools returns the aggregation tools
// Optimized for minimal token usage while providing full functionality
//
// Note: list_similar_tools is implemented but intentionally NOT exposed here.
// It's an internal/advanced tool for debugging service routing, not part of
// the standard aggregation workflow. AI review suggested adding it, but keeping
// the public API minimal reduces token usage and cognitive load for AI clients.
func (h *AggregationMCPHandler) GetAggregationTools(group *MCPServiceGroupDTO) AggregationToolsListResult {
	serviceNames := make([]interface{}, 0, len(group.Services))
	for _, svc := range group.Services {
		if svc.Enabled {
			serviceNames = append(serviceNames, svc.Name)
		}
	}

	return AggregationToolsListResult{
		Tools: []AggregationToolDefinition{
			{
				Name:        "list_all_tools",
				Description: "List all tools from all services (unified by aliases). Call first to discover available tools.",
				InputSchema: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
			{
				Name:        "search_tools",
				Description: "List tools in a specific service with full details.",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"mcp_name": map[string]interface{}{"type": "string", "enum": serviceNames},
					},
					"required": []string{"mcp_name"},
				},
			},
			{
				Name:        "smart_execute",
				Description: "Execute tool with auto service selection and failover. Supports unified alias names. RECOMMENDED.",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tool_name": map[string]interface{}{"type": "string", "description": "Tool name (supports unified alias names)"},
						"arguments": map[string]interface{}{"type": "object"},
					},
					"required": []string{"tool_name", "arguments"},
				},
			},
			{
				Name:        "execute_tool",
				Description: "Execute tool on specific service.",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"mcp_name":  map[string]interface{}{"type": "string", "enum": serviceNames},
						"tool_name": map[string]interface{}{"type": "string"},
						"arguments": map[string]interface{}{"type": "object"},
					},
					"required": []string{"mcp_name", "tool_name", "arguments"},
				},
			},
		},
	}
}

// SearchTools searches for tools in a specific service
func (h *AggregationMCPHandler) SearchTools(ctx context.Context, group *MCPServiceGroupDTO, args *searchToolsArgs) (map[string]interface{}, error) {
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

	yamlTools := make([]yamlTool, 0, len(targetService.Tools))
	for _, tool := range targetService.Tools {
		yt := yamlTool{Name: tool.Name, Desc: tool.Description}
		if props, ok := tool.InputSchema["properties"].(map[string]interface{}); ok && len(props) > 0 {
			yt.Params = props
		}
		yamlTools = append(yamlTools, yt)
	}

	// AI Review: yaml.Marshal rarely fails for simple structs, but log errors for debugging
	yamlBytes, err := yaml.Marshal(yamlTools)
	if err != nil {
		logrus.WithFields(logrus.Fields{"service": targetService.Name, "error": err}).Warn("Failed to marshal tools to YAML")
		yamlBytes = []byte("# marshal error")
	}
	return map[string]interface{}{
		"tools_yaml": string(yamlBytes),
		"tool_count": len(targetService.Tools),
		"content":    []map[string]interface{}{{"type": "text", "text": string(yamlBytes)}},
	}, nil
}

// unifiedTool represents a tool unified across services by alias
type unifiedTool struct {
	Name     string   `yaml:"name"`
	Desc     string   `yaml:"desc,omitempty"`
	Services []string `yaml:"services"` // services that provide this tool
}

// ListAllTools lists all tools from all services, unified by aliases
// This provides a compact overview for the model to understand available capabilities
// Strategy:
// 1. If alias has custom description configured, use it (highest priority)
// 2. Otherwise, keep the SHORTEST description to save tokens
func (h *AggregationMCPHandler) ListAllTools(ctx context.Context, group *MCPServiceGroupDTO) (map[string]interface{}, error) {
	// Build alias lookup: tool_name -> canonical_name
	aliasToCanonical := make(map[string]string)
	// Build custom description lookup: canonical_name -> description
	customDescriptions := make(map[string]string)

	if group.ToolAliasConfigs != nil {
		for canonical, config := range group.ToolAliasConfigs {
			aliasToCanonical[canonical] = canonical
			for _, alias := range config.Aliases {
				aliasToCanonical[alias] = canonical
			}
			if config.Description != "" {
				customDescriptions[canonical] = config.Description
			}
		}
	} else if group.ToolAliases != nil {
		// Fallback to old format
		for canonical, aliases := range group.ToolAliases {
			aliasToCanonical[canonical] = canonical
			for _, alias := range aliases {
				aliasToCanonical[alias] = canonical
			}
		}
	}

	// Collect tools, merging by canonical name
	// canonical_name -> {desc, services}
	toolMap := make(map[string]*unifiedTool)

	for _, svc := range group.Services {
		if !svc.Enabled {
			continue
		}
		for _, tool := range svc.Tools {
			// Resolve to canonical name
			canonical := tool.Name
			if resolved, ok := aliasToCanonical[tool.Name]; ok {
				canonical = resolved
			}

			if existing, ok := toolMap[canonical]; ok {
				// Add service to existing tool
				existing.Services = append(existing.Services, svc.Name)
				// Only update description if no custom description and current is shorter
				if customDescriptions[canonical] == "" {
					if len(tool.Description) > 0 && (len(existing.Desc) == 0 || len(tool.Description) < len(existing.Desc)) {
						existing.Desc = tool.Description
					}
				}
			} else {
				// Create new unified tool entry
				desc := tool.Description
				// Use custom description if configured
				if customDesc, ok := customDescriptions[canonical]; ok {
					desc = customDesc
				}
				toolMap[canonical] = &unifiedTool{
					Name:     canonical,
					Desc:     desc,
					Services: []string{svc.Name},
				}
			}
		}
	}

	// Convert to slice for YAML output
	tools := make([]unifiedTool, 0, len(toolMap))
	for _, t := range toolMap {
		tools = append(tools, *t)
	}

	// AI Review: yaml.Marshal rarely fails for simple structs, but log errors for debugging
	yamlBytes, err := yaml.Marshal(tools)
	if err != nil {
		logrus.WithFields(logrus.Fields{"group": group.Name, "error": err}).Warn("Failed to marshal unified tools to YAML")
		yamlBytes = []byte("# marshal error")
	}
	return map[string]interface{}{
		"tools_yaml":    string(yamlBytes),
		"tool_count":    len(tools),
		"service_count": len(group.Services),
		"content":       []map[string]interface{}{{"type": "text", "text": string(yamlBytes)}},
	}, nil
}

// ExecuteTool executes a tool from a service
func (h *AggregationMCPHandler) ExecuteTool(ctx context.Context, group *MCPServiceGroupDTO, args *executeToolArgs) (map[string]interface{}, error) {
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

	if targetService.Type == string(ServiceTypeAPIBridge) && h.apiExecutor != nil {
		result, err := h.apiExecutor.ExecuteAPIBridgeTool(ctx, targetService.ID, args.ToolName, args.Arguments)
		h.recordServiceCall(targetService.ID, err == nil, "")
		return result, err
	}

	return map[string]interface{}{
		"service": args.MCPName, "tool": args.ToolName, "type": targetService.Type, "arguments": args.Arguments,
		"content": []map[string]interface{}{{"type": "text", "text": fmt.Sprintf("Tool '%s' from '%s' ready.", args.ToolName, args.MCPName)}},
	}, nil
}


// SmartExecute executes a tool with automatic service selection and failover
func (h *AggregationMCPHandler) SmartExecute(ctx context.Context, group *MCPServiceGroupDTO, args *smartExecuteArgs) (map[string]interface{}, error) {
	services := h.findServicesWithTool(group, args.ToolName)
	if len(services) == 0 {
		return nil, fmt.Errorf("tool '%s' not found in any enabled service", args.ToolName)
	}

	maxRetries := args.MaxRetries
	if maxRetries <= 0 {
		maxRetries = len(services) - 1
	}
	if maxRetries > len(services)-1 {
		maxRetries = len(services) - 1
	}

	excludedServices := make(map[uint]bool)
	var lastError error
	var attempts []map[string]interface{}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		selected := h.selectServiceByWeight(services, excludedServices)
		if selected == nil {
			break
		}

		logrus.WithFields(logrus.Fields{
			"tool": args.ToolName, "service": selected.Service.Name, "attempt": attempt + 1,
			"weight": selected.Weight, "error_rate": fmt.Sprintf("%.2f%%", selected.ErrorRate*100),
		}).Debug("SmartExecute: attempting service")

		result, err := h.executeToolOnService(ctx, selected.Service, args.ToolName, args.Arguments)
		attemptInfo := map[string]interface{}{"service": selected.Service.Name, "attempt": attempt + 1}

		if err != nil {
			h.recordServiceCall(selected.Service.ID, false, err.Error())
			excludedServices[selected.Service.ID] = true
			lastError = err
			attemptInfo["error"] = err.Error()
			attempts = append(attempts, attemptInfo)
			logrus.WithFields(logrus.Fields{"tool": args.ToolName, "service": selected.Service.Name, "error": err.Error()}).Debug("SmartExecute: failed, trying next")
			continue
		}

		h.recordServiceCall(selected.Service.ID, true, "")
		attemptInfo["success"] = true
		attempts = append(attempts, attemptInfo)
		result["_smart_execute"] = map[string]interface{}{"selected_service": selected.Service.Name, "attempts": attempts, "total_attempts": len(attempts)}
		return result, nil
	}

	return nil, fmt.Errorf("all services failed for tool '%s' after %d attempts: %v", args.ToolName, len(attempts), lastError)
}

// ListSimilarTools lists all services that provide a specific tool
func (h *AggregationMCPHandler) ListSimilarTools(ctx context.Context, group *MCPServiceGroupDTO, args *listSimilarToolsArgs) (map[string]interface{}, error) {
	services := h.findServicesWithTool(group, args.ToolName)
	if len(services) == 0 {
		return map[string]interface{}{
			"tool_name": args.ToolName, "service_count": 0, "services": []interface{}{},
			"content": []map[string]interface{}{{"type": "text", "text": fmt.Sprintf("Tool '%s' not found.", args.ToolName)}},
		}, nil
	}

	serviceInfos := make([]map[string]interface{}, 0, len(services))
	for _, svc := range services {
		serviceInfos = append(serviceInfos, map[string]interface{}{
			"mcp_name": svc.Service.Name, "weight": svc.Weight,
			"error_rate": fmt.Sprintf("%.2f%%", svc.ErrorRate*100), "total_calls": svc.TotalCalls,
		})
	}

	// AI Review: yaml.Marshal rarely fails for simple structs, but log errors for debugging
	yamlBytes, err := yaml.Marshal(serviceInfos)
	if err != nil {
		logrus.WithFields(logrus.Fields{"tool": args.ToolName, "error": err}).Warn("Failed to marshal service infos to YAML")
		yamlBytes = []byte("# marshal error")
	}
	return map[string]interface{}{
		"tool_name": args.ToolName, "service_count": len(services), "services": serviceInfos,
		"content": []map[string]interface{}{{"type": "text", "text": fmt.Sprintf("Tool '%s' in %d services:\n%s", args.ToolName, len(services), string(yamlBytes))}},
	}, nil
}

// executeToolOnService executes a tool on a specific service
func (h *AggregationMCPHandler) executeToolOnService(ctx context.Context, service *MCPServiceDTO, toolName string, arguments map[string]interface{}) (map[string]interface{}, error) {
	if service.Type == string(ServiceTypeAPIBridge) && h.apiExecutor != nil {
		return h.apiExecutor.ExecuteAPIBridgeTool(ctx, service.ID, toolName, arguments)
	}
	return map[string]interface{}{
		"service": service.Name, "tool": toolName, "type": service.Type, "arguments": arguments,
		"content": []map[string]interface{}{{"type": "text", "text": fmt.Sprintf("Tool '%s' from '%s' ready.", toolName, service.Name)}},
	}, nil
}

// findServicesWithTool finds all enabled services that have a specific tool
// Supports tool aliases: if toolName matches a canonical name or any alias, all matching services are returned
func (h *AggregationMCPHandler) findServicesWithTool(group *MCPServiceGroupDTO, toolName string) []serviceWithTool {
	var result []serviceWithTool

	// Build alias lookup map for efficient matching
	aliasLookup := make(map[string]string) // alias -> canonical
	if group.ToolAliases != nil {
		for canonical, aliases := range group.ToolAliases {
			aliasLookup[canonical] = canonical
			for _, alias := range aliases {
				aliasLookup[alias] = canonical
			}
		}
	}

	// Resolve the requested tool name to canonical name (if aliased)
	canonicalName := toolName
	if resolved, ok := aliasLookup[toolName]; ok {
		canonicalName = resolved
	}

	// Collect all tool names that should match (canonical + all aliases)
	matchingNames := map[string]bool{toolName: true, canonicalName: true}
	if group.ToolAliases != nil {
		if aliases, ok := group.ToolAliases[canonicalName]; ok {
			for _, alias := range aliases {
				matchingNames[alias] = true
			}
		}
	}

	for i := range group.Services {
		svc := &group.Services[i]
		if !svc.Enabled {
			continue
		}
		for j := range svc.Tools {
			if matchingNames[svc.Tools[j].Name] {
				weight := 100
				if group.ServiceWeights != nil {
					if w, ok := group.ServiceWeights[svc.ID]; ok {
						weight = w
					}
				}
				errorRate, totalCalls := h.getServiceErrorRate(svc.ID)
				result = append(result, serviceWithTool{Service: svc, Tool: &svc.Tools[j], Weight: weight, ErrorRate: errorRate, TotalCalls: totalCalls})
				break
			}
		}
	}
	return result
}

// selectServiceByWeight selects a service using weighted random selection adjusted by error rate
func (h *AggregationMCPHandler) selectServiceByWeight(services []serviceWithTool, excludeIDs map[uint]bool) *serviceWithTool {
	if len(services) == 0 {
		return nil
	}

	weights := make([]float64, len(services))
	totalWeight := 0.0
	for i, svc := range services {
		if excludeIDs[svc.Service.ID] {
			continue
		}
		effectiveWeight := float64(svc.Weight) * (1 - svc.ErrorRate*0.5)
		if effectiveWeight < 1 {
			effectiveWeight = 1
		}
		weights[i] = effectiveWeight
		totalWeight += effectiveWeight
	}

	if totalWeight == 0 {
		return nil
	}

	r := rand.Float64() * totalWeight
	cumulative := 0.0
	for i, w := range weights {
		cumulative += w
		if r <= cumulative {
			return &services[i]
		}
	}
	return nil
}


// recordServiceCall records a service call result for error tracking
func (h *AggregationMCPHandler) recordServiceCall(serviceID uint, success bool, errorMsg string) {
	h.serviceErrorsMu.Lock()
	defer h.serviceErrorsMu.Unlock()

	stats, ok := h.serviceErrors[serviceID]
	if !ok {
		stats = &serviceErrorStats{RecentCalls: make([]bool, 0, 100)}
		h.serviceErrors[serviceID] = stats
	}

	stats.TotalCalls++
	if !success {
		stats.ErrorCalls++
		stats.LastError = time.Now()
		stats.LastErrorMsg = errorMsg
	}

	if len(stats.RecentCalls) < 100 {
		stats.RecentCalls = append(stats.RecentCalls, success)
	} else {
		stats.RecentCalls[stats.RecentIndex] = success
		stats.RecentIndex = (stats.RecentIndex + 1) % 100
	}
}

// getServiceErrorRate returns the recent error rate for a service
func (h *AggregationMCPHandler) getServiceErrorRate(serviceID uint) (float64, int64) {
	h.serviceErrorsMu.RLock()
	defer h.serviceErrorsMu.RUnlock()

	stats, ok := h.serviceErrors[serviceID]
	if !ok || stats.TotalCalls == 0 {
		return 0, 0
	}

	if len(stats.RecentCalls) == 0 {
		return float64(stats.ErrorCalls) / float64(stats.TotalCalls), stats.TotalCalls
	}

	errorCount := 0
	for _, success := range stats.RecentCalls {
		if !success {
			errorCount++
		}
	}
	return float64(errorCount) / float64(len(stats.RecentCalls)), stats.TotalCalls
}

// HandleMCPRequest handles an MCP JSON-RPC request
func (h *AggregationMCPHandler) HandleMCPRequest(ctx context.Context, group *MCPServiceGroupDTO, req *MCPRequest) *MCPResponse {
	response := &MCPResponse{JSONRPC: "2.0", ID: req.ID}

	serviceNames := make([]string, 0, len(group.Services))
	for _, svc := range group.Services {
		if svc.Enabled {
			serviceNames = append(serviceNames, svc.Name)
		}
	}

	switch req.Method {
	case "initialize":
		// Build comprehensive instructions for the model
		instructions := h.buildAggregationInstructions(group, serviceNames)
		response.Result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{"listChanged": false}},
			"serverInfo":      map[string]interface{}{"name": fmt.Sprintf("gpt-load-aggregation-%s", group.Name), "version": "1.0.0", "services": serviceNames},
			"instructions":    instructions,
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
		case "list_all_tools":
			result, err := h.ListAllTools(ctx, group)
			if err != nil {
				response.Error = &MCPError{Code: -32000, Message: err.Error()}
			} else {
				response.Result = result
			}

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

		case "smart_execute":
			args, err := h.parseSmartExecuteArgs(callParams.Arguments)
			if err != nil {
				response.Error = &MCPError{Code: -32602, Message: err.Error()}
				return response
			}
			result, err := h.SmartExecute(ctx, group, args)
			if err != nil {
				response.Error = &MCPError{Code: -32000, Message: err.Error()}
			} else {
				response.Result = result
			}

		case "list_similar_tools":
			args, err := h.parseListSimilarToolsArgs(callParams.Arguments)
			if err != nil {
				response.Error = &MCPError{Code: -32602, Message: err.Error()}
				return response
			}
			result, err := h.ListSimilarTools(ctx, group, args)
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
	return &searchToolsArgs{MCPName: strings.TrimSpace(mcpName)}, nil
}

// parseExecuteToolArgs parses arguments for execute_tool
func (h *AggregationMCPHandler) parseExecuteToolArgs(args map[string]interface{}) (*executeToolArgs, error) {
	mcpName, _ := args["mcp_name"].(string)
	toolName, _ := args["tool_name"].(string)
	if strings.TrimSpace(mcpName) == "" || strings.TrimSpace(toolName) == "" {
		return nil, fmt.Errorf("mcp_name and tool_name are required")
	}

	arguments := h.parseArgumentsValue(args)
	if arguments == nil {
		arguments = h.extractRemainingAsArguments(args)
	}
	if arguments == nil {
		arguments = map[string]interface{}{}
	}

	return &executeToolArgs{MCPName: strings.TrimSpace(mcpName), ToolName: strings.TrimSpace(toolName), Arguments: arguments}, nil
}

// parseSmartExecuteArgs parses arguments for smart_execute
func (h *AggregationMCPHandler) parseSmartExecuteArgs(args map[string]interface{}) (*smartExecuteArgs, error) {
	toolName, _ := args["tool_name"].(string)
	if strings.TrimSpace(toolName) == "" {
		return nil, fmt.Errorf("tool_name is required")
	}

	maxRetries := 3
	if v, ok := args["max_retries"]; ok {
		switch val := v.(type) {
		case float64:
			maxRetries = int(val)
		case int:
			maxRetries = val
		}
	}

	arguments := h.parseArgumentsValue(args)
	if arguments == nil {
		arguments = h.extractRemainingAsArguments(args)
	}
	if arguments == nil {
		arguments = map[string]interface{}{}
	}

	return &smartExecuteArgs{ToolName: strings.TrimSpace(toolName), Arguments: arguments, MaxRetries: maxRetries}, nil
}

// parseListSimilarToolsArgs parses arguments for list_similar_tools
func (h *AggregationMCPHandler) parseListSimilarToolsArgs(args map[string]interface{}) (*listSimilarToolsArgs, error) {
	toolName, _ := args["tool_name"].(string)
	if strings.TrimSpace(toolName) == "" {
		return nil, fmt.Errorf("tool_name is required")
	}
	return &listSimilarToolsArgs{ToolName: strings.TrimSpace(toolName)}, nil
}

// parseArgumentsValue parses arguments from "arguments" or "parameters" field
func (h *AggregationMCPHandler) parseArgumentsValue(args map[string]interface{}) map[string]interface{} {
	for _, fieldName := range []string{"arguments", "parameters"} {
		if v, ok := args[fieldName]; ok && v != nil {
			if m, ok := v.(map[string]interface{}); ok {
				return m
			}
			if s, ok := v.(string); ok && s != "" {
				var m map[string]interface{}
				if json.Unmarshal([]byte(s), &m) == nil {
					return m
				}
			}
		}
	}
	return nil
}

// extractRemainingAsArguments collects all fields except reserved ones as arguments
func (h *AggregationMCPHandler) extractRemainingAsArguments(args map[string]interface{}) map[string]interface{} {
	reserved := map[string]bool{"mcp_name": true, "tool_name": true, "arguments": true, "parameters": true, "max_retries": true}
	result := make(map[string]interface{})
	for k, v := range args {
		if !reserved[k] {
			result[k] = v
		}
	}
	return result
}

// buildAggregationInstructions builds minimal instructions for the model
// Keep it short to save tokens - detailed tool info is available via list_all_tools
func (h *AggregationMCPHandler) buildAggregationInstructions(group *MCPServiceGroupDTO, serviceNames []string) string {
	var sb strings.Builder

	// Add group description if available (user-defined, keep it)
	if group.Description != "" {
		sb.WriteString(group.Description)
		sb.WriteString("\n\n")
	}

	// Minimal workflow guide
	sb.WriteString("MCP Aggregation: ")
	sb.WriteString(strings.Join(serviceNames, ", "))
	sb.WriteString("\n")
	sb.WriteString("Use list_all_tools first, then smart_execute.\n")

	// Only show aliases if configured (important for smart_execute)
	if len(group.ToolAliases) > 0 {
		sb.WriteString("Aliases: ")
		aliasPairs := make([]string, 0, len(group.ToolAliases))
		for canonical, aliases := range group.ToolAliases {
			aliasPairs = append(aliasPairs, fmt.Sprintf("%s=%s", canonical, strings.Join(aliases, "/")))
		}
		sb.WriteString(strings.Join(aliasPairs, "; "))
	}

	return sb.String()
}
