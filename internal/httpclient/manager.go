package httpclient

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Config defines the parameters for creating an HTTP client.
// This struct is used to generate a unique fingerprint for client reuse.
type Config struct {
	ConnectTimeout        time.Duration
	RequestTimeout        time.Duration
	IdleConnTimeout       time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	ResponseHeaderTimeout time.Duration
	DisableCompression    bool
	WriteBufferSize       int
	ReadBufferSize        int
	ForceAttemptHTTP2     bool
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration
	ProxyURL              string
}

// HTTPClientManager manages the lifecycle of HTTP clients.
// It creates and caches clients based on their configuration fingerprint,
// ensuring that clients with the same configuration are reused.
type HTTPClientManager struct {
	clients map[string]*http.Client
	lock    sync.RWMutex
}

// NewHTTPClientManager creates a new client manager.
func NewHTTPClientManager() *HTTPClientManager {
	return &HTTPClientManager{
		clients: make(map[string]*http.Client),
	}
}

// testProxyConnectivity tests if the proxy is reachable.
// This runs asynchronously to avoid blocking client creation.
// It helps diagnose proxy configuration issues early.
func testProxyConnectivity(proxyURL *url.URL, originalProxyStr string) {
	// Simple TCP connectivity test
	dialer := &net.Dialer{
		Timeout: 3 * time.Second,
	}

	conn, err := dialer.Dial("tcp", proxyURL.Host)
	if err != nil {
		logrus.Warnf("Proxy connectivity test FAILED for '%s': %v", originalProxyStr, err)
		logrus.Warnf("Troubleshooting steps:")
		logrus.Warnf("  1. Verify proxy is running at %s", proxyURL.Host)
		logrus.Warnf("  2. Check firewall allows connection to proxy")
		logrus.Warnf("  3. Verify proxy URL format is correct (http://host:port)")
		logrus.Warnf("  4. Check if proxy requires authentication")
		logrus.Warnf("Requests through this proxy will likely fail!")
		return
	}
	defer conn.Close()

	logrus.Infof("✓ Proxy connectivity test PASSED for '%s' (host: %s)", originalProxyStr, proxyURL.Host)
	logrus.Debugf("Proxy at %s is reachable and accepting TCP connections", proxyURL.Host)
}

// GetClient returns an HTTP client that matches the given configuration.
// If a matching client already exists in the cache, it is returned.
// Otherwise, a new client is created, cached, and returned.
func (m *HTTPClientManager) GetClient(config *Config) *http.Client {
	fingerprint := config.getFingerprint()

	// Fast path with read lock
	m.lock.RLock()
	client, exists := m.clients[fingerprint]
	m.lock.RUnlock()
	if exists {
		return client
	}

	// Slow path with write lock
	m.lock.Lock()
	defer m.lock.Unlock()

	// Double-check in case another goroutine created the client while we were waiting for the lock.
	if client, exists = m.clients[fingerprint]; exists {
		return client
	}

	// Create a new transport and client with the specified configuration.
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   config.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     config.ForceAttemptHTTP2,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ExpectContinueTimeout: config.ExpectContinueTimeout,
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		DisableCompression:    config.DisableCompression,
		WriteBufferSize:       config.WriteBufferSize,
		ReadBufferSize:        config.ReadBufferSize,
	}

	// Set HTTP proxy with validation and detailed logging
	// Trim whitespace from proxy URL before parsing to handle common configuration issues
	trimmedProxyURL := strings.TrimSpace(config.ProxyURL)
	if trimmedProxyURL != "" {
		proxyURL, err := url.Parse(trimmedProxyURL)
		if err != nil {
			logrus.Warnf("Invalid proxy URL '%s' provided, falling back to environment settings: %v", trimmedProxyURL, err)
			transport.Proxy = http.ProxyFromEnvironment
		} else {
			// Validate proxy URL scheme
			if proxyURL.Scheme != "http" && proxyURL.Scheme != "https" && proxyURL.Scheme != "socks5" {
				logrus.Warnf("Unsupported proxy scheme '%s' in URL '%s', falling back to environment settings", proxyURL.Scheme, trimmedProxyURL)
				transport.Proxy = http.ProxyFromEnvironment
			} else {
				// Set proxy with detailed logging
				transport.Proxy = http.ProxyURL(proxyURL)
				logrus.Debugf("HTTP client configured with proxy: %s (scheme: %s, host: %s)", trimmedProxyURL, proxyURL.Scheme, proxyURL.Host)

				// Test proxy connectivity (non-blocking)
				go testProxyConnectivity(proxyURL, trimmedProxyURL)
			}
		}
	} else {
		// ProxyURL was empty or only whitespace, fall back to environment
		transport.Proxy = http.ProxyFromEnvironment
	}

	newClient := &http.Client{
		Transport: transport,
		Timeout:   config.RequestTimeout,
	}

	m.clients[fingerprint] = newClient

	// Log client creation with full configuration details for debugging
	logrus.WithFields(logrus.Fields{
		"fingerprint":  fingerprint,
		"proxy_url":    trimmedProxyURL,
		"timeout":      config.RequestTimeout,
		"has_proxy":    trimmedProxyURL != "",
	}).Debug("Created new HTTP client")

	return newClient
}

// getFingerprint generates a unique string representation of the client configuration.
// ProxyURL is normalized (trimmed) to ensure configs that differ only by whitespace generate the same fingerprint.
func (c *Config) getFingerprint() string {
	normalizedProxy := strings.TrimSpace(c.ProxyURL)
	return fmt.Sprintf(
		"ct:%.0fs|rt:%.0fs|it:%.0fs|mic:%d|mich:%d|rht:%.0fs|dc:%t|wbs:%d|rbs:%d|fh2:%t|tlst:%.0fs|ect:%.0fs|proxy:%s",
		c.ConnectTimeout.Seconds(),
		c.RequestTimeout.Seconds(),
		c.IdleConnTimeout.Seconds(),
		c.MaxIdleConns,
		c.MaxIdleConnsPerHost,
		c.ResponseHeaderTimeout.Seconds(),
		c.DisableCompression,
		c.WriteBufferSize,
		c.ReadBufferSize,
		c.ForceAttemptHTTP2,
		c.TLSHandshakeTimeout.Seconds(),
		c.ExpectContinueTimeout.Seconds(),
		normalizedProxy,
	)
}
