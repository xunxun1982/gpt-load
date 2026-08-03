package proxy

import (
	"net/url"
	"strconv"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	testProxyIdentityContextKey = "authenticated_proxy_identity_digest"
	testProxyIdentityDigest     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func setTestProxyIdentity(c *gin.Context) {
	c.Set(testProxyIdentityContextKey, testProxyIdentityDigest)
}

func testCodexAffinityCacheKey(routeGroupID uint, threadKey string) string {
	return codexAffinityScopedCacheKey(routeGroupID, testProxyIdentityDigest, threadKey)
}

func cacheTestCodexAffinityBinding(t *testing.T, ps *ProxyServer, routeGroup, executionGroup *models.Group, keyID uint, threadKey string) string {
	t.Helper()
	handler, err := ps.channelFactory.GetChannel(executionGroup)
	require.NoError(t, err)
	upstream, err := handler.SelectUpstreamWithClients(&url.URL{Path: "/proxy/" + executionGroup.Name + "/v1/responses"}, executionGroup.Name)
	require.NoError(t, err)
	require.NotNil(t, upstream)
	require.NotEmpty(t, upstream.Identity)
	cacheKey := testCodexAffinityCacheKey(routeGroup.ID, threadKey)
	ps.codexAffinityCache.setBinding(cacheKey, codexAffinityBinding{
		executionGroupID: executionGroup.ID,
		keyID:            keyID,
		upstreamIdentity: upstream.Identity,
	}, time.Now())
	return cacheKey
}

const legacyProxyIdentityDigest = "legacy-internal-context"

type codexAggregateAffinityCache = codexAffinityCache

func newCodexAggregateAffinityCache(ttl time.Duration, maxSize int) *codexAggregateAffinityCache {
	return newCodexAffinityCache(ttl, maxSize)
}

func codexAggregateAffinityCacheKey(groupID uint, affinityKey string) string {
	return codexAffinityScopedCacheKey(groupID, legacyProxyIdentityDigest, affinityKey)
}

func (cache *codexAffinityCache) get(key string, now time.Time) (uint, bool) {
	binding, ok := cache.getBinding(key, now)
	return binding.executionGroupID, ok
}

func (cache *codexAffinityCache) getWithGeneration(key string, now time.Time) (uint, uint64, bool) {
	binding, ok := cache.getBinding(key, now)
	return binding.executionGroupID, binding.generation, ok
}

func (cache *codexAffinityCache) set(key string, executionGroupID uint, now time.Time) {
	if cache == nil || key == "" || executionGroupID == 0 {
		return
	}
	cache.setBinding(key, legacyCodexAffinityBinding(executionGroupID), now)
}

func (cache *codexAffinityCache) deleteIfMatches(key string, executionGroupID uint, generation uint64) {
	binding := legacyCodexAffinityBinding(executionGroupID)
	binding.generation = generation
	cache.deleteBindingIfMatches(key, binding)
}

func legacyCodexAffinityBinding(executionGroupID uint) codexAffinityBinding {
	identity := strconv.FormatUint(uint64(executionGroupID), 10)
	return codexAffinityBinding{
		executionGroupID: executionGroupID,
		keyID:            executionGroupID,
		upstreamIdentity: "legacy:" + identity,
	}
}
