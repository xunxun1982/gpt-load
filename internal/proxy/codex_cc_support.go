// Package proxy provides OpenAI Responses CC (Claude Code) support functionality.
// The implementation keeps Codex names for Codex CLI-compatible protocol details,
// not for a standalone codex channel type.
package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// isOpenAIResponseCCMode checks if the current request is in OpenAI Responses CC mode.
// The conversion still uses Codex CLI-compatible payload details internally.
func isOpenAIResponseCCMode(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyOpenAIResponseCC); ok {
		if enabled, ok := v.(bool); ok && enabled {
			return true
		}
	}
	return false
}

// CodexContentBlock represents a content block in Codex/Responses API format.
type CodexContentBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// CodexTool represents a tool definition in Codex/Responses API format.
type CodexTool struct {
	Type         string          `json:"type"`
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	Parameters   json.RawMessage `json:"parameters,omitempty"`
	Format       json.RawMessage `json:"format,omitempty"`
	Strict       *bool           `json:"strict,omitempty"`
	DeferLoading *bool           `json:"defer_loading,omitempty"`
	Execution    string          `json:"execution,omitempty"`
	Tools        []CodexTool     `json:"tools,omitempty"`
	Children     []CodexTool     `json:"children,omitempty"`
}

// CodexRequest represents a Codex/Responses API request.
type CodexRequest struct {
	Model             string          `json:"model"`
	Input             json.RawMessage `json:"input"`
	Instructions      string          `json:"instructions,omitempty"`
	MaxOutputTokens   *int            `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stream            bool            `json:"stream"`
	Tools             []CodexTool     `json:"tools,omitempty"`
	ToolChoice        interface{}     `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         *CodexReasoning `json:"reasoning,omitempty"`
	StreamOptions     json.RawMessage `json:"stream_options,omitempty"`
	ServiceTier       string          `json:"service_tier,omitempty"`
	PromptCacheKey    string          `json:"prompt_cache_key,omitempty"`
	Text              *CodexText      `json:"text,omitempty"`
	ClientMetadata    json.RawMessage `json:"client_metadata,omitempty"`
	Store             *bool           `json:"store,omitempty"`
	Include           []string        `json:"include,omitempty"`
	unsupportedFields []string
}

// CodexReasoning represents reasoning configuration for Codex CLI-compatible Responses requests.
// Effort values are provider-defined and are forwarded without normalization.
type CodexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"` // "auto", "none", "detailed"
	Context string `json:"context,omitempty"` // "auto", "current_turn", "all_turns"
}

// CodexText represents current Responses text controls used by Codex clients.
type CodexText struct {
	Verbosity string          `json:"verbosity,omitempty"`
	Format    json.RawMessage `json:"format,omitempty"`
}

// CodexOutputItem represents an output item in Codex/Responses API format.
type CodexOutputItem struct {
	Type    string              `json:"type"`
	ID      string              `json:"id,omitempty"`
	Status  string              `json:"status,omitempty"`
	Role    string              `json:"role,omitempty"`
	Content []CodexContentBlock `json:"content,omitempty"`
	// Summary is used for reasoning output items to contain the thinking summary.
	// Each summary item has type "summary_text" and text field.
	Summary   []CodexSummaryItem `json:"summary,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Namespace string             `json:"namespace,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	Input     any                `json:"input,omitempty"`
	Execution string             `json:"execution,omitempty"`
}

func (item CodexOutputItem) MarshalJSON() ([]byte, error) {
	type alias CodexOutputItem
	out, err := json.Marshal(alias(item))
	if err != nil {
		return nil, err
	}
	if item.Type != "tool_search_call" || strings.TrimSpace(item.Arguments) == "" {
		return out, nil
	}
	var payload map[string]any
	if err := decodeCodexJSONUseNumber(out, &payload); err != nil {
		return out, nil
	}
	var args any
	if err := decodeCodexJSONUseNumber([]byte(item.Arguments), &args); err == nil {
		payload["arguments"] = args
	}
	return json.Marshal(payload)
}

func (item *CodexOutputItem) UnmarshalJSON(data []byte) error {
	type alias CodexOutputItem
	var raw struct {
		alias
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*item = CodexOutputItem(raw.alias)
	if len(raw.Arguments) == 0 || string(raw.Arguments) == "null" {
		return nil
	}
	var arguments string
	if err := json.Unmarshal(raw.Arguments, &arguments); err == nil {
		item.Arguments = arguments
		return nil
	}
	item.Arguments = string(raw.Arguments)
	return nil
}

func codexCustomToolClaudeInput(input any) json.RawMessage {
	inputString := codexCustomToolInputString(input)
	out, err := json.Marshal(map[string]string{"input": inputString})
	if err != nil {
		return json.RawMessage(`{"input":""}`)
	}
	return out
}

func codexClaudeToolName(item CodexOutputItem, reverseToolNameMap map[string]string) string {
	if item.Type == "tool_search_call" {
		return codexToolSearchProxyName
	}
	toolName := item.Name
	if reverseToolNameMap != nil {
		if orig, ok := reverseToolNameMap[item.Name]; ok {
			toolName = orig
		}
	}
	return toolName
}

// codexToolCallID prefers the protocol call_id and falls back to item id for
// output kinds whose schema makes call_id optional (for example tool_search_call).
func codexToolCallID(item CodexOutputItem) string {
	if item.CallID != "" {
		return item.CallID
	}
	return item.ID
}

func codexClaudeToolInput(item CodexOutputItem) json.RawMessage {
	if item.Type == "custom_tool_call" {
		return codexCustomToolClaudeInput(item.Input)
	}
	// Tool arguments pass through verbatim: the converter must not interpret
	// provider-specific payloads (large integers survive as exact JSON text).
	return codexToolArgumentsRawMessage(item.Arguments)
}

func isCodexResponseToolCall(item CodexOutputItem) bool {
	if !codexToolCallItemType(item.Type) || codexToolCallID(item) == "" {
		return false
	}
	return codexClaudeToolName(item, nil) != ""
}

// CodexSummaryItem represents a summary item in reasoning output.
type CodexSummaryItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type TokenUsageDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// CodexUsage represents usage information in Codex/Responses API format.
type CodexUsage struct {
	InputTokens         int                `json:"input_tokens"`
	OutputTokens        int                `json:"output_tokens"`
	TotalTokens         int                `json:"total_tokens"`
	InputTokensDetails  *TokenUsageDetails `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *TokenUsageDetails `json:"output_tokens_details,omitempty"`
	// Extra fields preserve upstream cache/thinking counters for request logs.
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	ThinkingTokens   int `json:"thinking_tokens,omitempty"`
}

// CodexResponse represents a Codex/Responses API response.
type CodexResponse struct {
	ID          string            `json:"id"`
	Object      string            `json:"object"`
	CreatedAt   int64             `json:"created_at"`
	Status      string            `json:"status"`
	Model       string            `json:"model"`
	Output      []CodexOutputItem `json:"output"`
	Usage       *CodexUsage       `json:"usage,omitempty"`
	ToolChoice  string            `json:"tool_choice,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	Error       *CodexError       `json:"error,omitempty"`
}

// CodexError represents an error in Codex/Responses API format.
type CodexError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// NOTE: codexDefaultInstructions and CodexOfficialInstructions are defined in codex_instructions.go
// for better code organization and maintainability (per AI review suggestion).

// codexToolNameLimit is the maximum length for tool names in Codex CLI-compatible Responses requests.
// Names exceeding this limit will be shortened to ensure compatibility.
const codexToolNameLimit = 64

// buildToolNameShortMap builds a map of original names to shortened names,
// ensuring uniqueness within the request. This is necessary because multiple
// tools may have the same shortened name after truncation.
// Duplicate original names are skipped to prevent map overwrite issues.
//
// Per AI review: Fast path optimization - if all names are already <=64 chars
// and collision-free, we still build the map but avoid expensive shortening logic.
// The map is always returned for consistent downstream handling.
func buildToolNameShortMap(names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}

	// Fast path: check if any name needs shortening
	needsShortening := false
	for _, n := range names {
		if len(n) > codexToolNameLimit {
			needsShortening = true
			break
		}
	}

	// If no shortening needed, build identity map directly (fast path)
	if !needsShortening {
		result := make(map[string]string, len(names))
		seen := make(map[string]struct{}, len(names))
		for _, n := range names {
			if _, ok := seen[n]; ok {
				continue // Skip duplicates
			}
			seen[n] = struct{}{}
			result[n] = n
		}
		return result
	}

	// Slow path: need to shorten some names
	used := make(map[string]struct{}, len(names))
	result := make(map[string]string, len(names))
	seenOrig := make(map[string]struct{}, len(names))

	// Helper to get base candidate name
	baseCandidate := func(n string) string {
		if len(n) <= codexToolNameLimit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > codexToolNameLimit {
					return cand[:codexToolNameLimit]
				}
				return cand
			}
		}
		return n[:codexToolNameLimit]
	}

	// Helper to make name unique by appending suffix.
	// Per AI review: ensure at least 1 character from base is preserved to avoid
	// names like "_1" which may be rejected by Responses tool name charset rules.
	makeUnique := func(cand string) string {
		// Per AI review: guard against empty candidate to avoid invalid tool names like "_1"
		if cand == "" {
			cand = "tool"
		}
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; i < 1000; i++ {
			suffix := "_" + fmt.Sprintf("%d", i)
			allowed := codexToolNameLimit - len(suffix)
			// Ensure at least 1 character from base is preserved
			if allowed < 1 {
				allowed = 1
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
		// Per AI review: use UUID suffix if 1000 iterations exhausted to guarantee uniqueness.
		// This should never happen in practice but provides a robust fallback.
		// Loop until unique name found (UUID collision probability ~10^-18 per attempt).
		for {
			suffix := "_" + uuid.New().String()[:8]
			allowed := codexToolNameLimit - len(suffix)
			if allowed < 1 {
				allowed = 1
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			candidate := tmp + suffix
			if _, ok := used[candidate]; !ok {
				return candidate
			}
		}
	}

	for _, n := range names {
		// Skip duplicate original names to prevent map overwrite
		if _, ok := seenOrig[n]; ok {
			continue
		}
		seenOrig[n] = struct{}{}
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		result[n] = uniq
	}
	return result
}

// buildReverseToolNameMap builds a reverse map from shortened to original names.
// This is used to restore original tool names in responses.
func buildReverseToolNameMap(shortMap map[string]string) map[string]string {
	reverse := make(map[string]string, len(shortMap))
	for orig, short := range shortMap {
		reverse[short] = orig
	}
	return reverse
}

// convertClaudeToCodex converts a Claude request to Codex/Responses API format.
// The Codex/Responses API requires:
// 1. "instructions" field MUST be non-empty and contain a valid system prompt
// 2. "input" array uses structured format: {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "..."}]}
// Claude's system prompt is converted to a developer message in the input array.
// The customInstructions parameter allows overriding the default instructions for providers that validate this field.
// Tool name shortening is handled internally via buildToolNameShortMap; the reverse map is stored
// in context for response restoration (see setCodexToolNameReverseMap).
func convertClaudeToCodex(claudeReq *ClaudeRequest, customInstructions string, group *models.Group) (*CodexRequest, error) {
	effort, err := claudeOutputEffort(claudeReq.OutputConfig)
	if err != nil {
		return nil, err
	}
	thinkingActive := claudeThinkingActive(claudeReq.Thinking)
	thinkingDisabled := claudeThinkingDisabled(claudeReq.Thinking)

	// Use custom instructions if provided, otherwise use default
	instructions := codexDefaultInstructions
	if customInstructions != "" {
		instructions = customInstructions
	}

	codexReq := &CodexRequest{
		Model:        claudeReq.Model,
		Stream:       claudeReq.Stream,
		Temperature:  claudeReq.Temperature,
		TopP:         claudeReq.TopP,
		Instructions: instructions,
	}

	// Note: max_output_tokens is intentionally NOT sent.
	// Codex CLI (as of commit f7d2f3e) does not send this parameter.
	// Reason: Some providers may reject or mishandle this parameter, and the
	// Responses upstreams typically use provider defaults for output length.
	// See: https://github.com/openai/codex/issues/4138

	// Build tool name short map for tools that exceed the 64 char limit
	var toolNameShortMap map[string]string
	if len(claudeReq.Tools) > 0 {
		names := make([]string, 0, len(claudeReq.Tools))
		for _, tool := range claudeReq.Tools {
			names = append(names, tool.Name)
		}
		toolNameShortMap = buildToolNameShortMap(names)
	}

	// Build input array using the Codex CLI-compatible Responses format.
	var inputItems []interface{}

	// Claude Code may (non-conformingly) place system prompts inside messages
	// with role "system"; merge them into the system item below instead of
	// emitting a raw system message (which some Responses upstreams reject).
	inlineSystem, nonSystemMessages, err := collectInlineClaudeSystemMessages(claudeReq.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Claude message: %w", err)
	}
	var convertedInputs []interface{}
	for _, msg := range nonSystemMessages {
		converted, err := convertClaudeMessageToCodexFormatWithToolMap(msg, toolNameShortMap)
		if err != nil {
			return nil, fmt.Errorf("failed to convert Claude message: %w", err)
		}
		convertedInputs = append(convertedInputs, converted...)
	}

	// Preserve Claude's system prompt as a developer input item by default.
	systemContent := ""
	if len(claudeReq.System) > 0 {
		systemContent = extractSystemContent(claudeReq.System)
	}
	if inlineSystem != "" {
		if systemContent != "" {
			systemContent += "\n\n" + inlineSystem
		} else {
			systemContent = inlineSystem
		}
	}
	if systemContent != "" {
		systemRole := "developer"
		if getGroupConfigBool(group, "responses_legacy_user_role") {
			systemRole = "user"
		}
		inputItems = append(inputItems, map[string]interface{}{
			"type": "message",
			"role": systemRole,
			"content": []map[string]interface{}{
				{"type": "input_text", "text": systemContent},
			},
		})
		logrus.WithFields(logrus.Fields{"system_len": len(systemContent), "role": systemRole}).Debug("Codex CC: Added system message")
	}

	// Handle prompt-only requests
	if len(nonSystemMessages) == 0 && strings.TrimSpace(claudeReq.Prompt) != "" {
		inputItems = append(inputItems, map[string]interface{}{
			"type": "message",
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "input_text", "text": strings.TrimSpace(claudeReq.Prompt)},
			},
		})
	}

	inputItems = append(inputItems, convertedInputs...)

	// Marshal input items
	inputBytes, err := json.Marshal(inputItems)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input items: %w", err)
	}
	codexReq.Input = inputBytes

	// Convert tools with shortened names
	if len(claudeReq.Tools) > 0 {
		tools := make([]CodexTool, 0, len(claudeReq.Tools))
		for _, tool := range claudeReq.Tools {
			// Apply shortened name if needed
			toolName := tool.Name
			if short, ok := toolNameShortMap[tool.Name]; ok {
				toolName = short
			}
			// Normalize tool parameters to ensure valid JSON schema
			params := normalizeToolParameters(tool.InputSchema)
			tools = append(tools, CodexTool{
				Type:        "function",
				Name:        toolName,
				Description: tool.Description,
				Parameters:  params,
			})
		}
		codexReq.Tools = tools
	}

	// Convert tool_choice with shortened name if applicable
	// Claude tool_choice types: "auto", "any", "tool" (with name), "none"
	// Codex/OpenAI tool_choice: "auto", "required", "none", or {"type": "function", "name": "..."}
	if len(claudeReq.ToolChoice) > 0 {
		var toolChoice map[string]interface{}
		if err := json.Unmarshal(claudeReq.ToolChoice, &toolChoice); err != nil {
			// Per AI review: log parse error at debug level for troubleshooting
			logrus.WithError(err).Debug("Codex CC: Failed to parse tool_choice, using default")
		} else {
			if tcType, ok := toolChoice["type"].(string); ok {
				switch tcType {
				case "tool":
					if toolName, ok := toolChoice["name"].(string); ok {
						// Apply shortened name if needed
						if short, ok := toolNameShortMap[toolName]; ok {
							toolName = short
						}
						codexReq.ToolChoice = map[string]interface{}{
							"type": "function",
							"name": toolName,
						}
					}
				case "any":
					codexReq.ToolChoice = "required"
				case "auto":
					codexReq.ToolChoice = "auto"
				case "none":
					// Prevent tool calling even when tools are defined
					codexReq.ToolChoice = "none"
				}
			}
		}
	}

	// Apply parallel_tool_calls config for OpenAI Responses CC mode.
	// Only set when tools are present (some upstreams reject the parameter without tools).
	// Default behavior: if not configured, enable parallel tool calls for Codex CLI-compatible requests.
	// Users can disable via group config: {"parallel_tool_calls": false}
	// NOTE: force_function_call precedence check is not needed here because force_function_call
	// is UI-restricted to OpenAI chat channels and is not applicable to OpenAI Responses.
	if len(codexReq.Tools) > 0 {
		parallelConfig := getParallelToolCallsConfig(group)
		if parallelConfig != nil {
			codexReq.ParallelToolCalls = parallelConfig
		} else {
			// Default to true to preserve the original Codex CLI-compatible behavior.
			parallelCalls := true
			codexReq.ParallelToolCalls = &parallelCalls
		}
	}

	// Explicit effort only moves between protocol field shapes. budget_tokens
	// has no lossless Responses equivalent, so it is not converted to a guessed
	// effort value. When effort is absent, leave it omitted so the upstream
	// provider can apply its own default; empty is not a portable "auto" value.
	// Explicitly disabled thinking maps to the target protocol's explicit off value.
	if effort != "" || thinkingActive || thinkingDisabled {
		// Explicitly disabled thinking takes precedence over a conflicting
		// effort value; forwarding both can re-enable reasoning or yield 400.
		if thinkingDisabled {
			// Model names are provider-defined and dynamic; preserve the explicit
			// disable intent instead of guessing support from a stale local list.
			effort = "none"
		}
		codexReq.Reasoning = &CodexReasoning{Effort: effort}
	}
	if thinkingActive {
		codexReq.Reasoning.Summary = "auto"
		// Disable response storage for privacy (store: false means don't store)
		// Reference: CLIProxyAPI uses sjson.Set(template, "store", false)
		store := false
		codexReq.Store = &store
		// Include encrypted reasoning content for full thinking support
		codexReq.Include = []string{"reasoning.encrypted_content"}
	}

	return codexReq, nil
}

// normalizeToolParameters ensures tool parameters have valid JSON schema structure.
// Returns a valid JSON schema with at least type and properties fields.
func normalizeToolParameters(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	var schema map[string]interface{}
	if err := decodeCodexJSONUseNumber(raw, &schema); err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	// Ensure type field exists
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}

	// Ensure properties field exists for object type
	if schema["type"] == "object" {
		if _, ok := schema["properties"]; !ok {
			schema["properties"] = map[string]interface{}{}
		}
	}

	// Remove $schema field if present (not needed for OpenAI Responses).
	delete(schema, "$schema")

	result, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return result
}

// convertClaudeMessageToCodexFormatWithToolMap converts a single Claude message to Codex input items.
// Uses the tool name short map to apply shortened names for tool_use blocks.
func convertClaudeMessageToCodexFormatWithToolMap(msg ClaudeMessage, toolNameShortMap map[string]string) ([]interface{}, error) {
	var result []interface{}
	if msg.Role != "user" && msg.Role != "assistant" {
		return nil, fmt.Errorf("unsupported Anthropic message role %q", msg.Role)
	}

	// Try to parse content as string first
	var contentStr string
	if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
		contentType := "input_text"
		if msg.Role == "assistant" {
			contentType = "output_text"
		}
		result = append(result, map[string]interface{}{
			"type": "message",
			"role": msg.Role,
			"content": []map[string]interface{}{
				{"type": contentType, "text": contentStr},
			},
		})
		return result, nil
	}

	// Parse content as array of blocks
	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, fmt.Errorf("failed to parse content blocks: %w", err)
	}

	// Separate block types. Claude thinking history has no Responses reasoning ID
	// or encrypted_content, so replay it as assistant text instead of inventing an
	// invalid reasoning item for store:false requests.
	var textParts []string
	var reasoningItems []interface{}
	var toolCalls []interface{}
	var toolResults []interface{}

	for _, block := range blocks {
		switch {
		case block.Type == "text":
			textParts = append(textParts, block.Text)
		case block.Type == "thinking":
			if block.Thinking != "" {
				// Reasoning items are only valid for assistant messages (OpenAI
				// Responses semantics: reasoning belongs to the assistant output);
				// thinking from any other role is replayed as text so it is never
				// silently dropped by the role dispatch below.
				if msg.Role == "assistant" && block.ID != "" && block.EncryptedContent != "" {
					reasoningItems = append(reasoningItems, map[string]interface{}{
						"type":              "reasoning",
						"id":                block.ID,
						"encrypted_content": block.EncryptedContent,
						"status":            "completed",
						"summary":           []map[string]interface{}{{"type": "summary_text", "text": block.Thinking}},
					})
				} else {
					textParts = append(textParts, block.Thinking)
				}
			}
		case isClaudeToolUseBlock(block):
			if msg.Role != "assistant" {
				return nil, fmt.Errorf("Anthropic tool_use block %q is only valid in an assistant message", block.Type)
			}
			if block.ID == "" || block.Name == "" {
				return nil, fmt.Errorf("Anthropic tool_use requires id and name")
			}
			// Apply shortened name if needed
			toolName := block.Name
			if short, ok := toolNameShortMap[block.Name]; ok {
				toolName = short
			}
			argsStr := string(block.Input)
			// Normalize blank or JSON null input to "{}" so Codex clients never
			// receive the literal "null" as arguments, matching the OpenAI
			// conversion semantics (convertClaudeMessageToOpenAI).
			if trimmed := strings.TrimSpace(argsStr); trimmed == "" || trimmed == "null" {
				argsStr = "{}"
			}
			// Claude tool ids are used verbatim as Codex call ids.
			toolCalls = append(toolCalls, map[string]interface{}{
				"type":      "function_call",
				"id":        "fc_" + block.ID,
				"call_id":   block.ID,
				"name":      toolName,
				"arguments": argsStr,
			})
		case isClaudeToolResultBlock(block):
			// Anthropic only allows tool_result blocks in user messages; an
			// assistant-role tool result is non-conformant input. Reject it
			// explicitly so it is not silently collected and then dropped by the
			// assistant branch below (mirroring convertClaudeMessageToOpenAI).
			if msg.Role == "assistant" {
				return nil, fmt.Errorf("Anthropic tool_result block %q is not valid in an assistant message", block.Type)
			}
			if block.ToolUseID == "" {
				return nil, fmt.Errorf("Anthropic tool_result requires tool_use_id")
			}
			toolResults = append(toolResults, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": block.ToolUseID,
				"output":  toolResultOutput(block),
			})
		default:
			// Chat conversion has an explicit image mapper, but Codex input does
			// not. Fail closed for every unmapped block instead of dropping data.
			return nil, ccUnsupported("content block", block.Type)
		}
	}

	// Build result based on role
	switch msg.Role {
	case "assistant":
		result = append(result, reasoningItems...)
		if len(textParts) > 0 {
			result = append(result, map[string]interface{}{
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": strings.Join(textParts, "")},
				},
			})
		}
		result = append(result, toolCalls...)
	case "user":
		if len(textParts) > 0 {
			result = append(result, map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": strings.Join(textParts, "")},
				},
			})
		}
		result = append(result, toolResults...)
	}

	return result, nil
}

// extractSystemContent extracts text content from Claude system field.
func extractSystemContent(system json.RawMessage) string {
	var systemContent string
	if err := json.Unmarshal(system, &systemContent); err == nil {
		return systemContent
	}
	// System might be an array of content blocks
	var systemBlocks []ClaudeContentBlock
	if err := json.Unmarshal(system, &systemBlocks); err == nil {
		var sb strings.Builder
		for _, block := range systemBlocks {
			if block.Type == "text" {
				sb.WriteString(block.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func toolResultOutput(block ClaudeContentBlock) any {
	if len(block.Content) == 0 {
		if block.IsError {
			return map[string]any{"is_error": true, "content": ""}
		}
		return ""
	}
	var value any
	if err := decodeCodexJSONUseNumber(block.Content, &value); err == nil {
		if block.IsError {
			return map[string]any{"is_error": true, "content": value}
		}
		return value
	}
	if block.IsError {
		return map[string]any{"is_error": true, "content": string(block.Content)}
	}
	return string(block.Content)
}

// extractToolResultContent extracts content from a tool_result block.
// Converts Windows paths to Unix-style for Claude Code compatibility.
func extractToolResultContent(block ClaudeContentBlock) string {
	var resultContent string
	if err := json.Unmarshal(block.Content, &resultContent); err == nil {
		return convertWindowsPathsInToolResult(resultContent)
	}
	var contentBlocks []ClaudeContentBlock
	if err := json.Unmarshal(block.Content, &contentBlocks); err == nil {
		var sb strings.Builder
		for _, cb := range contentBlocks {
			if cb.Type == "text" {
				sb.WriteString(cb.Text)
			}
		}
		return convertWindowsPathsInToolResult(sb.String())
	}
	return convertWindowsPathsInToolResult(string(block.Content))
}

// convertCodexToClaudeResponse converts a Codex/Responses API response to Claude format.
// The reverseToolNameMap is used to restore original tool names that were shortened.
func convertCodexToClaudeResponse(codexResp *CodexResponse, reverseToolNameMap map[string]string) *ClaudeResponse {
	claudeResp := &ClaudeResponse{
		ID:      codexResp.ID,
		Type:    "message",
		Role:    "assistant",
		Model:   codexResp.Model,
		Content: make([]ClaudeContentBlock, 0),
	}

	for _, item := range codexResp.Output {
		switch {
		case item.Type == "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					if content.Text != "" {
						// Convert Windows paths to Unix-style for Claude Code compatibility
						text := convertWindowsPathsInToolResult(content.Text)
						claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
							Type: "text",
							Text: text,
						})
					}
				case "refusal":
					if content.Text != "" {
						// Convert Windows paths to Unix-style for Claude Code compatibility
						text := convertWindowsPathsInToolResult(content.Text)
						claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
							Type: "text",
							Text: text,
						})
					}
				}
			}
		case isCodexResponseToolCall(item):
			toolName := codexClaudeToolName(item, reverseToolNameMap)
			if toolName == "" {
				continue
			}
			if item.Type != "custom_tool_call" && !isValidCodexToolCallArguments(toolName, item.Arguments, nil) {
				continue
			}
			claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
				Type:  "tool_use",
				ID:    codexToolCallID(item),
				Name:  toolName,
				Input: codexClaudeToolInput(item),
			})
		case item.Type == "reasoning":
			// Convert reasoning to thinking block.
			// Codex CLI-compatible Responses returns reasoning in "summary" field with type "summary_text".
			// First try summary field, then fall back to content.
			var thinkingText strings.Builder
			for _, summaryItem := range item.Summary {
				if summaryItem.Type == "summary_text" && summaryItem.Text != "" {
					thinkingText.WriteString(summaryItem.Text)
				}
			}
			// Fall back to content field if summary is empty (for compatibility)
			if thinkingText.Len() == 0 {
				for _, content := range item.Content {
					if content.Type == "output_text" && content.Text != "" {
						thinkingText.WriteString(content.Text)
					}
				}
			}
			if thinkingText.Len() > 0 {
				logrus.WithField("thinking_len", thinkingText.Len()).Debug("Codex CC: Converted reasoning to thinking block")
				// Convert Windows paths to Unix-style for Claude Code compatibility
				thinking := convertWindowsPathsInToolResult(thinkingText.String())
				claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
					Type:     "thinking",
					Thinking: thinking,
				})
			} else {
				logrus.WithFields(logrus.Fields{
					"summary_count": len(item.Summary),
					"content_count": len(item.Content),
				}).Debug("Codex CC: Reasoning item has no text content")
			}
		}
	}

	// Determine stop reason
	hasToolUse := false
	for _, block := range claudeResp.Content {
		if block.Type == "tool_use" {
			hasToolUse = true
			break
		}
	}

	if hasToolUse {
		stopReason := "tool_use"
		claudeResp.StopReason = &stopReason
	} else if codexResp.Status == "completed" {
		stopReason := "end_turn"
		claudeResp.StopReason = &stopReason
	}

	// Convert usage
	if codexResp.Usage != nil {
		claudeResp.Usage = &ClaudeUsage{
			InputTokens:  codexResp.Usage.InputTokens,
			OutputTokens: codexResp.Usage.OutputTokens,
		}
	} else {
		claudeResp.Usage = &ClaudeUsage{
			InputTokens:  0,
			OutputTokens: 0,
		}
	}
	applyTokenMultiplier(claudeResp.Usage)

	return claudeResp
}

// Context key for storing tool name reverse map for response conversion.
const ctxKeyCodexToolNameReverseMap = "codex_tool_name_reverse_map"

// applyCodexCCRequestConversion converts Claude request to Codex CLI-compatible Responses format.
// Returns the converted body bytes, whether conversion was applied, and any error.
// Also stores the tool name reverse map in context for response conversion.
func (ps *ProxyServer) applyCodexCCRequestConversion(
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
	if originalModel != "" {
		setModelRedirectContext(c, originalModel, -1, true)
	}

	// Apply model redirect rules for Codex CC mode
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

				logrus.WithFields(logFields).Debug("Codex CC: Applied model redirect")
			} else if targetModel == "" && group.ModelRedirectStrict {
				// Strict mode: model not found in redirect rules
				return bodyBytes, false, fmt.Errorf("model '%s' is not configured in redirect rules", originalModel)
			}
		} else if group.ModelRedirectStrict {
			// Strict mode with no redirect rules configured
			return bodyBytes, false, fmt.Errorf("model '%s' is not configured in redirect rules (no rules defined)", originalModel)
		}
	}

	// Auto-select thinking model when thinking mode is enabled.
	// AI REVIEW NOTE: Suggestion to validate thinking model against a supported list was considered.
	// This is intentionally NOT implemented because:
	// 1. Model names are dynamically configured by users and vary across providers
	// 2. New models are released frequently; hardcoding a list would require constant updates
	// 3. Invalid model names will be rejected by the upstream API with clear error messages
	// 4. Users have full control over their group configuration
	if claudeReq.Thinking != nil && strings.EqualFold(claudeReq.Thinking.Type, "enabled") {
		thinkingModel := getThinkingModel(group)
		if thinkingModel != "" && thinkingModel != claudeReq.Model {
			logrus.WithFields(logrus.Fields{
				"group":          group.Name,
				"original_model": claudeReq.Model,
				"thinking_model": thinkingModel,
				"budget_tokens":  claudeReq.Thinking.BudgetTokens,
			}).Info("Codex CC: Auto-selecting thinking model for extended thinking")
			claudeReq.Model = thinkingModel
			c.Set("thinking_model_applied", true)
			// NOTE: c.Set("thinking_model", thinkingModel) removed per AI review.
			// Only thinking_model_applied is used by downstream handlers (function_call.go).
		}
	}

	// Get custom instructions from group config (for providers that validate instructions field)
	// Mode: "auto" (default), "official", "custom"
	instructionsMode := getGroupConfigString(group, "codex_instructions_mode")
	customInstructions := ""

	switch instructionsMode {
	case "official":
		// Use official Codex CLI instructions
		customInstructions = CodexOfficialInstructions
	case "custom":
		// Use custom instructions from config
		customInstructions = getGroupConfigString(group, "codex_instructions")
	default:
		// "auto" or empty: use default instructions (codexDefaultInstructions)
		customInstructions = ""
	}

	// Build tool name short map and store reverse map in context for response conversion.
	// AI REVIEW NOTE: This map is also built inside convertClaudeToCodex for internal use.
	// The duplication is intentional because:
	// 1. The function is deterministic (same input produces same output)
	// 2. We need the reverse map stored in context for response conversion
	// 3. Changing convertClaudeToCodex signature would affect other callers
	// 4. The performance impact is negligible (typically < 100 tools)
	if len(claudeReq.Tools) > 0 {
		names := make([]string, 0, len(claudeReq.Tools))
		for _, tool := range claudeReq.Tools {
			names = append(names, tool.Name)
		}
		shortMap := buildToolNameShortMap(names)
		reverseMap := buildReverseToolNameMap(shortMap)
		c.Set(ctxKeyCodexToolNameReverseMap, reverseMap)
	}

	// Convert to Codex CLI-compatible Responses format with custom instructions.
	codexReq, err := convertClaudeToCodex(&claudeReq, customInstructions, group)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to convert Claude to Codex: %w", err)
	}

	// Marshal Codex request
	convertedBody, err := json.Marshal(codexReq)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to marshal Codex request: %w", err)
	}

	// Mark CC conversion as enabled for OpenAI Responses.
	c.Set(ctxKeyCCEnabled, true)
	c.Set(ctxKeyOriginalFormat, "claude")
	c.Set(ctxKeyOpenAIResponseCC, true)

	// Debug log: output converted request details for troubleshooting
	// Only log input_preview when EnableRequestBodyLogging is enabled to avoid leaking sensitive data
	logFields := logrus.Fields{
		"group":              group.Name,
		"original_model":     originalModel,
		"codex_model":        codexReq.Model,
		"stream":             codexReq.Stream,
		"tools_count":        len(codexReq.Tools),
		"converted_body_len": len(convertedBody),
	}
	if group.EffectiveConfig.EnableRequestBodyLogging {
		// Per AI review: use TruncateString for UTF-8 safe truncation and SanitizeErrorBody
		// to prevent leaking secrets/PII. Sanitize first, then truncate.
		inputPreview := utils.TruncateString(utils.SanitizeErrorBody(string(codexReq.Input)), 500)
		logFields["input_preview"] = inputPreview
	}
	logrus.WithFields(logFields).Debug("OpenAI Responses CC: Converted Claude request to Responses format")

	return convertedBody, true, nil
}

// getCodexToolNameReverseMap retrieves the tool name reverse map from context.
// Returns nil if not found.
func getCodexToolNameReverseMap(c *gin.Context) map[string]string {
	if v, ok := c.Get(ctxKeyCodexToolNameReverseMap); ok {
		if m, ok := v.(map[string]string); ok {
			return m
		}
	}
	return nil
}

// CodexStreamEvent represents a Codex streaming event.
type CodexStreamEvent struct {
	Type        string             `json:"type"`
	ResponseID  string             `json:"response_id,omitempty"`
	ItemID      string             `json:"item_id,omitempty"`
	OutputIdx   int                `json:"output_index,omitempty"`
	ContentIdx  int                `json:"content_index,omitempty"`
	Item        *CodexOutputItem   `json:"item,omitempty"`
	Part        *CodexContentBlock `json:"part,omitempty"`
	Delta       string             `json:"delta,omitempty"`
	Text        string             `json:"text,omitempty"`
	Arguments   string             `json:"arguments,omitempty"`
	Input       any                `json:"input,omitempty"`
	Response    *CodexResponse     `json:"response,omitempty"`
	Error       *CodexError        `json:"error,omitempty"`
	SequenceNum int                `json:"sequence_number,omitempty"`
}

// codexStreamState tracks state during Codex streaming response conversion.
// AI REVIEW NOTE: Added openBlockType field per AI review to track block start/stop pairing.
// This prevents orphaned stops and ensures proper SSE contract compliance.
type codexStreamState struct {
	messageID       string
	currentToolID   string
	currentToolName string
	currentToolArgs strings.Builder
	toolInputSent   bool
	toolUseBlocks   []ClaudeContentBlock
	model           string
	// nextClaudeIndex tracks the next content_block index for Claude events.
	// This is independent of Codex's output_index/content_index to ensure
	// Claude clients receive sequential, non-conflicting indices.
	// Index is incremented only after content_block_stop events to maintain
	// correct ordering for Claude clients.
	nextClaudeIndex int
	// openBlockType tracks the type of currently open block at nextClaudeIndex.
	// Values: "", "text", "thinking", "tool". Empty means no block is open.
	// Used to prevent orphaned stops and ensure proper block pairing.
	openBlockType string
	// finalSent tracks whether the final message_delta/message_stop events have been sent.
	// This prevents duplicate final events when response.completed is received multiple times
	// or when [DONE] is processed after response.completed.
	finalSent bool
	// reverseToolNameMap maps shortened tool names back to original names.
	// Used to restore original tool names in streaming responses.
	reverseToolNameMap map[string]string
	// inThinkingBlock tracks whether we are currently inside a thinking/reasoning block.
	// Used to properly handle reasoning summary events.
	inThinkingBlock bool
	// skipActiveToolItem marks the current output item as skipped because its
	// tool name could not be resolved. Argument deltas for such an item are
	// ignored so they cannot auto-open a fabricated unknown_tool block.
	// Cleared whenever a new output item begins.
	skipActiveToolItem bool
}

// newCodexStreamState creates a new stream state for Codex CLI-compatible Responses conversion.
// The reverseToolNameMap is used to restore original tool names in streaming responses.
func newCodexStreamState(reverseToolNameMap map[string]string) *codexStreamState {
	return &codexStreamState{
		messageID:          "msg_" + uuid.New().String()[:8],
		reverseToolNameMap: reverseToolNameMap,
	}
}

// processCodexStreamEvent processes a single Codex stream event and returns Claude events.
func (s *codexStreamState) processCodexStreamEvent(event *CodexStreamEvent) []ClaudeStreamEvent {
	var events []ClaudeStreamEvent

	// closeOpenBlock closes any currently open block and increments the index.
	// This helper ensures proper block pairing and prevents orphaned stops.
	closeOpenBlock := func() {
		if s.openBlockType == "" {
			return
		}
		events = append(events, ClaudeStreamEvent{
			Type:  "content_block_stop",
			Index: s.nextClaudeIndex,
		})
		s.nextClaudeIndex++
		s.openBlockType = ""
		s.inThinkingBlock = false
	}
	appendFinalMessageEvents := func(stopReason string) {
		events = append(events, ClaudeStreamEvent{
			Type:  "message_delta",
			Delta: &ClaudeStreamDelta{StopReason: stopReason},
			Usage: &ClaudeUsage{OutputTokens: 0},
		})
		events = append(events, ClaudeStreamEvent{Type: "message_stop"})
	}
	appendToolInputDelta := func(inputJSON json.RawMessage) {
		if len(inputJSON) == 0 {
			return
		}
		events = append(events, ClaudeStreamEvent{
			Type:  "content_block_delta",
			Index: s.nextClaudeIndex,
			Delta: &ClaudeStreamDelta{
				Type:        "input_json_delta",
				PartialJSON: string(inputJSON),
			},
		})
		s.toolInputSent = true
	}
	toolInputForCurrentItem := func(item CodexOutputItem) json.RawMessage {
		if item.Type == "custom_tool_call" && item.Input == nil {
			item.Input = s.currentToolArgs.String()
		}
		if item.Arguments == "" {
			item.Arguments = s.currentToolArgs.String()
		}
		return codexClaudeToolInput(item)
	}

	switch event.Type {
	case "response.created":
		if event.Response != nil {
			s.model = event.Response.Model
		}
		// Send message_start event
		events = append(events, ClaudeStreamEvent{
			Type: "message_start",
			Message: &ClaudeResponse{
				ID:    s.messageID,
				Type:  "message",
				Role:  "assistant",
				Model: s.model,
				Usage: &ClaudeUsage{InputTokens: 0, OutputTokens: 0},
			},
		})

	// Reasoning summary events - convert to Claude thinking blocks
	case "response.reasoning_summary_part.added":
		// Close any open block before starting thinking block
		closeOpenBlock()
		// Start a thinking content block
		s.inThinkingBlock = true
		s.openBlockType = "thinking"
		logrus.WithField("claude_index", s.nextClaudeIndex).Debug("Codex CC: Starting thinking block")
		events = append(events, ClaudeStreamEvent{
			Type:  "content_block_start",
			Index: s.nextClaudeIndex,
			ContentBlock: &ClaudeContentBlock{
				Type:     "thinking",
				Thinking: "",
			},
		})

	case "response.reasoning_summary_text.delta":
		// Delta for thinking content
		// Auto-start thinking block if not present (handles case where part.added event is missing or out of order)
		// NOTE: AI suggested extracting auto-start logic to a helper function, but we intentionally keep it inline because:
		// 1. Each block type has different state requirements (thinking needs inThinkingBlock, tool needs ID/Name fallback)
		// 2. ContentBlock structures differ significantly between types
		// 3. Inline code is more readable and easier to maintain for this state machine pattern
		if event.Delta != "" && !s.inThinkingBlock {
			closeOpenBlock()
			s.inThinkingBlock = true
			s.openBlockType = "thinking"
			logrus.WithField("claude_index", s.nextClaudeIndex).Debug("Codex CC: Auto-starting thinking block for reasoning_summary_text")
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_start",
				Index: s.nextClaudeIndex,
				ContentBlock: &ClaudeContentBlock{
					Type:     "thinking",
					Thinking: "",
				},
			})
		}
		if event.Delta != "" && s.inThinkingBlock && s.openBlockType == "thinking" {
			logrus.WithFields(logrus.Fields{
				"delta_len":    len(event.Delta),
				"claude_index": s.nextClaudeIndex,
			}).Debug("Codex CC: Thinking delta received")
			// Convert Windows paths to Unix-style for Claude Code compatibility
			thinkingDelta := convertWindowsPathsInToolResult(event.Delta)
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: s.nextClaudeIndex,
				Delta: &ClaudeStreamDelta{
					Type:     "thinking_delta",
					Thinking: thinkingDelta,
				},
			})
		}

	case "response.reasoning_summary_text.done":
		// Text done event - no action needed, part.done handles block closure.
		// This event contains the full text but we've already streamed deltas.
		logrus.WithField("text_len", len(event.Text)).Debug("Codex CC: Reasoning summary text done")

	case "response.reasoning_summary_part.done":
		// End thinking content block only if one is open
		if s.openBlockType == "thinking" {
			logrus.WithField("claude_index", s.nextClaudeIndex).Debug("Codex CC: Ending thinking block")
			closeOpenBlock()
		}

	// Handle non-summary reasoning events (encrypted reasoning content).
	// These events contain the raw reasoning text when include=["reasoning.encrypted_content"] is set.
	case "response.reasoning_text.delta":
		// Auto-start thinking block if not present (reasoning_text independent of summary).
		// Per AI review: reasoning_text and reasoning_summary are independent event streams.
		if event.Delta != "" && !s.inThinkingBlock {
			closeOpenBlock()
			s.inThinkingBlock = true
			s.openBlockType = "thinking"
			logrus.WithField("claude_index", s.nextClaudeIndex).Debug("Codex CC: Auto-starting thinking block for reasoning_text")
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_start",
				Index: s.nextClaudeIndex,
				ContentBlock: &ClaudeContentBlock{
					Type:     "thinking",
					Thinking: "",
				},
			})
		}
		// Delta for raw reasoning content
		if event.Delta != "" && s.inThinkingBlock && s.openBlockType == "thinking" {
			logrus.WithFields(logrus.Fields{
				"delta_len":    len(event.Delta),
				"claude_index": s.nextClaudeIndex,
			}).Debug("Codex CC: Reasoning text delta received")
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: s.nextClaudeIndex,
				Delta: &ClaudeStreamDelta{
					Type:     "thinking_delta",
					Thinking: event.Delta,
				},
			})
		}

	case "response.reasoning_text.done":
		// Raw reasoning text done - no action needed, part.done handles block closure.
		logrus.WithField("text_len", len(event.Text)).Debug("Codex CC: Reasoning text done")

	case "response.in_progress", "response.queued":
		// Response is being generated, no action needed
		logrus.WithField("status", event.Type).Debug("Codex CC: Response status update")

	case "response.output_item.added":
		if event.Item != nil {
			// A new output item resets the skip state of a previously skipped tool item.
			s.skipActiveToolItem = false
			logrus.WithFields(logrus.Fields{
				"item_type":    event.Item.Type,
				"item_id":      event.Item.ID,
				"item_call_id": event.Item.CallID,
				"item_name":    event.Item.Name,
				"output_idx":   event.OutputIdx,
			}).Debug("Codex CC: Output item added")
			switch {
			case event.Item.Type == "message":
				// Message item added, wait for content_part.added for actual content
				logrus.WithField("item_type", event.Item.Type).Debug("Codex CC: Message item added")
			case codexToolCallItemType(event.Item.Type):
				toolName := codexClaudeToolName(*event.Item, s.reverseToolNameMap)
				toolCallID := codexToolCallID(*event.Item)
				if toolCallID == "" || toolName == "" {
					closeOpenBlock()
					s.currentToolID = ""
					s.currentToolName = ""
					s.currentToolArgs.Reset()
					s.toolInputSent = false
					// Mark this output item as skipped so argument deltas cannot
					// auto-open a fabricated unknown_tool block (per AI review).
					s.skipActiveToolItem = true
					logrus.WithFields(logrus.Fields{
						"item_type":    event.Item.Type,
						"item_call_id": toolCallID,
						"item_name":    event.Item.Name,
					}).Debug("Codex CC: Skipping tool item with missing ID or unresolved name")
					return events
				}
				// Resolve and validate the name before replacing the active tool state.
				closeOpenBlock()
				s.currentToolID = toolCallID
				s.currentToolName = toolName
				s.currentToolArgs.Reset()
				s.toolInputSent = false
				// Content block start for tool_use
				logrus.WithFields(logrus.Fields{
					"tool_id":       s.currentToolID,
					"tool_name":     s.currentToolName,
					"original_name": event.Item.Name,
					"claude_index":  s.nextClaudeIndex,
				}).Debug("Codex CC: Function call started")
				s.openBlockType = "tool"
				events = append(events, ClaudeStreamEvent{
					Type:  "content_block_start",
					Index: s.nextClaudeIndex,
					ContentBlock: &ClaudeContentBlock{
						Type:  "tool_use",
						ID:    s.currentToolID,
						Name:  s.currentToolName,
						Input: json.RawMessage("{}"),
					},
				})
			}
		}

	case "response.content_part.added":
		// Content part added - start a new content block only for output_text
		if event.Part != nil && event.Part.Type == "output_text" {
			// Close any open block before starting text block
			closeOpenBlock()
			s.openBlockType = "text"
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_start",
				Index: s.nextClaudeIndex,
				ContentBlock: &ClaudeContentBlock{
					Type: "text",
					Text: "",
				},
			})
		}

	case "response.output_text.delta":
		// Per AI review: guard delta emission with block state to prevent orphan deltas.
		// Auto-open text block if not present when receiving first delta.
		if event.Delta != "" && s.openBlockType != "text" {
			closeOpenBlock()
			s.openBlockType = "text"
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_start",
				Index: s.nextClaudeIndex,
				ContentBlock: &ClaudeContentBlock{
					Type: "text",
					Text: "",
				},
			})
		}
		if event.Delta != "" {
			// Convert Windows paths to Unix-style for Claude Code compatibility
			textDelta := convertWindowsPathsInToolResult(event.Delta)
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: s.nextClaudeIndex,
				Delta: &ClaudeStreamDelta{
					Type: "text_delta",
					Text: textDelta,
				},
			})
		}

	case "response.output_text.done":
		// Text output complete
		logrus.WithField("text_len", len(event.Text)).Debug("Codex CC: Text output done")

	case "response.content_part.done":
		// Content part complete - only close if a text block is open
		if s.openBlockType == "text" {
			closeOpenBlock()
		}

	case "response.function_call_arguments.delta":
		if s.skipActiveToolItem {
			// Ignore argument deltas for a tool item skipped due to an unresolved
			// name; auto-opening here would emit a fabricated unknown_tool block.
			return events
		}
		// Per AI review: guard delta emission with block state to prevent orphan deltas.
		// Auto-open tool block if not present when receiving first delta.
		if event.Delta != "" && s.openBlockType != "tool" {
			closeOpenBlock()
			s.openBlockType = "tool"
			s.toolInputSent = false
			// Use current tool info if available, with fallback for out-of-order events.
			// Per AI review: add fallback for empty tool ID/name to handle edge cases
			// where arguments.delta arrives before output_item.added (unlikely but possible).
			toolUseID := s.currentToolID
			if toolUseID == "" {
				toolUseID = "call_" + uuid.New().String()[:8]
			}
			toolName := s.currentToolName
			if toolName == "" {
				toolName = "unknown_tool"
			}
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_start",
				Index: s.nextClaudeIndex,
				ContentBlock: &ClaudeContentBlock{
					Type:  "tool_use",
					ID:    toolUseID,
					Name:  toolName,
					Input: json.RawMessage("{}"),
				},
			})
		}
		if event.Delta != "" {
			s.currentToolArgs.WriteString(event.Delta)
			s.toolInputSent = true
			logrus.WithField("delta_len", len(event.Delta)).Debug("Codex CC: Function call arguments delta")
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: s.nextClaudeIndex,
				Delta: &ClaudeStreamDelta{
					Type:        "input_json_delta",
					PartialJSON: event.Delta,
				},
			})
		}

	case "response.function_call_arguments.done":
		// Function call arguments complete
		// Pass through verbatim; see codexClaudeToolInput for the contract.
		if event.Arguments != "" && s.openBlockType == "tool" && !s.toolInputSent {
			appendToolInputDelta(codexToolArgumentsRawMessage(event.Arguments))
		}
		logrus.WithField("args_len", s.currentToolArgs.Len()).Debug("Codex CC: Function call arguments done")

	case "response.custom_tool_call_input.delta":
		if s.skipActiveToolItem {
			// Ignore input deltas for a tool item skipped due to an unresolved name.
			return events
		}
		if event.Delta != "" {
			s.currentToolArgs.WriteString(event.Delta)
		}

	case "response.custom_tool_call_input.done":
		if s.skipActiveToolItem {
			// Ignore the completion of a tool item skipped due to an unresolved name.
			return events
		}
		if s.openBlockType == "tool" && !s.toolInputSent {
			input := event.Input
			if input == nil {
				input = s.currentToolArgs.String()
			}
			appendToolInputDelta(codexCustomToolClaudeInput(input))
		}

	case "response.output_item.done":
		if event.Item != nil {
			switch {
			case event.Item.Type == "message":
				// Message complete - no action needed, content_part.done handles it
				logrus.Debug("Codex CC: Message item done")
			case isCodexResponseToolCall(*event.Item):
				// Store completed tool use block. The effective call ID is guaranteed
				// non-empty by isCodexResponseToolCall, including item.ID fallbacks.
				// Only the reverse name map lookup can still resolve to an empty name.
				toolName := codexClaudeToolName(*event.Item, s.reverseToolNameMap)
				if toolName == "" {
					logrus.WithField("item_type", event.Item.Type).Debug("Codex CC: Skipping completed tool item with missing ID or name")
					return events
				}
				inputJSON := toolInputForCurrentItem(*event.Item)
				if s.openBlockType == "tool" && !s.toolInputSent {
					appendToolInputDelta(inputJSON)
				}

				s.toolUseBlocks = append(s.toolUseBlocks, ClaudeContentBlock{
					Type:  "tool_use",
					ID:    codexToolCallID(*event.Item),
					Name:  toolName,
					Input: inputJSON,
				})
				// Only close if a tool block is open
				if s.openBlockType == "tool" {
					closeOpenBlock()
				}
				s.toolInputSent = false
			}
		}

	case "response.failed", "error":
		if s.finalSent {
			return events
		}
		s.finalSent = true
		closeOpenBlock()

		errorType := "api_error"
		errorMessage := "Upstream response failed"
		if event.Response != nil && event.Response.Error != nil {
			errorType = apiErrorTypeToClaudeErrorType(event.Response.Error.Type)
			if event.Response.Error.Message != "" {
				errorMessage = strings.TrimSpace(utils.SanitizeErrorBody(event.Response.Error.Message))
			}
		} else if event.Error != nil {
			errorType = apiErrorTypeToClaudeErrorType(event.Error.Type)
			if event.Error.Message != "" {
				errorMessage = strings.TrimSpace(utils.SanitizeErrorBody(event.Error.Message))
			}
		}
		if errorMessage == "" {
			errorMessage = "Upstream response failed"
		}
		events = append(events, ClaudeStreamEvent{
			Type: "error",
			Error: &ClaudeError{
				Type:    errorType,
				Message: errorMessage,
			},
		})
		appendFinalMessageEvents("end_turn")

	case "response.incomplete":
		if s.finalSent {
			return events
		}
		s.finalSent = true
		closeOpenBlock()
		appendFinalMessageEvents("max_tokens")

	case "response.cancelled", "response.canceled":
		if s.finalSent {
			return events
		}
		s.finalSent = true
		closeOpenBlock()
		appendFinalMessageEvents("end_turn")

	case "response.completed", "response.done":
		// Prevent duplicate final events when response.completed is received multiple times
		// or when [DONE] is processed after response.completed
		if s.finalSent {
			return events
		}
		s.finalSent = true

		// Per AI review: ensure all open blocks are closed before final message events.
		// This prevents Claude clients from hanging or rejecting the stream.
		closeOpenBlock()

		// Determine stop reason
		stopReason := "end_turn"
		if len(s.toolUseBlocks) > 0 {
			stopReason = "tool_use"
		}

		appendFinalMessageEvents(stopReason)

	default:
		// Log unknown event types at debug level for forward compatibility.
		// New Responses event types may be introduced; logging helps debugging.
		// without breaking existing functionality.
		if event.Type != "" {
			logrus.WithField("event_type", event.Type).Debug("Codex CC: Ignoring unknown stream event type")
		}
	}

	return events
}

// handleCodexCCNormalResponse handles non-streaming Codex response conversion to Claude format.
func (ps *ProxyServer) handleCodexCCNormalResponse(c *gin.Context, resp *http.Response) {
	bodyBytes, err := readAllWithLimit(resp.Body, maxUpstreamResponseBodySize)
	if err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			maxMB := maxUpstreamResponseBodySize / (1024 * 1024)
			message := fmt.Sprintf("Upstream response exceeded maximum allowed size (%dMB) for OpenAI Responses CC conversion", maxMB)
			logrus.WithField("limit_mb", maxMB).
				Warn("Codex CC: Upstream response body too large for conversion")
			// Per AI review: use overloaded_error for size exceeded errors
			// as it indicates server capacity limits rather than client mistakes.
			claudeErr := ClaudeErrorResponse{
				Type: "error",
				Error: ClaudeError{
					Type:    "overloaded_error",
					Message: message,
				},
			}
			clearUpstreamEncodingHeaders(c)
			c.JSON(http.StatusBadGateway, claudeErr)
			return
		}

		logrus.WithError(err).Error("Failed to read Codex response body for CC conversion")
		clearUpstreamEncodingHeaders(c)
		c.Status(http.StatusInternalServerError)
		return
	}

	// Track original encoding and decompression state to ensure correct header handling.
	// When decompression fails, we must preserve Content-Encoding if returning original bytes.
	origEncoding := resp.Header.Get("Content-Encoding")
	decompressed := false

	// Decompress response body if encoded with size limit to prevent memory exhaustion.
	// The limit matches maxUpstreamResponseBodySize to ensure consistent memory bounds.
	bodyBytes, err = utils.DecompressResponseWithLimit(origEncoding, bodyBytes, maxUpstreamResponseBodySize)
	if err != nil {
		// Use errors.Is() for sentinel error comparison to handle wrapped errors properly
		if errors.Is(err, utils.ErrDecompressedTooLarge) {
			maxMB := maxUpstreamResponseBodySize / (1024 * 1024)
			message := fmt.Sprintf("Decompressed response exceeded maximum allowed size (%dMB) for OpenAI Responses CC conversion", maxMB)
			logrus.WithField("limit_mb", maxMB).
				Warn("Codex CC: Decompressed response body too large for conversion")
			// Per AI review: use overloaded_error for size exceeded errors
			// as it indicates server capacity limits rather than client mistakes.
			claudeErr := ClaudeErrorResponse{
				Type: "error",
				Error: ClaudeError{
					Type:    "overloaded_error",
					Message: message,
				},
			}
			clearUpstreamEncodingHeaders(c)
			c.JSON(http.StatusBadGateway, claudeErr)
			return
		}
		// Other decompression errors: continue with original data but preserve encoding header
		logrus.WithError(err).Warn("Codex CC: Decompression failed, using original data")
	} else if origEncoding != "" {
		// Decompression succeeded, mark as decompressed
		decompressed = true
	}

	// Parse Codex response
	var codexResp CodexResponse
	if err := json.Unmarshal(bodyBytes, &codexResp); err != nil {
		// Per AI review: sanitize BEFORE truncate to prevent leaking truncated secrets.
		// If truncation cuts a token, it may no longer match the sanitization regex.
		safePreview := utils.TruncateString(utils.SanitizeErrorBody(string(bodyBytes)), 512)
		logrus.WithError(err).WithField("body_preview", safePreview).
			Warn("Failed to parse Codex response for CC conversion")

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
			}).Warn("Codex CC: Converting upstream error to Claude format")

			c.Set("response_body", safePreview)
			returnClaudeError(c, resp.StatusCode, errorMessage)
			return
		}

		// For 2xx responses with JSON parse failure, return original body
		// (this shouldn't happen normally but provides a fallback)
		c.Set("response_body", safePreview)
		clearUpstreamEncodingHeaders(c)
		// Preserve original Content-Encoding if data was not decompressed
		if !decompressed && origEncoding != "" {
			c.Header("Content-Encoding", origEncoding)
		}
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
		return
	}

	// Check for Codex error
	if codexResp.Error != nil {
		setTokenUsageFromBody(c, bodyBytes)
		safeErrorMessage := strings.TrimSpace(utils.SanitizeErrorBody(codexResp.Error.Message))
		logrus.WithFields(logrus.Fields{
			"error_type":    codexResp.Error.Type,
			"error_message": safeErrorMessage,
		}).Warn("Codex CC: Codex returned error in CC conversion")

		claudeErr := ClaudeErrorResponse{
			Type: "error",
			Error: ClaudeError{
				Type:    apiErrorTypeToClaudeErrorType(codexResp.Error.Type),
				Message: safeErrorMessage,
			},
		}
		if claudeErr.Error.Message == "" {
			claudeErr.Error.Message = "Upstream returned an error"
		}
		clearUpstreamEncodingHeaders(c)
		c.JSON(resp.StatusCode, claudeErr)
		return
	}
	setTokenUsageOrEstimateFromFullBodyIf(c, bodyBytes, resp.StatusCode < http.StatusBadRequest)

	// Get tool name reverse map from context for restoring original tool names
	reverseToolNameMap := getCodexToolNameReverseMap(c)

	// Convert to Claude format with tool name restoration
	claudeResp := convertCodexToClaudeResponse(&codexResp, reverseToolNameMap)

	// Debug: log output items
	for i, item := range codexResp.Output {
		logrus.WithFields(logrus.Fields{
			"index":    i,
			"type":     item.Type,
			"call_id":  item.CallID,
			"name":     item.Name,
			"args_len": len(item.Arguments),
		}).Debug("Codex CC: Output item in non-streaming response")
	}

	logrus.WithFields(logrus.Fields{
		"codex_id":    codexResp.ID,
		"claude_id":   claudeResp.ID,
		"stop_reason": claudeResp.StopReason,
		"content_len": len(claudeResp.Content),
	}).Debug("Codex CC: Converted Codex response to Claude format")

	// Marshal Claude response
	claudeBody, err := json.Marshal(claudeResp)
	if err != nil {
		logrus.WithError(err).Error("Failed to marshal Claude response")
		clearUpstreamEncodingHeaders(c)
		// Preserve original Content-Encoding if data was not decompressed
		if !decompressed && origEncoding != "" {
			c.Header("Content-Encoding", origEncoding)
		}
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
		return
	}

	c.Set("response_body", sanitizeAndTruncateBytesForLog(claudeBody, maxResponseCaptureBytes))
	clearUpstreamEncodingHeaders(c)
	c.Header("Content-Type", "application/json")
	c.Data(resp.StatusCode, "application/json", claudeBody)
}

// codexLineReadResult is one line (or read error) produced by the codexLineReader goroutine.
type codexLineReadResult struct {
	line string
	err  error
}

// codexLineReader reads lines from an upstream stream using a single long-lived
// reader goroutine per stream (Plan A), so per-read timeouts never leave orphan
// goroutines behind.
type codexLineReader struct {
	reader *bufio.Reader
	lines  chan codexLineReadResult

	// done + closeOnce implement Close: cancelling the reader goroutine when
	// the consumer abandons the stream (see Close for the contract).
	done      chan struct{}
	closeOnce sync.Once
}

// newCodexLineReader starts one long-lived reader goroutine for the stream.
func newCodexLineReader(reader *bufio.Reader) *codexLineReader {
	lr := &codexLineReader{
		reader: reader,
		lines:  make(chan codexLineReadResult, 1),
		done:   make(chan struct{}),
	}
	go lr.readLoop()
	return lr
}

// readLoop is the single long-lived reader goroutine per stream.
//
// Lifecycle contract: it reads lines from the upstream stream in a loop and
// pushes each one (or the terminating error) into lines (buffered, size 1).
// It exits exactly when ReadString returns an error — i.e. on EOF or when the
// underlying connection/body is closed — or when Close cancels it via done —
// and closes lines so consumers observe io.EOF. A readLine timeout does not
// kill or spawn goroutines: the timed-out caller simply leaves this goroutine
// parked on the reader/channel, so the stream holds exactly 1 goroutine
// regardless of how many timeouts occur. Once the consumer abandons the stream
// (e.g. after a read timeout) and the upstream keeps sending, a successful send
// would otherwise block forever on the full channel; Close (done) releases it.
func (lr *codexLineReader) readLoop() {
	defer close(lr.lines)
	for {
		line, err := lr.reader.ReadString('\n')
		if err != nil {
			// Terminal result (EOF / connection close): deliver it if the
			// consumer is still waiting, but never block. If a timed-out caller
			// abandoned the stream and left a buffered line unconsumed, the
			// channel is full — dropping this error is fine (nobody will read
			// it) and guarantees this goroutine exits as soon as the underlying
			// reader errors.
			select {
			case lr.lines <- codexLineReadResult{line: line, err: err}:
			default:
			}
			return
		}
		select {
		case lr.lines <- codexLineReadResult{line: line, err: err}:
		case <-lr.done:
			return
		}
	}
}

// Close cancels the single reader goroutine so a blocked successful send cannot
// leak forever when the consumer abandons the stream (e.g. after a read timeout)
// while the upstream keeps sending lines. It is idempotent: closing done makes
// readLoop exit and close lines, so any later readLine call observes io.EOF.
// It does not close the underlying reader: an in-progress blocking read is
// released only when the caller closes the stream (body/connection), as before.
func (lr *codexLineReader) Close() {
	lr.closeOnce.Do(func() { close(lr.done) })
}

// readLine waits up to timeout for the next line. It returns ErrSSETimeout on
// timeout, io.EOF once the stream has ended, or the line and nil error.
func (lr *codexLineReader) readLine(timeout time.Duration) (string, error) {
	if timeout <= 0 {
		result, ok := <-lr.lines
		if !ok {
			return "", io.EOF
		}
		return result.line, result.err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result, ok := <-lr.lines:
		if !ok {
			return "", io.EOF
		}
		return result.line, result.err
	case <-timer.C:
		return "", ErrSSETimeout
	}
}

// handleCodexCCStreamingResponse handles streaming Codex response conversion to Claude format.
func (ps *ProxyServer) handleCodexCCStreamingResponse(c *gin.Context, resp *http.Response) {
	// Log response headers for debugging
	logrus.WithFields(logrus.Fields{
		"content_type":      resp.Header.Get("Content-Type"),
		"content_encoding":  resp.Header.Get("Content-Encoding"),
		"transfer_encoding": resp.Header.Get("Transfer-Encoding"),
		"status_code":       resp.StatusCode,
	}).Debug("Codex CC: Streaming response headers")

	// Set streaming headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	clearUpstreamEncodingHeaders(c)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		logrus.Error("Codex CC: ResponseWriter does not support Flusher")
		c.JSON(http.StatusInternalServerError, ClaudeErrorResponse{
			Type: "error",
			Error: ClaudeError{
				Type:    "api_error",
				Message: "Streaming not supported",
			},
		})
		return
	}

	// Get tool name reverse map from context for restoring original tool names
	reverseToolNameMap := getCodexToolNameReverseMap(c)
	state := newCodexStreamState(reverseToolNameMap)

	// Handle gzip/deflate/br decompression for streaming response
	// Responses upstreams may return gzip-compressed streaming responses.
	reader := resp.Body
	contentEncoding := resp.Header.Get("Content-Encoding")
	var decompressErr error
	if contentEncoding != "" {
		var err error
		reader, err = utils.NewDecompressReader(contentEncoding, resp.Body)
		if err != nil {
			decompressErr = err
			logrus.WithError(err).WithField("content_encoding", contentEncoding).
				Warn("Codex CC: Failed to create decompression reader")
		} else {
			logrus.WithField("content_encoding", contentEncoding).
				Debug("Codex CC: Created decompression reader for streaming response")
			// Ensure decompression reader is closed
			defer func() {
				if closer, ok := reader.(io.Closer); ok && closer != resp.Body {
					closer.Close()
				}
			}()
		}
	}

	// Helper function to write Claude SSE event
	writeClaudeEvent := func(event ClaudeStreamEvent) error {
		eventBytes, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, string(eventBytes))
		if err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// Fail fast if decompression failed - emit error event and return early
	// Continuing with compressed body would break SSE parsing and hang the client
	if decompressErr != nil {
		_ = writeClaudeEvent(ClaudeStreamEvent{
			Type: "error",
			Error: &ClaudeError{
				Type:    "api_error",
				Message: "Failed to decompress upstream stream",
			},
		})
		return
	}

	bufReader := bufio.NewReader(reader)
	// Wrap with a single long-lived reader goroutine for timeout reads without orphans.
	lineReader := newCodexLineReader(bufReader)
	// Release the reader goroutine on every return path (including the ErrSSETimeout
	// early return and off failure returns), so a blocked successful send cannot
	// leak it when the client abandons the stream.
	defer lineReader.Close()

	// Timeout state for CC streaming to prevent hanging when upstream is in thinking phase
	// Timeout values are derived from group/system config with preset upper bounds.
	firstByteReceived := false
	effectiveFirstByteTimeout, effectiveSubsequentTimeout := getEffectiveSSETimeouts(c)
	getTimeout := func() time.Duration {
		if !firstByteReceived {
			return effectiveFirstByteTimeout
		}
		return effectiveSubsequentTimeout
	}

	var currentEventType string
	lineCount := 0
	streamCompleted := false
	var estimateCapture estimatedTokenCapture
	codexEstimateDeltas := make(map[string]struct{})
	finalizeEstimatedUsage := func() {
		if _, _, ok := getTokenUsage(c); ok {
			return
		}
		setEstimatedOutputTokens(c, estimateCapture.Tokens())
	}
	// AI REVIEW NOTE: Suggestion to add explicit context cancellation check in the loop was considered.
	// This is unnecessary because Go's http.Response.Body is automatically closed when the request
	// context is cancelled. When the body is closed, ReadString returns an error (io.EOF or
	// context.Canceled), which is already handled below. Adding a select{} with ctx.Done() would
	// not help during blocking reads - it would only check between reads, which is already covered
	// by the error handling. The current implementation correctly handles all termination cases.

	// Per AI review: check if request body logging is enabled for debug log safety
	enableBodyLogging := false
	if gv, ok := c.Get("group"); ok {
		if g, ok := gv.(*models.Group); ok {
			enableBodyLogging = g.EffectiveConfig.EnableRequestBodyLogging
		}
	}

	for {
		// The single long-lived reader goroutine (codexLineReader) does the actual
		// read; readLine applies the timeout while waiting, so timeouts no longer
		// spawn orphan goroutines (see codexLineReader.readLoop lifecycle contract).
		// Cache timeout value before read to avoid calling getTimeout() twice.
		timeoutDuration := getTimeout()
		line, err := lineReader.readLine(timeoutDuration)
		if err == ErrSSETimeout {
			timeoutType := "subsequent"
			if !firstByteReceived {
				timeoutType = "first-byte"
			}
			logrus.WithFields(logrus.Fields{
				"timeout_type":    timeoutType,
				"timeout_seconds": timeoutDuration.Seconds(),
			}).Warn("Codex CC: SSE read timeout, upstream did not send data")
			// Send error event to client
			// Anthropic documents timeout_error as HTTP 504. Preserve that protocol
			// distinction so clients can classify this transient failure correctly.
			errorEvent := ClaudeStreamEvent{
				Type: "error",
				Error: &ClaudeError{
					Type:    "timeout_error",
					Message: fmt.Sprintf("Upstream did not respond within %.0f seconds", timeoutDuration.Seconds()),
				},
			}
			_ = writeClaudeEvent(errorEvent)
			// Send final events to properly terminate the stream
			finalEvents := state.processCodexStreamEvent(&CodexStreamEvent{Type: "response.completed"})
			for _, event := range finalEvents {
				_ = writeClaudeEvent(event)
			}
			return
		}
		if err == nil {
			firstByteReceived = true
		}

		lineCount++
		// Per AI review: only log line preview when EnableRequestBodyLogging is enabled
		// to avoid leaking sensitive SSE payloads (tool args, file paths, etc.)
		// Limit to first 5 lines for initial handshake debugging without overwhelming logs
		if lineCount <= 5 && enableBodyLogging {
			logrus.WithFields(logrus.Fields{
				"line_num":     lineCount,
				"line_len":     len(line),
				"line_preview": utils.TruncateString(utils.SanitizeErrorBody(line), 200),
			}).Debug("Codex CC: Read stream line")
		}
		if err != nil {
			if err == io.EOF {
				// Ensure final events are sent on EOF to prevent client hanging.
				// Per AI review: return immediately on write failure for consistent error handling.
				finalEvents := state.processCodexStreamEvent(&CodexStreamEvent{Type: "response.completed"})
				for _, event := range finalEvents {
					if writeErr := writeClaudeEvent(event); writeErr != nil {
						logrus.WithError(writeErr).Error("Codex CC: Failed to write final event on EOF")
						return
					}
				}
				streamCompleted = true
				break
			}
			logrus.WithError(err).Error("Codex CC: Error reading stream")
			// Send final events on error to ensure client receives termination.
			// Per AI review: return immediately on write failure for consistent error handling.
			finalEvents := state.processCodexStreamEvent(&CodexStreamEvent{Type: "response.completed"})
			for _, event := range finalEvents {
				if writeErr := writeClaudeEvent(event); writeErr != nil {
					logrus.WithError(writeErr).Error("Codex CC: Failed to write final event on error")
					return
				}
			}
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse SSE format - handle both "event:" and "data:" lines
		if strings.HasPrefix(line, "event: ") {
			currentEventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				// Send final events if not already sent.
				// Per AI review: return immediately on write failure for consistent error handling.
				finalEvents := state.processCodexStreamEvent(&CodexStreamEvent{Type: "response.completed"})
				for _, event := range finalEvents {
					if err := writeClaudeEvent(event); err != nil {
						logrus.WithError(err).Error("Codex CC: Failed to write final event")
						return
					}
				}
				streamCompleted = true
				break
			}

			var codexEvent CodexStreamEvent
			if err := json.Unmarshal([]byte(data), &codexEvent); err != nil {
				// Per AI review: sanitize BEFORE truncate to prevent leaking truncated secrets
				logrus.WithError(err).WithField("data_preview", utils.TruncateString(utils.SanitizeErrorBody(data), 512)).
					Debug("Codex CC: Failed to parse stream event")
				continue
			}
			setTokenUsageFromBody(c, []byte(data))

			// Use event type from "event:" line if available, otherwise from JSON
			if currentEventType != "" && codexEvent.Type == "" {
				codexEvent.Type = currentEventType
			}
			currentEventType = "" // Reset for next event
			captureCodexStreamOutputEstimate(&estimateCapture, &codexEvent, codexEstimateDeltas)

			// Debug log: show received event type
			logrus.WithFields(logrus.Fields{
				"event_type": codexEvent.Type,
				"item_id":    codexEvent.ItemID,
				"output_idx": codexEvent.OutputIdx,
				"has_item":   codexEvent.Item != nil,
				"has_delta":  codexEvent.Delta != "",
			}).Debug("Codex CC: Received stream event")

			// Process event and get Claude events
			claudeEvents := state.processCodexStreamEvent(&codexEvent)
			for _, event := range claudeEvents {
				if err := writeClaudeEvent(event); err != nil {
					logrus.WithError(err).Error("Codex CC: Failed to write stream event")
					return
				}
			}
			if codexEvent.Type == "response.completed" || codexEvent.Type == "response.done" ||
				codexEvent.Type == "response.failed" || codexEvent.Type == "response.incomplete" ||
				codexEvent.Type == "response.cancelled" || codexEvent.Type == "response.canceled" ||
				codexEvent.Type == "error" {
				streamCompleted = codexEvent.Type != "response.failed" && codexEvent.Type != "error"
				break
			}
		}
	}

	if streamCompleted {
		finalizeEstimatedUsage()
	}
	logrus.Debug("Codex CC: Streaming response completed")
}

func captureCodexStreamOutputEstimate(capture *estimatedTokenCapture, event *CodexStreamEvent, seenDelta map[string]struct{}) {
	if capture == nil || event == nil {
		return
	}
	switch event.Type {
	case "response.output_text.delta":
		captureCodexStreamEstimateDelta(capture, seenDelta, event, "output_text", event.Delta)
	case "response.reasoning_summary_text.delta":
		captureCodexStreamEstimateDelta(capture, seenDelta, event, "reasoning_summary_text", event.Delta)
	case "response.reasoning_text.delta":
		captureCodexStreamEstimateDelta(capture, seenDelta, event, "reasoning_text", event.Delta)
	case "response.function_call_arguments.delta":
		captureCodexStreamEstimateDelta(capture, seenDelta, event, "function_call_arguments", event.Delta)
	case "response.output_text.done":
		captureCodexStreamEstimateDoneFallback(capture, seenDelta, event, "output_text", codexStreamEventText(event))
	case "response.reasoning_summary_text.done":
		captureCodexStreamEstimateDoneFallback(capture, seenDelta, event, "reasoning_summary_text", codexStreamEventText(event))
	case "response.reasoning_text.done":
		captureCodexStreamEstimateDoneFallback(capture, seenDelta, event, "reasoning_text", codexStreamEventText(event))
	case "response.function_call_arguments.done":
		captureCodexStreamEstimateDoneFallback(capture, seenDelta, event, "function_call_arguments", codexStreamEventArguments(event))
	case "response.output_item.done":
		captureCodexOutputItemEstimate(capture, seenDelta, event)
	}
}

func captureCodexStreamEstimateDelta(capture *estimatedTokenCapture, seenDelta map[string]struct{}, event *CodexStreamEvent, kind, text string) {
	if text == "" {
		return
	}
	if seenDelta != nil {
		seenDelta[codexStreamEstimateKey(event, kind)] = struct{}{}
	}
	capture.addString(text)
}

func captureCodexStreamEstimateDoneFallback(capture *estimatedTokenCapture, seenDelta map[string]struct{}, event *CodexStreamEvent, kind, text string) {
	if text == "" {
		return
	}
	if seenDelta != nil {
		if _, ok := seenDelta[codexStreamEstimateKey(event, kind)]; ok {
			return
		}
	}
	capture.addString(text)
}

func codexStreamEstimateKey(event *CodexStreamEvent, kind string) string {
	if event == nil {
		return kind
	}
	return fmt.Sprintf("%s:%d:%d", kind, event.OutputIdx, event.ContentIdx)
}

func codexStreamEventText(event *CodexStreamEvent) string {
	if event == nil {
		return ""
	}
	if event.Text != "" {
		return event.Text
	}
	if event.Part != nil && event.Part.Text != "" {
		return event.Part.Text
	}
	if event.Item != nil {
		for _, content := range event.Item.Content {
			if content.Text != "" {
				return content.Text
			}
		}
		for _, summary := range event.Item.Summary {
			if summary.Text != "" {
				return summary.Text
			}
		}
	}
	return ""
}

func codexStreamEventArguments(event *CodexStreamEvent) string {
	if event == nil {
		return ""
	}
	if event.Arguments != "" {
		return event.Arguments
	}
	if event.Item != nil {
		return event.Item.Arguments
	}
	return ""
}

func captureCodexOutputItemEstimate(capture *estimatedTokenCapture, seenDelta map[string]struct{}, event *CodexStreamEvent) {
	if event == nil || event.Item == nil {
		return
	}
	switch event.Item.Type {
	case "function_call":
		captureCodexStreamEstimateDoneFallback(capture, seenDelta, event, "function_call_arguments", event.Item.Arguments)
	case "message":
		for i, content := range event.Item.Content {
			if content.Text == "" {
				continue
			}
			itemEvent := *event
			itemEvent.ContentIdx = i
			captureCodexStreamEstimateDoneFallback(capture, seenDelta, &itemEvent, "output_text", content.Text)
		}
	case "reasoning":
		for i, summary := range event.Item.Summary {
			if summary.Text == "" {
				continue
			}
			itemEvent := *event
			itemEvent.ContentIdx = i
			captureCodexStreamEstimateDoneFallback(capture, seenDelta, &itemEvent, "reasoning_summary_text", summary.Text)
		}
	}
}
