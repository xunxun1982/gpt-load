package channel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamIdentityRejectsChangedOrDisabledGatewayBase(t *testing.T) {
	previous := GatewayProxyBaseURL("betterclaude")
	t.Cleanup(func() { restoreGatewayProxyBaseURL("betterclaude", previous) })
	SetGatewayProxyBaseURL("betterclaude", "https://gateway-one.example.com")

	channel := &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{{
		URL: mustParseURL("https://api.example.com"), Weight: 100, GatewayProxy: "betterclaude",
	}}}
	originalURL := mustParseURL("/proxy/group/v1/models")
	selected, err := channel.SelectUpstreamWithClients(originalURL, "group")
	require.NoError(t, err)

	SetGatewayProxyBaseURL("betterclaude", "https://gateway-two.example.com")
	_, err = channel.ResolveUpstreamByIdentity(selected.Identity, originalURL, "group")
	require.ErrorContains(t, err, "upstream identity not found")
	SetGatewayProxyBaseURL("betterclaude", "https://gateway-one.example.com")
	_, err = channel.ResolveUpstreamByIdentity(selected.Identity, originalURL, "group")
	require.NoError(t, err)
	DisableGatewayProxyBaseURL("betterclaude")
	_, err = channel.ResolveUpstreamByIdentity(selected.Identity, originalURL, "group")
	require.ErrorContains(t, err, "upstream identity not found")
}

func TestUpstreamIdentityGatewaySnapshotKeepsRouteAndIdentityConsistent(t *testing.T) {
	previous := GatewayProxyBaseURL("betterclaude")
	t.Cleanup(func() { restoreGatewayProxyBaseURL("betterclaude", previous) })
	SetGatewayProxyBaseURL("betterclaude", "https://gateway-one.example.com")

	channel := &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{{
		URL: mustParseURL("https://api.example.com"), Weight: 100, GatewayProxy: "betterclaude",
	}}}
	originalURL := mustParseURL("/proxy/group/v1/models")
	selected, err := channel.SelectUpstreamWithClients(originalURL, "group")
	require.NoError(t, err)
	require.Equal(t, "https://gateway-one.example.com/openai/api.example.com/v1/models", selected.URL)

	snapshot, err := channel.resolveUpstreamSnapshot(selected.Identity)
	require.NoError(t, err)
	SetGatewayProxyBaseURL("betterclaude", "https://gateway-two.example.com")
	resolved, err := channel.buildUpstreamSelection(snapshot, originalURL, "group")
	require.NoError(t, err)
	require.Equal(t, selected.Identity, resolved.Identity)
	require.Equal(t, selected.URL, resolved.URL)

	current, err := channel.SelectUpstreamWithClients(originalURL, "group")
	require.NoError(t, err)
	require.Equal(t, "https://gateway-two.example.com/openai/api.example.com/v1/models", current.URL)
	require.NotEqual(t, selected.Identity, current.Identity)
}

func TestUpstreamIdentityTreatsWhitespaceGatewayAsDirect(t *testing.T) {
	channel := &BaseChannel{Name: "openai", Upstreams: []UpstreamInfo{{
		URL: mustParseURL("https://api.example.com"), Weight: 100, GatewayProxy: " \t ",
	}}}
	originalURL := mustParseURL("/proxy/group/v1/models")

	selected, err := channel.SelectUpstreamWithClients(originalURL, "group")
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/v1/models", selected.URL)
	require.Empty(t, selected.GatewayProxy)
}
