package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

func init() {
	Register("codex", newCodexChannel)
}

// CodexChannel handles OpenAI Codex/Responses API requests.
// Codex uses the Responses API (/v1/responses) which is an evolution of Chat Completions.
// It supports both the new Responses format and legacy Chat Completions format for compatibility.
type CodexChannel struct {
	*BaseChannel
}

func newCodexChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("codex", group)
	if err != nil {
		return nil, err
	}

	return &CodexChannel{
		BaseChannel: base,
	}, nil
}

// CodexUserAgent is the User-Agent header value for Codex CLI requests.
// Format: codex-cli/VERSION - matches the npm package @openai/codex client format.
// NOTE: The Rust binary uses a different format (codex_cli_rs/VERSION), but we use
// the npm format as it's more commonly used and compatible with OpenAI's API.
// Update this version when the official Codex CLI releases a new stable version.
// Check: https://github.com/openai/codex/releases for latest stable release.
const CodexUserAgent = "codex-cli/0.84.0"

// ModifyRequest sets the Authorization header for the Codex/Responses API.
// Note: User-Agent is NOT set here to ensure passthrough behavior for non-CC requests.
// User-Agent is only set in specific cases:
// 1. When fetching models (/v1/models) - handled by FetchGroupModels in group_service.go
// 2. When CC support is enabled - handled by server.go using isCodexCCMode check
func (ch *CodexChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
}

// ValidateKey checks if the given API key is valid by making a responses request.
// Uses the Responses API endpoint for validation.
func (ch *CodexChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) (bool, error) {
	// Parse validation endpoint to extract path and query parameters
	endpointURL, err := url.Parse(ch.ValidationEndpoint)
	if err != nil {
		return false, fmt.Errorf("failed to parse validation endpoint: %w", err)
	}

	// Select upstream with dedicated client using the unified helper
	selection, err := ch.SelectValidationUpstream(group, endpointURL.Path, endpointURL.RawQuery)
	if err != nil {
		return false, fmt.Errorf("failed to select validation upstream: %w", err)
	}
	if selection == nil || selection.URL == "" {
		return false, fmt.Errorf("failed to select validation upstream: empty result")
	}
	reqURL := selection.URL

	// Use Responses API format for validation
	// The Responses API uses "input" instead of "messages"
	payload := gin.H{
		"model": ch.TestModel,
		"input": "hi",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal validation payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return false, fmt.Errorf("failed to create validation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
	req.Header.Set("Content-Type", "application/json")

	// Apply custom header rules if available
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContext(group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	client := selection.HTTPClient
	if client == nil {
		client = ch.HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send validation request: %w", err)
	}
	defer resp.Body.Close()

	// Any 2xx status code indicates the key is valid.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}

	// For non-200 responses, parse the body to provide a more specific error reason.
	errorBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, err)
	}

	// Use the parser to extract a clean error message.
	parsedError := app_errors.ParseUpstreamError(errorBody)

	return false, fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError)
}

// IsStreamRequest checks if the request is for a streaming response.
// Responses API uses "stream" parameter similar to Chat Completions.
func (ch *CodexChannel) IsStreamRequest(c *gin.Context, bodyBytes []byte) bool {
	// Check Accept header for SSE
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}

	// Check query parameter
	if c.Query("stream") == "true" {
		return true
	}

	// Check JSON body for stream field
	type streamPayload struct {
		Stream bool `json:"stream"`
	}
	var p streamPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Stream
	}

	return false
}

// ExtractModel extracts the model name from the request body.
// Supports both Responses API format and Chat Completions format.
func (ch *CodexChannel) ExtractModel(c *gin.Context, bodyBytes []byte) string {
	type modelPayload struct {
		Model string `json:"model"`
	}
	var p modelPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Model
	}
	return ""
}

// ForceStreamRequest modifies the request body to force stream: true.
// Codex API requires streaming for reliable responses (per CLIProxyAPI implementation).
// Returns the modified body bytes and whether the original request was non-streaming.
func ForceStreamRequest(bodyBytes []byte) ([]byte, bool) {
	if len(bodyBytes) == 0 {
		return bodyBytes, false
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return bodyBytes, false
	}

	// Check original stream value
	originalStream := false
	if v, ok := payload["stream"].(bool); ok {
		originalStream = v
	}

	// If already streaming, no modification needed
	if originalStream {
		return bodyBytes, false
	}

	// Force stream: true
	payload["stream"] = true
	modifiedBody, err := json.Marshal(payload)
	if err != nil {
		return bodyBytes, false
	}

	return modifiedBody, true
}
