package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const codexStateDomainWithoutTurnState = "turn-state:none"

func codexStateDomainKeyFromPayload(c *gin.Context, payload map[string]any, payloadOK bool) string {
	if payloadOK {
		if metadata, ok := payload["client_metadata"].(map[string]any); ok {
			if turnID := codexTurnIDFromMetadataValue(metadata["x-codex-turn-metadata"]); turnID != "" {
				return "turn:" + turnID
			}
			if turnID := stringFromJSONMap(metadata, "turn_id"); turnID != "" {
				return "turn:" + turnID
			}
			if stateKey := codexOpaqueTurnStateKey(stringFromJSONMap(metadata, "x-codex-turn-state")); stateKey != "" {
				return stateKey
			}
		}
	}
	if c != nil && c.Request != nil {
		if turnID := codexTurnIDFromMetadataValue(c.Request.Header.Get("X-Codex-Turn-Metadata")); turnID != "" {
			return "turn:" + turnID
		}
		if stateKey := codexOpaqueTurnStateKey(c.Request.Header.Get("X-Codex-Turn-State")); stateKey != "" {
			return stateKey
		}
	}
	return codexStateDomainWithoutTurnState
}

func codexOpaqueTurnStateKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return "turn-state:" + hex.EncodeToString(digest[:])
}

func codexTurnIDFromMetadataValue(value any) string {
	switch typed := value.(type) {
	case string:
		var metadata map[string]any
		if json.Unmarshal([]byte(typed), &metadata) == nil {
			return stringFromJSONMap(metadata, "turn_id")
		}
	case map[string]any:
		return stringFromJSONMap(typed, "turn_id")
	}
	return ""
}

func (cache *codexAffinityCache) markStateReset(key, turnKey string, now time.Time) {
	if cache == nil || key == "" || strings.TrimSpace(turnKey) == "" {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.entries[key]; ok {
		entry := element.Value.(*codexAffinityCacheEntry)
		if !entry.expiresAt.After(now) {
			cache.removeElementLocked(element)
		} else {
			entry.resetTurnKey = turnKey
			entry.expiresAt = now.Add(cache.ttl)
			cache.order.MoveToFront(element)
			return
		}
	}
	cache.removeExpiredTailLocked(now)
	entry := &codexAffinityCacheEntry{key: key, resetTurnKey: turnKey, expiresAt: now.Add(cache.ttl)}
	cache.entries[key] = cache.order.PushFront(entry)
	for len(cache.entries) > cache.maxSize {
		cache.removeElementLocked(cache.order.Back())
	}
}

func (cache *codexAffinityCache) requiresStateReset(key, turnKey string, now time.Time) bool {
	if cache == nil || key == "" || turnKey == "" {
		return false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, ok := cache.entries[key]
	if !ok {
		return false
	}
	entry := element.Value.(*codexAffinityCacheEntry)
	if !entry.expiresAt.After(now) {
		cache.removeElementLocked(element)
		return false
	}
	cache.order.MoveToFront(element)
	return entry.resetTurnKey == turnKey
}
