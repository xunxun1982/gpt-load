package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	requestmiddleware "gpt-load/internal/middleware"
	"gpt-load/internal/models"

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
			body := []byte(fmt.Sprintf(`{"model":"gpt-5","stream":%t,"input":"hello"}`, tc.stream))

			response := runCodexAffinityRequest(t, handler, group.Name, "proxy-a", "thread-rate-limit", "", body)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.NotContains(t, response.Body.String(), "response.failed")
			if tc.stream {
				require.Contains(t, response.Body.String(), "response.completed")
			} else {
				require.Contains(t, response.Body.String(), `"status":"completed"`)
			}
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
			handler, group, requestCount := setupChannelStreamRetryGroup(t, tc.channelType, tc.aggregate, tc.firstStream, tc.successBody)
			response := runStreamRetryRequest(t, handler, group.Name, tc.path, tc.requestBody)

			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, int32(2), requestCount.Load())
			require.NotContains(t, response.Body.String(), "rate_limit_error")
			require.Contains(t, response.Body.String(), "ok")
		})
	}
}

func setupChannelStreamRetryGroup(t *testing.T, channelType string, aggregate bool, firstStream, successStream string) (http.Handler, *models.Group, *atomic.Int32) {
	t.Helper()

	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	requestCount := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requestCount.Add(1) == 1 {
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
