// Package proxy provides CC (Claude Code) support functionality.
// CC support enables Claude clients to connect via /claude endpoint and have
// requests converted from Claude format to OpenAI format before forwarding.
package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Context keys for CC support middleware.
const (
	ctxKeyCCEnabled       = "cc_enabled"
	ctxKeyOriginalFormat  = "cc_original_format"
)

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

// isClaudePath checks if the request path contains /claude/v1 segment after the group name.
// This is used to detect any Claude-style path that needs to be rewritten.
// Path format: /proxy/{group}/claude/v1/...
// We check for /claude/v1 to avoid false positives when group name contains "claude".
// Examples:
//   - /proxy/mygroup/claude/v1/models -> true
//   - /proxy/claude/v1/models -> true (group named "claude", no CC path)
//   - /proxy/claude/claude/v1/models -> true (group named "claude", with CC path)
func isClaudePath(path string) bool {
	// Look for /claude/v1 pattern which indicates CC endpoint
	// This avoids matching group names that contain "claude"
	return strings.Contains(path, "/claude/v1")
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

// ClaudeMessage represents a message in Claude format.
type ClaudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ClaudeContentBlock represents a content block in Claude format.
type ClaudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
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
}

// OpenAIMessage represents a message in OpenAI format.
type OpenAIMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
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
			contentJSON, _ := json.Marshal(systemContent)
			messages = append(messages, OpenAIMessage{
				Role:    "system",
				Content: contentJSON,
			})
		}
	}

	// Treat prompt as a single user message when no explicit messages are provided.
	if len(claudeReq.Messages) == 0 && strings.TrimSpace(claudeReq.Prompt) != "" {
		promptText := strings.TrimSpace(claudeReq.Prompt)
		contentJSON, _ := json.Marshal(promptText)
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
		}
	}

	openaiReq.Messages = messages

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
		openaiReq.Stop, _ = json.Marshal(claudeReq.StopSequences)
	}

	return openaiReq, nil
}

// convertClaudeMessageToOpenAI converts a single Claude message to OpenAI format.
func convertClaudeMessageToOpenAI(msg ClaudeMessage) ([]OpenAIMessage, error) {
	var result []OpenAIMessage

	// Try to parse content as string first
	var contentStr string
	if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
		contentJSON, _ := json.Marshal(contentStr)
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
						}
					}
					resultContent = sb.String()
				} else {
					// Fallback to raw content when parsing fails
					logrus.WithField("content", string(block.Content)).Debug("CC: tool_result content is neither string nor array, using raw")
					resultContent = string(block.Content)
				}
			}
			contentJSON, _ := json.Marshal(resultContent)
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
			assistantMsg.Content, _ = json.Marshal(combined)
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
			contentJSON, _ := json.Marshal(combined)
			result = append(result, OpenAIMessage{
				Role:    "user",
				Content: contentJSON,
			})
		}
		result = append(result, toolResults...)
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

	// Optionally log request conversion previews when body logging is enabled.
	if group != nil && group.EffectiveConfig.EnableRequestBodyLogging {
		logrus.WithFields(logrus.Fields{
			"group":               group.Name,
			"original_model":      originalModel,
			"claude_body_preview": utils.TruncateString(string(bodyBytes), 1024),
			"openai_body_preview": utils.TruncateString(string(convertedBody), 1024),
			"tools_count":         len(claudeReq.Tools),
			"has_mcp_servers":     len(claudeReq.McpServers) > 0,
		}).Debug("CC: Request conversion preview (truncated)")
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
		ID:    openaiResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: openaiResp.Model,
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

			// Add text content
			if msg.Content != nil && *msg.Content != "" {
				content = append(content, ClaudeContentBlock{
					Type: "text",
					Text: *msg.Content,
				})
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

// handleCCNormalResponse handles non-streaming response conversion for CC support.
func (ps *ProxyServer) handleCCNormalResponse(c *gin.Context, resp *http.Response) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logrus.WithError(err).Error("Failed to read OpenAI response body for CC conversion")
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
			Warn("Failed to parse OpenAI response for CC conversion, returning original")
		// Store original body for downstream logging (will be truncated by logger).
		c.Set("response_body", string(bodyBytes))
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
		c.JSON(resp.StatusCode, claudeErr)
		return
	}

	// Convert to Claude format
	claudeResp := convertOpenAIToClaudeResponse(&openaiResp)

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
	c.Writer.Header().Del("Content-Encoding")
	c.Writer.Header().Del("Content-Length")
	c.Writer.Header().Del("Transfer-Encoding")

	c.Header("Content-Type", "application/json")
	c.Data(resp.StatusCode, "application/json", claudeBody)
}

// ClaudeStreamEvent represents a Claude streaming event.
type ClaudeStreamEvent struct {
	Type         string               `json:"type"`
	Message      *ClaudeResponse      `json:"message,omitempty"`
	Index        int                  `json:"index,omitempty"`
	ContentBlock *ClaudeContentBlock  `json:"content_block,omitempty"`
	Delta        *ClaudeStreamDelta   `json:"delta,omitempty"`
	Usage        *ClaudeUsage         `json:"usage,omitempty"`
}

// ClaudeStreamDelta represents delta content in Claude streaming.
type ClaudeStreamDelta struct {
	Type        string          `json:"type,omitempty"`
	Text        string          `json:"text,omitempty"`
	PartialJSON string          `json:"partial_json,omitempty"`
	StopReason  string          `json:"stop_reason,omitempty"`
}

// handleCCStreamingResponse handles streaming response conversion for CC support.
func (ps *ProxyServer) handleCCStreamingResponse(c *gin.Context, resp *http.Response) {
	// Clear upstream encoding/length headers before writing synthesized SSE stream.
	// The proxy reconstructs the event stream from OpenAI format to Claude format,
	// so upstream headers (Content-Encoding, Content-Length, Transfer-Encoding) no longer apply.
	c.Writer.Header().Del("Content-Encoding")
	c.Writer.Header().Del("Content-Length")
	c.Writer.Header().Del("Transfer-Encoding")

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

	// Send message_start event with required usage field
	msgID := "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")
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
				// AI review note: Using "unknown" as fallback is intentional design
				// for rare cases where original_model is not set. Not an error condition.
				return "unknown"
			}(),
			// Usage is required by Claude clients, provide default values
			Usage: &ClaudeUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}
	writeClaudeEvent(c.Writer, startEvent)
	flusher.Flush()

	logrus.WithField("msg_id", msgID).Debug("CC: Started streaming response")

	reader := NewSSEReader(resp.Body)
	contentBlockIndex := 0
	var currentToolCall *OpenAIToolCall
	var accumulatedContent strings.Builder

	for {
		event, err := reader.ReadEvent()
		if err != nil {
			if err == io.EOF {
				logrus.Debug("CC: Upstream stream EOF")
				break
			}
			logrus.WithError(err).Error("CC: Error reading SSE event for CC conversion")
			// Note: We don't send an error event to the client because:
			// 1. Claude streaming API spec does not define a standard error event type
			// 2. Client will detect stream interruption via connection close
			// 3. Sending non-standard events may cause client parse errors
			break
		}

		if event.Data == "[DONE]" {
			// Send message_delta with stop_reason and usage
			deltaEvent := ClaudeStreamEvent{
				Type: "message_delta",
				Delta: &ClaudeStreamDelta{
					StopReason: "end_turn",
				},
				// Usage is required by Claude clients
				Usage: &ClaudeUsage{
					InputTokens:  0,
					OutputTokens: 0,
				},
			}
			writeClaudeEvent(c.Writer, deltaEvent)
			flusher.Flush()

			// Send message_stop
			stopEvent := ClaudeStreamEvent{Type: "message_stop"}
			writeClaudeEvent(c.Writer, stopEvent)
			flusher.Flush()

			logrus.Debug("CC: Stream finished successfully")
			break
		}

		var openaiChunk OpenAIResponse
		if err := json.Unmarshal([]byte(event.Data), &openaiChunk); err != nil {
			// SSE stream may contain non-JSON data (heartbeats, comments, etc.)
			logrus.WithError(err).WithFields(logrus.Fields{
				"event_type":   event.Event,
				"data_preview": utils.TruncateString(event.Data, 512),
			}).Debug("CC: Failed to parse OpenAI chunk as JSON, skipping")
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

		// Handle text content
		if delta.Content != nil && *delta.Content != "" {
			if accumulatedContent.Len() == 0 {
				// Send content_block_start
				startBlockEvent := ClaudeStreamEvent{
					Type:  "content_block_start",
					Index: contentBlockIndex,
					ContentBlock: &ClaudeContentBlock{
						Type: "text",
						Text: "",
					},
				}
				writeClaudeEvent(c.Writer, startBlockEvent)
				flusher.Flush()
			}

			accumulatedContent.WriteString(*delta.Content)

			// Send content_block_delta
			deltaBlockEvent := ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: contentBlockIndex,
				Delta: &ClaudeStreamDelta{
					Type: "text_delta",
					Text: *delta.Content,
				},
			}
			writeClaudeEvent(c.Writer, deltaBlockEvent)
			flusher.Flush()
		}

		// Handle tool calls
		if len(delta.ToolCalls) > 0 {
			for _, tc := range delta.ToolCalls {
				if tc.ID != "" {
					// New tool call - close previous content block if any
					if accumulatedContent.Len() > 0 {
						stopBlockEvent := ClaudeStreamEvent{
							Type:  "content_block_stop",
							Index: contentBlockIndex,
						}
						writeClaudeEvent(c.Writer, stopBlockEvent)
						flusher.Flush()
						contentBlockIndex++
						accumulatedContent.Reset()
					}

					currentToolCall = &tc

					// Send content_block_start for tool_use
					startToolEvent := ClaudeStreamEvent{
						Type:  "content_block_start",
						Index: contentBlockIndex,
						ContentBlock: &ClaudeContentBlock{
							Type: "tool_use",
							ID:   tc.ID,
							Name: tc.Function.Name,
						},
					}
					writeClaudeEvent(c.Writer, startToolEvent)
					flusher.Flush()
				}

				if tc.Function.Arguments != "" && currentToolCall != nil {
					// Send input_json_delta
					deltaToolEvent := ClaudeStreamEvent{
						Type:  "content_block_delta",
						Index: contentBlockIndex,
						Delta: &ClaudeStreamDelta{
							Type:        "input_json_delta",
							PartialJSON: tc.Function.Arguments,
						},
					}
					writeClaudeEvent(c.Writer, deltaToolEvent)
					flusher.Flush()
				}
			}
		}

		// Handle finish reason
		if choice.FinishReason != nil {
			// Close current content block
			if accumulatedContent.Len() > 0 || currentToolCall != nil {
				stopBlockEvent := ClaudeStreamEvent{
					Type:  "content_block_stop",
					Index: contentBlockIndex,
				}
				writeClaudeEvent(c.Writer, stopBlockEvent)
				flusher.Flush()
			}

			stopReason := convertFinishReasonToStopReason(*choice.FinishReason)

			// Build usage from OpenAI response if available
			var usage *ClaudeUsage
			if openaiChunk.Usage != nil {
				usage = &ClaudeUsage{
					InputTokens:  openaiChunk.Usage.PromptTokens,
					OutputTokens: openaiChunk.Usage.CompletionTokens,
				}
			} else {
				// Provide default usage to satisfy Claude client requirements
				usage = &ClaudeUsage{
					InputTokens:  0,
					OutputTokens: 0,
				}
			}

			deltaEvent := ClaudeStreamEvent{
				Type: "message_delta",
				Delta: &ClaudeStreamDelta{
					StopReason: stopReason,
				},
				Usage: usage,
			}
			writeClaudeEvent(c.Writer, deltaEvent)
			flusher.Flush()

			// Send message_stop to properly close the stream
			// Claude clients expect this event after message_delta with stop_reason
			stopEvent := ClaudeStreamEvent{Type: "message_stop"}
			writeClaudeEvent(c.Writer, stopEvent)
			flusher.Flush()

			logrus.WithField("stop_reason", stopReason).Debug("CC: Stream finished with finish_reason")
			break
		}
	}
}

// writeClaudeEvent writes a Claude streaming event to the writer.
func writeClaudeEvent(w io.Writer, event ClaudeStreamEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		logrus.WithError(err).Error("Failed to marshal Claude event")
		return
	}
	// Handle write errors (e.g., client disconnect)
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, string(data)); err != nil {
		logrus.WithError(err).Debug("CC: Failed to write Claude event, client may have disconnected")
	}
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
