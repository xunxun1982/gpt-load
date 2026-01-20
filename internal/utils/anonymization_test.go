package utils

import (
	"net/http"
	"testing"
)

// TestCleanAnonymizationHeaders tests header anonymization
func TestCleanAnonymizationHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		headers         map[string]string
		shouldBeRemoved []string
		shouldRemain    []string
	}{
		{
			"CloudflareHeaders",
			map[string]string{
				"CF-Connecting-IP": "1.2.3.4",
				"CF-Ray":           "abc123",
				"Content-Type":     "application/json",
			},
			[]string{"CF-Connecting-IP", "CF-Ray"},
			[]string{"Content-Type"},
		},
		{
			"ProxyHeaders",
			map[string]string{
				"X-Forwarded-For": "1.2.3.4",
				"X-Real-IP":       "1.2.3.4",
				"X-Client-IP":     "1.2.3.4",
				"User-Agent":      "Mozilla/5.0",
			},
			[]string{"X-Forwarded-For", "X-Real-IP", "X-Client-IP"},
			[]string{"User-Agent"},
		},
		{
			"TrackingHeaders",
			map[string]string{
				"X-Request-ID":     "req-123",
				"X-Correlation-ID": "corr-456",
				"Traceparent":      "00-abc-def-01",
				"Accept":           "application/json",
			},
			[]string{"X-Request-ID", "X-Correlation-ID", "Traceparent"},
			[]string{"Accept"},
		},
		{
			"BrowserFingerprintingHeaders",
			map[string]string{
				"Sec-CH-UA":          "Chrome",
				"Sec-CH-UA-Mobile":   "?0",
				"Sec-CH-UA-Platform": "Windows",
				"Sec-Fetch-Site":     "same-origin",
				"Sec-Fetch-Mode":     "navigate",
				"Accept-Language":    "en-US",
			},
			[]string{"Sec-CH-UA", "Sec-CH-UA-Mobile", "Sec-CH-UA-Platform", "Sec-Fetch-Site", "Sec-Fetch-Mode"},
			[]string{"Accept-Language"},
		},
		{
			"AWSHeaders",
			map[string]string{
				"X-Amzn-Trace-ID":   "trace-123",
				"X-Amzn-Request-ID": "req-456",
				"Authorization":     "Bearer token",
			},
			[]string{"X-Amzn-Trace-ID", "X-Amzn-Request-ID"},
			[]string{"Authorization"},
		},
		{
			"RefererHeader",
			map[string]string{
				"Referer":    "https://proxy.example.com",
				"User-Agent": "Mozilla/5.0",
			},
			[]string{"Referer"},
			[]string{"User-Agent"},
		},
		{
			"NilRequest",
			nil,
			[]string{},
			[]string{},
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.headers == nil {
				CleanAnonymizationHeaders(nil)
				return
			}

			req, _ := http.NewRequest("GET", "http://example.com", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			CleanAnonymizationHeaders(req)

			for _, header := range tt.shouldBeRemoved {
				if req.Header.Get(header) != "" {
					t.Errorf("Header %q should be removed but still exists", header)
				}
			}

			for _, header := range tt.shouldRemain {
				if req.Header.Get(header) == "" {
					t.Errorf("Header %q should remain but was removed", header)
				}
			}
		})
	}
}

// TestCleanClientAuthHeaders tests client authentication header removal
func TestCleanClientAuthHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		headers         map[string]string
		shouldBeRemoved []string
		shouldRemain    []string
	}{
		{
			"AllAuthHeaders",
			map[string]string{
				"Authorization":       "Bearer token",
				"X-Api-Key":           "key123",
				"X-Goog-Api-Key":      "goog-key",
				"Proxy-Authorization": "Basic abc",
				"Content-Type":        "application/json",
			},
			[]string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Proxy-Authorization"},
			[]string{"Content-Type"},
		},
		{
			"OnlyAuthorization",
			map[string]string{
				"Authorization": "Bearer token",
				"User-Agent":    "Mozilla/5.0",
			},
			[]string{"Authorization"},
			[]string{"User-Agent"},
		},
		{
			"NoAuthHeaders",
			map[string]string{
				"Content-Type": "application/json",
				"Accept":       "application/json",
			},
			[]string{},
			[]string{"Content-Type", "Accept"},
		},
		{
			"NilRequest",
			nil,
			[]string{},
			[]string{},
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.headers == nil {
				CleanClientAuthHeaders(nil)
				return
			}

			req, _ := http.NewRequest("GET", "http://example.com", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			CleanClientAuthHeaders(req)

			for _, header := range tt.shouldBeRemoved {
				if req.Header.Get(header) != "" {
					t.Errorf("Header %q should be removed but still exists", header)
				}
			}

			for _, header := range tt.shouldRemain {
				if req.Header.Get(header) == "" {
					t.Errorf("Header %q should remain but was removed", header)
				}
			}
		})
	}
}

// TestCleanAnonymizationHeadersCaseInsensitive tests case-insensitive header matching
// Note: http.Header.Set canonicalizes keys, so we test each variant separately
func TestCleanAnonymizationHeadersCaseInsensitive(t *testing.T) {
	variants := []string{"x-forwarded-for", "X-FORWARDED-FOR", "X-Forwarded-For"}
	for _, header := range variants {
		t.Run(header, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			req.Header.Set(header, "1.2.3.4")

			CleanAnonymizationHeaders(req)

			if req.Header.Get(header) != "" {
				t.Errorf("Header %q should be removed", header)
			}
		})
	}
}

// BenchmarkCleanAnonymizationHeaders benchmarks header cleaning
func BenchmarkCleanAnonymizationHeaders(b *testing.B) {
	b.ReportAllocs()
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Request-ID", "req-123")
	req.Header.Set("Sec-CH-UA", "Chrome")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Content-Type", "application/json")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Clone headers for each iteration
		testReq, _ := http.NewRequest("GET", "http://example.com", nil)
		for k, v := range req.Header {
			testReq.Header[k] = v
		}
		CleanAnonymizationHeaders(testReq)
	}
}

// BenchmarkCleanClientAuthHeaders benchmarks auth header cleaning
func BenchmarkCleanClientAuthHeaders(b *testing.B) {
	b.ReportAllocs()
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("X-Api-Key", "key123")
	req.Header.Set("Content-Type", "application/json")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testReq, _ := http.NewRequest("GET", "http://example.com", nil)
		for k, v := range req.Header {
			testReq.Header[k] = v
		}
		CleanClientAuthHeaders(testReq)
	}
}
