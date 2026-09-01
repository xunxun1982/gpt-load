package httpclient

import (
	"crypto/tls"
	"fmt"
	"gpt-load/internal/utils"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewHTTPClientManager tests client manager creation
func TestNewHTTPClientManager(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()
	assert.NotNil(t, manager)
	assert.NotNil(t, manager.clients)
}

func TestStripSensitiveOnCrossHostRedirect(t *testing.T) {
	t.Parallel()

	headers := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client := NewHTTPClientManager().GetClient(&Config{RequestTimeout: 5 * time.Second})
	req, err := http.NewRequest(http.MethodGet, source.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("x-api-key", "secret")
	req.Header.Set("api-key", "secret")
	req.Header.Set("X-Goog-Api-Key", "secret")
	req.Header.Set("X-Auth-Token", "secret")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	received := <-headers
	for _, header := range sensitiveProxyHeaders {
		assert.Empty(t, received.Get(header), "%s should be stripped", header)
	}
}

func TestStripSensitiveOnSameHostRedirectPreservesHeaders(t *testing.T) {
	t.Parallel()

	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
			return
		}
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewHTTPClientManager().GetClient(&Config{RequestTimeout: 5 * time.Second})
	req, err := http.NewRequest(http.MethodGet, server.URL+"/redirect", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("x-api-key", "secret")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	received := <-headers
	assert.Equal(t, "Bearer secret", received.Get("Authorization"))
	assert.Equal(t, "secret", received.Get("x-api-key"))
}

func TestStripSensitiveOnSchemeDowngradeRedirect(t *testing.T) {
	t.Parallel()

	previous := httptest.NewRequest(http.MethodGet, "https://api.example.com/source", nil)
	req := httptest.NewRequest(http.MethodGet, "http://API.EXAMPLE.COM/target", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("x-api-key", "secret")

	err := stripSensitiveOnCrossHostRedirect(req, []*http.Request{previous})
	require.NoError(t, err)
	assert.Empty(t, req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get("x-api-key"))
}

// TestGetClient tests client retrieval and caching
func TestGetClient(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:        10 * time.Second,
		RequestTimeout:        30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		ResponseHeaderTimeout: 10 * time.Second,
	}

	// First call should create a new client
	client1 := manager.GetClient(config)
	require.NotNil(t, client1)

	// Second call with same config should return cached client
	client2 := manager.GetClient(config)
	assert.Equal(t, client1, client2, "Should return cached client")

	// Different config should create new client
	config2 := &Config{
		ConnectTimeout:  5 * time.Second,
		RequestTimeout:  15 * time.Second,
		IdleConnTimeout: 60 * time.Second,
		MaxIdleConns:    50,
	}

	client3 := manager.GetClient(config2)
	assert.NotEqual(t, client1, client3, "Should create new client for different config")
}

// TestGetClient_WithProxy tests client with proxy configuration
func TestGetClient_WithProxy(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 30 * time.Second,
		ProxyURL:       "http://proxy.example.com:8080",
	}

	client := manager.GetClient(config)
	require.NotNil(t, client)
	assert.NotNil(t, client.Transport)
}

func TestGetClient_WithSkipTLSVerify(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	client := manager.GetClient(&Config{
		RequestTimeout: 5 * time.Second,
		SkipTLSVerify:  true,
	})

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
}

func TestGetClient_WithSkipTLSVerifyLogsWarning(t *testing.T) {
	hook := captureGlobalLogrusEntries(t)
	manager := NewHTTPClientManager()

	manager.GetClient(&Config{
		RequestTimeout: 5 * time.Second,
		SkipTLSVerify:  true,
	})

	for _, entry := range hook.AllEntries() {
		if entry.Message != "HTTP client created with TLS certificate verification disabled" {
			continue
		}
		assert.Equal(t, logrus.WarnLevel, entry.Level)
		assert.Equal(t, true, entry.Data["skip_tls_verify"])
		assert.NotEmpty(t, entry.Data["fingerprint"])
		return
	}
	require.Fail(t, "expected skip_tls_verify warning log")
}

// TestGetClient_Concurrent tests concurrent client access
func TestGetClient_Concurrent(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 30 * time.Second,
	}

	// Run concurrent requests and collect results via channel
	results := make(chan *http.Client, 10)
	start := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			<-start // Wait for signal
			client := manager.GetClient(config)
			results <- client
		}()
	}
	close(start) // Release all goroutines simultaneously

	// Wait for all goroutines and assert in main goroutine
	clients := make([]*http.Client, 10)
	for i := 0; i < 10; i++ {
		clients[i] = <-results
		assert.NotNil(t, clients[i])
	}

	// All clients should be the same instance (cached)
	for i := 1; i < 10; i++ {
		assert.Equal(t, clients[0], clients[i], "All clients should be the same cached instance")
	}
}

// TestConfig_Fingerprint tests configuration fingerprinting
func TestConfig_Fingerprint(t *testing.T) {
	t.Parallel()

	config1 := &Config{
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 30 * time.Second,
		MaxIdleConns:   100,
	}

	config2 := &Config{
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 30 * time.Second,
		MaxIdleConns:   100,
	}

	config3 := &Config{
		ConnectTimeout: 5 * time.Second,
		RequestTimeout: 30 * time.Second,
		MaxIdleConns:   100,
	}

	fp1 := config1.getFingerprint()
	fp2 := config2.getFingerprint()
	fp3 := config3.getFingerprint()

	assert.Equal(t, fp1, fp2, "Same configs should have same fingerprint")
	assert.NotEqual(t, fp1, fp3, "Different configs should have different fingerprints")
}

func TestConfig_FingerprintIncludesSkipTLSVerify(t *testing.T) {
	t.Parallel()

	secure := (&Config{RequestTimeout: 30 * time.Second}).getFingerprint()
	insecure := (&Config{RequestTimeout: 30 * time.Second, SkipTLSVerify: true}).getFingerprint()

	assert.NotEqual(t, secure, insecure)
}

func captureGlobalLogrusEntries(t *testing.T) *logrustest.Hook {
	t.Helper()

	logger := logrus.StandardLogger()
	originalHooks := make(logrus.LevelHooks, len(logger.Hooks))
	for level, hooks := range logger.Hooks {
		originalHooks[level] = append([]logrus.Hook(nil), hooks...)
	}
	t.Cleanup(func() {
		logger.ReplaceHooks(originalHooks)
	})

	return logrustest.NewGlobal()
}

// TestGetClient_DifferentConfigs tests multiple different configurations
func TestGetClient_DifferentConfigs(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	configs := []*Config{
		{ConnectTimeout: 5 * time.Second, RequestTimeout: 10 * time.Second},
		{ConnectTimeout: 10 * time.Second, RequestTimeout: 20 * time.Second},
		{ConnectTimeout: 15 * time.Second, RequestTimeout: 30 * time.Second},
	}

	clients := make([]*http.Client, len(configs))
	for i, config := range configs {
		clients[i] = manager.GetClient(config)
		require.NotNil(t, clients[i])
	}

	// All clients should be different
	for i := 0; i < len(clients); i++ {
		for j := i + 1; j < len(clients); j++ {
			assert.NotEqual(t, clients[i], clients[j])
		}
	}

	// Verify cache contains multiple clients by checking they are all different instances
	assert.Greater(t, len(clients), 1, "Should have created multiple different clients")
}

// TestGetClient_WithCompression tests client with compression settings
func TestGetClient_WithCompression(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config1 := &Config{
		ConnectTimeout:     10 * time.Second,
		DisableCompression: false,
	}

	config2 := &Config{
		ConnectTimeout:     10 * time.Second,
		DisableCompression: true,
	}

	client1 := manager.GetClient(config1)
	client2 := manager.GetClient(config2)

	assert.NotEqual(t, client1, client2, "Different compression settings should create different clients")
}

// Sink variable to prevent compiler optimization
var benchSink interface{}

// BenchmarkGetClient benchmarks client retrieval
func BenchmarkGetClient(b *testing.B) {
	manager := NewHTTPClientManager()
	config := &Config{
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 30 * time.Second,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = manager.GetClient(config)
	}
}

// BenchmarkGetClient_Concurrent benchmarks concurrent client access
func BenchmarkGetClient_Concurrent(b *testing.B) {
	manager := NewHTTPClientManager()
	config := &Config{
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 30 * time.Second,
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var localSink interface{}
		for pb.Next() {
			localSink = manager.GetClient(config)
		}
		_ = localSink // Prevent unused variable warning
	})
}

// BenchmarkGetFingerprint benchmarks fingerprint generation
func BenchmarkGetFingerprint(b *testing.B) {
	config := &Config{
		ConnectTimeout:        10 * time.Second,
		RequestTimeout:        30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		ResponseHeaderTimeout: 10 * time.Second,
		ProxyURL:              "http://proxy.example.com:8080",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = config.getFingerprint()
	}
}

// TestGetClient_WithAllConfigs tests client with all configuration options
func TestGetClient_WithAllConfigs(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:        10 * time.Second,
		RequestTimeout:        30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   100,
		ResponseHeaderTimeout: 60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		DisableCompression:    false,
		WriteBufferSize:       4096,
		ReadBufferSize:        4096,
		ForceAttemptHTTP2:     true,
	}

	client := manager.GetClient(config)
	assert.NotNil(t, client)
	assert.Equal(t, 30*time.Second, client.Timeout)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "Expected *http.Transport")
	assert.Equal(t, 200, transport.MaxIdleConns)
	assert.Equal(t, 100, transport.MaxIdleConnsPerHost)
	assert.Equal(t, 90*time.Second, transport.IdleConnTimeout)
	assert.Equal(t, 60*time.Second, transport.ResponseHeaderTimeout)
	assert.Equal(t, 15*time.Second, transport.TLSHandshakeTimeout)
	assert.Equal(t, 2*time.Second, transport.ExpectContinueTimeout)
	assert.False(t, transport.DisableCompression)
	assert.True(t, transport.ForceAttemptHTTP2)
}

// TestGetClient_WithInvalidProxy tests client with invalid proxy URL
func TestGetClient_WithInvalidProxy(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:  10 * time.Second,
		RequestTimeout:  30 * time.Second,
		IdleConnTimeout: 90 * time.Second,
		ProxyURL:        "://invalid-proxy",
	}

	client := manager.GetClient(config)
	assert.NotNil(t, client)
}

// TestGetClient_WithUnsupportedProxyScheme tests client with unsupported proxy scheme
func TestGetClient_WithUnsupportedProxyScheme(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:  10 * time.Second,
		RequestTimeout:  30 * time.Second,
		IdleConnTimeout: 90 * time.Second,
		ProxyURL:        "ftp://proxy.example.com:8080",
	}

	client := manager.GetClient(config)
	assert.NotNil(t, client)
}

func TestGetClient_WithUnresolvedProxyPoolRefDoesNotFallbackToEnvironment(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:  10 * time.Second,
		RequestTimeout:  30 * time.Second,
		IdleConnTimeout: 90 * time.Second,
		ProxyURL:        utils.BuildProxyPoolItemRef(404),
	}

	client := manager.GetClient(config)
	assert.NotNil(t, client)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.Proxy)

	req := httptest.NewRequest(http.MethodGet, "http://upstream.example.com", nil)
	proxyURL, err := transport.Proxy(req)
	require.Error(t, err)
	assert.Nil(t, proxyURL)
	assert.Contains(t, err.Error(), "unresolved proxy pool reference")
}

// TestGetClient_WithWhitespaceProxy tests client with proxy URL containing whitespace
func TestGetClient_WithWhitespaceProxy(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:  10 * time.Second,
		RequestTimeout:  30 * time.Second,
		IdleConnTimeout: 90 * time.Second,
		ProxyURL:        "  http://proxy.example.com:8080  ",
	}

	client := manager.GetClient(config)
	assert.NotNil(t, client)
}

// TestGetClient_MaxConnsPerHost tests MaxConnsPerHost calculation
func TestGetClient_MaxConnsPerHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		maxIdleConnsPerHost int
		expectedMinConns    int
	}{
		{"low idle conns", 2, 10},
		{"medium idle conns", 10, 20},
		{"high idle conns", 50, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			manager := NewHTTPClientManager()
			config := &Config{
				ConnectTimeout:      10 * time.Second,
				RequestTimeout:      30 * time.Second,
				MaxIdleConnsPerHost: tt.maxIdleConnsPerHost,
			}

			client := manager.GetClient(config)
			transport := client.Transport.(*http.Transport)
			assert.GreaterOrEqual(t, transport.MaxConnsPerHost, tt.expectedMinConns)
		})
	}
}

// TestGetClient_DisableCompression tests client with compression disabled
func TestGetClient_DisableCompression(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:     10 * time.Second,
		RequestTimeout:     30 * time.Second,
		DisableCompression: true,
	}

	client := manager.GetClient(config)
	transport := client.Transport.(*http.Transport)
	assert.True(t, transport.DisableCompression)
}

// TestGetClient_CustomBufferSizes tests client with custom buffer sizes
func TestGetClient_CustomBufferSizes(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:  10 * time.Second,
		RequestTimeout:  30 * time.Second,
		WriteBufferSize: 8192,
		ReadBufferSize:  8192,
	}

	client := manager.GetClient(config)
	transport := client.Transport.(*http.Transport)
	assert.Equal(t, 8192, transport.WriteBufferSize)
	assert.Equal(t, 8192, transport.ReadBufferSize)
}

// TestGetClient_HTTP2 tests client with HTTP/2 enabled
func TestGetClient_HTTP2(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	config := &Config{
		ConnectTimeout:    10 * time.Second,
		RequestTimeout:    30 * time.Second,
		ForceAttemptHTTP2: true,
	}

	client := manager.GetClient(config)
	transport := client.Transport.(*http.Transport)
	assert.True(t, transport.ForceAttemptHTTP2)
}

// TestConfig_FingerprintWithAllFields tests fingerprint with all fields
func TestConfig_FingerprintWithAllFields(t *testing.T) {
	t.Parallel()

	config := &Config{
		ConnectTimeout:        10 * time.Second,
		RequestTimeout:        30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   100,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true,
		WriteBufferSize:       4096,
		ReadBufferSize:        4096,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		ProxyURL:              "http://proxy.example.com:8080",
	}

	fp1 := config.getFingerprint()
	fp2 := config.getFingerprint()

	assert.Equal(t, fp1, fp2)
	assert.NotEmpty(t, fp1)
}

// TestGetClient_ConcurrentSameConfig tests concurrent access with same config
func TestGetClient_ConcurrentSameConfig(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()
	config := &Config{
		ConnectTimeout:  10 * time.Second,
		RequestTimeout:  30 * time.Second,
		IdleConnTimeout: 90 * time.Second,
	}

	const goroutines = 100
	results := make(chan *http.Client, goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		go func() {
			<-start // Wait for signal
			client := manager.GetClient(config)
			results <- client
		}()
	}
	close(start) // Release all goroutines simultaneously

	// Collect all results in main goroutine
	clients := make([]*http.Client, goroutines)
	for i := 0; i < goroutines; i++ {
		clients[i] = <-results
	}

	// All clients should be the same instance
	for i := 1; i < goroutines; i++ {
		assert.Equal(t, clients[0], clients[i])
	}
}

// TestGetClient_ConcurrentDifferentConfigs tests concurrent access with different configs
func TestGetClient_ConcurrentDifferentConfigs(t *testing.T) {
	t.Parallel()

	manager := NewHTTPClientManager()

	const goroutines = 10
	results := make(chan *http.Client, goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			<-start // Wait for signal
			config := &Config{
				ConnectTimeout:  time.Duration(idx+1) * time.Second,
				RequestTimeout:  30 * time.Second,
				IdleConnTimeout: 90 * time.Second,
			}
			client := manager.GetClient(config)
			results <- client
		}(i)
	}
	close(start) // Release all goroutines simultaneously

	// Collect all results in main goroutine
	clients := make([]*http.Client, goroutines)
	for i := 0; i < goroutines; i++ {
		clients[i] = <-results
	}

	// All clients should be different
	for i := 0; i < goroutines; i++ {
		assert.NotNil(t, clients[i])
		for j := i + 1; j < goroutines; j++ {
			assert.NotEqual(t, clients[i], clients[j], "clients[%d] and clients[%d] should be different", i, j)
		}
	}
}

// BenchmarkGetClientWithProxy benchmarks client creation with proxy
func BenchmarkGetClientWithProxy(b *testing.B) {
	manager := NewHTTPClientManager()
	config := &Config{
		ConnectTimeout:  10 * time.Second,
		RequestTimeout:  30 * time.Second,
		IdleConnTimeout: 90 * time.Second,
		ProxyURL:        "http://proxy.example.com:8080",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = manager.GetClient(config)
	}
}

// BenchmarkGetClientWithCompression benchmarks client with compression settings
func BenchmarkGetClientWithCompression(b *testing.B) {
	manager := NewHTTPClientManager()
	config := &Config{
		ConnectTimeout:     10 * time.Second,
		RequestTimeout:     30 * time.Second,
		DisableCompression: true,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = manager.GetClient(config)
	}
}

// BenchmarkFingerprintGeneration benchmarks fingerprint generation
func BenchmarkFingerprintGeneration(b *testing.B) {
	config := &Config{
		ConnectTimeout:        10 * time.Second,
		RequestTimeout:        30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   100,
		ResponseHeaderTimeout: 60 * time.Second,
		ProxyURL:              "http://proxy.example.com:8080",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = config.getFingerprint()
	}
}

func TestGetClient_EvictsExcessClients(t *testing.T) {
	manager := NewHTTPClientManager()
	manager.maxClients = 3

	cfg1 := &Config{RequestTimeout: 1 * time.Second}
	cfg2 := &Config{RequestTimeout: 2 * time.Second}
	cfg3 := &Config{RequestTimeout: 3 * time.Second}

	c1 := manager.GetClient(cfg1)
	c2 := manager.GetClient(cfg2)
	c3 := manager.GetClient(cfg3)
	require.NotNil(t, c1)
	require.NotNil(t, c2)
	require.NotNil(t, c3)

	// Make c1 the least-recently-used so adding a 4th client evicts it.

	fp1 := cfg1.getFingerprint()
	manager.lock.Lock()
	manager.clients[fp1].lastUsed.Store(1)
	manager.lock.Unlock()

	c4 := manager.GetClient(&Config{RequestTimeout: 4 * time.Second})
	require.NotNil(t, c4)

	assert.Len(t, manager.clients, 3, "cache should be capped at maxClients")

	manager.lock.RLock()
	_, c1exists := manager.clients[fp1]
	manager.lock.RUnlock()
	assert.False(t, c1exists, "least-recently-used client should be evicted")

	// Reusing a surviving config returns the same client instance.

	assert.Same(t, c2, manager.GetClient(cfg2))
	assert.Same(t, c3, manager.GetClient(cfg3))
}

func TestGetClient_EvictedClientConnectionsClosed(t *testing.T) {
	manager := NewHTTPClientManager()
	manager.maxClients = 1

	tr := &closeTrackingTransport{}
	fp := "tracked-fp"
	manager.lock.Lock()
	manager.clients[fp] = &clientCacheEntry{client: &http.Client{Transport: tr}}
	manager.clients[fp].lastUsed.Store(1)
	manager.lock.Unlock()

	// Adding a new client evicts the only existing one; its connections close.

	manager.GetClient(&Config{RequestTimeout: 5 * time.Second})
	assert.True(t, tr.closed.Load(), "connections of the evicted client must be closed")
}

type closeTrackingTransport struct {
	closed atomic.Bool
}

func (t *closeTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("not used")
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closed.Store(true)
}

// TestGetClientHitRacesCapacityEviction verifies that a cache hit racing with
// capacity eviction does not lose the recently-used entry.
// GetClient's fast path must refresh lastUsed while still holding the read lock;
// otherwise evictExcessClientsLocked (which runs under the write lock) may
// select the freshly-hit entry based on a stale timestamp, remove it from the
// cache, and lose connection reuse for subsequent requests.
// Each round builds a fresh manager, marks A as the LRU candidate (lastUsed=1)
// and B as the runner-up (lastUsed=2), then races several hits on A against an
// insert that triggers eviction. The pinned B keeps the LRU choice unambiguous:
// a refreshed A (now) is never the minimum, so B is evicted; a stale A (1) is
// the minimum and gets evicted. Observable bug symptom: a fast-path hit returns
// the original clientA while the entry is concurrently evicted. If A is
// legitimately evicted before any hit arrives, the hits go through the slow
// path and return a fresh client, which is fine.
func TestGetClientHitRacesCapacityEviction(t *testing.T) {
	const (
		rounds  = 60
		hitters = 3
	)

	for i := 0; i < rounds; i++ {
		manager := NewHTTPClientManager()
		manager.maxClients = 2

		// Distinct timeouts per round keep fingerprints unique across rounds.
		// Build the per-round offset in seconds, then add it to the base
		// timeout: (1+base) would multiply two durations (a durationcheck
		// error), even though the accidental value happened to be correct.
		base := time.Duration(i*10) * time.Second
		configA := &Config{RequestTimeout: time.Second + base}
		configB := &Config{RequestTimeout: 2*time.Second + base}
		configC := &Config{RequestTimeout: 3*time.Second + base}

		clientA := manager.GetClient(configA)
		require.NotNil(t, clientA)
		manager.GetClient(configB) // populate entry B

		fpA := configA.getFingerprint()
		fpB := configB.getFingerprint()

		// Pin A as the LRU target (1) and B as the runner-up (2). If a hit
		// refreshes A before eviction scans, A's timestamp becomes current and
		// B is strictly the oldest; a stale (unrefreshed) A stays the oldest.
		manager.lock.Lock()
		manager.clients[fpA].lastUsed.Store(1)
		manager.clients[fpB].lastUsed.Store(2)
		manager.lock.Unlock()

		// Race the hits and the insert simultaneously.
		var start, done sync.WaitGroup
		start.Add(hitters + 1)
		done.Add(hitters + 1)

		hits := make([]*http.Client, hitters)
		for h := 0; h < hitters; h++ {
			go func(idx int) {
				start.Done()
				start.Wait()
				hits[idx] = manager.GetClient(configA) // hit: refresh lastUsed
				done.Done()
			}(h)
		}
		go func() {
			start.Done()
			start.Wait()
			manager.GetClient(configC) // insert C: triggers eviction
			done.Done()
		}()

		done.Wait()

		// Only when a hit returned the original clientA does the exact entry
		// matter: that very entry must still be the cached one. A fast-path hit
		// followed by eviction of the same entry (stale lastUsed selecting it)
		// is the bug this test guards against.
		manager.lock.RLock()
		currentA, aExists := manager.clients[fpA]
		manager.lock.RUnlock()
		for h := 0; h < hitters; h++ {
			if hits[h] != clientA {
				continue
			}
			// A hit returned the original clientA, so that exact entry must
			// still be the cached one.
			require.True(t, aExists,
				"hit returned an evicted client (round %d, hitter %d)", i, h)
			assert.Same(t, clientA, currentA.client,
				"hit returned a client whose entry was replaced (round %d, hitter %d)", i, h)
		}
	}
}
