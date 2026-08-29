package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	requestmiddleware "gpt-load/internal/middleware"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// DeepSeek rejects thinking-mode conversations that do not pass the previous
// reasoning_content back. The error is typed invalid_request_error but is
// recoverable: failover to another key or upstream may accept the request, so
// isPermanentLogicalFailure must NOT classify it as permanent and both the
// standard and the aggregate paths must retry it (default failover status codes
// cover HTTP 400/502). These integration tests lock that contract end to end.

// TestHandleProxyRetriesReasoningContentPassbackResponsesStreaming covers the
// OpenAI Responses streaming endpoint: the upstream returns HTTP 200 SSE with a
// retryable response.created prelude followed by response.failed carrying the
// reasoning_content passback error. The retry must reach a second upstream
// attempt and surface only the success marker.
func TestHandleProxyRetriesReasoningContentPassbackResponsesStreaming(t *testing.T) {
	firstStream := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"event: response.failed\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"invalid_request_error\",\"message\":\"The `reasoning_content` in the thinking mode must be passed back to the API.\"}}}\n\n"
	success := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-ok\",\"status\":\"completed\",\"output\":[]}}\n\n"

	for _, aggregate := range []bool{false, true} {
		name := "standard"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			handler, group, requestCount := setupChannelStreamRetryGroup(t, "openai-response", aggregate, http.StatusOK, firstStream, success, "")
			response := runStreamRetryRequest(t, handler, group.Name, "/v1/responses",
				`{"model":"gpt-5","stream":true,"input":"hello"}`)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.Contains(t, response.Body.String(), "response.completed")
			require.NotContains(t, response.Body.String(), "passed back")
		})
	}
}

// TestHandleProxyRetriesReasoningContentPassbackChatNonStream502 covers the
// OpenAI Chat non-streaming endpoint with an HTTP 502 plus a pure JSON error
// body (Content-Type application/json, not SSE). The non-stream JSON probe
// only applies to the Responses endpoint, so the chat path is judged purely
// by failover_status_codes -- the default matcher (400-403,405-999) includes
// 502, which is exactly what makes this reasoning_content passback retryable.
func TestHandleProxyRetriesReasoningContentPassbackChatNonStream502(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		name := "standard"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			handler, group, requestCount := setupChatNonStreamReasoningRetryGroup(t, aggregate)
			response := runStreamRetryRequest(t, handler, group.Name, "/v1/chat/completions",
				`{"model":"gpt-5","stream":false,"messages":[{"role":"user","content":"hello"}]}`)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.Contains(t, response.Body.String(), "\"content\":\"ok\"")
			require.NotContains(t, response.Body.String(), "passed back")
		})
	}
}

// TestHandleProxyRetriesReasoningContentPassbackResponsesNonStream covers the
// Responses non-streaming logical-failure path. With status failed and
// error.code invalid_request_error, the reasoning_content passback message
// must retry successfully (requestCount == 2), while an ordinary
// invalid_request_error message ("logical failure") must stay a permanent
// failure (requestCount == 1), consistent with
// TestHandleProxyDoesNotRetryPermanentResponsesLogicalFailures.
func TestHandleProxyRetriesReasoningContentPassbackResponsesNonStream(t *testing.T) {
	testCases := []struct {
		name         string
		aggregate    bool
		errorMessage string
		wantRetries  bool
	}{
		{name: "standard reasoning passback retries", errorMessage: "The `reasoning_content` in the thinking mode must be passed back to the API.", wantRetries: true},
		{name: "aggregate reasoning passback retries", aggregate: true, errorMessage: "The `reasoning_content` in the thinking mode must be passed back to the API.", wantRetries: true},
		{name: "standard ordinary invalid_request_error stays permanent", errorMessage: "logical failure"},
		{name: "aggregate ordinary invalid_request_error stays permanent", aggregate: true, errorMessage: "logical failure"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var handler http.Handler
			var group *models.Group
			var requestCount *atomic.Int32
			if tc.wantRetries {
				handler, group, requestCount = setupNonStreamResponsesReasoningFailureGroup(t, tc.aggregate, tc.errorMessage)
			} else {
				// Reuse the stock helper whose message is hardcoded to "logical failure".
				h, g, rc, _ := setupNonStreamResponsesLogicalFailureGroup(t, tc.aggregate, false, "invalid_request_error")
				handler, group, requestCount = h, g, rc
			}

			response := runStreamRetryRequest(t, handler, group.Name, "/v1/responses",
				`{"model":"gpt-5","input":"hello","stream":false}`)

			require.Equal(t, http.StatusOK, response.Code)
			if tc.wantRetries {
				require.Equal(t, int32(2), requestCount.Load())
				require.Contains(t, response.Body.String(), "\"status\":\"completed\"")
				require.NotContains(t, response.Body.String(), "passed back")
			} else {
				// Permanent logical failure: the proxy forwards a synthetic empty
				// completed response and never retries (consistent with
				// TestHandleProxyDoesNotRetryPermanentResponsesLogicalFailures).
				require.Equal(t, int32(1), requestCount.Load())
				require.Contains(t, response.Body.String(), "\"status\":\"completed\"")
				require.NotContains(t, response.Body.String(), "passed back")
			}
		})
	}
}

// TestHandleProxyRetriesReasoningContentPassbackMessageCase locks the
// case-insensitive message match: an uppercase REASONING_CONTENT marker (as
// observed from some DeepSeek deployments) must still be classified as a
// retryable passback failure.
func TestHandleProxyRetriesReasoningContentPassbackMessageCase(t *testing.T) {
	firstStream := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"event: response.failed\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"invalid_request_error\",\"message\":\"The REASONING_CONTENT in the thinking mode must be passed back to the API.\"}}}\n\n"
	success := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-ok\",\"status\":\"completed\",\"output\":[]}}\n\n"

	for _, aggregate := range []bool{false, true} {
		name := "standard"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			handler, group, requestCount := setupChannelStreamRetryGroup(t, "openai-response", aggregate, http.StatusOK, firstStream, success, "")
			response := runStreamRetryRequest(t, handler, group.Name, "/v1/responses",
				`{"model":"gpt-5","stream":true,"input":"hello"}`)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.Contains(t, response.Body.String(), "response.completed")
			require.NotContains(t, response.Body.String(), "passed back")
		})
	}
}

// setupChatNonStreamReasoningRetryGroup builds an openai (chat) group whose
// upstream answers the first request with HTTP 502 and a pure JSON
// reasoning_content error body, then the second request with a success body.
// The non-stream JSON probe only runs for the Responses endpoint, so this
// variant depends on failover_status_codes to retry.
func setupChatNonStreamReasoningRetryGroup(t *testing.T, aggregate bool) (http.Handler, *models.Group, *atomic.Int32) {
	t.Helper()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	requestCount := &atomic.Int32{}
	failureBody := []byte("{\"error\":{\"message\":\"The `reasoning_content` in the thinking mode must be passed back to the API.\",\"type\":\"invalid_request_error\"}}")
	successBody := []byte(`{"id":"chatcmpl-ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}}]}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requestCount.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(failureBody)
			return
		}
		_, _ = w.Write(successBody)
	}))
	t.Cleanup(upstream.Close)

	suffix := "standard"
	if aggregate {
		suffix = "aggregate"
	}
	subGroup := createTestGroup(t, db, "chat-reasoning-passback-"+suffix+"-sub", "openai")
	subGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	subGroup.Config = map[string]any{"max_retries": 1, "blacklist_threshold": 100}
	if !aggregate {
		subGroup.ProxyKeys = "proxy-a"
	}
	require.NoError(t, db.Save(subGroup).Error)

	group := subGroup
	if aggregate {
		group = &models.Group{
			Name:        "chat-reasoning-passback-aggregate",
			ProxyKeys:   "proxy-a",
			ChannelType: "openai",
			GroupType:   "aggregate",
			Enabled:     true,
			Upstreams:   []byte(`[]`),
			Config:      map[string]any{"max_retries": 0},
		}
		require.NoError(t, db.Create(group).Error)
		require.NoError(t, db.Create(&models.GroupSubGroup{
			GroupID:         group.ID,
			SubGroupID:      subGroup.ID,
			SubGroupName:    subGroup.Name,
			SubGroupEnabled: true,
			Weight:          100,
		}).Error)
	}

	createTestKey(t, db, subGroup.ID, "sk-chat-reasoning-passback-"+suffix+"-a", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-chat-reasoning-passback-"+suffix+"-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, group, requestCount
}

// setupNonStreamResponsesReasoningFailureGroup mirrors the stock
// setupNonStreamResponsesLogicalFailureGroup but sends a configurable error
// message so the reasoning_content passback text can be exercised on the
// Responses non-streaming logical-failure path.
func setupNonStreamResponsesReasoningFailureGroup(t *testing.T, aggregate bool, errorMessage string) (http.Handler, *models.Group, *atomic.Int32) {
	t.Helper()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	requestCount := &atomic.Int32{}
	limitedBody := []byte(fmt.Sprintf(`{"id":"resp-limited","status":"failed","error":{"code":"invalid_request_error","message":%q},"output":[]}`, errorMessage))
	successBody := []byte(`{"id":"resp-ok","status":"completed","error":null,"output":[]}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requestCount.Add(1) == 1 {
			_, _ = w.Write(limitedBody)
			return
		}
		_, _ = w.Write(successBody)
	}))
	t.Cleanup(upstream.Close)

	suffix := "standard"
	if aggregate {
		suffix = "aggregate"
	}
	subGroup := createTestGroup(t, db, "responses-reasoning-passback-"+suffix+"-sub", "openai-response")
	subGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	subGroup.Config = map[string]any{"max_retries": 1, "blacklist_threshold": 100}
	if !aggregate {
		subGroup.ProxyKeys = "proxy-a"
	}
	require.NoError(t, db.Save(subGroup).Error)

	group := subGroup
	if aggregate {
		group = &models.Group{
			Name:        "responses-reasoning-passback-aggregate",
			ProxyKeys:   "proxy-a",
			ChannelType: "openai-response",
			GroupType:   "aggregate",
			Enabled:     true,
			Upstreams:   []byte(`[]`),
			Config:      map[string]any{"max_retries": 0},
		}
		require.NoError(t, db.Create(group).Error)
		require.NoError(t, db.Create(&models.GroupSubGroup{
			GroupID:         group.ID,
			SubGroupID:      subGroup.ID,
			SubGroupName:    subGroup.Name,
			SubGroupEnabled: true,
			Weight:          100,
		}).Error)
	}

	createTestKey(t, db, subGroup.ID, "sk-responses-reasoning-passback-"+suffix+"-a", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-responses-reasoning-passback-"+suffix+"-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, group, requestCount
}
