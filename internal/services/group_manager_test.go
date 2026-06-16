package services

import (
	"context"
	"encoding/json"
	"testing"

	"gpt-load/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type groupManagerProxyResolverStub struct{}

func (groupManagerProxyResolverStub) ResolveProxyURL(_ context.Context, raw string) (string, error) {
	if raw == "proxy-pool:1" {
		return "http://proxy.example.com:8080", nil
	}
	return "", nil
}

func TestGroupManagerResolveUpstreamProxyReferencesPreservesGatewayProxy(t *testing.T) {
	t.Parallel()

	raw := []byte(`[
		{"url":"https://api-a.example.com","weight":100,"proxy_url":"proxy-pool:1"},
		{"url":"https://api-b.example.com","weight":100,"gateway_proxy":"betterclaude"}
	]`)

	settingsManager := config.NewSystemSettingsManager()
	settingsManager.SetProxyURLResolver(groupManagerProxyResolverStub{})

	resolved := (&GroupManager{settingsManager: settingsManager}).resolveUpstreamProxyReferences(
		context.Background(),
		raw,
		map[string]string{"proxy-pool:1": "http://proxy.example.com:8080"},
	)

	var upstreams []struct {
		URL          string  `json:"url"`
		ProxyURL     *string `json:"proxy_url,omitempty"`
		GatewayProxy string  `json:"gateway_proxy,omitempty"`
	}
	require.NoError(t, json.Unmarshal(resolved, &upstreams))
	require.Len(t, upstreams, 2)
	require.NotNil(t, upstreams[0].ProxyURL)
	assert.Equal(t, "http://proxy.example.com:8080", *upstreams[0].ProxyURL)
	assert.Equal(t, "betterclaude", upstreams[1].GatewayProxy)
}
