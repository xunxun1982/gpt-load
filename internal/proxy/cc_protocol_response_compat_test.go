package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCCProtocolResponseCompatStopAndUsage(t *testing.T) {
	var upstream OpenAIResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"chatcmpl_1","model":"gpt-test",
		"choices":[{"index":0,"message":{"role":"assistant","content":"blocked"},"finish_reason":"content_filter","stop_sequence":"END"}],
		"usage":{"prompt_tokens":20,"completion_tokens":12,"total_tokens":32,
			"prompt_tokens_details":{"cached_tokens":7,"cache_write_tokens":3},
			"completion_tokens_details":{"reasoning_tokens":11}}
	}`), &upstream))

	got := convertOpenAIToClaudeResponse(&upstream, cleanupModeArtifactsOnly, false, nil)
	require.NotNil(t, got.StopReason)
	require.Equal(t, "refusal", *got.StopReason)
	require.NotNil(t, got.StopSequence)
	require.Equal(t, "END", *got.StopSequence)
	require.Equal(t, 7, got.Usage.CacheReadInputTokens)
	require.Equal(t, 3, got.Usage.CacheCreationInputTokens)
	require.Equal(t, 11, got.Usage.ThinkingTokens)
}

func TestCCProtocolResponseCompatKnownStopReasons(t *testing.T) {
	tests := map[string]string{
		"stop":                          "end_turn",
		"length":                        "max_tokens",
		"tool_calls":                    "tool_use",
		"content_filter":                "refusal",
		"pause_turn":                    "pause_turn",
		"context_length_exceeded":       "model_context_window_exceeded",
		"model_context_window_exceeded": "model_context_window_exceeded",
	}
	for upstream, want := range tests {
		got, isError := convertFinishReasonToStopReason(upstream)
		require.False(t, isError, upstream)
		require.Equal(t, want, got, upstream)
	}
	for _, upstream := range []string{"network_error", "error", "timeout"} {
		got, isError := convertFinishReasonToStopReason(upstream)
		require.True(t, isError, upstream)
		require.Equal(t, "end_turn", got, upstream)
	}
}

func TestCCProtocolResponseCompatErrorPreserved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"type":"invalid_request_error","message":"bad request","param":"messages","code":"invalid"}}`,
		)),
	}

	(&ProxyServer{}).handleCCNormalResponse(ctx, resp)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var got ClaudeErrorResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &got))
	require.Equal(t, "invalid_request_error", got.Error.Type)
	require.Equal(t, "bad request", got.Error.Message)
}

func TestCCProtocolResponseCompatPlainReasoningDegradesToThinking(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"private"},"finish_reason":"stop"}]}`,
		)),
	}

	(&ProxyServer{}).handleCCNormalResponse(ctx, resp)
	require.Equal(t, http.StatusOK, recorder.Code)
	var got ClaudeResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &got))
	require.Len(t, got.Content, 1)
	require.Equal(t, "thinking", got.Content[0].Type)
	require.Equal(t, "private", got.Content[0].Thinking)
}
