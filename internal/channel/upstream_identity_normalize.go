package channel

import (
	"net/url"
	"strings"
)

func normalizeUpstreamBaseURL(base *url.URL) string {
	if base == nil {
		return ""
	}
	normalized := *base
	normalized.Scheme = strings.ToLower(normalized.Scheme)
	normalized.Host = strings.ToLower(normalized.Host)
	normalized.Path = strings.TrimRight(normalized.Path, "/")
	normalized.RawPath = strings.TrimRight(normalized.RawPath, "/")
	normalized.RawQuery = ""
	normalized.ForceQuery = false
	normalized.Fragment = ""
	normalized.RawFragment = ""
	return normalized.String()
}

func normalizeProxyURL(proxyURL *string) string {
	if proxyURL == nil {
		return ""
	}
	return strings.TrimSpace(*proxyURL)
}
