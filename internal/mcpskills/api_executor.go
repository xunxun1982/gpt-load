// Package mcpskills provides MCP service management and API bridge execution
package mcpskills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gpt-load/internal/encryption"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// APIExecutor handles API bridge tool execution
type APIExecutor struct {
	db            *gorm.DB
	encryptionSvc encryption.Service
	httpClient    *http.Client
}

// NewAPIExecutor creates a new API executor instance
func NewAPIExecutor(db *gorm.DB, encryptionSvc encryption.Service) *APIExecutor {
	return &APIExecutor{
		db:            db,
		encryptionSvc: encryptionSvc,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ExecuteAPIBridgeTool executes an API bridge tool and returns the result
func (e *APIExecutor) ExecuteAPIBridgeTool(ctx context.Context, serviceID uint, toolName string, arguments map[string]interface{}) (map[string]interface{}, error) {
	// Get service from database
	var svc MCPService
	if err := e.db.WithContext(ctx).First(&svc, serviceID).Error; err != nil {
		return nil, fmt.Errorf("service not found: %w", err)
	}

	if !svc.Enabled {
		return nil, fmt.Errorf("service '%s' is disabled", svc.Name)
	}

	if svc.Type != string(ServiceTypeAPIBridge) {
		return nil, fmt.Errorf("service '%s' is not an API bridge service", svc.Name)
	}

	// Find the tool definition
	tools, err := svc.GetTools()
	if err != nil {
		return nil, fmt.Errorf("failed to get tools: %w", err)
	}

	var targetTool *ToolDefinition
	for i := range tools {
		if tools[i].Name == toolName {
			targetTool = &tools[i]
			break
		}
	}

	if targetTool == nil {
		return nil, fmt.Errorf("tool '%s' not found in service '%s'", toolName, svc.Name)
	}

	// Decrypt API key
	apiKey := ""
	if svc.APIKeyValue != "" {
		decrypted, err := e.encryptionSvc.Decrypt(svc.APIKeyValue)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt API key: %w", err)
		}
		apiKey = decrypted
	}

	// Build and execute the API request
	return e.executeAPIRequest(ctx, &svc, targetTool, arguments, apiKey)
}

// executeAPIRequest builds and executes the HTTP request for an API bridge tool
// It handles authentication, custom headers, and response parsing
func (e *APIExecutor) executeAPIRequest(ctx context.Context, svc *MCPService, tool *ToolDefinition, arguments map[string]interface{}, apiKey string) (map[string]interface{}, error) {
	// Build full URL from endpoint and tool-specific path
	endpoint := svc.APIEndpoint
	endpoint = strings.TrimSuffix(endpoint, "/")

	// Map tool names to API paths based on service type
	apiPath := e.getAPIPath(svc.Name, tool.Name)
	fullURL := endpoint + apiPath

	// Serialize request arguments to JSON
	reqBody, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal arguments: %w", err)
	}

	// Create HTTP request with context for cancellation support
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set standard headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Set authentication header if API key is configured
	// Supports various auth schemes: Bearer, x-api-key, custom headers
	if apiKey != "" {
		authHeader := svc.APIKeyHeader
		if authHeader == "" {
			authHeader = "Authorization"
		}
		authValue := apiKey
		if svc.APIKeyPrefix != "" {
			authValue = svc.APIKeyPrefix + " " + apiKey
		}
		req.Header.Set(authHeader, authValue)

		// Debug log for authentication (mask API key for security)
		maskedKey := "***"
		if len(apiKey) > 4 {
			maskedKey = apiKey[:4] + "***"
		}
		logrus.WithFields(logrus.Fields{
			"service":     svc.Name,
			"auth_header": authHeader,
			"auth_prefix": svc.APIKeyPrefix,
			"api_key":     maskedKey,
			"url":         fullURL,
		}).Debug("API bridge request authentication")
	} else {
		logrus.WithFields(logrus.Fields{
			"service": svc.Name,
			"url":     fullURL,
		}).Warn("API bridge request without API key")
	}

	// Add any custom headers defined in service configuration
	headers, err := svc.GetHeaders()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"service": svc.Name,
			"error":   err.Error(),
		}).Warn("Failed to parse custom headers, skipping")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Execute the HTTP request
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read and parse response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Attempt to parse as JSON, fallback to text wrapper
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		result = map[string]interface{}{
			"text": string(body),
		}
	}

	// Handle error responses (4xx, 5xx status codes)
	if resp.StatusCode >= 400 {
		logrus.WithFields(logrus.Fields{
			"service":     svc.Name,
			"tool":        tool.Name,
			"status_code": resp.StatusCode,
		}).Warn("API bridge request returned error status")

		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("API returned status %d", resp.StatusCode),
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": fmt.Sprintf("Error: API returned status %d - %s", resp.StatusCode, string(body)),
				},
			},
		}, nil
	}

	// Return successful result in MCP-compatible format
	return map[string]interface{}{
		"success": true,
		"result":  result,
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": string(body),
			},
		},
	}, nil
}

// getAPIPath returns the API path for a given service and tool
func (e *APIExecutor) getAPIPath(serviceName, toolName string) string {
	// API path mappings for predefined services in APIBridgeTemplates
	pathMappings := map[string]map[string]string{
		"exa-search": {
			"search":       "/search",
			"find_similar": "/findSimilar",
			"get_contents": "/contents",
		},
	}

	if servicePaths, ok := pathMappings[serviceName]; ok {
		if path, ok := servicePaths[toolName]; ok {
			return path
		}
	}

	// Default: use tool name as path
	return "/" + toolName
}

// ExecuteToolByName executes a tool by service name and tool name
func (e *APIExecutor) ExecuteToolByName(ctx context.Context, serviceName, toolName string, arguments map[string]interface{}) (map[string]interface{}, error) {
	// Get service by name
	var svc MCPService
	if err := e.db.WithContext(ctx).Where("name = ?", serviceName).First(&svc).Error; err != nil {
		return nil, fmt.Errorf("service '%s' not found: %w", serviceName, err)
	}

	return e.ExecuteAPIBridgeTool(ctx, svc.ID, toolName, arguments)
}
