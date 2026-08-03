package proxy

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeCodexStateDomainRemovesProviderBoundState(t *testing.T) {
	t.Parallel()

	body := []byte(`{
  "model":"gpt-5","previous_response_id":"resp_old","conversation":{"id":"conv_old"},
  "reasoning":{"effort":"high","summary":"auto"},"prompt_cache_key":"cache-key",
  "client_metadata":{"thread_id":"thread-1"},"numeric_probe":9007199254740993,
  "include":["reasoning.encrypted_content","web_search_call.results"],
  "input":[
    {"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
    {"type":"reasoning","id":"rs_1","encrypted_content":"enc_reasoning"},
    {"type":"item_reference","id":"item_1"},
    {"type":"function_call","call_id":"call_keep","name":"spawn_agent","arguments":"{}","encrypted_function_args":[]},
    {"type":"function_call_output","call_id":"call_keep","output":"ok"},
    {"type":"agent_message","id":"enc","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]},
    {"type":"agent_message","id":"plain","content":[{"type":"input_text","text":"portable"}]},
	{"type":"message","id":"enc-message","role":"assistant","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]},
	{"type":"encrypted_content","encrypted_content":"cipher"},
    {"type":"compaction","id":"cmp_1","encrypted_content":"cipher"},
    {"type":"compaction_summary","id":"cmp_2"},
    {"type":"context_compaction","id":"cmp_3"},
    {"type":"future_extension","value":{"nested":true}}
  ]
}`)

	got, changed, err := sanitizeCodexStateDomain(body, true)
	require.NoError(t, err)
	require.True(t, changed)

	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got, &payload))
	require.NotContains(t, payload, "previous_response_id")
	require.NotContains(t, payload, "conversation")
	require.JSONEq(t, `{"effort":"high","summary":"auto"}`, string(payload["reasoning"]))
	require.JSONEq(t, `{"thread_id":"thread-1"}`, string(payload["client_metadata"]))
	require.Equal(t, "9007199254740993", string(payload["numeric_probe"]))
	require.Contains(t, string(payload["include"]), "reasoning.encrypted_content")
	require.NotContains(t, string(payload["input"]), "encrypted_function_args")
	require.NotContains(t, string(payload["input"]), `"type":"reasoning"`)
	require.NotContains(t, string(payload["input"]), `"type":"item_reference"`)
	require.NotContains(t, string(payload["input"]), `"id":"enc"`)
	require.NotContains(t, string(payload["input"]), `"id":"enc-message"`)
	require.NotContains(t, string(payload["input"]), `"type":"encrypted_content"`)
	require.Contains(t, string(payload["input"]), `"id":"plain"`)
	require.Contains(t, string(payload["input"]), `"type":"future_extension"`)
}

func TestSanitizeCodexStateDomainRemovesClientMetadataTurnState(t *testing.T) {
	t.Parallel()

	body := []byte(`{
  "model":"gpt-5",
  "client_metadata":{
    "thread_id":"thread-new",
    "x-codex-window-id":"window-stable",
    "x-codex-turn-state":"turn-state-old",
    "custom":"keep"
  },
  "input":"hello"
}`)

	got, changed, err := sanitizeCodexStateDomain(body, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{
  "model":"gpt-5",
  "client_metadata":{
    "thread_id":"thread-new",
    "x-codex-window-id":"window-stable",
    "custom":"keep"
  },
  "input":"hello"
}`, string(got))
}

func TestSanitizeCodexStateDomainRemovesAllCasedTurnStateKeys(t *testing.T) {
	t.Parallel()

	body := []byte(`{
  "model":"gpt-5",
  "client_metadata":{
    "thread_id":"thread-new",
    "x-codex-turn-state":"old-lower",
    "X-Codex-Turn-State":"old-upper"
  },
  "input":"hello"
}`)

	got, changed, err := sanitizeCodexStateDomain(body, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, string(got), "old-lower")
	require.NotContains(t, string(got), "old-upper")
	require.Contains(t, string(got), "thread-new")
}

func TestSanitizeCodexStateDomainDropsUnsupportedEncryptedReasoningInclude(t *testing.T) {
	t.Parallel()

	body := []byte(`{"include":["reasoning.encrypted_content","web_search_call.results"],"input":[]}`)
	got, changed, err := sanitizeCodexStateDomain(body, false)

	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{"include":["web_search_call.results"],"input":[]}`, string(got))
}

func TestSanitizeCodexStateDomainReturnsOriginalBytesWhenPortable(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-5","reasoning":{"effort":"high"},"input":"hello"}`)
	got, changed, err := sanitizeCodexStateDomain(body, true)

	require.NoError(t, err)
	require.False(t, changed)
	require.True(t, bytes.Equal(body, got))
}

func TestSanitizeCodexStateDomainRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	_, _, err := sanitizeCodexStateDomain([]byte(`{"input":[`), true)
	require.Error(t, err)
}
