package proxy

import (
	"encoding/json"
	"strings"
	"testing"

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
		{name: "server web search", extra: `"tools":[{"type":"web_search_20260209","name":"web_search"}]`},
		{name: "tool search", extra: `"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search"}]`},
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

func TestCCProtocolRequestCompatMapsCurrentFields(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],
		"metadata":{"user_id":"user-42"},"service_tier":"standard_only",
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

func ccBoolPtr(value bool) *bool { return &value }
