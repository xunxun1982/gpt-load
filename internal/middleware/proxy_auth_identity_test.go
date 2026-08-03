package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAuthenticatedProxyIdentityStoresOnlyStableDigest(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setAuthenticatedProxyIdentity(c, "proxy-secret-a")
	digestA := AuthenticatedProxyIdentityDigest(c)

	require.Len(t, digestA, 64)
	require.NotContains(t, digestA, "proxy-secret-a")
	setAuthenticatedProxyIdentity(c, "proxy-secret-a")
	require.Equal(t, digestA, AuthenticatedProxyIdentityDigest(c))
	setAuthenticatedProxyIdentity(c, "proxy-secret-b")
	require.NotEqual(t, digestA, AuthenticatedProxyIdentityDigest(c))
}

func TestAuthenticatedProxyIdentityMissingContextIsEmpty(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Empty(t, AuthenticatedProxyIdentityDigest(c))
}
