package proxy

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	requestmiddleware "gpt-load/internal/middleware"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

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
