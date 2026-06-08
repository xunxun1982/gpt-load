package channel

import (
	"context"
	"encoding/json"
	"gpt-load/internal/config"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"gpt-load/internal/types"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// TestGetChannels tests retrieving all registered channel types
func TestGetChannels(t *testing.T) {
	channels := GetChannels()
	if len(channels) == 0 {
		t.Error("Expected at least one registered channel")
	}
	assert.Equal(t, []string{"openai", "openai-response", "anthropic", "gemini"}, channels)
	assert.NotContains(t, channels, "codex")
}

// setupTestFactory creates a test factory
func setupTestFactory(t *testing.T) *Factory {
	t.Helper() // Mark as test helper for better stack traces
	settingsManager := config.NewSystemSettingsManager()
	clientManager := httpclient.NewHTTPClientManager()
	factory := NewFactory(settingsManager, clientManager)
	return factory
}

// setupTestFactoryForBenchmark creates a test factory for benchmarks
func setupTestFactoryForBenchmark() *Factory {
	settingsManager := config.NewSystemSettingsManager()
	clientManager := httpclient.NewHTTPClientManager()
	return NewFactory(settingsManager, clientManager)
}

func TestNewBaseChannelUsesSplitRequestTimeouts(t *testing.T) {
	t.Parallel()

	factory := setupTestFactory(t)
	upstreams := []map[string]any{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, err := json.Marshal(upstreams)
	require.NoError(t, err)

	base, err := factory.newBaseChannel("openai", &models.Group{
		ID:          1,
		Name:        "split-timeout-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:          15,
			RequestTimeout:          90,
			NonStreamRequestTimeout: 45,
			StreamRequestTimeout:    120,
			IdleConnTimeout:         90,
			MaxIdleConns:            100,
			MaxIdleConnsPerHost:     10,
			ResponseHeaderTimeout:   30,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, base.HTTPClient)
	require.NotNil(t, base.StreamClient)

	assert.Equal(t, 45*time.Second, base.HTTPClient.Timeout)
	assert.Equal(t, 120*time.Second, base.StreamClient.Timeout)
}

func TestNewBaseChannelAllowsUnlimitedStreamTimeout(t *testing.T) {
	t.Parallel()

	factory := setupTestFactory(t)
	upstreams := []map[string]any{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, err := json.Marshal(upstreams)
	require.NoError(t, err)

	base, err := factory.newBaseChannel("openai", &models.Group{
		ID:          1,
		Name:        "unlimited-stream-timeout-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:          15,
			RequestTimeout:          90,
			NonStreamRequestTimeout: 45,
			StreamRequestTimeout:    0,
			IdleConnTimeout:         90,
			MaxIdleConns:            100,
			MaxIdleConnsPerHost:     10,
			ResponseHeaderTimeout:   30,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, base.StreamClient)

	assert.Zero(t, base.StreamClient.Timeout)
}

func TestNewBaseChannelUsesSelectedProxyForHTTPRequests(t *testing.T) {
	t.Parallel()

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstreamServer.Close)

	proxyHits := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case proxyHits <- r.URL.String():
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxyServer.Close)

	upstreamsJSON, err := json.Marshal([]map[string]any{
		{"url": upstreamServer.URL, "weight": 100, "proxy_url": proxyServer.URL},
	})
	require.NoError(t, err)

	base, err := setupTestFactory(t).newBaseChannel("openai", &models.Group{
		ID:          1,
		Name:        "proxy-flow-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:          1,
			NonStreamRequestTimeout: 2,
			StreamRequestTimeout:    0,
			IdleConnTimeout:         30,
			MaxIdleConns:            10,
			MaxIdleConnsPerHost:     10,
			ResponseHeaderTimeout:   2,
		},
	})
	require.NoError(t, err)

	selection, err := base.SelectUpstreamWithClients(mustParseURL("/proxy/proxy-flow-group/v1/models"), "proxy-flow-group")
	require.NoError(t, err)
	require.NotNil(t, selection.ProxyURL)
	require.Equal(t, proxyServer.URL, *selection.ProxyURL)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, selection.URL, nil)
	require.NoError(t, err)
	resp, err := selection.HTTPClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	select {
	case requestedURL := <-proxyHits:
		assert.Equal(t, selection.URL, requestedURL)
	case <-time.After(2 * time.Second):
		t.Fatal("expected request to pass through configured proxy")
	}
}

// TestNewFactory tests factory creation
func TestNewFactory(t *testing.T) {
	factory := setupTestFactory(t)
	assert.NotNil(t, factory)
	assert.NotNil(t, factory.settingsManager)
	assert.NotNil(t, factory.clientManager)
}

// TestGetChannelCaching tests channel caching mechanism
func TestGetChannelCaching(t *testing.T) {
	factory := setupTestFactory(t)
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, err := json.Marshal(upstreams)
	require.NoError(t, err)
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}
	channel1, err := factory.GetChannel(group)
	require.NoError(t, err)
	require.NotNil(t, channel1)
	channel2, err := factory.GetChannel(group)
	require.NoError(t, err)
	// Use assert.Same to verify pointer identity (cache returns exact same instance)
	assert.Same(t, channel1, channel2)
}

// TestInvalidateCache tests cache invalidation
func TestInvalidateCache(t *testing.T) {
	factory := setupTestFactory(t)
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, err := json.Marshal(upstreams)
	require.NoError(t, err)
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}
	channel1, err := factory.GetChannel(group)
	require.NoError(t, err)
	require.NotNil(t, channel1)

	// Verify cache entry exists
	factory.cacheLock.Lock()
	_, ok := factory.channelCache[group.ID]
	factory.cacheLock.Unlock()
	require.True(t, ok, "expected cache entry to exist")

	// Invalidate cache
	factory.InvalidateCache(group.ID)

	// Verify cache entry is removed
	factory.cacheLock.Lock()
	_, ok = factory.channelCache[group.ID]
	factory.cacheLock.Unlock()
	require.False(t, ok, "expected cache entry to be removed")

	// Get channel again - should create a new instance
	channel2, err := factory.GetChannel(group)
	require.NoError(t, err)
	require.NotNil(t, channel2)

	// Verify cache is repopulated
	factory.cacheLock.Lock()
	_, ok = factory.channelCache[group.ID]
	factory.cacheLock.Unlock()
	require.True(t, ok, "expected cache entry to be repopulated")
}

// TestGetChannelConcurrency tests concurrent channel creation
func TestGetChannelConcurrency(t *testing.T) {
	factory := setupTestFactory(t)
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, err := json.Marshal(upstreams)
	require.NoError(t, err)
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}
	var wg sync.WaitGroup
	results := make(chan ChannelProxy, 10)
	errCh := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := factory.GetChannel(group)
			if err != nil {
				errCh <- err
				return
			}
			results <- ch
		}()
	}
	wg.Wait()
	close(results)
	close(errCh)

	// Check for errors
	for err := range errCh {
		t.Errorf("GetChannel failed: %v", err)
	}

	// Collect channels and verify they're the same
	channels := make([]ChannelProxy, 0, 10)
	for ch := range results {
		channels = append(channels, ch)
	}

	require.Len(t, channels, 10, "Should have 10 successful channel creations")
	// Use assert.Same to verify pointer identity (cache returns exact same instance)
	for i := 1; i < len(channels); i++ {
		assert.Same(t, channels[0], channels[i])
	}
}

// BenchmarkGetChannel benchmarks channel retrieval
func BenchmarkGetChannel(b *testing.B) {
	factory := setupTestFactoryForBenchmark()
	upstreams := []map[string]interface{}{
		{"url": "https://api.openai.com", "weight": 100},
	}
	upstreamsJSON, err := json.Marshal(upstreams)
	if err != nil {
		b.Fatal(err)
	}
	group := &models.Group{
		ID:          1,
		Name:        "test-group",
		ChannelType: "openai",
		Upstreams:   datatypes.JSON(upstreamsJSON),
		EffectiveConfig: types.SystemSettings{
			ConnectTimeout:        30,
			RequestTimeout:        300,
			IdleConnTimeout:       90,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 30,
		},
	}

	// Warm cache to benchmark cached retrieval performance
	if _, err := factory.GetChannel(group); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := factory.GetChannel(group)
		if err != nil {
			b.Fatal(err)
		}
	}
}
