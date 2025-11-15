package utils

import (
	"net/http"
	"strings"
)

// blacklistedHeaders contains headers that should be removed for anonymization.
// These headers can reveal proxy infrastructure, client identity, or tracking information.
// Based on best practices from Supabase Edge Functions and anti-detection research (2025).
// Using a map for O(1) lookup performance.
var blacklistedHeaders = map[string]bool{
	// Cloudflare headers - expose CDN/proxy usage
	"cf-connecting-ip": true,
	"cf-ipcountry":     true,
	"cf-ray":           true,
	"cf-visitor":       true,
	"cf-worker":        true,
	"cdn-loop":         true,
	"cf-ew-via":        true,
	"cf-pseudo-ipv4":   true,

	// AWS/Lambda headers - expose cloud infrastructure
	"x-amzn-trace-id":   true,
	"x-amzn-request-id": true,
	"x-amz-cf-id":       true,

	// Proxy and forwarding headers - primary proxy detection vectors
	"x-forwarded-for":          true,
	"x-forwarded-host":         true,
	"x-forwarded-proto":        true,
	"x-forwarded-server":       true,
	"x-forwarded":              true,
	"forwarded-for":            true,
	"x-original-forwarded-for": true,
	"x-real-ip":                true,
	"x-original-host":          true,
	"x-client-ip":              true,
	"x-cluster-client-ip":      true,
	"true-client-ip":           true,
	"forwarded":                true,
	"via":                      true,

	// Tracking and correlation headers - expose request tracing
	"x-request-id":     true,
	"x-correlation-id": true,
	"x-trace-id":       true,
	"baggage":          true,
	"sb-request-id":    true,
	"sentry-trace":     true,
	"traceparent":      true,
	"tracestate":       true,

	// Supabase specific headers - expose edge function usage
	"x-supabase-request-id": true,

	// Browser fingerprinting headers - handled separately via prefix check
	// sec-ch-ua, sec-ch-ua-mobile, sec-ch-ua-platform
	// sec-fetch-site, sec-fetch-mode, sec-fetch-dest

	// Referer can leak proxy origin
	"referer": true,
}

// CleanAnonymizationHeaders removes blacklisted headers and browser fingerprinting headers.
// This implements double-layer anonymization: dynamic IP hiding + header cleaning.
// Based on best practices from Supabase Edge Functions and anti-detection research (2025).
// Performance: O(n) where n is the number of headers in the request.
//
// IMPORTANT: This is a pass-through proxy. We only REMOVE headers, never ADD or MODIFY them.
// This preserves the original client's fingerprint while removing proxy-revealing metadata.
func CleanAnonymizationHeaders(req *http.Request) {
	if req == nil || req.Header == nil {
		return
	}

	// Remove blacklisted headers and fingerprinting headers
	for header := range req.Header {
		lowerHeader := strings.ToLower(header)

		// Check blacklist (O(1) map lookup)
		if blacklistedHeaders[lowerHeader] {
			req.Header.Del(header)
			continue
		}

		// Remove browser fingerprinting headers
		// These expose automation/proxy usage:
		// - sec-ch-ua-* (Chrome client hints)
		// - sec-fetch-* (fetch metadata)
		if strings.HasPrefix(lowerHeader, "sec-ch-ua") || strings.HasPrefix(lowerHeader, "sec-fetch-") {
			req.Header.Del(header)
		}
	}
}

// CleanClientAuthHeaders removes client authentication headers.
// These headers contain the client's credentials and must be removed before forwarding to upstream.
// This is called after the proxy has validated the client's authentication.
func CleanClientAuthHeaders(req *http.Request) {
	if req == nil || req.Header == nil {
		return
	}

	// Remove all common authentication headers
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	req.Header.Del("X-Goog-Api-Key")
	req.Header.Del("Proxy-Authorization")
}
