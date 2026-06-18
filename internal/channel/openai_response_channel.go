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
	Register("openai-response", newOpenAIResponseChannel)
}

// OpenAIResponseChannel handles OpenAI-compatible Responses API requests.
// It also carries Codex CLI compatibility behavior used by CC mode.
type OpenAIResponseChannel struct {
	*BaseChannel
}

func newOpenAIResponseChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("openai-response", group)
	if err != nil {
		return nil, err
	}

	return &OpenAIResponseChannel{
		BaseChannel: base,
	}, nil
}

// DefaultCodexVersion is the default Codex TUI version used for simulated client fingerprints.
const DefaultCodexVersion = "0.141.0"

// BuildCodexUserAgent builds the Codex TUI User-Agent string for the given version.
func BuildCodexUserAgent(version string) string {
	return "codex-tui/" + version + " (Windows 10.0.19045; x86_64) WindowsTerminal (codex-tui; " + version + ")"
}

// CodexUserAgent is the default User-Agent header value for Codex CLI-compatible requests.
var CodexUserAgent = BuildCodexUserAgent(DefaultCodexVersion)

// ModifyRequest sets the Authorization header for the Responses API.
// User-Agent is intentionally left unchanged for normal proxy requests.
func (ch *OpenAIResponseChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
}

// ValidateKey checks whether the given API key can call the Responses API.
func (ch *OpenAIResponseChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) (bool, error) {
	endpointURL, err := url.Parse(ch.ValidationEndpoint)
	if err != nil {
		return false, fmt.Errorf("failed to parse validation endpoint: %w", err)
	}

	selection, err := ch.SelectValidationUpstream(group, endpointURL.Path, endpointURL.RawQuery)
	if err != nil {
		return false, fmt.Errorf("failed to select validation upstream: %w", err)
	}
	if selection == nil || selection.URL == "" {
		return false, fmt.Errorf("failed to select validation upstream: empty result")
	}
	reqURL := selection.URL

	payload := gin.H{
		"model": ch.TestModel,
		"input": validationPromptForGroup(group),
	}
	if validationStreamEnabled(group) {
		payload["stream"] = true
	}
	if validationResponsesIncludeEncryptedReasoning(group) {
		payload["include"] = []string{"reasoning.encrypted_content"}
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

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}

	errorBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, err)
	}

	parsedError := app_errors.ParseUpstreamError(errorBody)
	return false, fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError)
}

// IsStreamRequest checks whether the request expects a streaming response.
func (ch *OpenAIResponseChannel) IsStreamRequest(c *gin.Context, bodyBytes []byte) bool {
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}

	if c.Query("stream") == "true" {
		return true
	}

	type streamPayload struct {
		Stream bool `json:"stream"`
	}
	var p streamPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Stream
	}

	return false
}

// ExtractModel extracts the model name from a Responses API request body.
func (ch *OpenAIResponseChannel) ExtractModel(c *gin.Context, bodyBytes []byte) string {
	type modelPayload struct {
		Model string `json:"model"`
	}
	var p modelPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Model
	}
	return ""
}

// ForceStreamRequest modifies a Responses API request body to force stream: true.
// It returns the modified body and whether the original request was non-streaming.
func ForceStreamRequest(bodyBytes []byte) ([]byte, bool) {
	if len(bodyBytes) == 0 {
		return bodyBytes, false
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return bodyBytes, false
	}

	originalStream := false
	if v, ok := payload["stream"].(bool); ok {
		originalStream = v
	}

	if originalStream {
		return bodyBytes, false
	}

	payload["stream"] = true
	modifiedBody, err := json.Marshal(payload)
	if err != nil {
		return bodyBytes, false
	}

	return modifiedBody, true
}
