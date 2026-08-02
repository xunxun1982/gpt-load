package proxy

import (
	"strconv"
	"time"
)

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
	cache.setLocked(key, legacyCodexAffinityBinding(executionGroupID), now)
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
