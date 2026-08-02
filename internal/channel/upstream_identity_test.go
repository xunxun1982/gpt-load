package channel

import (
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamIdentityIsStableNormalizedAndOpaque(t *testing.T) {
	proxyURL := "http://proxy-user:proxy-secret@proxy.example.com:8080"
	upstream := UpstreamInfo{
		URL:          mustParseURL("https://API.EXAMPLE.COM/v1/"),
		Weight:       100,
		ProxyURL:     &proxyURL,
		HTTPClient:   &http.Client{},
		StreamClient: &http.Client{},
	}
	channel := &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{upstream}}

	first, err := channel.SelectUpstreamWithClients(mustParseURL("/proxy/group/v1/models"), "group")
	require.NoError(t, err)
	second, err := channel.SelectUpstreamWithClients(mustParseURL("/proxy/group/v1/chat/completions"), "group")
	require.NoError(t, err)

	require.Equal(t, first.Identity, second.Identity)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), first.Identity)
	require.NotContains(t, first.Identity, "proxy-secret")
	require.NotContains(t, first.Identity, "api.example.com")

	normalized := &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{{
		URL:      mustParseURL("https://api.example.com/v1"),
		Weight:   100,
		ProxyURL: &proxyURL,
	}}}
	normalizedSelection, err := normalized.SelectUpstreamWithClients(mustParseURL("/proxy/group/v1/models"), "group")
	require.NoError(t, err)
	require.Equal(t, first.Identity, normalizedSelection.Identity)
}

func TestUpstreamIdentityDiffersForSameURLWithDifferentProxy(t *testing.T) {
	proxyA := "http://proxy-a.example.com:8080"
	proxyB := "http://proxy-b.example.com:8080"
	newChannel := func(proxy *string) *BaseChannel {
		return &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{{
			URL:      mustParseURL("https://api.example.com"),
			Weight:   100,
			ProxyURL: proxy,
		}}}
	}

	selectionA, err := newChannel(&proxyA).SelectUpstreamWithClients(mustParseURL("/proxy/group/v1/models"), "group")
	require.NoError(t, err)
	selectionB, err := newChannel(&proxyB).SelectUpstreamWithClients(mustParseURL("/proxy/group/v1/models"), "group")
	require.NoError(t, err)

	require.NotEqual(t, selectionA.Identity, selectionB.Identity)
}

func TestUpstreamIdentityResolvesAfterReorderUsingCurrentClients(t *testing.T) {
	oldHTTPClient := &http.Client{Timeout: time.Second}
	oldStreamClient := &http.Client{Timeout: 2 * time.Second}
	target := UpstreamInfo{
		URL:          mustParseURL("https://target.example.com/base"),
		Weight:       100,
		HTTPClient:   oldHTTPClient,
		StreamClient: oldStreamClient,
	}
	other := UpstreamInfo{URL: mustParseURL("https://other.example.com"), Weight: 0}
	channel := &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{target, other}}
	originalURL := mustParseURL("/proxy/group/v1/models?limit=10")

	selected, err := channel.SelectUpstreamWithClients(originalURL, "group")
	require.NoError(t, err)

	currentHTTPClient := &http.Client{Timeout: 3 * time.Second}
	currentStreamClient := &http.Client{Timeout: 4 * time.Second}
	target.HTTPClient = currentHTTPClient
	target.StreamClient = currentStreamClient
	channel.Upstreams = []UpstreamInfo{other, target}

	resolved, err := channel.ResolveUpstreamByIdentity(selected.Identity, originalURL, "group")
	require.NoError(t, err)
	require.Equal(t, selected.Identity, resolved.Identity)
	require.Equal(t, selected.URL, resolved.URL)
	require.Same(t, currentHTTPClient, resolved.HTTPClient)
	require.Same(t, currentStreamClient, resolved.StreamClient)
	require.NotSame(t, oldHTTPClient, resolved.HTTPClient)
	require.NotSame(t, oldStreamClient, resolved.StreamClient)
}

func TestUpstreamIdentityRejectsDisabledDeletedAndChangedProxy(t *testing.T) {
	proxyA := "http://proxy-a.example.com:8080"
	target := UpstreamInfo{
		URL:      mustParseURL("https://api.example.com"),
		Weight:   100,
		ProxyURL: &proxyA,
	}
	channel := &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{target}}
	originalURL := mustParseURL("/proxy/group/v1/models")
	selected, err := channel.SelectUpstreamWithClients(originalURL, "group")
	require.NoError(t, err)

	t.Run("disabled", func(t *testing.T) {
		disabled := target
		disabled.Weight = 0
		channel.Upstreams = []UpstreamInfo{disabled}
		_, err := channel.ResolveUpstreamByIdentity(selected.Identity, originalURL, "group")
		require.ErrorContains(t, err, "upstream identity not found")
	})

	t.Run("deleted", func(t *testing.T) {
		channel.Upstreams = nil
		_, err := channel.ResolveUpstreamByIdentity(selected.Identity, originalURL, "group")
		require.ErrorContains(t, err, "upstream identity not found")
	})

	t.Run("proxy changed", func(t *testing.T) {
		proxyB := "http://proxy-b.example.com:8080"
		changed := target
		changed.ProxyURL = &proxyB
		channel.Upstreams = []UpstreamInfo{changed}
		_, err := channel.ResolveUpstreamByIdentity(selected.Identity, originalURL, "group")
		require.ErrorContains(t, err, "upstream identity not found")
	})
}
