package proxy

import (
	"bytes"
	"encoding/json"
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProtocolToolCompatReadsAdditionalAndToolSearchTools(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"namespace","name":"mail","description":"Mail tools","tools":[
					{"type":"function","name":"send","description":"Send mail","parameters":{"type":"object","properties":{}}}
				]}
			]},
			{"type":"tool_search_output","call_id":"call_search","status":"completed","execution":"client","tools":[
				{"type":"function","name":"big_id","description":"Use an exact ID","parameters":{"type":"object","properties":{"id":{"type":"integer","maximum":9007199254740993}}}}
			]}
		]`),
		Tools: []CodexTool{{
			Type: "tool_search", Description: "Find deferred tools exactly",
			Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		}},
	}

	got, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	require.Len(t, got.Tools, 3)
	assert.Equal(t, "Find deferred tools exactly", got.Tools[0].Function.Description)
	assert.JSONEq(t, string(req.Tools[0].Parameters), string(got.Tools[0].Function.Parameters))
	assert.Equal(t, "mail__send", got.Tools[1].Function.Name)
	assert.Contains(t, got.Tools[1].Function.Description, "Mail tools")
	assert.Equal(t, "big_id", got.Tools[2].Function.Name)
	require.Len(t, got.Messages, 1)
	var output string
	require.NoError(t, json.Unmarshal(got.Messages[0].Content, &output))
	assert.Contains(t, output, "9007199254740993")
	assert.Contains(t, output, `"name":"big_id"`)
}

func TestProtocolToolCompatDefaultsToolSearchExecutionToClient(t *testing.T) {
	body := []byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"tool_search"}]}`)
	var req CodexRequest
	require.NoError(t, json.Unmarshal(body, &req))
	item := codexOutputItemFromChatToolCall("call_search", codexToolSearchProxyName, `{}`, newCodexToolContext(req.Tools))
	require.Equal(t, "client", item.Execution)

	for _, channelType := range []string{"openai", "anthropic"} {
		t.Run(channelType, func(t *testing.T) {
			out := applyForceCodexCompat(t, channelType, body)
			tool := decodeCompatObject(t, out)["tools"].([]any)[0].(map[string]any)
			schemaKey := "input_schema"
			if channelType == "openai" {
				tool = tool["function"].(map[string]any)
				schemaKey = "parameters"
			}
			require.NotEmpty(t, tool["description"])
			schema := tool[schemaKey].(map[string]any)
			require.Equal(t, "object", schema["type"])
			require.Contains(t, schema["properties"].(map[string]any), "query")
		})
	}
}

func TestProtocolToolCompatDeduplicatesDiscoveredToolsAfterExplicitTools(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Tools: []CodexTool{{
			Type: "function", Name: "lookup", Description: "explicit definition",
			Parameters: json.RawMessage(`{"type":"object"}`),
		}},
		Input: json.RawMessage(`[
			{"type":"additional_tools","tools":[{"type":"function","name":"lookup","description":"discovered definition","parameters":{"type":"object"}}]},
			{"type":"tool_search_output","tools":[{"type":"function","name":"lookup","description":"replayed definition","parameters":{"type":"object"}}]}
		]`),
	}

	got, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	require.Len(t, got.Tools, 1)
	assert.Equal(t, "lookup", got.Tools[0].Function.Name)
	assert.Equal(t, "explicit definition", got.Tools[0].Function.Description)
}

func TestProtocolToolCompatPreservesStrictAcrossSupportedUpstreams(t *testing.T) {
	body := []byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"function","name":"lookup","description":"Lookup","strict":true,"parameters":{"type":"object","properties":{"id":{"type":"integer","maximum":9007199254740993}},"required":["id"],"additionalProperties":false}}]}`)
	for _, channelType := range []string{"openai", "anthropic"} {
		t.Run(channelType, func(t *testing.T) {
			out := applyForceCodexCompat(t, channelType, body)
			payload := decodeCompatObject(t, out)
			tools := payload["tools"].([]any)
			tool := tools[0].(map[string]any)
			schemaKey := "input_schema"
			if channelType == "openai" {
				tool = tool["function"].(map[string]any)
				schemaKey = "parameters"
			}
			assert.Equal(t, true, tool["strict"])
			parameters := tool[schemaKey].(map[string]any)
			maximum := parameters["properties"].(map[string]any)["id"].(map[string]any)["maximum"].(json.Number)
			assert.Equal(t, "9007199254740993", maximum.String())
		})
	}
}

func TestProtocolToolCompatPreservesAnthropicDeferredLoading(t *testing.T) {
	body := []byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"function","name":"lookup","defer_loading":true,"parameters":{"type":"object","properties":{}}}]}`)
	out := applyForceCodexCompat(t, "anthropic", body)
	tool := decodeCompatObject(t, out)["tools"].([]any)[0].(map[string]any)
	assert.Equal(t, true, tool["defer_loading"])
}

func TestProtocolToolCompatConvertsCustomTools(t *testing.T) {
	body := []byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"custom","name":"apply_patch","description":"Apply a patch","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}]}`)
	for _, channelType := range []string{"openai", "anthropic"} {
		t.Run(channelType, func(t *testing.T) {
			out := applyForceCodexCompat(t, channelType, body)
			tool := decodeCompatObject(t, out)["tools"].([]any)[0].(map[string]any)
			if channelType == "openai" {
				tool = tool["function"].(map[string]any)
			}
			assert.Equal(t, "apply_patch", tool["name"])
			schemaKey := "parameters"
			if channelType == "anthropic" {
				schemaKey = "input_schema"
			}
			schema := tool[schemaKey].(map[string]any)
			properties := schema["properties"].(map[string]any)
			assert.Contains(t, properties, "input")
		})
	}
}

func TestProtocolToolCompatConvertsUnknownCodexToolsThroughFunctionShell(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Tools: []CodexTool{
			{Type: "web_search", Name: "web_search", Description: "Search", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
			{Type: "future_tool_2026", Name: "future_lookup", Description: "Future", Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`)},
		},
		Input: json.RawMessage(`[
			{"type":"web_search_call","call_id":"call_web","name":"web_search","arguments":"{\"query\":\"go\"}"},
			{"type":"future_tool_call","call_id":"call_future","name":"future_lookup","arguments":"{\"id\":9007199254740993}"},
			{"type":"web_search_call_output","call_id":"call_web","output":"result"}
		]`),
	}

	chat, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	require.Len(t, chat.Tools, 2)
	assert.Equal(t, "web_search", chat.Tools[0].Function.Name)
	assert.Equal(t, "future_lookup", chat.Tools[1].Function.Name)
	require.Len(t, chat.Messages, 3)
	assert.Equal(t, "web_search", chat.Messages[0].ToolCalls[0].Function.Name)
	assert.Equal(t, `{"id":9007199254740993}`, chat.Messages[1].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "call_web", chat.Messages[2].ToolCallID)

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, claude.Tools, 2)
	assert.Equal(t, "web_search", claude.Tools[0].Name)
	assert.Equal(t, "future_lookup", claude.Tools[1].Name)
	require.Len(t, claude.Messages, 2)
	assert.JSONEq(t, `[
		{"type":"tool_use","id":"call_web","name":"web_search","input":{"query":"go"}},
		{"type":"tool_use","id":"call_future","name":"future_lookup","input":{"id":9007199254740993}}
	]`, string(claude.Messages[0].Content))
	assert.JSONEq(t, `[{"type":"tool_result","tool_use_id":"call_web","content":"result"}]`, string(claude.Messages[1].Content))
}

func TestProtocolToolCompatAggregatesConsecutiveClaudeToolBlocks(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_a","name":"lookup_a","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"lookup_b","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":"a"},
			{"type":"function_call_output","call_id":"call_b","output":"b"}
		]`),
	}

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, claude.Messages, 2)
	assert.Equal(t, "assistant", claude.Messages[0].Role)
	assert.Equal(t, "user", claude.Messages[1].Role)

	var assistantBlocks []ClaudeContentBlock
	require.NoError(t, json.Unmarshal(claude.Messages[0].Content, &assistantBlocks))
	require.Len(t, assistantBlocks, 2)
	assert.Equal(t, []string{"call_a", "call_b"}, []string{assistantBlocks[0].ID, assistantBlocks[1].ID})

	var userBlocks []ClaudeContentBlock
	require.NoError(t, json.Unmarshal(claude.Messages[1].Content, &userBlocks))
	require.Len(t, userBlocks, 2)
	assert.Equal(t, []string{"call_a", "call_b"}, []string{userBlocks[0].ToolUseID, userBlocks[1].ToolUseID})
}

func TestProtocolToolCompatAggregatesClaudeAssistantTextWithToolUse(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"message","role":"assistant","content":"checking"},
			{"type":"function_call","call_id":"call_a","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":"done"}
		]`),
	}

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, claude.Messages, 2)
	assert.Equal(t, "assistant", claude.Messages[0].Role)
	assert.Equal(t, "user", claude.Messages[1].Role)
	assert.JSONEq(t, `[
		{"type":"text","text":"checking"},
		{"type":"tool_use","id":"call_a","name":"lookup","input":{}}
	]`, string(claude.Messages[0].Content))
}

func TestProtocolToolCompatAggregatesToolOutputWithFollowingUserText(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_lookup","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_lookup","output":"done"},
			{"type":"message","role":"user","content":"and now?"}
		]`),
	}

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, claude.Messages, 2)
	assert.Equal(t, "assistant", claude.Messages[0].Role)
	assert.Equal(t, "user", claude.Messages[1].Role)
	assert.JSONEq(t, `[
		{"type":"tool_result","tool_use_id":"call_lookup","content":"done"},
		{"type":"text","text":"and now?"}
	]`, string(claude.Messages[1].Content))
}

func TestProtocolToolCompatSkipsOrphanedFutureToolOutputs(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"future_tool_output","output":"orphan"},
			{"type":"future_tool_result","call_id":"call_valid","output":{"ok":true}}
		]`),
	}

	chat, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	require.Len(t, chat.Messages, 1)
	assert.Equal(t, "tool", chat.Messages[0].Role)
	assert.Equal(t, "call_valid", chat.Messages[0].ToolCallID)

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, claude.Messages, 1)
	var blocks []ClaudeContentBlock
	require.NoError(t, json.Unmarshal(claude.Messages[0].Content, &blocks))
	require.Len(t, blocks, 1)
	assert.Equal(t, "tool_result", blocks[0].Type)
	assert.Equal(t, "call_valid", blocks[0].ToolUseID)
}

func TestProtocolToolCompatPreservesCodexReasoningHistory(t *testing.T) {
	// Thinking must be explicitly enabled on the request for reasoning
	// summaries to be attached: Anthropic rejects thinking blocks when
	// extended thinking is off. When thinking is not enabled the summaries
	// are omitted (see TestCodexInputToClaudeMessagesGatesThinkingOnRequest).
	req := &CodexRequest{
		Model:     "gpt-test",
		Reasoning: &CodexReasoning{Effort: "adaptive"},
		Input: json.RawMessage(`[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"plan before lookup"}]},
			{"type":"function_call","call_id":"call_lookup","name":"lookup","arguments":"{}"}
		]`),
	}

	chat, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	require.Len(t, chat.Messages, 1)
	require.NotNil(t, chat.Messages[0].ReasoningContent)
	assert.Equal(t, "plan before lookup", *chat.Messages[0].ReasoningContent)
	require.Len(t, chat.Messages[0].ToolCalls, 1)

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, claude.Messages, 1)
	assert.JSONEq(t, `[
		{"type":"thinking","thinking":"plan before lookup"},
		{"type":"tool_use","id":"call_lookup","name":"lookup","input":{}}
	]`, string(claude.Messages[0].Content))
}

func TestProtocolToolCompatNormalizesNullCodexToolArguments(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
	}{
		{
			name:  "function arguments",
			input: json.RawMessage(`[{"type":"function_call","call_id":"call_lookup","name":"lookup","arguments":null}]`),
		},
		{
			name:  "custom input",
			input: json.RawMessage(`[{"type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":null}]`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &CodexRequest{Model: "gpt-test", Input: tt.input}

			chat, err := convertCodexRequestToOpenAIChat(req)
			require.NoError(t, err)
			require.Len(t, chat.Messages, 1)
			assert.Equal(t, "{}", chat.Messages[0].ToolCalls[0].Function.Arguments)

			claude, err := convertCodexRequestToClaude(req)
			require.NoError(t, err)
			require.Len(t, claude.Messages, 1)
			var blocks []ClaudeContentBlock
			require.NoError(t, json.Unmarshal(claude.Messages[0].Content, &blocks))
			require.Len(t, blocks, 1)
			assert.JSONEq(t, `{}`, string(blocks[0].Input))
		})
	}
}

func TestProtocolToolCompatNormalizesNullToolArgumentsForClaude(t *testing.T) {
	// JSON null, blank and invalid arguments normalize to an empty object;
	// valid non-object payloads (arrays/scalars) stay verbatim per the
	// passthrough contract locked by PreservesNonObjectToolPayloads and
	// DefaultsInvalidToolArguments (the Anthropic object-input wrapping from
	// the AI review was rejected there).
	directTests := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "object preserved", arguments: `{"a":1}`, want: `{"a":1}`},
		{name: "array preserved verbatim", arguments: `[1,2]`, want: `[1,2]`},
		{name: "null stays empty object", arguments: `null`, want: `{}`},
		{name: "blank stays empty object", arguments: `   `, want: `{}`},
		{name: "invalid stays empty object", arguments: `not json`, want: `{}`},
	}
	for _, tt := range directTests {
		t.Run("direct/"+tt.name, func(t *testing.T) {
			assert.JSONEq(t, tt.want, string(codexToolArgumentsRawMessage(tt.arguments)))
		})
	}

	// Codex response tool call with null arguments must produce an empty
	// object for the Claude tool_use block.
	got := convertCodexToClaudeResponse(&CodexResponse{
		ID: "resp_test", Status: "completed", Model: "gpt-test",
		Output: []CodexOutputItem{{
			Type: "function_call", CallID: "call_null", Name: "list_items", Arguments: `null`,
		}},
	}, nil)
	require.Len(t, got.Content, 1)
	assert.JSONEq(t, `{}`, string(got.Content[0].Input))
}

func TestProtocolToolCompatNormalizesNullClaudeToolInputToCodex(t *testing.T) {
	// Request conversion: Claude tool_use with blank/null input must produce
	// valid "{}" arguments for Codex, matching the OpenAI conversion.
	req := &ClaudeRequest{
		Model: "gpt-test",
		Messages: []ClaudeMessage{{
			Role: "assistant",
			Content: json.RawMessage(`[
				{"type":"tool_use","id":"call_null","name":"lookup","input":null}
			]`),
		}},
	}
	got, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	var input []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &input))
	require.Len(t, input, 1)
	assert.Equal(t, "{}", input[0]["arguments"])

	// Response conversion: the same normalization applies when a Claude
	// upstream response carries a tool_use block.
	resp := convertClaudeToCodexResponse(&ClaudeResponse{
		ID: "msg_null", Model: "gpt-test",
		Content: []ClaudeContentBlock{{
			Type: "tool_use", ID: "call_null", Name: "lookup", Input: json.RawMessage(`null`),
		}},
	}, nil)
	require.Len(t, resp.Output, 1)
	assert.Equal(t, "{}", resp.Output[0].Arguments)
}

func TestProtocolToolCompatDropsOrphanReasoning(t *testing.T) {
	tests := []struct {
		name      string
		input     json.RawMessage
		wantEmpty bool
	}{
		{
			name: "trailing reasoning",
			input: json.RawMessage(`[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"trailing plan"}]}
		]`),
			wantEmpty: true,
		},
		{
			name: "reasoning before user",
			input: json.RawMessage(`[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"prior plan"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &CodexRequest{Model: "gpt-test", Input: tt.input}

			chat, err := convertCodexRequestToOpenAIChat(req)
			require.NoError(t, err)

			claude, err := convertCodexRequestToClaude(req)
			require.NoError(t, err)

			if tt.wantEmpty {
				assert.Empty(t, chat.Messages)
				assert.Empty(t, claude.Messages)
				return
			}

			require.Len(t, chat.Messages, 1)
			assert.Nil(t, chat.Messages[0].ReasoningContent)
			assert.Equal(t, json.RawMessage(`"continue"`), chat.Messages[0].Content)
			require.Len(t, claude.Messages, 1)
			assert.JSONEq(t, `[{"type":"text","text":"continue"}]`, string(claude.Messages[0].Content))
		})
	}
}

func TestProtocolToolCompatOmitsEmptyCodexEffortOnReverseConversion(t *testing.T) {
	req := &CodexRequest{
		Model:     "gpt-test",
		Reasoning: &CodexReasoning{Summary: "auto"},
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]`),
	}

	chat, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	chatJSON, err := json.Marshal(chat)
	require.NoError(t, err)
	assert.NotContains(t, string(chatJSON), `"reasoning_effort"`)

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	claudeJSON, err := json.Marshal(claude)
	require.NoError(t, err)
	assert.NotContains(t, string(claudeJSON), `"thinking"`)
	assert.NotContains(t, string(claudeJSON), `"effort"`)
}

func TestProtocolToolCompatPreservesNonObjectToolPayloads(t *testing.T) {
	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"future_tool_call","call_id":"call_array","name":"future_lookup","arguments":["a",9007199254740993]},
			{"type":"future_tool_call_output","call_id":"call_array","output":{"path":"F:\\work\\file.txt","ok":true}}
		]`),
	}

	chat, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	require.Len(t, chat.Messages, 2)
	assert.Equal(t, `["a",9007199254740993]`, chat.Messages[0].ToolCalls[0].Function.Arguments)
	chatOutput := decodeCompatObject(t, []byte(rawMessageString(t, chat.Messages[1].Content)))
	assert.Equal(t, `F:\work\file.txt`, chatOutput["path"])
	assert.Equal(t, true, chatOutput["ok"])

	claude, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, claude.Messages, 2)
	var toolUse []struct {
		Input []any `json:"input"`
	}
	decoder := json.NewDecoder(bytes.NewReader(claude.Messages[0].Content))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&toolUse))
	require.Len(t, toolUse, 1)
	assert.Equal(t, "a", toolUse[0].Input[0])
	assert.Equal(t, json.Number("9007199254740993"), toolUse[0].Input[1])
	var toolResult []struct {
		Content map[string]any `json:"content"`
	}
	decoder = json.NewDecoder(bytes.NewReader(claude.Messages[1].Content))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&toolResult))
	require.Len(t, toolResult, 1)
	assert.Equal(t, `F:\work\file.txt`, toolResult[0].Content["path"])
	assert.Equal(t, true, toolResult[0].Content["ok"])
}

func TestProtocolToolCompatDerivesNameForUnknownCodexToolType(t *testing.T) {
	body := []byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"web_search"}]}`)
	for _, channelType := range []string{"openai", "anthropic"} {
		t.Run(channelType, func(t *testing.T) {
			out := applyForceCodexCompat(t, channelType, body)
			tools := decodeCompatObject(t, out)["tools"].([]any)
			tool := tools[0].(map[string]any)
			if channelType == "openai" {
				tool = tool["function"].(map[string]any)
			}
			assert.Equal(t, "web_search", tool["name"])
		})
	}
}

func TestProtocolToolCompatRestoresUnknownClaudeToolUseResponse(t *testing.T) {
	toolCtx := newCodexToolContext([]CodexTool{{Type: "future_tool_2026", Name: "future_lookup"}})
	got := convertClaudeToCodexResponse(&ClaudeResponse{
		ID: "msg_future", Model: "gpt-test",
		Content: []ClaudeContentBlock{{
			Type: "server_tool_use", ID: "call_future", Name: "future_lookup", Input: json.RawMessage(`{"id":9007199254740993}`),
		}},
	}, toolCtx)
	require.Len(t, got.Output, 1)
	assert.Equal(t, "function_call", got.Output[0].Type)
	assert.Equal(t, "call_future", got.Output[0].CallID)
	assert.Equal(t, "future_lookup", got.Output[0].Name)
	assert.Equal(t, `{"id":9007199254740993}`, got.Output[0].Arguments)
}

func TestProtocolToolCompatRejectsUnnamedFunctionsBeforeConversion(t *testing.T) {
	for _, tool := range []string{
		`{"type":"function","parameters":{"type":"object"}}`,
		`{"type":"namespace","name":"mail","tools":[{"type":"function","parameters":{"type":"object"}}]}`,
	} {
		t.Run(tool, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello","tools":[` + tool + `]}`)
			c, _ := gin.CreateTestContext(nil)
			_, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: "openai"}, body)
			require.Error(t, err)
			assert.False(t, converted)
			assert.Contains(t, err.Error(), "unsupported_tool")
			assert.Contains(t, err.Error(), "name is required")
		})
	}
}

func TestProtocolToolCompatRejectsUnnamedFunctionHiddenByDuplicate(t *testing.T) {
	for _, channelType := range []string{"openai", "anthropic"} {
		t.Run(channelType, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello","tools":[` +
				`{"type":"function","name":"function","parameters":{"type":"object"}},` +
				`{"type":"function","parameters":{"type":"object"}}]}`)
			c, _ := gin.CreateTestContext(nil)
			_, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: channelType}, body)
			require.Error(t, err)
			assert.False(t, converted)
			assert.Contains(t, err.Error(), "unsupported_tool")
			assert.Contains(t, err.Error(), "name is required")
		})
	}
}

func applyForceCodexCompat(t *testing.T, channelType string, body []byte) []byte {
	t.Helper()
	c, _ := gin.CreateTestContext(nil)
	out, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: channelType}, body)
	require.NoError(t, err)
	require.True(t, converted)
	return out
}

func TestCodexInputToClaudeMessagesGatesThinkingOnRequest(t *testing.T) {
	// Codex reasoning summaries carry no Anthropic signature; the Messages API
	// only accepts thinking blocks when extended thinking is enabled on the
	// request. Summaries must be omitted when thinking is not active.
	input := json.RawMessage(`[
		{"type":"reasoning","summary":[{"type":"summary_text","text":"draft plan"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
	]`)

	messages, err := convertCodexInputToClaudeMessages(input, false)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	var blocks []ClaudeContentBlock
	require.NoError(t, json.Unmarshal(messages[0].Content, &blocks))
	require.Len(t, blocks, 1)
	assert.Equal(t, "text", blocks[0].Type)

	messages, err = convertCodexInputToClaudeMessages(input, true)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.NoError(t, json.Unmarshal(messages[0].Content, &blocks))
	require.Len(t, blocks, 2)
	assert.Equal(t, "thinking", blocks[0].Type)
	assert.Equal(t, "text", blocks[1].Type)
}

func decodeCompatObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	require.NoError(t, decoder.Decode(&value))
	return value
}
