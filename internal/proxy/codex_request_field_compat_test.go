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

func TestProtocolToolCompatModelsCurrentCodexToolFields(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","input":"hello","tools":[
		{"type":"custom","name":"parser","description":"Parse","format":{"type":"grammar","syntax":"lark","definition":"start: WORD"}},
		{"type":"function","name":"late","defer_loading":true,"parameters":{"type":"object","properties":{}}},
		{"type":"namespace","name":"mail","description":"Mail namespace","tools":[]},
		{"type":"tool_search","execution":"client","description":"Find tools","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}
	]}`)
	var req CodexRequest
	require.NoError(t, json.Unmarshal(raw, &req))
	out, err := json.Marshal(req)
	require.NoError(t, err)
	payload := decodeCompatObject(t, out)
	tools := payload["tools"].([]any)
	format, ok := tools[0].(map[string]any)["format"].(map[string]any)
	require.True(t, ok, "custom format must survive Codex request decoding")
	assert.Equal(t, "grammar", format["type"])
	assert.Equal(t, true, tools[1].(map[string]any)["defer_loading"])
	assert.Equal(t, "Mail namespace", tools[2].(map[string]any)["description"])
	assert.Equal(t, "client", tools[3].(map[string]any)["execution"])
	assert.Equal(t, "Find tools", tools[3].(map[string]any)["description"])
}

func TestProtocolToolCompatMovesCodexSystemRolesToClaudeSystem(t *testing.T) {
	req := &CodexRequest{
		Model:        "claude-test",
		Instructions: "base instructions",
		Input: json.RawMessage(`[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"system context"}]},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer context"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]`),
	}
	got, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	system := extractSystemContent(got.System)
	assert.Contains(t, system, "base instructions")
	assert.Contains(t, system, "system context")
	assert.Contains(t, system, "developer context")
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
}

func TestProtocolToolCompatUsesDeveloperRoleForClaudeSystem(t *testing.T) {
	got, err := convertClaudeToCodex(&ClaudeRequest{
		Model:    "gpt-test",
		System:   json.RawMessage(`"application rules"`),
		Messages: []ClaudeMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
	}, "", nil)
	require.NoError(t, err)
	var input []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &input))
	require.Len(t, input, 2)
	assert.Equal(t, "developer", input[0]["role"])
	assert.Equal(t, "user", input[1]["role"])
}

func TestProtocolToolCompatCanUseLegacyUserRoleForClaudeSystem(t *testing.T) {
	got, err := convertClaudeToCodex(&ClaudeRequest{
		Model:    "gpt-test",
		System:   json.RawMessage(`"application rules"`),
		Messages: []ClaudeMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
	}, "", &models.Group{Config: map[string]any{"responses_legacy_user_role": true}})
	require.NoError(t, err)
	var input []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &input))
	require.Len(t, input, 2)
	assert.Equal(t, "user", input[0]["role"])
	assert.Equal(t, "user", input[1]["role"])
}

func TestProtocolToolCompatDoesNotInventStrictForClaudeTools(t *testing.T) {
	got, err := convertClaudeToCodex(&ClaudeRequest{
		Model: "gpt-test",
		Tools: []ClaudeTool{{
			Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}, "", nil)
	require.NoError(t, err)

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	payload := decodeCompatObject(t, encoded)
	tool := payload["tools"].([]any)[0].(map[string]any)
	assert.NotContains(t, tool, "strict")
}

func TestProtocolToolCompatKeepsZeroArgumentToolCall(t *testing.T) {
	got := convertCodexToClaudeResponse(&CodexResponse{
		ID: "resp_test", Status: "completed", Model: "gpt-test",
		Output: []CodexOutputItem{{
			Type: "function_call", CallID: "call_empty", Name: "current_time", Arguments: `{}`,
		}},
	}, nil)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "tool_use", got.Content[0].Type)
	assert.Equal(t, "call_empty", got.Content[0].ID)
	assert.JSONEq(t, `{}`, string(got.Content[0].Input))
}

func TestProtocolToolCompatCodexToClaudeConvertsUnknownResponseTool(t *testing.T) {
	got := convertCodexToClaudeResponse(&CodexResponse{
		ID: "resp_test", Status: "completed", Model: "gpt-test",
		Output: []CodexOutputItem{{
			Type: "future_tool_call", CallID: "call_future", Name: "future_lookup", Arguments: `{"id":9007199254740993}`,
		}},
	}, nil)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "tool_use", got.Content[0].Type)
	assert.Equal(t, "future_lookup", got.Content[0].Name)
	assert.Equal(t, `{"id":9007199254740993}`, string(got.Content[0].Input))
}

func TestProtocolToolCompatCodexStreamConvertsUnknownResponseTool(t *testing.T) {
	state := newCodexStreamState(nil)
	events := state.processCodexStreamEvent(&CodexStreamEvent{
		Type: "response.output_item.added",
		Item: &CodexOutputItem{Type: "future_tool_call", CallID: "call_future", Name: "future_lookup"},
	})
	require.Len(t, events, 1)
	require.NotNil(t, events[0].ContentBlock)
	assert.Equal(t, "tool_use", events[0].ContentBlock.Type)
	assert.Equal(t, "call_future", events[0].ContentBlock.ID)
	assert.Equal(t, "future_lookup", events[0].ContentBlock.Name)
}

func TestProtocolToolCompatPassesThroughToolArguments(t *testing.T) {
	const arguments = `{"cursor":9007199254740993,"allowed_domains":[]}`
	assert.Equal(t, arguments, cleanToolCallArguments(arguments))
}

func TestProtocolToolCompatReadsToolSearchOutputToolsForClaude(t *testing.T) {
	req := &CodexRequest{Model: "claude-test", Input: json.RawMessage(`[
		{"type":"tool_search_output","call_id":"call_search","tools":[{"type":"function","name":"loaded","parameters":{"type":"object","properties":{}}}]}
	]`)}
	got, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	var blocks []ClaudeContentBlock
	require.NoError(t, json.Unmarshal(got.Messages[0].Content, &blocks))
	require.Len(t, blocks, 1)
	var output []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(blocks[0].Content))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&output))
	require.Len(t, output, 1)
	assert.Equal(t, "loaded", output[0]["name"])
}

func TestCodexRequestOptionCompatRejectsUnmodeledTopLevelFields(t *testing.T) {
	for _, field := range []string{
		`"background":true`,
		`"max_tool_calls":4`,
		`"prompt_cache_retention":"24h"`,
		`"future_protocol_field":{"enabled":true}`,
	} {
		t.Run(field, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello",` + field + `}`)
			c, _ := gin.CreateTestContext(nil)
			_, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: "openai"}, body)
			require.Error(t, err)
			assert.False(t, converted)
			assert.Contains(t, err.Error(), "unsupported_request_option")
			assert.Contains(t, err.Error(), "Not Supported")
		})
	}
}
