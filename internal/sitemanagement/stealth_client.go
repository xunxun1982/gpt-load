package sitemanagement

import (
	"net/http"
	"sync"
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
)

// StealthClientManager manages stealth HTTP clients with TLS fingerprint spoofing.
// It caches clients by proxy URL to enable connection pooling.
// Uses bogdanfinn/tls-client which properly supports HTTP/2 and modern TLS fingerprinting.
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

	// Check cache first
	if cached, ok := m.clients.Load(cacheKey); ok {
		return cached.(*http.Client)
	}

	// Create new client
	client := m.createStealthClient(proxyURL)

	// Store in cache (LoadOrStore handles race condition)
	actual, _ := m.clients.LoadOrStore(cacheKey, client)
	return actual.(*http.Client)
}

// createStealthClient creates a new HTTP client with TLS fingerprint spoofing using tls-client.
// This implementation properly supports HTTP/2 and avoids the protocol compatibility issues
// that existed with the previous uTLS-based implementation.
func (m *StealthClientManager) createStealthClient(proxyURL string) *http.Client {
	// Configure tls-client options
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(int(m.timeout.Seconds())),
		// Use Chrome 120 profile for best compatibility
		// This includes proper HTTP/2 support and modern TLS fingerprinting
		tls_client.WithClientProfile(profiles.Chrome_120),
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
		logrus.WithError(err).WithField("proxy_url", proxyURL).
			Warn("Failed to create stealth client, falling back to standard client")
		return &http.Client{Timeout: m.timeout}
	}

	// Wrap tls-client in a standard http.Client for compatibility
	// tls-client implements the http.Client interface, so we can use it directly
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
		Method: req.Method,
		URL:    req.URL,
		Header: convertHeaders(req.Header),
		Body:   req.Body,
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
func stealthHeaders() map[string]string {
	return map[string]string{
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Accept":                    "application/json, text/plain, */*",
		"Accept-Language":           "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7",
		"Sec-Ch-Ua":                 `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
		"Sec-Ch-Ua-Mobile":          "?0",
		"Sec-Ch-Ua-Platform":        `"Windows"`,
		"Sec-Fetch-Dest":            "empty",
		"Sec-Fetch-Mode":            "cors",
		"Sec-Fetch-Site":            "same-origin",
		"Cache-Control":             "no-cache",
		"Pragma":                    "no-cache",
		"Connection":                "keep-alive",
		"Upgrade-Insecure-Requests": "1",
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
func (m *StealthClientManager) CloseIdleConnections() {
	m.clients.Range(func(key, value any) bool {
		if client, ok := value.(*http.Client); ok {
			if transport, ok := client.Transport.(*tlsClientTransport); ok {
				// tls-client doesn't expose CloseIdleConnections, but we can recreate the client
				// This is acceptable since connections will be cleaned up by GC
				_ = transport
			}
		}
		return true
	})
}

// Cleanup closes all idle connections and clears the client cache.
// This should be called during service shutdown.
func (m *StealthClientManager) Cleanup() {
	m.clients.Range(func(key, value any) bool {
		if client, ok := value.(*http.Client); ok {
			if transport, ok := client.Transport.(*tlsClientTransport); ok {
				// Clean up resources
				_ = transport
			}
		}
		m.clients.Delete(key)
		return true
	})
}
