package proxy

import (
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		"reasoning":{"effort":"high","context":"current_turn"}
	}`)

	payload := decodeCompatObject(t, applyForceCodexCompat(t, "openai", body))
	assert.Equal(t, "priority", payload["service_tier"])
	assert.Equal(t, "cache-thread", payload["prompt_cache_key"])
	assert.Equal(t, "high", payload["verbosity"])
	assert.Equal(t, "high", payload["reasoning_effort"])
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

func TestCodexRequestOptionCompatMapsStructuredOutputToAnthropic(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"input":"hello",
		"stream_options":{"reasoning_summary_delivery":"sequential_cutoff"},
		"service_tier":"priority",
		"prompt_cache_key":"cache-thread",
		"text":{"verbosity":"medium","format":{"type":"json_schema","name":"result","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}},
		"client_metadata":{"thread_id":"thread-1"},
		"reasoning":{"context":"current_turn"}
	}`)

	payload := decodeCompatObject(t, applyForceCodexCompat(t, "anthropic", body))
	assert.NotContains(t, payload, "stream_options")
	assert.NotContains(t, payload, "service_tier")
	assert.NotContains(t, payload, "prompt_cache_key")
	assert.NotContains(t, payload, "text")
	assert.NotContains(t, payload, "client_metadata")

	outputConfig := payload["output_config"].(map[string]any)
	format := outputConfig["format"].(map[string]any)
	assert.Equal(t, "json_schema", format["type"])
	assert.NotContains(t, format, "name")
	assert.NotContains(t, format, "strict")
	assert.Equal(t, "object", format["schema"].(map[string]any)["type"])
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
