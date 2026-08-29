package proxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHandleProxyRetriesReasoningContentPassbackLeadingSSEFailure locks the
// DeepSeek thinking-mode passback rejection ("The `reasoning_content` in the
// thinking mode must be passed back to the API.") as a retryable leading SSE
// failure. The upstream rejects the request before any output, so failover may
// reach a key or upstream that accepts the conversation. The error is typed
// invalid_request_error but is recoverable: it must NOT be treated as a
// permanent logical failure in either the standard or the aggregate path, and
// the client-visible HTTP status (200 or 502) must not close the retry window.
func TestHandleProxyRetriesReasoningContentPassbackLeadingSSEFailure(t *testing.T) {
	reasoningFailure := "data: {\"error\":{\"message\":\"The `reasoning_content` in the thinking mode must be passed back to the API.\",\"type\":\"invalid_request_error\",\"param\":null,\"code\":null}}\n\n"
	success := "data: {\"id\":\"chatcmpl-ok\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: [DONE]\n\n"

	testCases := []struct {
		name        string
		aggregate   bool
		firstStatus int
	}{
		{name: "standard HTTP 200 SSE error", firstStatus: http.StatusOK},
		{name: "aggregate HTTP 200 SSE error", aggregate: true, firstStatus: http.StatusOK},
		{name: "standard HTTP 502 SSE error", firstStatus: http.StatusBadGateway},
		{name: "aggregate HTTP 502 SSE error", aggregate: true, firstStatus: http.StatusBadGateway},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler, group, requestCount := setupChannelStreamRetryGroup(t, "openai", tc.aggregate, tc.firstStatus, reasoningFailure, success, "")
			response := runStreamRetryRequest(t, handler, group.Name, "/v1/chat/completions",
				`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.Contains(t, response.Body.String(), "\"content\":\"ok\"")
			require.NotContains(t, response.Body.String(), "passed back")
		})
	}
}
