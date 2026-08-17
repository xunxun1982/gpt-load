package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseClaudeRequest(t *testing.T, raw string) *ClaudeRequest {
	t.Helper()
	var req ClaudeRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	return &req
}

func rawMessageString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}

func TestCCProtocolToolCompatParallelHistory(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":128,
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_a","name":"lookup","input":{"q":"a"}},
				{"type":"tool_use","id":"call_b","name":"lookup","input":{"q":"b"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_a","content":[{"type":"text","text":"a1"},{"type":"text","text":"a2"}]},
				{"type":"tool_result","tool_use_id":"call_b","content":{"ok":true},"is_error":true},
				{"type":"text","text":"continue"}
			]}
		]}`)

	got, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 4)
	require.Len(t, got.Messages[0].ToolCalls, 2)
	require.Equal(t, "call_a", got.Messages[0].ToolCalls[0].ID)
	require.Equal(t, "call_b", got.Messages[0].ToolCalls[1].ID)
	require.Equal(t, []string{"tool", "tool", "user"}, []string{
		got.Messages[1].Role, got.Messages[2].Role, got.Messages[3].Role,
	})
	require.Equal(t, "call_a", got.Messages[1].ToolCallID)
	require.Equal(t, "call_b", got.Messages[2].ToolCallID)

	var blocks []map[string]any
	require.NoError(t, json.Unmarshal([]byte(rawMessageString(t, got.Messages[1].Content)), &blocks))
	require.Len(t, blocks, 2)
	require.Equal(t, "a2", blocks[1]["text"])
	require.JSONEq(t, `{"is_error":true,"content":{"ok":true}}`, rawMessageString(t, got.Messages[2].Content))
}

func TestCCProtocolToolResultsPrecedeUserContent(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":128,
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"lookup","input":{}}]},
			{"role":"user","content":[
				{"type":"text","text":"continue after the result"},
				{"type":"tool_result","tool_use_id":"call_a","content":"done"}
			]}
		]}`)

	got, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 3)
	require.Equal(t, []string{"assistant", "tool", "user"}, []string{
		got.Messages[0].Role, got.Messages[1].Role, got.Messages[2].Role,
	})
	require.Equal(t, "call_a", got.Messages[1].ToolCallID)
	require.Equal(t, "continue after the result", rawMessageString(t, got.Messages[2].Content))
}

func TestCCProtocolToolCompatChoiceAndMaxTokens(t *testing.T) {
	tests := []struct {
		name         string
		choice       string
		wantChoice   any
		wantParallel *bool
	}{
		{name: "auto parallel", choice: `{"type":"auto","disable_parallel_tool_use":false}`, wantChoice: "auto", wantParallel: ccBoolPtr(true)},
		{name: "any serial", choice: `{"type":"any","disable_parallel_tool_use":true}`, wantChoice: "required", wantParallel: ccBoolPtr(false)},
		{name: "specific serial", choice: `{"type":"tool","name":"lookup","disable_parallel_tool_use":true}`, wantChoice: map[string]any{
			"type": "function", "function": map[string]string{"name": "lookup"},
		}, wantParallel: ccBoolPtr(false)},
		{name: "none", choice: `{"type":"none"}`, wantChoice: "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"gpt-test","max_tokens":0,"messages":[{"role":"user","content":"hi"}],` +
				`"tools":[{"type":"custom","name":"lookup","input_schema":{"type":"object"}}],"tool_choice":` + tt.choice + `}`
			got, err := convertClaudeToOpenAI(mustParseClaudeRequest(t, raw), nil)
			require.NoError(t, err)
			if tt.wantChoice != nil {
				require.Equal(t, tt.wantChoice, got.ToolChoice)
			}
			require.Equal(t, tt.wantParallel, got.ParallelToolCalls)

			encoded, err := json.Marshal(got)
			require.NoError(t, err)
			require.Contains(t, string(encoded), `"max_tokens":0`)
		})
	}
}

func TestCCProtocolToolCompatUnsupported(t *testing.T) {
	tests := []struct{ name, extra string }{
		{name: "MCP connector", extra: `"mcp_servers":[{"name":"docs","url":"https://example.test"}]`},
		{name: "server container", extra: `"container":{"id":"container_123"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],` + tt.extra + `}`
			_, err := convertClaudeToOpenAI(mustParseClaudeRequest(t, raw), nil)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), "Not Supported"), err.Error())
		})
	}
}

func TestCCProtocolToolCompatConvertsUnknownToolsAndBlocks(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"tools":[{"type":"web_search_20260209","name":"web_search","description":"Search","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}},
			{"type":"future_tool_2026","name":"future_lookup","input_schema":{"type":"object","properties":{"id":{"type":"integer"}}}}],
		"messages":[
			{"role":"assistant","content":[{"type":"server_tool_use","id":"call_web","name":"web_search","input":{"query":"go"}},{"type":"future_tool_call","id":"call_future","name":"future_lookup","input":{"id":9007199254740993}}]},
			{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"call_web","content":"result"},{"type":"future_tool_result","tool_use_id":"call_future","content":{"ok":true}}]}
		]}`)

	chat, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, chat.Tools, 2)
	assert.Equal(t, "web_search", chat.Tools[0].Function.Name)
	assert.Equal(t, "future_lookup", chat.Tools[1].Function.Name)
	require.Len(t, chat.Messages, 3)
	require.Len(t, chat.Messages[0].ToolCalls, 2)
	assert.Equal(t, "web_search", chat.Messages[0].ToolCalls[0].Function.Name)
	assert.Equal(t, `{"id":9007199254740993}`, chat.Messages[0].ToolCalls[1].Function.Arguments)
	assert.Equal(t, "call_web", chat.Messages[1].ToolCallID)
	assert.Equal(t, "call_future", chat.Messages[2].ToolCallID)

	codex, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	require.Len(t, codex.Tools, 2)
	assert.Equal(t, "web_search", codex.Tools[0].Name)
	assert.Equal(t, "future_lookup", codex.Tools[1].Name)
	var input []map[string]any
	require.NoError(t, json.Unmarshal(codex.Input, &input))
	require.Len(t, input, 4)
	assert.Equal(t, "web_search", input[0]["name"])
	assert.Equal(t, "future_lookup", input[1]["name"])
	assert.Equal(t, "call_web", input[2]["call_id"])
	assert.Equal(t, "call_future", input[3]["call_id"])
	assert.Equal(t, map[string]any{"ok": true}, input[3]["output"])
}

func TestCCProtocolRequestCompatStripsContextManagementFields(t *testing.T) {
	// context_management and sibling beta fields are official Anthropic
	// Messages API parameters with no Chat Completions equivalent; they are
	// recognized and stripped instead of rejecting the conversion.
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],
		"context_management":{"edits":[{"type":"compact_20260112"}]},
		"compaction_control":{"type":"off"},
		"diagnostics":{"enabled":true},
		"cache_control":{"type":"ephemeral"}
	}`)
	got, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	payload := decodeCompatObject(t, encoded)
	require.NotContains(t, payload, "context_management")
	require.NotContains(t, payload, "compaction_control")
	require.NotContains(t, payload, "diagnostics")
	require.NotContains(t, payload, "cache_control")
}

func TestCCProtocolRequestCompatMergesInlineSystemMessages(t *testing.T) {
	// Claude Code may place system prompts inside messages with role "system";
	// they are merged into the leading system message instead of failing.
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"system":"top-level system",
		"messages":[
			{"role":"system","content":"inline system one"},
			{"role":"user","content":"hi"}
		]
	}`)
	got, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 2)
	require.Equal(t, "system", got.Messages[0].Role)
	require.Contains(t, string(got.Messages[0].Content), "top-level system")
	require.Contains(t, string(got.Messages[0].Content), "inline system one")
	require.Equal(t, "user", got.Messages[1].Role)

	// Inline-only case: no top-level system field, block-based content.
	req2 := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"messages":[
			{"role":"system","content":[{"type":"text","text":"inline via blocks"}]},
			{"role":"assistant","content":"ok"}
		]
	}`)
	got2, err := convertClaudeToOpenAI(req2, nil)
	require.NoError(t, err)
	require.Len(t, got2.Messages, 2)
	require.Equal(t, "system", got2.Messages[0].Role)
	require.Contains(t, string(got2.Messages[0].Content), "inline via blocks")
	require.Equal(t, "assistant", got2.Messages[1].Role)
}

func TestCCProtocolRequestCompatCollectsInlineSystemMessagesInOrder(t *testing.T) {
	messages := []ClaudeMessage{
		{Role: "user", Content: json.RawMessage(`"first"`)},
		{Role: "system", Content: json.RawMessage(`"one"`)},
		{Role: "system", Content: json.RawMessage(`[{"type":"text","text":"two"}]`)},
		{Role: "assistant", Content: json.RawMessage(`"last"`)},
	}

	inlineSystem, nonSystem, err := collectInlineClaudeSystemMessages(messages)
	require.NoError(t, err)
	assert.Equal(t, "one\n\ntwo", inlineSystem)
	require.Len(t, nonSystem, 2)
	assert.Equal(t, []string{"user", "assistant"}, []string{nonSystem[0].Role, nonSystem[1].Role})
}

func TestCCProtocolRequestCompatMapsCurrentFields(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],
		"metadata":{"user_id":"user-42"},"service_tier":" Standard_Only ",
		"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}
	}`)

	got, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"user":"user-42"`)
	require.Contains(t, string(encoded), `"service_tier":"default"`)
	require.Contains(t, string(encoded), `"reasoning_effort":"high"`)
}

func TestCCProtocolRequestCompatPreservesReasoningEffort(t *testing.T) {
	for _, effort := range []string{"max", "xhigh", "future_effort_2026"} {
		t.Run(effort, func(t *testing.T) {
			req := mustParseClaudeRequest(t, `{
				"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],
				"thinking":{"type":"adaptive"},"output_config":{"effort":"`+effort+`"}
			}`)

			chat, err := convertClaudeToOpenAI(req, nil)
			require.NoError(t, err)
			assert.Equal(t, effort, chat.ReasoningEffort)

			codex, err := convertClaudeToCodex(req, "", nil)
			require.NoError(t, err)
			require.NotNil(t, codex.Reasoning)
			assert.Equal(t, effort, codex.Reasoning.Effort)
		})
	}
}

func TestCCProtocolRequestCompatDoesNotDeriveEffortOrRewriteUserContent(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"original user text"}],
		"thinking":{"type":"enabled","budget_tokens":20000}
	}`)

	chat, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	assert.Empty(t, chat.ReasoningEffort)
	chatJSON, err := json.Marshal(chat)
	require.NoError(t, err)
	assert.NotContains(t, string(chatJSON), `"reasoning_effort"`)
	require.Len(t, chat.Messages, 1)
	assert.Equal(t, "original user text", rawMessageString(t, chat.Messages[0].Content))

	codex, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	require.NotNil(t, codex.Reasoning)
	assert.Empty(t, codex.Reasoning.Effort)
	codexJSON, err := json.Marshal(codex)
	require.NoError(t, err)
	assert.NotContains(t, string(codexJSON), `"effort"`)
	var input []map[string]any
	require.NoError(t, json.Unmarshal(codex.Input, &input))
	content := input[0]["content"].([]any)[0].(map[string]any)
	assert.Equal(t, "original user text", content["text"])
}

func TestCCProtocolRequestCompatPreservesThinkingSummaryWithoutInventingEffort(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],
		"thinking":{"type":"adaptive"}
	}`)

	chat, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	assert.Empty(t, chat.ReasoningEffort)
	chatJSON, err := json.Marshal(chat)
	require.NoError(t, err)
	assert.NotContains(t, string(chatJSON), `"reasoning_effort"`)

	codex, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	require.NotNil(t, codex.Reasoning)
	assert.Empty(t, codex.Reasoning.Effort)
	assert.Equal(t, "auto", codex.Reasoning.Summary)
	codexJSON, err := json.Marshal(codex)
	require.NoError(t, err)
	assert.Contains(t, string(codexJSON), `"summary":"auto"`)
	assert.NotContains(t, string(codexJSON), `"effort"`)
}

func TestCCProtocolRequestCompatConvertsDisabledThinkingSwitch(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],
		"thinking":{"type":"disabled"}
	}`)

	chat, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	assert.Equal(t, "none", chat.ReasoningEffort)

	codex, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	require.NotNil(t, codex.Reasoning)
	assert.Equal(t, "none", codex.Reasoning.Effort)
}

func TestCCProtocolRequestCompatDisabledThinkingOverridesExplicitEffort(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],
		"thinking":{"type":"disabled"},"output_config":{"effort":"future_effort_2026"}
	}`)

	chat, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	assert.Equal(t, "none", chat.ReasoningEffort)

	codex, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	require.NotNil(t, codex.Reasoning)
	assert.Equal(t, "none", codex.Reasoning.Effort)
}

func TestCCProtocolToolCompatPreservesToolPayloads(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"messages":[
			{"role":"assistant","content":[{"type":"future_tool_call","id":"call_array","name":"future_lookup","input":["a",9007199254740993]}]},
			{"role":"user","content":[{"type":"future_tool_result","tool_use_id":"call_array","content":{"path":"F:\\work\\file.txt","ok":true}}]}
		]}`)

	chat, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, chat.Messages, 2)
	assert.Equal(t, `["a",9007199254740993]`, chat.Messages[0].ToolCalls[0].Function.Arguments)
	chatOutput := decodeCompatObject(t, []byte(rawMessageString(t, chat.Messages[1].Content)))
	assert.Equal(t, `F:\work\file.txt`, chatOutput["path"])
	assert.Equal(t, true, chatOutput["ok"])

	codex, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	var input []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(codex.Input))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&input))
	require.Len(t, input, 2)
	assert.Equal(t, `["a",9007199254740993]`, input[0]["arguments"])
	output := input[1]["output"].(map[string]any)
	assert.Equal(t, `F:\work\file.txt`, output["path"])
	assert.Equal(t, true, output["ok"])
}

func TestCCProtocolToolCompatPreservesLargeIntegerToolResult(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"messages":[{"role":"user","content":[{
			"type":"future_tool_result","tool_use_id":"call_big",
			"content":{"id":9007199254740993}
		}]}]
	}`)

	chat, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, chat.Messages, 1)
	assert.Equal(t, `{"id":9007199254740993}`, rawMessageString(t, chat.Messages[0].Content))
}

func TestCCProtocolRequestCompatRejectsKnownUnsupportedFields(t *testing.T) {
	tests := []struct{ name, extra string }{
		{name: "top_k", extra: `"top_k":10`},
		{name: "unsupported service tier", extra: `"service_tier":"priority"`},
		{name: "output format", extra: `"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}`},
		{name: "unknown metadata", extra: `"metadata":{"tenant":"private"}`},
		{name: "future request field", extra: `"future_protocol_field":{"enabled":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],` + tt.extra + `}`
			_, err := convertClaudeToOpenAI(mustParseClaudeRequest(t, raw), nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), "Not Supported")
		})
	}
}

func TestCCProtocolToolCompatRejectsAssistantToolResultBlocks(t *testing.T) {
	// Anthropic only allows tool_result blocks in user messages. An assistant
	// message carrying one is non-conformant input; both converters must
	// reject it explicitly instead of silently dropping it (Codex) or
	// reporting a generic unsupported block (Chat).
	msg := ClaudeMessage{
		Role: "assistant",
		Content: json.RawMessage(`[
			{"type":"web_search_tool_result","tool_use_id":"call_web","content":"result"},
			{"type":"text","text":"assistant text"}
		]`),
	}
	_, err := convertClaudeMessageToOpenAI(msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "assistant message")

	_, err = convertClaudeMessageToCodexFormatWithToolMap(msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "assistant message")
}

func TestCCProtocolToolCompatCodexRejectsNonAssistantToolUse(t *testing.T) {
	msg := ClaudeMessage{
		Role: "user",
		Content: json.RawMessage(`[
			{"type":"tool_use","id":"call_search","name":"web_search","input":{"query":"test"}}
		]`),
	}

	_, err := convertClaudeMessageToCodexFormatWithToolMap(msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "assistant message")
}

func TestCCProtocolToolCompatCodexRejectsUnknownMessageRole(t *testing.T) {
	msg := ClaudeMessage{Role: "tool", Content: json.RawMessage(`"unexpected"`)}

	_, err := convertClaudeMessageToCodexFormatWithToolMap(msg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "message role")
}

func ccBoolPtr(value bool) *bool { return &value }
