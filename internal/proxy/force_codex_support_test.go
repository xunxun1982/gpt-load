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
