package utils

import (
	"net/url"
	"strings"
)

// sensitiveQueryParams lists query parameter names that should be redacted from logs.
// These parameters may contain authentication tokens or other sensitive data.
// Based on security best practices for credential leakage prevention.
var sensitiveQueryParams = []string{
	"key",
	"api_key",
	"apikey",
	"token",
	"access_token",
	"refresh_token",
	"auth",
	"authorization",
	"secret",
	"client_secret",
	"password",
}

// SanitizeURLForLog removes sensitive query parameters and user info from a URL.
// This prevents leaking credentials and auth tokens in logs.
func SanitizeURLForLog(u *url.URL) string {
	if u == nil {
		return ""
	}
	copy := *u
	copy.User = nil

	// Remove sensitive query parameters
	if copy.RawQuery != "" {
		query := copy.Query()
		for _, param := range sensitiveQueryParams {
			if query.Has(param) {
				query.Set(param, "[REDACTED]")
			}
		}
		copy.RawQuery = query.Encode()
	}

	return copy.String()
}

// SanitizeRequestURLForLog sanitizes a request URL string for logging.
// It removes sensitive query parameters to prevent credential leakage.
func SanitizeRequestURLForLog(urlStr string) string {
	if urlStr == "" {
		return ""
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		// Best-effort: return original if parsing fails
		return urlStr
	}
	return SanitizeURLForLog(u)
}

// SanitizeProxyURLForLog returns a string form of the URL with user info removed.
// This prevents leaking credentials (e.g., http://user:pass@host:port) in logs.
func SanitizeProxyURLForLog(u *url.URL) string {
	if u == nil {
		return ""
	}
	copy := *u
	copy.User = nil
	return copy.String()
}

// SanitizeProxyString tries to remove user info from a proxy URL string.
// If parsing fails, it performs a best-effort removal of the userinfo segment.
func SanitizeProxyString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil {
		return SanitizeProxyURLForLog(u)
	}
	// Best-effort removal if parsing failed
	schemeIdx := strings.Index(s, "://")
	atIdx := strings.LastIndex(s, "@")
	if schemeIdx >= 0 && atIdx > schemeIdx+3 {
		return s[:schemeIdx+3] + s[atIdx+1:]
	}
	return s
}
