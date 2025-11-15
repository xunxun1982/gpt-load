package utils

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sirupsen/logrus"
)

// ProxyDiagnostics contains diagnostic information about a proxy connection.
type ProxyDiagnostics struct {
	ProxyURL          string
	TCPConnectable    bool
	TCPError          error
	HTTPConnectable   bool
	HTTPError         error
	ResponseTime      time.Duration
	RecommendedAction string
}

// DiagnoseProxy performs comprehensive diagnostics on a proxy configuration.
// This helps identify common proxy issues like:
// - Proxy server not running
// - Firewall blocking connection
// - Incorrect proxy URL format
// - Proxy authentication issues
func DiagnoseProxy(proxyURLStr string, testTargetURL string) *ProxyDiagnostics {
	diag := &ProxyDiagnostics{
		ProxyURL: proxyURLStr,
	}

	// Parse proxy URL
	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		diag.RecommendedAction = fmt.Sprintf("Invalid proxy URL format: %v. Expected format: http://host:port or socks5://host:port", err)
		return diag
	}

	// Test 1: TCP connectivity to proxy
	logrus.Debugf("Testing TCP connectivity to proxy %s...", proxyURL.Host)
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
	}

	conn, err := dialer.Dial("tcp", proxyURL.Host)
	if err != nil {
		diag.TCPConnectable = false
		diag.TCPError = err
		diag.RecommendedAction = fmt.Sprintf("Cannot connect to proxy at %s. Please check: 1) Proxy is running, 2) Firewall allows connection, 3) Host and port are correct", proxyURL.Host)
		logrus.Warnf("TCP connectivity test failed: %v", err)
		return diag
	}
	conn.Close()
	diag.TCPConnectable = true
	logrus.Debugf("TCP connectivity test passed for %s", proxyURL.Host)

	// Test 2: HTTP request through proxy
	if testTargetURL == "" {
		testTargetURL = "https://www.google.com"
	}

	logrus.Debugf("Testing HTTP request through proxy to %s...", testTargetURL)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}

	startTime := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testTargetURL, nil)
	if err != nil {
		diag.HTTPError = err
		diag.RecommendedAction = fmt.Sprintf("Failed to create test request: %v", err)
		return diag
	}

	resp, err := client.Do(req)
	diag.ResponseTime = time.Since(startTime)

	if err != nil {
		diag.HTTPConnectable = false
		diag.HTTPError = err

		// Provide specific recommendations based on error type
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			diag.RecommendedAction = fmt.Sprintf("Proxy connection timeout after %.2fs. Proxy may be slow or not responding to HTTP requests.", diag.ResponseTime.Seconds())
		} else {
			diag.RecommendedAction = fmt.Sprintf("HTTP request through proxy failed: %v. Proxy may require authentication or have configuration issues.", err)
		}
		logrus.Warnf("HTTP connectivity test failed: %v", err)
		return diag
	}
	defer resp.Body.Close()

	diag.HTTPConnectable = true
	logrus.Infof("HTTP connectivity test passed. Response time: %.2fs, Status: %d", diag.ResponseTime.Seconds(), resp.StatusCode)

	if resp.StatusCode == 407 {
		diag.RecommendedAction = "Proxy requires authentication (HTTP 407). Please configure proxy credentials in the URL: http://user:pass@host:port"
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		diag.RecommendedAction = "Proxy is working correctly!"
	} else {
		diag.RecommendedAction = fmt.Sprintf("Proxy returned unexpected status code: %d. This may indicate proxy configuration issues.", resp.StatusCode)
	}

	return diag
}

// LogProxyDiagnostics logs the diagnostic results in a user-friendly format.
func LogProxyDiagnostics(diag *ProxyDiagnostics) {
	logrus.Info("=== Proxy Diagnostics Report ===")
	logrus.Infof("Proxy URL: %s", SanitizeProxyString(diag.ProxyURL))
	logrus.Infof("TCP Connectable: %v", diag.TCPConnectable)
	if diag.TCPError != nil {
		logrus.Infof("TCP Error: %v", diag.TCPError)
	}
	logrus.Infof("HTTP Connectable: %v", diag.HTTPConnectable)
	if diag.HTTPError != nil {
		logrus.Infof("HTTP Error: %v", diag.HTTPError)
	}
	if diag.ResponseTime > 0 {
		logrus.Infof("Response Time: %.2fs", diag.ResponseTime.Seconds())
	}
	logrus.Infof("Recommendation: %s", diag.RecommendedAction)
	logrus.Info("================================")
}
