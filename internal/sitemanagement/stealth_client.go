package sitemanagement

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

const (
	// BypassMethodNone indicates no bypass method is used
	BypassMethodNone = "none"
	// BypassMethodStealth uses TLS fingerprint spoofing to bypass Cloudflare
	BypassMethodStealth = "stealth"
)

// StealthClientManager manages stealth HTTP clients with TLS fingerprint spoofing.
// It caches clients by proxy URL to enable connection pooling.
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

// createStealthClient creates a new HTTP client with TLS fingerprint spoofing.
// IMPORTANT: When an HTTP proxy is configured, the uTLS fingerprint spoofing is bypassed
// because Go's http.Transport with HTTP CONNECT tunneling does not use DialTLSContext
// after the CONNECT succeeds - the TLS handshake uses Go's standard crypto/tls instead.
// This is a known limitation. For full TLS fingerprint spoofing through proxies,
// a custom RoundTripper with explicit CONNECT+uTLS handling would be required.
// For most use cases (direct connections), the fingerprint spoofing works correctly.
func (m *StealthClientManager) createStealthClient(proxyURL string) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		// Use custom DialTLS for TLS fingerprint spoofing (direct connections only)
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return m.dialTLSWithFingerprint(ctx, network, addr, dialer)
		},
	}

	// Configure proxy if provided
	// Note: When proxy is used, DialTLSContext is not invoked for HTTPS requests
	// because the proxy handles the CONNECT tunnel. TLS fingerprint spoofing
	// will not be effective in this case.
	if proxyURL != "" {
		if parsedProxy, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsedProxy)
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   m.timeout,
	}
}

// dialTLSWithFingerprint establishes a TLS connection with Chrome-like fingerprint.
func (m *StealthClientManager) dialTLSWithFingerprint(ctx context.Context, network, addr string, dialer *net.Dialer) (net.Conn, error) {
	// Extract hostname for SNI
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	// Establish TCP connection
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial failed: %w", err)
	}

	// Create uTLS connection with Chrome fingerprint
	tlsConn := utls.UClient(conn, &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: false,
		MinVersion:         tls.VersionTLS12,
	}, utls.HelloChrome_Auto)

	// Perform TLS handshake
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tls handshake failed: %w", err)
	}

	return tlsConn, nil
}

// stealthHeaders returns browser-like HTTP headers for stealth requests.
// These headers help bypass basic bot detection.
// Note: Accept-Encoding is intentionally omitted to let Go's http.Client handle
// automatic gzip/deflate decompression. Setting it manually would disable
// Go's transparent decompression, causing json.Unmarshal to fail on compressed responses.
func stealthHeaders() map[string]string {
	return map[string]string{
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		"Accept":                    "application/json, text/plain, */*",
		"Accept-Language":           "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7",
		"Sec-Ch-Ua":                 `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
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
