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
	Kind      codexToolKind
	Name      string
	Namespace string
	Execution string
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
				Kind:      codexToolKindFunction,
				Name:      tool.Name,
				Namespace: namespace,
			}
		}
	case "custom":
		if tool.Name != "" {
			ctx.byChatName[tool.Name] = codexToolSpec{Kind: codexToolKindCustom, Name: tool.Name}
		}
	case "tool_search":
		ctx.byChatName[codexToolSearchProxyName] = codexToolSpec{
			Kind: codexToolKindToolSearch, Name: codexToolSearchProxyName, Execution: tool.Execution,
		}
	case "namespace":
		nextNamespace := codexChatToolName(tool.Name, namespace)
		for _, child := range codexNamespaceChildren(tool) {
			ctx.addTool(child, nextNamespace)
		}
	default:
		// Future/unknown tool kinds have no gateway executor here. Convert them
		// through the target protocol's function shell and retain their name.
		name := codexToolName(tool)
		chatName := codexChatToolName(name, namespace)
		if chatName != "" {
			ctx.byChatName[chatName] = codexToolSpec{
				Kind:      codexToolKindFunction,
				Name:      name,
				Namespace: namespace,
			}
		}
	}
}

func codexToolName(tool CodexTool) string {
	if strings.TrimSpace(tool.Name) != "" {
		return tool.Name
	}
	return tool.Type
}

func isValidCodexToolCallArguments(toolName, arguments string, toolCtx *codexToolContext) bool {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "{}" {
		return true
	}
	return isValidToolCallArguments(toolName, arguments)
}

func decodeCodexJSONUseNumber(data []byte, value any) error {
	return utils.UnmarshalJSONUseNumber(data, value)
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

func codexRequestTools(req *CodexRequest) ([]CodexTool, error) {
	if req == nil {
		return nil, nil
	}
	inputTools, err := codexInputToolDefinitions(req.Input)
	if err != nil {
		return nil, err
	}
	total := len(req.Tools) + len(inputTools)
	seen := make(map[string]struct{}, total)
	tools := make([]CodexTool, 0, total)
	for _, candidates := range [...][]CodexTool{req.Tools, inputTools} {
		for _, tool := range candidates {
			if filtered, ok := deduplicateCodexTool(tool, "", seen); ok {
				tools = append(tools, filtered)
			}
		}
	}
	return tools, nil
}

func deduplicateCodexTool(tool CodexTool, namespace string, seen map[string]struct{}) (CodexTool, bool) {
	if tool.Type == "namespace" {
		nextNamespace := codexChatToolName(tool.Name, namespace)
		children := codexNamespaceChildren(tool)
		filtered := make([]CodexTool, 0, len(children))
		for _, child := range children {
			if unique, ok := deduplicateCodexTool(child, nextNamespace, seen); ok {
				filtered = append(filtered, unique)
			}
		}
		if len(filtered) == 0 {
			return CodexTool{}, false
		}
		if len(tool.Tools) > 0 {
			tool.Tools = filtered
		} else {
			tool.Children = filtered
		}
		return tool, true
	}

	chatName := codexChatToolName(codexToolName(tool), namespace)
	switch tool.Type {
	case "custom":
		chatName = tool.Name
	case "tool_search":
		chatName = codexToolSearchProxyName
	}
	if chatName == "" {
		return tool, true
	}
	if _, duplicate := seen[chatName]; duplicate {
		return CodexTool{}, false
	}
	seen[chatName] = struct{}{}
	return tool, true
}

func codexInputToolDefinitions(input json.RawMessage) ([]CodexTool, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("failed to parse Codex input items: %w", err)
	}
	var tools []CodexTool
	for _, item := range items {
		var envelope struct {
			Type  string          `json:"type"`
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(item, &envelope); err != nil {
			return nil, fmt.Errorf("failed to parse Codex input item: %w", err)
		}
		if envelope.Type != "additional_tools" && envelope.Type != "tool_search_output" {
			continue
		}
		if len(envelope.Tools) == 0 || string(envelope.Tools) == "null" {
			continue
		}
		var loaded []CodexTool
		if err := json.Unmarshal(envelope.Tools, &loaded); err != nil {
			return nil, fmt.Errorf("failed to parse %s tools: %w", envelope.Type, err)
		}
		tools = append(tools, loaded...)
	}
	return tools, nil
}

func codexToolCallItemType(itemType string) bool {
	if codexToolOutputItemType(itemType) {
		return false
	}
	if itemType == "function_call" || itemType == "custom_tool_call" || itemType == "tool_search_call" || itemType == "mcp_tool_call" {
		return true
	}
	return strings.HasSuffix(itemType, "_call") || strings.Contains(itemType, "_tool_call")
}

func codexToolOutputItemType(itemType string) bool {
	if itemType == "function_call_output" || itemType == "custom_tool_call_output" || itemType == "tool_search_output" || itemType == "mcp_tool_call_output" {
		return true
	}
	return strings.HasSuffix(itemType, "_output") || strings.HasSuffix(itemType, "_result") || strings.Contains(itemType, "_tool_result")
}

func codexToolCallArguments(item map[string]any, itemType string) string {
	if raw, ok := item["arguments"]; ok && raw != nil {
		return stringFromValue(raw)
	}
	input, ok := item["input"]
	if !ok || input == nil {
		return ""
	}
	if itemType == "custom_tool_call" {
		encoded, _ := json.Marshal(map[string]any{"input": input})
		return string(encoded)
	}
	encoded, _ := json.Marshal(input)
	return string(encoded)
}

func codexInputSystemText(input json.RawMessage) ([]string, error) {
	var raw any
	if err := decodeCodexJSONUseNumber(input, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse Codex input: %w", err)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	var text []string
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := message["type"].(string)
		role, _ := message["role"].(string)
		if (itemType == "message" || itemType == "") && (role == "system" || role == "developer") {
			if content := codexContentText(message["content"], role); content != "" {
				text = append(text, content)
			}
		}
	}
	return text, nil
}

func convertCodexRequestToOpenAIChat(codexReq *CodexRequest) (*OpenAIRequest, error) {
	if codexReq == nil {
		return nil, fmt.Errorf("codex request is nil")
	}
	requestTools, err := codexRequestTools(codexReq)
	if err != nil {
		return nil, err
	}
	toolCtx := newCodexToolContext(requestTools)
	req := &OpenAIRequest{
		Model:       codexReq.Model,
		Stream:      codexReq.Stream,
		Temperature: codexReq.Temperature,
		TopP:        codexReq.TopP,
		MaxTokens:   codexReq.MaxOutputTokens,
	}
	if codexReq.Reasoning != nil {
		// Effort is provider-defined. Move the field between protocol shapes
		// without normalizing or validating values so newly introduced levels
		// continue to pass through unchanged.
		req.ReasoningEffort = codexReq.Reasoning.Effort
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

	if len(requestTools) > 0 {
		req.Tools = make([]OpenAITool, 0, len(requestTools))
		for _, tool := range requestTools {
			appendCodexToolToOpenAIChat(&req.Tools, tool, "")
		}
		req.ToolChoice = convertResponsesToolChoiceToOpenAIChat(codexReq.ToolChoice, toolCtx)
		req.ParallelToolCalls = codexReq.ParallelToolCalls
	}
	return req, nil
}

func convertCodexRequestToClaude(codexReq *CodexRequest) (*ClaudeRequest, error) {
	if codexReq == nil {
		return nil, fmt.Errorf("codex request is nil")
	}
	requestTools, err := codexRequestTools(codexReq)
	if err != nil {
		return nil, err
	}
	toolCtx := newCodexToolContext(requestTools)
	req := &ClaudeRequest{
		Model:       codexReq.Model,
		Stream:      codexReq.Stream,
		Temperature: codexReq.Temperature,
		TopP:        codexReq.TopP,
	}
	if codexReq.MaxOutputTokens != nil {
		req.MaxTokens = *codexReq.MaxOutputTokens
	}
	if codexReq.Reasoning != nil {
		effort := codexReq.Reasoning.Effort
		switch effort {
		case "":
			// An empty effort carries no portable enable/disable signal; omit
			// Claude thinking fields and let the target model choose its default.
		case "none":
			req.Thinking = &ThinkingConfig{Type: "disabled"}
		default:
			req.Thinking = &ThinkingConfig{Type: "adaptive"}
			req.OutputConfig = &ClaudeOutputConfig{Effort: effort}
			req.Temperature = nil
			req.TopP = nil
		}
	}
	systemText := make([]string, 0, 3)
	if strings.TrimSpace(codexReq.Instructions) != "" {
		systemText = append(systemText, codexReq.Instructions)
	}
	inputSystemText, err := codexInputSystemText(codexReq.Input)
	if err != nil {
		return nil, err
	}
	systemText = append(systemText, inputSystemText...)
	if len(systemText) > 0 {
		req.System = marshalStringAsJSONRaw("codex_instructions", strings.Join(systemText, "\n\n"))
	}

	messages, err := convertCodexInputToClaudeMessages(codexReq.Input, toolCtx)
	if err != nil {
		return nil, err
	}
	req.Messages = messages

	if len(requestTools) > 0 {
		req.Tools = make([]ClaudeTool, 0, len(requestTools))
		for _, tool := range requestTools {
			appendCodexToolToClaude(&req.Tools, tool, "")
		}
	}
	req.ToolChoice = convertResponsesToolChoiceToClaude(codexReq.ToolChoice, toolCtx)
	req.ToolChoice, err = codexClaudeToolChoiceWithParallel(req.ToolChoice, codexReq.ParallelToolCalls, len(req.Tools) > 0)
	if err != nil {
		return nil, err
	}
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
			Message: strings.TrimSpace(utils.SanitizeErrorBody(openaiResp.Error.Message)),
		}
		if resp.Error.Message == "" {
			resp.Error.Message = "Upstream returned an error"
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
	if claudeResp.Error != nil {
		message := strings.TrimSpace(utils.SanitizeErrorBody(claudeResp.Error.Message))
		if message == "" {
			message = "Upstream returned an error"
		}
		return &CodexResponse{
			ID:        "resp_" + time.Now().Format("20060102150405"),
			Object:    "response",
			CreatedAt: time.Now().Unix(),
			Status:    "failed",
			Model:     claudeResp.Model,
			Output:    []CodexOutputItem{},
			Error: &CodexError{
				Type:    claudeResp.Error.Type,
				Message: message,
			},
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
		switch {
		case block.Type == "text":
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
		case block.Type == "thinking":
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
		case isClaudeToolUseBlock(block):
			if block.ID != "" && block.Name != "" {
				argsStr := string(block.Input)
				// Normalize blank or JSON null input to "{}" like the request
				// conversion (convertClaudeMessageToCodexFormatWithToolMap), so
				// Codex clients always receive valid JSON arguments.
				if trimmed := strings.TrimSpace(argsStr); trimmed == "" || trimmed == "null" {
					argsStr = "{}"
				}
				resp.Output = append(resp.Output, codexOutputItemFromChatToolCall(block.ID, block.Name, argsStr, toolCtx))
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
			execution := spec.Execution
			if execution == "" {
				execution = "client"
			}
			return CodexOutputItem{
				Type:      "tool_search_call",
				ID:        "tsc_" + strings.TrimPrefix(callID, "call_"),
				Status:    "completed",
				CallID:    callID,
				Execution: execution,
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
	if err := decodeCodexJSONUseNumber([]byte(arguments), &parsed); err != nil {
		return arguments
	}
	if input, ok := parsed["input"]; ok {
		return input
	}
	return arguments
}

func codexCustomToolInputString(input any) string {
	switch v := input.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.RawMessage:
		return string(v)
	case []byte:
		return string(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

// codexToolArgumentsRawMessage converts Codex tool arguments into a Claude
// tool_use input. Blank or invalid arguments become an empty object; JSON null
// keeps the existing "arguments:null means no arguments" empty-object
// semantics (see CodexOutputItem.UnmarshalJSON). Valid non-object payloads
// (arrays and scalars) are preserved verbatim because the protocol converter
// must not reinterpret or rewrite provider-specific tool payloads (see
// cleanToolCallArguments). Per AI review, Anthropic requires tool_use.input to
// be a JSON object; wrapping arrays/scalars under {"input": ...} was rejected
// because existing tests lock the verbatim passthrough contract for non-object
// payloads (TestProtocolToolCompatPreservesNonObjectToolPayloads and
// TestConvertCodexRequestToClaudeDefaultsInvalidToolArguments).
func codexToolArgumentsRawMessage(arguments string) json.RawMessage {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return json.RawMessage(`{}`)
	}

	var parsed any
	if err := decodeCodexJSONUseNumber([]byte(arguments), &parsed); err != nil {
		return json.RawMessage(`{}`)
	}
	if parsed == nil {
		// JSON null means no arguments; keep the existing empty-object semantics.
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(arguments)
}

func codexReasoningText(item map[string]any) string {
	var parts []string
	if summary, ok := item["summary"].([]any); ok {
		for _, raw := range summary {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if text := stringFromMap(part, "text"); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		if text := codexContentText(item["content"], "assistant"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
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
	if err := decodeCodexJSONUseNumber(input, &raw); err != nil {
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
	var pendingReasoning []string
	takeReasoning := func() *string {
		if len(pendingReasoning) == 0 {
			return nil
		}
		text := strings.Join(pendingReasoning, "")
		pendingReasoning = nil
		return &text
	}
	discardReasoning := func() {
		pendingReasoning = nil
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		switch {
		case itemType == "reasoning":
			if text := codexReasoningText(m); text != "" {
				pendingReasoning = append(pendingReasoning, text)
			}
		case itemType == "message" || itemType == "":
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
			if role != "assistant" {
				discardReasoning()
			}
			message := OpenAIMessage{Role: role, Content: marshalStringAsJSONRaw("codex_message", text)}
			if role == "assistant" {
				message.ReasoningContent = takeReasoning()
			}
			messages = append(messages, message)
		case codexToolCallItemType(itemType):
			callID := stringFromMap(m, "call_id")
			if callID == "" {
				callID = stringFromMap(m, "id")
			}
			name := stringFromMap(m, "name")
			arguments := codexToolCallArguments(m, itemType)
			if itemType == "custom_tool_call" {
				if inputValue, ok := m["input"]; ok && inputValue != nil {
					inputBytes, _ := json.Marshal(map[string]any{"input": inputValue})
					arguments = string(inputBytes)
				}
			} else if itemType == "tool_search_call" {
				name = codexToolSearchProxyName
			} else if len(toolCtx) > 0 && toolCtx[0] != nil {
				name = toolCtx[0].chatNameFor(name, stringFromMap(m, "namespace"))
			}
			if arguments == "" {
				arguments = "{}"
			}
			if callID == "" || name == "" {
				continue
			}
			messages = append(messages, OpenAIMessage{
				Role:             "assistant",
				ReasoningContent: takeReasoning(),
				ToolCalls: []OpenAIToolCall{{
					ID:   callID,
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      name,
						Arguments: arguments,
					},
				}},
			})
		case codexToolOutputItemType(itemType):
			callID := stringFromMap(m, "call_id")
			if callID == "" {
				continue
			}
			discardReasoning()
			output := codexToolOutputText(m, itemType)
			messages = append(messages, OpenAIMessage{
				Role:       "tool",
				ToolCallID: callID,
				Content:    marshalStringAsJSONRaw("codex_tool_output", output),
			})
		}
	}
	discardReasoning()
	return messages, nil
}

func convertCodexInputToClaudeMessages(input json.RawMessage, toolCtx ...*codexToolContext) ([]ClaudeMessage, error) {
	var raw any
	if err := decodeCodexJSONUseNumber(input, &raw); err != nil {
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
	var pendingThinking []ClaudeContentBlock
	takeThinking := func() []ClaudeContentBlock {
		thinking := pendingThinking
		pendingThinking = nil
		return thinking
	}
	discardThinking := func() {
		pendingThinking = nil
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		switch {
		case itemType == "reasoning":
			if text := codexReasoningText(m); text != "" {
				pendingThinking = append(pendingThinking, ClaudeContentBlock{Type: "thinking", Thinking: text})
			}
		case itemType == "message" || itemType == "":
			role, _ := m["role"].(string)
			if role == "developer" || role == "system" {
				continue
			}
			if role == "" {
				role = "user"
			}
			text := codexContentText(m["content"], role)
			if text == "" {
				continue
			}
			if role != "assistant" {
				discardThinking()
			}
			blocks := takeThinking()
			blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: text})
			content, _ := json.Marshal(blocks)
			messages = append(messages, ClaudeMessage{Role: role, Content: content})
		case codexToolCallItemType(itemType):
			callID := stringFromMap(m, "call_id")
			if callID == "" {
				callID = stringFromMap(m, "id")
			}
			name := stringFromMap(m, "name")
			arguments := codexToolCallArguments(m, itemType)
			if itemType == "custom_tool_call" {
				if inputValue, ok := m["input"]; ok && inputValue != nil {
					inputBytes, _ := json.Marshal(map[string]any{"input": inputValue})
					arguments = string(inputBytes)
				}
			} else if itemType == "tool_search_call" {
				name = codexToolSearchProxyName
			} else if len(toolCtx) > 0 && toolCtx[0] != nil {
				name = toolCtx[0].chatNameFor(name, stringFromMap(m, "namespace"))
			}
			if callID == "" || name == "" {
				continue
			}
			blocks := takeThinking()
			blocks = append(blocks, ClaudeContentBlock{
				Type:  "tool_use",
				ID:    callID,
				Name:  name,
				Input: codexToolArgumentsRawMessage(arguments),
			})
			content, _ := json.Marshal(blocks)
			messages = append(messages, ClaudeMessage{Role: "assistant", Content: content})
		case codexToolOutputItemType(itemType):
			callID := stringFromMap(m, "call_id")
			if callID == "" {
				continue
			}
			discardThinking()
			content, _ := json.Marshal([]ClaudeContentBlock{{
				Type:      "tool_result",
				ToolUseID: callID,
				Content:   codexToolOutputRawMessage(m, itemType),
			}})
			messages = append(messages, ClaudeMessage{Role: "user", Content: content})
		}
	}
	discardThinking()
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
	return stringFromValue(v)
}

func stringFromValue(v any) string {
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

func codexToolOutputText(item map[string]any, itemType string) string {
	value := codexToolOutputValue(item, itemType)
	if text, ok := value.(string); ok {
		return text
	}
	out, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(out)
}

func codexToolOutputRawMessage(item map[string]any, itemType string) json.RawMessage {
	out, err := json.Marshal(codexToolOutputValue(item, itemType))
	if err != nil {
		return json.RawMessage(`""`)
	}
	return out
}

func codexToolOutputValue(item map[string]any, itemType string) any {
	if itemType == "tool_search_output" {
		if tools, ok := item["tools"]; ok {
			return tools
		}
	}
	if output, ok := item["output"]; ok {
		return output
	}
	return ""
}

func appendCodexToolToOpenAIChat(tools *[]OpenAITool, tool CodexTool, namespace string) {
	appendCodexToolToOpenAIChatWithDescription(tools, tool, namespace, "")
}

func appendCodexToolToOpenAIChatWithDescription(tools *[]OpenAITool, tool CodexTool, namespace, namespaceDescription string) {
	switch tool.Type {
	case "", "function":
		name := codexChatToolName(codexToolName(tool), namespace)
		if name == "" {
			return
		}
		*tools = append(*tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        name,
				Description: codexNamespacedDescription(namespaceDescription, tool.Description),
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
				Description: codexNamespacedDescription(namespaceDescription, codexCustomToolDescription(tool)),
				Parameters:  codexCustomToolParameters(),
			},
		})
	case "tool_search":
		description := tool.Description
		parameters := tool.Parameters
		if description == "" {
			description = "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task."
		}
		if len(parameters) == 0 || string(parameters) == "null" {
			parameters = codexToolSearchParameters()
		}
		*tools = append(*tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        codexToolSearchProxyName,
				Description: codexNamespacedDescription(namespaceDescription, description),
				Parameters:  normalizeToolParameters(parameters),
			},
		})
	case "namespace":
		nextNamespace := codexChatToolName(tool.Name, namespace)
		nextDescription := codexNamespacedDescription(namespaceDescription, tool.Description)
		for _, child := range codexNamespaceChildren(tool) {
			appendCodexToolToOpenAIChatWithDescription(tools, child, nextNamespace, nextDescription)
		}
	default:
		name := codexChatToolName(codexToolName(tool), namespace)
		if name == "" {
			return
		}
		*tools = append(*tools, OpenAITool{Type: "function", Function: OpenAIFunction{
			Name: name, Description: codexNamespacedDescription(namespaceDescription, tool.Description), Parameters: normalizeToolParameters(tool.Parameters),
		}})
	}
}

func appendCodexToolToClaude(tools *[]ClaudeTool, tool CodexTool, namespace string) {
	appendCodexToolToClaudeWithDescription(tools, tool, namespace, "")
}

func appendCodexToolToClaudeWithDescription(tools *[]ClaudeTool, tool CodexTool, namespace, namespaceDescription string) {
	switch tool.Type {
	case "", "function":
		name := codexChatToolName(codexToolName(tool), namespace)
		if name == "" {
			return
		}
		*tools = append(*tools, ClaudeTool{
			Name:        name,
			Description: codexNamespacedDescription(namespaceDescription, tool.Description),
			InputSchema: normalizeToolParameters(tool.Parameters),
		})
	case "custom":
		if tool.Name == "" {
			return
		}
		*tools = append(*tools, ClaudeTool{
			Name:        tool.Name,
			Description: codexNamespacedDescription(namespaceDescription, codexCustomToolDescription(tool)),
			InputSchema: codexCustomToolParameters(),
		})
	case "tool_search":
		description := tool.Description
		parameters := tool.Parameters
		if description == "" {
			description = "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task."
		}
		if len(parameters) == 0 || string(parameters) == "null" {
			parameters = codexToolSearchParameters()
		}
		*tools = append(*tools, ClaudeTool{
			Name:        codexToolSearchProxyName,
			Description: codexNamespacedDescription(namespaceDescription, description),
			InputSchema: normalizeToolParameters(parameters),
		})
	case "namespace":
		nextNamespace := codexChatToolName(tool.Name, namespace)
		nextDescription := codexNamespacedDescription(namespaceDescription, tool.Description)
		for _, child := range codexNamespaceChildren(tool) {
			appendCodexToolToClaudeWithDescription(tools, child, nextNamespace, nextDescription)
		}
	default:
		name := codexChatToolName(codexToolName(tool), namespace)
		if name == "" {
			return
		}
		*tools = append(*tools, ClaudeTool{
			Name: name, Description: codexNamespacedDescription(namespaceDescription, tool.Description), InputSchema: normalizeToolParameters(tool.Parameters),
		})
	}
}

func codexNamespacedDescription(namespaceDescription, toolDescription string) string {
	if namespaceDescription == "" {
		return toolDescription
	}
	if toolDescription == "" {
		return namespaceDescription
	}
	return namespaceDescription + "\n\n" + toolDescription
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
		if name := responsesToolChoiceName(v, toolCtx...); name != "" {
			return map[string]any{
				"type": "function",
				"function": map[string]string{
					"name": name,
				},
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
		if name := responsesToolChoiceName(v, toolCtx...); name != "" {
			out, _ := json.Marshal(map[string]any{"type": "tool", "name": name})
			return out
		}
	}
	return nil
}

func responsesToolChoiceName(selector map[string]any, toolCtx ...*codexToolContext) string {
	selectorType, _ := selector["type"].(string)
	if selectorType == "tool_search" {
		return codexToolSearchProxyName
	}
	name, _ := selector["name"].(string)
	if name == "" || len(toolCtx) == 0 || toolCtx[0] == nil {
		return name
	}
	namespace, _ := selector["namespace"].(string)
	return toolCtx[0].chatNameFor(name, namespace)
}

func codexClaudeToolChoiceWithParallel(toolChoice json.RawMessage, parallel *bool, hasTools bool) (json.RawMessage, error) {
	if parallel == nil || !hasTools {
		return toolChoice, nil
	}
	choice := map[string]any{"type": "auto"}
	if len(toolChoice) > 0 {
		var decoded map[string]any
		if err := decodeCodexJSONUseNumber(toolChoice, &decoded); err != nil || decoded == nil {
			return nil, unsupportedCodexRequestOption("tool_choice", codexUpstreamClaude, "tool_choice must be an object when parallel_tool_calls is set")
		}
		choice = decoded
	}
	if choiceType, _ := choice["type"].(string); choiceType == "none" {
		return toolChoice, nil
	}
	choice["disable_parallel_tool_use"] = !*parallel
	return json.Marshal(choice)
}

type codexProtocolCompatibilityError struct {
	Code     string
	ToolType string
	Feature  string
	Target   string
	Detail   string
}

func (e *codexProtocolCompatibilityError) Error() string {
	if e.Feature != "" {
		return fmt.Sprintf("%s: Not Supported: Codex request option %q cannot be converted to %s: %s", e.Code, e.Feature, e.Target, e.Detail)
	}
	return fmt.Sprintf("%s: Not Supported: Codex tool %q cannot be converted to %s: %s", e.Code, e.ToolType, e.Target, e.Detail)
}

func validateForceCodexTools(tools []CodexTool, target string) error {
	for _, tool := range tools {
		toolType := tool.Type
		if toolType == "" {
			toolType = "function"
		}
		switch toolType {
		case "function":
			if strings.TrimSpace(tool.Name) == "" {
				return unsupportedCodexTool(toolType, target, "name is required for reversible conversion")
			}
			if target == codexUpstreamOpenAIChat && tool.DeferLoading != nil && *tool.DeferLoading {
				return unsupportedCodexTool(toolType, target, "defer_loading has no Chat Completions equivalent")
			}
		case "custom":
			if strings.TrimSpace(tool.Name) == "" {
				return unsupportedCodexTool(toolType, target, "name is required for reversible conversion")
			}
		case "namespace":
			if err := validateForceCodexTools(codexNamespaceChildren(tool), target); err != nil {
				return err
			}
		case "tool_search":
			if tool.Execution != "" && tool.Execution != "client" {
				return unsupportedCodexTool(toolType, target, "only client execution is reversible")
			}
			// Missing description and parameters use the shared conversion defaults.
		default:
			// Unknown tool kinds are transported as ordinary function tools. The
			// gateway does not execute or interpret them, so there is no tool-name
			// allowlist to update when a provider adds a new kind.
			if strings.TrimSpace(codexToolName(tool)) == "" {
				return unsupportedCodexTool(toolType, target, "name is required for function-shell conversion")
			}
		}
	}
	return nil
}

func unsupportedCodexTool(toolType, target, detail string) error {
	return &codexProtocolCompatibilityError{
		Code: "unsupported_tool", ToolType: toolType, Target: target, Detail: detail,
	}
}

func unsupportedCodexRequestOption(feature, target, detail string) error {
	return &codexProtocolCompatibilityError{
		Code: "unsupported_request_option", Feature: feature, Target: target, Detail: detail,
	}
}

func validateForceCodexRequestOptions(req *CodexRequest, target string) error {
	if req == nil {
		return fmt.Errorf("codex request is nil")
	}
	if len(req.unsupportedFields) > 0 {
		return unsupportedCodexRequestOption(req.unsupportedFields[0], target, "request field is not modeled by the target protocol converter")
	}
	if err := validateForceCodexToolChoice(req.ToolChoice, target); err != nil {
		return err
	}
	if req.Text != nil {
		verbosity := strings.ToLower(strings.TrimSpace(req.Text.Verbosity))
		switch verbosity {
		case "", "medium":
		case "low", "high":
			if target == codexUpstreamClaude {
				return unsupportedCodexRequestOption("text.verbosity", target, "Anthropic Messages has no equivalent response verbosity control")
			}
		default:
			return unsupportedCodexRequestOption("text.verbosity", target, "expected low, medium, or high")
		}
	}
	if req.Reasoning != nil {
		context := strings.ToLower(strings.TrimSpace(req.Reasoning.Context))
		switch context {
		case "", "auto", "current_turn":
		case "all_turns":
			return unsupportedCodexRequestOption("reasoning.context", target, "all_turns state cannot be represented after protocol conversion")
		default:
			return unsupportedCodexRequestOption("reasoning.context", target, "expected auto, current_turn, or all_turns")
		}
	}
	return nil
}

func validateForceCodexToolChoice(toolChoice any, target string) error {
	switch value := toolChoice.(type) {
	case nil:
		return nil
	case string:
		switch value {
		case "none", "auto", "required":
			return nil
		default:
			return unsupportedCodexRequestOption("tool_choice", target, "expected none, auto, required, or a supported named tool selector")
		}
	case map[string]any:
		selectorType, _ := value["type"].(string)
		switch selectorType {
		case "function", "custom":
			if strings.TrimSpace(stringFromMap(value, "name")) == "" {
				return unsupportedCodexRequestOption("tool_choice", target, selectorType+" selector requires name")
			}
			return nil
		case "tool_search":
			return nil
		default:
			if strings.TrimSpace(stringFromMap(value, "name")) != "" {
				return nil
			}
			return unsupportedCodexRequestOption("tool_choice", target, "selector requires a named tool for function-shell conversion")
		}
	default:
		return unsupportedCodexRequestOption("tool_choice", target, "tool_choice must be a string or object")
	}
}

func codexRawJSONPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func codexTextFormatForTarget(raw json.RawMessage, target string) (map[string]any, error) {
	if !codexRawJSONPresent(raw) {
		return nil, nil
	}
	var format map[string]any
	if err := decodeCodexJSONUseNumber(raw, &format); err != nil || format == nil {
		return nil, unsupportedCodexRequestOption("text.format", target, "format must be a JSON object")
	}
	formatType, _ := format["type"].(string)
	if formatType == "text" {
		return nil, nil
	}
	if formatType != "json_schema" {
		return nil, unsupportedCodexRequestOption("text.format", target, "only json_schema has an equivalent output contract")
	}
	schema, ok := format["schema"]
	if !ok || schema == nil {
		return nil, unsupportedCodexRequestOption("text.format", target, "json_schema requires schema")
	}
	if target == codexUpstreamClaude {
		return map[string]any{
			"format": map[string]any{"type": "json_schema", "schema": schema},
		}, nil
	}
	name, _ := format["name"].(string)
	if strings.TrimSpace(name) == "" {
		return nil, unsupportedCodexRequestOption("text.format", target, "Chat Completions json_schema requires name")
	}
	strict := false
	if rawStrict, ok := format["strict"]; ok {
		var valid bool
		strict, valid = rawStrict.(bool)
		if !valid {
			return nil, unsupportedCodexRequestOption("text.format", target, "strict must be a boolean")
		}
	}
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": name, "strict": strict, "schema": schema,
		},
	}, nil
}

func codexChatStreamOptions(raw json.RawMessage) (map[string]any, error) {
	if !codexRawJSONPresent(raw) {
		return nil, nil
	}
	var source map[string]any
	if err := decodeCodexJSONUseNumber(raw, &source); err != nil || source == nil {
		return nil, unsupportedCodexRequestOption("stream_options", codexUpstreamOpenAIChat, "stream_options must be a JSON object")
	}
	result := make(map[string]any, 1)
	if includeUsage, ok := source["include_usage"]; ok {
		value, valid := includeUsage.(bool)
		if !valid {
			return nil, unsupportedCodexRequestOption("stream_options.include_usage", codexUpstreamOpenAIChat, "include_usage must be a boolean")
		}
		result["include_usage"] = value
	}
	return result, nil
}

type codexConvertedToolFields struct {
	Strict       *bool
	DeferLoading *bool
}

func flattenCodexConvertedToolFields(tools []CodexTool) []codexConvertedToolFields {
	var fields []codexConvertedToolFields
	for _, tool := range tools {
		if tool.Type == "namespace" {
			fields = append(fields, flattenCodexConvertedToolFields(codexNamespaceChildren(tool))...)
			continue
		}
		fields = append(fields, codexConvertedToolFields{Strict: tool.Strict, DeferLoading: tool.DeferLoading})
	}
	return fields
}

func marshalForceCodexOpenAIChatRequest(req *OpenAIRequest, sourceTools []CodexTool, source *CodexRequest) ([]byte, error) {
	out, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	var payload map[string]any
	if err := decodeCodexJSONUseNumber(out, &payload); err != nil {
		return nil, err
	}
	if len(req.Tools) > 0 {
		tools, ok := payload["tools"].([]any)
		fields := flattenCodexConvertedToolFields(sourceTools)
		if !ok || len(tools) != len(fields) {
			return nil, fmt.Errorf("Codex/OpenAI tool conversion count mismatch")
		}
		for i, field := range fields {
			if field.Strict == nil {
				continue
			}
			tool, ok := tools[i].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid converted OpenAI tool at index %d", i)
			}
			function, ok := tool["function"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid converted OpenAI function at index %d", i)
			}
			function["strict"] = *field.Strict
		}
	}
	if source != nil {
		if source.ServiceTier != "" {
			payload["service_tier"] = source.ServiceTier
		}
		if source.PromptCacheKey != "" {
			payload["prompt_cache_key"] = source.PromptCacheKey
		}
		streamOptions, err := codexChatStreamOptions(source.StreamOptions)
		if err != nil {
			return nil, err
		}
		if len(streamOptions) > 0 {
			payload["stream_options"] = streamOptions
		}
		if source.Text != nil {
			if verbosity := strings.TrimSpace(source.Text.Verbosity); verbosity != "" {
				payload["verbosity"] = strings.ToLower(verbosity)
			}
			responseFormat, err := codexTextFormatForTarget(source.Text.Format, codexUpstreamOpenAIChat)
			if err != nil {
				return nil, err
			}
			if responseFormat != nil {
				payload["response_format"] = responseFormat
			}
		}
	}
	return json.Marshal(payload)
}

func marshalForceCodexClaudeRequest(req *ClaudeRequest, sourceTools []CodexTool, source *CodexRequest) ([]byte, error) {
	out, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	var payload map[string]any
	if err := decodeCodexJSONUseNumber(out, &payload); err != nil {
		return nil, err
	}
	if len(req.Tools) > 0 {
		tools, ok := payload["tools"].([]any)
		fields := flattenCodexConvertedToolFields(sourceTools)
		if !ok || len(tools) != len(fields) {
			return nil, fmt.Errorf("Codex/Anthropic tool conversion count mismatch")
		}
		for i, field := range fields {
			tool, ok := tools[i].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid converted Anthropic tool at index %d", i)
			}
			if field.Strict != nil {
				tool["strict"] = *field.Strict
			}
			if field.DeferLoading != nil {
				tool["defer_loading"] = *field.DeferLoading
			}
		}
	}
	// Responses-only routing hints such as service_tier and prompt_cache_key
	// have no Anthropic wire equivalent. Claude conversion rebuilds the request
	// from modeled Anthropic fields and omits them instead of rejecting it.
	if source != nil && source.Text != nil {
		outputConfig, err := codexTextFormatForTarget(source.Text.Format, codexUpstreamClaude)
		if err != nil {
			return nil, err
		}
		if outputConfig != nil {
			current, _ := payload["output_config"].(map[string]any)
			if current == nil {
				current = make(map[string]any, len(outputConfig))
			}
			for key, value := range outputConfig {
				current[key] = value
			}
			payload["output_config"] = current
		}
	}
	return json.Marshal(payload)
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
	requestTools, err := codexRequestTools(&codexReq)
	if err != nil {
		return bodyBytes, false, err
	}
	toolCtx := newCodexToolContext(requestTools)
	c.Set(ctxKeyCodexToolContext, toolCtx)

	switch group.ChannelType {
	case "openai":
		if err := validateForceCodexRequestOptions(&codexReq, codexUpstreamOpenAIChat); err != nil {
			return bodyBytes, false, err
		}
		if err := validateForceCodexTools(requestTools, codexUpstreamOpenAIChat); err != nil {
			return bodyBytes, false, err
		}
		chatReq, err := convertCodexRequestToOpenAIChat(&codexReq)
		if err != nil {
			return bodyBytes, false, err
		}
		out, err := marshalForceCodexOpenAIChatRequest(chatReq, requestTools, &codexReq)
		if err != nil {
			return bodyBytes, false, err
		}
		c.Set(ctxKeyCodexEnabled, true)
		setCodexUpstreamFormat(c, codexUpstreamOpenAIChat)
		return out, true, nil
	case "anthropic":
		if err := validateForceCodexRequestOptions(&codexReq, codexUpstreamClaude); err != nil {
			return bodyBytes, false, err
		}
		if err := validateForceCodexTools(requestTools, codexUpstreamClaude); err != nil {
			return bodyBytes, false, err
		}
		claudeReq, err := convertCodexRequestToClaude(&codexReq)
		if err != nil {
			return bodyBytes, false, err
		}
		out, err := marshalForceCodexClaudeRequest(claudeReq, requestTools, &codexReq)
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

func captureForceCodexLogicalFailure(c *gin.Context, response *CodexResponse) {
	if c == nil {
		return
	}
	if response == nil {
		c.Set(ctxKeyResponsesStatusUnverified, true)
		return
	}
	errorCode := ""
	errorMessage := ""
	if response.Error != nil {
		errorCode = response.Error.Code
		if errorCode == "" {
			errorCode = response.Error.Type
		}
		errorMessage = response.Error.Message
	}
	setResponsesLogicalFailure(c, response.Status, errorCode, errorMessage)
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

	captureForceCodexLogicalFailure(c, codexResp)
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
	streamResp, statusCode, conversionErr := ps.convertForceCodexCollectedStream(c, resp.StatusCode, format, bodyBytes)
	if conversionErr != nil {
		markResponseProcessingFailed(c)
		logrus.WithError(conversionErr).Warn("Force Codex: invalid or incomplete upstream stream")
		writeForceCodexGatewayError(c, "Invalid or incomplete upstream streaming response")
		return
	}
	captureForceCodexLogicalFailure(c, streamResp)
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
		markResponseProcessingFailed(c)
		logrus.WithError(err).Warn("Force Codex: failed to write collected SSE stream")
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

	terminalEvent := "response.completed"
	switch streamResp.Status {
	case "failed":
		terminalEvent = "response.failed"
	case "incomplete":
		terminalEvent = "response.incomplete"
	case "cancelled", "canceled":
		terminalEvent = "response.cancelled"
	}
	if err := writeEvent(terminalEvent, map[string]any{
		"type":     terminalEvent,
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
	case "custom_tool_call":
		addedItem.Input = ""
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
	case "custom_tool_call":
		input := codexCustomToolInputString(item.Input)
		if input != "" {
			if err := writeEvent("response.custom_tool_call_input.delta", map[string]any{
				"type":         "response.custom_tool_call_input.delta",
				"item_id":      item.ID,
				"output_index": outputIndex,
				"delta":        input,
			}); err != nil {
				return err
			}
		}
		if err := writeEvent("response.custom_tool_call_input.done", map[string]any{
			"type":         "response.custom_tool_call_input.done",
			"item_id":      item.ID,
			"output_index": outputIndex,
			"input":        input,
		}); err != nil {
			return err
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

func (ps *ProxyServer) convertForceCodexCollectedStream(c *gin.Context, statusCode int, format string, bodyBytes []byte) (*CodexResponse, int, error) {
	if statusCode >= http.StatusBadRequest {
		return rawCodexErrorResponse(statusCode, bodyBytes), statusCode, nil
	}
	payloads, err := extractSSEDataPayloads(bodyBytes)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("parse upstream SSE: %w", err)
	}

	switch format {
	case codexUpstreamOpenAIChat:
		openaiResp, terminalSeen, err := collectOpenAIChatStreamPayloads(payloads)
		if err != nil {
			return nil, http.StatusBadGateway, err
		}
		if !terminalSeen {
			return nil, http.StatusBadGateway, fmt.Errorf("OpenAI stream ended without a terminal signal")
		}
		return convertOpenAIChatToCodexResponse(openaiResp, functionCallTriggerSignal(c), codexToolContextFromGin(c)), statusCode, nil
	case codexUpstreamClaude:
		claudeResp, terminalSeen, err := collectClaudeStreamPayloads(payloads)
		if err != nil {
			return nil, http.StatusBadGateway, err
		}
		if !terminalSeen {
			return nil, http.StatusBadGateway, fmt.Errorf("Anthropic stream ended without message_stop")
		}
		return convertClaudeToCodexResponse(claudeResp, codexToolContextFromGin(c)), statusCode, nil
	default:
		return rawCodexErrorResponse(http.StatusBadGateway, []byte("unsupported Codex stream conversion")), http.StatusBadGateway, nil
	}
}

func collectOpenAIChatStreamToResponse(bodyBytes []byte) *OpenAIResponse {
	payloads, _ := extractSSEDataPayloads(bodyBytes)
	resp, _, _ := collectOpenAIChatStreamPayloads(payloads)
	return resp
}

func collectOpenAIChatStreamPayloads(payloads []string) (*OpenAIResponse, bool, error) {
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
	terminalSeen := false
	for _, data := range payloads {
		if data == "[DONE]" {
			terminalSeen = true
			break
		}
		var chunk OpenAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return resp, false, fmt.Errorf("invalid OpenAI stream payload: %w", err)
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
		if chunk.Error != nil {
			resp.Error = chunk.Error
			terminalSeen = true
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if choiceFinishReason := chunk.Choices[0].FinishReason; choiceFinishReason != nil && strings.TrimSpace(*choiceFinishReason) != "" {
			finishReason = *choiceFinishReason
			terminalSeen = true
		}
		if chunk.Choices[0].Delta == nil {
			continue
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
	return resp, terminalSeen, nil
}

func collectClaudeStreamToResponse(bodyBytes []byte) *ClaudeResponse {
	payloads, _ := extractSSEDataPayloads(bodyBytes)
	resp, _, _ := collectClaudeStreamPayloads(payloads)
	return resp
}

func collectClaudeStreamPayloads(payloads []string) (*ClaudeResponse, bool, error) {
	resp := &ClaudeResponse{
		ID:      "msg_" + utils.GenerateRandomSuffix(),
		Type:    "message",
		Role:    "assistant",
		Content: make([]ClaudeContentBlock, 0),
		Usage:   &ClaudeUsage{},
	}
	blocks := make(map[int]*ClaudeContentBlock)
	terminalSeen := false
	for _, data := range payloads {
		if data == "[DONE]" {
			continue
		}
		var event ClaudeStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return resp, false, fmt.Errorf("invalid Anthropic stream payload: %w", err)
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
		case "error":
			resp.Type = "error"
			terminalSeen = true
			if event.Error != nil {
				resp.Error = event.Error
			} else {
				resp.Error = &ClaudeError{Type: "api_error", Message: "upstream stream error"}
			}
		case "message_stop":
			terminalSeen = true
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
	return resp, terminalSeen, nil
}

func extractSSEDataPayloads(bodyBytes []byte) ([]string, error) {
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
	if err := scanner.Err(); err != nil {
		return payloads, err
	}
	return payloads, nil
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
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		c.Set(ctxKeyResponsesStatusUnverified, true)
	}
	if shouldCaptureResponse(c) {
		c.Set("response_body", sanitizeAndTruncateBytesForLog(body, maxResponseCaptureBytes))
	}
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}
