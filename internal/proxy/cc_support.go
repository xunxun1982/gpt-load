// Package proxy provides CC (Claude Code) support functionality.
// CC support enables Claude clients to connect via /claude endpoint and have
// requests converted from Claude format to OpenAI format before forwarding.
// NOTE: This file intentionally keeps the CC conversion + streaming logic in one place.
// Splitting into multiple files would improve navigation, but we avoid it here to
// minimize refactor risk and keep performance-sensitive paths localized.
package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Context keys for CC support middleware.
const (
	ctxKeyCCEnabled      = "cc_enabled"
	ctxKeyOriginalFormat = "cc_original_format"
	// ctxKeyOpenAIResponseCC indicates OpenAI Responses CC mode.
	// The conversion still uses Codex CLI-compatible request/response details internally.
	ctxKeyOpenAIResponseCC = "openai_response_cc"
	// ctxKeyOpenAIToolNameReverseMap stores the reverse map for tool name restoration in OpenAI CC mode.
	// This is used to restore original tool names that were shortened to comply with OpenAI's 64-char limit.
	ctxKeyOpenAIToolNameReverseMap = "openai_tool_name_reverse_map"
	// ctxKeyOpenAIResponseForcedStream indicates that OpenAI Responses forced stream: true for upstream.
	// When set, the response handler should collect stream and return non-stream if client requested non-stream.
	ctxKeyOpenAIResponseForcedStream = "openai_response_forced_stream"
)

// ctxKeyTriggerSignal and ctxKeyFunctionCallEnabled are declared in server.go (same package proxy).
// We keep them there to avoid introducing an extra constants file.
const maxUpstreamResponseBodySize = 32 * 1024 * 1024

var ErrBodyTooLarge = errors.New("CC: upstream response body exceeded maximum allowed size")

// ccModelRedirectSelector is a shared selector instance for V2 rules in CC mode.
// Stateless, safe for concurrent use.
var ccModelRedirectSelector = models.NewModelRedirectSelector(utils.WeightedRandomSelect)

// getModelRedirectSelector returns the shared model redirect selector for CC mode.
func getModelRedirectSelector() *models.ModelRedirectSelector {
	return ccModelRedirectSelector
}

// isValidToolCallArguments checks if tool call arguments are valid (not empty or just "{}").
// Some upstream models (especially in thinking mode like deepseek-reasoner) may return
// tool_calls with empty arguments as placeholders during reasoning. These should be
// skipped to avoid Claude Code errors like "The required parameter 'pattern' is missing".
// Returns true if arguments are valid and should be converted to tool_use block.
//
// NOTE: We intentionally do NOT handle whitespace-padded empty objects like "{ }" or "{\n}".
// Upstream APIs (OpenAI, DeepSeek, etc.) use standard JSON serializers that produce "{}"
// without internal whitespace. This matches the project's existing pattern in model_mapping.go.
// Adding strings.ReplaceAll would add unnecessary overhead for a case that doesn't occur in practice.
func isValidToolCallArguments(toolName, arguments string) bool {
	trimmed := strings.TrimSpace(arguments)
	// Empty string or empty JSON object are invalid (standard JSON serializers produce "{}" without whitespace)
	if trimmed == "" || trimmed == "{}" {
		logrus.WithFields(logrus.Fields{
			"tool_name": toolName,
			"arguments": trimmed,
		}).Debug("CC: Skipping tool_call with empty arguments (likely thinking mode placeholder)")
		return false
	}
	return true
}

// maxContentBufferBytes is declared in function_call.go (same package proxy).
// We keep the single source of truth there to avoid drift without adding extra files.
// clearUpstreamEncodingHeaders removes upstream transfer-related headers before
// writing a synthesized response body for CC support. This avoids mismatches
// between headers and the rewritten body (for example after decompression).
func clearUpstreamEncodingHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Del("Content-Encoding")
	h.Del("Content-Length")
	h.Del("Transfer-Encoding")
}

// readAllWithLimit reads all data from the reader up to the given limit.
// If the response exceeds the limit, ErrBodyTooLarge is returned and the
// caller should not attempt to parse the partial payload.
func readAllWithLimit(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}

	// Read up to limit+1 bytes so we can detect overflow without keeping
	// more than a small constant above the configured limit in memory.
	limited := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}

// getGroupConfigBool extracts a boolean value from group config with flexible type handling.
// Supports bool, float64, int, and string ("true", "1", "yes", "on") types.
// Returns false if the key doesn't exist or the value cannot be interpreted as true.
func getGroupConfigBool(group *models.Group, key string) bool {
	if group == nil || group.Config == nil {
		return false
	}

	raw, ok := group.Config[key]
	if !ok || raw == nil {
		return false
	}

	switch v := raw.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case int:
		return v != 0
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		return lower == "true" || lower == "1" || lower == "yes" || lower == "on"
	default:
		return false
	}
}

// getGroupConfigString extracts a string value from group config.
// Returns empty string if the key doesn't exist or the value is not a string.
// Trims whitespace for consistency with other config handling.
func getGroupConfigString(group *models.Group, key string) string {
	if group == nil || group.Config == nil {
		return ""
	}

	raw, ok := group.Config[key]
	if !ok || raw == nil {
		return ""
	}

	if v, ok := raw.(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// isCCSupportEnabled checks whether the cc_support flag is enabled for the given group.
// This flag is stored in the group-level JSON config.
// Supports OpenAI and OpenAI Responses channel types.
func isCCSupportEnabled(group *models.Group) bool {
	if group == nil || (group.ChannelType != "openai" && group.ChannelType != "openai-response") {
		return false
	}
	return getGroupConfigBool(group, "cc_support")
}

func isClaudeEndpointSupported(group *models.Group) bool {
	if group == nil {
		return false
	}
	if group.ChannelType == "anthropic" {
		return true
	}
	return isCCSupportEnabled(group)
}

// isInterceptEventLogEnabled checks whether the intercept_event_log flag is enabled for the given group.
// This flag is stored in the group-level JSON config and only applies to Anthropic channel groups.
// When enabled, the /api/event_logging/batch endpoint is intercepted and not forwarded to upstream.
// Default: true (enabled) for Anthropic channel groups when not explicitly configured.
func isInterceptEventLogEnabled(group *models.Group) bool {
	// Only enable for Anthropic channel groups.
	if group == nil || group.ChannelType != "anthropic" {
		return false
	}
	// Check if explicitly configured
	if group.Config != nil {
		if raw, ok := group.Config["intercept_event_log"]; ok && raw != nil {
			return getGroupConfigBool(group, "intercept_event_log")
		}
	}
	// Default to true for Anthropic channel groups
	return true
}

// sanitizeCCQueryParams removes Claude-specific query parameters from the URL.
// This is used by CC support to avoid forwarding Anthropic beta flags to OpenAI-style upstreams.
func sanitizeCCQueryParams(u *url.URL) {
	if u == nil || u.RawQuery == "" {
		return
	}

	query := u.Query()
	// Remove Claude-specific beta flag
	query.Del("beta")
	u.RawQuery = query.Encode()
}

// isClaudePath checks if the request path contains a Claude-style segment after the group name.
// This is used to detect any Claude-style path that needs to be rewritten.
// Path format: /proxy/{group}/claude/v1/...
// For groups literally named "claude", OpenAI-style paths like /proxy/claude/v1/messages are NOT treated as CC paths.
// Examples:
//   - /proxy/mygroup/claude/v1/models -> true
//   - /proxy/claude/v1/models -> false (group named "claude", OpenAI-style path)
//   - /proxy/claude/claude/v1/models -> true (group named "claude", with CC path)
func isClaudePath(path, groupName string) bool {
	// For proxy routes, require /proxy/{group}/claude/v1 prefix to avoid dropping the group segment.
	if groupName != "" {
		prefix := "/proxy/" + groupName + "/"
		if strings.HasPrefix(path, prefix) {
			suffix := strings.TrimPrefix(path, prefix)
			return strings.HasPrefix(suffix, "claude/v1/") || suffix == "claude/v1"
		}
	}

	// Fallback for non-proxy paths or when groupName is unknown.
	return strings.Contains(path, "/claude/v1/") || strings.HasSuffix(path, "/claude/v1")
}

// rewriteClaudePathToOpenAIGeneric removes the /claude segment from the path.
// This converts any Claude-style path to OpenAI-style path.
// Only removes /claude when followed by /v1 to avoid affecting group names.
// Examples:
//   - /proxy/{group}/claude/v1/models -> /proxy/{group}/v1/models
//   - /proxy/{group}/claude/v1/messages -> /proxy/{group}/v1/messages
//   - /proxy/claude/claude/v1/models -> /proxy/claude/v1/models
func rewriteClaudePathToOpenAIGeneric(path string) string {
	// Only replace /claude/v1 pattern to avoid affecting group names
	return strings.Replace(path, "/claude/v1", "/v1", 1)
}

// isCCEnabled returns true if CC support was enabled for the current request.
func isCCEnabled(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyCCEnabled); ok {
		if enabled, ok := v.(bool); ok && enabled {
			return true
		}
	}
	return false
}

// isCCRequest returns true if the current request is a Claude Code request,
// checking both the original path and context flags set during request processing.
// This helper consolidates the three-way check pattern used across CC handlers.
func isCCRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if c.Request == nil || c.Request.URL == nil {
		return c.GetBool("cc_was_claude_path") || c.GetString(ctxKeyOriginalFormat) == "claude"
	}
	// Check original path contains Claude segment
	if strings.Contains(c.Request.URL.Path, "/claude/") {
		return true
	}
	// Check if CC was detected during path rewriting
	if c.GetBool("cc_was_claude_path") {
		return true
	}
	// Check if CC conversion was applied
	return c.GetString(ctxKeyOriginalFormat) == "claude"
}

func getTriggerSignal(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyTriggerSignal); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// getOpenAIToolNameReverseMap retrieves the tool name reverse map from context.
// Returns nil if not found or if tool name shortening was not applied.
//
// PERFORMANCE: Returns the underlying map by reference for zero-copy performance.
// SAFETY: Callers MUST treat the returned map as read-only. Mutation would corrupt
// subsequent restorations within the same request. Per AI review, a copy could be
// returned for safety, but the performance cost is not justified since all current
// callers only read from the map (verified by code inspection).
func getOpenAIToolNameReverseMap(c *gin.Context) map[string]string {
	if v, ok := c.Get(ctxKeyOpenAIToolNameReverseMap); ok {
		if m, ok := v.(map[string]string); ok {
			return m
		}
	}
	return nil
}

// isShortenToolNamesEnabled checks whether tool name shortening is enabled for the group.
// This is controlled by the "shorten_tool_names" config option.
// Default: true (enabled) for compatibility with OpenAI's 64-char tool name limit.
// Set to false for third-party OpenAI-compatible APIs that don't have this limit.
// NOTE: This function intentionally does NOT reuse getGroupConfigBool because:
// 1. Default value is true (vs false in getGroupConfigBool)
// 2. String logic is inverted: only explicit "false"/"no"/"off"/"0" disables
// 3. Invalid/unknown values default to enabled for safety (OpenAI compatibility)
func isShortenToolNamesEnabled(group *models.Group) bool {
	if group == nil || group.Config == nil {
		return true // Default enabled for OpenAI compatibility
	}
	raw, ok := group.Config["shorten_tool_names"]
	if !ok {
		return true // Default enabled
	}
	// If explicitly set, use the value
	// Handle multiple types since JSON decoding may produce float64 for numbers
	switch v := raw.(type) {
	case bool:
		return v
	case float64:
		// JSON numbers decode as float64; treat 0 as disabled
		return v != 0
	case int:
		return v != 0
	case int64:
		// Support int64 for programmatically constructed configs
		return v != 0
	case uint64:
		// Support uint64 for programmatically constructed configs
		return v != 0
	case json.Number:
		// Support json.Number when UseNumber is enabled in decoder
		if f, err := v.Float64(); err == nil {
			return f != 0
		}
		return true // Default enabled on parse error
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		// Only disable if explicitly set to false/no/off/0
		return lower != "false" && lower != "no" && lower != "off" && lower != "0"
	default:
		return true
	}
}

// ClaudeMessage represents a message in Claude format.
type ClaudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ClaudeContentBlock represents a content block in Claude format.
type ClaudeContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// EncryptedContent is accepted for Responses reasoning replay when a client
	// provides the original opaque reasoning item metadata.
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	ToolUseID        string          `json:"tool_use_id,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	Source           json.RawMessage `json:"source,omitempty"`
	Title            string          `json:"title,omitempty"`
	Context          string          `json:"context,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
}

// ClaudeTool represents a tool definition in Claude format.
type ClaudeTool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ThinkingConfig represents Claude extended thinking configuration.
type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type ClaudeOutputConfig struct {
	Effort string          `json:"effort,omitempty"`
	Format json.RawMessage `json:"format,omitempty"`
}

// ClaudeRequest represents a Claude API request.
// It is intentionally a superset of the basic fields to support newer Claude Code features
// such as prompt-only requests, alternative max token fields, tool_choice and MCP metadata.
type ClaudeRequest struct {
	Model             string              `json:"model"`
	Prompt            string              `json:"prompt,omitempty"`
	System            json.RawMessage     `json:"system,omitempty"`
	Messages          []ClaudeMessage     `json:"messages"`
	MaxTokens         int                 `json:"max_tokens,omitempty"`
	MaxTokensToSample int                 `json:"max_tokens_to_sample,omitempty"`
	Temperature       *float64            `json:"temperature,omitempty"`
	TopK              *int                `json:"top_k,omitempty"`
	TopP              *float64            `json:"top_p,omitempty"`
	Stream            bool                `json:"stream"`
	Tools             []ClaudeTool        `json:"tools,omitempty"`
	StopSequences     []string            `json:"stop_sequences,omitempty"`
	ToolChoice        json.RawMessage     `json:"tool_choice,omitempty"`
	McpServers        json.RawMessage     `json:"mcp_servers,omitempty"`
	Metadata          json.RawMessage     `json:"metadata,omitempty"`
	Container         json.RawMessage     `json:"container,omitempty"`
	Thinking          *ThinkingConfig     `json:"thinking,omitempty"`
	OutputConfig      *ClaudeOutputConfig `json:"output_config,omitempty"`
	ServiceTier       string              `json:"service_tier,omitempty"`
	maxTokensSet      bool
	unsupportedFields []string
}

type ccOptionalInt struct {
	value int
	set   bool
}

func (v *ccOptionalInt) UnmarshalJSON(data []byte) error {
	v.set = true
	if strings.TrimSpace(string(data)) == "null" {
		return fmt.Errorf("max_tokens must be an integer")
	}
	return json.Unmarshal(data, &v.value)
}

func (r *ClaudeRequest) UnmarshalJSON(data []byte) error {
	type plain ClaudeRequest
	aux := struct {
		*plain
		MaxTokens ccOptionalInt `json:"max_tokens"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.maxTokensSet = aux.MaxTokens.set
	if aux.MaxTokens.set {
		r.MaxTokens = aux.MaxTokens.value
	}
	unsupported, err := captureUnsupportedJSONFields(data, claudeKnownRequestFields)
	if err != nil {
		return err
	}
	r.unsupportedFields = unsupported
	return nil
}

// OpenAIMessage represents a message in OpenAI format.
type OpenAIMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"` // DeepSeek reasoner thinking content for multi-turn
}

// OpenAIToolCall represents a tool call in OpenAI format.
type OpenAIToolCall struct {
	Index    *int               `json:"index,omitempty"`
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall represents a function call in OpenAI format.
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAITool represents a tool definition in OpenAI format.
type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction represents a function definition in OpenAI format.
type OpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// OpenAIRequest represents an OpenAI API request.
// Only include fields that are known to be compatible with OpenAI-style and
// z.ai chat-completion APIs. Advanced fields like metadata and Anthropic-style
// tool_choice objects are intentionally not forwarded to avoid parameter errors.
type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stream      bool            `json:"stream"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
	Stop        json.RawMessage `json:"stop,omitempty"`
	// NOTE: We intentionally keep interface{} here (instead of json.RawMessage).
	// This field is only used for outbound serialization to upstream OpenAI-style APIs,
	// not for inbound JSON parsing. Using json.RawMessage would require manual JSON
	// quoting for string values (e.g. "auto") and increases the chance of producing
	// invalid JSON. With interface{}, json.Marshal guarantees correct JSON encoding
	// for both string and object forms while keeping the code simple (KISS).
	ToolChoice interface{} `json:"tool_choice,omitempty"`
	// ParallelToolCalls is preserved when converting Responses requests to Chat Completions
	// for the explicit /codex force endpoint.
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty"`
	// ReasoningEffort is provider-defined and is forwarded without value normalization.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	User            string `json:"user,omitempty"`
}

type claudeMediaSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

func ccUnsupported(kind, value string) error {
	safeValue := strings.TrimSpace(utils.SanitizeErrorBody(value))
	if safeValue == "" {
		safeValue = "<empty>"
	}
	safeValue = utils.TruncateString(safeValue, 128)
	return fmt.Errorf("%s %q is Not Supported by Anthropic-to-OpenAI CC conversion", kind, safeValue)
}

func ccRawJSONPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return true
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return value != nil
	}
}

func claudeMetadataUserID(raw json.RawMessage) (string, error) {
	if !ccRawJSONPresent(raw) {
		return "", nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return "", ccUnsupported("request field", "metadata")
	}
	var userID string
	if value, ok := metadata["user_id"]; ok {
		if err := json.Unmarshal(value, &userID); err != nil {
			return "", ccUnsupported("metadata field", "user_id")
		}
		delete(metadata, "user_id")
	}
	if len(metadata) > 0 {
		return "", ccUnsupported("metadata field", firstJSONMapKey(metadata))
	}
	return userID, nil
}

func firstJSONMapKey(values map[string]json.RawMessage) string {
	for key := range values {
		return key
	}
	return ""
}

func claudeOutputEffort(config *ClaudeOutputConfig) (string, error) {
	if config == nil {
		return "", nil
	}
	if ccRawJSONPresent(config.Format) {
		return "", ccUnsupported("output_config field", "format")
	}
	// Values stay provider-defined, but surrounding whitespace is never meaningful.
	return strings.TrimSpace(config.Effort), nil
}

func claudeServiceTierToOpenAI(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "auto":
		return "auto", nil
	case "standard_only":
		return "default", nil
	default:
		return "", ccUnsupported("service_tier", value)
	}
}

func claudeThinkingActive(config *ThinkingConfig) bool {
	return config != nil && strings.TrimSpace(config.Type) != "" &&
		!strings.EqualFold(strings.TrimSpace(config.Type), "disabled")
}

func claudeThinkingDisabled(config *ThinkingConfig) bool {
	return config != nil && strings.EqualFold(strings.TrimSpace(config.Type), "disabled")
}

func convertClaudeSystemContent(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("invalid Anthropic system content: %w", err)
	}
	var out strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			return "", ccUnsupported("system content block", block.Type)
		}
		out.WriteString(block.Text)
	}
	return out.String(), nil
}

func convertClaudeToolChoice(raw json.RawMessage, shortNames map[string]string) (any, *bool, error) {
	if !ccRawJSONPresent(raw) {
		return nil, nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil, fmt.Errorf("invalid Anthropic tool_choice: %w", err)
	}
	choiceType := ""
	var choice map[string]any
	switch v := value.(type) {
	case string:
		choiceType = v
	case map[string]any:
		choice = v
		choiceType, _ = v["type"].(string)
	default:
		return nil, nil, fmt.Errorf("invalid Anthropic tool_choice value")
	}

	var parallel *bool
	if rawDisabled, ok := choice["disable_parallel_tool_use"]; ok {
		disabled, ok := rawDisabled.(bool)
		if !ok {
			return nil, nil, fmt.Errorf("invalid disable_parallel_tool_use value")
		}
		enabled := !disabled
		parallel = &enabled
	}

	switch choiceType {
	case "auto":
		return "auto", parallel, nil
	case "any":
		return "required", parallel, nil
	case "none":
		return "none", nil, nil
	case "tool":
		name, _ := choice["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("Anthropic tool_choice type tool requires name")
		}
		if short, ok := shortNames[name]; ok {
			name = short
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]string{"name": name},
		}, parallel, nil
	default:
		return nil, nil, ccUnsupported("tool_choice type", choiceType)
	}
}

func claudeBlockToOpenAIUserPart(block ClaudeContentBlock) (map[string]any, error) {
	if block.Type == "text" {
		return map[string]any{"type": "text", "text": block.Text}, nil
	}
	var source claudeMediaSource
	if err := json.Unmarshal(block.Source, &source); err != nil {
		return nil, fmt.Errorf("invalid Anthropic %s source: %w", block.Type, err)
	}
	switch block.Type {
	case "image":
		var imageURL string
		switch source.Type {
		case "base64":
			if source.MediaType == "" || source.Data == "" {
				return nil, fmt.Errorf("Anthropic base64 image requires media_type and data")
			}
			imageURL = "data:" + source.MediaType + ";base64," + source.Data
		case "url":
			if source.URL == "" {
				return nil, fmt.Errorf("Anthropic URL image requires url")
			}
			imageURL = source.URL
		default:
			return nil, ccUnsupported("image source type", source.Type)
		}
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}}, nil
	case "document":
		switch source.Type {
		case "base64":
			if source.Data == "" {
				return nil, fmt.Errorf("Anthropic base64 document requires data")
			}
			filename := block.Title
			if filename == "" {
				filename = "document.pdf"
			}
			mediaType := source.MediaType
			if mediaType == "" {
				mediaType = "application/pdf"
			}
			// OpenAI file_data requires a data URL rather than Anthropic's raw base64 payload.
			fileData := source.Data
			if !strings.HasPrefix(fileData, "data:") {
				fileData = "data:" + mediaType + ";base64," + fileData
			}
			return map[string]any{
				"type": "file",
				"file": map[string]any{"file_data": fileData, "filename": filename},
			}, nil
		case "text":
			var text strings.Builder
			if block.Title != "" {
				text.WriteString("Title: ")
				text.WriteString(block.Title)
				text.WriteByte('\n')
			}
			if block.Context != "" {
				text.WriteString("Context: ")
				text.WriteString(block.Context)
				text.WriteByte('\n')
			}
			text.WriteString(source.Data)
			return map[string]any{"type": "text", "text": text.String()}, nil
		default:
			return nil, ccUnsupported("document source type", source.Type)
		}
	default:
		return nil, ccUnsupported("content block", block.Type)
	}
}

func marshalOpenAIUserContent(parts []map[string]any) (json.RawMessage, error) {
	allText := len(parts) > 0
	var text strings.Builder
	for _, part := range parts {
		if part["type"] != "text" {
			allText = false
			break
		}
		value, ok := part["text"].(string)
		if !ok {
			allText = false
			break
		}
		text.WriteString(value)
	}
	if allText {
		encoded, err := json.Marshal(text.String())
		return json.RawMessage(encoded), err
	}
	encoded, err := json.Marshal(parts)
	return json.RawMessage(encoded), err
}

func claudeToolResultContent(block ClaudeContentBlock) (string, error) {
	if len(block.Content) == 0 {
		if block.IsError {
			return `{"is_error":true,"content":""}`, nil
		}
		return "", nil
	}
	var value any
	if err := decodeCodexJSONUseNumber(block.Content, &value); err != nil {
		return "", fmt.Errorf("invalid Anthropic tool_result content: %w", err)
	}
	if block.IsError {
		value = map[string]any{"is_error": true, "content": value}
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("failed to encode Anthropic tool_result content: %w", err)
	}
	return string(encoded), nil
}

// convertClaudeToOpenAI converts a Claude request to OpenAI format.
// toolNameShortMap is used to apply shortened tool names for OpenAI's 64-char limit.
// Pass nil to disable tool name shortening (for third-party APIs without this limit).
func convertClaudeToOpenAI(claudeReq *ClaudeRequest, toolNameShortMap map[string]string) (*OpenAIRequest, error) {
	if len(claudeReq.unsupportedFields) > 0 {
		return nil, ccUnsupported("request field", claudeReq.unsupportedFields[0])
	}
	if ccRawJSONPresent(claudeReq.McpServers) {
		return nil, ccUnsupported("request field", "mcp_servers")
	}
	if ccRawJSONPresent(claudeReq.Container) {
		return nil, ccUnsupported("request field", "container")
	}
	if claudeReq.TopK != nil {
		return nil, ccUnsupported("request field", "top_k")
	}
	userID, err := claudeMetadataUserID(claudeReq.Metadata)
	if err != nil {
		return nil, err
	}
	effort, err := claudeOutputEffort(claudeReq.OutputConfig)
	if err != nil {
		return nil, err
	}
	thinkingActive := claudeThinkingActive(claudeReq.Thinking)
	serviceTier, err := claudeServiceTierToOpenAI(claudeReq.ServiceTier)
	if err != nil {
		return nil, err
	}

	openaiReq := &OpenAIRequest{
		Model:       claudeReq.Model,
		Stream:      claudeReq.Stream,
		Temperature: claudeReq.Temperature,
		TopP:        claudeReq.TopP,
		ServiceTier: serviceTier,
		User:        userID,
	}

	// Prefer MaxTokens; fall back to MaxTokensToSample for compatibility with
	// newer Claude APIs that use max_tokens_to_sample.
	effectiveMaxTokens := claudeReq.MaxTokens
	if !claudeReq.maxTokensSet && effectiveMaxTokens <= 0 && claudeReq.MaxTokensToSample > 0 {
		effectiveMaxTokens = claudeReq.MaxTokensToSample
	}
	if claudeReq.maxTokensSet || effectiveMaxTokens > 0 {
		openaiReq.MaxTokens = &effectiveMaxTokens
	}

	// Claude Code may (non-conformingly) place system prompts inside messages
	// with role "system". Merge those into the leading system message instead
	// of failing the conversion; OpenAI requires system to be the first
	// message when present.
	inlineSystem, nonSystemMessages, err := collectInlineClaudeSystemMessages(claudeReq.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Claude message: %w", err)
	}
	var convertedMessages []OpenAIMessage
	for _, msg := range nonSystemMessages {
		openaiMsg, err := convertClaudeMessageToOpenAI(msg, toolNameShortMap)
		if err != nil {
			return nil, fmt.Errorf("failed to convert Claude message: %w", err)
		}
		convertedMessages = append(convertedMessages, openaiMsg...)
	}

	// Convert system message
	systemContent := ""
	if len(claudeReq.System) > 0 {
		var err error
		systemContent, err = convertClaudeSystemContent(claudeReq.System)
		if err != nil {
			return nil, err
		}
	}
	if inlineSystem != "" {
		if systemContent != "" {
			systemContent += "\n\n" + inlineSystem
		} else {
			systemContent = inlineSystem
		}
	}

	messages := make([]OpenAIMessage, 0, len(convertedMessages)+1)
	if systemContent != "" {
		contentJSON := marshalStringAsJSONRaw("system", systemContent)
		messages = append(messages, OpenAIMessage{
			Role:    "system",
			Content: contentJSON,
		})
	}
	messages = append(messages, convertedMessages...)

	// Treat prompt as a single user message when no non-system messages are provided.
	if len(nonSystemMessages) == 0 && strings.TrimSpace(claudeReq.Prompt) != "" {
		promptText := strings.TrimSpace(claudeReq.Prompt)
		contentJSON := marshalStringAsJSONRaw("prompt", promptText)
		messages = append(messages, OpenAIMessage{
			Role:    "user",
			Content: contentJSON,
		})
	}

	// Some upstream providers (including GLM chat-completion) require that the
	// messages list does not consist of only system/assistant messages. As a
	// defensive fallback, ensure there is at least one user/assistant message.
	hasUserOrAssistant := false
	for _, m := range messages {
		if m.Role == "user" || m.Role == "assistant" {
			hasUserOrAssistant = true
			break
		}
	}
	if !hasUserOrAssistant && len(messages) > 0 {
		// Downgrade the first system message to a user message. This keeps the
		// overall instruction content while satisfying provider requirements.
		if messages[0].Role == "system" {
			messages[0].Role = "user"
			logrus.Warn("CC: Downgraded system message to user role (no user/assistant messages present)")
		}
	}

	openaiReq.Messages = messages

	// Convert tools with optional name shortening for OpenAI's 64-char limit
	if len(claudeReq.Tools) > 0 {
		tools := make([]OpenAITool, 0, len(claudeReq.Tools))
		for _, tool := range claudeReq.Tools {
			// Apply shortened name if available
			toolName := tool.Name
			if toolNameShortMap != nil {
				if short, ok := toolNameShortMap[tool.Name]; ok {
					toolName = short
				}
			}
			tools = append(tools, OpenAITool{
				Type: "function",
				Function: OpenAIFunction{
					Name:        toolName,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			})
		}
		openaiReq.Tools = tools
	}

	// Convert stop sequences.
	// For compatibility with OpenAI-style and z.ai chat-completion APIs, always
	// encode stop as an array of strings (even when there is only one element).
	if len(claudeReq.StopSequences) > 0 {
		stopBytes, err := json.Marshal(claudeReq.StopSequences)
		if err != nil {
			logrus.WithError(err).Warn("CC: Failed to marshal stop sequences, skipping")
		} else {
			openaiReq.Stop = stopBytes
		}
	}

	toolChoice, parallelToolCalls, err := convertClaudeToolChoice(claudeReq.ToolChoice, toolNameShortMap)
	if err != nil {
		return nil, err
	}
	openaiReq.ToolChoice = toolChoice
	openaiReq.ParallelToolCalls = parallelToolCalls

	// Explicit effort only changes protocol field location. A token budget has no
	// lossless equivalent and must not be guessed into an effort level.
	// Explicitly disabled thinking takes precedence over a conflicting effort
	// value; forwarding both can re-enable reasoning or yield 400 upstream.
	if claudeThinkingDisabled(claudeReq.Thinking) {
		// Do not gate explicit disable by model name: upstream model sets are dynamic,
		// while omitting the field can silently restore the provider's reasoning default.
		openaiReq.ReasoningEffort = "none"
	} else if effort != "" {
		openaiReq.ReasoningEffort = effort
	}
	if thinkingActive {
		logrus.WithFields(logrus.Fields{
			"reasoning_effort": openaiReq.ReasoningEffort,
		}).Debug("CC: Preserved explicit reasoning effort for thinking mode")
	}

	return openaiReq, nil
}

// claudeMessageSystemText extracts the text of a Claude message that carries
// role "system" (a non-conforming but real-world placement of system prompts
// used by Claude Code). Non-text blocks are skipped; returns "" when empty.
func claudeMessageSystemText(msg ClaudeMessage) (string, error) {
	var contentStr string
	if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
		return contentStr, nil
	}
	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return "", fmt.Errorf("failed to parse content blocks: %w", err)
	}
	var textParts []string
	for _, block := range blocks {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
			continue
		}
		// Inline system messages are a non-conforming Claude Code placement and
		// stay tolerant: unlike the top-level system field (convertClaudeSystemContent,
		// which fails closed on non-text blocks), the dropped block is only logged
		// so real-world traffic is not rejected while the loss stays observable.
		logrus.WithField("block_type", block.Type).
			Warn("CC: Dropped non-text block from inline system message")
	}
	return strings.Join(textParts, "\n"), nil
}

// collectInlineClaudeSystemMessages keeps non-system message order while
// centralizing the non-conforming Claude Code system-message merge policy.
func collectInlineClaudeSystemMessages(messages []ClaudeMessage) (string, []ClaudeMessage, error) {
	inlineSystemParts := make([]string, 0)
	nonSystemMessages := messages
	foundSystem := false
	for i, msg := range messages {
		if msg.Role != "system" {
			if foundSystem {
				nonSystemMessages = append(nonSystemMessages, msg)
			}
			continue
		}
		if !foundSystem {
			foundSystem = true
			nonSystemMessages = make([]ClaudeMessage, 0, len(messages)-1)
			nonSystemMessages = append(nonSystemMessages, messages[:i]...)
		}
		text, err := claudeMessageSystemText(msg)
		if err != nil {
			return "", nil, err
		}
		if text != "" {
			inlineSystemParts = append(inlineSystemParts, text)
		}
	}
	return strings.Join(inlineSystemParts, "\n\n"), nonSystemMessages, nil
}

// convertClaudeMessageToOpenAI converts a single Claude message to OpenAI format.
// toolNameShortMap is used to apply shortened tool names for historical tool_use blocks.
func convertClaudeMessageToOpenAI(msg ClaudeMessage, toolNameShortMap map[string]string) ([]OpenAIMessage, error) {
	if msg.Role != "user" && msg.Role != "assistant" {
		return nil, fmt.Errorf("invalid Anthropic message role %q", msg.Role)
	}

	// Try to parse content as string first
	var contentStr string
	if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
		contentJSON := marshalStringAsJSONRaw("message_text", contentStr)
		return []OpenAIMessage{{
			Role:    msg.Role,
			Content: contentJSON,
		}}, nil
	}

	// Parse content as array of blocks
	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, fmt.Errorf("failed to parse content blocks: %w", err)
	}

	if msg.Role == "assistant" {
		var textParts, thinkingParts []string
		var toolCalls []OpenAIToolCall
		for _, block := range blocks {
			switch {
			case block.Type == "text":
				textParts = append(textParts, block.Text)
			case block.Type == "thinking":
				// The target has no signature field; preserve visible thinking text and drop the opaque signature.
				if block.Thinking != "" {
					thinkingParts = append(thinkingParts, block.Thinking)
				}
			case isClaudeToolUseBlock(block):
				if block.ID == "" || block.Name == "" {
					return nil, fmt.Errorf("Anthropic tool_use requires id and name")
				}
				toolName := block.Name
				if short, ok := toolNameShortMap[block.Name]; ok {
					toolName = short
				}
				arguments := string(block.Input)
				if strings.TrimSpace(arguments) == "" || strings.TrimSpace(arguments) == "null" {
					arguments = "{}"
				}
				var input any
				if err := decodeCodexJSONUseNumber([]byte(arguments), &input); err != nil {
					return nil, fmt.Errorf("Anthropic tool_use input must be valid JSON: %w", err)
				}
				toolCalls = append(toolCalls, OpenAIToolCall{
					ID: block.ID, Type: "function",
					Function: OpenAIFunctionCall{Name: toolName, Arguments: arguments},
				})
			case isClaudeToolResultBlock(block):
				// Anthropic only allows tool_result blocks in user messages, so an
				// assistant-role tool result is non-conformant input with no OpenAI
				// equivalent. Reject it explicitly instead of the generic default,
				// keeping the same policy as the Codex converter.
				return nil, ccUnsupported("content block in assistant message", block.Type)
			default:
				return nil, ccUnsupported("content block", block.Type)
			}
		}

		assistantMsg := OpenAIMessage{Role: "assistant"}
		if len(textParts) > 0 {
			combined := strings.Join(textParts, "")
			assistantMsg.Content = marshalStringAsJSONRaw("assistant_delta", combined)
		}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = toolCalls
		}
		// Convert thinking blocks back to reasoning_content for DeepSeek reasoner models.
		// Per DeepSeek API docs: "reasoning_content must be passed back to the API in subsequent turns"
		// See: https://api-docs.deepseek.com/guides/thinking_mode
		if len(thinkingParts) > 0 {
			combined := strings.Join(thinkingParts, "\n")
			assistantMsg.ReasoningContent = &combined
		}
		if assistantMsg.Content != nil || len(assistantMsg.ToolCalls) > 0 || assistantMsg.ReasoningContent != nil {
			return []OpenAIMessage{assistantMsg}, nil
		}
		return nil, nil
	}

	var result []OpenAIMessage
	var userParts []map[string]any
	var toolMessages []OpenAIMessage
	flushUser := func() error {
		if len(userParts) == 0 {
			return nil
		}
		content, err := marshalOpenAIUserContent(userParts)
		if err != nil {
			return fmt.Errorf("failed to encode Anthropic user content: %w", err)
		}
		result = append(result, OpenAIMessage{Role: "user", Content: content})
		userParts = nil
		return nil
	}
	for _, block := range blocks {
		switch {
		case block.Type == "text" || block.Type == "image" || block.Type == "document":
			part, err := claudeBlockToOpenAIUserPart(block)
			if err != nil {
				return nil, err
			}
			userParts = append(userParts, part)
		case isClaudeToolResultBlock(block):
			if block.ToolUseID == "" {
				return nil, fmt.Errorf("Anthropic tool_result requires tool_use_id")
			}
			content, err := claudeToolResultContent(block)
			if err != nil {
				return nil, err
			}
			toolMessages = append(toolMessages, OpenAIMessage{
				Role: "tool", Content: marshalStringAsJSONRaw("tool_result", content), ToolCallID: block.ToolUseID,
			})
		default:
			return nil, ccUnsupported("content block", block.Type)
		}
	}
	result = append(result, toolMessages...)
	if err := flushUser(); err != nil {
		return nil, err
	}
	return result, nil
}

func isClaudeToolUseBlock(block ClaudeContentBlock) bool {
	if block.Type == "tool_use" {
		return true
	}
	// Suffix forms cover Anthropic namespaced variants (server_tool_use,
	// future_tool_call). Substring matching would also capture unrelated
	// error-qualified variants (e.g. mcp_tool_use_error) that must fall
	// through to the unsupported-block handling.
	return strings.HasSuffix(block.Type, "_tool_use") || strings.HasSuffix(block.Type, "_tool_call")
}

func isClaudeToolResultBlock(block ClaudeContentBlock) bool {
	if block.Type == "tool_result" {
		return true
	}
	// Same policy as isClaudeToolUseBlock: accept namespaced suffix forms
	// (web_search_tool_result, code_execution_tool_result) but not their
	// error variants (*_tool_result_error).
	return strings.HasSuffix(block.Type, "_tool_result") || strings.HasSuffix(block.Type, "_tool_output")
}

// getThinkingModel returns the thinking model configured for the group.
// Returns empty string if not configured.
func getThinkingModel(group *models.Group) string {
	if group == nil || group.Config == nil {
		return ""
	}

	raw, ok := group.Config["thinking_model"]
	if !ok || raw == nil {
		return ""
	}

	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// applyCCRequestConversionDirect converts Claude request to OpenAI format directly.
// This function does not check the path, assuming the caller has already verified
// that this is a Claude messages endpoint. Used when path has been pre-rewritten.
// When thinking mode is enabled, the model will be set to the source model from
// redirect rules (if available) to allow Claude Code to find thinking-capable models.
func (ps *ProxyServer) applyCCRequestConversionDirect(
	c *gin.Context,
	group *models.Group,
	bodyBytes []byte,
) ([]byte, bool, error) {
	// Parse Claude request
	var claudeReq ClaudeRequest
	if err := json.Unmarshal(bodyBytes, &claudeReq); err != nil {
		return bodyBytes, false, fmt.Errorf("failed to parse Claude request: %w", err)
	}

	// Store original model for logging
	originalModel := claudeReq.Model

	// Preserve any existing original_model (from model mapping) so
	// MappedModel logging continues to work. Only set it when absent.
	if originalModel != "" {
		setModelRedirectContext(c, originalModel, -1, true)
	}

	// Apply model redirect rules for OpenAI CC mode
	// Pass both V1 and V2 maps for backward compatibility with un-migrated groups
	// This ensures the model name in the request body matches the redirect configuration
	if originalModel != "" {
		// Check if either V1 or V2 redirect maps are configured to support both legacy and new formats
		if len(group.ModelRedirectMapV2) > 0 || len(group.ModelRedirectMap) > 0 {
			targetModel, ruleVersion, targetCount, selectedIdx, err := models.ResolveTargetModelWithIndex(
				originalModel,
				group.ModelRedirectMap, // Pass V1 map for backward compatibility
				group.ModelRedirectMapV2,
				getModelRedirectSelector(),
			)
			if err != nil {
				return bodyBytes, false, fmt.Errorf("failed to resolve target model: %w", err)
			}
			if targetModel != "" && targetModel != originalModel {
				claudeReq.Model = targetModel
				setModelRedirectContext(c, originalModel, selectedIdx, true)

				// Log with additional context for V2 multi-target rules
				logFields := logrus.Fields{
					"group":          group.Name,
					"original_model": originalModel,
					"target_model":   targetModel,
				}

				// Add selection details for V2 rules to help debug distribution
				if ruleVersion == "v2" && targetCount > 1 {
					logFields["target_count"] = targetCount
					logFields["target_index"] = selectedIdx
					if rule, found := group.ModelRedirectMapV2[originalModel]; found && selectedIdx >= 0 && selectedIdx < len(rule.Targets) {
						logFields["target_weight"] = rule.Targets[selectedIdx].GetWeight()
					}
				}

				logrus.WithFields(logFields).Debug("CC: Applied model redirect")
			} else if targetModel == "" && group.ModelRedirectStrict {
				// Strict mode: model not found in redirect rules
				return bodyBytes, false, fmt.Errorf("model '%s' is not configured in redirect rules", originalModel)
			}
		} else if group.ModelRedirectStrict {
			// Strict mode with no redirect rules configured
			return bodyBytes, false, fmt.Errorf("model '%s' is not configured in redirect rules (no rules defined)", originalModel)
		}
	}

	// Auto-select thinking model when thinking mode is enabled
	// This allows Claude Code to automatically use thinking-capable models
	// (like deepseek-reasoner) when the user enables extended thinking.
	// Each group can configure its own thinking_model in the group config.
	// AI REVIEW NOTE: Suggestion to validate thinking model against a supported list was considered.
	// This is intentionally NOT implemented because:
	// 1. Model names are dynamically configured by users and vary across providers
	// 2. New models are released frequently; hardcoding a list would require constant updates
	// 3. Invalid model names will be rejected by the upstream API with clear error messages
	// 4. Users have full control over their group configuration
	thinkingModelApplied := false
	if claudeReq.Thinking != nil && strings.EqualFold(claudeReq.Thinking.Type, "enabled") {
		thinkingModel := getThinkingModel(group)
		if thinkingModel != "" && thinkingModel != claudeReq.Model {
			logrus.WithFields(logrus.Fields{
				"group":          group.Name,
				"original_model": claudeReq.Model,
				"thinking_model": thinkingModel,
				"budget_tokens":  claudeReq.Thinking.BudgetTokens,
			}).Info("CC: Auto-selecting thinking model for extended thinking")
			claudeReq.Model = thinkingModel
			thinkingModelApplied = true
			c.Set("thinking_model_applied", true)
			// NOTE: c.Set("thinking_model", thinkingModel) removed per AI review.
			// Only thinking_model_applied is used by downstream handlers (function_call.go).
		}
	}

	// Build tool name short map for tools that exceed the 64 char limit.
	// This is controlled by the "shorten_tool_names" config option (default: true).
	// Set to false for third-party OpenAI-compatible APIs without this limit.
	var toolNameShortMap map[string]string
	if len(claudeReq.Tools) > 0 && isShortenToolNamesEnabled(group) {
		names := make([]string, 0, len(claudeReq.Tools)+1)
		for _, tool := range claudeReq.Tools {
			names = append(names, tool.Name)
		}
		// Per AI review: also include tool_choice name if present, in case it's not in Tools.
		// This prevents tool_choice from bypassing shortening and exceeding upstream limits.
		if len(claudeReq.ToolChoice) > 0 {
			var tc map[string]any
			if err := json.Unmarshal(claudeReq.ToolChoice, &tc); err == nil {
				if t, _ := tc["type"].(string); t == "tool" {
					if n, _ := tc["name"].(string); n != "" {
						names = append(names, n)
					}
				}
			}
		}
		toolNameShortMap = buildToolNameShortMap(names)
		// Store reverse map in context for response conversion
		reverseMap := buildReverseToolNameMap(toolNameShortMap)
		c.Set(ctxKeyOpenAIToolNameReverseMap, reverseMap)
	}

	// Convert to OpenAI format with tool name shortening
	openaiReq, err := convertClaudeToOpenAI(&claudeReq, toolNameShortMap)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to convert Claude to OpenAI: %w", err)
	}

	// Marshal OpenAI request
	convertedBody, err := json.Marshal(openaiReq)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	// Log thinking model application
	if thinkingModelApplied {
		logrus.WithFields(logrus.Fields{
			"group":          group.Name,
			"original_model": originalModel,
			"final_model":    claudeReq.Model,
		}).Debug("CC: Thinking model applied to request")
	}

	// Optionally log request conversion info when body logging is enabled.
	// Reduced logging to avoid excessive output in production
	if group != nil && group.EffectiveConfig.EnableRequestBodyLogging && logrus.IsLevelEnabled(logrus.DebugLevel) {
		// Check if mcp_servers is actually present (not just empty json.RawMessage)
		hasMcpServers := false
		if len(claudeReq.McpServers) > 0 {
			raw := strings.TrimSpace(string(claudeReq.McpServers))
			hasMcpServers = raw != "" && raw != "null"
		}
		logrus.WithFields(logrus.Fields{
			"group":           group.Name,
			"original_model":  originalModel,
			"tools_count":     len(claudeReq.Tools),
			"has_mcp_servers": hasMcpServers,
		}).Debug("CC: Request conversion completed")
	}

	// Mark CC conversion as enabled
	c.Set(ctxKeyCCEnabled, true)
	c.Set(ctxKeyOriginalFormat, "claude")

	groupName := "unknown"
	if group != nil {
		groupName = group.Name
	}

	logrus.WithFields(logrus.Fields{
		"group":          groupName,
		"original_model": originalModel,
		"stream":         claudeReq.Stream,
		"tools_count":    len(claudeReq.Tools),
	}).Debug("CC: Converted Claude request to OpenAI format")

	return convertedBody, true, nil
}

// OpenAIResponse represents an OpenAI API response.
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
	Error   *OpenAIError   `json:"error,omitempty"`
}

// OpenAIError represents an error in OpenAI response.
type OpenAIError struct {
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Param   interface{} `json:"param"`
	Code    interface{} `json:"code"`
}

// ClaudeErrorResponse represents a Claude error response.
type ClaudeErrorResponse struct {
	Type  string      `json:"type"`
	Error ClaudeError `json:"error"`
}

// ClaudeError represents a Claude error.
type ClaudeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// mapStatusToClaudeErrorType maps HTTP status codes to Claude error types.
// This centralizes the mapping logic to avoid duplication across error handlers.
func mapStatusToClaudeErrorType(statusCode int) string {
	switch {
	case statusCode == 400:
		return "invalid_request_error"
	case statusCode == 401:
		return "authentication_error"
	case statusCode == 403:
		return "permission_error"
	case statusCode == 404:
		return "not_found_error"
	case statusCode == 429:
		return "rate_limit_error"
	case statusCode == 504:
		return "timeout_error"
	case statusCode == 502 || statusCode == 503 || statusCode == 529:
		// Anthropic documents 529 as overloaded_error; keep proxy-generated errors protocol-correct.
		return "overloaded_error"
	case statusCode >= 500 && statusCode < 600:
		return "api_error"
	default:
		return "api_error"
	}
}

func apiErrorTypeToClaudeErrorType(errorType string) string {
	switch errorType {
	case "invalid_request_error":
		return "invalid_request_error"
	case "authentication_error":
		return "authentication_error"
	case "permission_error":
		return "permission_error"
	case "not_found_error":
		return "not_found_error"
	case "rate_limit_error":
		return "rate_limit_error"
	case "overloaded_error":
		return "overloaded_error"
	case "timeout_error":
		return "timeout_error"
	case "server_error", "internal_error":
		return "api_error"
	default:
		return "api_error"
	}
}

// returnClaudeError sends a Claude-formatted error response.
// This is used when CC mode is enabled to ensure Claude Code clients
// can properly parse and display error messages from upstream.
// Per AI review: clears upstream encoding headers and sanitizes message to prevent credential leakage.
func returnClaudeError(c *gin.Context, statusCode int, message string) {
	// Clear upstream encoding headers to avoid mismatches with rewritten body
	clearUpstreamEncodingHeaders(c)

	// Sanitize message to prevent leaking sensitive data (API keys, tokens, etc.)
	message = strings.TrimSpace(utils.SanitizeErrorBody(message))
	if message == "" {
		message = fmt.Sprintf("Upstream returned status %d", statusCode)
	}

	claudeErr := ClaudeErrorResponse{
		Type: "error",
		Error: ClaudeError{
			Type:    mapStatusToClaudeErrorType(statusCode),
			Message: message,
		},
	}
	// Record first byte at the client delivery point; the helper marks only
	// after a successful non-empty write, so a failed response write leaves
	// first_byte_duration_ms NULL.
	writeJSONMarkingFirstByte(c, statusCode, claudeErr)
}

// OpenAIChoice represents a choice in OpenAI response.
type OpenAIChoice struct {
	Index        int                `json:"index"`
	Message      *OpenAIRespMessage `json:"message,omitempty"`
	Delta        *OpenAIRespMessage `json:"delta,omitempty"`
	FinishReason *string            `json:"finish_reason,omitempty"`
	StopSequence *string            `json:"stop_sequence,omitempty"`
}

// OpenAIRespMessage represents a message in OpenAI response.
type OpenAIRespMessage struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"` // DeepSeek reasoner thinking content
}

// OpenAIUsage represents usage info in OpenAI response.
type OpenAIUsage struct {
	PromptTokens            int                `json:"prompt_tokens"`
	CompletionTokens        int                `json:"completion_tokens"`
	TotalTokens             int                `json:"total_tokens"`
	PromptTokensDetails     *TokenUsageDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *TokenUsageDetails `json:"completion_tokens_details,omitempty"`
}

// ClaudeResponse represents a Claude API response.
type ClaudeResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []ClaudeContentBlock `json:"content"`
	Model        string               `json:"model"`
	StopReason   *string              `json:"stop_reason,omitempty"`
	StopSequence *string              `json:"stop_sequence,omitempty"`
	Usage        *ClaudeUsage         `json:"usage,omitempty"`
	Error        *ClaudeError         `json:"error,omitempty"`
}

// ClaudeUsage represents usage info in Claude response.
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	ThinkingTokens           int `json:"thinking_tokens,omitempty"`
}

// convertOpenAIToClaudeResponse converts OpenAI response to Claude format.
// When normalizeToolArgs is true, tool call arguments are normalized (JSON parsed and re-serialized).
// When false, arguments are passed through unchanged to preserve upstream formatting.
// reverseToolNameMap is used to restore original tool names that were shortened.
func convertOpenAIToClaudeResponse(openaiResp *OpenAIResponse, cleanupMode functionCallCleanupMode, normalizeToolArgs bool, reverseToolNameMap map[string]string) *ClaudeResponse {
	claudeResp := &ClaudeResponse{
		ID:      openaiResp.ID,
		Type:    "message",
		Role:    "assistant",
		Model:   openaiResp.Model,
		Content: make([]ClaudeContentBlock, 0),
	}

	if len(openaiResp.Choices) > 0 {
		choice := openaiResp.Choices[0]
		msg := choice.Message
		if msg == nil {
			msg = choice.Delta
		}

		if msg != nil {
			var content []ClaudeContentBlock

			// Handle reasoning_content from DeepSeek reasoner models (non-streaming).
			// This is emitted as thinking content in Claude format.
			// CRITICAL: Apply removeFunctionCallsBlocks to clean malformed XML/JSON
			// that may leak into thinking content (same as streaming mode).
			if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
				thinking := removeFunctionCallsBlocks(strings.TrimSpace(*msg.ReasoningContent), cleanupMode)
				if thinking != "" {
					content = append(content, ClaudeContentBlock{
						Type:     "thinking",
						Thinking: thinking,
					})
				}
			}

			// Add text and thinking content
			if msg.Content != nil && *msg.Content != "" {
				content = append(content, splitThinkingContent(*msg.Content, cleanupMode)...)
			}

			// Add tool_use blocks
			// NOTE: Skip tool_calls with empty arguments to avoid Claude Code errors.
			// Some upstream models (e.g., deepseek-reasoner in thinking mode) may return
			// tool_calls with empty arguments as placeholders during reasoning phase.
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					continue
				}
				// Restore original tool name if it was shortened
				toolName := tc.Function.Name
				if reverseToolNameMap != nil {
					if orig, ok := reverseToolNameMap[tc.Function.Name]; ok {
						toolName = orig
					}
				}
				// Validate arguments before conversion - skip empty/placeholder tool_calls
				if !isValidToolCallArguments(toolName, tc.Function.Arguments) {
					continue
				}
				inputJSON := json.RawMessage("{}")
				if tc.Function.Arguments != "" {
					argsStr := tc.Function.Arguments
					// When normalizeToolArgs is true (force FC enabled), normalize arguments
					// to fix potential issues like Windows path escapes and tool-specific formatting.
					// When false (only CC support), pass through arguments unchanged.
					if normalizeToolArgs {
						if normalized, ok := normalizeOpenAIToolCallArguments(toolName, argsStr); ok {
							argsStr = normalized
						}
					}
					// CRITICAL: Fix for Claude Code Windows path escape issue in Bash commands.
					// Claude Code client performs additional escape processing on Bash command strings,
					// which corrupts Windows paths. We double-escape backslashes ONLY in the "command"
					// field to compensate for this.
					// See: https://github.com/anthropics/claude-code/issues/15290
					argsStr = doubleEscapeWindowsPathsForBash(argsStr)
					inputJSON = json.RawMessage(argsStr)
				}
				content = append(content, ClaudeContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  toolName,
					Input: inputJSON,
				})
			}

			claudeResp.Content = content
		}

		// Convert finish reason
		if choice.FinishReason != nil {
			stopReason, isError := convertFinishReasonToStopReason(*choice.FinishReason)
			if isError {
				logrus.WithField("finish_reason", *choice.FinishReason).
					Warn("CC: Upstream returned error finish_reason")
			}
			// If upstream says tool_calls but we didn't receive any valid tool calls,
			// convert to end_turn to prevent Claude Code from hanging waiting for tool results
			hasToolUseBlocks := false
			for _, block := range claudeResp.Content {
				if block.Type == "tool_use" && block.ID != "" {
					hasToolUseBlocks = true
					break
				}
			}
			if *choice.FinishReason == "tool_calls" && !hasToolUseBlocks {
				logrus.WithField("original_finish_reason", *choice.FinishReason).
					Warn("CC: Received tool_calls finish_reason but no valid tool_use blocks, converting to end_turn")
				stopReason = "end_turn"
			}
			claudeResp.StopReason = &stopReason
		}
		claudeResp.StopSequence = choice.StopSequence
	}

	// Convert usage - always provide usage to satisfy Claude client requirements
	if openaiResp.Usage != nil {
		claudeResp.Usage = &ClaudeUsage{
			InputTokens:  openaiResp.Usage.PromptTokens,
			OutputTokens: openaiResp.Usage.CompletionTokens,
		}
		if details := openaiResp.Usage.PromptTokensDetails; details != nil {
			claudeResp.Usage.CacheReadInputTokens = details.CachedTokens
			claudeResp.Usage.CacheCreationInputTokens = details.CacheWriteTokens
		}
		if details := openaiResp.Usage.CompletionTokensDetails; details != nil {
			claudeResp.Usage.ThinkingTokens = details.ReasoningTokens
		}
	} else {
		// Provide default usage if not available from OpenAI
		claudeResp.Usage = &ClaudeUsage{
			InputTokens:  0,
			OutputTokens: 0,
		}
	}
	applyTokenMultiplier(claudeResp.Usage)

	return claudeResp
}

func splitThinkingContent(content string, cleanupMode functionCallCleanupMode) []ClaudeContentBlock {
	if content == "" {
		return nil
	}

	parser := NewThinkingParser()
	for _, r := range content {
		parser.FeedRune(r)
	}
	parser.FlushText()
	parser.Finish()

	events := parser.ConsumeEvents()
	blocks := make([]ClaudeContentBlock, 0, len(events))
	for _, evt := range events {
		switch evt.Type {
		case "thinking":
			thinking := removeFunctionCallsBlocks(strings.TrimSpace(evt.Content), cleanupMode)
			if thinking == "" {
				continue
			}
			// Convert Windows paths to Unix-style for Claude Code compatibility
			thinking = convertWindowsPathsInToolResult(thinking)
			blocks = append(blocks, ClaudeContentBlock{Type: "thinking", Thinking: thinking})
		case "text":
			orig := evt.Content
			leading, core, trailing := trimWhitespacePreserving(orig)
			cleanedCore := removeFunctionCallsBlocks(core, cleanupMode)
			text := leading + cleanedCore + trailing
			if text == "" {
				continue
			}
			// Convert Windows paths to Unix-style for Claude Code compatibility
			text = convertWindowsPathsInToolResult(text)
			blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: text})
		}
	}
	return blocks
}

// trimWhitespacePreserving returns the leading whitespace, trimmed core, and trailing whitespace.
func trimWhitespacePreserving(s string) (string, string, string) {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}

	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}

	return s[:start], s[start:end], s[end:]
}

// convertFinishReasonToStopReason converts OpenAI finish_reason to Claude stop_reason.
// Returns the stop_reason and a boolean indicating if the finish_reason represents an error.
func convertFinishReasonToStopReason(finishReason string) (string, bool) {
	switch finishReason {
	case "stop":
		return "end_turn", false
	case "length":
		return "max_tokens", false
	case "tool_calls", "function_call":
		return "tool_use", false
	case "stop_sequence", "pause_turn", "refusal", "model_context_window_exceeded":
		return finishReason, false
	case "content_filter":
		return "refusal", false
	case "context_length_exceeded":
		return "model_context_window_exceeded", false
	case "network_error", "error", "timeout":
		// These are error conditions that should be reported to the client
		return "end_turn", true
	default:
		return "end_turn", false
	}
}

func parseTokenMultiplier(raw string) float64 {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 1
	}

	s = strings.TrimPrefix(s, "x")
	isPercent := strings.HasSuffix(s, "%")
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSuffix(s, "x")
	if s == "" {
		return 1
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 1
	}
	if isPercent {
		v = v / 100
		if v <= 0 {
			return 1
		}
	}
	return v
}

func getTokenMultiplier() float64 {
	return parseTokenMultiplier(os.Getenv("TOKEN_MULTIPLIER"))
}

func applyTokenMultiplier(usage *ClaudeUsage) {
	if usage == nil {
		return
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
		return
	}
	if usage.OutputTokens == 0 {
		// Preserve genuine zero-usage responses.
		return
	}

	multiplier := getTokenMultiplier()
	raw := float64(usage.OutputTokens) * multiplier
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		// Handle numeric edge cases from invalid multipliers by preserving the
		// upstream count.
		return
	}
	if raw <= 0 {
		// This should not happen when OutputTokens > 0 and multiplier > 0.
		return
	}

	// Ceil() guarantees >= 1 for any positive raw.
	usage.OutputTokens = int(math.Ceil(raw))
}

func normalizeArgsGenericInPlace(args map[string]any) {
	if args == nil {
		return
	}

	// First, convert Git Bash/MSYS2 Unix-style paths to Windows format
	// This must be done before other normalizations to ensure consistent path format
	// Example: /f/MyProjects/test/file.py -> F:\MyProjects\test\file.py
	fixGitBashPathsInArgs(args)

	for key, val := range args {
		strVal, ok := val.(string)
		if !ok {
			continue
		}
		trimmedStr := strings.TrimSpace(strVal)
		if trimmedStr == "" {
			continue
		}

		// Fix Windows file paths where JSON escape sequences were incorrectly interpreted.
		// When upstream models return paths like "F:\MyProjects\test\file.py", the JSON parser
		// interprets \t as tab, \n as newline, etc.
		//
		// We apply fixes in two scenarios:
		// 1. Path-like keys (path, file, dir, etc.): fix the entire value
		// 2. Any string containing embedded Windows paths: fix paths within the string
		//
		// This auto-extends to any tool/parameter that may contain Windows paths,
		// including git commands, custom tools, etc.
		if containsControlChars(strVal) {
			if isPathLikeKey(key) && looksLikeWindowsPath(strVal) {
				// Scenario 1: The entire value is a path
				strVal = fixWindowsPathEscapes(strVal)
				args[key] = strVal
				trimmedStr = strings.TrimSpace(strVal)
			} else if containsWindowsDrivePath(strVal) {
				// Scenario 2: The value contains embedded Windows paths
				// This handles commands like "python F:\path\file.py" or "git clone C:\repo"
				strVal = fixEmbeddedWindowsPathsInCommand(strVal)
				args[key] = strVal
				trimmedStr = strings.TrimSpace(strVal)
			}
		}

		if (strings.HasPrefix(trimmedStr, "{") && strings.HasSuffix(trimmedStr, "}")) ||
			(strings.HasPrefix(trimmedStr, "[") && strings.HasSuffix(trimmedStr, "]")) {
			var jsonVal any
			if err := utils.UnmarshalJSONUseNumber([]byte(strVal), &jsonVal); err == nil {
				args[key] = jsonVal
				continue
			}
		}

		// NOTE: The following code was removed because it incorrectly replaced literal
		// backslash-n sequences in Windows paths. For example, "F:\new\file.py" would
		// have "\n" replaced with a newline character, corrupting the path.
		// The Windows path fix above handles the case where JSON escape sequences
		// were incorrectly interpreted (actual control characters in the string).
	}
}

// isPathLikeKey returns true if the key name suggests it contains a file path.
func isPathLikeKey(key string) bool {
	lowerKey := strings.ToLower(key)
	return strings.Contains(lowerKey, "path") ||
		strings.Contains(lowerKey, "file") ||
		strings.Contains(lowerKey, "dir") ||
		lowerKey == "cwd" ||
		lowerKey == "root" ||
		lowerKey == "location"
}

// containsControlChars returns true if the string contains control characters
// that might have been incorrectly interpreted from JSON escape sequences.
func containsControlChars(s string) bool {
	for _, r := range s {
		// Check for common control characters that result from JSON escape interpretation:
		// \t (tab), \n (newline), \r (carriage return), \b (backspace), \f (form feed)
		if r == '\t' || r == '\n' || r == '\r' || r == '\b' || r == '\f' {
			return true
		}
	}
	return false
}

// looksLikeWindowsPath returns true if the string looks like a Windows file path.
// Checks for drive letter pattern (e.g., "C:", "F:") or backslash presence.
func looksLikeWindowsPath(s string) bool {
	if len(s) < 2 {
		return false
	}
	// Check for drive letter pattern: letter followed by colon
	firstChar := s[0]
	if (firstChar >= 'A' && firstChar <= 'Z') || (firstChar >= 'a' && firstChar <= 'z') {
		if s[1] == ':' {
			return true
		}
	}
	// Also check if it contains backslashes (even after some were converted to control chars)
	return strings.Contains(s, "\\")
}

// reGitBashPath matches Git Bash/MSYS2 Unix-style paths like /c/, /f/, /d/
// These paths need to be converted to Windows format (C:\, F:\, D:\)
// Pattern: starts with /, followed by single letter, followed by /
// This distinguishes Git Bash paths from real Unix paths like /home/, /usr/, /var/
var reGitBashPath = regexp.MustCompile(`^/([a-zA-Z])/`)

// reGitBashPathInCommand matches Git Bash paths embedded in command strings
// Pattern matches /[a-z]/ followed by path components (non-whitespace, non-quote)
var reGitBashPathInCommand = regexp.MustCompile(`/([a-zA-Z])/[^\s"']+`)

// reWindowsPathNormal matches normal Windows paths with backslashes (C:\path\file.txt)
var reWindowsPathNormal = regexp.MustCompile(`[A-Za-z]:\\[^\s\n\r"'<>|]+`)

// reWindowsPathCorrupted matches Windows paths where backslashes may have been corrupted
// This happens when JSON escape sequences like \t, \n are interpreted as tab, newline, etc.
// Pattern matches drive letter + colon + any characters except space and special chars
// NOTE: This pattern intentionally matches control characters (tab, newline, etc.) to catch corrupted paths
// We use [^ ] (not space) instead of \s (not whitespace) to allow tabs and other control chars
var reWindowsPathCorrupted = regexp.MustCompile(`[A-Za-z]:[^ "'<>|]+`)

// reWindowsDrivePath matches Windows drive letter paths (e.g., "C:", "F:")
// This pattern finds the start of a Windows path within a command string.
// The path may contain control characters where backslashes were incorrectly interpreted.
var reWindowsDrivePath = regexp.MustCompile(`[A-Za-z]:`)

// reWindowsPathBackslash matches ALL backslashes in Windows paths for double-escaping.
// When we detect a Windows drive path (C:, F:, etc.), ALL backslashes in the command
// are path separators and should be doubled, even if they look like escape sequences.
// Examples:
//   - F:\test\file.py - the \t and \f are path separators, not escape sequences
//   - C:\new\readme.txt - the \n and \r are path separators, not escape sequences
//
// Pattern: Match any single backslash (not already doubled)
// Strategy: Match \ that is NOT followed by another \ (to avoid matching already-doubled \\)
var reWindowsPathBackslash = regexp.MustCompile(`\\(?:[^\\]|$)`)

// isLikelyGitBashPath returns true if the path looks like a Git Bash/MSYS2 path
// Git Bash paths have the pattern /[single-letter]/ (e.g., /c/, /f/, /d/)
// This is distinct from Unix paths which typically start with /home/, /usr/, /var/, /etc/
func isLikelyGitBashPath(s string) bool {
	if !strings.HasPrefix(s, "/") || len(s) < 3 {
		return false
	}

	// Check for Git Bash pattern: /[letter]/
	if !reGitBashPath.MatchString(s) {
		return false
	}

	// Additional validation: exclude common Unix paths that might accidentally match
	// Common Unix paths: /bin, /dev, /etc, /lib, /opt, /proc, /run, /sbin, /srv, /sys, /tmp, /usr, /var
	// Also: /home, /root, /mnt, /media
	secondPart := ""
	if len(s) > 3 {
		endIdx := strings.IndexByte(s[3:], '/')
		if endIdx == -1 {
			secondPart = s[3:]
		} else {
			secondPart = s[3 : 3+endIdx]
		}
	}

	// If the second part is a common Unix directory name, it's likely a Unix path, not Git Bash
	unixDirs := []string{"bin", "dev", "etc", "lib", "opt", "proc", "run", "sbin", "srv", "sys", "tmp", "usr", "var", "home", "root", "mnt", "media"}
	for _, dir := range unixDirs {
		if strings.EqualFold(secondPart, dir) {
			return false
		}
	}

	// If we get here, it's likely a Git Bash path (e.g., /c/Users, /f/MyProjects)
	return true
}

// convertGitBashPathToWindows converts Git Bash/MSYS2 Unix-style paths to Windows format
// Examples:
//   - /f/MyProjects/test/file.py -> F:\MyProjects\test\file.py
//   - /c/Users/name/file.txt -> C:\Users\name\file.txt
//   - /d/work/project -> D:\work\project
//
// Only converts paths that match the Git Bash pattern to avoid breaking real Unix paths
func convertGitBashPathToWindows(s string) string {
	if !isLikelyGitBashPath(s) {
		return s
	}

	matches := reGitBashPath.FindStringSubmatch(s)
	if len(matches) < 2 {
		return s
	}

	// Extract drive letter and convert to uppercase
	driveLetter := strings.ToUpper(matches[1])

	// Replace /x/ with X:\ and convert forward slashes to backslashes
	remainder := s[3:] // Skip /x/
	windowsPath := driveLetter + ":\\" + strings.ReplaceAll(remainder, "/", "\\")

	return windowsPath
}

// fixGitBashPathsInArgs converts Git Bash/MSYS2 paths in tool arguments to Windows format
// This handles cases where Claude Code running in Git Bash generates Unix-style paths
// that need to be converted for Windows tools
// Only processes paths that match the Git Bash pattern to avoid breaking real Unix paths
func fixGitBashPathsInArgs(args map[string]interface{}) {
	for key, val := range args {
		strVal, ok := val.(string)
		if !ok || strVal == "" {
			continue
		}

		// Convert path-like keys (path, file, dir, etc.) if they match Git Bash pattern
		if isPathLikeKey(key) && isLikelyGitBashPath(strVal) {
			args[key] = convertGitBashPathToWindows(strVal)
			continue
		}

		// Also check for embedded Git Bash paths in command strings
		if key == "command" && strings.Contains(strVal, " /") {
			// Find and convert all Git Bash paths in the command
			// Example: "python /f/MyProjects/test/file.py" -> "python F:\MyProjects\test\file.py"
			converted := convertGitBashPathsInCommand(strVal)
			if converted != strVal {
				args[key] = converted
			}
		}
	}
}

// convertGitBashPathsInCommand converts Git Bash paths embedded in command strings
// Example: "python /f/MyProjects/test/file.py" -> "python F:\MyProjects\test\file.py"
// Only converts paths that match the Git Bash pattern to avoid breaking real Unix paths
func convertGitBashPathsInCommand(cmd string) string {
	return reGitBashPathInCommand.ReplaceAllStringFunc(cmd, func(match string) string {
		// Only convert if it looks like a Git Bash path
		if isLikelyGitBashPath(match) {
			return convertGitBashPathToWindows(match)
		}
		return match
	})
}

// fixWindowsPathEscapes converts control characters back to their backslash-letter form.
// This fixes paths where JSON escape sequences like \t, \n were incorrectly interpreted.
// Example: "F:\MyProjects	est\file.py" -> "F:\MyProjects\test\file.py"
func fixWindowsPathEscapes(s string) string {
	// Map of control characters to their original escape sequences
	// These are the JSON escape sequences that get interpreted during JSON parsing
	replacements := []struct {
		char   rune
		escape string
	}{
		{'\t', `\t`}, // tab -> \t
		{'\n', `\n`}, // newline -> \n
		{'\r', `\r`}, // carriage return -> \r
		{'\b', `\b`}, // backspace -> \b
		{'\f', `\f`}, // form feed -> \f
	}

	var result strings.Builder
	result.Grow(len(s) + 10) // Pre-allocate with some extra space

	for _, r := range s {
		replaced := false
		for _, rep := range replacements {
			if r == rep.char {
				result.WriteString(rep.escape)
				replaced = true
				break
			}
		}
		if !replaced {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// convertWindowsPathToUnixStyle converts Windows-style paths to Unix-style paths for Claude Code compatibility.
// Claude Code expects Unix-style paths (forward slashes) in tool results to avoid backslash escape sequence issues.
// Reference: Claude Code docs state "Use forward slashes (Unix style) in all paths"
// Reference: Claude Code v2.0.62 fixed "bash commands failing on Windows when temp directory paths contained
// characters like `t` or `n` that were misinterpreted as escape sequences"
//
// Examples:
//   - "F:\MyProjects\test\hello.py" -> "F:/MyProjects/test/hello.py"
//   - "C:\Users\Admin\file.txt" -> "C:/Users/Admin/file.txt"
//   - "/home/user/file.py" -> "/home/user/file.py" (unchanged)
func convertWindowsPathToUnixStyle(path string) string {
	// Only convert if it looks like a Windows path (has drive letter)
	if !reWindowsDrivePath.MatchString(path) {
		return path
	}
	// Replace all backslashes with forward slashes
	return strings.ReplaceAll(path, `\`, `/`)
}

// convertWindowsPathsInToolResult converts Windows-style paths to Unix-style in tool result content.
// This prevents Claude Code from misinterpreting backslashes as escape sequences.
// Handles both normal Windows paths (C:\path) and corrupted paths where backslashes were stripped (C:path).
//
// CRITICAL: This function must handle paths where backslashes have already been interpreted as
// JSON escape sequences (e.g., \t → tab, \n → newline). These control characters appear in the
// string and must be matched and converted.
//
// Reference: Claude Code issue #15290 - backslash escape sequences in tool parameters are
// interpreted as control characters during JSON transmission.
func convertWindowsPathsInToolResult(content string) string {
	if content == "" {
		return content
	}

	// Quick check: if no drive letter pattern, no conversion needed
	if !reWindowsDrivePath.MatchString(content) {
		return content
	}

	// Pattern 1: Normal Windows paths with backslashes (C:\path\file.txt)
	content = reWindowsPathNormal.ReplaceAllStringFunc(content, func(match string) string {
		return convertWindowsPathToUnixStyle(match)
	})

	// Pattern 2: Corrupted Windows paths where backslashes were interpreted as control characters
	content = reWindowsPathCorrupted.ReplaceAllStringFunc(content, func(match string) string {
		// Check if this looks like a corrupted path (no separator after colon)
		if len(match) > 2 && match[2] != '/' && match[2] != '\\' {
			// This is a corrupted path like "F:MyProjects..." or "F:MyProjects<tab>est..."
			// Replace control characters with path separators to reconstruct the path
			var result strings.Builder
			result.Grow(len(match) + 10)

			for i, r := range match {
				// Keep drive letter (position 0) and colon (position 1) as-is
				if i == 0 && ((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
					result.WriteRune(r)
					continue
				}
				if i == 1 && r == ':' {
					result.WriteRune(r)
					continue
				}
				// Replace control characters (tab, newline, etc.) with path separator
				// These are where backslashes were incorrectly interpreted as escape sequences
				if r < 32 || r == 127 {
					result.WriteRune('/')
					continue
				}
				// Keep printable characters except Windows-invalid filename chars.
				// This preserves non-ASCII folder names (e.g., Chinese, Japanese, Cyrillic)
				// and other valid Windows path characters like (), [], ~, @, #, +, spaces.
				// AI Review: Changed from whitelist (ASCII only) to blacklist approach to
				// support Unicode paths. Colon is only allowed as drive separator (handled above).
				switch r {
				case '<', '>', '"', '|', '?', '*', ':':
					// Skip invalid Windows filename characters
				default:
					result.WriteRune(r)
				}
			}

			cleaned := result.String()
			// Add slash after drive letter if missing
			if len(cleaned) > 2 && cleaned[2] != '/' && cleaned[2] != '\\' {
				cleaned = string(cleaned[0]) + ":/" + cleaned[2:]
			}
			// Normalize to Unix-style paths (convert any remaining backslashes to forward slashes)
			// This ensures the corrupted-path branch returns Unix-style paths consistently
			return convertWindowsPathToUnixStyle(cleaned)
		}
		return match
	})

	return content
}

// reWindowsDrivePath matches Windows drive letter paths (e.g., "C:", "F:")
// This pattern finds the start of a Windows path within a command string.
// The path may contain control characters where backslashes were incorrectly interpreted.
// NOTE: This variable is declared at package level above for performance (avoid regex recompilation)

// containsWindowsDrivePath returns true if the string contains a Windows drive letter pattern.
// This is used to detect embedded Windows paths in any string value, regardless of key name.
// This allows auto-extension to any tool that may contain Windows paths (git, custom tools, etc.)
func containsWindowsDrivePath(s string) bool {
	return reWindowsDrivePath.MatchString(s)
}

// fixEmbeddedWindowsPathsInCommand fixes Windows paths embedded within command strings.
// Example: "python F:\MyProjects\test\file.py" where \t in the path became a tab character.
// This function finds Windows drive letter patterns and fixes control characters after them.
func fixEmbeddedWindowsPathsInCommand(s string) string {
	if !containsControlChars(s) {
		return s
	}

	// Find all potential Windows path starts (drive letters like "C:", "F:")
	matches := reWindowsDrivePath.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return s
	}

	var result strings.Builder
	result.Grow(len(s) + 20)

	lastEnd := 0
	for _, match := range matches {
		pathStart := match[0]

		// Write everything before this path unchanged
		result.WriteString(s[lastEnd:pathStart])

		// Find the end of this path (space, quote, or end of string)
		pathEnd := len(s)
		for i := match[1]; i < len(s); i++ {
			r := rune(s[i])
			// Path ends at whitespace (but not control chars that were originally backslashes),
			// quotes, or other command separators
			if r == ' ' || r == '"' || r == '\'' || r == '|' || r == '&' || r == ';' || r == '>' || r == '<' {
				pathEnd = i
				break
			}
		}

		// Extract and fix the path portion
		pathPortion := s[pathStart:pathEnd]
		if containsControlChars(pathPortion) {
			pathPortion = fixWindowsPathEscapes(pathPortion)
		}
		result.WriteString(pathPortion)

		lastEnd = pathEnd
	}

	// Write any remaining content after the last path
	if lastEnd < len(s) {
		result.WriteString(s[lastEnd:])
	}

	return result.String()
}

// reJSONWindowsPath matches Windows paths in JSON strings.
// Looks for patterns like: "F:\\" or "C:\\" (drive letter followed by colon and escaped backslash)
// This regex finds the JSON-escaped form of Windows paths.
var reJSONWindowsPath = regexp.MustCompile(`([A-Za-z]):\\\\`)

// reAlreadyDoubleEscaped matches Windows paths that are already double-escaped.
// Pattern: drive letter followed by double backslash (e.g., "F:\\" in the parsed string)
var reAlreadyDoubleEscaped = regexp.MustCompile(`[A-Za-z]:\\\\`)

// doubleEscapeWindowsPathsForBash doubles the backslash escaping in Windows paths
// ONLY within the "command" field of Bash tool arguments.
//
// This is needed because Claude Code client performs additional escape processing
// on Bash command strings, which corrupts Windows paths. For example:
//   - Input JSON:  {"command": "python F:\\MyProjects\\test\\file.py"}
//   - After CC processing: "python F:\MyProjects	est\file.py" (corrupted - \t became tab)
//
// By double-escaping only the command field:
//   - We send:     {"command": "python F:\\\\MyProjects\\\\test\\\\file.py"}
//   - After CC processing: "python F:\MyProjects\test\file.py" (correct)
//
// IMPORTANT: We only process the "command" field because:
//  1. Only Bash tool performs additional escape processing on its command string
//  2. Other tools (Read, Write, Edit) use file_path for path matching
//  3. Double-escaping file_path would break Claude Code's file tracking
//     (e.g., Read("hello.py") vs Write("F:\\\\path\\\\hello.py") won't match)
func doubleEscapeWindowsPathsForBash(jsonStr string) string {
	// Quick check: if no "command" field with Windows path, return unchanged
	if !strings.Contains(jsonStr, `"command"`) {
		return jsonStr
	}
	if !reJSONWindowsPath.MatchString(jsonStr) {
		return jsonStr
	}

	// Parse JSON to selectively process only the "command" field
	var args map[string]any
	if err := utils.UnmarshalJSONUseNumber([]byte(jsonStr), &args); err != nil {
		return jsonStr
	}

	command, ok := args["command"].(string)
	if !ok || command == "" {
		return jsonStr
	}

	// Check if command contains Windows path with single backslash
	// If the path already has double backslashes (\\), it's already escaped
	// and we should not double-escape again.
	// Pattern: drive letter followed by single backslash (not double)
	// e.g., "F:\MyProjects" should be escaped, but "F:\\MyProjects" should not
	if !strings.ContainsAny(command, "\\") {
		return jsonStr
	}

	// Check if already double-escaped by looking for patterns like "F:\\" or "C:\\"
	// If the command contains "X:\\" (double backslash after drive letter), it's already escaped
	if reAlreadyDoubleEscaped.MatchString(command) {
		// Already double-escaped, don't escape again
		return jsonStr
	}

	// Double-escape backslashes in the command string
	// CRITICAL: Only double-escape backslashes that are part of Windows paths,
	// NOT escape sequences like \n, \t, \r, etc.
	// Strategy: Replace backslashes that are followed by path-like characters
	// (letters, numbers, dots, spaces, underscores, hyphens) but NOT by escape chars (n, t, r, etc.)
	// This preserves legitimate escape sequences while fixing Windows paths.
	//
	// Pattern explanation:
	// - Match backslash followed by:
	//   - Drive letter pattern: [A-Za-z]: (e.g., C:\, F:\)
	//   - Path separator: \ (double backslash in path)
	//   - Path characters: letters, numbers, dots, spaces, underscores, hyphens, Chinese characters
	// - Do NOT match backslash followed by escape characters: n, t, r, b, f, v, 0, x, u
	//
	// Use a more targeted approach: only double-escape backslashes in Windows drive paths
	// Pattern: X:\ where X is a drive letter
	// This avoids corrupting escape sequences like \n, \t, \r
	if reWindowsDrivePath.MatchString(command) {
		// Only double-escape backslashes that are part of Windows paths
		// AI Review Note: The concern about "doubling all backslashes including \n, \t" refers to
		// the case where JSON escape sequences have already been interpreted as control characters.
		// In that case, the backslash is gone and we have a control char (0x0A for \n, 0x09 for \t).
		// However, in Windows paths like "F:\test\file.py", the \t is TWO characters (backslash + 't'),
		// not a tab character. These MUST be doubled for Bash tool compatibility.
		// The regex pattern \\(?:[^\\]|$) matches backslash + next-char, so match[1] is the char after \.
		// We double-escape all matches because:
		// 1. In Windows paths, all backslashes are path separators (even \t, \n, \r in path names)
		// 2. Real escape sequences (like \n in "echo hello\nworld") are already converted to control
		//    chars during JSON parsing, so they won't match this pattern (no backslash remains)
		command = reWindowsPathBackslash.ReplaceAllStringFunc(command, func(match string) string {
			// match is like "\M" or "\P" or "\t" (two chars: backslash + letter)
			// We want to replace "\M" with "\\M" (double the backslash, keep the character)
			if len(match) >= 2 {
				return "\\\\" + match[1:] // Double backslash + rest of match
			}
			return match
		})
		args["command"] = command
	} else {
		// No Windows paths detected, return unchanged to preserve escape sequences
		return jsonStr
	}

	// Re-serialize to JSON
	result, err := json.Marshal(args)
	if err != nil {
		return jsonStr
	}

	return string(result)
}

func normalizeArgsEnsureSlice(args map[string]any, key string) {
	v, ok := args[key]
	if !ok || v == nil {
		return
	}
	if _, isSlice := v.([]any); isSlice {
		return
	}
	strVal, ok := v.(string)
	if !ok {
		return
	}
	var list []any
	if err := utils.UnmarshalJSONUseNumber([]byte(strVal), &list); err == nil {
		args[key] = list
		return
	}
	args[key] = []any{strVal}
}

func normalizeArgsEnsureMap(args map[string]any, key string) {
	v, ok := args[key]
	if !ok || v == nil {
		return
	}
	if _, isMap := v.(map[string]any); isMap {
		return
	}
	strVal, ok := v.(string)
	if !ok {
		return
	}
	var out map[string]any
	if err := utils.UnmarshalJSONUseNumber([]byte(strVal), &out); err == nil {
		args[key] = out
	}
}

func normalizeAskUserQuestionArgs(args map[string]any) {
	normalizeArgsEnsureSlice(args, "questions")
	normalizeArgsEnsureMap(args, "answers")
}

func normalizeListDirArgs(args map[string]any) {
	if _, ok := args["recursive"]; !ok {
		args["recursive"] = false
	}
}

func normalizeWebSearchArgs(args map[string]any) {
	normalizeArgsEnsureSlice(args, "allowed_domains")
	normalizeArgsEnsureSlice(args, "blocked_domains")
}

func normalizeEditArgs(args map[string]any) {
	for _, key := range []string{"old_string", "new_string"} {
		v, ok := args[key]
		if !ok || v == nil {
			continue
		}
		strVal, ok := v.(string)
		if !ok {
			continue
		}
		if strings.Contains(strVal, "\\n") {
			args[key] = strings.ReplaceAll(strVal, "\\n", "\n")
		}
	}
}

func normalizeTodoWriteTodos(args map[string]any) ([]map[string]any, bool) {
	todos, ok := args["todos"]
	if !ok {
		if v, exists := args["value"]; exists {
			// Some malformed outputs place the todos array under a generic
			// "value" key. Treat this as the candidate todos source.
			todos = v
			ok = true
		}
	}

	if !ok {
		return nil, false
	}

	var todoList []any
	hasValidTodos := false

	switch v := todos.(type) {
	case []any:
		if len(v) > 0 {
			todoList = v
			hasValidTodos = true
		}
	case string:
		trimmedStr := strings.TrimSpace(v)
		if trimmedStr != "" {
			var parsed []any
			if err := utils.UnmarshalJSONUseNumber([]byte(trimmedStr), &parsed); err == nil && len(parsed) > 0 {
				todoList = parsed
				hasValidTodos = true
			}
		}
	case map[string]any:
		mapVal := v
		foundList := false
		for _, k := range []string{"todos", "todo", "item", "task", "value"} {
			if val, exists := mapVal[k]; exists {
				if list, ok := val.([]any); ok && len(list) > 0 {
					todoList = list
					foundList = true
					break
				} else if val != nil {
					todoList = []any{val}
					foundList = true
					break
				}
			}
		}
		if !foundList && len(mapVal) > 0 {
			todoList = []any{mapVal}
			foundList = true
		}
		hasValidTodos = foundList && len(todoList) > 0
	}

	if !hasValidTodos {
		return nil, false
	}

	normalizedTodos := make([]map[string]any, 0, len(todoList))
	for idx, item := range todoList {
		defaultID := fmt.Sprintf("task-%d", idx+1)

		if strItem, ok := item.(string); ok {
			normalizedTodos = append(normalizedTodos, map[string]any{
				"activeForm": strItem,
				"content":    strItem,
				"status":     "pending",
				"priority":   "medium",
				"id":         defaultID,
			})
			continue
		}

		mapItem, ok := item.(map[string]any)
		if !ok {
			continue
		}

		cleanItem := make(map[string]any)
		if content, ok := mapItem["content"]; ok {
			cleanItem["content"] = content
		} else if task, ok := mapItem["task"]; ok {
			cleanItem["content"] = task
		} else if desc, ok := mapItem["description"]; ok {
			cleanItem["content"] = desc
		}
		if existingAF, ok := mapItem["activeForm"]; ok {
			cleanItem["activeForm"] = existingAF
		} else {
			switch v := cleanItem["content"].(type) {
			case string:
				cleanItem["activeForm"] = v
			default:
				cleanItem["activeForm"] = fmt.Sprint(v)
			}
		}

		var rawStatus any
		if status, ok := mapItem["status"]; ok {
			rawStatus = status
		} else if state, ok := mapItem["state"]; ok {
			rawStatus = state
		}

		finalStatus := "pending"
		if strStatus, ok := rawStatus.(string); ok {
			lowerStatus := strings.ToLower(strings.TrimSpace(strStatus))
			switch lowerStatus {
			case "completed", "complete", "finished", "done", "success", "succeeded":
				finalStatus = "completed"
			case "in_progress", "in progress", "working", "doing", "running", "active":
				finalStatus = "in_progress"
			case "pending", "todo", "not_started", "not started", "planned":
				finalStatus = "pending"
			default:
				finalStatus = "pending"
			}
		}
		cleanItem["status"] = finalStatus

		var rawPriority any
		if p, ok := mapItem["priority"]; ok {
			rawPriority = p
		}
		finalPriority := "medium"
		if strP, ok := rawPriority.(string); ok {
			lowerP := strings.ToLower(strings.TrimSpace(strP))
			switch lowerP {
			case "high":
				finalPriority = "high"
			case "low":
				finalPriority = "low"
			case "medium":
				finalPriority = "medium"
			}
		}
		cleanItem["priority"] = finalPriority

		var idStr string
		if rawID, ok := mapItem["id"]; ok {
			switch v := rawID.(type) {
			case string:
				idStr = strings.TrimSpace(v)
			case float64:
				idStr = fmt.Sprintf("task-%d", int(v))
			case json.Number:
				if id, err := v.Int64(); err == nil {
					idStr = fmt.Sprintf("task-%d", id)
				} else if id, err := v.Float64(); err == nil {
					idStr = fmt.Sprintf("task-%d", int(id))
				}
			case int:
				idStr = fmt.Sprintf("task-%d", v)
			default:
				idStr = ""
			}
		}
		if len(idStr) < 3 {
			idStr = defaultID
		}
		cleanItem["id"] = idStr

		// Only keep todos that have meaningful content, matching the
		// CC XML parsing path behavior.
		if _, hasContent := cleanItem["content"]; hasContent {
			normalizedTodos = append(normalizedTodos, cleanItem)
		}
	}

	if len(normalizedTodos) == 0 {
		return nil, false
	}

	return normalizedTodos, true
}

func normalizeOpenAIToolCallArguments(toolName string, arguments string) (string, bool) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}", true
	}

	var args map[string]any
	if err := utils.UnmarshalJSONUseNumber([]byte(trimmed), &args); err != nil {
		var raw any
		if err2 := utils.UnmarshalJSONUseNumber([]byte(trimmed), &raw); err2 == nil {
			args = map[string]any{"value": raw}
		} else {
			return arguments, false
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	// Apply generic normalization for all tools (including Windows path fix).
	// This must be done before tool-specific normalization.
	normalizeArgsGenericInPlace(args)

	// Tool-specific normalization
	switch toolName {
	case "TodoWrite":
		if normalizedTodos, ok := normalizeTodoWriteTodos(args); ok {
			args = map[string]any{"todos": normalizedTodos}
		} else {
			return arguments, false
		}

	case "AskUserQuestion":
		normalizeAskUserQuestionArgs(args)

	case "list_dir":
		normalizeListDirArgs(args)

	case "WebSearch":
		normalizeWebSearchArgs(args)

	case "Edit":
		normalizeEditArgs(args)

	default:
		// For tools without specific normalization, still return the generically
		// normalized args (e.g., with Windows path fixes applied).
	}

	// Always marshal and return the normalized args, even for tools without
	// specific normalization. This ensures Windows path fixes are applied.
	out, err := json.Marshal(args)
	if err != nil {
		return arguments, false
	}
	return string(out), true
}

// ...

func parseFunctionCallsFromContentForCC(c *gin.Context, content string) (string, []ClaudeContentBlock) {
	// ...

	// Parse function calls from the content
	triggerSignal := getTriggerSignal(c)
	calls := parseFunctionCallsXML(content, triggerSignal)

	// Fallback: try parsing without trigger signal if none found
	// This handles cases where thinking models output tool calls without trigger signal
	if len(calls) == 0 && (strings.Contains(content, "<function_calls>") || strings.Contains(content, "<invoke ")) {
		calls = parseFunctionCallsXML(content, "")
		if len(calls) > 0 {
			logrus.WithField("parsed_count", len(calls)).
				Debug("CC+FC: Parsed function calls using fallback (no trigger signal)")
		}
	}

	// Fallback: try extracting tool calls from embedded JSON structures
	// This handles cases where thinking models output tool call info in JSON format
	// instead of XML format (e.g., {"name":"Read","file_path":"..."})
	if len(calls) == 0 {
		calls = extractToolCallsFromEmbeddedJSON(content)
		if len(calls) > 0 {
			logrus.WithField("parsed_count", len(calls)).
				Debug("CC+FC: Parsed function calls from embedded JSON")
		}
	}

	// Debug logging for troubleshooting when no tool calls found
	if len(calls) == 0 && logrus.IsLevelEnabled(logrus.DebugLevel) {
		hasInvoke := strings.Contains(content, "<invoke")
		hasFunctionCalls := strings.Contains(content, "<function_calls>")
		hasTrigger := triggerSignal != "" && strings.Contains(content, triggerSignal)
		// NOTE: Use "<antml" instead of "antml" to avoid false positives from words like "semantml"
		// This is only for debug logging, so precision is preferred over recall
		// Parentheses added for clarity: && has higher precedence than ||, but explicit grouping improves readability
		hasThinking := strings.Contains(content, "<thinking>") || strings.Contains(content, "<think>") ||
			(strings.Contains(content, "<antml") && strings.Contains(content, "thinking"))
		// Detect execution intent phrases (model describes action but doesn't call tool)
		// This helps diagnose cases where thinking models output intent without actual tool calls
		hasExecutionIntent := reExecutionIntent.MatchString(content)
		fields := logrus.Fields{
			"content_len":          len(content),
			"has_invoke":           hasInvoke,
			"has_function_calls":   hasFunctionCalls,
			"has_trigger":          hasTrigger,
			"has_thinking":         hasThinking,
			"has_execution_intent": hasExecutionIntent,
			"trigger_signal":       triggerSignal,
		}
		// Only include content preview when body logging is enabled for the group
		// to avoid potential privacy concerns (content may contain user prompts or paths)
		if gv, ok := c.Get("group"); ok {
			if g, ok := gv.(*models.Group); ok && g.EffectiveConfig.EnableRequestBodyLogging {
				fields["content_preview"] = utils.TruncateString(content, 200)
			}
		}
		logrus.WithFields(fields).Debug("CC+FC: No tool calls found in content")
	}

	if len(calls) == 0 {
		return content, nil
	}

	// Convert to Claude tool_use blocks
	var toolUseBlocks []ClaudeContentBlock
	for i, call := range calls {
		if call.Name == "" {
			continue
		}

		normalizeArgsGenericInPlace(call.Args)

		// Specific normalization for tools to handle schema strictness.
		// NOTE: We intentionally do NOT route this through normalizeOpenAIToolCallArguments.
		// Doing so would require json.Marshal + json.Unmarshal per call, which adds extra
		// allocations in the CC hot path. We reuse the same in-place helpers as the
		// OpenAI tool-call path to avoid drift without adding JSON overhead.
		skipCall := false
		switch call.Name {
		case "TodoWrite":
			// Use shared helper for TodoWrite normalization
			if normalizedTodos, ok := normalizeTodoWriteTodos(call.Args); ok {
				call.Args["todos"] = normalizedTodos
			} else {
				skipCall = true
				logrus.Debug("CC+FC: Skipping TodoWrite call - validation failed or no todos found")
			}

		case "AskUserQuestion":
			normalizeAskUserQuestionArgs(call.Args)

		case "list_dir":
			normalizeListDirArgs(call.Args)

		case "WebSearch":
			normalizeWebSearchArgs(call.Args)

		case "Edit":
			normalizeEditArgs(call.Args)
		}

		if skipCall {
			continue
		}

		// Marshal arguments to JSON
		inputJSON, err := json.Marshal(call.Args)
		if err != nil {
			logrus.WithError(err).Debug("CC+FC: Failed to marshal function call arguments, skipping")
			continue
		}

		// Apply Windows path escape fix for Bash commands
		// This must be done after marshaling to handle the final JSON string
		inputJSONStr := doubleEscapeWindowsPathsForBash(string(inputJSON))

		// Generate unique tool use ID
		toolUseID := fmt.Sprintf("toolu_%s_%d", utils.GenerateRandomSuffix(), i)

		// Restore original tool name from reverse map if tool name shortening was applied.
		// The model outputs shortened names (from request conversion), but Claude client
		// expects original names. This mirrors the restoration in convertOpenAIToClaudeResponse.
		toolName := call.Name
		if reverseMap := getOpenAIToolNameReverseMap(c); reverseMap != nil {
			if orig, ok := reverseMap[call.Name]; ok {
				toolName = orig
			}
		}

		toolUseBlocks = append(toolUseBlocks, ClaudeContentBlock{
			Type:  "tool_use",
			ID:    toolUseID,
			Name:  toolName,
			Input: json.RawMessage(inputJSONStr),
		})
	}

	if len(toolUseBlocks) == 0 {
		return content, nil
	}

	// Remove function call XML blocks from content
	cleanedContent := removeFunctionCallsBlocks(content, cleanupModeFull)

	logrus.WithFields(logrus.Fields{
		"trigger_signal":  triggerSignal,
		"tool_use_count":  len(toolUseBlocks),
		"content_cleaned": len(cleanedContent) != len(content),
	}).Debug("CC+FC: Converted XML function calls to Claude tool_use blocks")

	return cleanedContent, toolUseBlocks
}

// handleCCNormalResponse handles non-streaming response conversion for CC support.
func (ps *ProxyServer) handleCCNormalResponse(c *gin.Context, resp *http.Response) {
	bodyBytes, err := readAllWithLimit(resp.Body, maxUpstreamResponseBodySize)
	if err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			// Upstream response is too large to safely convert. Return a structured
			// Claude error instead of attempting to parse a truncated JSON payload.
			maxMB := maxUpstreamResponseBodySize / (1024 * 1024)
			message := fmt.Sprintf("Upstream response exceeded maximum allowed size (%dMB) for CC conversion", maxMB)
			logrus.WithField("limit_mb", maxMB).
				Warn("CC: Upstream response body too large for CC conversion")
			claudeErr := ClaudeErrorResponse{
				Type: "error",
				Error: ClaudeError{
					Type:    "invalid_request_error",
					Message: message,
				},
			}
			clearUpstreamEncodingHeaders(c)
			// Record first byte at the client delivery point; the helper marks
			// only after a successful non-empty write, so a failed response
			// write leaves first_byte_duration_ms NULL.
			writeJSONMarkingFirstByte(c, http.StatusBadGateway, claudeErr)
			return
		}

		logrus.WithError(err).Error("Failed to read OpenAI response body for CC conversion")
		clearUpstreamEncodingHeaders(c)
		c.Status(http.StatusInternalServerError)
		return
	}
	// defer resp.Body.Close() - caller (executeRequestWithRetry) handles this

	// Track original encoding and decompression state to ensure correct header handling.
	// When decompression fails, we must preserve Content-Encoding if returning original bytes.
	origEncoding := resp.Header.Get("Content-Encoding")
	decompressed := false

	// Decompress response body if it is encoded (e.g., gzip) before JSON parsing.
	// This avoids returning compressed bytes to Claude clients and matches CC API expectations.
	// Use size-limited decompression to prevent memory exhaustion from malicious compressed payloads.
	bodyBytes, err = utils.DecompressResponseWithLimit(origEncoding, bodyBytes, maxUpstreamResponseBodySize)
	if err != nil {
		// Use errors.Is() for sentinel error comparison to handle wrapped errors properly
		if errors.Is(err, utils.ErrDecompressedTooLarge) {
			maxMB := maxUpstreamResponseBodySize / (1024 * 1024)
			message := fmt.Sprintf("Decompressed response exceeded maximum allowed size (%dMB) for CC conversion", maxMB)
			logrus.WithField("limit_mb", maxMB).
				Warn("CC: Decompressed response body too large for conversion")
			claudeErr := ClaudeErrorResponse{
				Type: "error",
				Error: ClaudeError{
					Type:    "invalid_request_error",
					Message: message,
				},
			}
			clearUpstreamEncodingHeaders(c)
			writeJSONMarkingFirstByte(c, http.StatusBadGateway, claudeErr)
			return
		}
		// Other decompression errors: continue with original data but preserve encoding header
		logrus.WithError(err).Warn("CC: Decompression failed, using original data")
	} else if origEncoding != "" {
		// Decompression succeeded, mark as decompressed
		decompressed = true
	}

	// Parse OpenAI response
	var openaiResp OpenAIResponse
	if err := json.Unmarshal(bodyBytes, &openaiResp); err != nil {
		// Per AI review: sanitize BEFORE truncate to prevent leaking truncated secrets
		safePreview := utils.TruncateString(utils.SanitizeErrorBody(string(bodyBytes)), 512)
		logrus.WithError(err).WithField("body_preview", safePreview).
			Warn("Failed to parse OpenAI response for CC conversion")
		// Store sanitized preview for downstream logging
		c.Set("response_body", safePreview)

		// For non-2xx responses, convert to Claude error format
		// so Claude Code can properly display the error message to the user.
		// This handles cases like upstream returning plain text errors (e.g., "当前模型负载过高，请稍后重试")
		// Per AI review: removed "|| err != nil" since we're already inside err != nil block,
		// making that condition always true and the 2xx fallback unreachable
		if resp.StatusCode >= 400 {
			setTokenUsageFromBody(c, bodyBytes)
			// Extract error message from response body
			errorMessage := strings.TrimSpace(string(bodyBytes))

			// Per AI review: reuse returnClaudeError to eliminate duplicate mapping logic
			// and ensure consistent sanitization of error messages
			logrus.WithFields(logrus.Fields{
				"status_code":   resp.StatusCode,
				"error_type":    mapStatusToClaudeErrorType(resp.StatusCode),
				"error_message": utils.TruncateString(utils.SanitizeErrorBody(errorMessage), 200),
			}).Warn("CC: Converting upstream error to Claude format")

			returnClaudeError(c, resp.StatusCode, errorMessage)
			return
		}

		// For 2xx responses with JSON parse failure, return original body
		// (this shouldn't happen normally but provides a fallback)
		// Clear upstream encoding/length headers since we may have decompressed the body above.
		// Returning decompressed bytes with a stale Content-Encoding header would cause clients
		// to attempt decompression again and corrupt the payload.
		clearUpstreamEncodingHeaders(c)
		// Preserve original Content-Encoding if data was not decompressed
		if !decompressed && origEncoding != "" {
			c.Header("Content-Encoding", origEncoding)
		}

		canEstimateFromBody := resp.StatusCode < http.StatusBadRequest && (origEncoding == "" || decompressed)
		setTokenUsageOrEstimateFromFullBodyIf(c, bodyBytes, canEstimateFromBody)
		// writeDataMarkingFirstByte records first-byte delivery only when the
		// write actually succeeds with non-empty bytes; an empty fallback body
		// keeps first_byte_duration_ms NULL.
		writeDataMarkingFirstByte(c, resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
		return
	}

	// Check for OpenAI error
	if openaiResp.Error != nil {
		setTokenUsageFromBody(c, bodyBytes)
		safeErrorMessage := strings.TrimSpace(utils.SanitizeErrorBody(openaiResp.Error.Message))
		logrus.WithFields(logrus.Fields{
			"error_type":    openaiResp.Error.Type,
			"error_message": safeErrorMessage,
			"error_code":    openaiResp.Error.Code,
		}).Warn("CC: OpenAI returned error in CC conversion")

		claudeErr := ClaudeErrorResponse{
			Type: "error",
			Error: ClaudeError{
				Type:    apiErrorTypeToClaudeErrorType(openaiResp.Error.Type),
				Message: safeErrorMessage,
			},
		}
		if claudeErr.Error.Message == "" {
			claudeErr.Error.Message = "Upstream returned an error"
		}
		clearUpstreamEncodingHeaders(c)
		writeJSONMarkingFirstByte(c, resp.StatusCode, claudeErr)
		return
	}
	if len(openaiResp.Choices) == 0 && openaiResp.Usage == nil {
		clearUpstreamEncodingHeaders(c)
		canEstimateFromBody := resp.StatusCode < http.StatusBadRequest && (origEncoding == "" || decompressed)
		setTokenUsageOrEstimateFromFullBodyIf(c, bodyBytes, canEstimateFromBody)
		// writeDataMarkingFirstByte records first-byte delivery only when the
		// write actually succeeds with non-empty bytes; an empty fallback body
		// keeps first_byte_duration_ms NULL.
		writeDataMarkingFirstByte(c, resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
		return
	}
	setTokenUsageOrEstimateFromFullBodyIf(c, bodyBytes, resp.StatusCode < http.StatusBadRequest)

	// When force_function_call is enabled in CC mode, extract original content
	// BEFORE conversion for function call parsing. This is necessary because
	// convertOpenAIToClaudeResponse calls splitThinkingContent which removes
	// XML function call blocks via removeFunctionCallsBlocks.
	var originalContent string
	if isFunctionCallEnabled(c) && len(openaiResp.Choices) > 0 {
		if msg := openaiResp.Choices[0].Message; msg != nil && msg.Content != nil {
			originalContent = *msg.Content
		}
	}

	cleanupMode := cleanupModeArtifactsOnly
	if isFunctionCallEnabled(c) {
		cleanupMode = cleanupModeFull
	}

	// Convert to Claude format
	// DESIGN DECISION: JSON/path repair of tool arguments is intentionally limited to the
	// force-function-call (FC) bridge mode. When only CC support is enabled (no FC), arguments
	// are passed through unchanged to preserve upstream formatting.
	//
	// Rationale:
	// - FC bridge mode synthesizes tool calls from XML, requiring normalization for compatibility
	// - Plain CC mode forwards native OpenAI tool_calls which are already well-formed
	// - Bash command double-escaping (doubleEscapeWindowsPathsForBash) is always applied for CC
	//   to fix Claude Code's Windows path escape bug, regardless of FC mode
	normalizeToolArgs := isFunctionCallEnabled(c)
	// Get tool name reverse map from context for restoring original tool names
	reverseToolNameMap := getOpenAIToolNameReverseMap(c)
	claudeResp := convertOpenAIToClaudeResponse(&openaiResp, cleanupMode, normalizeToolArgs, reverseToolNameMap)

	// Handle error finish_reason for non-streaming responses.
	// When upstream returns error (network_error, timeout, etc.) with no content,
	// return a Claude error response to notify the client.
	if len(openaiResp.Choices) > 0 && openaiResp.Choices[0].FinishReason != nil {
		finishReason := *openaiResp.Choices[0].FinishReason
		_, isError := convertFinishReasonToStopReason(finishReason)

		// Check if response has meaningful content
		hasContent := false
		for _, block := range claudeResp.Content {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				hasContent = true
				break
			}
			if block.Type == "tool_use" && block.ID != "" {
				hasContent = true
				break
			}
		}

		if isError && !hasContent {
			// Error with no content - return Claude error response
			logrus.WithField("finish_reason", finishReason).
				Warn("CC: Non-streaming upstream error with no content, returning error response")

			claudeErr := ClaudeErrorResponse{
				Type: "error",
				Error: ClaudeError{
					Type:    "api_error",
					Message: fmt.Sprintf("Upstream returned %s with no content", finishReason),
				},
			}
			clearUpstreamEncodingHeaders(c)
			writeJSONMarkingFirstByte(c, http.StatusBadGateway, claudeErr)
			return
		}
	}

	// When force_function_call is enabled in CC mode, parse XML function calls
	// from the ORIGINAL response content and convert them to Claude tool_use blocks.
	// This bridges the gap between the XML-based function call prompt injection
	// and Claude Code's expected tool_use format.
	if isFunctionCallEnabled(c) && originalContent != "" {
		cleanedContent, toolUseBlocks := parseFunctionCallsFromContentForCC(c, originalContent)

		if len(toolUseBlocks) > 0 {
			// Rebuild content: preserve thinking blocks + clean text + tool_use blocks
			var newContent []ClaudeContentBlock

			// Preserve existing thinking blocks from reasoning_content
			for _, block := range claudeResp.Content {
				if block.Type == "thinking" {
					newContent = append(newContent, block)
				}
			}

			// Add cleaned text content if not empty
			cleanedText := removeFunctionCallsBlocks(cleanedContent, cleanupModeFull)
			if strings.TrimSpace(cleanedText) != "" {
				newContent = append(newContent, ClaudeContentBlock{
					Type: "text",
					Text: cleanedText,
				})
			}

			// Add tool_use blocks
			newContent = append(newContent, toolUseBlocks...)

			claudeResp.Content = newContent

			// Update stop_reason to tool_use since we have tool calls
			toolUseReason := "tool_use"
			claudeResp.StopReason = &toolUseReason

			logrus.WithFields(logrus.Fields{
				"tool_use_count": len(toolUseBlocks),
				"text_retained":  strings.TrimSpace(cleanedText) != "",
			}).Debug("CC+FC: Added tool_use blocks to Claude response")
		}
	}

	logrus.WithFields(logrus.Fields{
		"openai_id":   openaiResp.ID,
		"claude_id":   claudeResp.ID,
		"stop_reason": claudeResp.StopReason,
		"content_len": len(claudeResp.Content),
	}).Debug("CC: Converted OpenAI normal response to Claude format")

	// Marshal Claude response
	claudeBody, err := json.Marshal(claudeResp)
	if err != nil {
		logrus.WithError(err).Error("Failed to marshal Claude response")
		// Clear headers and preserve original encoding if data was not decompressed
		clearUpstreamEncodingHeaders(c)
		if !decompressed && origEncoding != "" {
			c.Header("Content-Encoding", origEncoding)
		}
		// writeDataMarkingFirstByte records first-byte delivery only when the
		// write actually succeeds with non-empty bytes; an empty fallback body
		// keeps first_byte_duration_ms NULL.
		writeDataMarkingFirstByte(c, resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
		return
	}

	// Store a sanitized Claude response copy for downstream logging only.
	c.Set("response_body", sanitizeAndTruncateBytesForLog(claudeBody, maxResponseCaptureBytes))

	// Clear upstream encoding/length headers before writing synthesized response.
	// The proxy decompresses and re-encodes the response, so upstream headers no longer match.
	// Per RFC 7230, mismatched Content-Length causes client to treat response as incomplete.
	clearUpstreamEncodingHeaders(c)

	c.Header("Content-Type", "application/json")
	writeDataMarkingFirstByte(c, resp.StatusCode, "application/json", claudeBody)
}

// ClaudeStreamEvent represents a Claude streaming event.
type ClaudeStreamEvent struct {
	Type         string              `json:"type"`
	Message      *ClaudeResponse     `json:"message,omitempty"`
	Index        int                 `json:"index,omitempty"`
	ContentBlock *ClaudeContentBlock `json:"content_block,omitempty"`
	Delta        *ClaudeStreamDelta  `json:"delta,omitempty"`
	Usage        *ClaudeUsage        `json:"usage,omitempty"`
	Error        *ClaudeError        `json:"error,omitempty"` // For error events
}

// ClaudeStreamDelta represents delta content in Claude streaming.
type ClaudeStreamDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
}

const (
	ThinkingStartTag    = "<thinking>"
	ThinkingEndTag      = "</thinking>"
	ThinkingAltStartTag = "<think>"
	ThinkingAltEndTag   = "</think>"
	// ANTML format thinking tags used by some models (e.g., claude-opus-4-5-thinking)
	// The \b in the tag name is a marker used by models to identify internal control tags
	// Format: <antml\b:thinking>...</antml\b:thinking> or </antml> as generic closer
	ThinkingANTMLStartTag = "<antml\\b:thinking>"
	ThinkingANTMLEndTag   = "</antml\\b:thinking>"
	ThinkingANTMLAltEnd   = "</antml>" // Generic ANTML closer
)

// Pre-computed rune slices for tag matching to avoid repeated allocations in hot path
var (
	thinkingEndTagRunes      = []rune(ThinkingEndTag)
	thinkingAltEndTagRunes   = []rune(ThinkingAltEndTag)
	thinkingStartTagRunes    = []rune(ThinkingStartTag)
	thinkingAltStartTagRunes = []rune(ThinkingAltStartTag)
	// ANTML format rune slices
	thinkingANTMLStartTagRunes = []rune(ThinkingANTMLStartTag)
	thinkingANTMLEndTagRunes   = []rune(ThinkingANTMLEndTag)
	thinkingANTMLAltEndRunes   = []rune(ThinkingANTMLAltEnd)
)

type ThinkingEvent struct {
	Type    string
	Content string
}

type ThinkingParser struct {
	mu             sync.Mutex
	buffer         strings.Builder
	thinkingBuffer strings.Builder
	thinkingMode   bool
	events         []ThinkingEvent
	// Ring buffer to track last N characters for efficient suffix matching in normal mode
	// This avoids O(n²) cost of calling buffer.String() on every rune
	suffixRing     []rune
	suffixRingSize int
	// Ring buffer for thinking mode end-tag detection to avoid O(n²) String() calls
	thinkingRing     []rune
	thinkingRingSize int
}

func NewThinkingParser() *ThinkingParser {
	// Ring buffer size needs to hold the longest tag we need to match
	// Max tag length is len("</antml\\b:thinking>") = 19 (ANTML format)
	maxTagLen := 19
	return &ThinkingParser{
		suffixRing:       make([]rune, maxTagLen),
		suffixRingSize:   0,
		thinkingRing:     make([]rune, maxTagLen),
		thinkingRingSize: 0,
	}
}

func (p *ThinkingParser) FeedRune(char rune) {
	p.mu.Lock()
	defer p.mu.Unlock()

	charStr := string(char)

	if p.thinkingMode {
		// Write to buffer first, then check for end tag using ring buffer
		p.thinkingBuffer.WriteString(charStr)
		p.addToThinkingRing(char)
		// Check for all supported end tag formats: </thinking>, </think>, </antml\b:thinking>, </antml>
		if p.thinkingRingSuffixMatches(thinkingEndTagRunes) ||
			p.thinkingRingSuffixMatches(thinkingAltEndTagRunes) ||
			p.thinkingRingSuffixMatches(thinkingANTMLEndTagRunes) ||
			p.thinkingRingSuffixMatches(thinkingANTMLAltEndRunes) {
			// Extract content by trimming the matched end tag
			fullContent := p.thinkingBuffer.String()
			var tagLen int
			if p.thinkingRingSuffixMatches(thinkingEndTagRunes) {
				tagLen = len(ThinkingEndTag)
			} else if p.thinkingRingSuffixMatches(thinkingAltEndTagRunes) {
				tagLen = len(ThinkingAltEndTag)
			} else if p.thinkingRingSuffixMatches(thinkingANTMLEndTagRunes) {
				tagLen = len(ThinkingANTMLEndTag)
			} else {
				tagLen = len(ThinkingANTMLAltEnd)
			}
			content := fullContent[:len(fullContent)-tagLen]
			// Remove leading ">" artifact from parsing logic per b4u2cc reference implementation
			// See: b4u2cc/deno-proxy/src/parser.ts lines 122, 274, 338
			// Pattern: /^\s*>\s*/ - only strip if it's specifically whitespace + ">" + whitespace
			content = strings.TrimSpace(content)
			if strings.HasPrefix(content, ">") {
				// Only strip the ">" if followed by space/newline (known artifact pattern)
				if len(content) > 1 && (content[1] == ' ' || content[1] == '\n' || content[1] == '\r' || content[1] == '\t') {
					content = strings.TrimSpace(content[1:])
				}
			}
			if trimmed := strings.TrimSpace(content); trimmed != "" {
				p.events = append(p.events, ThinkingEvent{Type: "thinking", Content: trimmed})
			}
			p.thinkingBuffer.Reset()
			p.resetThinkingRing()
			p.thinkingMode = false
		}
		return
	}

	// Write to buffer first, then add to ring and check for start tags
	// This ensures buffer.Len() includes the current character when calculating text portion
	p.buffer.WriteString(charStr)
	p.addToRing(char)

	// Check if ring buffer ends with start tags using O(1) suffix check
	// Support all formats: <thinking>, <think>, <antml\b:thinking>
	if p.ringSuffixMatches(thinkingStartTagRunes) ||
		p.ringSuffixMatches(thinkingAltStartTagRunes) ||
		p.ringSuffixMatches(thinkingANTMLStartTagRunes) {
		// Extract text portion by removing the matched tag
		textLen := p.buffer.Len()
		var tagLen int
		if p.ringSuffixMatches(thinkingStartTagRunes) {
			tagLen = len(ThinkingStartTag)
		} else if p.ringSuffixMatches(thinkingAltStartTagRunes) {
			tagLen = len(ThinkingAltStartTag)
		} else {
			tagLen = len(ThinkingANTMLStartTag)
		}

		if textLen > tagLen {
			// Get text before the tag
			fullText := p.buffer.String()
			textPortion := fullText[:textLen-tagLen]
			if textPortion != "" {
				p.events = append(p.events, ThinkingEvent{Type: "text", Content: textPortion})
			}
		}
		p.buffer.Reset()
		p.thinkingMode = true
		p.thinkingBuffer.Reset()
		p.resetRing()
		p.resetThinkingRing()
		return
	}
}

// addToRing adds a rune to the ring buffer for efficient suffix matching
func (p *ThinkingParser) addToRing(r rune) {
	maxSize := cap(p.suffixRing)
	if p.suffixRingSize < maxSize {
		p.suffixRing[p.suffixRingSize] = r
		p.suffixRingSize++
	} else {
		// Ring is full, shift left and add new rune at end
		copy(p.suffixRing, p.suffixRing[1:])
		p.suffixRing[maxSize-1] = r
	}
}

// resetRing clears the ring buffer
func (p *ThinkingParser) resetRing() {
	p.suffixRingSize = 0
}

// addToThinkingRing adds a rune to the thinking ring buffer for end-tag detection
func (p *ThinkingParser) addToThinkingRing(r rune) {
	maxSize := cap(p.thinkingRing)
	if p.thinkingRingSize < maxSize {
		p.thinkingRing[p.thinkingRingSize] = r
		p.thinkingRingSize++
	} else {
		// Ring is full, shift left and add new rune at end
		copy(p.thinkingRing, p.thinkingRing[1:])
		p.thinkingRing[maxSize-1] = r
	}
}

// resetThinkingRing clears the thinking ring buffer
func (p *ThinkingParser) resetThinkingRing() {
	p.thinkingRingSize = 0
}

// thinkingRingSuffixMatches checks if the thinking ring buffer ends with the given tag runes
func (p *ThinkingParser) thinkingRingSuffixMatches(tagRunes []rune) bool {
	tagLen := len(tagRunes)

	if p.thinkingRingSize < tagLen {
		return false
	}

	// Compare the last tagLen runes in the ring with the tag
	start := p.thinkingRingSize - tagLen
	for i := 0; i < tagLen; i++ {
		if p.thinkingRing[start+i] != tagRunes[i] {
			return false
		}
	}
	return true
}

// ringSuffixMatches checks if the ring buffer ends with the given tag runes (O(1) operation)
func (p *ThinkingParser) ringSuffixMatches(tagRunes []rune) bool {
	tagLen := len(tagRunes)

	if p.suffixRingSize < tagLen {
		return false
	}

	// Compare the last tagLen runes in the ring with the tag
	start := p.suffixRingSize - tagLen
	for i := 0; i < tagLen; i++ {
		if p.suffixRing[start+i] != tagRunes[i] {
			return false
		}
	}
	return true
}

func (p *ThinkingParser) FlushText() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.thinkingMode {
		return
	}
	if p.buffer.Len() == 0 {
		return
	}

	content := p.buffer.String()

	// Check if buffer ends with a potential partial tag that we should hold back
	// This handles streaming cases where tags are split across chunks
	// e.g., "<antml\" in one chunk and "b:thinking>" in the next
	holdBackLen := 0
	for i := len(content) - 1; i >= 0 && i >= len(content)-19; i-- {
		if content[i] == '<' {
			// Found a '<' near the end - check if it could be start of a tag we recognize
			suffix := content[i:]
			// Check if this could be the start of any thinking tag
			if isPotentialThinkingTagStart(suffix) {
				holdBackLen = len(content) - i
				break
			}
		}
	}

	if holdBackLen > 0 {
		if holdBackLen < len(content) {
			// Emit text before the potential tag start
			textToEmit := content[:len(content)-holdBackLen]
			if textToEmit != "" {
				p.events = append(p.events, ThinkingEvent{Type: "text", Content: textToEmit})
			}
			// Keep the potential tag start in buffer
			p.buffer.Reset()
			p.buffer.WriteString(content[len(content)-holdBackLen:])
			// Also update ring buffer to match
			p.resetRing()
			for _, r := range content[len(content)-holdBackLen:] {
				p.addToRing(r)
			}
		}
		// If holdBackLen == len(content), keep entire buffer (don't emit anything yet)
		// This handles cases where the entire content is a potential tag start
	} else {
		// No potential tag start, emit all content
		p.events = append(p.events, ThinkingEvent{Type: "text", Content: content})
		p.buffer.Reset()
	}
}

// isPotentialThinkingTagStart checks if a string could be the start of a thinking tag
func isPotentialThinkingTagStart(s string) bool {
	// Check against all supported thinking tag prefixes
	prefixes := []string{
		"<thinking>",
		"<think>",
		"<antml\\b:thinking>",
		"</thinking>",
		"</think>",
		"</antml\\b:thinking>",
		"</antml>",
	}
	for _, prefix := range prefixes {
		if len(s) <= len(prefix) && prefix[:len(s)] == s {
			return true
		}
	}
	return false
}

func (p *ThinkingParser) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.thinkingMode && p.buffer.Len() > 0 {
		p.events = append(p.events, ThinkingEvent{Type: "text", Content: p.buffer.String()})
	}
	if p.thinkingMode && p.thinkingBuffer.Len() > 0 {
		p.events = append(p.events, ThinkingEvent{Type: "thinking", Content: strings.TrimSpace(p.thinkingBuffer.String())})
	}
	p.events = append(p.events, ThinkingEvent{Type: "end"})
}

func (p *ThinkingParser) ConsumeEvents() []ThinkingEvent {
	p.mu.Lock()
	defer p.mu.Unlock()

	events := p.events
	p.events = nil
	return events
}

type TextAggregator struct {
	mu        sync.Mutex
	buffer    strings.Builder
	interval  time.Duration
	onFlush   func(string)
	lastFlush time.Time
	closed    bool
}

func NewTextAggregator(intervalMs int, onFlush func(string)) *TextAggregator {
	return &TextAggregator{
		interval:  time.Duration(intervalMs) * time.Millisecond,
		onFlush:   onFlush,
		lastFlush: time.Now(),
	}
}

// Add appends text to the buffer. Call MaybeFlush() periodically to check if flush is needed.
func (a *TextAggregator) Add(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return
	}

	a.buffer.WriteString(text)
}

// MaybeFlush flushes the buffer if the interval has elapsed since last flush.
// Returns true if flushed. This must be called from the same goroutine as Add/Flush/Close
// to maintain single-producer semantics.
func (a *TextAggregator) MaybeFlush() bool {
	a.mu.Lock()
	if a.closed || a.buffer.Len() == 0 {
		a.mu.Unlock()
		return false
	}
	if time.Since(a.lastFlush) < a.interval {
		a.mu.Unlock()
		return false
	}
	chunk := a.buffer.String()
	a.buffer.Reset()
	a.lastFlush = time.Now()
	a.mu.Unlock()

	a.onFlush(chunk)
	return true
}

// Flush immediately flushes any buffered content regardless of interval.
func (a *TextAggregator) Flush() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	if a.buffer.Len() == 0 {
		a.mu.Unlock()
		return
	}
	chunk := a.buffer.String()
	a.buffer.Reset()
	a.lastFlush = time.Now()
	a.mu.Unlock()

	a.onFlush(chunk)
}

// Close flushes any remaining content and marks the aggregator as closed.
func (a *TextAggregator) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	chunk := a.buffer.String()
	a.buffer.Reset()
	a.mu.Unlock()

	if chunk != "" {
		a.onFlush(chunk)
	}
}

// SSE writer tuning constants for lightweight backpressure.
// These values are tuned for interactive latency rather than bulk throughput.
const (
	sseWriterMaxQueue          = 100
	sseWriterDrainResetWindow  = 20 * time.Millisecond
	sseWriterBackoffOnOverflow = 10 * time.Millisecond
	sseWriterRetryBackoff      = 5 * time.Millisecond
)

// SSEWriter implements a lightweight backpressure-aware SSE writer.
// It uses a small in-memory queue and short sleep-based backoff to avoid
// overwhelming slow clients while keeping latency low for typical workloads.
//
// CONCURRENCY: This writer is designed for single-producer usage (one goroutine calling Send).
// Multiple concurrent producers will serialize through the mutex and may experience
// blocking during sleep/write operations. For multi-producer scenarios, consider using
// a buffered channel with a dedicated writer goroutine instead.
type SSEWriter struct {
	writer   io.Writer
	flusher  http.Flusher
	mu       sync.Mutex
	closed   bool
	maxQueue int
	pending  int
	lastSend time.Time
}

func NewSSEWriter(w io.Writer, f http.Flusher) *SSEWriter {
	return &SSEWriter{
		writer:   w,
		flusher:  f,
		maxQueue: sseWriterMaxQueue,
	}
}

func (s *SSEWriter) Send(event ClaudeStreamEvent, critical bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("SSE writer closed")
	}

	maxRetries := 1
	if critical {
		maxRetries = 3
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	payload := fmt.Sprintf("event: %s\ndata: %s\n\n", event.Type, string(data))

	for retry := 0; retry < maxRetries; retry++ {
		if time.Since(s.lastSend) > sseWriterDrainResetWindow {
			s.pending = 0
		}
		if s.pending >= s.maxQueue {
			time.Sleep(sseWriterBackoffOnOverflow)
			s.pending = 0
		}

		if _, err := s.writer.Write([]byte(payload)); err != nil {
			if retry == maxRetries-1 {
				s.closed = true
				return err
			}
			time.Sleep(sseWriterRetryBackoff)
			continue
		}

		s.pending++
		s.lastSend = time.Now()
		if s.flusher != nil {
			s.flusher.Flush()
		}
		return nil
	}

	return fmt.Errorf("failed to send SSE event after retries")
}

func (s *SSEWriter) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// handleCCStreamingResponse handles streaming response conversion for CC support.
func (ps *ProxyServer) handleCCStreamingResponse(c *gin.Context, resp *http.Response) {
	// NOTE: This handler is intentionally implemented as a single function.
	// Splitting into multiple files/types is desirable for maintainability, but it
	// can introduce subtle streaming regressions and extra allocations. Refactor
	// should be done only with dedicated benchmarks and test coverage.
	// Clear upstream encoding/length headers before writing synthesized SSE stream.
	// The proxy reconstructs the event stream from OpenAI format to Claude format,
	// so upstream headers (Content-Encoding, Content-Length, Transfer-Encoding) no longer apply.
	clearUpstreamEncodingHeaders(c)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		logrus.Error("Streaming unsupported for CC response")
		ps.handleCCNormalResponse(c, resp)
		return
	}

	streamWriter := io.Writer(c.Writer)
	var responseCapture *limitedResponseCaptureWriter
	if shouldCaptureResponse(c) {
		responseCapture = newLimitedResponseCaptureWriter(c.Writer, maxResponseCaptureBytes)
		streamWriter = responseCapture
		defer func() {
			if captured := responseCapture.String(); captured != "" {
				c.Set("response_body", sanitizeAndTruncateStringForLog(captured, maxResponseCaptureBytes))
			}
		}()
	}

	// Wrap the stream writer (including the response-capture variant) so the
	// first byte is recorded only after the client write succeeded, mirroring
	// the plain streaming handler. The wrapper sits outside the capture writer,
	// so capture behavior is unchanged.
	writer := NewSSEWriter(firstByteWriter{
		writer:      streamWriter,
		onFirstByte: func() { markFirstByte(c) },
	}, flusher)
	defer writer.Close()

	reqID := ""
	if c.Request != nil {
		reqID = c.Request.Header.Get("X-Request-ID")
	}
	triggerSignal := getTriggerSignal(c)

	msgID := ""
	msgUUID, err := uuid.NewRandom()
	if err != nil {
		msgID = "msg_fallback_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		logrus.WithError(err).Warn("CC: Failed to generate UUID for message_id, using fallback ID")
	} else {
		msgID = "msg_" + strings.ReplaceAll(msgUUID.String(), "-", "")
	}

	startEvent := ClaudeStreamEvent{
		Type: "message_start",
		Message: &ClaudeResponse{
			ID:      msgID,
			Type:    "message",
			Role:    "assistant",
			Content: []ClaudeContentBlock{},
			Model: func() string {
				if m := c.GetString("original_model"); m != "" {
					return m
				}
				return "unknown"
			}(),
			Usage: &ClaudeUsage{InputTokens: 0, OutputTokens: 0},
		},
	}
	if err := writer.Send(startEvent, true); err != nil {
		logrus.WithError(err).Warn("CC: Failed to write message_start event")
		return
	}

	logrus.WithFields(logrus.Fields{
		"msg_id":         msgID,
		"request_id":     reqID,
		"trigger_signal": triggerSignal,
	}).Debug("CC: Started streaming response")

	// Handle gzip/deflate/br decompression for streaming response
	// OpenAI API may return gzip-compressed streaming responses
	bodyReader := resp.Body
	contentEncoding := resp.Header.Get("Content-Encoding")
	if contentEncoding != "" {
		var err error
		bodyReader, err = utils.NewDecompressReader(contentEncoding, resp.Body)
		if err != nil {
			// Decompression failed - emit error event and return early
			// Continuing with compressed body would break SSE parsing and hang the client
			logrus.WithError(err).WithField("content_encoding", contentEncoding).
				Warn("CC: Failed to create decompression reader")

			// Send error event to client using existing writer
			_ = writer.Send(ClaudeStreamEvent{
				Type: "error",
				Error: &ClaudeError{
					Type:    "api_error",
					Message: "Failed to decompress upstream stream",
				},
			}, true)
			return
		}
		logrus.WithField("content_encoding", contentEncoding).
			Debug("CC: Created decompression reader for streaming response")
		// Ensure decompression reader is closed
		defer func() {
			if closer, ok := bodyReader.(io.Closer); ok && closer != resp.Body {
				closer.Close()
			}
		}()
	}

	// Use timeout-enabled SSE reader for CC support to prevent hanging when
	// upstream models (e.g., deepseek-reasoner) are in thinking phase without sending data.
	// Timeout values are derived from group/system config with preset upper bounds.
	firstByteTimeout, subsequentTimeout := getEffectiveSSETimeouts(c)
	reader := NewSSEReaderWithTimeout(bodyReader, firstByteTimeout, subsequentTimeout)
	// Release the reader goroutine on every return path: with Close, a send
	// blocked because the consumer abandoned the stream (e.g. after a read
	// timeout) is cancelled instead of leaking until the upstream stops.
	defer reader.Close()
	contentBlockIndex := 0
	var currentToolCall *OpenAIToolCall
	var currentToolCallName string
	var currentToolCallArgs strings.Builder
	var accumulatedContent strings.Builder
	var outputEstimate estimatedTokenCapture
	contentBufFullWarned := false
	cleanupMode := cleanupModeArtifactsOnly
	if isFunctionCallEnabled(c) {
		cleanupMode = cleanupModeFull
	}
	parser := NewThinkingParser()
	textBlockOpen := false
	thinkingBlockOpen := false // Track if thinking block is open for content merging
	var aggregator *TextAggregator
	hasValidToolCalls := false  // Track if any valid tool_calls were processed
	isErrorRecovery := false    // Track if error recovery was triggered (don't downgrade stop_reason)
	toolBlockStartSent := false // Track if content_block_start was sent for current tool block

	// Get tool name reverse map from context for restoring original tool names
	reverseToolNameMap := getOpenAIToolNameReverseMap(c)

	// Buffer to hold potential partial malformed tags across aggregator flushes
	var partialTagBuffer strings.Builder

	sanitizeText := func(text string) string {
		if triggerSignal != "" {
			text = strings.ReplaceAll(text, triggerSignal, "")
		}
		// Use the comprehensive removeFunctionCallsBlocks function to clean all
		// function call XML formats (function_calls, function_call, invoke,
		// invocation, tool_call, and trigger signals)
		text = removeFunctionCallsBlocks(text, cleanupMode)
		// Convert Windows paths to Unix-style for Claude Code compatibility
		text = convertWindowsPathsInToolResult(text)
		return text
	}

	// sanitizeTextWithPartialDetection handles streaming text that may contain
	// partial malformed tags split across chunks. It buffers potential partial
	// tags and only emits text that is safe to display.
	sanitizeTextWithPartialDetection := func(text string) string {
		// Prepend any buffered partial content from previous flush
		if partialTagBuffer.Len() > 0 {
			text = partialTagBuffer.String() + text
			partialTagBuffer.Reset()
		}

		// Check if text ends with a potential partial malformed tag
		// Patterns to detect: <>, <><, <><invokename, <><parametername, etc.
		holdBackLen := 0
		for i := len(text) - 1; i >= 0 && i >= len(text)-100; i-- {
			if text[i] == '<' {
				suffix := text[i:]
				// Check if this could be start of a malformed tag pattern.
				// NOTE: isPotentialMalformedTagStart is implemented in function_call.go and shared
				// between CC support and function call sanitizers.
				if isPotentialMalformedTagStart(suffix) {
					holdBackLen = len(text) - i
					break
				}
			}
		}

		if holdBackLen > 0 && holdBackLen < len(text) {
			// Hold back the potential partial tag
			partialTagBuffer.WriteString(text[len(text)-holdBackLen:])
			text = text[:len(text)-holdBackLen]
		} else if holdBackLen == len(text) {
			// Entire text is a potential partial tag, buffer it all
			partialTagBuffer.WriteString(text)
			return ""
		}

		return sanitizeText(text)
	}

	// flushPartialTagBuffer flushes any remaining partial tag buffer content
	// This is called at finalize to ensure no content is lost
	flushPartialTagBuffer := func() string {
		if partialTagBuffer.Len() == 0 {
			return ""
		}
		content := partialTagBuffer.String()
		partialTagBuffer.Reset()
		return sanitizeText(content)
	}

	ensureTextBlock := func() error {
		if textBlockOpen {
			return nil
		}
		startBlock := ClaudeStreamEvent{
			Type:  "content_block_start",
			Index: contentBlockIndex,
			ContentBlock: &ClaudeContentBlock{
				Type: "text",
				Text: "",
			},
		}
		if err := writer.Send(startBlock, true); err != nil {
			return err
		}
		textBlockOpen = true
		return nil
	}

	closeTextBlock := func() {
		if !textBlockOpen {
			return
		}
		stopEvent := ClaudeStreamEvent{Type: "content_block_stop", Index: contentBlockIndex}
		if err := writer.Send(stopEvent, true); err != nil {
			logrus.WithError(err).Debug("CC: Failed to stop text block")
			return
		}
		contentBlockIndex++
		textBlockOpen = false
	}

	// closeThinkingBlock closes the current thinking block if open.
	// This is called before switching to text or tool_use blocks.
	closeThinkingBlock := func() {
		if !thinkingBlockOpen {
			return
		}
		stopEvent := ClaudeStreamEvent{Type: "content_block_stop", Index: contentBlockIndex}
		if err := writer.Send(stopEvent, true); err != nil {
			logrus.WithError(err).Debug("CC: Failed to stop thinking block")
			return
		}
		contentBlockIndex++
		thinkingBlockOpen = false
	}

	closeToolBlock := func() {
		if currentToolCall == nil {
			return
		}

		argsLen := currentToolCallArgs.Len()
		argsStr := currentToolCallArgs.String()
		logrus.WithFields(logrus.Fields{
			"tool_id":          currentToolCall.ID,
			"tool_name":        currentToolCallName,
			"args_len":         argsLen,
			"content_index":    contentBlockIndex,
			"block_start_sent": toolBlockStartSent,
		}).Debug("CC: closeToolBlock() called")

		// Validate arguments before emitting - skip empty/placeholder tool_calls
		// Some upstream models (e.g., deepseek-reasoner in thinking mode) may return
		// tool_calls with empty arguments as placeholders during reasoning phase.
		//
		// Per AI review: This validation now happens BEFORE sending content_block_start.
		// Previously, content_block_start was sent when first args chunk arrived, but if
		// accumulated args became "{}" (e.g., "{" + "}"), closeToolBlock would skip
		// content_block_stop, corrupting the SSE block sequence.
		// Now we defer content_block_start until here, ensuring start/delta/stop are
		// always emitted together or not at all.
		if !isValidToolCallArguments(currentToolCallName, argsStr) {
			logrus.WithFields(logrus.Fields{
				"tool_name": currentToolCallName,
				"tool_id":   currentToolCall.ID,
			}).Warn("CC: Skipping tool_call with empty arguments in streaming mode")
			// Reset state without sending any events
			currentToolCall = nil
			currentToolCallName = ""
			currentToolCallArgs.Reset()
			toolBlockStartSent = false
			return
		}

		// Now we know args are valid; emit complete tool_use block (start -> delta -> stop)
		hasValidToolCalls = true
		outputEstimate.addString(argsStr)

		// Send content_block_start if not already sent
		if !toolBlockStartSent {
			startEvent := ClaudeStreamEvent{
				Type:         "content_block_start",
				Index:        contentBlockIndex,
				ContentBlock: &ClaudeContentBlock{Type: "tool_use", ID: currentToolCall.ID, Name: currentToolCallName},
			}
			if err := writer.Send(startEvent, true); err != nil {
				logrus.WithError(err).Debug("CC: Failed to start tool_use block")
				currentToolCall = nil
				currentToolCallName = ""
				currentToolCallArgs.Reset()
				toolBlockStartSent = false
				return
			}
			toolBlockStartSent = true
		}

		if currentToolCallName != "" && argsLen > 0 {
			// When force function call is enabled, normalize arguments to fix potential
			// issues like Windows path escapes and tool-specific formatting.
			// When only CC support is enabled (no force FC), pass through arguments
			// unchanged to preserve upstream formatting.
			if isFunctionCallEnabled(c) {
				if normalized, ok := normalizeOpenAIToolCallArguments(currentToolCallName, argsStr); ok {
					argsStr = normalized
				}
			}

			// CRITICAL: Fix for Claude Code Windows path escape issue in Bash commands.
			// Claude Code client performs additional escape processing on Bash command strings,
			// which corrupts Windows paths. We double-escape backslashes ONLY in the "command"
			// field to compensate for this.
			// See: https://github.com/anthropics/claude-code/issues/15290
			argsStr = doubleEscapeWindowsPathsForBash(argsStr)

			deltaEvent := ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: contentBlockIndex,
				Delta: &ClaudeStreamDelta{Type: "input_json_delta", PartialJSON: argsStr},
			}
			if err := writer.Send(deltaEvent, false); err != nil {
				logrus.WithError(err).Debug("CC: Failed to write tool_use delta")
			}
			// Log the actual arguments being sent for debugging path issues
			logrus.WithFields(logrus.Fields{
				"tool_name":    currentToolCallName,
				"args_len":     len(argsStr),
				"args_preview": utils.TruncateString(argsStr, 200),
			}).Debug("CC: Emitted tool_use input_json_delta")
		}

		stopEvent := ClaudeStreamEvent{Type: "content_block_stop", Index: contentBlockIndex}
		if err := writer.Send(stopEvent, true); err != nil {
			logrus.WithError(err).Debug("CC: Failed to stop tool block")
			currentToolCall = nil
			currentToolCallName = ""
			currentToolCallArgs.Reset()
			toolBlockStartSent = false
			return
		}
		contentBlockIndex++
		currentToolCall = nil
		currentToolCallName = ""
		currentToolCallArgs.Reset()
		toolBlockStartSent = false
	}

	// ensureThinkingBlock ensures a thinking block is open for content merging.
	// Following b4u2cc reference: thinking content should be merged into a single block
	// instead of creating separate blocks for each fragment.
	ensureThinkingBlock := func() error {
		if thinkingBlockOpen {
			return nil
		}
		startEvent := ClaudeStreamEvent{
			Type:         "content_block_start",
			Index:        contentBlockIndex,
			ContentBlock: &ClaudeContentBlock{Type: "thinking", Thinking: ""},
		}
		if err := writer.Send(startEvent, true); err != nil {
			return err
		}
		thinkingBlockOpen = true
		return nil
	}

	// emitThinking emits thinking content, merging into the current thinking block.
	// Per b4u2cc reference implementation: thinking content should be accumulated
	// into a single thinking block rather than creating separate blocks for each fragment.
	// This ensures Claude Code displays "∴ Thinking…" as a single merged block.
	emitThinking := func(content string) {
		aggregator.Flush()
		closeTextBlock()
		// CRITICAL: Sanitize thinking content to remove malformed XML/JSON that can cause
		// CC auto-pause issues. This handles cases where model outputs malformed content
		// like <>[": "task",Form":...] or </antml\b:format> inside thinking blocks.
		leadingWhitespace := content[:len(content)-len(strings.TrimLeft(content, " \t\r\n"))]
		trailingWhitespace := content[len(strings.TrimRight(content, " \t\r\n")):]
		thinking := sanitizeText(content)
		if thinking == "" && thinkingBlockOpen && strings.TrimSpace(content) == "" {
			// Once a thinking block exists, whitespace-only chunks are meaningful token separators.
			thinking = content
		}
		if thinking == "" || (!thinkingBlockOpen && strings.TrimSpace(thinking) == "") {
			return
		}
		if thinkingBlockOpen && leadingWhitespace != "" && !strings.HasPrefix(thinking, leadingWhitespace) {
			thinking = leadingWhitespace + thinking
		}
		if trailingWhitespace != "" && !strings.HasSuffix(thinking, trailingWhitespace) {
			thinking += trailingWhitespace
		}
		if !thinkingBlockOpen {
			thinking = strings.TrimLeft(thinking, " \t\r\n")
		}
		if err := ensureThinkingBlock(); err != nil {
			logrus.WithError(err).Debug("CC: Failed to start thinking block")
			return
		}
		deltaEvent := ClaudeStreamEvent{
			Type:  "content_block_delta",
			Index: contentBlockIndex,
			Delta: &ClaudeStreamDelta{Type: "thinking_delta", Thinking: thinking},
		}
		if err := writer.Send(deltaEvent, false); err != nil {
			logrus.WithError(err).Debug("CC: Failed to send thinking delta")
		}
	}

	emitToolUseBlocks := func(blocks []ClaudeContentBlock) {
		for i, toolUse := range blocks {
			startEvent := ClaudeStreamEvent{
				Type:         "content_block_start",
				Index:        contentBlockIndex,
				ContentBlock: &ClaudeContentBlock{Type: "tool_use", ID: toolUse.ID, Name: toolUse.Name},
			}
			if err := writer.Send(startEvent, true); err != nil {
				logrus.WithError(err).Debug("CC+FC: Failed to start tool_use block")
				continue
			}

			if len(toolUse.Input) > 0 {
				deltaEvent := ClaudeStreamEvent{
					Type:  "content_block_delta",
					Index: contentBlockIndex,
					Delta: &ClaudeStreamDelta{Type: "input_json_delta", PartialJSON: string(toolUse.Input)},
				}
				if err := writer.Send(deltaEvent, false); err != nil {
					logrus.WithError(err).Debug("CC+FC: Failed to send tool_use delta")
				}
			}

			stopEvent := ClaudeStreamEvent{Type: "content_block_stop", Index: contentBlockIndex}
			if err := writer.Send(stopEvent, true); err != nil {
				logrus.WithError(err).Debug("CC+FC: Failed to stop tool_use block")
			}
			contentBlockIndex++

			logrus.WithFields(logrus.Fields{"tool_index": i, "tool_name": toolUse.Name, "tool_id": toolUse.ID}).Debug("CC+FC: Emitted tool_use block in streaming response")
		}
	}

	// NOTE: TextAggregator interval is set to 50ms to balance interactive latency with network efficiency.
	// This value provides good responsiveness while reducing processing overhead for streaming responses.
	// Increased from 35ms to allow more content aggregation per flush, improving parsing accuracy.
	aggregator = NewTextAggregator(50, func(text string) {
		// Use partial detection to handle malformed tags split across chunks
		cleaned := sanitizeTextWithPartialDetection(text)
		if cleaned == "" {
			return
		}
		// Close thinking block before opening text block per b4u2cc reference
		// This ensures proper block sequencing: thinking -> text -> tool_use
		closeThinkingBlock()
		if err := ensureTextBlock(); err != nil {
			logrus.WithError(err).Debug("CC: Failed to start text block")
			return
		}
		deltaEvent := ClaudeStreamEvent{
			Type:  "content_block_delta",
			Index: contentBlockIndex,
			Delta: &ClaudeStreamDelta{Type: "text_delta", Text: cleaned},
		}
		if err := writer.Send(deltaEvent, false); err != nil {
			logrus.WithError(err).Debug("CC: Failed to write text delta")
		}
	})
	defer aggregator.Close()

	finalize := func(stopReason string, usage *OpenAIUsage, success bool) {
		initialStopReason := stopReason
		logrus.WithFields(logrus.Fields{
			"msg_id":                  msgID,
			"request_id":              reqID,
			"trigger_signal":          triggerSignal,
			"initial_stop_reason":     initialStopReason,
			"accumulated_content_len": accumulatedContent.Len(),
			"function_call_enabled":   isFunctionCallEnabled(c),
		}).Debug("CC: finalize() called")

		parser.Finish()
		for _, evt := range parser.ConsumeEvents() {
			switch evt.Type {
			case "text":
				aggregator.Add(evt.Content)
			case "thinking":
				emitThinking(evt.Content)
			}
		}

		aggregator.Flush()

		// Flush any remaining partial tag buffer content
		if remaining := flushPartialTagBuffer(); remaining != "" {
			closeThinkingBlock()
			if err := ensureTextBlock(); err == nil {
				deltaEvent := ClaudeStreamEvent{
					Type:  "content_block_delta",
					Index: contentBlockIndex,
					Delta: &ClaudeStreamDelta{Type: "text_delta", Text: remaining},
				}
				_ = writer.Send(deltaEvent, false)
			}
		}

		closeThinkingBlock() // Close thinking block before text block per b4u2cc reference
		closeTextBlock()
		closeToolBlock()

		if accumulatedContent.Len() > 0 && isFunctionCallEnabled(c) {
			content := accumulatedContent.String()
			logrus.WithFields(logrus.Fields{
				"content_len": len(content),
			}).Debug("CC+FC: Parsing accumulated content for tool calls")
			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, content)
			logrus.WithField("tool_use_blocks_count", len(toolUseBlocks)).Debug("CC+FC: parseFunctionCallsFromContentForCC returned")
			if len(toolUseBlocks) > 0 {
				for i, block := range toolUseBlocks {
					logrus.WithFields(logrus.Fields{
						"index":     i,
						"tool_name": block.Name,
						"tool_id":   block.ID,
					}).Debug("CC+FC: Tool block to emit")
				}
				emitToolUseBlocks(toolUseBlocks)
				stopReason = "tool_use"
				logrus.WithFields(logrus.Fields{
					"tool_use_count":      len(toolUseBlocks),
					"stop_reason_changed": stopReason,
				}).Debug("CC+FC: Changed stop_reason to tool_use")
			}
		} else {
			logrus.WithFields(logrus.Fields{
				"accumulated_content_len": accumulatedContent.Len(),
				"function_call_enabled":   isFunctionCallEnabled(c),
			}).Debug("CC+FC: Skipped tool call parsing (no content or FC disabled)")
			// When upstream finish_reason=tool_calls but there are no valid tool calls and FC is disabled,
			// downgrade stop_reason to end_turn to avoid clients waiting for non-existent tool results.
			// Exception: don't downgrade if this is an error recovery attempt (isErrorRecovery=true).
			// isErrorRecovery is only set when an SSE error event has been sent to the client,
			// which prioritizes surfacing the upstream error instead of masking it as end_turn.
			if stopReason == "tool_use" && !hasValidToolCalls && !isFunctionCallEnabled(c) && !isErrorRecovery {
				stopReason = "end_turn"
			}
		}

		usagePayload := &ClaudeUsage{InputTokens: 0, OutputTokens: 0}
		if usage != nil {
			usagePayload.InputTokens = usage.PromptTokens
			usagePayload.OutputTokens = usage.CompletionTokens
			setTokenUsageCounts(c, int64(usage.PromptTokens), int64(usage.CompletionTokens), int64(usage.TotalTokens))
		} else if estimatedOutputTokens := outputEstimate.Tokens(); success && estimatedOutputTokens > 0 {
			setEstimatedOutputTokens(c, estimatedOutputTokens)
			usagePayload.OutputTokens = int(estimatedOutputTokens)
			// Keep fallback tokens in the estimated path so request logs do not mark them as upstream usage.
		}
		applyTokenMultiplier(usagePayload)

		logrus.WithFields(logrus.Fields{
			"final_stop_reason": stopReason,
			"initial_was":       initialStopReason,
			"changed":           stopReason != initialStopReason,
		}).Debug("CC: FINAL stop_reason for message_delta")

		deltaEvent := ClaudeStreamEvent{Type: "message_delta", Delta: &ClaudeStreamDelta{StopReason: stopReason}, Usage: usagePayload}
		if err := writer.Send(deltaEvent, true); err != nil {
			logrus.WithError(err).Error("CC: Failed to write message_delta")
			return
		}
		if err := writer.Send(ClaudeStreamEvent{Type: "message_stop"}, true); err != nil {
			logrus.WithError(err).Error("CC: Failed to write message_stop")
		}
		logrus.WithFields(logrus.Fields{
			"msg_id":         msgID,
			"request_id":     reqID,
			"trigger_signal": triggerSignal,
			"stop_reason":    stopReason,
		}).Info("CC: Stream finalized successfully with stop_reason")
	}

	streamStopReason := "end_turn"
	var streamUsage *OpenAIUsage

	for {
		event, err := reader.ReadEvent()
		if err != nil {
			if err == io.EOF {
				logrus.Debug("CC: Upstream stream EOF")
				// Ensure final events are sent on EOF to prevent client hanging
				finalize(streamStopReason, streamUsage, false)
			} else if errors.Is(err, ErrSSETimeout) {
				// Handle timeout error - send error event to client instead of hanging
				logrus.WithError(err).Warn("CC: SSE read timeout, sending error to client")
				isErrorRecovery = true
				// Send error event to client
				// Anthropic documents timeout_error as HTTP 504. Preserve that protocol
				// distinction so clients can classify this transient failure correctly.
				errorEvent := ClaudeStreamEvent{
					Type: "error",
					Error: &ClaudeError{
						Type:    "timeout_error",
						Message: "Upstream did not respond within the expected time. The model may be processing a complex request.",
					},
				}
				if sendErr := writer.Send(errorEvent, true); sendErr != nil {
					logrus.WithError(sendErr).Error("CC: Failed to send timeout error event")
				}
				// Send final events to properly terminate the stream
				finalize(streamStopReason, streamUsage, false)
			} else {
				logrus.WithError(err).Error("CC: Error reading stream")
				// Send final events on error to ensure client receives termination
				finalize(streamStopReason, streamUsage, false)
			}
			break
		}

		if event.Data == "[DONE]" {
			finalize(streamStopReason, streamUsage, true)
			logrus.Debug("CC: Stream finished successfully")
			break
		}

		var openaiChunk OpenAIResponse
		if err := json.Unmarshal([]byte(event.Data), &openaiChunk); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"event_type": event.Event, "data_preview": utils.TruncateString(event.Data, 512)}).Debug("CC: Failed to parse OpenAI chunk as JSON, skipping")
			continue
		}

		if openaiChunk.Usage != nil {
			streamUsage = openaiChunk.Usage
		}

		if openaiChunk.Error != nil {
			isErrorRecovery = true
			errorMessage := strings.TrimSpace(utils.SanitizeErrorBody(openaiChunk.Error.Message))
			if errorMessage == "" {
				errorMessage = "Upstream returned an error"
			}
			errEvent := ClaudeStreamEvent{
				Type: "error",
				Error: &ClaudeError{
					Type:    apiErrorTypeToClaudeErrorType(openaiChunk.Error.Type),
					Message: errorMessage,
				},
			}
			if err := writer.Send(errEvent, true); err != nil {
				logrus.WithError(err).Debug("CC: Failed to send upstream error event")
				return
			}
			finalize(streamStopReason, streamUsage, false)
			break
		}

		if len(openaiChunk.Choices) == 0 {
			continue
		}

		choice := openaiChunk.Choices[0]
		delta := choice.Delta
		if delta == nil {
			continue
		}

		// Handle reasoning_content from DeepSeek reasoner models.
		// DeepSeek reasoner outputs: reasoning_content first, then content.
		// Tool calls may appear in either field or both, so we accumulate both.
		// This is emitted as thinking content in Claude format.
		if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
			reasoningStr := *delta.ReasoningContent
			outputEstimate.addString(reasoningStr)
			// Accumulate for tool call parsing in finalize()
			if accumulatedContent.Len()+len(reasoningStr) <= maxContentBufferBytes {
				accumulatedContent.WriteString(reasoningStr)
			} else if !contentBufFullWarned {
				logrus.WithFields(logrus.Fields{
					"buffer_limit_kb": maxContentBufferBytes / 1024,
					"accumulated_len": accumulatedContent.Len(),
				}).Warn("CC: content buffer limit reached during reasoning streaming; tool call parsing may be incomplete")
				contentBufFullWarned = true
			}
			// Emit reasoning content as thinking block
			emitThinking(reasoningStr)
		}

		// Handle content field (may contain tool calls after reasoning_content)
		if delta.Content != nil && *delta.Content != "" {
			contentStr := *delta.Content
			outputEstimate.addString(contentStr)
			if accumulatedContent.Len()+len(contentStr) <= maxContentBufferBytes {
				accumulatedContent.WriteString(contentStr)
			} else if !contentBufFullWarned {
				logrus.WithFields(logrus.Fields{
					"buffer_limit_kb": maxContentBufferBytes / 1024,
					"accumulated_len": accumulatedContent.Len(),
				}).Warn("CC: content buffer limit reached during streaming; tool call parsing may be incomplete")
				contentBufFullWarned = true
			}

			for _, r := range contentStr {
				parser.FeedRune(r)
			}
			parser.FlushText()
			for _, evt := range parser.ConsumeEvents() {
				switch evt.Type {
				case "text":
					aggregator.Add(evt.Content)
				case "thinking":
					emitThinking(evt.Content)
				}
			}
			// Check if aggregator needs flushing (single-producer: called from main loop)
			aggregator.MaybeFlush()
		}

		if len(delta.ToolCalls) > 0 {
			aggregator.Flush()
			closeThinkingBlock() // Close thinking block before tool_use per b4u2cc reference
			closeTextBlock()
			for _, tc := range delta.ToolCalls {
				call := tc
				logrus.WithFields(logrus.Fields{
					"tool_id":   call.ID,
					"tool_name": call.Function.Name,
					"args_len":  len(call.Function.Arguments),
				}).Debug("CC: Received delta.ToolCall")

				// OpenAI streaming tool_call format:
				// - First chunk: {id: "call_xxx", function: {name: "Glob", arguments: ""}}
				// - Subsequent chunks: {id: "", function: {name: "", arguments: "{\"pat"}}
				// We need to accumulate arguments from subsequent chunks to the current tool_call.
				if call.ID == "" {
					// This is a continuation chunk with only arguments
					if currentToolCall != nil && call.Function.Arguments != "" {
						// Per AI review: Defer content_block_start until closeToolBlock().
						// This prevents SSE block sequence corruption when args like "{" + "}"
						// accumulate to "{}" which would fail validation in closeToolBlock.
						currentToolCallArgs.WriteString(call.Function.Arguments)
						logrus.WithFields(logrus.Fields{
							"tool_id":        currentToolCall.ID,
							"chunk_args_len": len(call.Function.Arguments),
							"total_args_len": currentToolCallArgs.Len(),
						}).Debug("CC: Accumulated tool call arguments (continuation chunk)")
					}
					continue
				}
				isNew := currentToolCall == nil || currentToolCall.ID != call.ID
				if isNew && currentToolCall != nil && currentToolCall.ID != call.ID {
					closeToolBlock()
				}
				if isNew {
					if call.Function.Name == "" {
						logrus.WithField("tool_id", call.ID).Debug("CC: Skipping tool call with empty name")
						continue
					}
					currentToolCall = &call
					// Restore original tool name if it was shortened
					currentToolCallName = call.Function.Name
					if reverseToolNameMap != nil {
						if orig, ok := reverseToolNameMap[call.Function.Name]; ok {
							currentToolCallName = orig
						}
					}
					currentToolCallArgs.Reset()
					// NOTE: Don't set hasValidToolCalls here - wait until we have valid arguments
					// This prevents empty tool_calls from being counted as valid
					logrus.WithFields(logrus.Fields{
						"tool_id":   call.ID,
						"tool_name": call.Function.Name,
					}).Debug("CC: Buffering new tool_use block (waiting for arguments)")
					// NOTE: Don't send content_block_start here - defer until we have arguments
					// This prevents sending tool_use blocks with empty arguments to Claude Code
				}

				if call.Function.Arguments != "" && currentToolCall != nil {
					// Per AI review: Defer content_block_start until closeToolBlock().
					// This prevents SSE block sequence corruption when args like "{" + "}"
					// accumulate to "{}" which would fail validation in closeToolBlock.
					currentToolCallArgs.WriteString(call.Function.Arguments)
					logrus.WithFields(logrus.Fields{
						"tool_id":        call.ID,
						"chunk_args_len": len(call.Function.Arguments),
						"total_args_len": currentToolCallArgs.Len(),
						"chunk_preview":  utils.TruncateString(call.Function.Arguments, 200),
					}).Debug("CC: Accumulated tool call arguments")
				}
			}
		}

		if choice.FinishReason != nil {
			closeToolBlock()
			stopReason, isError := convertFinishReasonToStopReason(*choice.FinishReason)

			// Handle error finish_reason (network_error, timeout, etc.)
			// If we have valid tool calls, the tools were executed successfully,
			// so we should let the stream end normally with tool_use stop_reason.
			// This allows Claude Code to continue processing tool results.
			if isError {
				if hasValidToolCalls {
					// Tool calls succeeded, override to tool_use so client processes results
					logrus.WithFields(logrus.Fields{
						"finish_reason":        *choice.FinishReason,
						"has_valid_tool_calls": hasValidToolCalls,
					}).Debug("CC: Upstream error but tool calls succeeded, using tool_use stop_reason")
					stopReason = "tool_use"
				} else if accumulatedContent.Len() == 0 {
					// No content and no tool calls - send SSE error event to notify client.
					// This allows Claude Code to display the error and let user decide to retry.
					logrus.WithField("finish_reason", *choice.FinishReason).
						Warn("CC: Upstream error with no content, sending error event")

					// Send SSE error event with upstream error info
					errEvent := ClaudeStreamEvent{
						Type: "error",
						Error: &ClaudeError{
							Type:    "api_error",
							Message: fmt.Sprintf("Upstream returned %s with no content", *choice.FinishReason),
						},
					}
					if err := writer.Send(errEvent, true); err != nil {
						logrus.WithError(err).Debug("CC: Failed to send error event")
					}
					isErrorRecovery = true
				}
			}

			// If upstream says tool_calls but we didn't receive any valid tool calls,
			// convert to end_turn to prevent Claude Code from hanging waiting for tool results
			// NOTE: Similar to non-streaming handler but NOT extracted - checks accumulated
			// hasValidToolCalls flag vs. claudeResp.Content array. KISS principle applies.
			if *choice.FinishReason == "tool_calls" && !hasValidToolCalls {
				logrus.WithField("original_finish_reason", *choice.FinishReason).
					Warn("CC: Received tool_calls finish_reason but no valid tool calls were processed, converting to end_turn")
				stopReason = "end_turn"
			}
			streamStopReason = stopReason
			logrus.WithField("upstream_finish_reason", *choice.FinishReason).Debug("CC: Stream finished with upstream finish_reason")
		}
	}
}

// marshalStringAsJSONRaw safely marshals a string into json.RawMessage for CC conversion paths.
// When marshalling fails (which is rare for plain strings), it logs a warning and returns an
// empty JSON string literal to keep the upstream payload structurally valid.
func marshalStringAsJSONRaw(label string, value string) json.RawMessage {
	bytes, err := json.Marshal(value)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"label": label,
		}).WithError(err).Warn("CC: Failed to marshal string content, using empty")
		return json.RawMessage(`""`)
	}
	return json.RawMessage(bytes)
}

// appendToContent appends a suffix string to an existing json.RawMessage content, preserving
// the existing structure when possible. If the content is not a plain string, it falls back
// to returning the original content to avoid corrupting structured payloads.
func appendToContent(content json.RawMessage, suffix string) json.RawMessage {
	if len(content) == 0 {
		return marshalStringAsJSONRaw("thinking_hint", suffix)
	}

	var existing string
	if err := json.Unmarshal(content, &existing); err == nil {
		updated := existing + suffix
		if out, err := json.Marshal(updated); err == nil {
			return json.RawMessage(out)
		}
	}

	var parts []map[string]any
	if err := json.Unmarshal(content, &parts); err == nil {
		parts = append(parts, map[string]any{"type": "text", "text": suffix})
		if out, err := json.Marshal(parts); err == nil {
			return json.RawMessage(out)
		}
	}

	// Fallback: return original content if unable to append
	// This prevents corruption but hints may be lost for unexpected content shapes
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		// Only log metadata to avoid potential PII leakage
		logrus.WithFields(logrus.Fields{
			"content_len":  len(content),
			"content_type": "json.RawMessage",
		}).Debug("CC: Unable to append thinking hint, unexpected content format")
	}
	return content
}

// SSE timeout defaults for CC support streaming mode. Both are disabled by default;
// stream_first_byte_timeout may enable only the wait for the first SSE event.
const (
	// sseFirstByteTimeoutPreset disables the first-byte timeout by default.
	sseFirstByteTimeoutPreset = 0

	// sseSubsequentTimeoutPreset is the maximum time to wait between SSE events
	// after the first event has been received. Set to 60 seconds to allow
	// for reasonable pauses during model generation.
	sseSubsequentTimeoutPreset = 0

	// nonStreamFirstByteTimeoutPreset is the maximum time to wait for the first byte
	// in non-streaming mode. Set to 60 minutes to allow for complex reasoning tasks.
	nonStreamFirstByteTimeoutPreset = 60 * time.Minute
)

// Backward compatibility aliases for external references (e.g., codex_cc_support.go)
const (
	sseFirstByteTimeout       = sseFirstByteTimeoutPreset
	sseSubsequentTimeout      = sseSubsequentTimeoutPreset
	nonStreamFirstByteTimeout = nonStreamFirstByteTimeoutPreset
)

// getEffectiveSSETimeouts calculates effective SSE timeout values based on group config.
// The EffectiveConfig already merges group-level overrides with system settings,
// so we only need to compare against preset upper bounds.
//
// Logic: min(preset_value, effective_config_value)
// - If config value < preset value: use config value (allows stricter timeouts)
// - If config value >= preset value: use preset value (prevents excessively long timeouts)
// - StreamFirstByteTimeout controls only the first SSE event wait; subsequent reads are unbounded.
//
// Timeout mapping:
// - firstByteTimeout: derived from ResponseHeaderTimeout (time to wait for first response)
// - subsequentTimeout: always disabled because stream timeout is no longer a lifecycle/idle timeout
func getEffectiveSSETimeouts(c *gin.Context) (firstByteTimeout, subsequentTimeout time.Duration) {
	// Default to no timeout; an explicit positive group/system value enables it.
	firstByteTimeout = sseFirstByteTimeoutPreset
	subsequentTimeout = sseSubsequentTimeoutPreset

	// Try to get group from context
	gv, ok := c.Get("group")
	if !ok {
		return
	}
	group, ok := gv.(*models.Group)
	if !ok || group == nil {
		return
	}

	// EffectiveConfig already contains merged group + system settings
	cfg := group.EffectiveConfig

	// Apply the configured first-byte timeout (stricter than the preset).
	if cfg.StreamFirstByteTimeout > 0 {
		firstByteTimeout = time.Duration(cfg.StreamFirstByteTimeout) * time.Second
		// Single request-scoped deadline: the transport (ResponseHeaderTimeout) already
		// waited up to the same budget for response headers, so the SSE reader must only
		// receive the remaining time. This prevents the stream from awaiting nearly two
		// full first-byte timeouts (headers + first event) for the first SSE event.
		// When StreamFirstByteTimeout is 0 both the header wait and the read-side wait
		// are unbounded (preset 0 = no timeout), so the anchor is only consulted here.
		if startVal, ok := c.Get(ctxKeyUpstreamRequestStart); ok {
			if start, ok := startVal.(time.Time); ok && !start.IsZero() {
				elapsed := time.Since(start)
				if elapsed >= firstByteTimeout {
					// Deadline already exhausted while waiting for headers: fail fast on
					// the next read. 0 would mean "no timeout", so keep a tiny positive
					// value so the reader reports ErrSSETimeout almost immediately.
					firstByteTimeout = time.Millisecond
				} else {
					firstByteTimeout -= elapsed
				}
			}
		}
	}
	subsequentTimeout = 0

	return
}

// ErrSSETimeout is returned when SSE read times out waiting for data.
var ErrSSETimeout = errors.New("SSE read timeout: upstream did not send data within the expected time")

// SSEReader reads Server-Sent Events from a reader.
type SSEReader struct {
	reader *bufio.Reader
}

// SSEEvent represents a Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
}

// NewSSEReader creates a new SSE reader.
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{reader: bufio.NewReader(r)}
}

// ReadEvent reads the next SSE event.
func (r *SSEReader) ReadEvent() (*SSEEvent, error) {
	event := &SSEEvent{}
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			if event.Data != "" {
				return event, nil
			}
			continue
		}

		// Skip SSE comment lines
		if strings.HasPrefix(line, ":") {
			continue
		}

		if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if event.Data != "" {
				event.Data += "\n" + data
			} else {
				event.Data = data
			}
		}
	}
}

// SSEReaderWithTimeout wraps SSEReader with timeout support for streaming reads.
// This is used when force_function_call + cc_support are both enabled to prevent
// Claude Code from hanging indefinitely when upstream models are in thinking phase.
type SSEReaderWithTimeout struct {
	reader            *bufio.Reader
	firstByteTimeout  time.Duration
	subsequentTimeout time.Duration
	receivedFirst     bool

	// startOnce + eventCh + readLoop implement a single long-lived reader
	// goroutine per stream (see readLoop for the lifecycle contract).
	startOnce sync.Once
	eventCh   chan sseReadResult

	// terminalErr retains a non-EOF terminal error when the result channel is
	// already full (consumer abandoned the stream after a read timeout), so a
	// later read reports the real transport failure instead of misreading the
	// channel close as a normal io.EOF. Only written by readLoop before it
	// closes eventCh, and only read by ReadEvent after the channel is closed, so
	// a plain field is safe without atomics (close happens-before any receiver
	// observing the closed channel).
	terminalErr error

	// done + closeOnce implement Close: cancelling the reader goroutine when
	// the consumer abandons the stream (see Close for the contract).
	done      chan struct{}
	closeOnce sync.Once
}

// sseReadResult is one event (or read error) produced by the reader goroutine.
type sseReadResult struct {
	event *SSEEvent
	err   error
}

// NewSSEReaderWithTimeout creates a new SSE reader with timeout support.
// firstByteTimeout: maximum time to wait for the first SSE event
// subsequentTimeout: maximum time to wait between subsequent SSE events
func NewSSEReaderWithTimeout(r io.Reader, firstByteTimeout, subsequentTimeout time.Duration) *SSEReaderWithTimeout {
	return &SSEReaderWithTimeout{
		reader:            bufio.NewReader(r),
		firstByteTimeout:  firstByteTimeout,
		subsequentTimeout: subsequentTimeout,
		receivedFirst:     false,
		eventCh:           make(chan sseReadResult, 1),
		done:              make(chan struct{}),
	}
}

// readLoop is the single long-lived reader goroutine per stream.
//
// Lifecycle contract: it reads SSE events from the underlying reader in a loop
// and pushes each one (or the terminating error) into eventCh (buffered, size
// 1). It exits when readEventInternal returns an error — i.e. on EOF or when
// the underlying connection/body is closed — and closes eventCh so consumers
// observe io.EOF. It also exits when Close cancels it via done: this releases
// a successful send that would otherwise block forever once the consumer
// abandoned the stream (e.g. after a read timeout) while upstream keeps sending
// data. Close does not close the underlying reader; a read parked on the
// underlying reader is only released by the caller closing the stream
// (body/connection), as before. A ReadEvent timeout neither kills nor spawns
// goroutines: the timed-out caller simply leaves this goroutine parked on the
// reader/channel, so the stream holds exactly 1 goroutine regardless of how
// many timeouts occur, and that 1 goroutine is released by Close, EOF, or
// connection close. This replaces the previous per-read goroutine pattern,
// which leaked one goroutine (~2KB stack) per timeout until the connection
// closed.
//
// Terminal error retention: when the result channel is already full because an
// abandoned consumer left a result unconsumed, the terminating error cannot be
// delivered through the channel. A non-EOF error is then retained in
// terminalErr so a later ReadEvent reports the real transport failure
// (connection reset, decompression error, ...) instead of misreading the closed
// channel as a normal io.EOF; a genuine io.EOF is still reported as EOF. The
// retained value is visible to any receiver only after close(eventCh), which
// happens after the write, so no synchronization is required.
func (r *SSEReaderWithTimeout) readLoop() {
	defer close(r.eventCh)
	for {
		event, err := r.readEventInternal()
		if err != nil {
			// Terminal result (EOF / connection close): deliver it if the
			// consumer is still waiting, but never block. If a timed-out caller
			// abandoned the stream and left a buffered result unconsumed, the
			// channel is full — retain a non-EOF error in terminalErr so the
			// transport failure is not misread as a normal EOF, then guarantee
			// this goroutine exits as soon as the underlying reader errors.
			select {
			case r.eventCh <- sseReadResult{event: event, err: err}:
			default:
				if err != io.EOF {
					r.terminalErr = err
				}
			}
			return
		}
		select {
		case r.eventCh <- sseReadResult{event: event, err: err}:
		case <-r.done:
			return
		}
	}
}

// Close cancels the single reader goroutine so a blocked successful send cannot
// leak forever when the consumer abandons the stream (e.g. after a read timeout)
// while the upstream keeps sending data. It is idempotent: closing done makes
// readLoop exit and close eventCh, so any later ReadEvent call observes io.EOF.
// It does not close the underlying reader: an in-progress blocking read is
// released only when the caller closes the stream (body/connection), as before.
func (r *SSEReaderWithTimeout) Close() {
	r.closeOnce.Do(func() { close(r.done) })
}

// ReadEvent reads the next SSE event with timeout support.
// The read itself happens in the single long-lived reader goroutine (readLoop);
// this method only receives from eventCh with an optional timeout, so timeouts
// never leave orphan goroutines behind.
func (r *SSEReaderWithTimeout) ReadEvent() (*SSEEvent, error) {
	r.startOnce.Do(func() {
		go r.readLoop()
	})

	timeout := r.subsequentTimeout
	if !r.receivedFirst {
		timeout = r.firstByteTimeout
	}
	if timeout <= 0 {
		result, ok := <-r.eventCh
		if !ok {
			if r.terminalErr != nil {
				return nil, r.terminalErr
			}
			return nil, io.EOF
		}
		if result.err == nil && result.event != nil {
			r.receivedFirst = true
		}
		return result.event, result.err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result, ok := <-r.eventCh:
		if !ok {
			if r.terminalErr != nil {
				return nil, r.terminalErr
			}
			return nil, io.EOF
		}
		if result.err == nil && result.event != nil {
			r.receivedFirst = true
		}
		return result.event, result.err
	case <-timer.C:
		timeoutType := "subsequent"
		if !r.receivedFirst {
			timeoutType = "first-byte"
		}
		logrus.WithFields(logrus.Fields{
			"timeout_type":    timeoutType,
			"timeout_seconds": timeout.Seconds(),
		}).Warn("CC: SSE read timeout, upstream did not send data")
		return nil, ErrSSETimeout
	}
}

// readEventInternal reads the next SSE event without timeout (internal implementation).
// NOTE: AI review suggested extracting shared SSE parsing logic with SSEReader.ReadEvent().
// We intentionally keep them separate because:
// 1. The code is small (~30 lines) and duplication cost is minimal
// 2. Extracting would add function call overhead in a hot path
// 3. SSEReaderWithTimeout may need different behavior in the future (e.g., keep-alive handling)
// 4. Current implementation is stable and well-tested
func (r *SSEReaderWithTimeout) readEventInternal() (*SSEEvent, error) {
	event := &SSEEvent{}
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			if event.Data != "" {
				return event, nil
			}
			continue
		}

		// Skip SSE comment lines (including keep-alive comments like ": keep-alive")
		if strings.HasPrefix(line, ":") {
			continue
		}

		if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if event.Data != "" {
				event.Data += "\n" + data
			} else {
				event.Data = data
			}
		}
	}
}
