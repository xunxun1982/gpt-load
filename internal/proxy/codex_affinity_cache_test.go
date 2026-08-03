package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexAffinityCacheKeySeparatesAuthenticatedProxyIdentity(t *testing.T) {
	t.Parallel()

	keyA := codexAffinityScopedCacheKey(7, "proxy-a-digest", "thread-1")
	keyB := codexAffinityScopedCacheKey(7, "proxy-b-digest", "thread-1")

	require.Len(t, keyA, 64)
	require.Len(t, keyB, 64)
	require.NotEqual(t, keyA, keyB)
}

func TestCodexAffinityCacheKeyRejectsMissingAuthenticatedProxyIdentity(t *testing.T) {
	t.Parallel()

	require.Empty(t, codexAffinityScopedCacheKey(7, "", "thread-1"))
}

func TestCodexAffinityCacheStoresCompleteBinding(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	cache := newCodexAffinityCache(time.Minute, 2)
	want := codexAffinityBinding{
		executionGroupID: 11,
		keyID:            22,
		upstreamIdentity: "upstream-fingerprint",
	}

	cache.setBinding("cache-key", want, now)
	got, ok := cache.getBinding("cache-key", now.Add(time.Second))

	require.True(t, ok)
	require.Equal(t, want.executionGroupID, got.executionGroupID)
	require.Equal(t, want.keyID, got.keyID)
	require.Equal(t, want.upstreamIdentity, got.upstreamIdentity)
	require.NotZero(t, got.generation)
}

func TestCodexAffinityCacheCASPreservesRenewedAndABABindings(t *testing.T) {
	t.Parallel()

	now := time.Unix(200, 0)
	cache := newCodexAffinityCache(time.Minute, 2)
	bindingA := codexAffinityBinding{executionGroupID: 1, keyID: 2, upstreamIdentity: "a"}
	bindingB := codexAffinityBinding{executionGroupID: 1, keyID: 3, upstreamIdentity: "b"}
	cache.setBinding("cache-key", bindingA, now)
	observed, ok := cache.getBinding("cache-key", now)
	require.True(t, ok)

	cache.setBinding("cache-key", bindingB, now.Add(time.Second))
	cache.setBinding("cache-key", bindingA, now.Add(2*time.Second))
	cache.deleteBindingIfMatches("cache-key", observed)

	latest, ok := cache.getBinding("cache-key", now.Add(3*time.Second))
	require.True(t, ok)
	require.Equal(t, bindingA.executionGroupID, latest.executionGroupID)
	require.Equal(t, bindingA.keyID, latest.keyID)
	require.Equal(t, bindingA.upstreamIdentity, latest.upstreamIdentity)
	require.NotEqual(t, observed.generation, latest.generation)
}

func TestCodexAffinityCacheUsesTTLAndLRU(t *testing.T) {
	t.Parallel()

	now := time.Unix(300, 0)
	cache := newCodexAffinityCache(time.Second, 2)
	cache.setBinding("old", codexAffinityBinding{executionGroupID: 1, keyID: 1, upstreamIdentity: "old"}, now)
	cache.setBinding("hot", codexAffinityBinding{executionGroupID: 1, keyID: 2, upstreamIdentity: "hot"}, now)
	_, ok := cache.getBinding("old", now.Add(100*time.Millisecond))
	require.True(t, ok)
	cache.setBinding("new", codexAffinityBinding{executionGroupID: 1, keyID: 3, upstreamIdentity: "new"}, now.Add(200*time.Millisecond))

	_, hotOK := cache.getBinding("hot", now.Add(300*time.Millisecond))
	_, oldOK := cache.getBinding("old", now.Add(300*time.Millisecond))
	_, expiredOK := cache.getBinding("new", now.Add(2*time.Second))
	require.False(t, hotOK)
	require.True(t, oldOK)
	require.False(t, expiredOK)
}

func TestCodexAffinityCacheKeepsTurnResetAcrossRebinding(t *testing.T) {
	t.Parallel()

	now := time.Unix(400, 0)
	cache := newCodexAffinityCache(time.Minute, 2)
	bindingA := codexAffinityBinding{executionGroupID: 1, keyID: 2, upstreamIdentity: "a"}
	bindingB := codexAffinityBinding{executionGroupID: 1, keyID: 3, upstreamIdentity: "b"}
	cache.setBinding("cache-key", bindingA, now)
	observed, ok := cache.getBinding("cache-key", now)
	require.True(t, ok)

	cache.markStateReset("cache-key", "turn:1", now.Add(time.Second))
	cache.deleteBindingIfMatches("cache-key", observed)
	cache.setBinding("cache-key", bindingB, now.Add(2*time.Second))

	got, ok := cache.getBinding("cache-key", now.Add(3*time.Second))
	require.True(t, ok)
	require.True(t, got.sameIdentity(bindingB))
	require.True(t, cache.requiresStateReset("cache-key", "turn:1", now.Add(3*time.Second)))
	require.False(t, cache.requiresStateReset("cache-key", "turn:2", now.Add(3*time.Second)))
}
