package httpclient

import (
	"net/http"
	"testing"
	"time"

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
