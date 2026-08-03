package proxy

import (
	"container/list"
	"sync"
	"time"
)

type codexAffinityCacheEntry struct {
	key          string
	binding      codexAffinityBinding
	resetTurnKey string
	expiresAt    time.Time
}

type codexAffinityCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	ttl     time.Duration
	maxSize int
	nextGen uint64
}

func newCodexAffinityCache(ttl time.Duration, maxSize int) *codexAffinityCache {
	if ttl <= 0 {
		ttl = codexAggregateAffinityTTL
	}
	if maxSize <= 0 {
		maxSize = codexAggregateAffinityMaxEntries
	}
	return &codexAffinityCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

func (cache *codexAffinityCache) getBinding(key string, now time.Time) (codexAffinityBinding, bool) {
	if cache == nil || key == "" {
		return codexAffinityBinding{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, ok := cache.entries[key]
	if !ok {
		return codexAffinityBinding{}, false
	}
	entry := element.Value.(*codexAffinityCacheEntry)
	if !entry.expiresAt.After(now) {
		cache.removeElementLocked(element)
		return codexAffinityBinding{}, false
	}
	cache.order.MoveToFront(element)
	if !entry.binding.valid() {
		return codexAffinityBinding{}, false
	}
	return entry.binding, true
}

func (cache *codexAffinityCache) setBinding(key string, binding codexAffinityBinding, now time.Time) {
	if cache == nil || key == "" || !binding.valid() {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.nextGen++
	if cache.nextGen == 0 {
		cache.nextGen++
	}
	binding.generation = cache.nextGen
	if element, ok := cache.entries[key]; ok {
		entry := element.Value.(*codexAffinityCacheEntry)
		entry.binding = binding
		entry.expiresAt = now.Add(cache.ttl)
		cache.order.MoveToFront(element)
		return
	}
	cache.removeExpiredTailLocked(now)
	entry := &codexAffinityCacheEntry{key: key, binding: binding, expiresAt: now.Add(cache.ttl)}
	cache.entries[key] = cache.order.PushFront(entry)
	for len(cache.entries) > cache.maxSize {
		cache.removeElementLocked(cache.order.Back())
	}
}

func (cache *codexAffinityCache) removeExpiredTailLocked(now time.Time) {
	for element := cache.order.Back(); element != nil; element = cache.order.Back() {
		entry := element.Value.(*codexAffinityCacheEntry)
		if entry.expiresAt.After(now) {
			return
		}
		cache.removeElementLocked(element)
	}
}

func (cache *codexAffinityCache) deleteBindingIfMatches(key string, observed codexAffinityBinding) {
	if cache == nil || key == "" {
		return
	}
	// setBinding never assigns generation zero; reject it to prevent identity-only deletion.
	if observed.generation == 0 {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, ok := cache.entries[key]
	if !ok {
		return
	}
	entry := element.Value.(*codexAffinityCacheEntry)
	if entry.binding.generation == observed.generation && entry.binding.sameIdentity(observed) {
		if entry.resetTurnKey != "" {
			// Keep the failed turn marked across rebinding so encrypted state cannot cross identities on that turn.
			entry.binding = codexAffinityBinding{}
			cache.order.MoveToFront(element)
			return
		}
		cache.removeElementLocked(element)
	}
}

func (cache *codexAffinityCache) removeElementLocked(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*codexAffinityCacheEntry)
	delete(cache.entries, entry.key)
	cache.order.Remove(element)
}
