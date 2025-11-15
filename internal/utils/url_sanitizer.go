package utils

import (
	"net/url"
	"strings"
)

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
