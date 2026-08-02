package middleware

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

const authenticatedProxyIdentityContextKey = "authenticated_proxy_identity_digest"

func setAuthenticatedProxyIdentity(c *gin.Context, proxyKey string) {
	if c == nil || proxyKey == "" {
		return
	}
	h := sha256.New()
	_, _ = h.Write([]byte("gpt-load/proxy-auth/v1\x00"))
	_, _ = h.Write([]byte(proxyKey))
	c.Set(authenticatedProxyIdentityContextKey, hex.EncodeToString(h.Sum(nil)))
}

// AuthenticatedProxyIdentityDigest returns the domain-separated digest set by ProxyAuth.
func AuthenticatedProxyIdentityDigest(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(authenticatedProxyIdentityContextKey)
	if !ok {
		return ""
	}
	digest, _ := value.(string)
	return digest
}
