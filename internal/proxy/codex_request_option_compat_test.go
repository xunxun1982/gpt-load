package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestCodexRequestOptionCompatMapsCurrentFieldsToOpenAIChat(t *testing.T) {
	body := []byte(`{
		"model":"gpt-test",
		"input":"hello",
		"stream":true,
		"stream_options":{"include_usage":true,"reasoning_summary_delivery":"sequential_cutoff"},
		"service_tier":"priority",
		"prompt_cache_key":"cache-thread",
		"text":{"verbosity":"high","format":{"type":"json_schema","name":"result","strict":true,"schema":{"type":"object","properties":{"id":{"type":"integer","maximum":9007199254740993}},"required":["id"],"additionalProperties":false}}},
		"client_metadata":{"thread_id":"thread-1"},
		"reasoning":{"effort":"xhigh","context":"current_turn"}
	}`)

	payload := decodeCompatObject(t, applyForceCodexCompat(t, "openai", body))
	assert.Equal(t, "priority", payload["service_tier"])
	assert.Equal(t, "cache-thread", payload["prompt_cache_key"])
	assert.Equal(t, "high", payload["verbosity"])
	assert.Equal(t, "xhigh", payload["reasoning_effort"])
	assert.NotContains(t, payload, "client_metadata")

	streamOptions := payload["stream_options"].(map[string]any)
	assert.Equal(t, true, streamOptions["include_usage"])
	assert.NotContains(t, streamOptions, "reasoning_summary_delivery")

	responseFormat := payload["response_format"].(map[string]any)
	assert.Equal(t, "json_schema", responseFormat["type"])
	jsonSchema := responseFormat["json_schema"].(map[string]any)
	assert.Equal(t, "result", jsonSchema["name"])
	assert.Equal(t, true, jsonSchema["strict"])
	maximum := jsonSchema["schema"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["maximum"]
	assert.Equal(t, "9007199254740993", maximum.(interface{ String() string }).String())
}

func TestCodexRequestOptionCompatOmitsToolOptionsWithoutTools(t *testing.T) {
	body := []byte(`{
		"model":"gpt-test","input":"hello",
		"tool_choice":"auto","parallel_tool_calls":false
	}`)

	payload := decodeCompatObject(t, applyForceCodexCompat(t, "openai", body))
	assert.NotContains(t, payload, "tool_choice")
	assert.NotContains(t, payload, "parallel_tool_calls")
}

func TestCodexRequestOptionCompatAllowsExplicitPostConversionOverrides(t *testing.T) {
	converted := applyForceCodexCompat(t, "openai", []byte(`{
		"model":"gpt-test","input":"hello","prompt_cache_key":"source-cache"
	}`))
	group := &models.Group{ParamOverrides: datatypes.JSONMap{
		"prompt_cache_key": "configured-cache",
		"future_option":    map[string]any{"enabled": true},
	}}

	out, err := (&ProxyServer{}).applyParamOverrides(converted, group)
	require.NoError(t, err)
	payload := decodeCompatObject(t, out)
	assert.Equal(t, "configured-cache", payload["prompt_cache_key"])
	assert.Equal(t, map[string]any{"enabled": true}, payload["future_option"])
}

func TestProtocolConversionParamOverridesSkipUnsupportedPromptCacheKey(t *testing.T) {
	converted := applyForceCodexCompat(t, "openai", []byte(`{
		"model":"gpt-test","input":"hello"
	}`))
	group := &models.Group{
		ChannelType: "openai",
		Upstreams:   []byte(`[{"url":"https://api.deepseek.com/v1","weight":100}]`),
		ParamOverrides: datatypes.JSONMap{
			"prompt_cache_key": "configured-cache",
			"future_option":    map[string]any{"enabled": true},
		},
	}

	out, err := (&ProxyServer{}).applyParamOverrides(converted, group)
	require.NoError(t, err)
	out, err = filterProtocolConversionRequestBody(out, group, codexUpstreamOpenAIChat, "https://api.deepseek.com/v1/chat/completions")
	require.NoError(t, err)
	payload := decodeCompatObject(t, out)
	assert.NotContains(t, payload, "prompt_cache_key")
	assert.Equal(t, map[string]any{"enabled": true}, payload["future_option"])
}

func TestProtocolConversionParamOverridesKeepPromptCacheKeyForOpenAI(t *testing.T) {
	converted := applyForceCodexCompat(t, "openai", []byte(`{
		"model":"gpt-test","input":"hello"
	}`))
	group := &models.Group{
		ChannelType: "openai",
		Upstreams:   []byte(`[{"url":"https://api.openai.com/v1","weight":100}]`),
		ParamOverrides: datatypes.JSONMap{
			"prompt_cache_key": "configured-cache",
		},
	}

	out, err := (&ProxyServer{}).applyParamOverrides(converted, group)
	require.NoError(t, err)
	out, err = filterProtocolConversionRequestBody(out, group, codexUpstreamOpenAIChat, "https://api.openai.com/v1/chat/completions")
	require.NoError(t, err)
	payload := decodeCompatObject(t, out)
	assert.Equal(t, "configured-cache", payload["prompt_cache_key"])
}

func TestProtocolConversionRoutesSourcePromptCacheKeyByUpstreamCapability(t *testing.T) {
	converted := applyForceCodexCompat(t, "openai", []byte(`{
		"model":"gpt-test","input":"hello","prompt_cache_key":"source-cache"
	}`))
	gatewayBaseURL := strings.TrimRight(channel.GatewayProxyBaseURL("betterclaude"), "/")
	tests := []struct {
		name       string
		upstream   string
		wantCached bool
	}{
		{name: "OpenAI", upstream: "https://api.openai.com/v1/chat/completions", wantCached: true},
		{name: "Kimi Coding", upstream: "https://api.kimi.com/coding/v1/chat/completions", wantCached: true},
		{name: "gateway OpenAI", upstream: gatewayBaseURL + "/openai/api.openai.com/v1/chat/completions", wantCached: true},
		{name: "gateway Kimi Coding", upstream: gatewayBaseURL + "/openai/api.kimi.com/coding/v1/chat/completions", wantCached: true},
		{name: "gateway-shaped path on unrelated host", upstream: "https://future.example.com/openai/api.openai.com/v1/chat/completions", wantCached: false},
		{name: "unrelated OpenAI path", upstream: "https://future.example.com/docs/api.openai.com/v1/chat/completions", wantCached: false},
		{name: "embedded OpenAI host segment", upstream: "https://future.example.com/openai/prefix-api.openai.com/v1/chat/completions", wantCached: false},
		{name: "unrelated Kimi path", upstream: "https://future.example.com/docs/api.kimi.com/coding/v1/chat/completions", wantCached: false},
		{name: "unknown compatible gateway", upstream: "https://future.example.com/v1/chat/completions", wantCached: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := filterProtocolConversionRequestBody(converted, nil, codexUpstreamOpenAIChat, tt.upstream)
			require.NoError(t, err)
			payload := decodeCompatObject(t, out)
			if tt.wantCached {
				assert.Equal(t, "source-cache", payload["prompt_cache_key"])
			} else {
				assert.NotContains(t, payload, "prompt_cache_key")
			}
		})
	}
}

func TestProtocolConversionPromptCacheRoutingConfigOverridesAutoDetection(t *testing.T) {
	tests := []struct {
		name       string
		routing    string
		upstream   string
		wantCached bool
	}{
		{name: "explicit enable for future upstream", routing: "enabled", upstream: "https://future.example.com/v1/chat/completions", wantCached: true},
		{name: "explicit disable for OpenAI", routing: "disabled", upstream: "https://api.openai.com/v1/chat/completions", wantCached: false},
		{name: "future mode falls back to auto", routing: "future_mode", upstream: "https://strict.example.com/v1/chat/completions", wantCached: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := &models.Group{
				Config:         map[string]any{"prompt_cache_routing": tt.routing},
				ParamOverrides: datatypes.JSONMap{"prompt_cache_key": "configured-cache"},
			}
			out, err := (&ProxyServer{}).applyParamOverrides([]byte(`{"model":"gpt-test"}`), group)
			require.NoError(t, err)
			out, err = filterProtocolConversionRequestBody(out, group, codexUpstreamOpenAIChat, tt.upstream)
			require.NoError(t, err)
			payload := decodeCompatObject(t, out)
			if tt.wantCached {
				assert.Equal(t, "configured-cache", payload["prompt_cache_key"])
			} else {
				assert.NotContains(t, payload, "prompt_cache_key")
			}
		})
	}
}

func TestProtocolConversionParamOverridesRemovePromptCacheKeyForAnthropic(t *testing.T) {
	group := &models.Group{ParamOverrides: datatypes.JSONMap{
		"prompt_cache_key": "configured-cache",
		"future_option":    true,
	}}
	out, err := (&ProxyServer{}).applyParamOverrides([]byte(`{"model":"claude-test"}`), group)
	require.NoError(t, err)
	out, err = filterProtocolConversionRequestBody(out, group, codexUpstreamClaude, "https://api.anthropic.com/v1/messages")
	require.NoError(t, err)
	payload := decodeCompatObject(t, out)
	assert.NotContains(t, payload, "prompt_cache_key")
	assert.Equal(t, true, payload["future_option"])
}

func TestProtocolConversionDefersParamOverridesOnlyForConvertedEndpoints(t *testing.T) {
	ccGroup := &models.Group{
		ChannelType: "openai",
		Config:      map[string]any{"cc_support": true},
	}
	codexGroup := &models.Group{
		ChannelType: "openai",
		Config:      map[string]any{"codex_support": true},
	}
	openAIResponsesGroup := &models.Group{ChannelType: "openai-response"}
	openAIResponsesGroup.Config = map[string]any{"cc_support": true}

	assert.True(t, shouldDeferParamOverridesForProtocolConversion(ccGroup, true, false))
	assert.True(t, shouldDeferParamOverridesForProtocolConversion(codexGroup, false, true))
	assert.True(t, shouldDeferParamOverridesForProtocolConversion(openAIResponsesGroup, true, false))
	assert.False(t, shouldDeferParamOverridesForProtocolConversion(&models.Group{ChannelType: "openai-response"}, false, true))
	assert.False(t, shouldDeferParamOverridesForProtocolConversion(ccGroup, false, false))

	assert.True(t, aggregateClaudeConversionExpected(true, "openai-response", false))
	assert.True(t, aggregateClaudeConversionExpected(false, "anthropic", true))
	assert.False(t, aggregateClaudeConversionExpected(false, "anthropic", false))
}

func TestCodexRequestOptionCompatMapsStructuredOutputToAnthropic(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"input":"hello",
		"stream_options":{"reasoning_summary_delivery":"sequential_cutoff"},
		"text":{"verbosity":"medium","format":{"type":"json_schema","name":"result","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}},
		"client_metadata":{"thread_id":"thread-1"},
		"reasoning":{"context":"current_turn"}
	}`)

	payload := decodeCompatObject(t, applyForceCodexCompat(t, "anthropic", body))
	assert.NotContains(t, payload, "stream_options")
	assert.NotContains(t, payload, "text")
	assert.NotContains(t, payload, "client_metadata")

	outputConfig := payload["output_config"].(map[string]any)
	format := outputConfig["format"].(map[string]any)
	assert.Equal(t, "json_schema", format["type"])
	assert.NotContains(t, format, "name")
	assert.NotContains(t, format, "strict")
	assert.Equal(t, "object", format["schema"].(map[string]any)["type"])
}

func TestCodexRequestOptionCompatOmitsResponsesOnlyRoutingFieldsForAnthropic(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"input":"hello",
		"service_tier":"priority",
		"prompt_cache_key":"cache-thread"
	}`)

	payload := decodeCompatObject(t, applyForceCodexCompat(t, "anthropic", body))
	assert.NotContains(t, payload, "service_tier")
	assert.NotContains(t, payload, "prompt_cache_key")
}

func TestCodexRequestOptionCompatRejectsNonEquivalentOptions(t *testing.T) {
	tests := []struct {
		name        string
		channelType string
		field       string
	}{
		{name: "anthropic_low_verbosity", channelType: "anthropic", field: `"text":{"verbosity":"low"}`},
		{name: "anthropic_all_turns_reasoning", channelType: "anthropic", field: `"reasoning":{"context":"all_turns"}`},
		{name: "chat_all_turns_reasoning", channelType: "openai", field: `"reasoning":{"context":"all_turns"}`},
		{name: "chat_non_json_format", channelType: "openai", field: `"text":{"format":{"type":"grammar","name":"result","strict":true,"schema":{}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello",` + tt.field + `}`)
			c, _ := gin.CreateTestContext(nil)
			_, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: tt.channelType}, body)
			require.Error(t, err)
			assert.False(t, converted)
			assert.Contains(t, err.Error(), "unsupported_request_option")
			assert.Contains(t, err.Error(), "Not Supported")
		})
	}
}

func TestCodexRequestOptionCompatPreservesReasoningEffort(t *testing.T) {
	for _, effort := range []string{"max", "xhigh", "future_effort_2026"} {
		t.Run(effort, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello","reasoning":{"effort":"` + effort + `"}}`)

			chat := decodeCompatObject(t, applyForceCodexCompat(t, "openai", body))
			assert.Equal(t, effort, chat["reasoning_effort"])

			claude := decodeCompatObject(t, applyForceCodexCompat(t, "anthropic", body))
			assert.Equal(t, "adaptive", claude["thinking"].(map[string]any)["type"])
			assert.Equal(t, effort, claude["output_config"].(map[string]any)["effort"])
		})
	}
}

func TestCodexRequestOptionCompatSupportsPlainTextFormat(t *testing.T) {
	body := []byte(`{"model":"gpt-test","input":"hello","text":{"format":{"type":"text"}}}`)
	for _, channelType := range []string{"openai", "anthropic"} {
		t.Run(channelType, func(t *testing.T) {
			payload := decodeCompatObject(t, applyForceCodexCompat(t, channelType, body))
			assert.NotContains(t, payload, "response_format")
			assert.NotContains(t, payload, "output_config")
		})
	}
}

func TestCodexRequestOptionCompatRejectsUnknownToolChoice(t *testing.T) {
	for _, choice := range []string{`"sometimes"`, `{"type":"allowed_tools","tools":[{"type":"function","name":"lookup"}]}`} {
		t.Run(choice, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":` + choice + `}`)
			c, _ := gin.CreateTestContext(nil)
			_, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: "anthropic"}, body)
			require.Error(t, err)
			assert.False(t, converted)
			assert.Contains(t, err.Error(), "Not Supported")
		})
	}
}

func TestCodexRequestOptionCompatConvertsUnknownNamedToolChoice(t *testing.T) {
	for _, toolChoice := range []string{
		`{"type":"future_tool_2026","name":"future_lookup"}`,
		`{"name":"future_lookup"}`,
	} {
		t.Run(toolChoice, func(t *testing.T) {
			body := []byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"future_tool_2026","name":"future_lookup","parameters":{"type":"object"}}],"tool_choice":` + toolChoice + `}`)

			chat := decodeCompatObject(t, applyForceCodexCompat(t, "openai", body))
			assert.Equal(t, map[string]any{
				"type":     "function",
				"function": map[string]any{"name": "future_lookup"},
			}, chat["tool_choice"])

			claude := decodeCompatObject(t, applyForceCodexCompat(t, "anthropic", body))
			assert.Equal(t, map[string]any{"type": "tool", "name": "future_lookup"}, claude["tool_choice"])
		})
	}
}

func TestCodexRequestOptionCompatMapsAnthropicReasoningAndParallelCalls(t *testing.T) {
	body := []byte(`{
		"model":"claude-test","input":"hello","reasoning":{"effort":"high","summary":"auto"},
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"tool_choice":"auto","parallel_tool_calls":false
	}`)

	payload := decodeCompatObject(t, applyForceCodexCompat(t, "anthropic", body))
	assert.Equal(t, "adaptive", payload["thinking"].(map[string]any)["type"])
	assert.Equal(t, "high", payload["output_config"].(map[string]any)["effort"])
	toolChoice := payload["tool_choice"].(map[string]any)
	assert.Equal(t, "auto", toolChoice["type"])
	assert.Equal(t, true, toolChoice["disable_parallel_tool_use"])
}

func TestCodexClaudeParallelToolChoicePreservesSelectorShape(t *testing.T) {
	parallel := false

	withoutType, err := codexClaudeToolChoiceWithParallel(json.RawMessage(`{"name":"lookup"}`), &parallel, true)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"lookup","disable_parallel_tool_use":true}`, string(withoutType))

	none, err := codexClaudeToolChoiceWithParallel(json.RawMessage(`{"type":"none"}`), &parallel, true)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"none"}`, string(none))
}
