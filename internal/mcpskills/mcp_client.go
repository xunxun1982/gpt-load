// Package mcpskills provides MCP service management and tool discovery
package mcpskills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
)

// MCPToolDiscovery handles MCP tool discovery for services
type MCPToolDiscovery struct {
	timeout     time.Duration
	serviceName string // Service name for logging
}

// NewMCPToolDiscovery creates a new tool discovery instance
func NewMCPToolDiscovery() *MCPToolDiscovery {
	return &MCPToolDiscovery{
		timeout: 30 * time.Second,
	}
}

// NewMCPToolDiscoveryWithTimeout creates a new tool discovery instance with custom timeout
func NewMCPToolDiscoveryWithTimeout(timeout time.Duration) *MCPToolDiscovery {
	return &MCPToolDiscovery{
		timeout: timeout,
	}
}

// WithServiceName sets the service name for logging and returns the discovery instance
func (d *MCPToolDiscovery) WithServiceName(name string) *MCPToolDiscovery {
	d.serviceName = name
	return d
}

// DiscoveredTool represents a tool discovered from an MCP server
type DiscoveredTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// DiscoveryResult contains the result of tool discovery
type DiscoveryResult struct {
	Success     bool             `json:"success"`
	ServerName  string           `json:"server_name"`
	ServerVer   string           `json:"server_version"`
	Tools       []DiscoveredTool `json:"tools"`
	Error       string           `json:"error,omitempty"`
	Description string           `json:"description,omitempty"`
}

// mcpClientInterface defines common interface for MCP clients
type mcpClientInterface interface {
	Initialize(ctx context.Context, request mcp.InitializeRequest) (*mcp.InitializeResult, error)
	ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
	Close() error
}

// mcpClientWithStart extends mcpClientInterface with Start method
type mcpClientWithStart interface {
	mcpClientInterface
	Start(ctx context.Context) error
}

// buildInitRequest creates a standard MCP initialize request
func buildInitRequest() mcp.InitializeRequest {
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{
		Name:    "gpt-load",
		Version: "1.0.0",
	}
	return req
}

// extractServerInfo extracts server info from initialize result into discovery result
func extractServerInfo(result *DiscoveryResult, initResult *mcp.InitializeResult) {
	if initResult == nil {
		return
	}
	result.ServerName = initResult.ServerInfo.Name
	result.ServerVer = initResult.ServerInfo.Version
	if initResult.Instructions != "" {
		result.Description = initResult.Instructions
	}
}

// convertToolsResult converts MCP tools result to discovered tools
func convertToolsResult(toolsResult *mcp.ListToolsResult) []DiscoveredTool {
	if toolsResult == nil {
		return nil
	}
	tools := make([]DiscoveredTool, 0, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		dt := DiscoveredTool{
			Name:        tool.Name,
			Description: tool.Description,
		}
		if tool.InputSchema.Properties != nil {
			schemaMap := map[string]interface{}{
				"type":       "object",
				"properties": tool.InputSchema.Properties,
			}
			if len(tool.InputSchema.Required) > 0 {
				schemaMap["required"] = tool.InputSchema.Required
			}
			dt.InputSchema = schemaMap
		}
		tools = append(tools, dt)
	}
	return tools
}

// discoverFromClient performs tool discovery using an MCP client
func (d *MCPToolDiscovery) discoverFromClient(ctx context.Context, mcpClient mcpClientInterface, clientType string) *DiscoveryResult {
	result := &DiscoveryResult{Success: false, Tools: []DiscoveredTool{}}

	// Use parent context timeout if shorter than default, otherwise use default
	timeoutCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	// Build log fields with service name if available
	logFields := logrus.Fields{"type": clientType}
	if d.serviceName != "" {
		logFields["service"] = d.serviceName
	}

	// Initialize
	initResult, err := mcpClient.Initialize(timeoutCtx, buildInitRequest())
	if err != nil {
		result.Error = fmt.Sprintf("Failed to initialize %s MCP client: %v", clientType, err)
		logrus.WithFields(logFields).WithError(err).Warn("Failed to initialize MCP client")
		return result
	}
	extractServerInfo(result, initResult)

	// List tools
	toolsResult, err := mcpClient.ListTools(timeoutCtx, mcp.ListToolsRequest{})
	if err != nil {
		result.Error = fmt.Sprintf("Failed to list tools: %v", err)
		logrus.WithFields(logFields).WithError(err).Warn("Failed to list tools from MCP server")
		return result
	}

	result.Tools = convertToolsResult(toolsResult)
	result.Success = true
	logFields["server_name"] = result.ServerName
	logFields["tool_count"] = len(result.Tools)
	logrus.WithFields(logFields).Info("Successfully discovered tools from MCP server")

	return result
}

// DiscoverToolsForStdio discovers tools from a stdio MCP server
func (d *MCPToolDiscovery) DiscoverToolsForStdio(ctx context.Context, command string, args []string, envVars map[string]string) (*DiscoveryResult, error) {
	if command == "" {
		return &DiscoveryResult{Success: false, Tools: []DiscoveredTool{}, Error: "Command is empty"}, nil
	}

	// Prepare environment variables
	env := os.Environ()
	for key, value := range envVars {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	mcpClient, err := client.NewStdioMCPClient(command, env, args...)
	if err != nil {
		return &DiscoveryResult{
			Success: false,
			Tools:   []DiscoveredTool{},
			Error:   fmt.Sprintf("Failed to create MCP client: %v", err),
		}, nil
	}
	defer mcpClient.Close()

	return d.discoverFromClient(ctx, mcpClient, "stdio"), nil
}

// DiscoverToolsForSSE discovers tools from an SSE MCP server
func (d *MCPToolDiscovery) DiscoverToolsForSSE(ctx context.Context, url string, headers map[string]string) (*DiscoveryResult, error) {
	if url == "" {
		return &DiscoveryResult{Success: false, Tools: []DiscoveredTool{}, Error: "URL is empty"}, nil
	}

	var mcpClient *client.Client
	var err error
	if len(headers) > 0 {
		mcpClient, err = client.NewSSEMCPClient(url, client.WithHeaders(headers))
	} else {
		mcpClient, err = client.NewSSEMCPClient(url)
	}
	if err != nil {
		return &DiscoveryResult{
			Success: false,
			Tools:   []DiscoveredTool{},
			Error:   fmt.Sprintf("Failed to create SSE MCP client: %v", err),
		}, nil
	}
	defer mcpClient.Close()

	// Start the client
	timeoutCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	if err := mcpClient.Start(timeoutCtx); err != nil {
		return &DiscoveryResult{
			Success: false,
			Tools:   []DiscoveredTool{},
			Error:   fmt.Sprintf("Failed to start SSE MCP client: %v", err),
		}, nil
	}

	return d.discoverFromClient(ctx, mcpClient, "SSE"), nil
}

// DiscoverToolsForStreamableHTTP discovers tools from a Streamable HTTP MCP server
func (d *MCPToolDiscovery) DiscoverToolsForStreamableHTTP(ctx context.Context, url string, headers map[string]string) (*DiscoveryResult, error) {
	if url == "" {
		return &DiscoveryResult{Success: false, Tools: []DiscoveredTool{}, Error: "URL is empty"}, nil
	}

	var opts []transport.StreamableHTTPCOption
	if len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	mcpClient, err := client.NewStreamableHttpClient(url, opts...)
	if err != nil {
		return &DiscoveryResult{
			Success: false,
			Tools:   []DiscoveredTool{},
			Error:   fmt.Sprintf("Failed to create Streamable HTTP MCP client: %v", err),
		}, nil
	}
	defer mcpClient.Close()

	// Start the client
	timeoutCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	if err := mcpClient.Start(timeoutCtx); err != nil {
		return &DiscoveryResult{
			Success: false,
			Tools:   []DiscoveredTool{},
			Error:   fmt.Sprintf("Failed to start Streamable HTTP MCP client: %v", err),
		}, nil
	}

	return d.discoverFromClient(ctx, mcpClient, "Streamable HTTP"), nil
}

// DiscoverToolsForService discovers tools for a service based on its type
func (d *MCPToolDiscovery) DiscoverToolsForService(ctx context.Context, svc *MCPService) (*DiscoveryResult, error) {
	// Set service name for logging
	d.serviceName = svc.Name

	envVars := make(map[string]string)
	if defaultEnvs, err := svc.GetDefaultEnvs(); err == nil {
		envVars = defaultEnvs
	}

	headers := make(map[string]string)
	if h, err := svc.GetHeaders(); err == nil {
		headers = h
	}

	switch svc.Type {
	case string(ServiceTypeStdio):
		args, err := svc.GetArgs()
		if err != nil {
			return &DiscoveryResult{
				Success: false,
				Tools:   []DiscoveredTool{},
				Error:   fmt.Sprintf("Failed to parse service args: %v", err),
			}, nil
		}
		return d.DiscoverToolsForStdio(ctx, svc.Command, args, envVars)

	case string(ServiceTypeSSE):
		url := svc.APIEndpoint
		if url == "" {
			url = svc.Command
		}
		return d.DiscoverToolsForSSE(ctx, url, headers)

	case string(ServiceTypeStreamableHTTP):
		url := svc.APIEndpoint
		if url == "" {
			url = svc.Command
		}
		return d.DiscoverToolsForStreamableHTTP(ctx, url, headers)

	default:
		return &DiscoveryResult{
			Success: false,
			Error:   fmt.Sprintf("Unsupported service type for tool discovery: %s", svc.Type),
		}, nil
	}
}

// ConvertDiscoveredToolsToDefinitions converts discovered tools to ToolDefinition format
func ConvertDiscoveredToolsToDefinitions(tools []DiscoveredTool) []ToolDefinition {
	result := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		result = append(result, ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}
	return result
}

// CheckCommandExists checks if a command exists in PATH
func CheckCommandExists(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

// GuessPackageManagerFromCommand guesses the package manager from command
func GuessPackageManagerFromCommand(command string, args []string) (packageManager string, packageName string) {
	cmd := strings.ToLower(command)

	switch cmd {
	case "npx", "npm":
		packageManager = "npm"
		packageName = findPackageNameInArgs(args, true)

	case "uvx", "uv", "pip", "python", "python3":
		packageManager = "pypi"
		// Check for --from flag first
		for i, arg := range args {
			if arg == "--from" && i+1 < len(args) {
				packageName = args[i+1]
				return
			}
		}
		packageName = findPackageNameInArgs(args, false)

	default:
		packageManager = "custom"
	}

	return
}

// findPackageNameInArgs finds package name from command arguments
func findPackageNameInArgs(args []string, checkAtSign bool) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") || arg == "-y" {
			continue
		}
		if checkAtSign && (strings.Contains(arg, "@") || strings.Contains(arg, "/")) {
			return arg
		}
		if !checkAtSign || (!strings.HasPrefix(arg, "-") && arg != "-y") {
			return arg
		}
	}
	return ""
}
