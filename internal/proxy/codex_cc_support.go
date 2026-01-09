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
	CallID    string              `json:"call_id,omitempty"`
	Name      string              `json:"name,omitempty"`
	Arguments string              `json:"arguments,omitempty"`
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

	// Helper to make name unique by appending suffix
	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; i < 1000; i++ {
			suffix := "_" + fmt.Sprintf("%d", i)
			allowed := codexToolNameLimit - len(suffix)
			if allowed < 0 {
				allowed = 0
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
		// Fallback: should never reach here
		return cand
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
func convertClaudeToCodex(claudeReq *ClaudeRequest, customInstructions string) (*CodexRequest, error) {
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
		if err := json.Unmarshal(claudeReq.ToolChoice, &toolChoice); err == nil {
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

	// Enable parallel tool calls only when tools are present.
	// Some OpenAI-compatible upstreams reject parallel_tool_calls when no tools are defined.
	if len(codexReq.Tools) > 0 {
		parallelCalls := true
		codexReq.ParallelToolCalls = &parallelCalls
	}

	// Configure reasoning for Codex API when thinking is enabled.
	// This converts Claude's thinking.budget_tokens to Codex's reasoning.effort level.
	// Reference: CLIProxyAPI codex_claude_request.go reasoning configuration
	if claudeReq.Thinking != nil && strings.EqualFold(claudeReq.Thinking.Type, "enabled") {
		reasoningEffort := thinkingBudgetToReasoningEffortOpenAI(claudeReq.Thinking.BudgetTokens)
		codexReq.Reasoning = &CodexReasoning{
			Effort:  reasoningEffort,
			Summary: "auto", // Enable reasoning summary for streaming responses
		}
		// Disable response storage for privacy
		storeDisabled := false
		codexReq.Store = &storeDisabled
		// Include encrypted reasoning content for full thinking support
		codexReq.Include = []string{"reasoning.encrypted_content"}
		logrus.WithFields(logrus.Fields{
			"budget_tokens":    claudeReq.Thinking.BudgetTokens,
			"reasoning_effort": reasoningEffort,
		}).Debug("Codex CC: Configured reasoning for thinking mode")
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
			// Convert reasoning to thinking block
			for _, content := range item.Content {
				if content.Type == "output_text" && content.Text != "" {
					claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
						Type:     "thinking",
						Thinking: content.Text,
					})
				}
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

	// Auto-select thinking model when thinking mode is enabled
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
			c.Set("thinking_model", thinkingModel)
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

	// Build tool name short map and store reverse map in context for response conversion
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
	codexReq, err := convertClaudeToCodex(&claudeReq, customInstructions)
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
		inputPreview := string(codexReq.Input)
		if len(inputPreview) > 500 {
			inputPreview = inputPreview[:500] + "..."
		}
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
type codexStreamState struct {
	messageID         string
	currentText       strings.Builder
	currentToolID     string
	currentToolName   string
	currentToolArgs   strings.Builder
	toolUseBlocks     []ClaudeContentBlock
	hasContent        bool
	model             string
	// nextClaudeIndex tracks the next content_block index for Claude events.
	// This is independent of Codex's output_index/content_index to ensure
	// Claude clients receive sequential, non-conflicting indices.
	nextClaudeIndex   int
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
		// Start a thinking content block
		s.inThinkingBlock = true
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
		if event.Delta != "" && s.inThinkingBlock {
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_delta",
				Index: s.nextClaudeIndex,
				Delta: &ClaudeStreamDelta{
					Type:     "thinking_delta",
					Thinking: event.Delta,
				},
			})
		}

	case "response.reasoning_summary_part.done":
		// End thinking content block
		if s.inThinkingBlock {
			events = append(events, ClaudeStreamEvent{
				Type:  "content_block_stop",
				Index: s.nextClaudeIndex,
			})
			s.nextClaudeIndex++
			s.inThinkingBlock = false
		}

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
		// Content part added - start a new content block
		if event.Part != nil && event.Part.Type == "output_text" {
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
		if event.Delta != "" {
			s.currentText.WriteString(event.Delta)
			s.hasContent = true
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
		// Content part complete - send content_block_stop and increment index
		events = append(events, ClaudeStreamEvent{
			Type:  "content_block_stop",
			Index: s.nextClaudeIndex,
		})
		s.nextClaudeIndex++

	case "response.function_call_arguments.delta":
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
				events = append(events, ClaudeStreamEvent{
					Type:  "content_block_stop",
					Index: s.nextClaudeIndex,
				})
				s.nextClaudeIndex++
			}
		}

	case "response.completed", "response.done":
		// Prevent duplicate final events when response.completed is received multiple times
		// or when [DONE] is processed after response.completed
		if s.finalSent {
			return events
		}
		s.finalSent = true

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
		// Other decompression errors: continue with original data but preserve encoding header
		logrus.WithError(err).Warn("Codex CC: Decompression failed, using original data")
	} else if origEncoding != "" {
		// Decompression succeeded, mark as decompressed
		decompressed = true
	}

	// Parse Codex response
	var codexResp CodexResponse
	if err := json.Unmarshal(bodyBytes, &codexResp); err != nil {
		logrus.WithError(err).WithField("body_preview", utils.TruncateString(string(bodyBytes), 512)).
			Warn("Failed to parse Codex response for CC conversion, returning body without conversion")
		c.Set("response_body", string(bodyBytes))
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

		claudeErr := ClaudeErrorResponse{
			Type: "error",
			Error: ClaudeError{
				Type:    "invalid_request_error",
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
	// AI REVIEW NOTE: Suggestion to add explicit context cancellation check in the loop was considered.
	// This is unnecessary because Go's http.Response.Body is automatically closed when the request
	// context is cancelled. When the body is closed, ReadString returns an error (io.EOF or
	// context.Canceled), which is already handled below. Adding a select{} with ctx.Done() would
	// not help during blocking reads - it would only check between reads, which is already covered
	// by the error handling. The current implementation correctly handles all termination cases.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// Ensure final events are sent on EOF to prevent client hanging
				finalEvents := state.processCodexStreamEvent(&CodexStreamEvent{Type: "response.completed"})
				for _, event := range finalEvents {
					if writeErr := writeClaudeEvent(event); writeErr != nil {
						logrus.WithError(writeErr).Error("Codex CC: Failed to write final event on EOF")
					}
				}
				break
			}
			logrus.WithError(err).Error("Codex CC: Error reading stream")
			// Send final events on error to ensure client receives termination
			finalEvents := state.processCodexStreamEvent(&CodexStreamEvent{Type: "response.completed"})
			for _, event := range finalEvents {
				_ = writeClaudeEvent(event) // Best effort, ignore write errors during error handling
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
				// Send final events if not already sent
				finalEvents := state.processCodexStreamEvent(&CodexStreamEvent{Type: "response.completed"})
				for _, event := range finalEvents {
					if err := writeClaudeEvent(event); err != nil {
						logrus.WithError(err).Error("Codex CC: Failed to write final event")
					}
				}
				break
			}

			var codexEvent CodexStreamEvent
			if err := json.Unmarshal([]byte(data), &codexEvent); err != nil {
				// Truncate data to prevent sensitive information leakage in logs
				logrus.WithError(err).WithField("data_preview", utils.TruncateString(data, 512)).Debug("Codex CC: Failed to parse stream event")
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
