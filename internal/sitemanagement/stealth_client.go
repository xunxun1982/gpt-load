package sitemanagement

import (
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	http_tls "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/sirupsen/logrus"
)

const (
	// BypassMethodNone indicates no bypass method is used
	BypassMethodNone = "none"
	// BypassMethodStealth uses TLS fingerprint spoofing to bypass Cloudflare
	BypassMethodStealth = "stealth"

	// clientIdleEvictionTimeout is the idle threshold for evicting cached HTTP
	// clients during periodic cleanup. Entries unused for longer than this are
	// removed and their connections closed. Per-proxy clients each carry a full
	// connection pool (and stealth entries a heavy tls-client instance), so the
	// eviction bounds memory and open connections; evicted entries are rebuilt
	// on the next GetClient call (idle-timeout cache eviction best practice).
	clientIdleEvictionTimeout = 1 * time.Hour
)

// timedHTTPClientEntry wraps an *http.Client with last-use tracking backing the
// idle-eviction policy of the sitemanagement client caches.
type timedHTTPClientEntry struct {
	client   *http.Client
	lastUsed atomic.Int64 // UnixNano of the last GetClient hit; 0 = never hit
	// mu serializes lastUsed refreshes against eviction checks so a client
	// that was just used cannot be evicted as idle (the eviction check-reload race).
	mu sync.Mutex
}

// closeHTTPClientIdleConnections closes idle connections of a cached HTTP client.
// Supports both the tls-client wrapper transport and the standard http.Transport.
func closeHTTPClientIdleConnections(client *http.Client) {
	if client == nil || client.Transport == nil {
		return
	}
	switch transport := client.Transport.(type) {
	case *tlsClientTransport:
		transport.client.CloseIdleConnections()
	case *http.Transport:
		transport.CloseIdleConnections()
	}
}

// closeCachedHTTPClient closes idle connections of a cache value, accepting
// either a *timedHTTPClientEntry or a raw *http.Client.
func closeCachedHTTPClient(value any) {
	switch v := value.(type) {
	case *timedHTTPClientEntry:
		closeHTTPClientIdleConnections(v.client)
	case *http.Client:
		closeHTTPClientIdleConnections(v)
	}
}

// evictIdleCachedClient applies the idle-eviction policy to one cache entry:
// its idle connections are closed; if the entry has been unused since before
// cutoff it is deleted so the cache cannot accumulate idle clients. Raw
// *http.Client values are closed but never deleted (legacy direct stores).
func evictIdleCachedClient(clients *sync.Map, key, value any, cutoff int64) {
	if entry, ok := value.(*timedHTTPClientEntry); ok {
		// Check idle status and delete under the entry lock so an in-flight
		// GetClient refresh of lastUsed cannot be lost to this eviction check.
		entry.mu.Lock()
		idle := entry.lastUsed.Load() < cutoff
		if idle {
			clients.Delete(key)
		}
		entry.mu.Unlock()
		// Close idle connections outside the lock so concurrent GetClient hits
		// are not blocked while the drained connections are being closed.
		if idle {
			closeHTTPClientIdleConnections(entry.client)
		}
		return
	}
	if client, ok := value.(*http.Client); ok {
		closeHTTPClientIdleConnections(client)
	}
}

// StealthClientManager manages stealth HTTP clients with TLS fingerprint spoofing.
// It caches clients by proxy URL to enable connection pooling.
// Uses bogdanfinn/tls-client which properly supports HTTP/2 and modern TLS fingerprinting.
// Cache eviction contract: entries idle for longer than clientIdleEvictionTimeout
// are removed by evictIdleClients (called from the periodic cleanup of the
// owning services); evicted clients are transparently rebuilt on next GetClient.
type StealthClientManager struct {
	// clients stores cached stealth HTTP clients keyed by proxy URL (empty string for direct)
	clients sync.Map
	// timeout for HTTP requests
	timeout time.Duration
}

// NewStealthClientManager creates a new stealth client manager.
func NewStealthClientManager(timeout time.Duration) *StealthClientManager {
	return &StealthClientManager{
		timeout: timeout,
	}
}

// GetClient returns a stealth HTTP client, optionally configured with proxy.
// Clients are cached by proxy URL for connection reuse.
// Returns a standard http.Client that wraps the tls-client for compatibility.
func (m *StealthClientManager) GetClient(proxyURL string) *http.Client {
	cacheKey := proxyURL
	if cacheKey == "" {
		cacheKey = "__direct__"
	}

	// Check cache first; refresh last-use on every hit.
	if cached, ok := m.clients.Load(cacheKey); ok {
		if entry, ok := cached.(*timedHTTPClientEntry); ok {
			// Refresh under the entry lock so eviction cannot delete a client
			// between the freshness check and this refresh (see evictIdleCachedClient).
			entry.mu.Lock()
			entry.lastUsed.Store(time.Now().UnixNano())
			entry.mu.Unlock()
			return entry.client
		}
		if client, ok := cached.(*http.Client); ok {
			return client
		}
	}

	// Create new client
	client := m.createStealthClient(proxyURL)

	// Store in cache (LoadOrStore handles race condition)
	entry := &timedHTTPClientEntry{client: client}
	entry.lastUsed.Store(time.Now().UnixNano())
	actual, _ := m.clients.LoadOrStore(cacheKey, entry)
	if actualEntry, ok := actual.(*timedHTTPClientEntry); ok {
		// LoadOrStore returned an entry already stored by another goroutine;
		// refresh it under the entry lock too so this use is never lost to a
		// concurrent eviction check (same contract as the cache-hit path above).
		actualEntry.mu.Lock()
		actualEntry.lastUsed.Store(time.Now().UnixNano())
		actualEntry.mu.Unlock()
		return actualEntry.client
	}
	return actual.(*http.Client)
}

// createStealthClient creates a new HTTP client with TLS fingerprint spoofing using tls-client.
// This implementation properly supports HTTP/2 and avoids the protocol compatibility issues
// that existed with the previous uTLS-based implementation.
func (m *StealthClientManager) createStealthClient(proxyURL string) *http.Client {
	// Configure tls-client options
	options := []tls_client.HttpClientOption{
		// Use the newest Chrome profile supported by the pinned tls-client version.
		// This includes proper HTTP/2 support and modern TLS fingerprinting
		tls_client.WithClientProfile(profiles.Chrome_146),
		// Enable random TLS extension order for better fingerprint randomization
		tls_client.WithRandomTLSExtensionOrder(),
	}

	// Add proxy if provided
	if proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(proxyURL))
	}

	// Create tls-client
	tlsClient, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		// Sanitize proxy URL before logging to avoid credential leakage
		sanitizedProxy := proxyURL
		if proxyURL != "" {
			if parsed, parseErr := url.Parse(proxyURL); parseErr == nil && parsed.User != nil {
				parsed.User = nil
				sanitizedProxy = parsed.String()
			}
		}
		logrus.WithError(err).WithField("proxy_url", sanitizedProxy).
			Warn("Failed to create stealth client, falling back to standard client")

		// Fallback client should preserve proxy settings
		transport := &http.Transport{
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     5 * time.Second,
		}
		if proxyURL != "" {
			if parsed, parseErr := url.Parse(proxyURL); parseErr == nil {
				transport.Proxy = http.ProxyURL(parsed)
			}
		}
		return &http.Client{
			Transport: transport,
			Timeout:   m.timeout,
		}
	}

	// Wrap tls-client in a standard http.Client for compatibility
	// Timeout is set on the outer http.Client for consistent behavior
	return &http.Client{
		Transport: &tlsClientTransport{client: tlsClient},
		Timeout:   m.timeout,
	}
}

// tlsClientTransport wraps tls-client to implement http.RoundTripper interface
type tlsClientTransport struct {
	client tls_client.HttpClient
}

// RoundTrip implements http.RoundTripper interface
func (t *tlsClientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Convert standard http.Request to fhttp.Request
	fhttpReq := &http_tls.Request{
		Method:        req.Method,
		URL:           req.URL,
		Header:        convertHeaders(req.Header),
		Body:          req.Body,
		Host:          req.Host,
		ContentLength: req.ContentLength,
	}

	// Copy context
	fhttpReq = fhttpReq.WithContext(req.Context())

	// Execute request using tls-client
	fhttpResp, err := t.client.Do(fhttpReq)
	if err != nil {
		return nil, err
	}

	// Convert fhttp.Response to standard http.Response
	return &http.Response{
		Status:        fhttpResp.Status,
		StatusCode:    fhttpResp.StatusCode,
		Proto:         fhttpResp.Proto,
		ProtoMajor:    fhttpResp.ProtoMajor,
		ProtoMinor:    fhttpResp.ProtoMinor,
		Header:        convertHeadersBack(fhttpResp.Header),
		Body:          fhttpResp.Body,
		ContentLength: fhttpResp.ContentLength,
		Request:       req,
	}, nil
}

// convertHeaders converts standard http.Header to fhttp.Header
func convertHeaders(h http.Header) http_tls.Header {
	fh := make(http_tls.Header, len(h))
	for k, v := range h {
		fh[k] = v
	}
	return fh
}

// convertHeadersBack converts fhttp.Header to standard http.Header
func convertHeadersBack(fh http_tls.Header) http.Header {
	h := make(http.Header, len(fh))
	for k, v := range fh {
		h[k] = v
	}
	return h
}

// stealthHeaders returns browser-like HTTP headers for stealth requests.
// These headers help bypass basic bot detection.
// Note: Accept-Encoding is intentionally omitted to let Go's http.Client handle
// automatic gzip/deflate decompression. Setting it manually would disable
// Go's transparent decompression, causing json.Unmarshal to fail on compressed responses.
// Note: Connection header is omitted as it violates RFC 9113 §8.2.2 for HTTP/2.
func stealthHeaders() map[string]string {
	return map[string]string{
		"User-Agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		"Accept":             "application/json, text/plain, */*",
		"Accept-Language":    "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7",
		"Sec-Ch-Ua":          `"Not_A Brand";v="8", "Chromium";v="146", "Google Chrome";v="146"`,
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"Windows"`,
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "same-origin",
		"Cache-Control":      "no-cache",
		"Pragma":             "no-cache",
	}
}

// applyStealthHeaders applies stealth headers to an HTTP request.
// It preserves existing headers and only sets missing ones.
func applyStealthHeaders(req *http.Request, baseURL string) {
	headers := stealthHeaders()

	// Set Referer and Origin based on base URL
	if baseURL != "" {
		if req.Header.Get("Referer") == "" {
			req.Header.Set("Referer", baseURL)
		}
		if req.Header.Get("Origin") == "" {
			req.Header.Set("Origin", baseURL)
		}
	}

	// Apply stealth headers (don't override existing)
	for key, value := range headers {
		if req.Header.Get(key) == "" {
			req.Header.Set(key, value)
		}
	}
}

// isStealthBypassMethod checks if the bypass method requires stealth client.
func isStealthBypassMethod(method string) bool {
	return method == BypassMethodStealth
}

// CloseIdleConnections closes idle connections for all cached stealth clients.
// This should be called after batch operations complete to free resources.
// Cache entries are never removed here; eviction happens via evictIdleClients.
func (m *StealthClientManager) CloseIdleConnections() {
	m.clients.Range(func(key, value any) bool {
		closeCachedHTTPClient(value)
		return true
	})
}

// evictIdleClients removes cached clients unused for longer than idleTimeout and
// closes their connections. Evicted clients are transparently recreated by the
// next GetClient call. Callers run this from their periodic cleanup.
func (m *StealthClientManager) evictIdleClients(idleTimeout time.Duration) {
	cutoff := time.Now().Add(-idleTimeout).UnixNano()
	m.clients.Range(func(key, value any) bool {
		evictIdleCachedClient(&m.clients, key, value, cutoff)
		return true
	})
}

// Cleanup closes all idle connections and clears the client cache.
// This should be called during service shutdown.
func (m *StealthClientManager) Cleanup() {
	m.clients.Range(func(key, value any) bool {
		closeCachedHTTPClient(value)
		m.clients.Delete(key)
		return true
	})
}
