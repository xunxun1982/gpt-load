package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	requestmiddleware "gpt-load/internal/middleware"
	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHandleProxyRetriesLeadingRateLimitStreamFailure(t *testing.T) {
	testCases := []struct {
		name      string
		aggregate bool
		stream    bool
	}{
		{name: "standard forced upstream stream", aggregate: false, stream: false},
		{name: "aggregate native stream", aggregate: true, stream: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler, group, requestCount := setupLeadingRateLimitStreamRetryGroup(t, tc.aggregate)
			// Keep force_non_stream disabled: enabling it would bypass the forced
			// upstream-stream conversion that this standard case must exercise.
			body := []byte(fmt.Sprintf(`{"model":"gpt-5","stream":%t,"input":"hello"}`, tc.stream))

			response := runCodexAffinityRequest(t, handler, group.Name, "proxy-a", "thread-rate-limit", "", body)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.NotContains(t, response.Body.String(), "response.failed")
			if tc.stream {
				require.Contains(t, response.Body.String(), "response.completed")
			} else {
				var payload map[string]any
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
				require.Equal(t, "completed", payload["status"])
			}
		})
	}
}

func TestHandleProxyRetriesPreludeEOFForAllSSEChannels(t *testing.T) {
	testCases := []struct {
		name        string
		channelType string
		firstStatus int
		path        string
		requestBody string
		firstStream string
		successBody string
		successMark string
	}{
		{
			name:        "OpenAI Chat",
			channelType: "openai",
			firstStatus: http.StatusNotFound,
			path:        "/v1/chat/completions",
			requestBody: `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			firstStream: "data: {\"id\":\"chatcmpl-pending\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n",
			successBody: "data: {\"id\":\"chatcmpl-ok\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
				"data: [DONE]\n\n",
			successMark: `"content":"ok"`,
		},
		{
			name:        "Anthropic Claude",
			channelType: "anthropic",
			firstStatus: http.StatusNotFound,
			path:        "/v1/messages",
			requestBody: `{"model":"claude-sonnet","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
			firstStream: "event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_pending\",\"content\":[]}}\n\n",
			successBody: "event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ok\",\"content\":[]}}\n\n" +
				"event: content_block_delta\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"event: message_stop\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
			successMark: `"text":"ok"`,
		},
		{
			name:        "Gemini",
			channelType: "gemini",
			firstStatus: http.StatusNotFound,
			path:        "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			requestBody: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			firstStream: "",
			successBody: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}]}\n\n",
			successMark: `"text":"ok"`,
		},
		{
			name:        "OpenAI Responses",
			channelType: "openai-response",
			firstStatus: http.StatusOK,
			path:        "/v1/responses",
			requestBody: `{"model":"gpt-5","stream":true,"input":"hello"}`,
			firstStream: "event: response.created\n" +
				"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n",
			successBody: "event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-ok\",\"status\":\"completed\",\"output\":[]}}\n\n",
			successMark: "response.completed",
		},
	}

	for _, tc := range testCases {
		for _, aggregate := range []bool{false, true} {
			groupType := "standard"
			if aggregate {
				groupType = "aggregate"
			}
			t.Run(fmt.Sprintf("%s/%s", tc.name, groupType), func(t *testing.T) {
				handler, group, requestCount := setupChannelStreamRetryGroup(t, tc.channelType, aggregate, tc.firstStatus, tc.firstStream, tc.successBody, "")
				response := runStreamRetryRequest(t, handler, group.Name, tc.path, tc.requestBody)

				require.Equal(t, http.StatusOK, response.Code)
				require.Equal(t, int32(2), requestCount.Load())
				require.Contains(t, response.Body.String(), tc.successMark)
				require.NotContains(t, response.Body.String(), "upstream_eof")
			})
		}
	}
}

func TestHandleProxyDoesNotRetryPermanentResponsesLogicalFailures(t *testing.T) {
	testCases := []struct {
		name           string
		errorCode      string
		expectedStatus int
	}{
		{name: "invalid request", errorCode: "invalid_request_error", expectedStatus: http.StatusBadRequest},
		{name: "model not found", errorCode: "model_not_found", expectedStatus: http.StatusNotFound},
	}

	for _, tc := range testCases {
		for _, aggregate := range []bool{false, true} {
			groupType := "standard"
			if aggregate {
				groupType = "aggregate"
			}
			t.Run(fmt.Sprintf("%s/%s", tc.name, groupType), func(t *testing.T) {
				handler, group, requestCount, requestLogStore := setupNonStreamResponsesLogicalFailureGroup(t, aggregate, false, tc.errorCode)
				response := runStreamRetryRequest(t, handler, group.Name, "/v1/responses",
					`{"model":"gpt-5","input":"hello","stream":false}`)

				require.Equal(t, http.StatusOK, response.Code)
				require.Equal(t, int32(1), requestCount.Load())
				logEntry := popRecordedRequestLog(t, requestLogStore)
				require.Equal(t, tc.expectedStatus, logEntry.StatusCode)
			})
		}
	}
}

func TestHandleProxyRetriesResponsesIncompleteUpstreamEOFAfterHTTP404(t *testing.T) {
	failure := "event: response.incomplete\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"upstream_eof\"}}}\n\n" +
		"data: [DONE]\n\n"
	success := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"

	for _, aggregate := range []bool{false, true} {
		name := "standard"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			handler, group, requestCount := setupChannelStreamRetryGroup(t, "openai-response", aggregate, http.StatusNotFound, failure, success, "")
			response := runStreamRetryRequest(t, handler, group.Name, "/v1/responses",
				`{"model":"gpt-5","stream":true,"input":"hello"}`)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.Contains(t, response.Body.String(), "response.completed")
			require.NotContains(t, response.Body.String(), "upstream_eof")
		})
	}
}

func TestHandleProxyRetriesHTTPRateLimitWithinRetryBudget(t *testing.T) {
	testCases := []struct {
		name      string
		aggregate bool
	}{
		{name: "standard", aggregate: false},
		{name: "aggregate", aggregate: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			response, requestCount, authorizations := runHTTPRateLimitRetryCase(t, tc.aggregate)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.Contains(t, response.Body.String(), `"content":"ok"`)
			require.NotContains(t, response.Body.String(), "rate_limit_error")
			if tc.aggregate {
				require.Equal(t, "Bearer sk-http-rate-limit-aggregate-a", <-authorizations)
				require.Equal(t, "Bearer sk-http-rate-limit-aggregate-success", <-authorizations)
			}
		})
	}
}

func TestHandleProxyRetriesNonStreamResponsesLogicalRateLimit(t *testing.T) {
	testCases := []struct {
		name         string
		aggregate    bool
		gzipResponse bool
	}{
		{name: "standard", aggregate: false},
		{name: "aggregate", aggregate: true},
		{name: "standard gzip", aggregate: false, gzipResponse: true},
		{name: "aggregate gzip", aggregate: true, gzipResponse: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler, group, requestCount, _ := setupNonStreamResponsesLogicalFailureGroup(t, tc.aggregate, tc.gzipResponse, "rate_limit_exceeded")
			response := runStreamRetryRequest(t, handler, group.Name, "/v1/responses",
				`{"model":"gpt-5","input":"hello","stream":false}`)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.NotContains(t, response.Body.String(), "rate_limit_exceeded")
			require.Contains(t, response.Body.String(), `"status":"completed"`)
		})
	}
}

func TestHandleProxyRetriesLeadingChannelStreamFailures(t *testing.T) {
	testCases := []struct {
		name        string
		channelType string
		aggregate   bool
		path        string
		requestBody string
		firstStream string
		successBody string
	}{
		{
			name:        "OpenAI Chat standard",
			channelType: "openai",
			path:        "/v1/chat/completions",
			requestBody: `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			firstStream: "data: {\"id\":\"chatcmpl-pending\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
				"data: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"temporary request rate limit\"}}\n\n",
			successBody: "data: {\"id\":\"chatcmpl-ok\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name:        "OpenAI Chat aggregate",
			channelType: "openai",
			aggregate:   true,
			path:        "/v1/chat/completions",
			requestBody: `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			firstStream: "data: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"temporary request rate limit\"}}\n\n",
			successBody: "data: {\"id\":\"chatcmpl-ok\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name:        "Anthropic Messages standard",
			channelType: "anthropic",
			path:        "/v1/messages",
			requestBody: `{"model":"claude-sonnet","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
			firstStream: "event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_pending\",\"content\":[]}}\n\n" +
				"event: ping\n" +
				"data: {\"type\":\"ping\"}\n\n" +
				"event: error\n" +
				"data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"temporary request limit\"}}\n\n",
			successBody: "event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ok\",\"content\":[]}}\n\n" +
				"event: content_block_delta\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"event: message_stop\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
		},
		{
			name:        "Anthropic Messages aggregate",
			channelType: "anthropic",
			aggregate:   true,
			path:        "/v1/messages",
			requestBody: `{"model":"claude-sonnet","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
			firstStream: "event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_pending\",\"content\":[]}}\n\n" +
				"event: ping\n" +
				"data: {\"type\":\"ping\"}\n\n" +
				"event: error\n" +
				"data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"temporary request limit\"}}\n\n",
			successBody: "event: message_start\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ok\",\"content\":[]}}\n\n" +
				"event: content_block_delta\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"event: message_stop\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
		},
		{
			name:        "Gemini native standard",
			channelType: "gemini",
			path:        "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			requestBody: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			firstStream: "data: {\"error\":{\"code\":429,\"message\":\"Resource exhausted\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n",
			successBody: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}]}\n\n",
		},
		{
			name:        "Gemini native aggregate",
			channelType: "gemini",
			aggregate:   true,
			path:        "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			requestBody: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			firstStream: "data: {\"error\":{\"code\":429,\"message\":\"Resource exhausted\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n",
			successBody: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}]}\n\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler, group, requestCount := setupChannelStreamRetryGroup(t, tc.channelType, tc.aggregate, http.StatusOK, tc.firstStream, tc.successBody, "")
			response := runStreamRetryRequest(t, handler, group.Name, tc.path, tc.requestBody)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.NotContains(t, response.Body.String(), "rate_limit_error")
			require.Contains(t, response.Body.String(), "ok")
		})
	}
}

func TestHandleProxyReturnsFinalLogicalStreamFailureAfterRetryExhaustion(t *testing.T) {
	const failure = "event: error\n" +
		"data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"temporary request rate limit\"}\n\n"
	handler, group, requestCount := setupChannelStreamRetryGroup(t, "openai", false, http.StatusOK, failure, failure, "")

	response := runStreamRetryRequest(t, handler, group.Name, "/v1/chat/completions",
		`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	require.Equal(t, http.StatusTooManyRequests, response.Code)
	require.Equal(t, int32(2), requestCount.Load())
	require.Contains(t, response.Body.String(), "temporary request rate limit")
}

func TestHandleProxyPreservesHTTPStatusWhenLogicalFailureIsExcludedFromFailover(t *testing.T) {
	const failure = "event: error\n" +
		"data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"temporary request rate limit\"}\n\n"
	for _, tc := range []struct {
		name      string
		aggregate bool
	}{
		{name: "standard"},
		{name: "aggregate", aggregate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, group, requestCount := setupChannelStreamRetryGroup(t, "openai", tc.aggregate, http.StatusOK, failure, failure, "500")
			response := runStreamRetryRequest(t, handler, group.Name, "/v1/chat/completions",
				`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(1), requestCount.Load())
			require.Contains(t, response.Body.String(), "temporary request rate limit")
		})
	}
}

func setupChannelStreamRetryGroup(t *testing.T, channelType string, aggregate bool, firstStatus int, firstStream, successStream, failoverStatusCodes string) (http.Handler, *models.Group, *atomic.Int32) {
	t.Helper()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	requestCount := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requestCount.Add(1) == 1 {
			w.WriteHeader(firstStatus)
			_, _ = io.WriteString(w, firstStream)
			return
		}
		_, _ = io.WriteString(w, successStream)
	}))
	t.Cleanup(upstream.Close)

	suffix := channelType + "-standard"
	if aggregate {
		suffix = channelType + "-aggregate"
	}
	subGroup := createTestGroup(t, db, "stream-retry-"+suffix+"-sub", channelType)
	subGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	subGroup.Config = map[string]any{"max_retries": 1, "blacklist_threshold": 100}
	if failoverStatusCodes != "" {
		subGroup.Config["failover_status_codes"] = failoverStatusCodes
	}
	if !aggregate {
		subGroup.ProxyKeys = "proxy-a"
	}
	require.NoError(t, db.Save(subGroup).Error)

	group := subGroup
	if aggregate {
		group = &models.Group{
			Name:        "stream-retry-" + suffix,
			ProxyKeys:   "proxy-a",
			ChannelType: channelType,
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

	createTestKey(t, db, subGroup.ID, "sk-stream-retry-"+suffix+"-a", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-stream-retry-"+suffix+"-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, group, requestCount
}

func runStreamRetryRequest(t *testing.T, handler http.Handler, groupName, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/proxy/"+groupName+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer proxy-a")
	handler.ServeHTTP(w, req)
	return w
}

func setupLeadingRateLimitStreamRetryGroup(t *testing.T, aggregate bool) (http.Handler, *models.Group, *atomic.Int32) {
	t.Helper()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	requestCount := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requestCount.Add(1) == 1 {
			_, _ = io.WriteString(w, "event: response.created\n"+
				"data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n"+
				"event: response.in_progress\n"+
				"data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n"+
				"event: response.failed\n"+
				"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"Your requests have exceeded rate limit\"}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: response.completed\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(upstream.Close)

	suffix := "standard"
	if aggregate {
		suffix = "aggregate"
	}
	subGroup := createTestGroup(t, db, "stream-rate-limit-"+suffix+"-sub", "openai-response")
	subGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	subGroup.Config = map[string]any{"max_retries": 1, "blacklist_threshold": 100}
	if !aggregate {
		subGroup.ProxyKeys = "proxy-a"
	}
	require.NoError(t, db.Save(subGroup).Error)

	targetGroup := subGroup
	if aggregate {
		targetGroup = &models.Group{
			Name:        "stream-rate-limit-aggregate",
			ProxyKeys:   "proxy-a",
			ChannelType: "openai-response",
			GroupType:   "aggregate",
			Enabled:     true,
			Upstreams:   []byte(`[]`),
			Config:      map[string]any{"max_retries": 0},
		}
		require.NoError(t, db.Create(targetGroup).Error)
		require.NoError(t, db.Create(&models.GroupSubGroup{
			GroupID:         targetGroup.ID,
			SubGroupID:      subGroup.ID,
			SubGroupName:    subGroup.Name,
			SubGroupEnabled: true,
			Weight:          100,
		}).Error)
	}

	createTestKey(t, db, subGroup.ID, "sk-stream-rate-limit-"+suffix+"-a", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-stream-rate-limit-"+suffix+"-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, targetGroup, requestCount
}

func setupNonStreamResponsesLogicalFailureGroup(t *testing.T, aggregate, gzipResponse bool, errorCode string) (http.Handler, *models.Group, *atomic.Int32, store.Store) {
	t.Helper()

	db := setupTestDB(t)
	ps, requestLogStore := setupTestProxyServerWithStore(t, db)
	requestCount := &atomic.Int32{}
	limitedBody := []byte(fmt.Sprintf(`{"id":"resp-limited","status":"failed","error":{"code":%q,"message":"logical failure"},"output":[]}`, errorCode))
	successBody := []byte(`{"id":"resp-ok","status":"completed","error":null,"output":[]}`)
	if gzipResponse {
		limitedBody = compressGzipForResponseHandlerTest(t, limitedBody)
		successBody = compressGzipForResponseHandlerTest(t, successBody)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if gzipResponse {
			w.Header().Set("Content-Encoding", "gzip")
		}
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
	subGroup := createTestGroup(t, db, "responses-logical-rate-limit-"+suffix+"-sub", "openai-response")
	subGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	subGroup.Config = map[string]any{"max_retries": 1, "blacklist_threshold": 100}
	subGroup.EffectiveConfig.EnableRequestBodyLogging = true
	if !aggregate {
		subGroup.ProxyKeys = "proxy-a"
	}
	require.NoError(t, db.Save(subGroup).Error)

	targetGroup := subGroup
	if aggregate {
		targetGroup = &models.Group{
			Name:        "responses-logical-rate-limit-aggregate",
			ProxyKeys:   "proxy-a",
			ChannelType: "openai-response",
			GroupType:   "aggregate",
			Enabled:     true,
			Upstreams:   []byte(`[]`),
			Config:      map[string]any{"max_retries": 0},
		}
		require.NoError(t, db.Create(targetGroup).Error)
		require.NoError(t, db.Create(&models.GroupSubGroup{
			GroupID:         targetGroup.ID,
			SubGroupID:      subGroup.ID,
			SubGroupName:    subGroup.Name,
			SubGroupEnabled: true,
			Weight:          100,
		}).Error)
	}

	createTestKey(t, db, subGroup.ID, "sk-responses-logical-rate-limit-"+suffix+"-a", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-responses-logical-rate-limit-"+suffix+"-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, targetGroup, requestCount, requestLogStore
}

func runHTTPRateLimitRetryCase(t *testing.T, aggregate bool) (*httptest.ResponseRecorder, *atomic.Int32, <-chan string) {
	t.Helper()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	requestCount := &atomic.Int32{}
	authorizations := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if requestCount.Add(1) == 1 {
			// A long Retry-After describes pressure on this upstream. It must not
			// suppress a bounded retry that can rotate to another key or upstream.
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"temporarily limited"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"chatcmpl-ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)

	suffix := "standard"
	if aggregate {
		suffix = "aggregate"
	}
	limitedSubGroup := createTestGroup(t, db, "http-rate-limit-"+suffix+"-limited", "openai")
	limitedSubGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	limitedSubGroup.Config = map[string]any{"max_retries": 1, "blacklist_threshold": 100}
	limitedSubGroup.ProxyKeys = "proxy-a"
	require.NoError(t, db.Save(limitedSubGroup).Error)

	createTestKey(t, db, limitedSubGroup.ID, "sk-http-rate-limit-"+suffix+"-a", ps.encryptionSvc)
	if !aggregate {
		createTestKey(t, db, limitedSubGroup.ID, "sk-http-rate-limit-"+suffix+"-b", ps.encryptionSvc)
		require.NoError(t, ps.keyProvider.LoadKeysFromDB())
		require.NoError(t, ps.groupManager.Initialize())
		t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

		router := gin.New()
		router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
		return runStreamRetryRequest(t, router, limitedSubGroup.Name, "/v1/chat/completions",
			`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`), requestCount, authorizations
	}

	successSubGroup := createTestGroup(t, db, "http-rate-limit-aggregate-success", "openai")
	successSubGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	successSubGroup.Config = map[string]any{"max_retries": 0, "blacklist_threshold": 100}
	require.NoError(t, db.Save(successSubGroup).Error)
	createTestKey(t, db, successSubGroup.ID, "sk-http-rate-limit-aggregate-success", ps.encryptionSvc)

	targetGroup := &models.Group{
		Name:        "http-rate-limit-aggregate",
		ProxyKeys:   "proxy-a",
		ChannelType: "openai",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   []byte(`[]`),
		Config:      map[string]any{"max_retries": 1, "sub_max_retries": 0},
	}
	require.NoError(t, db.Create(targetGroup).Error)
	for _, subGroup := range []*models.Group{limitedSubGroup, successSubGroup} {
		require.NoError(t, db.Create(&models.GroupSubGroup{
			GroupID:         targetGroup.ID,
			SubGroupID:      subGroup.ID,
			SubGroupName:    subGroup.Name,
			SubGroupEnabled: true,
			Weight:          100,
		}).Error)
	}

	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	cachedAggregate, err := ps.groupManager.GetGroupByName(targetGroup.Name)
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/proxy/"+cachedAggregate.Name+"/v1/chat/completions", strings.NewReader(string(body)))
	retryCtx := &retryContext{
		excludedSubGroups:   make(map[uint]bool, len(cachedAggregate.SubGroups)),
		originalBodyBytes:   body,
		originalPath:        c.Request.URL.Path,
		subGroupKeyRetryMap: make(map[uint]int, len(cachedAggregate.SubGroups)),
		forcedSubGroupID:    limitedSubGroup.ID,
	}
	ps.executeRequestWithAggregateRetry(c, nil, cachedAggregate, body, false, time.Now(), retryCtx)
	return w, requestCount, authorizations
}
