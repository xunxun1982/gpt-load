package proxy

import (
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

func TestHandleProxyAggregateCodexAffinityRetriesLeadingEncryptedContentFailureClean(t *testing.T) {
	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	observations := make(chan codexAffinityObservation, 4)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Non-blocking: an unexpected extra attempt must fail via the
		// requests count assertion below instead of hanging the handler.
		select {
		case observations <- codexAffinityObservation{auth: r.Header.Get("Authorization"), body: body}:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 2 {
			_, _ = io.WriteString(w, "event: response.failed\n"+
				"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"The encrypted content could not be decrypted or parsed\"}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: response.completed\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(upstream.Close)

	subGroup := createTestGroup(t, db, "aggregate-affinity-stream-sub", "openai-response")
	subGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, upstream.URL))
	subGroup.Config = map[string]any{"max_retries": 0, "blacklist_threshold": 100}
	require.NoError(t, db.Save(subGroup).Error)
	parent := &models.Group{
		Name: "aggregate-affinity-stream-parent", ProxyKeys: "proxy-a", ChannelType: "openai-response",
		GroupType: "aggregate", Enabled: true, Upstreams: []byte(`[]`),
		Config: map[string]any{"max_retries": 0, "codex_affinity_enabled": true, "codex_affinity_max_retries": 2},
	}
	require.NoError(t, db.Create(parent).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{GroupID: parent.ID, SubGroupID: subGroup.ID, SubGroupName: subGroup.Name, SubGroupEnabled: true, Weight: 100}).Error)
	createTestKey(t, db, subGroup.ID, "sk-aggregate-stream-a", ps.encryptionSvc)
	createTestKey(t, db, subGroup.ID, "sk-aggregate-stream-b", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	body := []byte(`{"model":"gpt-5","stream":true,"include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":"hello"},{"type":"reasoning","encrypted_content":"cipher"}]}`)
	require.Equal(t, http.StatusOK, runCodexAffinityRequest(t, router, parent.Name, "proxy-a", "thread-aggregate", "", body).Code)
	warmup := <-observations
	require.NotContains(t, string(warmup.body), "reasoning.encrypted_content")

	response := runCodexAffinityRequest(t, router, parent.Name, "proxy-a", "thread-aggregate", "", body)
	failed := <-observations
	retried := <-observations
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "response.failed")
	require.Contains(t, response.Body.String(), "response.completed")
	require.Contains(t, string(failed.body), "reasoning.encrypted_content")
	require.NotContains(t, string(retried.body), "reasoning.encrypted_content")
	require.NotEqual(t, failed.auth, retried.auth)
	// Exactly one warmup, one failed and one retried attempt reached upstream.
	require.Equal(t, int32(3), requests.Load())
}

func TestHandleProxyAggregateCodexAffinityReusesExactExecutionBinding(t *testing.T) {
	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	observations := make(chan codexAffinityObservation, 8)
	targetA := newCodexAffinityUpstream(t, "target-a", observations, http.StatusOK)
	targetB := newCodexAffinityUpstream(t, "target-b", observations, http.StatusOK)
	fallback := newCodexAffinityUpstream(t, "fallback", observations, http.StatusOK)

	targetGroup := createTestGroup(t, db, "aggregate-affinity-target", "openai-response")
	targetGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100},{"url":%q,"weight":0}]`, targetA.URL, targetB.URL))
	targetGroup.Config = map[string]any{"max_retries": 0, "force_non_stream": true}
	require.NoError(t, db.Save(targetGroup).Error)
	fallbackGroup := createTestGroup(t, db, "aggregate-affinity-fallback", "openai-response")
	fallbackGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":100}]`, fallback.URL))
	fallbackGroup.Config = map[string]any{"max_retries": 0, "force_non_stream": true}
	require.NoError(t, db.Save(fallbackGroup).Error)

	parent := &models.Group{
		Name: "aggregate-affinity-parent", ProxyKeys: "proxy-a", ChannelType: "openai-response",
		GroupType: "aggregate", Enabled: true, Upstreams: []byte(`[{"url":"https://unused.example","weight":100}]`),
		Config: map[string]any{"max_retries": 1, "codex_affinity_enabled": true},
	}
	require.NoError(t, db.Create(parent).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{GroupID: parent.ID, SubGroupID: targetGroup.ID, SubGroupName: targetGroup.Name, SubGroupEnabled: true, Weight: 5000}).Error)
	require.NoError(t, db.Create(&models.GroupSubGroup{GroupID: parent.ID, SubGroupID: fallbackGroup.ID, SubGroupName: fallbackGroup.Name, SubGroupEnabled: true, Weight: 1}).Error)
	createTestKey(t, db, targetGroup.ID, "sk-target-a", ps.encryptionSvc)
	createTestKey(t, db, targetGroup.ID, "sk-target-b", ps.encryptionSvc)
	createTestKey(t, db, fallbackGroup.ID, "sk-fallback", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())
	require.NoError(t, ps.groupManager.Initialize())
	t.Cleanup(func() { ps.groupManager.Stop(context.Background()) })

	cachedParent, err := ps.groupManager.GetGroupByName(parent.Name)
	require.NoError(t, err)
	thread := requireAffinityKeyForSubGroup(t, ps, cachedParent, targetGroup.ID, "exact-binding")
	router := gin.New()
	router.POST("/proxy/:group_name/*path", requestmiddleware.ProxyAuth(ps.groupManager, nil), ps.HandleProxy)
	body := []byte(fmt.Sprintf(`{"model":"gpt-5","input":"hello","stream":false,"prompt_cache_key":%q,"include":["reasoning.encrypted_content"]}`, thread))

	require.Equal(t, http.StatusOK, runCodexAffinityRequest(t, router, parent.Name, "proxy-a", thread, "", body).Code)
	first := <-observations
	require.NotContains(t, string(first.body), "reasoning.encrypted_content")
	targetGroup.Upstreams = []byte(fmt.Sprintf(`[{"url":%q,"weight":1},{"url":%q,"weight":1000000}]`, targetA.URL, targetB.URL))
	require.NoError(t, db.Save(targetGroup).Error)
	require.NoError(t, db.Model(&models.GroupSubGroup{}).Where("group_id = ? AND sub_group_id = ?", parent.ID, targetGroup.ID).Update("weight", 1).Error)
	require.NoError(t, db.Model(&models.GroupSubGroup{}).Where("group_id = ? AND sub_group_id = ?", parent.ID, fallbackGroup.ID).Update("weight", 5000).Error)
	require.NoError(t, ps.groupManager.Reload())
	ps.channelFactory.InvalidateAllCaches()

	require.Equal(t, http.StatusOK, runCodexAffinityRequest(t, router, parent.Name, "proxy-a", thread, "", body).Code)
	second := <-observations
	require.Equal(t, first.auth, second.auth)
	require.Equal(t, first.upstream, second.upstream)
	require.Contains(t, string(second.body), "reasoning.encrypted_content")
	require.Empty(t, observations)
}
