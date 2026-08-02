package proxy

import (
	"net/url"
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
