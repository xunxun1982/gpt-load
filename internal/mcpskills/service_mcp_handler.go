package mcpskills

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// ServiceMCPHandler handles MCP requests for a single service
// This exposes the service's tools via standard MCP protocol (initialize, tools/list, tools/call)
type ServiceMCPHandler struct {
	db          *gorm.DB
	mcpService  *Service
	apiExecutor *APIExecutor
}

// NewServiceMCPHandler creates a new service MCP handler
func NewServiceMCPHandler(db *gorm.DB, mcpService *Service, apiExecutor *APIExecutor) *ServiceMCPHandler {
	return &ServiceMCPHandler{
		db:          db,
		mcpService:  mcpService,
		apiExecutor: apiExecutor,
	}
}

// GetServiceTools returns the tools list for a service in MCP format
func (h *ServiceMCPHandler) GetServiceTools(svc *MCPServiceDTO) map[string]interface{} {
	tools := make([]map[string]interface{}, 0, len(svc.Tools))
	for _, tool := range svc.Tools {
		toolDef := map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
		}
		if tool.InputSchema != nil {
			toolDef["inputSchema"] = tool.InputSchema
		} else {
			toolDef["inputSchema"] = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		tools = append(tools, toolDef)
	}
	return map[string]interface{}{"tools": tools}
}

// HandleMCPRequest handles an MCP JSON-RPC request for a single service
func (h *ServiceMCPHandler) HandleMCPRequest(ctx context.Context, svc *MCPServiceDTO, req *MCPRequest) *MCPResponse {
	response := &MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
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
				"name":    fmt.Sprintf("gpt-load-%s", svc.Name),
				"version": "1.0.0",
			},
			"instructions": svc.Description,
		}

	case "tools/list":
		response.Result = h.GetServiceTools(svc)

	case "tools/call":
		var callParams struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			response.Error = &MCPError{Code: -32602, Message: "Invalid params: " + err.Error()}
			return response
		}

		// Find the tool
		var targetTool *ToolDefinition
		for i := range svc.Tools {
			if svc.Tools[i].Name == callParams.Name {
				targetTool = &svc.Tools[i]
				break
			}
		}
		if targetTool == nil {
			response.Error = &MCPError{Code: -32601, Message: fmt.Sprintf("Tool not found: %s", callParams.Name)}
			return response
		}

		// Execute the tool
		result, err := h.executeTool(ctx, svc, callParams.Name, callParams.Arguments)
		if err != nil {
			response.Error = &MCPError{Code: -32000, Message: err.Error()}
		} else {
			response.Result = result
		}

	case "notifications/initialized":
		// Client notification, no response needed but return empty result
		response.Result = map[string]interface{}{}

	default:
		response.Error = &MCPError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}

	return response
}

// executeTool executes a tool from the service
func (h *ServiceMCPHandler) executeTool(ctx context.Context, svc *MCPServiceDTO, toolName string, arguments map[string]interface{}) (map[string]interface{}, error) {
	// For API bridge services, execute the actual API call
	if svc.Type == string(ServiceTypeAPIBridge) && h.apiExecutor != nil {
		return h.apiExecutor.ExecuteAPIBridgeTool(ctx, svc.ID, toolName, arguments)
	}

	// For other service types, return execution info
	return map[string]interface{}{
		"service":   svc.Name,
		"tool":      toolName,
		"type":      svc.Type,
		"arguments": arguments,
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": fmt.Sprintf("Tool '%s' from service '%s' ready for execution.", toolName, svc.Name),
			},
		},
	}, nil
}
