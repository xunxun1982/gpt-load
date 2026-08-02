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
			if channelType == "openai" {
				tool = tool["function"].(map[string]any)
			}
			assert.Equal(t, true, tool["strict"])
			parameters := tool[map[bool]string{true: "parameters", false: "input_schema"}[channelType == "openai"]].(map[string]any)
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

func TestProtocolToolCompatRejectsNonReversibleTools(t *testing.T) {
	for _, toolType := range []string{
		"web_search", "web_search_preview", "file_search", "computer", "computer_use",
		"computer_use_preview", "code_interpreter", "image_generation", "mcp", "shell", "local_shell",
	} {
		for _, channelType := range []string{"openai", "anthropic"} {
			t.Run(channelType+"_"+toolType, func(t *testing.T) {
				tool := `{"type":"` + toolType + `","name":"unsafe"}`
				body := []byte(`{"model":"gpt-test","input":"hello","tools":[` + tool + `]}`)
				w := &ProxyServer{}
				c, _ := gin.CreateTestContext(nil)
				_, converted, err := w.applyForceCodexRequestConversion(c, &models.Group{ChannelType: channelType}, body)
				require.Error(t, err)
				assert.False(t, converted)
				assert.Contains(t, err.Error(), "unsupported_tool")
				assert.Contains(t, err.Error(), "Not Supported")
			})
		}
	}
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

func applyForceCodexCompat(t *testing.T, channelType string, body []byte) []byte {
	t.Helper()
	c, _ := gin.CreateTestContext(nil)
	out, converted, err := (&ProxyServer{}).applyForceCodexRequestConversion(c, &models.Group{ChannelType: channelType}, body)
	require.NoError(t, err)
	require.True(t, converted)
	return out
}

func decodeCompatObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	require.NoError(t, decoder.Decode(&value))
	return value
}
