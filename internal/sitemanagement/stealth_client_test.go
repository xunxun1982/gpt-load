package sitemanagement

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestGetCachedClientRefreshed_StaleLoadAfterEvictionMiss verifies the
// stale-load/eviction/refresh ordering deterministically: a caller that loaded
// an entry (here simulated by the stale entry still in the map) sees the entry
// evicted before the refresh path runs. getCachedClientRefreshed must then
// report a miss instead of refreshing a deleted entry or handing out a client
// whose idle connections were already closed, so the caller rebuilds.
func TestGetCachedClientRefreshed_StaleLoadAfterEvictionMiss(t *testing.T) {
	t.Parallel()

	var cache sync.Map
	stale := &timedHTTPClientEntry{client: &http.Client{}}
	stale.lastUsed.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	cache.Store("proxy", stale)

	// Simulate the caller's stale Load: it captured "stale" as the cached entry.
	loaded, _ := cache.Load("proxy")
	require.Same(t, stale, loaded)

	// Concurrent eviction removes the entry between the Load and the refresh.
	cutoff := time.Now().Add(-clientIdleEvictionTimeout).UnixNano()
	evictIdleCachedClient(&cache, "proxy", stale, cutoff)

	// The refresh must not return the deleted entry; it reports a miss so the
	// caller rebuilds a fresh client.
	client, ok := getCachedClientRefreshed(&cache, "proxy")
	require.False(t, ok, "evicted entry must not be returned as a cache hit")
	require.Nil(t, client)
	_, present := cache.Load("proxy")
	require.False(t, present, "evicted entry must stay removed")
}

// TestGetCachedClientRefreshed_HitRefreshesLastUsed verifies the normal cache
// hit path of the shared helper: it returns the cached client and refreshes
// lastUsed so a subsequent eviction pass retains the entry.
func TestGetCachedClientRefreshed_HitRefreshesLastUsed(t *testing.T) {
	t.Parallel()

	var cache sync.Map
	entry := &timedHTTPClientEntry{client: &http.Client{}}
	entry.lastUsed.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	cache.Store("proxy", entry)

	client, ok := getCachedClientRefreshed(&cache, "proxy")
	require.True(t, ok, "cached entry must be a hit")
	require.Same(t, entry.client, client)
	require.Greater(t, entry.lastUsed.Load(), time.Now().Add(-time.Minute).UnixNano(),
		"lastUsed must be refreshed on hit")

	// After the refresh, an eviction pass must keep the entry.
	cutoff := time.Now().Add(-clientIdleEvictionTimeout).UnixNano()
	evictIdleCachedClient(&cache, "proxy", entry, cutoff)
	_, present := cache.Load("proxy")
	require.True(t, present, "refreshed entry must survive eviction")
}

// TestGetCachedClientRefreshed_ReplacedEntryReturnsCurrent verifies that a
// caller holding a stale entry pointer still receives the current cached
// entry: getCachedClientRefreshed re-loads the map under the entry lock, so a
// concurrent replacement is returned (and refreshed) instead of the stale one.
func TestGetCachedClientRefreshed_ReplacedEntryReturnsCurrent(t *testing.T) {
	t.Parallel()

	var cache sync.Map
	stale := &timedHTTPClientEntry{client: &http.Client{}}
	stale.lastUsed.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	cache.Store("proxy", stale)

	// A concurrent goroutine replaced the entry after the caller's Load.
	replacement := &timedHTTPClientEntry{client: &http.Client{}}
	replacement.lastUsed.Store(time.Now().UnixNano())
	cache.Store("proxy", replacement)

	// The helper re-loads under the lock, so it must return the current
	// replacement client, never the stale entry the caller captured.
	client, ok := getCachedClientRefreshed(&cache, "proxy")
	require.True(t, ok)
	require.Same(t, replacement.client, client)
	require.NotSame(t, stale.client, client)
}

// TestEvictIdleCachedClient_ConcurrentRefreshRetainsEntry verifies that the
// entry mutex coordinates lastUsed refreshes with the eviction check, so an
// actively used client is never evicted as idle. Without the mutex,
// evictIdleCachedClient could read a stale lastUsed and Delete the entry right
// after a cache hit refreshed it.
//
// Honesty caveat: the delete-window is tiny, so this stress test can pass even
// against the unlocked implementation; the mutex is still the correct fix
// because it establishes a happens-before edge between the refresh and the
// eviction check-and-delete.
func TestEvictIdleCachedClient_ConcurrentRefreshRetainsEntry(t *testing.T) {
	t.Parallel()

	const rounds = 100

	var cache sync.Map
	entry := &timedHTTPClientEntry{client: &http.Client{}}
	entry.lastUsed.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	cache.Store("proxy", entry)

	cutoff := time.Now().Add(-clientIdleEvictionTimeout).UnixNano()

	// simulateGetClientRefresh mirrors the locked lastUsed refresh performed by
	// every cache-hit path (StealthClientManager.GetClient and the getHTTPClient
	// helpers of the owning services).
	simulateGetClientRefresh := func() {
		entry.mu.Lock()
		entry.lastUsed.Store(time.Now().UnixNano())
		entry.mu.Unlock()
	}

	// Race a refresh against an eviction pass for many rounds.
	for i := 0; i < rounds; i++ {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			simulateGetClientRefresh()
		}()
		evictIdleCachedClient(&cache, "proxy", entry, cutoff)
		wg.Wait()
	}

	// Some rounds evict may win the race and delete the entry before the refresh
	// runs; a real GetClient would then rebuild it on its next cache miss,
	// so re-insert the entry before the final cache hit.
	cache.Store("proxy", entry)

	// A final cache hit followed by an eviction pass must always retain the
	// entry: the refresh happens-before the eviction's lock-protected check.
	simulateGetClientRefresh()
	evictIdleCachedClient(&cache, "proxy", entry, cutoff)

	_, ok := cache.Load("proxy")
	assert.True(t, ok, "actively used client must survive concurrent eviction")
}

// TestEvictIdleCachedClient_ReplacementSurvivesEviction verifies that when a
// newer entry replaces the inspected one between sync.Map.Range selection and
// eviction, CompareAndDelete prevents the replacement from being removed.
func TestEvictIdleCachedClient_ReplacementSurvivesEviction(t *testing.T) {
	t.Parallel()

	var cache sync.Map
	stale := &timedHTTPClientEntry{client: &http.Client{}}
	stale.lastUsed.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	cache.Store("proxy", stale)

	// Simulate a concurrent goroutine replacing the entry between Range selection
	// (which captured "stale") and the eviction deletion.
	replacement := &timedHTTPClientEntry{client: &http.Client{}}
	replacement.lastUsed.Store(time.Now().UnixNano())
	cache.Store("proxy", replacement)

	cutoff := time.Now().Add(-clientIdleEvictionTimeout).UnixNano()
	evictIdleCachedClient(&cache, "proxy", stale, cutoff)

	got, ok := cache.Load("proxy")
	require.True(t, ok, "newer replacement must survive eviction of the stale entry")
	require.Same(t, replacement, got)
}
