package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type codexAffinityBinding struct {
	executionGroupID uint
	keyID            uint
	upstreamIdentity string
	generation       uint64
}

func (binding codexAffinityBinding) valid() bool {
	return binding.executionGroupID != 0 && binding.keyID != 0 && binding.upstreamIdentity != ""
}

func (binding codexAffinityBinding) sameIdentity(other codexAffinityBinding) bool {
	return binding.executionGroupID == other.executionGroupID &&
		binding.keyID == other.keyID &&
		binding.upstreamIdentity == other.upstreamIdentity
}

func codexAffinityScopedCacheKey(routeGroupID uint, proxyIdentityDigest, threadKey string) string {
	threadKey = strings.TrimSpace(threadKey)
	if routeGroupID == 0 || proxyIdentityDigest == "" || threadKey == "" {
		return ""
	}

	h := sha256.New()
	_, _ = h.Write([]byte("gpt-load/codex-affinity/v2\x00"))
	writeAffinityHashField(h, strconv.FormatUint(uint64(routeGroupID), 10))
	writeAffinityHashField(h, proxyIdentityDigest)
	writeAffinityHashField(h, threadKey)
	return hex.EncodeToString(h.Sum(nil))
}

func writeAffinityHashField(h interface{ Write([]byte) (int, error) }, value string) {
	_, _ = h.Write([]byte(strconv.Itoa(len(value))))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(value))
}

func (ps *ProxyServer) bindCodexAffinityIfSuccessful(c *gin.Context, respStatus int, retryCtx *retryContext) {
	if retryCtx == nil || retryCtx.codexAffinityCacheKey == "" || retryCtx.codexSelection == nil ||
		respStatus < 200 || respStatus >= 300 || c.Writer.Status() < 200 || c.Writer.Status() >= 300 {
		return
	}
	_, _, logicalFailure := logicalStatusFromContext(c)
	_, statusUnverified := c.Get(ctxKeyResponsesStatusUnverified)
	_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
	if logicalFailure || statusUnverified || processingFailed {
		return
	}
	ps.codexAffinityCache.setBinding(retryCtx.codexAffinityCacheKey, retryCtx.codexSelection.binding, time.Now())
}
