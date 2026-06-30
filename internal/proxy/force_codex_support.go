package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const (
	ctxKeyCodexEnabled        = "codex_enabled"
	ctxKeyCodexUpstreamFormat = "codex_upstream_format"
	ctxKeyCodexToolContext    = "codex_tool_context"

	codexUpstreamOpenAIChat = "openai_chat"
	codexUpstreamClaude     = "claude"
	codexUpstreamResponses  = "openai_response"

	codexToolSearchProxyName = "tool_search"
)

// isCodexPath detects the explicit /codex force endpoint without confusing it
// with a group that is literally named "codex".
func isCodexPath(path, groupName string) bool {
	if groupName != "" {
		prefix := "/proxy/" + groupName + "/"
		if strings.HasPrefix(path, prefix) {
			suffix := strings.TrimPrefix(path, prefix)
			return strings.HasPrefix(suffix, "codex/v1/") || suffix == "codex/v1"
		}
	}
	return strings.Contains(path, "/codex/v1/") || strings.HasSuffix(path, "/codex/v1")
}

// rewriteCodexPathToOpenAIGeneric removes only the /codex segment that precedes
// /v1 so group names remain untouched.
func rewriteCodexPathToOpenAIGeneric(path string) string {
	return strings.Replace(path, "/codex/v1", "/v1", 1)
}

func isCodexSupportEnabled(group *models.Group) bool {
	if group == nil || (group.ChannelType != "openai" && group.ChannelType != "anthropic") {
		return false
	}
	return getGroupConfigBool(group, "codex_support")
}

func isCodexEndpointSupported(group *models.Group) bool {
	if group == nil {
		return false
	}
	if group.ChannelType == "openai-response" {
		return true
	}
	return isCodexSupportEnabled(group)
}

func isCodexEnabled(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyCodexEnabled); ok {
		if enabled, ok := v.(bool); ok && enabled {
			return true
		}
	}
	return false
}

func setCodexUpstreamFormat(c *gin.Context, format string) {
	c.Set(ctxKeyCodexUpstreamFormat, format)
}

func getCodexUpstreamFormat(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyCodexUpstreamFormat); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

type codexToolKind string

const (
	codexToolKindFunction   codexToolKind = "function"
	codexToolKindCustom     codexToolKind = "custom"
	codexToolKindToolSearch codexToolKind = "tool_search"
)

type codexToolSpec struct {
	Kind                 codexToolKind
	Name                 string
	Namespace            string
	AllowsEmptyArguments bool
}

type codexToolContext struct {
	byChatName map[string]codexToolSpec
}

func newCodexToolContext(tools []CodexTool) *codexToolContext {
	ctx := &codexToolContext{byChatName: make(map[string]codexToolSpec)}
	for _, tool := range tools {
		ctx.addTool(tool, "")
	}
	return ctx
}

func codexToolContextFromGin(c *gin.Context) *codexToolContext {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(ctxKeyCodexToolContext); ok {
		if toolCtx, ok := v.(*codexToolContext); ok {
			return toolCtx
		}
	}
	return nil
}

func (ctx *codexToolContext) addTool(tool CodexTool, namespace string) {
	if ctx == nil {
		return
	}
	switch tool.Type {
	case "", "function":
		chatName := codexChatToolName(tool.Name, namespace)
		if chatName != "" {
			ctx.byChatName[chatName] = codexToolSpec{
				Kind:                 codexToolKindFunction,
				Name:                 tool.Name,
				Namespace:            namespace,
				AllowsEmptyArguments: codexToolAllowsEmptyArguments(tool),
			}
		}
	case "custom":
		if tool.Name != "" {
			ctx.byChatName[tool.Name] = codexToolSpec{Kind: codexToolKindCustom, Name: tool.Name}
		}
	case "tool_search":
		ctx.byChatName[codexToolSearchProxyName] = codexToolSpec{Kind: codexToolKindToolSearch, Name: codexToolSearchProxyName}
	case "namespace":
		for _, child := range codexNamespaceChildren(tool) {
			ctx.addTool(child, tool.Name)
		}
	}
}

func codexToolAllowsEmptyArguments(tool CodexTool) bool {
	if len(tool.Parameters) == 0 || string(tool.Parameters) == "null" {
		return true
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		return false
	}
	return len(schema.Required) == 0
}

func isValidCodexToolCallArguments(toolName, arguments string, toolCtx *codexToolContext) bool {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "{}" {
		if spec, ok := toolCtx.lookup(toolName); ok && spec.AllowsEmptyArguments {
			return true
		}
	}
	return isValidToolCallArguments(toolName, arguments)
}

func (ctx *codexToolContext) chatNameFor(name, namespace string) string {
	if namespace != "" {
		return codexChatToolName(name, namespace)
	}
	return name
}

func (ctx *codexToolContext) lookup(chatName string) (codexToolSpec, bool) {
	if ctx == nil || chatName == "" {
		return codexToolSpec{}, false
	}
	spec, ok := ctx.byChatName[chatName]
	return spec, ok
}

func codexChatToolName(name, namespace string) string {
	if name == "" {
		return ""
	}
	if namespace == "" {
		return name
	}
	return namespace + "__" + name
}

func codexNamespaceChildren(tool CodexTool) []CodexTool {
	if len(tool.Tools) > 0 {
		return tool.Tools
	}
	return tool.Children
}

func convertCodexRequestToOpenAIChat(codexReq *CodexRequest) (*OpenAIRequest, error) {
	if codexReq == nil {
		return nil, fmt.Errorf("codex request is nil")
	}
	toolCtx := newCodexToolContext(codexReq.Tools)
	req := &OpenAIRequest{
		Model:             codexReq.Model,
		Stream:            codexReq.Stream,
		Temperature:       codexReq.Temperature,
		TopP:              codexReq.TopP,
		MaxTokens:         codexReq.MaxOutputTokens,
		ParallelToolCalls: codexReq.ParallelToolCalls,
	}

	if strings.TrimSpace(codexReq.Instructions) != "" {
		req.Messages = append(req.Messages, OpenAIMessage{
			Role:    "system",
			Content: marshalStringAsJSONRaw("codex_instructions", codexReq.Instructions),
		})
	}

	messages, err := convertCodexInputToOpenAIMessages(codexReq.Input, toolCtx)
	if err != nil {
		return nil, err
	}
	req.Messages = append(req.Messages, messages...)

	if len(codexReq.Tools) > 0 {
		req.Tools = make([]OpenAITool, 0, len(codexReq.Tools))
		for _, tool := range codexReq.Tools {
			appendCodexToolToOpenAIChat(&req.Tools, tool, "")
		}
	}
	req.ToolChoice = convertResponsesToolChoiceToOpenAIChat(codexReq.ToolChoice, toolCtx)
	return req, nil
}

func convertCodexRequestToClaude(codexReq *CodexRequest) (*ClaudeRequest, error) {
	if codexReq == nil {
		return nil, fmt.Errorf("codex request is nil")
	}
	toolCtx := newCodexToolContext(codexReq.Tools)
	req := &ClaudeRequest{
		Model:       codexReq.Model,
		Stream:      codexReq.Stream,
		Temperature: codexReq.Temperature,
		TopP:        codexReq.TopP,
	}
	if codexReq.MaxOutputTokens != nil {
		req.MaxTokens = *codexReq.MaxOutputTokens
	}
	if strings.TrimSpace(codexReq.Instructions) != "" {
		req.System = marshalStringAsJSONRaw("codex_instructions", codexReq.Instructions)
	}

	messages, err := convertCodexInputToClaudeMessages(codexReq.Input, toolCtx)
	if err != nil {
		return nil, err
	}
	req.Messages = messages

	if len(codexReq.Tools) > 0 {
		req.Tools = make([]ClaudeTool, 0, len(codexReq.Tools))
		for _, tool := range codexReq.Tools {
			appendCodexToolToClaude(&req.Tools, tool, "")
		}
	}
	req.ToolChoice = convertResponsesToolChoiceToClaude(codexReq.ToolChoice, toolCtx)
	return req, nil
}

func convertOpenAIChatToCodexResponse(openaiResp *OpenAIResponse, triggerSignal string, toolCtxOpt ...*codexToolContext) *CodexResponse {
	if openaiResp == nil {
		return &CodexResponse{
			ID:        "resp_" + time.Now().Format("20060102150405"),
			Object:    "response",
			CreatedAt: time.Now().Unix(),
			Status:    "failed",
			Error:     &CodexError{Type: "server_error", Message: "empty upstream response"},
		}
	}
	resp := &CodexResponse{
		ID:        openaiResp.ID,
		Object:    "response",
		CreatedAt: openaiResp.Created,
		Status:    "completed",
		Model:     openaiResp.Model,
		Output:    make([]CodexOutputItem, 0),
	}
	if resp.ID == "" {
		resp.ID = "resp_" + time.Now().Format("20060102150405")
	}
	if resp.CreatedAt == 0 {
		resp.CreatedAt = time.Now().Unix()
	}
	if openaiResp.Error != nil {
		resp.Status = "failed"
		resp.Error = &CodexError{
			Type:    openaiResp.Error.Type,
			Message: openaiResp.Error.Message,
		}
		return resp
	}

	if len(openaiResp.Choices) > 0 {
		choice := openaiResp.Choices[0]
		msg := choice.Message
		if msg == nil {
			msg = choice.Delta
		}
		if msg != nil {
			var parsedCalls []functionCall
			if len(msg.ToolCalls) == 0 && msg.Content != nil && *msg.Content != "" {
				parsedCalls = parseFunctionCallsXML(*msg.Content, triggerSignal)
				if len(parsedCalls) == 0 && strings.Contains(*msg.Content, "<function_calls>") {
					parsedCalls = parseFunctionCallsXML(*msg.Content, "")
				}
			}
			if len(msg.ToolCalls) == 0 && len(parsedCalls) == 0 && msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
				reasoning := *msg.ReasoningContent
				if triggerSignal != "" && strings.Contains(reasoning, triggerSignal) ||
					strings.Contains(reasoning, "<invoke") ||
					strings.Contains(reasoning, "<function_calls>") {
					parsedCalls = parseFunctionCallsXML(reasoning, triggerSignal)
					if len(parsedCalls) == 0 {
						parsedCalls = parseFunctionCallsXML(reasoning, "")
					}
				}
			}

			if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
				reasoning := strings.TrimSpace(removeFunctionCallsBlocks(*msg.ReasoningContent, cleanupModeFull))
				if reasoning != "" {
					resp.Output = append(resp.Output, CodexOutputItem{
						Type:   "reasoning",
						Status: "completed",
						Summary: []CodexSummaryItem{{
							Type: "summary_text",
							Text: reasoning,
						}},
					})
				}
			}
			if msg.Content != nil && *msg.Content != "" {
				text := strings.TrimSpace(removeFunctionCallsBlocks(*msg.Content, cleanupModeFull))
				if text != "" {
					resp.Output = append(resp.Output, CodexOutputItem{
						Type:   "message",
						Role:   "assistant",
						Status: "completed",
						Content: []CodexContentBlock{{
							Type: "output_text",
							Text: text,
						}},
					})
				}
			}
			var toolCtx *codexToolContext
			if len(toolCtxOpt) > 0 {
				toolCtx = toolCtxOpt[0]
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" || !isValidCodexToolCallArguments(tc.Function.Name, tc.Function.Arguments, toolCtx) {
					continue
				}
				resp.Output = append(resp.Output, codexOutputItemFromChatToolCall(tc.ID, tc.Function.Name, tc.Function.Arguments, toolCtx))
			}
			if len(msg.ToolCalls) == 0 {
				appendParsedFunctionCallsToCodex(resp, parsedCalls)
			}
		}
	}
	if openaiResp.Usage != nil {
		resp.Usage = &CodexUsage{
			InputTokens:  openaiResp.Usage.PromptTokens,
			OutputTokens: openaiResp.Usage.CompletionTokens,
			TotalTokens:  openaiResp.Usage.TotalTokens,
		}
		if details := codexInputTokenDetailsFromOpenAI(openaiResp.Usage.PromptTokensDetails); details != nil {
			resp.Usage.InputTokensDetails = details
			resp.Usage.CacheReadTokens = details.CachedTokens
		}
		if details := codexOutputTokenDetailsFromOpenAI(openaiResp.Usage.CompletionTokensDetails); details != nil {
			resp.Usage.OutputTokensDetails = details
			resp.Usage.ThinkingTokens = details.ReasoningTokens
		}
		if resp.Usage.TotalTokens == 0 {
			resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		}
	}
	return resp
}

func convertClaudeToCodexResponse(claudeResp *ClaudeResponse, toolCtxOpt ...*codexToolContext) *CodexResponse {
	if claudeResp == nil {
		return &CodexResponse{
			ID:        "resp_" + time.Now().Format("20060102150405"),
			Object:    "response",
			CreatedAt: time.Now().Unix(),
			Status:    "failed",
			Error:     &CodexError{Type: "server_error", Message: "empty upstream response"},
		}
	}
	resp := &CodexResponse{
		ID:        claudeResp.ID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Status:    "completed",
		Model:     claudeResp.Model,
		Output:    make([]CodexOutputItem, 0, len(claudeResp.Content)),
	}
	if resp.ID == "" {
		resp.ID = "resp_" + time.Now().Format("20060102150405")
	}
	var toolCtx *codexToolContext
	if len(toolCtxOpt) > 0 {
		toolCtx = toolCtxOpt[0]
	}
	for _, block := range claudeResp.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				resp.Output = append(resp.Output, CodexOutputItem{
					Type:   "message",
					Role:   "assistant",
					Status: "completed",
					Content: []CodexContentBlock{{
						Type: "output_text",
						Text: block.Text,
					}},
				})
			}
		case "thinking":
			if block.Thinking != "" {
				resp.Output = append(resp.Output, CodexOutputItem{
					Type:   "reasoning",
					Status: "completed",
					Summary: []CodexSummaryItem{{
						Type: "summary_text",
						Text: block.Thinking,
					}},
				})
			}
		case "tool_use":
			if block.ID != "" && block.Name != "" {
				resp.Output = append(resp.Output, codexOutputItemFromChatToolCall("call_"+block.ID, block.Name, string(block.Input), toolCtx))
			}
		}
	}
	if claudeResp.Usage != nil {
		resp.Usage = &CodexUsage{
			InputTokens:      claudeResp.Usage.InputTokens,
			OutputTokens:     claudeResp.Usage.OutputTokens,
			TotalTokens:      codexTotalTokensFromClaudeUsage(claudeResp.Usage),
			CacheReadTokens:  claudeResp.Usage.CacheReadInputTokens,
			CacheWriteTokens: claudeResp.Usage.CacheCreationInputTokens,
			ThinkingTokens:   claudeResp.Usage.ThinkingTokens,
		}
		if claudeResp.Usage.CacheReadInputTokens > 0 {
			resp.Usage.InputTokensDetails = &TokenUsageDetails{CachedTokens: claudeResp.Usage.CacheReadInputTokens}
		}
		if claudeResp.Usage.ThinkingTokens > 0 {
			resp.Usage.OutputTokensDetails = &TokenUsageDetails{ReasoningTokens: claudeResp.Usage.ThinkingTokens}
		}
	}
	return resp
}

func codexTotalTokensFromClaudeUsage(usage *ClaudeUsage) int {
	if usage == nil {
		return 0
	}
	total := usage.InputTokens + usage.OutputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	if usage.ThinkingTokens > 0 {
		total += usage.ThinkingTokens
	}
	return total
}

func codexOutputItemFromChatToolCall(callID, chatName, arguments string, toolCtx *codexToolContext) CodexOutputItem {
	spec, ok := toolCtx.lookup(chatName)
	if ok {
		switch spec.Kind {
		case codexToolKindCustom:
			return CodexOutputItem{
				Type:   "custom_tool_call",
				ID:     "ctc_" + strings.TrimPrefix(callID, "call_"),
				Status: "completed",
				CallID: callID,
				Name:   spec.Name,
				Input:  codexCustomToolInputFromArguments(arguments),
			}
		case codexToolKindToolSearch:
			return CodexOutputItem{
				Type:      "tool_search_call",
				ID:        "tsc_" + strings.TrimPrefix(callID, "call_"),
				Status:    "completed",
				CallID:    callID,
				Execution: "client",
				Arguments: arguments,
			}
		case codexToolKindFunction:
			return CodexOutputItem{
				Type:      "function_call",
				ID:        "fc_" + strings.TrimPrefix(callID, "call_"),
				Status:    "completed",
				CallID:    callID,
				Namespace: spec.Namespace,
				Name:      spec.Name,
				Arguments: arguments,
			}
		}
	}
	return CodexOutputItem{
		Type:      "function_call",
		ID:        "fc_" + strings.TrimPrefix(callID, "call_"),
		Status:    "completed",
		CallID:    callID,
		Name:      chatName,
		Arguments: arguments,
	}
}

func codexCustomToolInputFromArguments(arguments string) any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return arguments
	}
	if input, ok := parsed["input"]; ok {
		return input
	}
	return arguments
}

func codexToolArgumentsRawMessage(arguments string) json.RawMessage {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || !json.Valid([]byte(arguments)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(arguments)
}

func codexInputTokenDetailsFromOpenAI(details *TokenUsageDetails) *TokenUsageDetails {
	if details == nil || details.CachedTokens <= 0 {
		return nil
	}
	return &TokenUsageDetails{CachedTokens: details.CachedTokens}
}

func codexOutputTokenDetailsFromOpenAI(details *TokenUsageDetails) *TokenUsageDetails {
	if details == nil || details.ReasoningTokens <= 0 {
		return nil
	}
	return &TokenUsageDetails{ReasoningTokens: details.ReasoningTokens}
}

func convertCodexInputToOpenAIMessages(input json.RawMessage, toolCtx ...*codexToolContext) ([]OpenAIMessage, error) {
	var raw any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse Codex input: %w", err)
	}
	items, ok := raw.([]any)
	if !ok {
		if s, ok := raw.(string); ok {
			return []OpenAIMessage{{Role: "user", Content: marshalStringAsJSONRaw("codex_input", s)}}, nil
		}
		return nil, fmt.Errorf("unsupported Codex input format")
	}
	messages := make([]OpenAIMessage, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		switch itemType {
		case "message", "":
			role, _ := m["role"].(string)
			if role == "" {
				role = "user"
			}
			text := codexContentText(m["content"], role)
			if text == "" {
				continue
			}
			if role == "developer" {
				role = "system"
			}
			messages = append(messages, OpenAIMessage{Role: role, Content: marshalStringAsJSONRaw("codex_message", text)})
		case "function_call", "custom_tool_call", "tool_search_call", "mcp_tool_call":
			callID := stringFromMap(m, "call_id")
			if callID == "" {
				callID = stringFromMap(m, "id")
			}
			name := stringFromMap(m, "name")
			arguments := stringFromMap(m, "arguments")
			if itemType == "custom_tool_call" {
				inputValue := m["input"]
				inputBytes, _ := json.Marshal(map[string]any{"input": inputValue})
				arguments = string(inputBytes)
			} else if itemType == "tool_search_call" {
				name = codexToolSearchProxyName
			} else if len(toolCtx) > 0 && toolCtx[0] != nil {
				name = toolCtx[0].chatNameFor(name, stringFromMap(m, "namespace"))
			}
			if callID == "" || name == "" {
				continue
			}
			messages = append(messages, OpenAIMessage{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{{
					ID:   callID,
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      name,
						Arguments: arguments,
					},
				}},
			})
		case "function_call_output", "custom_tool_call_output", "tool_search_output", "mcp_tool_call_output":
			callID := stringFromMap(m, "call_id")
			output := stringFromMap(m, "output")
			messages = append(messages, OpenAIMessage{
				Role:       "tool",
				ToolCallID: callID,
				Content:    marshalStringAsJSONRaw("codex_tool_output", output),
			})
		}
	}
	return messages, nil
}

func convertCodexInputToClaudeMessages(input json.RawMessage, toolCtx ...*codexToolContext) ([]ClaudeMessage, error) {
	var raw any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse Codex input: %w", err)
	}
	items, ok := raw.([]any)
	if !ok {
		if s, ok := raw.(string); ok {
			content, _ := json.Marshal([]ClaudeContentBlock{{Type: "text", Text: s}})
			return []ClaudeMessage{{Role: "user", Content: content}}, nil
		}
		return nil, fmt.Errorf("unsupported Codex input format")
	}
	messages := make([]ClaudeMessage, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		switch itemType {
		case "message", "":
			role, _ := m["role"].(string)
			if role == "" || role == "developer" || role == "system" {
				role = "user"
			}
			text := codexContentText(m["content"], role)
			if text == "" {
				continue
			}
			content, _ := json.Marshal([]ClaudeContentBlock{{Type: "text", Text: text}})
			messages = append(messages, ClaudeMessage{Role: role, Content: content})
		case "function_call", "custom_tool_call", "tool_search_call", "mcp_tool_call":
			callID := strings.TrimPrefix(stringFromMap(m, "call_id"), "call_")
			name := stringFromMap(m, "name")
			arguments := stringFromMap(m, "arguments")
			if itemType == "custom_tool_call" {
				inputValue := m["input"]
				inputBytes, _ := json.Marshal(map[string]any{"input": inputValue})
				arguments = string(inputBytes)
			} else if itemType == "tool_search_call" {
				name = codexToolSearchProxyName
			} else if len(toolCtx) > 0 && toolCtx[0] != nil {
				name = toolCtx[0].chatNameFor(name, stringFromMap(m, "namespace"))
			}
			if callID == "" || name == "" {
				continue
			}
			content, _ := json.Marshal([]ClaudeContentBlock{{
				Type:  "tool_use",
				ID:    callID,
				Name:  name,
				Input: codexToolArgumentsRawMessage(arguments),
			}})
			messages = append(messages, ClaudeMessage{Role: "assistant", Content: content})
		case "function_call_output", "custom_tool_call_output", "tool_search_output", "mcp_tool_call_output":
			callID := strings.TrimPrefix(stringFromMap(m, "call_id"), "call_")
			output := stringFromMap(m, "output")
			content, _ := json.Marshal([]ClaudeContentBlock{{
				Type:      "tool_result",
				ToolUseID: callID,
				Content:   marshalStringAsJSONRaw("codex_tool_output", output),
			}})
			messages = append(messages, ClaudeMessage{Role: "user", Content: content})
		}
	}
	return messages, nil
}

func codexContentText(content any, role string) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, part := range v {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := m["type"].(string)
			if partType == "input_text" || partType == "output_text" || partType == "text" {
				sb.WriteString(stringFromMap(m, "text"))
			}
		}
		return sb.String()
	default:
		if content == nil {
			return ""
		}
		b, err := json.Marshal(content)
		if err != nil {
			return fmt.Sprint(content)
		}
		logrus.WithField("role", role).Debug("Force Codex: converted non-text content to JSON string")
		return string(b)
	}
}

func stringFromMap(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		b, err := json.Marshal(s)
		if err != nil {
			return fmt.Sprint(s)
		}
		return string(b)
	}
}

func appendCodexToolToOpenAIChat(tools *[]OpenAITool, tool CodexTool, namespace string) {
	switch tool.Type {
	case "", "function":
		name := codexChatToolName(tool.Name, namespace)
		if name == "" {
			return
		}
		*tools = append(*tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        name,
				Description: tool.Description,
				Parameters:  normalizeToolParameters(tool.Parameters),
			},
		})
	case "custom":
		if tool.Name == "" {
			return
		}
		*tools = append(*tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        tool.Name,
				Description: codexCustomToolDescription(tool),
				Parameters:  codexCustomToolParameters(),
			},
		})
	case "tool_search":
		*tools = append(*tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        codexToolSearchProxyName,
				Description: "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
				Parameters:  codexToolSearchParameters(),
			},
		})
	case "namespace":
		for _, child := range codexNamespaceChildren(tool) {
			appendCodexToolToOpenAIChat(tools, child, tool.Name)
		}
	}
}

func appendCodexToolToClaude(tools *[]ClaudeTool, tool CodexTool, namespace string) {
	switch tool.Type {
	case "", "function":
		name := codexChatToolName(tool.Name, namespace)
		if name == "" {
			return
		}
		*tools = append(*tools, ClaudeTool{
			Name:        name,
			Description: tool.Description,
			InputSchema: normalizeToolParameters(tool.Parameters),
		})
	case "custom":
		if tool.Name == "" {
			return
		}
		*tools = append(*tools, ClaudeTool{
			Name:        tool.Name,
			Description: codexCustomToolDescription(tool),
			InputSchema: codexCustomToolParameters(),
		})
	case "tool_search":
		*tools = append(*tools, ClaudeTool{
			Name:        codexToolSearchProxyName,
			Description: "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
			InputSchema: codexToolSearchParameters(),
		})
	case "namespace":
		for _, child := range codexNamespaceChildren(tool) {
			appendCodexToolToClaude(tools, child, tool.Name)
		}
	}
}

func codexCustomToolDescription(tool CodexTool) string {
	if tool.Description == "" {
		return "Original Codex custom tool. Pass raw input in the input field."
	}
	return tool.Description
}

func codexCustomToolParameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"Raw string input for the original custom tool."}},"required":["input"]}`)
}

func codexToolSearchParameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`)
}

func convertResponsesToolChoiceToOpenAIChat(toolChoice any, toolCtx ...*codexToolContext) any {
	switch v := toolChoice.(type) {
	case nil:
		return nil
	case string:
		return v
	case map[string]any:
		if t, _ := v["type"].(string); t == "function" || t == "custom" || t == "tool_search" {
			if name, _ := v["name"].(string); name != "" {
				if t == "tool_search" {
					name = codexToolSearchProxyName
				} else if len(toolCtx) > 0 && toolCtx[0] != nil {
					namespace, _ := v["namespace"].(string)
					name = toolCtx[0].chatNameFor(name, namespace)
				}
				return map[string]any{
					"type": "function",
					"function": map[string]string{
						"name": name,
					},
				}
			}
			if t == "tool_search" {
				return map[string]any{
					"type": "function",
					"function": map[string]string{
						"name": codexToolSearchProxyName,
					},
				}
			}
		}
		return v
	default:
		return v
	}
}

func convertResponsesToolChoiceToClaude(toolChoice any, toolCtx ...*codexToolContext) json.RawMessage {
	switch v := toolChoice.(type) {
	case nil:
		return nil
	case string:
		var mapped any
		switch v {
		case "required":
			mapped = map[string]any{"type": "any"}
		case "auto", "none":
			mapped = map[string]any{"type": v}
		default:
			return nil
		}
		out, _ := json.Marshal(mapped)
		return out
	case map[string]any:
		if t, _ := v["type"].(string); t == "function" || t == "custom" || t == "tool_search" {
			if name, _ := v["name"].(string); name != "" {
				if t == "tool_search" {
					name = codexToolSearchProxyName
				} else if len(toolCtx) > 0 && toolCtx[0] != nil {
					namespace, _ := v["namespace"].(string)
					name = toolCtx[0].chatNameFor(name, namespace)
				}
				out, _ := json.Marshal(map[string]any{"type": "tool", "name": name})
				return out
			}
			if t == "tool_search" {
				out, _ := json.Marshal(map[string]any{"type": "tool", "name": codexToolSearchProxyName})
				return out
			}
		}
	}
	return nil
}

func appendParsedFunctionCallsToCodex(resp *CodexResponse, calls []functionCall) {
	for _, call := range calls {
		if call.Name == "" {
			continue
		}
		argsJSON, err := json.Marshal(call.Args)
		if err != nil {
			logrus.WithError(err).Debug("Force Codex: failed to marshal parsed function call args")
			continue
		}
		callID := "call_" + utils.GenerateRandomSuffix()
		resp.Output = append(resp.Output, CodexOutputItem{
			Type:      "function_call",
			ID:        "fc_" + strings.TrimPrefix(callID, "call_"),
			Status:    "completed",
			CallID:    callID,
			Name:      call.Name,
			Arguments: string(argsJSON),
		})
	}
}

func (ps *ProxyServer) applyForceCodexRequestConversion(c *gin.Context, group *models.Group, bodyBytes []byte) ([]byte, bool, error) {
	var codexReq CodexRequest
	if err := json.Unmarshal(bodyBytes, &codexReq); err != nil {
		return bodyBytes, false, fmt.Errorf("failed to parse Codex request: %w", err)
	}
	toolCtx := newCodexToolContext(codexReq.Tools)
	c.Set(ctxKeyCodexToolContext, toolCtx)

	switch group.ChannelType {
	case "openai":
		chatReq, err := convertCodexRequestToOpenAIChat(&codexReq)
		if err != nil {
			return bodyBytes, false, err
		}
		out, err := json.Marshal(chatReq)
		if err != nil {
			return bodyBytes, false, err
		}
		c.Set(ctxKeyCodexEnabled, true)
		setCodexUpstreamFormat(c, codexUpstreamOpenAIChat)
		return out, true, nil
	case "anthropic":
		claudeReq, err := convertCodexRequestToClaude(&codexReq)
		if err != nil {
			return bodyBytes, false, err
		}
		out, err := json.Marshal(claudeReq)
		if err != nil {
			return bodyBytes, false, err
		}
		c.Set(ctxKeyCodexEnabled, true)
		setCodexUpstreamFormat(c, codexUpstreamClaude)
		return out, true, nil
	case "openai-response":
		c.Set(ctxKeyCodexEnabled, true)
		setCodexUpstreamFormat(c, codexUpstreamResponses)
		return bodyBytes, true, nil
	default:
		return bodyBytes, false, fmt.Errorf("unsupported channel type %q for Codex support", group.ChannelType)
	}
}

func (ps *ProxyServer) handleForceCodexNormalResponse(c *gin.Context, resp *http.Response) {
	format := getCodexUpstreamFormat(c)
	if format == codexUpstreamResponses {
		if isFunctionCallEnabled(c) {
			ps.handleFunctionCallNormalResponseByChannel(c, resp, functionCallGroupFromContext(c))
			return
		}
		ps.handleNormalResponse(c, resp)
		return
	}

	bodyBytes, err := readAllWithLimit(resp.Body, maxUpstreamResponseBodySize)
	if err != nil {
		writeForceCodexGatewayError(c, "Upstream response body is too large")
		return
	}

	origEncoding := resp.Header.Get("Content-Encoding")
	bodyBytes, err = utils.DecompressResponseWithLimit(origEncoding, bodyBytes, maxUpstreamResponseBodySize)
	if err != nil {
		writeForceCodexGatewayError(c, "Failed to decompress upstream response body")
		return
	}
	if origEncoding != "" {
		clearUpstreamEncodingHeaders(c)
	}

	var codexResp *CodexResponse
	switch format {
	case codexUpstreamOpenAIChat:
		var openaiResp OpenAIResponse
		if err := json.Unmarshal(bodyBytes, &openaiResp); err != nil {
			if resp.StatusCode >= http.StatusBadRequest {
				codexResp = rawCodexErrorResponse(resp.StatusCode, bodyBytes)
			} else {
				writeForceCodexPassthrough(c, resp, bodyBytes)
				return
			}
		} else {
			codexResp = convertOpenAIChatToCodexResponse(&openaiResp, functionCallTriggerSignal(c), codexToolContextFromGin(c))
		}
	case codexUpstreamClaude:
		var claudeResp ClaudeResponse
		if err := json.Unmarshal(bodyBytes, &claudeResp); err != nil {
			if resp.StatusCode >= http.StatusBadRequest {
				codexResp = rawCodexErrorResponse(resp.StatusCode, bodyBytes)
			} else {
				writeForceCodexPassthrough(c, resp, bodyBytes)
				return
			}
		} else {
			codexResp = convertClaudeToCodexResponse(&claudeResp, codexToolContextFromGin(c))
		}
	default:
		writeForceCodexPassthrough(c, resp, bodyBytes)
		return
	}

	out, err := json.Marshal(codexResp)
	if err != nil {
		writeForceCodexGatewayError(c, "Failed to marshal Codex response")
		return
	}
	setTokenUsageOrEstimateFromFullBodyIf(c, out, resp.StatusCode < http.StatusBadRequest)
	if shouldCaptureResponse(c) {
		c.Set("response_body", sanitizeAndTruncateBytesForLog(out, maxResponseCaptureBytes))
	}
	clearUpstreamEncodingHeaders(c)
	c.Data(resp.StatusCode, "application/json", out)
}

func (ps *ProxyServer) handleForceCodexStreamingResponse(c *gin.Context, resp *http.Response) {
	format := getCodexUpstreamFormat(c)
	if format == codexUpstreamResponses {
		if isFunctionCallEnabled(c) {
			ps.handleFunctionCallStreamingResponse(c, resp)
			return
		}
		ps.handleStreamingResponse(c, resp)
		return
	}

	// Streaming cross-protocol conversion is collected into a bounded buffer and
	// then emitted as Responses SSE. This mirrors the existing force_function_call
	// stream path and avoids leaking upstream-native events to Codex clients.
	bodyBytes, err := readAllWithLimit(resp.Body, maxUpstreamResponseBodySize)
	if err != nil {
		writeForceCodexGatewayError(c, "Upstream streaming response is too large")
		return
	}
	origEncoding := resp.Header.Get("Content-Encoding")
	bodyBytes, err = utils.DecompressResponseWithLimit(origEncoding, bodyBytes, maxUpstreamResponseBodySize)
	if err != nil {
		writeForceCodexGatewayError(c, "Failed to decompress upstream streaming response")
		return
	}
	streamResp, statusCode := ps.convertForceCodexCollectedStream(c, resp.StatusCode, format, bodyBytes)
	out, err := json.Marshal(streamResp)
	if err != nil {
		writeForceCodexGatewayError(c, "Failed to marshal collected Codex stream")
		return
	}
	if !setTokenUsageFromBody(c, bodyBytes) {
		setTokenUsageOrEstimateFromFullBodyIf(c, out, statusCode < http.StatusBadRequest)
	}
	clearUpstreamEncodingHeaders(c)
	c.Header("Content-Type", "text/event-stream")
	c.Status(statusCode)
	if err := writeForceCodexCollectedStreamEvents(c, streamResp); err != nil {
		writeForceCodexGatewayError(c, "Failed to marshal collected Codex stream event")
		return
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeForceCodexCollectedStreamEvents(c *gin.Context, streamResp *CodexResponse) error {
	responseForStatus := func(status string) *CodexResponse {
		cp := *streamResp
		cp.Status = status
		if status != "completed" {
			cp.Output = []CodexOutputItem{}
		}
		return &cp
	}

	var captured strings.Builder
	writeEvent := func(event string, payload any) error {
		eventBytes, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		chunk := "event: " + event + "\n" + "data: " + string(eventBytes) + "\n\n"
		if captured.Len() < maxResponseCaptureBytes {
			remaining := maxResponseCaptureBytes - captured.Len()
			if len(chunk) > remaining {
				captured.WriteString(chunk[:remaining])
			} else {
				captured.WriteString(chunk)
			}
		}
		_, err = c.Writer.Write([]byte(chunk))
		return err
	}

	if err := writeEvent("response.created", map[string]any{
		"type":     "response.created",
		"response": responseForStatus("in_progress"),
	}); err != nil {
		return err
	}

	for outputIndex := range streamResp.Output {
		item := &streamResp.Output[outputIndex]
		ensureCodexStreamOutputItemID(streamResp, item, outputIndex)
		if err := writeForceCodexOutputItemEvents(writeEvent, *item, outputIndex); err != nil {
			return err
		}
	}

	if err := writeEvent("response.completed", map[string]any{
		"type":     "response.completed",
		"response": streamResp,
	}); err != nil {
		return err
	}
	doneChunk := "data: [DONE]\n\n"
	if captured.Len() < maxResponseCaptureBytes {
		remaining := maxResponseCaptureBytes - captured.Len()
		if len(doneChunk) > remaining {
			captured.WriteString(doneChunk[:remaining])
		} else {
			captured.WriteString(doneChunk)
		}
	}
	if _, err := c.Writer.Write([]byte(doneChunk)); err != nil {
		return err
	}
	if shouldCaptureResponse(c) && captured.Len() > 0 {
		c.Set("response_body", sanitizeAndTruncateStringForLog(captured.String(), maxResponseCaptureBytes))
	}
	return nil
}

func ensureCodexStreamOutputItemID(resp *CodexResponse, item *CodexOutputItem, outputIndex int) {
	if item.ID != "" {
		return
	}
	prefix := "item"
	switch item.Type {
	case "reasoning":
		prefix = "rs"
	case "message":
		prefix = "msg"
	case "function_call":
		prefix = "fc"
	}
	base := strings.TrimPrefix(resp.ID, "resp_")
	if base == "" {
		base = "stream"
	}
	item.ID = fmt.Sprintf("%s_%s_%d", prefix, base, outputIndex)
}

func writeForceCodexOutputItemEvents(writeEvent func(string, any) error, item CodexOutputItem, outputIndex int) error {
	addedItem := item
	addedItem.Status = "in_progress"
	switch item.Type {
	case "reasoning":
		addedItem.Summary = []CodexSummaryItem{}
	case "message":
		addedItem.Content = []CodexContentBlock{}
	case "function_call":
		addedItem.Arguments = ""
	}
	if err := writeEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": outputIndex,
		"item":         addedItem,
	}); err != nil {
		return err
	}

	switch item.Type {
	case "reasoning":
		text := codexReasoningSummaryText(item)
		if text != "" {
			if err := writeEvent("response.reasoning_summary_part.added", map[string]any{
				"type":          "response.reasoning_summary_part.added",
				"item_id":       item.ID,
				"output_index":  outputIndex,
				"summary_index": 0,
				"part": map[string]any{
					"type": "summary_text",
					"text": "",
				},
			}); err != nil {
				return err
			}
			if err := writeEvent("response.reasoning_summary_text.delta", map[string]any{
				"type":          "response.reasoning_summary_text.delta",
				"item_id":       item.ID,
				"output_index":  outputIndex,
				"summary_index": 0,
				"delta":         text,
			}); err != nil {
				return err
			}
			if err := writeEvent("response.reasoning_summary_part.done", map[string]any{
				"type":          "response.reasoning_summary_part.done",
				"item_id":       item.ID,
				"output_index":  outputIndex,
				"summary_index": 0,
				"part": map[string]any{
					"type": "summary_text",
					"text": text,
				},
			}); err != nil {
				return err
			}
		}
	case "message":
		for contentIndex, content := range item.Content {
			if content.Type != "output_text" || content.Text == "" {
				continue
			}
			if err := writeEvent("response.output_text.delta", map[string]any{
				"type":          "response.output_text.delta",
				"item_id":       item.ID,
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"delta":         content.Text,
			}); err != nil {
				return err
			}
		}
	case "function_call":
		if item.Arguments != "" {
			if err := writeEvent("response.function_call_arguments.delta", map[string]any{
				"type":         "response.function_call_arguments.delta",
				"item_id":      item.ID,
				"output_index": outputIndex,
				"delta":        item.Arguments,
			}); err != nil {
				return err
			}
			if err := writeEvent("response.function_call_arguments.done", map[string]any{
				"type":         "response.function_call_arguments.done",
				"item_id":      item.ID,
				"output_index": outputIndex,
				"arguments":    item.Arguments,
			}); err != nil {
				return err
			}
		}
	}

	doneItem := item
	doneItem.Status = "completed"
	return writeEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": outputIndex,
		"item":         doneItem,
	})
}

func codexReasoningSummaryText(item CodexOutputItem) string {
	var b strings.Builder
	for _, summary := range item.Summary {
		if summary.Text != "" {
			b.WriteString(summary.Text)
		}
	}
	return b.String()
}

func (ps *ProxyServer) convertForceCodexCollectedStream(c *gin.Context, statusCode int, format string, bodyBytes []byte) (*CodexResponse, int) {
	if statusCode >= http.StatusBadRequest {
		return rawCodexErrorResponse(statusCode, bodyBytes), statusCode
	}

	switch format {
	case codexUpstreamOpenAIChat:
		openaiResp := collectOpenAIChatStreamToResponse(bodyBytes)
		return convertOpenAIChatToCodexResponse(openaiResp, functionCallTriggerSignal(c), codexToolContextFromGin(c)), statusCode
	case codexUpstreamClaude:
		claudeResp := collectClaudeStreamToResponse(bodyBytes)
		return convertClaudeToCodexResponse(claudeResp, codexToolContextFromGin(c)), statusCode
	default:
		return rawCodexErrorResponse(http.StatusBadGateway, []byte("unsupported Codex stream conversion")), http.StatusBadGateway
	}
}

func collectOpenAIChatStreamToResponse(bodyBytes []byte) *OpenAIResponse {
	resp := &OpenAIResponse{
		ID:      "chatcmpl_" + utils.GenerateRandomSuffix(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Choices: []OpenAIChoice{{
			Index: 0,
			Message: &OpenAIRespMessage{
				Role: "assistant",
			},
		}},
	}
	var content strings.Builder
	var reasoningContent strings.Builder
	toolCallsByIndex := make(map[int]*OpenAIToolCall)
	finishReason := ""
	for _, data := range extractSSEDataPayloads(bodyBytes) {
		if data == "[DONE]" {
			continue
		}
		var chunk OpenAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.ID != "" {
			resp.ID = chunk.ID
		}
		if chunk.Created != 0 {
			resp.Created = chunk.Created
		}
		if chunk.Model != "" {
			resp.Model = chunk.Model
		}
		if chunk.Usage != nil {
			resp.Usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		if chunk.Choices[0].FinishReason != nil {
			finishReason = *chunk.Choices[0].FinishReason
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != nil {
			content.WriteString(*delta.Content)
		}
		if delta.ReasoningContent != nil {
			reasoningContent.WriteString(*delta.ReasoningContent)
		}
		for idx, tc := range delta.ToolCalls {
			key := idx
			if tc.Index != nil {
				key = *tc.Index
			}
			current := toolCallsByIndex[key]
			if current == nil {
				copyCall := OpenAIToolCall{Type: "function"}
				current = &copyCall
				toolCallsByIndex[key] = current
			}
			if tc.ID != "" {
				current.ID = tc.ID
			}
			if tc.Type != "" {
				current.Type = tc.Type
			}
			if tc.Function.Name != "" {
				current.Function.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				current.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if content.Len() > 0 {
		text := content.String()
		resp.Choices[0].Message.Content = &text
	}
	if reasoningContent.Len() > 0 {
		reasoning := reasoningContent.String()
		resp.Choices[0].Message.ReasoningContent = &reasoning
	}
	if finishReason != "" {
		resp.Choices[0].FinishReason = &finishReason
	}
	for i := 0; i < len(toolCallsByIndex); i++ {
		if tc := toolCallsByIndex[i]; tc != nil && tc.ID != "" && tc.Function.Name != "" {
			resp.Choices[0].Message.ToolCalls = append(resp.Choices[0].Message.ToolCalls, *tc)
		}
	}
	return resp
}

func collectClaudeStreamToResponse(bodyBytes []byte) *ClaudeResponse {
	resp := &ClaudeResponse{
		ID:      "msg_" + utils.GenerateRandomSuffix(),
		Type:    "message",
		Role:    "assistant",
		Content: make([]ClaudeContentBlock, 0),
		Usage:   &ClaudeUsage{},
	}
	blocks := make(map[int]*ClaudeContentBlock)
	for _, data := range extractSSEDataPayloads(bodyBytes) {
		var event ClaudeStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				resp.ID = event.Message.ID
				resp.Model = event.Message.Model
				if event.Message.Usage != nil {
					resp.Usage.InputTokens = event.Message.Usage.InputTokens
				}
			}
		case "content_block_start":
			if event.ContentBlock != nil {
				copyBlock := *event.ContentBlock
				if copyBlock.Type == "tool_use" {
					copyBlock.Input = nil
				}
				blocks[event.Index] = &copyBlock
			}
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil || event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				block.Text += event.Delta.Text
			case "thinking_delta":
				block.Thinking += event.Delta.Thinking
			case "input_json_delta":
				block.Input = append(block.Input, []byte(event.Delta.PartialJSON)...)
			}
		case "content_block_stop":
			if block := blocks[event.Index]; block != nil {
				resp.Content = append(resp.Content, *block)
				delete(blocks, event.Index)
			}
		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				stop := event.Delta.StopReason
				resp.StopReason = &stop
			}
			if event.Usage != nil {
				resp.Usage.OutputTokens = event.Usage.OutputTokens
			}
		}
	}
	keys := make([]int, 0, len(blocks))
	for k := range blocks {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		if block := blocks[k]; block != nil {
			resp.Content = append(resp.Content, *block)
		}
	}
	return resp
}

func extractSSEDataPayloads(bodyBytes []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(bodyBytes))
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexStreamLineBytes)
	var payloads []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		payloads = append(payloads, current.String())
		current.Reset()
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "data:") {
			if current.Len() > 0 {
				current.WriteByte('\n')
			}
			current.WriteString(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:")))
		}
	}
	flush()
	return payloads
}

func rawCodexErrorResponse(statusCode int, body []byte) *CodexResponse {
	msg := strings.TrimSpace(utils.SanitizeErrorBody(string(body)))
	if msg == "" {
		msg = fmt.Sprintf("Upstream returned status %d", statusCode)
	}
	return &CodexResponse{
		ID:        "resp_" + utils.GenerateRandomSuffix(),
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Status:    "failed",
		Error: &CodexError{
			Type:    "server_error",
			Message: msg,
		},
	}
}

func writeForceCodexGatewayError(c *gin.Context, message string) {
	clearUpstreamEncodingHeaders(c)
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "server_error",
		},
	})
}

func writeForceCodexPassthrough(c *gin.Context, resp *http.Response, body []byte) {
	if shouldCaptureResponse(c) {
		c.Set("response_body", sanitizeAndTruncateBytesForLog(body, maxResponseCaptureBytes))
	}
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}
