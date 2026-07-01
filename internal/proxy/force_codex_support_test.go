package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failAfterWriteResponseWriter struct {
	gin.ResponseWriter
	failAfter int
	writes    int
}

func (w *failAfterWriteResponseWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes > w.failAfter {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(data)
}

func TestCodexPathHelpersAndSupport(t *testing.T) {
	t.Parallel()

	t.Run("isCodexPath", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name      string
			path      string
			groupName string
			expected  bool
		}{
			{"codex_path", "/proxy/mygroup/codex/v1/responses", "mygroup", true},
			{"codex_compact_path", "/proxy/mygroup/codex/v1/responses/compact", "mygroup", true},
			{"group_named_codex", "/proxy/codex/v1/responses", "codex", false},
			{"group_named_codex_with_force", "/proxy/codex/codex/v1/responses", "codex", true},
			{"claude_path_not_codex", "/proxy/mygroup/claude/v1/messages", "mygroup", false},
			{"plain_responses_path", "/proxy/mygroup/v1/responses", "mygroup", false},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, tt.expected, isCodexPath(tt.path, tt.groupName))
			})
		}
	})

	t.Run("rewriteCodexPathToOpenAIGeneric", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name     string
			input    string
			expected string
		}{
			{"basic", "/proxy/group/codex/v1/responses", "/proxy/group/v1/responses"},
			{"compact", "/proxy/group/codex/v1/responses/compact", "/proxy/group/v1/responses/compact"},
			{"group_named_codex", "/proxy/codex/codex/v1/responses", "/proxy/codex/v1/responses"},
			{"no_codex", "/proxy/group/v1/responses", "/proxy/group/v1/responses"},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, tt.expected, rewriteCodexPathToOpenAIGeneric(tt.input))
			})
		}
	})

	t.Run("isCodexSupportEnabled", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name     string
			group    *models.Group
			expected bool
		}{
			{"nil_group", nil, false},
			{"openai_enabled", &models.Group{ChannelType: "openai", Config: map[string]any{"codex_support": true}}, true},
			{"responses_native_no_force_switch", &models.Group{ChannelType: "openai-response", Config: map[string]any{"codex_support": true}}, false},
			{"anthropic_enabled", &models.Group{ChannelType: "anthropic", Config: map[string]any{"codex_support": true}}, true},
			{"gemini_disabled", &models.Group{ChannelType: "gemini", Config: map[string]any{"codex_support": true}}, false},
			{"missing_flag", &models.Group{ChannelType: "openai", Config: map[string]any{}}, false},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, tt.expected, isCodexSupportEnabled(tt.group))
			})
		}
	})

	t.Run("isCodexEndpointSupported", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name     string
			group    *models.Group
			expected bool
		}{
			{"nil_group", nil, false},
			{"openai_force_enabled", &models.Group{ChannelType: "openai", Config: map[string]any{"codex_support": true}}, true},
			{"openai_force_disabled", &models.Group{ChannelType: "openai", Config: map[string]any{}}, false},
			{"anthropic_force_enabled", &models.Group{ChannelType: "anthropic", Config: map[string]any{"codex_support": true}}, true},
			{"responses_native", &models.Group{ChannelType: "openai-response", Config: map[string]any{}}, true},
			{"gemini_unsupported", &models.Group{ChannelType: "gemini", Config: map[string]any{"codex_support": true}}, false},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, tt.expected, isCodexEndpointSupported(tt.group))
			})
		}
	})

	t.Run("codex_and_claude_paths_do_not_overlap", func(t *testing.T) {
		t.Parallel()
		groupName := "both"
		assert.True(t, isClaudePath("/proxy/both/claude/v1/messages", groupName))
		assert.False(t, isCodexPath("/proxy/both/claude/v1/messages", groupName))
		assert.True(t, isCodexPath("/proxy/both/codex/v1/responses", groupName))
		assert.False(t, isClaudePath("/proxy/both/codex/v1/responses", groupName))
	})

	t.Run("openai_responses_compact_is_codex_endpoint_only", func(t *testing.T) {
		t.Parallel()
		assert.True(t, isOpenAIResponsesCodexEndpoint("/proxy/group/v1/responses/compact"))
		assert.False(t, isOpenAIResponsesEndpoint("/proxy/group/v1/responses/compact"))
	})
}

func TestConvertCodexRequestToOpenAIChat(t *testing.T) {
	t.Parallel()

	req := &CodexRequest{
		Model:           "gpt-test",
		Instructions:    "Be concise.",
		Input:           json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"What time is it?"}]},{"type":"function_call","call_id":"call_lookup","name":"lookup_time","arguments":"{\"city\":\"Shanghai\"}"},{"type":"function_call_output","call_id":"call_lookup","output":"10:00"}]`),
		MaxOutputTokens: intPtr(512),
		Stream:          true,
		Tools: []CodexTool{{
			Type:        "function",
			Name:        "lookup_time",
			Description: "Lookup time",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ToolChoice: "required",
	}

	got, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)
	require.Len(t, got.Messages, 4)
	assert.Equal(t, "system", got.Messages[0].Role)
	assert.JSONEq(t, `"Be concise."`, string(got.Messages[0].Content))
	assert.Equal(t, "user", got.Messages[1].Role)
	assert.Equal(t, "assistant", got.Messages[2].Role)
	require.Len(t, got.Messages[2].ToolCalls, 1)
	assert.Equal(t, "lookup_time", got.Messages[2].ToolCalls[0].Function.Name)
	assert.Equal(t, "tool", got.Messages[3].Role)
	assert.Equal(t, "call_lookup", got.Messages[3].ToolCallID)
	require.Len(t, got.Tools, 1)
	assert.Equal(t, "lookup_time", got.Tools[0].Function.Name)
	assert.Equal(t, "required", got.ToolChoice)
	require.NotNil(t, got.MaxTokens)
	assert.Equal(t, 512, *got.MaxTokens)
	assert.True(t, got.Stream)
}

func TestConvertCodexRequestToOpenAIChatPreservesCodexToolKinds(t *testing.T) {
	t.Parallel()

	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"custom_tool_call","call_id":"call_custom","name":"apply_patch","input":"*** Begin Patch"},
			{"type":"tool_search_call","call_id":"call_search","arguments":{"query":"gmail","limit":2}},
			{"type":"function_call","call_id":"call_ns","namespace":"mcp__gmail","name":"send_email","arguments":"{\"to\":\"a@example.com\"}"},
			{"type":"custom_tool_call_output","call_id":"call_custom","output":"ok"},
			{"type":"tool_search_output","call_id":"call_search","output":"[]"}
		]`),
		Tools: []CodexTool{
			{Type: "custom", Name: "apply_patch", Description: "Apply a patch"},
			{Type: "tool_search"},
			{
				Type: "namespace",
				Name: "mcp__gmail",
				Tools: []CodexTool{{
					Type:        "function",
					Name:        "send_email",
					Description: "Send email",
					Parameters:  json.RawMessage(`{"type":"object","properties":{"to":{"type":"string"}}}`),
				}},
			},
		},
		ToolChoice: map[string]any{"type": "function", "namespace": "mcp__gmail", "name": "send_email"},
	}

	got, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)

	require.Len(t, got.Tools, 3)
	assert.Equal(t, "apply_patch", got.Tools[0].Function.Name)
	assert.JSONEq(t, `{"type":"object","properties":{"input":{"type":"string","description":"Raw string input for the original custom tool."}},"required":["input"]}`, string(got.Tools[0].Function.Parameters))
	assert.Equal(t, "tool_search", got.Tools[1].Function.Name)
	assert.Equal(t, "mcp__gmail__send_email", got.Tools[2].Function.Name)
	assert.Equal(t, map[string]any{
		"type": "function",
		"function": map[string]string{
			"name": "mcp__gmail__send_email",
		},
	}, got.ToolChoice)

	require.Len(t, got.Messages, 5)
	assert.Equal(t, "apply_patch", got.Messages[0].ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"input":"*** Begin Patch"}`, got.Messages[0].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "tool_search", got.Messages[1].ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"query":"gmail","limit":2}`, got.Messages[1].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "mcp__gmail__send_email", got.Messages[2].ToolCalls[0].Function.Name)
	assert.Equal(t, "tool", got.Messages[3].Role)
	assert.Equal(t, "call_custom", got.Messages[3].ToolCallID)
	assert.Equal(t, "tool", got.Messages[4].Role)
	assert.Equal(t, "call_search", got.Messages[4].ToolCallID)
}

func TestConvertCodexRequestToOpenAIChatPreservesNestedNamespaces(t *testing.T) {
	t.Parallel()

	req := &CodexRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_nested","namespace":"root__child","name":"run","arguments":"{\"value\":1}"}
		]`),
		Tools: []CodexTool{{
			Type: "namespace",
			Name: "root",
			Tools: []CodexTool{{
				Type: "namespace",
				Name: "child",
				Tools: []CodexTool{{
					Type:       "function",
					Name:       "run",
					Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}}}`),
				}},
			}},
		}},
		ToolChoice: map[string]any{"type": "function", "namespace": "root__child", "name": "run"},
	}

	got, err := convertCodexRequestToOpenAIChat(req)
	require.NoError(t, err)

	require.Len(t, got.Tools, 1)
	assert.Equal(t, "root__child__run", got.Tools[0].Function.Name)
	assert.Equal(t, map[string]any{
		"type": "function",
		"function": map[string]string{
			"name": "root__child__run",
		},
	}, got.ToolChoice)
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "root__child__run", got.Messages[0].ToolCalls[0].Function.Name)
}

func TestConvertOpenAIChatResponseToCodex(t *testing.T) {
	t.Parallel()

	finish := "tool_calls"
	content := "Checking."
	openaiResp := &OpenAIResponse{
		ID:      "chatcmpl_1",
		Object:  "chat.completion",
		Created: 123,
		Model:   "gpt-test",
		Choices: []OpenAIChoice{{
			Index: 0,
			Message: &OpenAIRespMessage{
				Role:    "assistant",
				Content: &content,
				ToolCalls: []OpenAIToolCall{{
					ID:   "call_lookup",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "lookup_time",
						Arguments: `{"city":"Shanghai"}`,
					},
				}},
			},
			FinishReason: &finish,
		}},
		Usage: &OpenAIUsage{
			PromptTokens:     7,
			CompletionTokens: 11,
			TotalTokens:      18,
			PromptTokensDetails: &TokenUsageDetails{
				CachedTokens: 3,
			},
			CompletionTokensDetails: &TokenUsageDetails{
				ReasoningTokens: 5,
			},
		},
	}

	got := convertOpenAIChatToCodexResponse(openaiResp, "")
	assert.Equal(t, "chatcmpl_1", got.ID)
	assert.Equal(t, "response", got.Object)
	assert.Equal(t, "completed", got.Status)
	require.Len(t, got.Output, 2)
	assert.Equal(t, "message", got.Output[0].Type)
	assert.Equal(t, "output_text", got.Output[0].Content[0].Type)
	assert.Equal(t, "Checking.", got.Output[0].Content[0].Text)
	assert.Equal(t, "function_call", got.Output[1].Type)
	assert.Equal(t, "call_lookup", got.Output[1].CallID)
	assert.Equal(t, "lookup_time", got.Output[1].Name)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 18, got.Usage.TotalTokens)
	require.NotNil(t, got.Usage.InputTokensDetails)
	assert.Equal(t, 3, got.Usage.InputTokensDetails.CachedTokens)
	require.NotNil(t, got.Usage.OutputTokensDetails)
	assert.Equal(t, 5, got.Usage.OutputTokensDetails.ReasoningTokens)
	assert.Equal(t, 3, got.Usage.CacheReadTokens)
	assert.Equal(t, 5, got.Usage.ThinkingTokens)
}

func TestConvertOpenAIChatResponseToCodexRestoresCodexToolKinds(t *testing.T) {
	t.Parallel()

	toolCtx := newCodexToolContext([]CodexTool{
		{Type: "custom", Name: "apply_patch"},
		{Type: "tool_search"},
		{
			Type: "namespace",
			Name: "mcp__gmail",
			Tools: []CodexTool{{
				Type: "function",
				Name: "send_email",
			}},
		},
	})
	openaiResp := &OpenAIResponse{
		ID:      "chatcmpl_1",
		Created: 123,
		Model:   "gpt-test",
		Choices: []OpenAIChoice{{
			Message: &OpenAIRespMessage{
				ToolCalls: []OpenAIToolCall{
					{ID: "call_custom", Type: "function", Function: OpenAIFunctionCall{Name: "apply_patch", Arguments: `{"input":"*** Begin Patch"}`}},
					{ID: "call_search", Type: "function", Function: OpenAIFunctionCall{Name: "tool_search", Arguments: `{"query":"gmail"}`}},
					{ID: "call_ns", Type: "function", Function: OpenAIFunctionCall{Name: "mcp__gmail__send_email", Arguments: `{"to":"a@example.com"}`}},
				},
			},
		}},
	}

	got := convertOpenAIChatToCodexResponse(openaiResp, "", toolCtx)
	require.Len(t, got.Output, 3)
	assert.Equal(t, "custom_tool_call", got.Output[0].Type)
	assert.Equal(t, "apply_patch", got.Output[0].Name)
	assert.Equal(t, "*** Begin Patch", got.Output[0].Input)
	assert.Equal(t, "tool_search_call", got.Output[1].Type)
	assert.Equal(t, "client", got.Output[1].Execution)
	assert.Equal(t, "function_call", got.Output[2].Type)
	assert.Equal(t, "mcp__gmail", got.Output[2].Namespace)
	assert.Equal(t, "send_email", got.Output[2].Name)
}

func TestConvertOpenAIChatResponseToCodexRestoresNestedNamespaces(t *testing.T) {
	t.Parallel()

	toolCtx := newCodexToolContext([]CodexTool{{
		Type: "namespace",
		Name: "root",
		Tools: []CodexTool{{
			Type: "namespace",
			Name: "child",
			Tools: []CodexTool{{
				Type:       "function",
				Name:       "run",
				Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}}}`),
			}},
		}},
	}})
	openaiResp := &OpenAIResponse{
		ID:      "chatcmpl_1",
		Created: 123,
		Model:   "gpt-test",
		Choices: []OpenAIChoice{{
			Message: &OpenAIRespMessage{
				ToolCalls: []OpenAIToolCall{{
					ID:   "call_nested",
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      "root__child__run",
						Arguments: `{"value":1}`,
					},
				}},
			},
		}},
	}

	got := convertOpenAIChatToCodexResponse(openaiResp, "", toolCtx)

	require.Len(t, got.Output, 1)
	assert.Equal(t, "function_call", got.Output[0].Type)
	assert.Equal(t, "run", got.Output[0].Name)
	assert.Equal(t, "root__child", got.Output[0].Namespace)
}

func TestCollectOpenAIChatStreamToResponseUsesToolCallIndex(t *testing.T) {
	t.Parallel()

	stream := []byte(`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{\"a\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"tool_b","arguments":"{\"b\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`)

	openaiResp := collectOpenAIChatStreamToResponse(stream)
	require.Len(t, openaiResp.Choices, 1)
	require.Len(t, openaiResp.Choices[0].Message.ToolCalls, 2)
	assert.Equal(t, "call_a", openaiResp.Choices[0].Message.ToolCalls[0].ID)
	assert.Equal(t, "tool_a", openaiResp.Choices[0].Message.ToolCalls[0].Function.Name)
	assert.Equal(t, `{"a":1}`, openaiResp.Choices[0].Message.ToolCalls[0].Function.Arguments)
	assert.Equal(t, "call_b", openaiResp.Choices[0].Message.ToolCalls[1].ID)
	assert.Equal(t, "tool_b", openaiResp.Choices[0].Message.ToolCalls[1].Function.Name)
	assert.Equal(t, `{"b":2}`, openaiResp.Choices[0].Message.ToolCalls[1].Function.Arguments)

	codexResp := convertOpenAIChatToCodexResponse(openaiResp, "")
	require.Len(t, codexResp.Output, 2)
	assert.Equal(t, "call_a", codexResp.Output[0].CallID)
	assert.Equal(t, "call_b", codexResp.Output[1].CallID)
}

func TestHandleForceCodexStreamingResponseCapturesUpstreamUsage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`data: {"id":"chatcmpl_usage","object":"chat.completion.chunk","created":123,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}

data: {"id":"chatcmpl_usage","object":"chat.completion.chunk","created":123,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":5},"completion_tokens_details":{"reasoning_tokens":3}}}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "event: response.completed")
	usage, source, ok := getTokenUsage(c)
	require.True(t, ok)
	assert.Equal(t, models.TokenUsageSourceUpstream, source)
	assert.Equal(t, int64(12), usage.InputTokens)
	assert.Equal(t, int64(8), usage.OutputTokens)
	assert.Equal(t, int64(20), usage.TotalTokens)
	assert.Equal(t, int64(5), usage.CacheReadTokens)
	assert.Equal(t, int64(3), usage.ThinkingTokens)
	assert.Zero(t, getEstimatedOutputTokens(c))
}

func TestHandleForceCodexStreamingResponseWrapsCompletedEvent(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}

data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	lines := strings.Split(w.Body.String(), "\n")
	var completedPayload map[string]any
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") && !strings.Contains(line, "[DONE]") {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload))
			if payload["type"] == "response.completed" {
				completedPayload = payload
				break
			}
		}
	}
	require.NotNil(t, completedPayload)
	assert.Equal(t, "response.completed", completedPayload["type"])
	responsePayload, ok := completedPayload["response"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "response", responsePayload["object"])
	assert.Equal(t, "deepseek-test", responsePayload["model"])
}

func TestHandleForceCodexNormalResponseConvertsAnthropicError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"busy Bearer sk-proj-12345678901234567890"}}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamClaude)
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexNormalResponse(c, resp)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	var got CodexResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "failed", got.Status)
	require.NotNil(t, got.Error)
	assert.Equal(t, "rate_limit_error", got.Error.Type)
	assert.Equal(t, "busy Bearer [REDACTED_API_KEY]", got.Error.Message)
	assert.NotContains(t, w.Body.String(), "sk-proj")
}

func TestHandleForceCodexNormalResponseConvertsOpenAIChatError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	body := []byte(`{"error":{"type":"rate_limit_error","message":"quota Bearer sk-proj-12345678901234567890"}}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexNormalResponse(c, resp)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	var got CodexResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "failed", got.Status)
	require.NotNil(t, got.Error)
	assert.Equal(t, "rate_limit_error", got.Error.Type)
	assert.Equal(t, "quota Bearer [REDACTED_API_KEY]", got.Error.Message)
	assert.NotContains(t, w.Body.String(), "sk-proj")
}

func TestHandleForceCodexStreamingResponseEmitsFailedForAnthropicError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"try later"}}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamClaude)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	assert.Contains(t, out, "event: response.failed")
	assert.NotContains(t, out, "event: response.completed")
	assert.Contains(t, out, `"type":"overloaded_error"`)
	assert.Contains(t, out, `"message":"try later"`)
}

func TestHandleForceCodexStreamingResponseEmitsFailedForOpenAIChatError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`event: error
data: {"error":{"type":"invalid_request_error","message":"bad request Bearer sk-proj-12345678901234567890"}}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	assert.Contains(t, out, "event: response.failed")
	assert.NotContains(t, out, "event: response.completed")
	assert.Contains(t, out, `"type":"invalid_request_error"`)
	assert.Contains(t, out, `"message":"bad request Bearer [REDACTED_API_KEY]"`)
	assert.NotContains(t, out, "sk-proj")
}

func TestHandleForceCodexStreamingResponseStopsAfterSSEWriteError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	failingWriter := &failAfterWriteResponseWriter{ResponseWriter: c.Writer}
	c.Writer = failingWriter
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	assert.Equal(t, 1, failingWriter.writes)
	assert.Empty(t, w.Body.String())
}

func TestHandleForceCodexStreamingResponseEmitsResponsesDeltas(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"reasoning_content":"Need context. "},"finish_reason":null}]}

data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}

data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	assert.Contains(t, out, "event: response.created")
	assert.Contains(t, out, "event: response.reasoning_summary_text.delta")
	assert.Contains(t, out, `"delta":"Need context."`)
	assert.Contains(t, out, "event: response.output_text.delta")
	assert.Contains(t, out, `"delta":"hello"`)
	assert.Contains(t, out, "event: response.completed")
	assert.Contains(t, out, `"input_tokens":3`)
	assert.NotContains(t, out, "event: response.in_progress")
	assert.NotContains(t, out, "event: response.content_part.added")
	assert.NotContains(t, out, "event: response.output_text.done")
}

func TestHandleForceCodexStreamingResponseEmitsFunctionCallArgumentsDone(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_list","type":"function","function":{"name":"list_mcp_resources","arguments":"{"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl_tools","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	c.Set(ctxKeyCodexToolContext, newCodexToolContext([]CodexTool{{
		Type:       "function",
		Name:       "list_mcp_resources",
		Parameters: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string"},"server":{"type":"string"}}}`),
	}}))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	assert.Contains(t, out, "event: response.function_call_arguments.delta")
	assert.Contains(t, out, "event: response.function_call_arguments.done")
	assert.Contains(t, out, `"arguments":"{}"`)
	assert.Contains(t, out, "event: response.output_item.done")
	assert.Contains(t, out, `"name":"list_mcp_resources"`)
}

func TestHandleForceCodexStreamingResponseEmitsCustomToolInputEvents(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	stream := []byte(`data: {"id":"chatcmpl_custom","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_custom","type":"function","function":{"name":"apply_patch","arguments":"{\"input\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl_custom","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"*** Begin Patch\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	c.Set(ctxKeyCodexToolContext, newCodexToolContext([]CodexTool{{
		Type: "custom",
		Name: "apply_patch",
	}}))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(stream)),
	}

	ps := &ProxyServer{}
	ps.handleForceCodexStreamingResponse(c, resp)

	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	assert.Contains(t, out, "event: response.custom_tool_call_input.delta")
	assert.Contains(t, out, "event: response.custom_tool_call_input.done")
	assert.NotContains(t, out, "event: response.function_call_arguments.delta")
	assert.NotContains(t, out, "event: response.function_call_arguments.done")
	assert.Contains(t, out, `"type":"custom_tool_call"`)
	assert.Contains(t, out, `"name":"apply_patch"`)
	assert.Contains(t, out, `"input":"*** Begin Patch"`)
}

func TestCollectOpenAIChatStreamToResponsePreservesReasoningAndUsageOnlyChunk(t *testing.T) {
	t.Parallel()

	stream := []byte(`data: {"id":"chatcmpl_reason","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"reasoning_content":"think"},"finish_reason":null}]}

data: {"id":"chatcmpl_reason","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}

data: {"id":"chatcmpl_reason","object":"chat.completion.chunk","created":123,"model":"deepseek-test","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}

`)

	openaiResp := collectOpenAIChatStreamToResponse(stream)
	require.Len(t, openaiResp.Choices, 1)
	require.NotNil(t, openaiResp.Choices[0].Message.ReasoningContent)
	assert.Equal(t, "think", *openaiResp.Choices[0].Message.ReasoningContent)
	require.NotNil(t, openaiResp.Usage)
	assert.Equal(t, 7, openaiResp.Usage.TotalTokens)

	codexResp := convertOpenAIChatToCodexResponse(openaiResp, "")
	require.Len(t, codexResp.Output, 2)
	assert.Equal(t, "reasoning", codexResp.Output[0].Type)
	assert.Equal(t, "message", codexResp.Output[1].Type)
	assert.Equal(t, 7, codexResp.Usage.TotalTokens)
}

func TestConvertCodexRequestToClaude(t *testing.T) {
	t.Parallel()

	req := &CodexRequest{
		Model:        "claude-test",
		Instructions: "System prompt",
		Input:        json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"Read file"}]},{"type":"function_call","call_id":"call_read","name":"read_file","arguments":"{\"path\":\"README.md\"}"},{"type":"function_call_output","call_id":"call_read","output":"content"}]`),
		Stream:       true,
		Tools: []CodexTool{{
			Type:        "function",
			Name:        "read_file",
			Description: "Read file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		ToolChoice: map[string]any{"type": "function", "name": "read_file"},
	}

	got, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	assert.Equal(t, "claude-test", got.Model)
	assert.Equal(t, "System prompt", extractSystemContent(got.System))
	require.Len(t, got.Messages, 3)
	assert.Equal(t, "user", got.Messages[0].Role)
	assert.JSONEq(t, `[{"type":"text","text":"Read file"}]`, string(got.Messages[0].Content))
	assert.Equal(t, "assistant", got.Messages[1].Role)
	assert.JSONEq(t, `[{"type":"tool_use","id":"read","name":"read_file","input":{"path":"README.md"}}]`, string(got.Messages[1].Content))
	assert.Equal(t, "user", got.Messages[2].Role)
	assert.JSONEq(t, `[{"type":"tool_result","tool_use_id":"read","content":"content"}]`, string(got.Messages[2].Content))
	require.Len(t, got.Tools, 1)
	assert.Equal(t, "read_file", got.Tools[0].Name)
	assert.JSONEq(t, `{"type":"tool","name":"read_file"}`, string(got.ToolChoice))
	assert.True(t, got.Stream)
}

func TestConvertCodexRequestToClaudePreservesCodexToolKinds(t *testing.T) {
	t.Parallel()

	req := &CodexRequest{
		Model: "claude-test",
		Input: json.RawMessage(`[
			{"type":"custom_tool_call","call_id":"call_custom","name":"apply_patch","input":"*** Begin Patch"},
			{"type":"tool_search_call","call_id":"call_search","arguments":{"query":"gmail"}},
			{"type":"function_call","call_id":"call_ns","namespace":"mcp__gmail","name":"send_email","arguments":"{\"to\":\"a@example.com\"}"}
		]`),
		Tools: []CodexTool{
			{Type: "custom", Name: "apply_patch", Description: "Apply a patch"},
			{Type: "tool_search"},
			{
				Type: "namespace",
				Name: "mcp__gmail",
				Tools: []CodexTool{{
					Type:       "function",
					Name:       "send_email",
					Parameters: json.RawMessage(`{"type":"object","properties":{"to":{"type":"string"}}}`),
				}},
			},
		},
		ToolChoice: map[string]any{"type": "tool_search"},
	}

	got, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, got.Tools, 3)
	assert.Equal(t, "apply_patch", got.Tools[0].Name)
	assert.Equal(t, "tool_search", got.Tools[1].Name)
	assert.Equal(t, "mcp__gmail__send_email", got.Tools[2].Name)
	assert.JSONEq(t, `{"type":"tool","name":"tool_search"}`, string(got.ToolChoice))

	require.Len(t, got.Messages, 3)
	assert.JSONEq(t, `[{"type":"tool_use","id":"custom","name":"apply_patch","input":{"input":"*** Begin Patch"}}]`, string(got.Messages[0].Content))
	assert.JSONEq(t, `[{"type":"tool_use","id":"search","name":"tool_search","input":{"query":"gmail"}}]`, string(got.Messages[1].Content))
	assert.JSONEq(t, `[{"type":"tool_use","id":"ns","name":"mcp__gmail__send_email","input":{"to":"a@example.com"}}]`, string(got.Messages[2].Content))
}

func TestConvertCodexRequestToClaudePreservesNestedNamespaces(t *testing.T) {
	t.Parallel()

	req := &CodexRequest{
		Model: "claude-test",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_nested","namespace":"root__child","name":"run","arguments":"{\"value\":1}"}
		]`),
		Tools: []CodexTool{{
			Type: "namespace",
			Name: "root",
			Tools: []CodexTool{{
				Type: "namespace",
				Name: "child",
				Tools: []CodexTool{{
					Type:       "function",
					Name:       "run",
					Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}}}`),
				}},
			}},
		}},
	}

	got, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)

	require.Len(t, got.Tools, 1)
	assert.Equal(t, "root__child__run", got.Tools[0].Name)
	require.Len(t, got.Messages, 1)
	assert.JSONEq(t, `[{"type":"tool_use","id":"nested","name":"root__child__run","input":{"value":1}}]`, string(got.Messages[0].Content))
}

func TestConvertCodexRequestToClaudeDefaultsInvalidToolArguments(t *testing.T) {
	t.Parallel()

	req := &CodexRequest{
		Model: "claude-test",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_empty","name":"empty_args","arguments":""},
			{"type":"function_call","call_id":"call_invalid","name":"invalid_args","arguments":"not-json"},
			{"type":"function_call","call_id":"call_array","name":"array_args","arguments":"[]"},
			{"type":"function_call","call_id":"call_string","name":"string_args","arguments":"\"foo\""}
		]`),
	}

	got, err := convertCodexRequestToClaude(req)
	require.NoError(t, err)
	require.Len(t, got.Messages, 4)
	assert.JSONEq(t, `[{"type":"tool_use","id":"empty","name":"empty_args","input":{}}]`, string(got.Messages[0].Content))
	assert.JSONEq(t, `[{"type":"tool_use","id":"invalid","name":"invalid_args","input":{}}]`, string(got.Messages[1].Content))
	assert.JSONEq(t, `[{"type":"tool_use","id":"array","name":"array_args","input":{}}]`, string(got.Messages[2].Content))
	assert.JSONEq(t, `[{"type":"tool_use","id":"string","name":"string_args","input":{}}]`, string(got.Messages[3].Content))
}

func TestConvertClaudeResponseToCodex(t *testing.T) {
	t.Parallel()

	stopReason := "tool_use"
	claudeResp := &ClaudeResponse{
		ID:    "msg_1",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-test",
		Content: []ClaudeContentBlock{
			{Type: "text", Text: "Need a file."},
			{Type: "tool_use", ID: "read", Name: "read_file", Input: json.RawMessage(`{"path":"README.md"}`)},
		},
		StopReason: &stopReason,
		Usage: &ClaudeUsage{
			InputTokens:              3,
			OutputTokens:             5,
			CacheCreationInputTokens: 2,
			CacheReadInputTokens:     4,
			ThinkingTokens:           6,
		},
	}

	got := convertClaudeToCodexResponse(claudeResp)
	assert.Equal(t, "msg_1", got.ID)
	assert.Equal(t, "response", got.Object)
	assert.Equal(t, "completed", got.Status)
	require.Len(t, got.Output, 2)
	assert.Equal(t, "message", got.Output[0].Type)
	assert.Equal(t, "Need a file.", got.Output[0].Content[0].Text)
	assert.Equal(t, "function_call", got.Output[1].Type)
	assert.Equal(t, "call_read", got.Output[1].CallID)
	assert.Equal(t, "read_file", got.Output[1].Name)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 20, got.Usage.TotalTokens)
	require.NotNil(t, got.Usage.InputTokensDetails)
	assert.Equal(t, 4, got.Usage.InputTokensDetails.CachedTokens)
	require.NotNil(t, got.Usage.OutputTokensDetails)
	assert.Equal(t, 6, got.Usage.OutputTokensDetails.ReasoningTokens)
	assert.Equal(t, 4, got.Usage.CacheReadTokens)
	assert.Equal(t, 2, got.Usage.CacheWriteTokens)
	assert.Equal(t, 6, got.Usage.ThinkingTokens)
}

func TestConvertClaudeResponseToCodexRestoresCodexToolKinds(t *testing.T) {
	t.Parallel()

	toolCtx := newCodexToolContext([]CodexTool{
		{Type: "custom", Name: "apply_patch"},
		{Type: "tool_search"},
		{
			Type: "namespace",
			Name: "mcp__gmail",
			Tools: []CodexTool{{
				Type: "function",
				Name: "send_email",
			}},
		},
	})
	claudeResp := &ClaudeResponse{
		ID:    "msg_1",
		Model: "claude-test",
		Content: []ClaudeContentBlock{
			{Type: "tool_use", ID: "custom", Name: "apply_patch", Input: json.RawMessage(`{"input":"*** Begin Patch"}`)},
			{Type: "tool_use", ID: "search", Name: "tool_search", Input: json.RawMessage(`{"query":"gmail"}`)},
			{Type: "tool_use", ID: "ns", Name: "mcp__gmail__send_email", Input: json.RawMessage(`{"to":"a@example.com"}`)},
		},
	}

	got := convertClaudeToCodexResponse(claudeResp, toolCtx)
	require.Len(t, got.Output, 3)
	assert.Equal(t, "custom_tool_call", got.Output[0].Type)
	assert.Equal(t, "*** Begin Patch", got.Output[0].Input)
	assert.Equal(t, "tool_search_call", got.Output[1].Type)
	assert.Equal(t, "client", got.Output[1].Execution)
	assert.Equal(t, "function_call", got.Output[2].Type)
	assert.Equal(t, "mcp__gmail", got.Output[2].Namespace)
	assert.Equal(t, "send_email", got.Output[2].Name)
}

func TestHandleProxyForceCodexOpenAIChatNonStreaming(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_force_codex","object":"chat.completion","created":123,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_lookup","type":"function","function":{"name":"lookup_time","arguments":"{\"city\":\"Shanghai\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}`)
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "force-codex-chat", "openai")
	group.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	group.Config = map[string]any{"codex_support": true, "blacklist_threshold": 100}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-force-codex-chat", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	body := []byte(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"time"}]}],"tools":[{"type":"function","name":"lookup_time","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],"stream":false}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+group.Name+"/codex/v1/responses", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: group.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/v1/chat/completions", <-receivedPath)
	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &upstreamPayload))
	assert.Contains(t, upstreamPayload, "messages")
	assert.NotContains(t, upstreamPayload, "input")

	var got CodexResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Output, 1)
	assert.Equal(t, "function_call", got.Output[0].Type)
	assert.Equal(t, "call_lookup", got.Output[0].CallID)
	assert.Equal(t, "lookup_time", got.Output[0].Name)
}

func TestHandleProxyForceCodexOpenAIChatCompactConvertsToChatEndpoint(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_force_codex_compact","object":"chat.completion","created":123,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"summary"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "force-codex-chat-compact", "openai")
	group.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	group.Config = map[string]any{"codex_support": true, "blacklist_threshold": 100}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-force-codex-chat-compact", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	body := []byte(`{"model":"gpt-test","instructions":"Summarize the conversation.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"history"}]}]}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+group.Name+"/codex/v1/responses/compact", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: group.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/v1/chat/completions", <-receivedPath)
	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &upstreamPayload))
	assert.Contains(t, upstreamPayload, "messages")
	assert.NotContains(t, upstreamPayload, "input")

	var got CodexResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Output, 1)
	require.Len(t, got.Output[0].Content, 1)
	assert.Equal(t, "summary", got.Output[0].Content[0].Text)
}

func TestHandleProxyForceCodexAnthropicNonStreaming(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_force_codex","type":"message","role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"read","name":"read_file","input":{"path":"README.md"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":7}}`)
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "force-codex-claude", "anthropic")
	group.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	group.Config = map[string]any{"codex_support": true, "blacklist_threshold": 100}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-force-codex-claude", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	body := []byte(`{"model":"claude-test","instructions":"System prompt","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read"}]}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}],"stream":false}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+group.Name+"/codex/v1/responses", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: group.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/v1/messages", <-receivedPath)
	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &upstreamPayload))
	assert.Contains(t, upstreamPayload, "messages")
	assert.Contains(t, upstreamPayload, "system")
	assert.NotContains(t, upstreamPayload, "input")

	var got CodexResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Output, 1)
	assert.Equal(t, "function_call", got.Output[0].Type)
	assert.Equal(t, "call_read", got.Output[0].CallID)
	assert.Equal(t, "read_file", got.Output[0].Name)
}

func TestHandleProxyForceCodexAnthropicCompactConvertsToMessagesEndpoint(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_force_codex_compact","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"summary"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":7}}`)
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "force-codex-claude-compact", "anthropic")
	group.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	group.Config = map[string]any{"codex_support": true, "blacklist_threshold": 100}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-force-codex-claude-compact", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	body := []byte(`{"model":"claude-test","instructions":"Summarize the conversation.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"history"}]}]}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+group.Name+"/codex/v1/responses/compact", bytes.NewReader(body))
	c.Params = gin.Params{{Key: "group_name", Value: group.Name}}

	ps.HandleProxy(c)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/v1/messages", <-receivedPath)
	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &upstreamPayload))
	assert.Contains(t, upstreamPayload, "messages")
	assert.Contains(t, upstreamPayload, "system")
	assert.NotContains(t, upstreamPayload, "input")

	var got CodexResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Output, 1)
	require.Len(t, got.Output[0].Content, 1)
	assert.Equal(t, "summary", got.Output[0].Content[0].Text)
}

func TestAggregateForceCodexUsesSelectedSubGroupConfig(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_agg_codex","object":"chat.completion","created":123,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "agg-codex-sub", "openai")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	subGroup.Config = map[string]any{
		"codex_support":       true,
		"max_retries":         0,
		"blacklist_threshold": 100,
	}
	require.NoError(t, db.Save(subGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-force-codex",
		ChannelType: "openai-response",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config:      map[string]any{"max_retries": 0},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:         aggregateGroup.ID,
		SubGroupID:      subGroup.ID,
		SubGroupName:    subGroup.Name,
		SubGroupEnabled: true,
		Weight:          100,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-agg-force-codex", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, memStore.Delete(activeKeysListKeyForTest(uint64(aggregateGroup.ID))))
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":false}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/responses", bytes.NewReader(body))
	c.Set(ctxKeyCCEnabled, true)
	c.Set(ctxKeyOriginalFormat, "claude")
	c.Set(ctxKeyOpenAIResponseCC, true)
	c.Set(ctxKeyCodexEnabled, true)
	c.Set(ctxKeyCodexUpstreamFormat, codexUpstreamOpenAIChat)
	c.Set(ctxKeyFunctionCallEnabled, true)
	c.Set(ctxKeyTriggerSignal, "<function_calls>")
	c.Set("cc_was_claude_path", true)
	c.Set("codex_was_codex_path", true)

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/v1/chat/completions", <-receivedPath)
	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &upstreamPayload))
	assert.Contains(t, upstreamPayload, "messages")
	assert.NotContains(t, upstreamPayload, "input")

	var got CodexResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Output, 1)
	assert.Equal(t, "message", got.Output[0].Type)
	assert.Equal(t, "ok", got.Output[0].Content[0].Text)
}

func TestAggregateForceCodexPassthroughNativeResponsesSubGroup(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	ps, memStore := setupTestProxyServerWithStore(t, db)

	receivedPath := make(chan string, 1)
	receivedBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		receivedPath <- r.URL.Path
		receivedBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_native","object":"response","created_at":123,"model":"gpt-test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"native ok"}]}]}`)
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "agg-codex-native-sub", "openai-response")
	subGroup.Upstreams = []byte(`[{"url":"` + upstream.URL + `","weight":100}]`)
	subGroup.Config = map[string]any{
		"max_retries":         0,
		"force_non_stream":    true,
		"blacklist_threshold": 100,
	}
	require.NoError(t, db.Save(subGroup).Error)

	aggregateGroup := &models.Group{
		Name:        "agg-force-codex-native-only",
		ChannelType: "openai-response",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config:      map[string]any{"max_retries": 0},
	}
	require.NoError(t, db.Create(aggregateGroup).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{
		GroupID:            aggregateGroup.ID,
		SubGroupID:         subGroup.ID,
		SubGroupName:       subGroup.Name,
		SubGroupEnabled:    true,
		Weight:             100,
		MinEffectiveWeight: 1,
	}).Error)

	createTestKey(t, db, subGroup.ID, "sk-agg-force-codex-native", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, memStore.Delete(activeKeysListKeyForTest(uint64(aggregateGroup.ID))))
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	cachedAggregate, err := ps.groupManager.GetGroupByName(aggregateGroup.Name)
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":false}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+aggregateGroup.Name+"/v1/responses", bytes.NewReader(body))

	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
	}

	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/v1/responses", <-receivedPath)
	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(<-receivedBody, &upstreamPayload))
	assert.Contains(t, upstreamPayload, "input")
	assert.NotContains(t, upstreamPayload, "messages")
	assert.JSONEq(t, `{"id":"resp_native","object":"response","created_at":123,"model":"gpt-test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"native ok"}]}]}`, w.Body.String())
}

func TestCollectClaudeStreamToResponseKeepsSparseUnclosedBlocks(t *testing.T) {
	t.Parallel()

	body := []byte(strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"first"}}`,
		"",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":"third"}}`,
		"",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":" block"}}`,
		"",
	}, "\n"))

	resp := collectClaudeStreamToResponse(body)

	require.Len(t, resp.Content, 2)
	assert.Equal(t, "first", resp.Content[0].Text)
	assert.Equal(t, "third block", resp.Content[1].Text)
}

func intPtr(v int) *int {
	return &v
}
