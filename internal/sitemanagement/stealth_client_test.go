package sitemanagement

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStealthHeadersMatchChromeProfileVersion(t *testing.T) {
	t.Parallel()

	headers := stealthHeaders()

	assert.Contains(t, headers["User-Agent"], "Chrome/146.0.0.0")
	assert.Contains(t, headers["Sec-Ch-Ua"], `v="146"`)
	assert.NotContains(t, strings.ToLower(headers["Sec-Ch-Ua"]), "120")
	assert.NotContains(t, headers, "Accept-Encoding")
}

func TestEvictIdleCachedClient_RemovesLongIdleEntry(t *testing.T) {
	t.Parallel()

	var cache sync.Map
	old := &timedHTTPClientEntry{client: &http.Client{}}
	old.lastUsed.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	fresh := &timedHTTPClientEntry{client: &http.Client{}}
	fresh.lastUsed.Store(time.Now().UnixNano())

	cache.Store("old", old)
	cache.Store("fresh", fresh)

	cutoff := time.Now().Add(-clientIdleEvictionTimeout).UnixNano()
	evictIdleCachedClient(&cache, "old", old, cutoff)
	evictIdleCachedClient(&cache, "fresh", fresh, cutoff)

	_, oldExists := cache.Load("old")
	_, freshExists := cache.Load("fresh")
	assert.False(t, oldExists, "long-idle entry should be evicted")
	assert.True(t, freshExists, "recently-used entry should be retained")
}

func TestEvictIdleCachedClient_KeepsRecentEntry(t *testing.T) {
	t.Parallel()

	var cache sync.Map
	recent := &timedHTTPClientEntry{client: &http.Client{}}
	recent.lastUsed.Store(time.Now().UnixNano())
	cache.Store("recent", recent)

	cutoff := time.Now().Add(-clientIdleEvictionTimeout).UnixNano()
	evictIdleCachedClient(&cache, "recent", recent, cutoff)

	_, ok := cache.Load("recent")
	assert.True(t, ok, "recently-used entry must survive eviction")
}

func TestCloseCachedHTTPClient_HandlesBothEntryShapes(t *testing.T) {
	t.Parallel()

	// *timedHTTPClientEntry with a standard transport should not panic.
	assert.NotPanics(t, func() {
		closeCachedHTTPClient(&timedHTTPClientEntry{client: &http.Client{Transport: &http.Transport{}}})
	})
	// Raw *http.Client legacy shape should not panic.
	assert.NotPanics(t, func() {
		closeCachedHTTPClient(&http.Client{Transport: &http.Transport{}})
	})
	// Nil / nil transport should not panic.
	assert.NotPanics(t, func() {
		closeCachedHTTPClient(nil)
	})
}

func TestStealthClientManager_EvictIdleClientsClearsIdle(t *testing.T) {
	t.Parallel()

	m := NewStealthClientManager(time.Second)
	idle := &timedHTTPClientEntry{client: &http.Client{}}
	idle.lastUsed.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	m.clients.Store("idle", idle)

	m.evictIdleClients(clientIdleEvictionTimeout)

	_, ok := m.clients.Load("idle")
	assert.False(t, ok, "idle stealth client should be evicted")
}
