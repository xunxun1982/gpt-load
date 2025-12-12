// Package proxy provides CC (Claude Code) support functionality.
// CC support enables Claude clients to connect via /claude endpoint and have
// requests converted from Claude format to OpenAI format before forwarding.
package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
)

// Note: ctxKeyTriggerSignal is defined in server.go and shared across function call and CC support

const maxUpstreamResponseBodySize = 32 * 1024 * 1024

var ErrBodyTooLarge = errors.New("CC: upstream response body exceeded maximum allowed size")

// Note: maxContentBufferBytes is defined in function_call.go (256KB) and shared for content buffering

const (
	// Thinking hints injected into user messages when extended thinking is enabled.
	// NOTE: The closing tag </antml> is intentionally kept for compatibility with
	// ANTML-style hints used in the b4u2cc reference implementation. The upstream
	// parser looks for these generic </antml> closers rather than matching the
	// opening tag name, so do not change them to </thinking_mode> / </max_thinking_length>.
	ThinkingHintInterleaved = "<thinking_mode>interleaved</antml>"
	ThinkingHintMaxLength   = "<max_thinking_length>%d</antml>"
)

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

// isCCSupportEnabled checks whether the cc_support flag is enabled for the given group.
// This flag is stored in the group-level JSON config.
func isCCSupportEnabled(group *models.Group) bool {
	if group == nil || group.Config == nil {
		return false
	}

	// Only enable CC support for OpenAI channel groups.
	if group.ChannelType != "openai" {
		return false
	}

	raw, ok := group.Config["cc_support"]
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

func getTriggerSignal(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyTriggerSignal); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ClaudeMessage represents a message in Claude format.
type ClaudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ClaudeContentBlock represents a content block in Claude format.
type ClaudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// ClaudeTool represents a tool definition in Claude format.
type ClaudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ThinkingConfig represents Claude extended thinking configuration.
type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// ClaudeRequest represents a Claude API request.
// It is intentionally a superset of the basic fields to support newer Claude Code features
// such as prompt-only requests, alternative max token fields, tool_choice and MCP metadata.
type ClaudeRequest struct {
	Model             string          `json:"model"`
	Prompt            string          `json:"prompt,omitempty"`
	System            json.RawMessage `json:"system,omitempty"`
	Messages          []ClaudeMessage `json:"messages"`
	MaxTokens         int             `json:"max_tokens,omitempty"`
	MaxTokensToSample int             `json:"max_tokens_to_sample,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stream            bool            `json:"stream"`
	Tools             []ClaudeTool    `json:"tools,omitempty"`
	StopSequences     []string        `json:"stop_sequences,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	McpServers        json.RawMessage `json:"mcp_servers,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	Container         json.RawMessage `json:"container,omitempty"`
	Thinking          *ThinkingConfig `json:"thinking,omitempty"`
}

// OpenAIMessage represents a message in OpenAI format.
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// OpenAIToolCall represents a tool call in OpenAI format.
type OpenAIToolCall struct {
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
	// interface{} is used here for flexibility to handle both string ("auto"/"none") and
	// object forms ({"type": "tool", "name": "..."}) from OpenAI API. json.Marshal ensures
	// type safety during serialization. This design is intentional for simplicity.
	ToolChoice interface{} `json:"tool_choice,omitempty"`
}

// convertClaudeToOpenAI converts a Claude request to OpenAI format.
func convertClaudeToOpenAI(claudeReq *ClaudeRequest) (*OpenAIRequest, error) {
	openaiReq := &OpenAIRequest{
		Model:       claudeReq.Model,
		Stream:      claudeReq.Stream,
		Temperature: claudeReq.Temperature,
		TopP:        claudeReq.TopP,
	}

	// Prefer MaxTokens; fall back to MaxTokensToSample for compatibility with
	// newer Claude APIs that use max_tokens_to_sample.
	effectiveMaxTokens := claudeReq.MaxTokens
	if effectiveMaxTokens <= 0 && claudeReq.MaxTokensToSample > 0 {
		effectiveMaxTokens = claudeReq.MaxTokensToSample
	}
	if effectiveMaxTokens > 0 {
		openaiReq.MaxTokens = &effectiveMaxTokens
	}

	// Convert system message
	messages := make([]OpenAIMessage, 0, len(claudeReq.Messages)+1)
	if len(claudeReq.System) > 0 {
		var systemContent string
		if err := json.Unmarshal(claudeReq.System, &systemContent); err != nil {
			// System might be an array of content blocks
			var systemBlocks []ClaudeContentBlock
			if err := json.Unmarshal(claudeReq.System, &systemBlocks); err == nil {
				var sb strings.Builder
				for _, block := range systemBlocks {
					if block.Type == "text" {
						sb.WriteString(block.Text)
					}
				}
				systemContent = sb.String()
			}
		}
		if systemContent != "" {
			contentJSON := marshalStringAsJSONRaw("system", systemContent)
			messages = append(messages, OpenAIMessage{
				Role:    "system",
				Content: contentJSON,
			})
		}
	}

	// Treat prompt as a single user message when no explicit messages are provided.
	if len(claudeReq.Messages) == 0 && strings.TrimSpace(claudeReq.Prompt) != "" {
		promptText := strings.TrimSpace(claudeReq.Prompt)
		contentJSON := marshalStringAsJSONRaw("prompt", promptText)
		messages = append(messages, OpenAIMessage{
			Role:    "user",
			Content: contentJSON,
		})
	}

	// Convert messages
	for _, msg := range claudeReq.Messages {
		openaiMsg, err := convertClaudeMessageToOpenAI(msg)
		if err != nil {
			return nil, fmt.Errorf("failed to convert Claude message: %w", err)
		}
		messages = append(messages, openaiMsg...)
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

	// Inject thinking hints when extended thinking is enabled.
	// NOTE: Only "enabled" type is currently supported. Other values like "disabled"
	// are silently ignored to allow graceful degradation.
	if claudeReq.Thinking != nil && strings.EqualFold(claudeReq.Thinking.Type, "enabled") {
		for i := len(openaiReq.Messages) - 1; i >= 0; i-- {
			if openaiReq.Messages[i].Role == "user" {
				hint := ThinkingHintInterleaved
				if claudeReq.Thinking.BudgetTokens > 0 {
					hint += fmt.Sprintf(ThinkingHintMaxLength, claudeReq.Thinking.BudgetTokens)
				}
				openaiReq.Messages[i].Content = appendToContent(openaiReq.Messages[i].Content, hint)
				break
			}
		}
	}

	// Convert tools
	if len(claudeReq.Tools) > 0 {
		tools := make([]OpenAITool, 0, len(claudeReq.Tools))
		for _, tool := range claudeReq.Tools {
			tools = append(tools, OpenAITool{
				Type: "function",
				Function: OpenAIFunction{
					Name:        tool.Name,
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

	// Convert tool_choice from Claude format to OpenAI format
	// COMPATIBILITY NOTE: Different OpenAI-compatible providers may have varying support for:
	// - "required" (used for Claude "any" type) vs "any" semantics
	// - Object format {"type":"function","function":{"name":...}} for specific tool forcing
	// This mapping follows OpenAI's documented API but may need provider-specific adjustments.
	// KNOWN LIMITATION: Azure OpenAI API versions 2024-06-01 and 2024-07-01-preview do not support
	// "required" for tool_choice (see GitHub issue Azure/azure-rest-api-specs#29844).
	// If using Azure OpenAI, the upstream may reject "required" with a 400 error.
	// DESIGN DECISION (rejected AI suggestion): We intentionally do NOT implement provider-specific
	// detection or fallback logic here to maintain simplicity (KISS principle). The b4u2cc reference
	// implementation uses a prompt-based approach that bypasses tool_choice entirely. Users should
	// configure their upstream provider appropriately or use group-level routing to avoid incompatible
	// combinations. Adding provider detection would violate our commitment to minimal complexity and
	// introduce maintenance burden for edge cases better handled at configuration level.
	if len(claudeReq.ToolChoice) > 0 {
		var toolChoice map[string]interface{}
		if err := json.Unmarshal(claudeReq.ToolChoice, &toolChoice); err == nil {
			// Claude format: {"type": "tool", "name": "tool_name"}
			// or: {"type": "auto"} / {"type": "any"}

			if tcType, ok := toolChoice["type"].(string); ok {
				switch tcType {
				case "tool":
					// Force call specific tool
					if toolName, ok := toolChoice["name"].(string); ok {
						openaiReq.ToolChoice = map[string]interface{}{
							"type": "function",
							"function": map[string]string{
								"name": toolName,
							},
						}
						logrus.WithField("tool_name", toolName).Debug("CC: Converted tool_choice to force specific tool")
					}
				case "any":
					// Force call any tool
					openaiReq.ToolChoice = "required"
					logrus.Debug("CC: Converted tool_choice to 'required' (force any tool)")
				case "auto":
					// Auto decide
					openaiReq.ToolChoice = "auto"
					logrus.Debug("CC: Converted tool_choice to 'auto'")
				default:
					logrus.WithField("type", tcType).Warn("CC: Unknown tool_choice type, skipping")
				}
			}
		} else {
			logrus.WithError(err).Warn("CC: Failed to parse tool_choice, skipping")
		}
	}

	return openaiReq, nil
}

// convertClaudeMessageToOpenAI converts a single Claude message to OpenAI format.
func convertClaudeMessageToOpenAI(msg ClaudeMessage) ([]OpenAIMessage, error) {
	var result []OpenAIMessage

	// Try to parse content as string first
	var contentStr string
	if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
		contentJSON := marshalStringAsJSONRaw("message_text", contentStr)
		result = append(result, OpenAIMessage{
			Role:    msg.Role,
			Content: contentJSON,
		})
		return result, nil
	}

	// Parse content as array of blocks
	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, fmt.Errorf("failed to parse content blocks: %w", err)
	}

	// Separate tool_use, tool_result, and text blocks
	var textParts []string
	var toolCalls []OpenAIToolCall
	var toolResults []OpenAIMessage

	for _, block := range blocks {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   block.ID,
				Type: "function",
				Function: OpenAIFunctionCall{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		case "tool_result":
			var resultContent string
			if err := json.Unmarshal(block.Content, &resultContent); err != nil {
				// Content might be an array
				var contentBlocks []ClaudeContentBlock
				if err := json.Unmarshal(block.Content, &contentBlocks); err == nil {
					var sb strings.Builder
					for _, cb := range contentBlocks {
						if cb.Type == "text" {
							sb.WriteString(cb.Text)
						} else {
							logrus.WithField("type", cb.Type).Debug("CC: Skipping non-text content block in tool_result")
						}
					}
					resultContent = sb.String()
				} else {
					// Fallback to raw content when parsing fails
					logrus.WithField("content", string(block.Content)).Debug("CC: tool_result content is neither string nor array, using raw")
					resultContent = string(block.Content)
				}
			}
			contentJSON := marshalStringAsJSONRaw("tool_result", resultContent)
			toolResults = append(toolResults, OpenAIMessage{
				Role:       "tool",
				Content:    contentJSON,
				ToolCallID: block.ToolUseID,
			})
		}
	}

	// Build assistant message with text and tool_calls
	// Note: Claude API only supports "user" and "assistant" roles per specification.
	// Any other roles are invalid and will result in the message being excluded from conversion.
	switch msg.Role {
	case "assistant":
		assistantMsg := OpenAIMessage{Role: "assistant"}
		if len(textParts) > 0 {
			combined := strings.Join(textParts, "")
			assistantMsg.Content = marshalStringAsJSONRaw("assistant_delta", combined)
		}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = toolCalls
		}
		if assistantMsg.Content != nil || len(assistantMsg.ToolCalls) > 0 {
			result = append(result, assistantMsg)
		}
	case "user":
		// User message with tool results
		if len(textParts) > 0 {
			combined := strings.Join(textParts, "")
			contentJSON := marshalStringAsJSONRaw("user_text", combined)
			result = append(result, OpenAIMessage{
				Role:    "user",
				Content: contentJSON,
			})
		}
		result = append(result, toolResults...)
	default:
		// Unknown roles are skipped but logged for easier debugging of API changes.
		logrus.WithField("role", msg.Role).Warn("CC: Unknown Claude message role, skipping message")
	}

	return result, nil
}

// applyCCRequestConversionDirect converts Claude request to OpenAI format directly.
// This function does not check the path, assuming the caller has already verified
// that this is a Claude messages endpoint. Used when path has been pre-rewritten.
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
		if _, exists := c.Get("original_model"); !exists {
			c.Set("original_model", originalModel)
		}
	}

	// Convert to OpenAI format
	openaiReq, err := convertClaudeToOpenAI(&claudeReq)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to convert Claude to OpenAI: %w", err)
	}

	// Marshal OpenAI request
	convertedBody, err := json.Marshal(openaiReq)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to marshal OpenAI request: %w", err)
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

// OpenAIChoice represents a choice in OpenAI response.
type OpenAIChoice struct {
	Index        int                `json:"index"`
	Message      *OpenAIRespMessage `json:"message,omitempty"`
	Delta        *OpenAIRespMessage `json:"delta,omitempty"`
	FinishReason *string            `json:"finish_reason,omitempty"`
}

// OpenAIRespMessage represents a message in OpenAI response.
type OpenAIRespMessage struct {
	Role      string           `json:"role,omitempty"`
	Content   *string          `json:"content,omitempty"`
	ToolCalls []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// OpenAIUsage represents usage info in OpenAI response.
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
}

// ClaudeUsage represents usage info in Claude response.
type ClaudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// convertOpenAIToClaudeResponse converts OpenAI response to Claude format.
func convertOpenAIToClaudeResponse(openaiResp *OpenAIResponse) *ClaudeResponse {
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

			// Add text and thinking content
			if msg.Content != nil && *msg.Content != "" {
				content = append(content, splitThinkingContent(*msg.Content)...)
			}

			// Add tool_use blocks
			for _, tc := range msg.ToolCalls {
				inputJSON := json.RawMessage("{}")
				if tc.Function.Arguments != "" {
					inputJSON = json.RawMessage(tc.Function.Arguments)
				}
				content = append(content, ClaudeContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: inputJSON,
				})
			}

			claudeResp.Content = content
		}

		// Convert finish reason
		if choice.FinishReason != nil {
			stopReason := convertFinishReasonToStopReason(*choice.FinishReason)
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
	}

	// Convert usage - always provide usage to satisfy Claude client requirements
	if openaiResp.Usage != nil {
		claudeResp.Usage = &ClaudeUsage{
			InputTokens:  openaiResp.Usage.PromptTokens,
			OutputTokens: openaiResp.Usage.CompletionTokens,
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

// convertFinishReasonToStopReason converts OpenAI finish_reason to Claude stop_reason.
// Also handles non-standard finish reasons from various upstream providers.
func convertFinishReasonToStopReason(finishReason string) string {
	switch finishReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	// Handle non-standard error-related finish reasons from upstream providers.
	// Convert these to end_turn to prevent Claude Code from treating them as
	// abnormal terminations. Log the original reason for debugging.
	case "network_error", "error", "timeout", "rate_limit", "server_error":
		logrus.WithField("original_finish_reason", finishReason).
			Warn("CC: Received non-standard finish_reason from upstream, converting to end_turn")
		return "end_turn"
	default:
		// For any other unknown finish reason, log it but still return as-is
		// to maintain compatibility with potential future OpenAI/Claude API changes
		if finishReason != "" {
			logrus.WithField("finish_reason", finishReason).
				Debug("CC: Unknown finish_reason, passing through as-is")
		}
		return finishReason
	}
}

func splitThinkingContent(content string) []ClaudeContentBlock {
	if content == "" {
		return nil
	}
	parser := NewThinkingParser()
	for _, r := range content {
		parser.FeedRune(r)
	}
	parser.Finish()

	events := parser.ConsumeEvents()
	blocks := make([]ClaudeContentBlock, 0, len(events))
	for _, evt := range events {
		switch evt.Type {
		case "text":
			// removeFunctionCallsBlocks already handles trigger signals, so no need to call reTriggerSignal separately
			cleaned := removeFunctionCallsBlocks(evt.Content)
			if cleaned != "" {
				blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: cleaned})
			}
		case "thinking":
			thinking := evt.Content
			// Apply same artifact removal pattern as ThinkingParser
			thinking = strings.TrimSpace(thinking)
			if strings.HasPrefix(thinking, ">") && len(thinking) > 1 {
				if thinking[1] == ' ' || thinking[1] == '\n' || thinking[1] == '\r' || thinking[1] == '\t' {
					thinking = strings.TrimSpace(thinking[1:])
				}
			}
			if thinking != "" {
				blocks = append(blocks, ClaudeContentBlock{Type: "thinking", Thinking: thinking})
			}
		}
	}
	return blocks
}

var (
	tokenMultiplierOnce   sync.Once
	cachedTokenMultiplier float64 = 1.0
)

func getTokenMultiplier() float64 {
	tokenMultiplierOnce.Do(func() {
		raw := strings.TrimSpace(utils.GetEnvOrDefault("TOKEN_MULTIPLIER", ""))
		if raw == "" {
			cachedTokenMultiplier = 1.0
			return
		}
		val, err := strconv.ParseFloat(raw, 64)
		// Clamp multiplier to a safe range to avoid overflow or unrealistic scaling.
		// Values outside (0, 1000] fall back to the default 1.0.
		if err != nil || val <= 0 || val > 1000 {
			logrus.WithFields(logrus.Fields{"raw": raw}).WithError(err).Warn("CC: Invalid TOKEN_MULTIPLIER, falling back to 1.0")
			cachedTokenMultiplier = 1.0
			return
		}
		cachedTokenMultiplier = val
	})
	return cachedTokenMultiplier
}

func applyTokenMultiplier(usage *ClaudeUsage) {
	if usage == nil {
		return
	}
	multiplier := getTokenMultiplier()
	if multiplier == 1.0 {
		return
	}
	// Apply multiplier only to non-zero values to preserve 0-usage semantics
	if usage.InputTokens > 0 {
		scaled := int(float64(usage.InputTokens) * multiplier)
		if scaled < 1 {
			scaled = 1
		}
		usage.InputTokens = scaled
	}
	if usage.OutputTokens > 0 {
		scaled := int(float64(usage.OutputTokens) * multiplier)
		if scaled < 1 {
			scaled = 1
		}
		usage.OutputTokens = scaled
	}
}

func isCountTokensEndpoint(path, method string) bool {
	if method != http.MethodPost {
		return false
	}
	return strings.HasSuffix(path, "/v1/messages/count_tokens")
}

func estimateTokensLocal(chunks []string) int {
	if len(chunks) == 0 {
		return 0
	}
	asciiCount := 0
	nonASCII := 0
	for _, chunk := range chunks {
		for _, r := range chunk {
			if r < 128 {
				asciiCount++
			} else {
				nonASCII++
			}
		}
	}
	// Heuristic: ASCII ~4 chars/token, non-ASCII ~1 rune/token.
	tokens := (asciiCount + 3) / 4
	tokens += nonASCII
	if tokens == 0 {
		tokens = 1
	}
	multiplier := getTokenMultiplier()
	scaled := int(float64(tokens) * multiplier)
	// Ensure non-empty input always returns at least 1 token after scaling
	if scaled < 1 {
		scaled = 1
	}
	return scaled
}

func extractTextFromRawContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			switch b.Type {
			case "text":
				sb.WriteString(b.Text)
			case "thinking":
				sb.WriteString(b.Thinking)
			case "tool_result":
				sb.Write(b.Content)
			}
		}
		return sb.String()
	}

	return string(raw)
}

func (ps *ProxyServer) countTokensLocally(bodyBytes []byte) int {
	var req ClaudeRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return estimateTokensLocal([]string{string(bodyBytes)})
	}

	var chunks []string
	if len(req.System) > 0 {
		if sys := extractTextFromRawContent(req.System); sys != "" {
			chunks = append(chunks, sys)
		}
	}
	for _, msg := range req.Messages {
		if text := extractTextFromRawContent(msg.Content); text != "" {
			chunks = append(chunks, text)
		}
	}

	return estimateTokensLocal(chunks)
}

func (ps *ProxyServer) handleTokenCount(c *gin.Context, group *models.Group, bodyBytes []byte) bool {
	if group == nil || !isCCSupportEnabled(group) {
		return false
	}
	if !isCountTokensEndpoint(c.Request.URL.Path, c.Request.Method) {
		return false
	}
	if !strings.Contains(c.Request.URL.Path, "/claude/") && !c.GetBool("cc_was_claude_path") && c.GetString(ctxKeyOriginalFormat) != "claude" {
		return false
	}

	// NOTE: The anthropic channel type upstream counting branch was removed here because
	// isCCSupportEnabled() requires group.ChannelType == "openai", making the anthropic
	// branch unreachable. CC support currently only works with OpenAI-compatible channels.
	// If future support for direct Anthropic channels is needed, isCCSupportEnabled() must
	// be relaxed first.

	// For now, always use local token estimation for CC-enabled OpenAI groups.
	tokens := ps.countTokensLocally(bodyBytes)
	c.JSON(http.StatusOK, gin.H{"input_tokens": tokens})
	return true
}

// parseFunctionCallsFromContentForCC parses function calls from content when force_function_call
// is enabled in CC mode. It extracts XML-based function calls and converts them to Claude
// tool_use format. Returns the cleaned content (with XML removed) and parsed tool_use blocks.
//
// This function bridges the gap between force_function_call (which injects XML-based prompts)
// and CC support (which expects Claude-style tool_use blocks).
func parseFunctionCallsFromContentForCC(c *gin.Context, content string) (string, []ClaudeContentBlock) {
	// Only process if function call is enabled for this request
	if !isFunctionCallEnabled(c) {
		return content, nil
	}

	// Retrieve trigger signal stored during request rewrite
	triggerVal, exists := c.Get(ctxKeyTriggerSignal)
	if !exists {
		return content, nil
	}
	triggerSignal, ok := triggerVal.(string)
	if !ok || triggerSignal == "" {
		return content, nil
	}

	// Parse function calls from the content
	calls := parseFunctionCallsXML(content, triggerSignal)

	// Fallback: try parsing without trigger signal if none found
	if len(calls) == 0 && strings.Contains(content, "<function_calls>") {
		calls = parseFunctionCallsXML(content, "")
		if len(calls) > 0 {
			logrus.WithField("parsed_count", len(calls)).
				Debug("CC+FC: Parsed function calls using fallback (no trigger signal)")
		}
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

		// Fix tool-specific parameter issues to handle common model errors
		// We apply generic fixes based on parameter types and names to cover more tools
		for key, val := range call.Args {
			if strVal, ok := val.(string); ok {
				// 1. Try to unmarshal JSON strings (arrays/objects)
				// This handles cases like TodoWrite's 'todos' passed as string
				trimmed := strings.TrimSpace(strVal)
				if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
					(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
					var jsonVal any
					if err := json.Unmarshal([]byte(strVal), &jsonVal); err == nil {
						call.Args[key] = jsonVal
						continue // Skip other fixes if it was valid JSON
					}
				}

				// 2. Fix escaped newlines in content-heavy parameters
				// This handles cases like Write/Edit 'content' or Bash 'command'
				// where models might double-escape newlines (e.g. \\n instead of \n)
				if key == "content" || key == "command" || key == "script" || key == "code" {
					if strings.Contains(strVal, "\\n") {
						call.Args[key] = strings.ReplaceAll(strVal, "\\n", "\n")
					}
				}
			}
		}

		// Specific normalization for tools to handle schema strictness
		skipCall := false
		switch call.Name {
		case "TodoWrite":
			// NOTE: An earlier automated review suggested extracting this TodoWrite
			// normalization into a helper. We intentionally keep it inline here to
			// avoid extra indirection on this hot path and to keep all TodoWrite-
			// specific fixes co-located. Behavior is covered by cc_function_test.go
			// (TodoWrite-related tests) and should not be modified without updating
			// the corresponding tests.
			// For TodoWrite, require a structurally valid todos list. If we cannot
			// parse a non-empty list of items, we skip this function call entirely
			// to avoid sending malformed plans to Claude Code (which would cause
			// repeated correction attempts and noisy output).
			todos, ok := call.Args["todos"]
			if !ok {
				if v, exists := call.Args["value"]; exists {
					// Some malformed outputs place the todos array under a generic
					// "value" key. Treat this as the candidate todos source.
					todos = v
					ok = true
				}
			}
			if ok {
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
						if err := json.Unmarshal([]byte(trimmedStr), &parsed); err == nil && len(parsed) > 0 {
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

				if hasValidTodos {
					normalizedTodos := make([]map[string]any, 0, len(todoList))
					for idx, item := range todoList {
						defaultID := fmt.Sprintf("task-%d", idx+1)

						if strItem, ok := item.(string); ok {
							normalizedTodos = append(normalizedTodos, map[string]any{
								"content":  strItem,
								"status":   "pending",
								"priority": "medium",
								"id":       defaultID,
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

						if _, hasContent := cleanItem["content"]; hasContent {
							normalizedTodos = append(normalizedTodos, cleanItem)
						}
					}

					if len(normalizedTodos) > 0 {
						call.Args["todos"] = normalizedTodos
					} else {
						hasValidTodos = false
					}
				}

				if !hasValidTodos {
					skipCall = true
				}
			} else {
				// No todos-like field present at all; skip this TodoWrite call.
				skipCall = true
			}

		case "AskUserQuestion":
			// Ensure 'questions' is an array
			if questions, ok := call.Args["questions"]; ok {
				if _, isSlice := questions.([]any); !isSlice {
					if strVal, ok := questions.(string); ok {
						var qList []any
						if err := json.Unmarshal([]byte(strVal), &qList); err == nil {
							call.Args["questions"] = qList
						} else {
							// Wrap single string as array
							call.Args["questions"] = []any{strVal}
						}
					}
				}
			}
			// Ensure 'answers' is an object
			if answers, ok := call.Args["answers"]; ok {
				if _, isMap := answers.(map[string]any); !isMap {
					if strVal, ok := answers.(string); ok {
						var aMap map[string]any
						if err := json.Unmarshal([]byte(strVal), &aMap); err == nil {
							call.Args["answers"] = aMap
						}
					}
				}
			}

		case "list_dir":
			// Ensure 'recursive' field exists, default to false
			// MCP list_dir tool requires this field, but models often omit it
			if _, ok := call.Args["recursive"]; !ok {
				call.Args["recursive"] = false
			}

		case "WebSearch":
			// Ensure 'allowed_domains' is an array
			if allowed, ok := call.Args["allowed_domains"]; ok {
				if _, isSlice := allowed.([]any); !isSlice {
					if strVal, ok := allowed.(string); ok {
						var list []any
						if err := json.Unmarshal([]byte(strVal), &list); err == nil {
							call.Args["allowed_domains"] = list
						} else {
							call.Args["allowed_domains"] = []any{strVal}
						}
					}
				}
			}
			// Ensure 'blocked_domains' is an array
			if blocked, ok := call.Args["blocked_domains"]; ok {
				if _, isSlice := blocked.([]any); !isSlice {
					if strVal, ok := blocked.(string); ok {
						var list []any
						if err := json.Unmarshal([]byte(strVal), &list); err == nil {
							call.Args["blocked_domains"] = list
						} else {
							call.Args["blocked_domains"] = []any{strVal}
						}
					}
				}
			}

		case "Edit":
			// Fix newlines in old_string and new_string
			for _, key := range []string{"old_string", "new_string"} {
				if val, ok := call.Args[key]; ok {
					if strVal, ok := val.(string); ok {
						if strings.Contains(strVal, "\\n") {
							call.Args[key] = strings.ReplaceAll(strVal, "\\n", "\n")
						}
					}
				}
			}
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

		// Generate unique tool use ID
		toolUseID := fmt.Sprintf("toolu_%s_%d", utils.GenerateRandomSuffix(), i)

		toolUseBlocks = append(toolUseBlocks, ClaudeContentBlock{
			Type:  "tool_use",
			ID:    toolUseID,
			Name:  call.Name,
			Input: json.RawMessage(inputJSON),
		})
	}

	if len(toolUseBlocks) == 0 {
		return content, nil
	}

	// Remove function call XML blocks from content
	cleanedContent := removeFunctionCallsBlocks(content)

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
			c.JSON(http.StatusBadGateway, claudeErr)
			return
		}

		logrus.WithError(err).Error("Failed to read OpenAI response body for CC conversion")
		clearUpstreamEncodingHeaders(c)
		c.Status(http.StatusInternalServerError)
		return
	}
	// defer resp.Body.Close() - caller (executeRequestWithRetry) handles this

	// Decompress response body if it is encoded (e.g., gzip) before JSON parsing.
	// This avoids returning compressed bytes to Claude clients and matches CC API expectations.
	bodyBytes, _ = utils.DecompressResponse(resp.Header.Get("Content-Encoding"), bodyBytes)

	// Parse OpenAI response
	var openaiResp OpenAIResponse
	if err := json.Unmarshal(bodyBytes, &openaiResp); err != nil {
		logrus.WithError(err).WithField("body_preview", utils.TruncateString(string(bodyBytes), 512)).
			Warn("Failed to parse OpenAI response for CC conversion, returning body without CC conversion")
		// Store original body for downstream logging (will be truncated by logger).
		c.Set("response_body", string(bodyBytes))

		// Clear upstream encoding/length headers since we may have decompressed the body above.
		// Returning decompressed bytes with a stale Content-Encoding header would cause clients
		// to attempt decompression again and corrupt the payload.
		clearUpstreamEncodingHeaders(c)

		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
		return
	}

	// Check for OpenAI error
	if openaiResp.Error != nil {
		logrus.WithFields(logrus.Fields{
			"error_type":    openaiResp.Error.Type,
			"error_message": openaiResp.Error.Message,
			"error_code":    openaiResp.Error.Code,
		}).Warn("CC: OpenAI returned error in CC conversion")

		claudeErr := ClaudeErrorResponse{
			Type: "error",
			Error: ClaudeError{
				Type:    "invalid_request_error",
				Message: openaiResp.Error.Message,
			},
		}
		clearUpstreamEncodingHeaders(c)
		c.JSON(resp.StatusCode, claudeErr)
		return
	}

	// Convert to Claude format
	claudeResp := convertOpenAIToClaudeResponse(&openaiResp)

	// When force_function_call is enabled in CC mode, parse XML function calls
	// from the response content and convert them to Claude tool_use blocks.
	// This bridges the gap between the XML-based function call prompt injection
	// and Claude Code's expected tool_use format.
	if isFunctionCallEnabled(c) && len(claudeResp.Content) > 0 {
		// Extract text content for function call parsing
		var textContent string
		for _, block := range claudeResp.Content {
			if block.Type == "text" {
				textContent += block.Text
			}
		}

		if textContent != "" {
			cleanedContent, toolUseBlocks := parseFunctionCallsFromContentForCC(c, textContent)

			if len(toolUseBlocks) > 0 {
				// Rebuild content: clean text block(s) + tool_use blocks
				var newContent []ClaudeContentBlock

				// Add cleaned text content if not empty
				if strings.TrimSpace(cleanedContent) != "" {
					newContent = append(newContent, ClaudeContentBlock{
						Type: "text",
						Text: cleanedContent,
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
					"text_retained":  strings.TrimSpace(cleanedContent) != "",
				}).Debug("CC+FC: Added tool_use blocks to Claude response")
			}
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
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
		return
	}

	// Store Claude response body for downstream logging (will be truncated by logger).
	c.Set("response_body", string(claudeBody))

	// Clear upstream encoding/length headers before writing synthesized response.
	// The proxy decompresses and re-encodes the response, so upstream headers no longer match.
	// Per RFC 7230, mismatched Content-Length causes client to treat response as incomplete.
	clearUpstreamEncodingHeaders(c)

	c.Header("Content-Type", "application/json")
	c.Data(resp.StatusCode, "application/json", claudeBody)
}

// ClaudeStreamEvent represents a Claude streaming event.
type ClaudeStreamEvent struct {
	Type         string              `json:"type"`
	Message      *ClaudeResponse     `json:"message,omitempty"`
	Index        int                 `json:"index,omitempty"`
	ContentBlock *ClaudeContentBlock `json:"content_block,omitempty"`
	Delta        *ClaudeStreamDelta  `json:"delta,omitempty"`
	Usage        *ClaudeUsage        `json:"usage,omitempty"`
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
)

// Pre-computed rune slices for tag matching to avoid repeated allocations in hot path
var (
	thinkingEndTagRunes      = []rune(ThinkingEndTag)
	thinkingAltEndTagRunes   = []rune(ThinkingAltEndTag)
	thinkingStartTagRunes    = []rune(ThinkingStartTag)
	thinkingAltStartTagRunes = []rune(ThinkingAltStartTag)
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
	// Max tag length is len("</thinking>") = 11
	maxTagLen := 11
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
		if p.thinkingRingSuffixMatches(thinkingEndTagRunes) || p.thinkingRingSuffixMatches(thinkingAltEndTagRunes) {
			// Extract content by trimming the matched end tag
			fullContent := p.thinkingBuffer.String()
			var tagLen int
			if p.thinkingRingSuffixMatches(thinkingEndTagRunes) {
				tagLen = len(ThinkingEndTag)
			} else {
				tagLen = len(ThinkingAltEndTag)
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
	if p.ringSuffixMatches(thinkingStartTagRunes) || p.ringSuffixMatches(thinkingAltStartTagRunes) {
		// Extract text portion by removing the matched tag
		textLen := p.buffer.Len()
		var tagLen int
		if p.ringSuffixMatches(thinkingStartTagRunes) {
			tagLen = len(ThinkingStartTag)
		} else {
			tagLen = len(ThinkingAltStartTag)
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
	if p.buffer.Len() > 0 {
		p.events = append(p.events, ThinkingEvent{Type: "text", Content: p.buffer.String()})
		p.buffer.Reset()
	}
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

	writer := NewSSEWriter(c.Writer, flusher)
	defer writer.Close()

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

	logrus.WithField("msg_id", msgID).Debug("CC: Started streaming response")

	reader := NewSSEReader(resp.Body)
	contentBlockIndex := 0
	var currentToolCall *OpenAIToolCall
	var accumulatedContent strings.Builder
	contentBufFullWarned := false
	triggerSignal := getTriggerSignal(c)
	parser := NewThinkingParser()
	textBlockOpen := false
	var aggregator *TextAggregator
	hasValidToolCalls := false // Track if any valid tool_calls were processed

	sanitizeText := func(text string) string {
		if triggerSignal != "" {
			text = strings.ReplaceAll(text, triggerSignal, "")
		}
		// Use the comprehensive removeFunctionCallsBlocks function to clean all
		// function call XML formats (function_calls, function_call, invoke,
		// invocation, tool_call, and trigger signals)
		text = removeFunctionCallsBlocks(text)
		return text
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

	closeToolBlock := func() {
		if currentToolCall == nil {
			return
		}
		stopEvent := ClaudeStreamEvent{Type: "content_block_stop", Index: contentBlockIndex}
		if err := writer.Send(stopEvent, true); err != nil {
			logrus.WithError(err).Debug("CC: Failed to stop tool block")
			return
		}
		contentBlockIndex++
		currentToolCall = nil
	}

	emitThinking := func(content string) {
		aggregator.Flush()
		closeTextBlock()
		thinking := strings.TrimSpace(content)
		if thinking == "" {
			return
		}
		startEvent := ClaudeStreamEvent{
			Type:         "content_block_start",
			Index:        contentBlockIndex,
			ContentBlock: &ClaudeContentBlock{Type: "thinking", Thinking: ""},
		}
		if err := writer.Send(startEvent, true); err != nil {
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
		stopEvent := ClaudeStreamEvent{Type: "content_block_stop", Index: contentBlockIndex}
		if err := writer.Send(stopEvent, true); err != nil {
			logrus.WithError(err).Debug("CC: Failed to stop thinking block")
		}
		contentBlockIndex++
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

	aggregator = NewTextAggregator(35, func(text string) {
		cleaned := sanitizeText(text)
		if cleaned == "" {
			return
		}
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

	finalize := func(stopReason string, usage *OpenAIUsage) {
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
		closeTextBlock()
		closeToolBlock()

		if accumulatedContent.Len() > 0 && isFunctionCallEnabled(c) {
			content := accumulatedContent.String()
			_, toolUseBlocks := parseFunctionCallsFromContentForCC(c, content)
			if len(toolUseBlocks) > 0 {
				emitToolUseBlocks(toolUseBlocks)
				stopReason = "tool_use"
			}
		}

		usagePayload := &ClaudeUsage{InputTokens: 0, OutputTokens: 0}
		if usage != nil {
			usagePayload.InputTokens = usage.PromptTokens
			usagePayload.OutputTokens = usage.CompletionTokens
		}
		applyTokenMultiplier(usagePayload)

		deltaEvent := ClaudeStreamEvent{Type: "message_delta", Delta: &ClaudeStreamDelta{StopReason: stopReason}, Usage: usagePayload}
		if err := writer.Send(deltaEvent, true); err != nil {
			logrus.WithError(err).Debug("CC: Failed to write message_delta")
			return
		}
		if err := writer.Send(ClaudeStreamEvent{Type: "message_stop"}, true); err != nil {
			logrus.WithError(err).Debug("CC: Failed to write message_stop")
		}
	}

	for {
		event, err := reader.ReadEvent()
		if err != nil {
			if err == io.EOF {
				logrus.Debug("CC: Upstream stream EOF")
			}
			break
		}

		if event.Data == "[DONE]" {
			finalize("end_turn", nil)
			logrus.Debug("CC: Stream finished successfully")
			break
		}

		var openaiChunk OpenAIResponse
		if err := json.Unmarshal([]byte(event.Data), &openaiChunk); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{"event_type": event.Event, "data_preview": utils.TruncateString(event.Data, 512)}).Debug("CC: Failed to parse OpenAI chunk as JSON, skipping")
			continue
		}

		if len(openaiChunk.Choices) == 0 {
			continue
		}

		choice := openaiChunk.Choices[0]
		delta := choice.Delta
		if delta == nil {
			continue
		}

		if delta.Content != nil && *delta.Content != "" {
			contentStr := *delta.Content
			if accumulatedContent.Len()+len(contentStr) <= maxContentBufferBytes {
				accumulatedContent.WriteString(contentStr)
			} else if !contentBufFullWarned {
				logrus.Warn("CC: content buffer limit reached during streaming; tool call parsing may be incomplete")
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
			closeTextBlock()
			for _, tc := range delta.ToolCalls {
				call := tc
				if call.ID == "" {
					continue
				}
				isNew := currentToolCall == nil || currentToolCall.ID != call.ID
				if isNew && currentToolCall != nil && currentToolCall.ID != call.ID {
					closeToolBlock()
				}
				if isNew {
					currentToolCall = &call
					hasValidToolCalls = true // Mark that we have valid tool calls
					startEvent := ClaudeStreamEvent{
						Type:         "content_block_start",
						Index:        contentBlockIndex,
						ContentBlock: &ClaudeContentBlock{Type: "tool_use", ID: call.ID, Name: call.Function.Name},
					}
					if err := writer.Send(startEvent, true); err != nil {
						logrus.WithError(err).Debug("CC: Failed to start tool_use block")
						continue
					}
				}

				if call.Function.Arguments != "" && currentToolCall != nil {
					deltaEvent := ClaudeStreamEvent{
						Type:  "content_block_delta",
						Index: contentBlockIndex,
						Delta: &ClaudeStreamDelta{Type: "input_json_delta", PartialJSON: call.Function.Arguments},
					}
					if err := writer.Send(deltaEvent, false); err != nil {
						logrus.WithError(err).Debug("CC: Failed to write tool_use delta")
					}
				}
			}
		}

		if choice.FinishReason != nil {
			closeToolBlock()
			stopReason := convertFinishReasonToStopReason(*choice.FinishReason)
			// If upstream says tool_calls but we didn't receive any valid tool calls,
			// convert to end_turn to prevent Claude Code from hanging waiting for tool results
			// NOTE: Similar to non-streaming handler but NOT extracted - checks accumulated
			// hasValidToolCalls flag vs. claudeResp.Content array. KISS principle applies.
			if *choice.FinishReason == "tool_calls" && !hasValidToolCalls {
				logrus.WithField("original_finish_reason", *choice.FinishReason).
					Warn("CC: Received tool_calls finish_reason but no valid tool calls were processed, converting to end_turn")
				stopReason = "end_turn"
			}
			finalize(stopReason, openaiChunk.Usage)
			logrus.WithField("stop_reason", *choice.FinishReason).Debug("CC: Stream finished with finish_reason")
			break
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
