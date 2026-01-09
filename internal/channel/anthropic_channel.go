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

	"github.com/gin-gonic/gin"
)

func init() {
	Register("anthropic", newAnthropicChannel)
}

// ClaudeCodeUserAgent is the User-Agent header value for Claude Code CLI requests.
// This matches the format used by the official Claude Code CLI client.
// Format: claude-cli/VERSION (external, cli)
const ClaudeCodeUserAgent = "claude-cli/2.1.1 (external, cli)"

type AnthropicChannel struct {
	*BaseChannel
}

func newAnthropicChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("anthropic", group)
	if err != nil {
		return nil, err
	}

	return &AnthropicChannel{
		BaseChannel: base,
	}, nil
}

// ModifyRequest sets the required headers for the Anthropic API.
// This method preserves client-sent anthropic-version and anthropic-beta headers
// to support newer API features like extended thinking.
func (ch *AnthropicChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	// Dual authentication: set both Authorization and x-api-key headers
	// Anthropic API supports both; some proxies may only recognize one
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
	req.Header.Set("x-api-key", apiKey.KeyValue)

	// Only set anthropic-version if not already present from client
	// This allows clients to use newer API versions for features like extended thinking
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	// Note: anthropic-beta header is preserved from client request (via Header.Clone())
	// to support beta features like extended thinking, computer use, etc.
}


// ValidateKey checks if the given API key is valid by making a messages request.
// It now uses BaseChannel.SelectValidationUpstream so that upstream-specific proxy configuration
// is honored consistently with normal traffic.
func (ch *AnthropicChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) (bool, error) {
	// Parse validation endpoint to extract path and query parameters
	endpointURL, err := url.Parse(ch.ValidationEndpoint)
	if err != nil {
		return false, fmt.Errorf("failed to parse validation endpoint: %w", err)
	}

	selection, err := ch.SelectValidationUpstream(group, endpointURL.Path, endpointURL.RawQuery)
	if err != nil {
		return false, fmt.Errorf("failed to select upstream for anthropic validation: %w", err)
	}
	if selection == nil || selection.URL == "" {
		return false, fmt.Errorf("failed to select upstream for anthropic validation: empty result")
	}

	reqURL := selection.URL

	// Use a minimal, low-cost payload for validation
	payload := gin.H{
		"model":      ch.TestModel,
		"max_tokens": 100,
		"messages": []gin.H{
			{"role": "user", "content": "hi"},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal validation payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return false, fmt.Errorf("failed to create validation request: %w", err)
	}
	// Apply dual authentication strategy consistent with ModifyRequest
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
	req.Header.Set("x-api-key", apiKey.KeyValue)
	req.Header.Set("anthropic-version", "2023-06-01")
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

	// Use the new parser to extract a clean error message.
	parsedError := app_errors.ParseUpstreamError(errorBody)

	return false, fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError)
}
