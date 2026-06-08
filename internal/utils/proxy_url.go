package utils

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeProxyURL trims and validates a supported outbound proxy URL.
func NormalizeProxyURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid proxy URL")
	}
	if !IsSupportedProxyScheme(parsed.Scheme) {
		return "", fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}
	return parsed.String(), nil
}

// IsSupportedProxyScheme reports whether the scheme is currently supported.
func IsSupportedProxyScheme(scheme string) bool {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "http", "https", "socks5":
		return true
	default:
		return false
	}
}
