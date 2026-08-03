package proxy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeCodexStateDomainFiltersEncryptedToolOutputs(t *testing.T) {
	t.Parallel()

	body := []byte(`{"input":[
  {"type":"function_call","call_id":"mixed","name":"mixed","arguments":"{}"},
  {"type":"function_call_output","call_id":"mixed","output":[
    {"type":"input_text","text":"portable"},
    {"type":"encrypted_content","encrypted_content":"cipher"},
    {"type":"input_image","image_url":"data:image/png;base64,AA=="}
  ]},
  {"type":"function_call","call_id":"drop","name":"secret","arguments":"{}"},
  {"type":"function_call_output","call_id":"drop","output":[
    {"type":"encrypted_content","encrypted_content":"cipher"}
  ]},
  {"type":"custom_tool_call","call_id":"custom-drop","name":"apply_patch","input":"patch"},
  {"type":"custom_tool_call_output","call_id":"custom-drop","output":[
    {"type":"encrypted_content","encrypted_content":"cipher"}
  ]},
  {"type":"custom_tool_call","call_id":"custom-keep","name":"apply_patch","input":"patch"},
  {"type":"custom_tool_call_output","call_id":"custom-keep","output":"Done!"},
  {"type":"tool_search_call","call_id":"encrypted-call","arguments":"{}","encrypted_content":"cipher"},
  {"type":"tool_search_output","call_id":"encrypted-call","output":"portable"}
]}`)

	got, changed, err := sanitizeCodexStateDomain(body, true)
	require.NoError(t, err)
	require.True(t, changed)

	var payload struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal(got, &payload))
	items := make(map[string]map[string]json.RawMessage)
	for _, item := range payload.Input {
		var callID string
		_ = json.Unmarshal(item["call_id"], &callID)
		items[callID+":"+string(item["type"])] = item
	}

	require.Contains(t, items, `mixed:"function_call"`)
	mixedOutput := items[`mixed:"function_call_output"`]
	require.NotNil(t, mixedOutput)
	require.Contains(t, string(mixedOutput["output"]), "portable")
	require.Contains(t, string(mixedOutput["output"]), "input_image")
	require.NotContains(t, string(mixedOutput["output"]), "encrypted_content")
	require.NotContains(t, items, `drop:"function_call"`)
	require.NotContains(t, items, `drop:"function_call_output"`)
	require.NotContains(t, items, `custom-drop:"custom_tool_call"`)
	require.NotContains(t, items, `custom-drop:"custom_tool_call_output"`)
	require.Contains(t, items, `custom-keep:"custom_tool_call"`)
	require.Contains(t, items, `custom-keep:"custom_tool_call_output"`)
	require.NotContains(t, items, `encrypted-call:"tool_search_call"`)
	require.NotContains(t, items, `encrypted-call:"tool_search_output"`)
}

func TestSanitizeCodexStateDomainDropsUnpairedEncryptedOutputOnly(t *testing.T) {
	t.Parallel()

	body := []byte(`{"input":[
  {"type":"function_call_output","call_id":"missing","output":[
    {"type":"encrypted_content","encrypted_content":"cipher"}
  ]},
  {"type":"message","role":"user","content":"hello"}
]}`)

	got, _, err := sanitizeCodexStateDomain(body, true)
	require.NoError(t, err)
	require.JSONEq(t, `{"input":[{"type":"message","role":"user","content":"hello"}]}`, string(got))
}

func TestSanitizeCodexStateDomainFiltersEncryptedBlocksFromAllToolOutputs(t *testing.T) {
	t.Parallel()

	body := []byte(`{"input":[
  {"type":"mcp_tool_call","call_id":"mcp-drop","name":"lookup","arguments":"{}"},
  {"type":"mcp_tool_call_output","call_id":"mcp-drop","output":[
    {"type":"encrypted_content","encrypted_content":"cipher"}
  ]},
  {"type":"local_shell_call","call_id":"shell-keep","action":{"type":"exec","command":"pwd"}},
  {"type":"local_shell_call_output","call_id":"shell-keep","output":[
    {"type":"input_text","text":"portable"},
    {"type":"encrypted_content","encrypted_content":"cipher"}
  ]},
  {"type":"tool_search_call","call_id":"search-drop","arguments":"{}"},
  {"type":"tool_search_output","call_id":"search-drop","output":[
    {"type":"encrypted_content","encrypted_content":"cipher"}
  ]}
]}`)

	got, changed, err := sanitizeCodexStateDomain(body, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, string(got), "mcp-drop")
	require.NotContains(t, string(got), "search-drop")
	require.Contains(t, string(got), `"call_id":"shell-keep"`)
	require.Contains(t, string(got), `"text":"portable"`)
	require.NotContains(t, string(got), "encrypted_content")
}
