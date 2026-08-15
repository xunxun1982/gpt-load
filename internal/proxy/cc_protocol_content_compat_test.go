package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCCProtocolContentCompatMedia(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":128,
		"system":[{"type":"text","text":"sys-a"},{"type":"text","text":"sys-b"}],
		"messages":[{"role":"user","content":[
			{"type":"text","text":"inspect"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},
			{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}},
			{"type":"document","title":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"cGRm"}},
			{"type":"document","title":"notes","context":"reference","source":{"type":"text","media_type":"text/plain","data":"facts"}}
		]}]}`)

	got, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 2)
	require.Equal(t, "sys-asys-b", rawMessageString(t, got.Messages[0].Content))

	var parts []map[string]any
	require.NoError(t, json.Unmarshal(got.Messages[1].Content, &parts))
	require.Equal(t, []any{"text", "image_url", "image_url", "file", "text"}, []any{
		parts[0]["type"], parts[1]["type"], parts[2]["type"], parts[3]["type"], parts[4]["type"],
	})
	require.Equal(t, "data:image/png;base64,aGVsbG8=", parts[1]["image_url"].(map[string]any)["url"])
	require.Equal(t, "data:application/pdf;base64,cGRm", parts[3]["file"].(map[string]any)["file_data"])
	require.Contains(t, parts[4]["text"], "facts")
}

func TestCCProtocolContentCompatUnsupportedBlocks(t *testing.T) {
	tests := []struct{ name, role, block string }{
		{name: "redacted thinking", role: "assistant", block: `{"type":"redacted_thinking","data":"opaque"}`},
		{name: "container upload", role: "user", block: `{"type":"container_upload","file_id":"file_1"}`},
		{name: "PDF URL", role: "user", block: `{"type":"document","source":{"type":"url","url":"https://example.test/a.pdf"}}`},
		{name: "image file ID", role: "user", block: `{"type":"image","source":{"type":"file","file_id":"file_1"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"gpt-test","max_tokens":64,"messages":[{"role":"` + tt.role + `","content":[` + tt.block + `]}]}`
			_, err := convertClaudeToOpenAI(mustParseClaudeRequest(t, raw), nil)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), "Not Supported"), err.Error())
			require.NotContains(t, err.Error(), "opaque-secret-value")
		})
	}
}

func TestCCProtocolContentCompatPlainThinkingHistory(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"messages":[{"role":"assistant","content":[
			{"type":"thinking","thinking":"plan","signature":"opaque-secret-value"},
			{"type":"tool_use","id":"call_1","name":"lookup","input":{}}
		]}]}`)

	got, err := convertClaudeToOpenAI(req, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	require.NotNil(t, got.Messages[0].ReasoningContent)
	require.Equal(t, "plan", *got.Messages[0].ReasoningContent)
	require.Equal(t, "{}", got.Messages[0].ToolCalls[0].Function.Arguments)
	encoded, err := json.Marshal(got.Messages[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "opaque-secret-value")
}

func TestCCProtocolContentCompatPreservesThinkingHistoryForCodex(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"messages":[{"role":"assistant","content":[
			{"type":"thinking","thinking":"plan before lookup"},
			{"type":"tool_use","id":"call_lookup","name":"lookup","input":{}}
		]}]}`)

	got, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	var input []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &input))
	require.Len(t, input, 2)
	require.Equal(t, "reasoning", input[0]["type"])
	require.Equal(t, "plan before lookup", input[0]["summary"].([]any)[0].(map[string]any)["text"])
	require.Equal(t, "function_call", input[1]["type"])
	require.Equal(t, "call_lookup", input[1]["call_id"])
}

func TestCCProtocolContentCompatMergesInlineSystemMessagesForCodex(t *testing.T) {
	// Claude Code may place system prompts inside messages with role "system";
	// they are merged into the leading developer item instead of being emitted
	// as a raw system message (which some Responses upstreams reject).
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"system":"top-level system",
		"messages":[
			{"role":"system","content":"inline system one"},
			{"role":"user","content":"hi"}
		]
	}`)

	got, err := convertClaudeToCodex(req, "", nil)
	require.NoError(t, err)
	var input []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &input))
	require.Len(t, input, 2)
	require.Equal(t, "developer", input[0]["role"])
	systemText := input[0]["content"].([]any)[0].(map[string]any)["text"].(string)
	require.Contains(t, systemText, "top-level system")
	require.Contains(t, systemText, "inline system one")
	require.Equal(t, "user", input[1]["role"])
}

func TestCCProtocolContentCompatRejectsNonTextSystem(t *testing.T) {
	req := mustParseClaudeRequest(t, `{
		"model":"gpt-test","max_tokens":64,
		"system":[{"type":"text","text":"ok"},{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}],
		"messages":[{"role":"user","content":"hi"}]}`)

	_, err := convertClaudeToOpenAI(req, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Not Supported")
}
