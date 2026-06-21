package channel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func init() {
	Register("anthropic", newAnthropicChannel)
}

// ClaudeCodeUserAgent is the User-Agent header value for Claude Code CLI requests.
// Format: claude-cli/VERSION (external, cli) - matches the official Claude Code CLI client.
// Check: https://github.com/anthropics/claude-code/releases
const DefaultClaudeCodeVersion = "2.1.183"

// BuildClaudeCodeUserAgent builds the Claude Code CLI User-Agent string for the given version.
func BuildClaudeCodeUserAgent(version string) string {
	return "claude-cli/" + version + " (external, cli)"
}

// ClaudeCodeUserAgent is the default User-Agent header value for Claude Code CLI requests.
var ClaudeCodeUserAgent = BuildClaudeCodeUserAgent(DefaultClaudeCodeVersion)

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

	payload, err := buildAnthropicValidationPayload(group, ch.TestModel)
	if err != nil {
		return false, fmt.Errorf("failed to build validation payload: %w", err)
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
	ApplySimulatedClientHeaders(req, group, validationStreamEnabled(group))

	client := selection.HTTPClient
	if client == nil {
		client = ch.HTTPClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send validation request: %w", err)
	}
	defer resp.Body.Close()

	return validateKeyResponseStatus(resp)
}

const claudeCodeValidationSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

func buildAnthropicValidationPayload(group *models.Group, model string) (gin.H, error) {
	if simulatedClientMode(group) != simulatedClientClaudeCode {
		payload := gin.H{
			"model":      model,
			"max_tokens": 100,
			"messages": []gin.H{
				{"role": "user", "content": validationPromptForGroup(group)},
			},
		}
		if streamValue, ok := validationStreamPayloadValue(group); ok {
			payload["stream"] = streamValue
		}
		return payload, nil
	}

	sessionID, err := buildClaudeCodeValidationUserID(simulatedClientVersion(group, "simulated_claude_code_version", DefaultClaudeCodeVersion))
	if err != nil {
		return nil, err
	}

	payload := gin.H{
		"model":       model,
		"max_tokens":  1024,
		"temperature": 1,
		"messages": []gin.H{
			{
				"role": "user",
				"content": []gin.H{
					{
						"type": "text",
						"text": validationPromptForGroup(group),
						"cache_control": gin.H{
							"type": "ephemeral",
						},
					},
				},
			},
		},
		"system": []gin.H{
			{
				"type": "text",
				"text": claudeCodeValidationSystemPrompt,
				"cache_control": gin.H{
					"type": "ephemeral",
				},
			},
		},
		"metadata": gin.H{
			"user_id": sessionID,
		},
	}
	if streamValue, ok := validationStreamPayloadValue(group); ok {
		payload["stream"] = streamValue
	}
	return payload, nil
}

func buildClaudeCodeValidationUserID(version string) (string, error) {
	deviceID, err := randomHexString(32)
	if err != nil {
		return "", err
	}
	sessionID := uuid.NewString()
	if isClaudeCodeJSONMetadataVersion(version) {
		return fmt.Sprintf("{\"device_id\":\"%s\",\"account_uuid\":\"\",\"session_id\":\"%s\"}", deviceID, sessionID), nil
	}
	return "user_" + deviceID + "_account__session_" + sessionID, nil
}

func randomHexString(byteLen int) (string, error) {
	if byteLen <= 0 {
		return "", nil
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	const hexChars = "0123456789abcdef"
	out := make([]byte, byteLen*2)
	for i, b := range buf {
		out[i*2] = hexChars[b>>4]
		out[i*2+1] = hexChars[b&0x0f]
	}
	return string(out), nil
}

func isClaudeCodeJSONMetadataVersion(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return false
	}
	major := parseVersionPart(parts[0])
	minor := parseVersionPart(parts[1])
	patch := parseVersionPart(parts[2])
	if major != 2 {
		return major > 2
	}
	if minor != 1 {
		return minor > 1
	}
	return patch >= 78
}

func parseVersionPart(part string) int {
	value := 0
	for _, ch := range part {
		if ch < '0' || ch > '9' {
			break
		}
		value = value*10 + int(ch-'0')
	}
	return value
}
