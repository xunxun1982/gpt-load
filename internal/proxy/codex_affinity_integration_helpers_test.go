package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	requestmiddleware "gpt-load/internal/middleware"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexAffinityObservation struct {
	upstream string
	auth     string
	turn     string
	body     []byte
}

func newCodexAffinityUpstream(t *testing.T, name string, observations chan<- codexAffinityObservation, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		observations <- codexAffinityObservation{
			upstream: name,
			auth:     r.Header.Get("Authorization"),
			turn:     r.Header.Get("X-Codex-Turn-State"),
			body:     body,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"id":"resp-test","object":"response","created_at":0,"status":"completed","model":"gpt-5","output":[]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func setupStandardCodexAffinityGroup(t *testing.T, enabled bool, rules []models.HeaderRule) (http.Handler, *models.Group, <-chan codexAffinityObservation) {
	t.Helper()
	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	observations := make(chan codexAffinityObservation, 8)
	upstreamA := newCodexAffinityUpstream(t, "a", observations, http.StatusOK)
	upstreamB := newCodexAffinityUpstream(t, "b", observations, http.StatusOK)
	group := createTestGroup(t, db, fmt.Sprintf("standard-affinity-%t", enabled), "openai-response")
	group.ProxyKeys = "proxy-a,proxy-b"
	group.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100},{"url":%q,"weight":100}]`, upstreamA.URL, upstreamB.URL))
	group.Config = map[string]any{
		"max_retries":            0,
		"force_non_stream":       true,
		"codex_affinity_enabled": enabled,
	}
	group.HeaderRuleList = rules
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-affinity-a", ps.encryptionSvc)
	createTestKey(t, db, group.ID, "sk-affinity-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })
	if len(rules) > 0 {
		cached, err := ps.groupManager.GetGroupByName(group.Name)
		require.NoError(t, err)
		cached.HeaderRuleList = rules
	}
	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, group, observations
}

func setupRetryingStandardCodexAffinityGroup(t *testing.T) (http.Handler, *models.Group, <-chan codexAffinityObservation) {
	t.Helper()
	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	observations := make(chan codexAffinityObservation, 2)
	var requestCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Non-blocking: an unexpected extra attempt must fail via the
		// observation count asserted by the caller instead of hanging the
		// upstream handler.
		select {
		case observations <- codexAffinityObservation{auth: r.Header.Get("Authorization"), turn: r.Header.Get("X-Codex-Turn-State"), body: body}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		if requestCount.Add(1) == 1 {
			http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"id":"resp-test","object":"response","status":"completed","model":"gpt-5","output":[]}`)
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "standard-affinity-retry", "openai-response")
	group.ProxyKeys = "proxy-a"
	group.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	group.ModelRedirectRules = map[string]any{"gpt-source": "gpt-target"}
	group.Config = map[string]any{
		"max_retries":                           1,
		"blacklist_threshold":                   100,
		"force_non_stream":                      true,
		"codex_affinity_enabled":                true,
		"codex_affinity_max_retries":            1,
		"responses_include_encrypted_reasoning": true,
	}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-affinity-retry-a", ps.encryptionSvc)
	createTestKey(t, db, group.ID, "sk-affinity-retry-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, group, observations
}

func setupStreamingRetryingStandardCodexAffinityGroup(t *testing.T) (http.Handler, *models.Group, <-chan codexAffinityObservation) {
	t.Helper()
	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	observations := make(chan codexAffinityObservation, 4)
	var requestCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Non-blocking: an unexpected extra attempt must fail via the
		// observation count asserted by the caller instead of hanging the
		// upstream handler.
		select {
		case observations <- codexAffinityObservation{auth: r.Header.Get("Authorization"), turn: r.Header.Get("X-Codex-Turn-State"), body: body}:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requestCount.Add(1) == 2 {
			_, _ = io.WriteString(w, "event: response.failed\n"+
				"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"The encrypted content could not be verified or decrypted\"}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: response.completed\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(upstream.Close)

	group := createTestGroup(t, db, "standard-affinity-stream-retry", "openai-response")
	group.ProxyKeys = "proxy-a"
	group.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	group.Config = map[string]any{
		"max_retries":                0,
		"blacklist_threshold":        100,
		"codex_affinity_enabled":     true,
		"codex_affinity_max_retries": 2,
	}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-affinity-stream-a", ps.encryptionSvc)
	createTestKey(t, db, group.ID, "sk-affinity-stream-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	return router, group, observations
}

func runStandardCodexAffinityRequest(t *testing.T, handler http.Handler, groupName, proxyKey, turn string, body []byte) *httptest.ResponseRecorder {
	return runCodexAffinityRequest(t, handler, groupName, proxyKey, "thread-1", turn, body)
}

func runCodexAffinityRequest(t *testing.T, handler http.Handler, groupName, proxyKey, thread, turn string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/proxy/"+groupName+"/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+proxyKey)
	req.Header.Set("Thread-Id", thread)
	if turn != "" {
		req.Header.Set("X-Codex-Turn-State", turn)
	}
	handler.ServeHTTP(w, req)
	return w
}
