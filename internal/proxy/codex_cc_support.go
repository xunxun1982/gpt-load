// Package proxy provides Codex CC (Claude Code) support functionality.
// Codex CC support enables Claude Code clients to connect via /claude endpoint
// and have requests converted from Claude format to Codex/Responses API format.
// This is similar to OpenAI CC support but targets the Responses API instead of Chat Completions.
package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// isCodexCCMode checks if the current request is in Codex CC mode.
// This is used to determine which response conversion to apply.
func isCodexCCMode(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyCodexCC); ok {
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
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
}

// CodexRequest represents a Codex/Responses API request.
type CodexRequest struct {
	Model             string            `json:"model"`
	Input             json.RawMessage   `json:"input"`
	Instructions      string            `json:"instructions,omitempty"`
	MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Stream            bool              `json:"stream"`
	Tools             []CodexTool       `json:"tools,omitempty"`
	ToolChoice        interface{}       `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Reasoning         *CodexReasoning   `json:"reasoning,omitempty"`
	Store             *bool             `json:"store,omitempty"`
	Include           []string          `json:"include,omitempty"`
}

// CodexReasoning represents reasoning configuration for Codex API.
// This enables thinking/reasoning capabilities in Codex models.
// NOTE: Effort values vary by model - we use "low", "medium", "high" for compatibility.
// GPT-5.2+ supports additional values like "minimal", "none", "xhigh".
type CodexReasoning struct {
	Effort  string `json:"effort,omitempty"`  // "low", "medium", "high" (compatible with all reasoning models)
	Summary string `json:"summary,omitempty"` // "auto", "none", "detailed"
}

// CodexOutputItem represents an output item in Codex/Responses API format.
type CodexOutputItem struct {
	Type      string              `json:"type"`
	ID        string              `json:"id,omitempty"`
	Status    string              `json:"status,omitempty"`
	Role      string              `json:"role,omitempty"`
	Content   []CodexContentBlock `json:"content,omitempty"`
	// Summary is used for reasoning output items to contain the thinking summary.
	// Each summary item has type "summary_text" and text field.
	Summary   []CodexSummaryItem  `json:"summary,omitempty"`
	CallID    string              `json:"call_id,omitempty"`
	Name      string              `json:"name,omitempty"`
	Arguments string              `json:"arguments,omitempty"`
}

// CodexSummaryItem represents a summary item in reasoning output.
type CodexSummaryItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CodexUsage represents usage information in Codex/Responses API format.
type CodexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
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

// codexToolNameLimit is the maximum length for tool names in Codex API.
// Names exceeding this limit will be shortened to ensure compatibility.
const codexToolNameLimit = 64

// buildToolNameShortMap builds a map of original names to shortened names,
// ensuring uniqueness within the request. This is necessary because multiple
// tools may have the same shortened name after truncation.
// Duplicate original names are skipped to prevent map overwrite issues.
func buildToolNameShortMap(names []string) map[string]string {
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
	// names like "_1" which may be rejected by Codex API's tool name charset rules.
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
	// Codex API typically uses provider defaults for output length.
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

	// Build input array using Codex format
	var inputItems []interface{}

	// Convert Claude system prompt to user message in input array
	// Note: Codex API doesn't support "developer" role in input, only in instructions
	// We prepend system content as a user message with clear delimiter
	//
	// AI REVIEW NOTE: Suggestion to merge system prompt into instructions was considered.
	// Current design keeps them separate because:
	// 1. instructions field contains Codex-specific behavior instructions (codexDefaultInstructions)
	// 2. Claude's system prompt is application-specific context from the client
	// 3. Merging could cause instruction conflicts and unpredictable behavior
	// 4. Using delimiters makes the system context clearly distinguishable
	if len(claudeReq.System) > 0 {
		systemContent := extractSystemContent(claudeReq.System)
		if systemContent != "" {
			// Add system prompt as first user message with delimiter
			inputItems = append(inputItems, map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": "[System Instructions]\n" + systemContent + "\n[End System Instructions]"},
				},
			})
			logrus.WithField("system_len", len(systemContent)).Debug("Codex CC: Added system as user message")
		}
	}

	// Handle prompt-only requests
	if len(claudeReq.Messages) == 0 && strings.TrimSpace(claudeReq.Prompt) != "" {
		inputItems = append(inputItems, map[string]interface{}{
			"type": "message",
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "input_text", "text": strings.TrimSpace(claudeReq.Prompt)},
			},
		})
	}

	// Convert messages with tool name mapping
	for _, msg := range claudeReq.Messages {
		converted, err := convertClaudeMessageToCodexFormatWithToolMap(msg, toolNameShortMap)
		if err != nil {
			return nil, fmt.Errorf("failed to convert Claude message: %w", err)
		}
		inputItems = append(inputItems, converted...)
	}

	// Inject thinking hints when extended thinking is enabled
	if claudeReq.Thinking != nil && strings.EqualFold(claudeReq.Thinking.Type, "enabled") {
		injectThinkingHint(inputItems, claudeReq.Thinking.BudgetTokens)
	}

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
				Strict:      false, // Codex API requires strict=false for flexibility
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

	// Apply parallel_tool_calls config for Codex channel.
	// Only set when tools are present (some upstreams reject the parameter without tools).
	// Default behavior: if not configured, enable parallel tool calls (true) for Codex.
	// Users can disable via group config: {"parallel_tool_calls": false}
	if len(codexReq.Tools) > 0 {
		parallelConfig := getParallelToolCallsConfig(group)
		if parallelConfig != nil {
			codexReq.ParallelToolCalls = parallelConfig
		} else {
			// Default to true for Codex channel (original behavior)
			parallelCalls := true
			codexReq.ParallelToolCalls = &parallelCalls
		}
	}

	// Configure reasoning for Codex API (Responses API) only when thinking is enabled.
	// AI REVIEW NOTE: Reasoning/Include fields are now gated behind thinking mode.
	// Non-reasoning models (e.g., gpt-4o) will reject these parameters with 400 errors.
	// Only set when client explicitly requests thinking to avoid breaking non-reasoning models.
	// Codex uses "reasoning.effort" (nested object) vs OpenAI Chat's "reasoning_effort" (flat field).
	//
	// NOTE: Users can override reasoning via param_overrides, e.g., {"reasoning": {"effort": "xhigh", "summary": "auto"}}
	// When overriding, include "summary" field to ensure reasoning summaries are returned in streaming responses.
	if claudeReq.Thinking != nil && strings.EqualFold(claudeReq.Thinking.Type, "enabled") {
		// Derive effort from thinking budget, default to "medium" when budget is 0 or not specified
		reasoningEffort := "medium"
		if claudeReq.Thinking.BudgetTokens > 0 {
			reasoningEffort = thinkingBudgetToReasoningEffortOpenAI(claudeReq.Thinking.BudgetTokens)
		}
		logrus.WithFields(logrus.Fields{
			"budget_tokens":    claudeReq.Thinking.BudgetTokens,
			"reasoning_effort": reasoningEffort,
		}).Debug("Codex CC: Configured reasoning effort from thinking budget")

		codexReq.Reasoning = &CodexReasoning{
			Effort:  reasoningEffort,
			Summary: "auto", // Enable reasoning summary for streaming responses
		}
		// Disable response storage for privacy
		storeDisabled := false
		codexReq.Store = &storeDisabled
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
	if err := json.Unmarshal(raw, &schema); err != nil {
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

	// Remove $schema field if present (not needed for Codex API)
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

	// Separate different block types
	var textParts []string
	var toolCalls []interface{}
	var toolResults []interface{}

	for _, block := range blocks {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "thinking":
			// AI REVIEW NOTE: Suggestion to handle thinking blocks separately was considered.
			// This is intentionally merged into textParts because:
			// 1. This is Claude→Codex REQUEST conversion (not response conversion)
			// 2. Codex API does not support thinking blocks as input format
			// 3. Thinking content from Claude client's history provides important reasoning context
			// 4. Discarding thinking would lose valuable context for multi-turn conversations
			// 5. Merging preserves the assistant's reasoning chain for better continuity
			if block.Thinking != "" {
				textParts = append(textParts, block.Thinking)
			}
		case "tool_use":
			// Apply shortened name if needed
			toolName := block.Name
			if short, ok := toolNameShortMap[block.Name]; ok {
				toolName = short
			}
			// Clean up tool arguments for compatibility with upstream APIs
			argsStr := cleanToolCallArguments(block.Name, string(block.Input))
			toolCalls = append(toolCalls, map[string]interface{}{
				"type":      "function_call",
				"id":        "fc_" + block.ID,
				"call_id":   "call_" + block.ID,
				"name":      toolName,
				"arguments": argsStr,
			})
		case "tool_result":
			resultContent := extractToolResultContent(block)
			toolResults = append(toolResults, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_" + block.ToolUseID,
				"output":  resultContent,
			})
		}
	}

	// Build result based on role
	switch msg.Role {
	case "assistant":
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

// injectThinkingHint adds thinking hint to the last user message.
func injectThinkingHint(inputItems []interface{}, budgetTokens int) {
	for i := len(inputItems) - 1; i >= 0; i-- {
		item, ok := inputItems[i].(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := item["role"].(string)
		if role != "user" {
			continue
		}
		content, ok := item["content"].([]map[string]interface{})
		if !ok || len(content) == 0 {
			continue
		}
		// Find the last input_text block
		for j := len(content) - 1; j >= 0; j-- {
			if content[j]["type"] == "input_text" {
				text, _ := content[j]["text"].(string)
				hint := ThinkingHintInterleaved
				if budgetTokens > 0 {
					hint += fmt.Sprintf(ThinkingHintMaxLength, budgetTokens)
				}
				content[j]["text"] = text + "\n" + hint
				return
			}
		}
	}
	// Per AI review: log when hint cannot be injected to aid debugging
	logrus.Debug("Codex CC: Could not inject thinking hint - no suitable user message found")
}

// cleanToolCallArguments cleans up tool call arguments for compatibility with upstream APIs.
// For WebSearch tool, removes empty allowed_domains and blocked_domains arrays that cause
// "Cannot specify both allowed_domains and blocked_domains" errors on some providers.
func cleanToolCallArguments(toolName, argsStr string) string {
	if argsStr == "" {
		return argsStr
	}

	// Only process WebSearch-related tools
	toolNameLower := strings.ToLower(toolName)
	if !strings.Contains(toolNameLower, "websearch") && !strings.Contains(toolNameLower, "web_search") {
		return argsStr
	}

	// Parse arguments as JSON
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		return argsStr
	}

	modified := false

	// Remove empty allowed_domains array
	if domains, ok := args["allowed_domains"]; ok {
		if arr, isArr := domains.([]interface{}); isArr && len(arr) == 0 {
			delete(args, "allowed_domains")
			modified = true
		}
	}

	// Remove empty blocked_domains array
	if domains, ok := args["blocked_domains"]; ok {
		if arr, isArr := domains.([]interface{}); isArr && len(arr) == 0 {
			delete(args, "blocked_domains")
			modified = true
		}
	}

	if !modified {
		return argsStr
	}

	// Re-marshal the cleaned arguments
	cleanedBytes, err := json.Marshal(args)
	if err != nil {
		return argsStr
	}

	logrus.WithFields(logrus.Fields{
		"tool_name":    toolName,
		"original_len": len(argsStr),
		"cleaned_len":  len(cleanedBytes),
	}).Debug("Codex CC: Cleaned WebSearch tool arguments")

	return string(cleanedBytes)
}

// extractToolResultContent extracts content from a tool_result block.
func extractToolResultContent(block ClaudeContentBlock) string {
	var resultContent string
	if err := json.Unmarshal(block.Content, &resultContent); err == nil {
		return resultContent
	}
	var contentBlocks []ClaudeContentBlock
	if err := json.Unmarshal(block.Content, &contentBlocks); err == nil {
		var sb strings.Builder
		for _, cb := range contentBlocks {
			if cb.Type == "text" {
				sb.WriteString(cb.Text)
			}
		}
		return sb.String()
	}
	return string(block.Content)
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
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					if content.Text != "" {
						claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
							Type: "text",
							Text: content.Text,
						})
					}
				case "refusal":
					if content.Text != "" {
						claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
							Type: "text",
							Text: content.Text,
						})
					}
				}
			}
		case "function_call":
			if item.CallID != "" && item.Name != "" {
				// Restore original tool name first for validation and cleanup.
				// This ensures tool-specific logic uses the correct name.
				toolName := item.Name
				if reverseToolNameMap != nil {
					if orig, ok := reverseToolNameMap[item.Name]; ok {
						toolName = orig
					}
				}
				// Validate arguments before conversion using restored tool name
				if !isValidToolCallArguments(toolName, item.Arguments) {
					continue
				}
				inputJSON := json.RawMessage("{}")
				if item.Arguments != "" {
					// Clean up WebSearch tool arguments for upstream compatibility
					argsStr := cleanToolCallArguments(toolName, item.Arguments)
					// Apply Windows path escape fix for Bash commands
					argsStr = doubleEscapeWindowsPathsForBash(argsStr)
					inputJSON = json.RawMessage(argsStr)
				}
				// Extract tool use ID from call_id (remove "call_" prefix if present)
				toolUseID := item.CallID
				if strings.HasPrefix(toolUseID, "call_") {
					toolUseID = strings.TrimPrefix(toolUseID, "call_")
				}
				claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
					Type:  "tool_use",
					ID:    toolUseID,
					Name:  toolName,
					Input: inputJSON,
				})
			}
		case "reasoning":
			// Convert reasoning to thinking block.
			// Codex API returns reasoning in "summary" field with type "summary_text".
			// First try summary field (standard Codex format), then fall back to content.
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
				claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
					Type:     "thinking",
					Thinking: thinkingText.String(),
				})
			} else {
				logrus.WithFields(logrus.Fields{
					"summary_count":  len(item.Summary),
					"content_count":  len(item.Content),
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

// applyCodexCCRequestConversion converts Claude request to Codex format.
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
		if _, exists := c.Get("original_model"); !exists {
			c.Set("original_model", originalModel)
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

	// Convert to Codex format with custom instructions
	codexReq, err := convertClaudeToCodex(&claudeReq, customInstructions, group)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to convert Claude to Codex: %w", err)
	}

	// Marshal Codex request
	convertedBody, err := json.Marshal(codexReq)
	if err != nil {
		return bodyBytes, false, fmt.Errorf("failed to marshal Codex request: %w", err)
	}

	// Mark CC conversion as enabled (for Codex)
	c.Set(ctxKeyCCEnabled, true)
	c.Set(ctxKeyOriginalFormat, "claude")
	c.Set(ctxKeyCodexCC, true) // Mark as Codex CC mode for response handling

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
	logrus.WithFields(logFields).Debug("Codex CC: Converted Claude request to Codex format")

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
	Type         string           `json:"type"`
	ResponseID   string           `json:"response_id,omitempty"`
	ItemID       string           `json:"item_id,omitempty"`
	OutputIdx    int              `json:"output_index,omitempty"`
	ContentIdx   int              `json:"content_index,omitempty"`
	Item         *CodexOutputItem `json:"item,omitempty"`
	Part         *CodexContentBlock `json:"part,omitempty"`
	Delta        string           `json:"delta,omitempty"`
	Text         string           `json:"text,omitempty"`
	Response     *CodexResponse   `json:"response,omitempty"`
	SequenceNum  int              `json:"sequence_number,omitempty"`
}

// codexStreamState tracks state during Codex streaming response conversion.
// AI REVIEW NOTE: Added openBlockType field per AI review to track block start/stop pairing.
// This prevents orphaned stops and ensures proper SSE contract compliance.
type codexStreamState struct {
	messageID         string
	currentToolID     string
	currentToolName   string
	currentToolArgs   strings.Builder
	toolUseBlocks     []ClaudeContentBlock
	model             string
	// nextClaudeIndex tracks the next content_block index for Claude events.
	// This is independent of Codex's output_index/content_index to ensure
	// Claude clients receive sequential, non-conflicting indices.
	// Index is incremented only after content_block_stop events to maintain
	// correct ordering for Claude clients.
	nextClaudeIndex   int
	// openBlockType tracks the type of currently open block at nextClaudeIndex.
	// Values: "", "text", "thinking", "tool". Empty means no block is open.
	// Used to prevent orphaned stops and ensure proper block pairing.
	openBlockType     string
	// finalSent tracks whether the final message_delta/message_stop events have been sent.
	// This prevents duplicate final events when response.completed is received multiple times
	// or when [DONE] is processed after response.completed.
	finalSent         bool
	// reverseToolNameMap maps shortened tool names back to original names.
	// Used to restore original tool names in streaming responses.
	reverseToolNameMap map[string]string
	// inThinkingBlock tracks whether we are currently inside a thinking/reasoning block.
	// Used to properly handle reasoning summary events.
	inThinkingBlock   bool
}

// newCodexStreamState creates a new stream state for Codex CC conversion.
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
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: s.nextClaudeIndex,
				Delta: &ClaudeStreamDelta{
					Type:     "thinking_delta",
					Thinking: event.Delta,
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
			logrus.WithFields(logrus.Fields{
				"item_type":   event.Item.Type,
				"item_id":     event.Item.ID,
				"item_call_id": event.Item.CallID,
				"item_name":   event.Item.Name,
				"output_idx":  event.OutputIdx,
			}).Debug("Codex CC: Output item added")
			switch event.Item.Type {
			case "message":
				// Message item added, wait for content_part.added for actual content
				logrus.WithField("item_type", event.Item.Type).Debug("Codex CC: Message item added")
			case "function_call":
				// Close any open block before starting tool block
				closeOpenBlock()
				s.currentToolID = event.Item.CallID
				s.currentToolName = event.Item.Name
				// Restore original tool name if it was shortened
				if s.reverseToolNameMap != nil {
					if orig, ok := s.reverseToolNameMap[event.Item.Name]; ok {
						s.currentToolName = orig
					}
				}
				s.currentToolArgs.Reset()
				// Content block start for tool_use
				toolUseID := s.currentToolID
				if strings.HasPrefix(toolUseID, "call_") {
					toolUseID = strings.TrimPrefix(toolUseID, "call_")
				}
				logrus.WithFields(logrus.Fields{
					"tool_id":       toolUseID,
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
						ID:    toolUseID,
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
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: s.nextClaudeIndex,
				Delta: &ClaudeStreamDelta{
					Type: "text_delta",
					Text: event.Delta,
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
		// Per AI review: guard delta emission with block state to prevent orphan deltas.
		// Auto-open tool block if not present when receiving first delta.
		if event.Delta != "" && s.openBlockType != "tool" {
			closeOpenBlock()
			s.openBlockType = "tool"
			// Use current tool info if available, with fallback for out-of-order events.
			// Per AI review: add fallback for empty tool ID/name to handle edge cases
			// where arguments.delta arrives before output_item.added (unlikely but possible).
			toolUseID := s.currentToolID
			if toolUseID == "" {
				toolUseID = "call_" + uuid.New().String()[:8]
			}
			if strings.HasPrefix(toolUseID, "call_") {
				toolUseID = strings.TrimPrefix(toolUseID, "call_")
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
		logrus.WithField("args_len", s.currentToolArgs.Len()).Debug("Codex CC: Function call arguments done")

	case "response.output_item.done":
		if event.Item != nil {
			switch event.Item.Type {
			case "message":
				// Message complete - no action needed, content_part.done handles it
				logrus.Debug("Codex CC: Message item done")
			case "function_call":
				// Store completed tool use block
				toolUseID := event.Item.CallID
				if strings.HasPrefix(toolUseID, "call_") {
					toolUseID = strings.TrimPrefix(toolUseID, "call_")
				}
				argsStr := event.Item.Arguments
				if argsStr == "" {
					argsStr = s.currentToolArgs.String()
				}
				// Restore original tool name if it was shortened
				toolName := event.Item.Name
				if s.reverseToolNameMap != nil {
					if orig, ok := s.reverseToolNameMap[event.Item.Name]; ok {
						toolName = orig
					}
				}
				// Clean up WebSearch tool arguments for upstream compatibility
				argsStr = cleanToolCallArguments(toolName, argsStr)
				// Apply Windows path escape fix
				argsStr = doubleEscapeWindowsPathsForBash(argsStr)
				s.toolUseBlocks = append(s.toolUseBlocks, ClaudeContentBlock{
					Type:  "tool_use",
					ID:    toolUseID,
					Name:  toolName,
					Input: json.RawMessage(argsStr),
				})
				// Only close if a tool block is open
				if s.openBlockType == "tool" {
					closeOpenBlock()
				}
			}
		}

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

		// Send message_delta with stop_reason
		events = append(events, ClaudeStreamEvent{
			Type: "message_delta",
			Delta: &ClaudeStreamDelta{
				StopReason: stopReason,
			},
			Usage: &ClaudeUsage{
				OutputTokens: 0,
			},
		})

		// Send message_stop
		events = append(events, ClaudeStreamEvent{
			Type: "message_stop",
		})

	default:
		// Log unknown event types at debug level for forward compatibility.
		// New Codex API event types may be introduced; logging helps debugging
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
			message := fmt.Sprintf("Upstream response exceeded maximum allowed size (%dMB) for Codex CC conversion", maxMB)
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
			message := fmt.Sprintf("Decompressed response exceeded maximum allowed size (%dMB) for Codex CC conversion", maxMB)
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
		logrus.WithFields(logrus.Fields{
			"error_type":    codexResp.Error.Type,
			"error_message": codexResp.Error.Message,
		}).Warn("Codex CC: Codex returned error in CC conversion")

		// Map Codex error types to Claude error types for better client compatibility.
		// Per AI review: use more accurate error types instead of generic invalid_request_error.
		claudeErrorType := "api_error" // Default for unknown error types
		switch codexResp.Error.Type {
		case "invalid_request_error":
			claudeErrorType = "invalid_request_error"
		case "authentication_error":
			claudeErrorType = "authentication_error"
		case "rate_limit_error":
			claudeErrorType = "rate_limit_error"
		case "overloaded_error":
			claudeErrorType = "overloaded_error"
		case "server_error", "internal_error":
			claudeErrorType = "api_error"
		}

		claudeErr := ClaudeErrorResponse{
			Type: "error",
			Error: ClaudeError{
				Type:    claudeErrorType,
				Message: codexResp.Error.Message,
			},
		}
		clearUpstreamEncodingHeaders(c)
		c.JSON(resp.StatusCode, claudeErr)
		return
	}

	// Get tool name reverse map from context for restoring original tool names
	reverseToolNameMap := getCodexToolNameReverseMap(c)

	// Convert to Claude format with tool name restoration
	claudeResp := convertCodexToClaudeResponse(&codexResp, reverseToolNameMap)

	// Debug: log output items
	for i, item := range codexResp.Output {
		logrus.WithFields(logrus.Fields{
			"index":     i,
			"type":      item.Type,
			"call_id":   item.CallID,
			"name":      item.Name,
			"args_len":  len(item.Arguments),
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

	c.Set("response_body", string(claudeBody))
	clearUpstreamEncodingHeaders(c)
	c.Header("Content-Type", "application/json")
	c.Data(resp.StatusCode, "application/json", claudeBody)
}

// handleCodexCCStreamingResponse handles streaming Codex response conversion to Claude format.
func (ps *ProxyServer) handleCodexCCStreamingResponse(c *gin.Context, resp *http.Response) {
	// Log response headers for debugging
	logrus.WithFields(logrus.Fields{
		"content_type":     resp.Header.Get("Content-Type"),
		"content_encoding": resp.Header.Get("Content-Encoding"),
		"transfer_encoding": resp.Header.Get("Transfer-Encoding"),
		"status_code":      resp.StatusCode,
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
	reader := bufio.NewReader(resp.Body)

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

	var currentEventType string
	lineCount := 0
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
		line, err := reader.ReadString('\n')
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
				break
			}

			var codexEvent CodexStreamEvent
			if err := json.Unmarshal([]byte(data), &codexEvent); err != nil {
				// Per AI review: sanitize BEFORE truncate to prevent leaking truncated secrets
				logrus.WithError(err).WithField("data_preview", utils.TruncateString(utils.SanitizeErrorBody(data), 512)).
					Debug("Codex CC: Failed to parse stream event")
				continue
			}

			// Use event type from "event:" line if available, otherwise from JSON
			if currentEventType != "" && codexEvent.Type == "" {
				codexEvent.Type = currentEventType
			}
			currentEventType = "" // Reset for next event

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
		}
	}

	logrus.Debug("Codex CC: Streaming response completed")
}
