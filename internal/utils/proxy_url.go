package utils

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeProxyURL keeps the string contract for callers that persist normalized values.
func NormalizeProxyURL(raw string) (string, error) {
	parsed, err := ParseProxyURL(raw)
	if err != nil {
		return "", err
	}
	if parsed == nil {
		return "", nil
	}
	return parsed.String(), nil
}

// ParseProxyURL trims and validates a supported outbound proxy URL for callers needing *url.URL.
func ParseProxyURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid port") {
			return nil, fmt.Errorf("invalid proxy URL: invalid port")
		}
		// Do not wrap url.Parse errors here; parse errors can contain proxy credentials.
		return nil, fmt.Errorf("invalid proxy URL")
	}
	if parsed.Scheme == "" {
		return nil, fmt.Errorf("invalid proxy URL: missing scheme")
	}
	if !IsSupportedProxyScheme(parsed.Scheme) {
		return nil, fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy URL: missing host")
	}
	if strings.LastIndex(parsed.Host, ":") > strings.LastIndex(parsed.Host, "]") {
		port := parsed.Port()
		if port == "" {
			return nil, fmt.Errorf("invalid proxy URL: invalid port")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, fmt.Errorf("invalid proxy URL: invalid port")
		}
	}
	return parsed, nil
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
