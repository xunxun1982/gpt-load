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

type groupManagerBatchProxyResolverStub struct {
	singleCalls int
	batchCalls  int
}

func (s *groupManagerBatchProxyResolverStub) ResolveProxyURL(_ context.Context, raw string) (string, error) {
	s.singleCalls++
	return "single://" + raw, nil
}

func (s *groupManagerBatchProxyResolverStub) ResolveProxyURLs(_ context.Context, refs []string) (map[string]string, error) {
	s.batchCalls++
	resolved := make(map[string]string, len(refs))
	for _, ref := range refs {
		resolved[ref] = "http://" + ref + ".example.com:8080"
	}
	return resolved, nil
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

func TestGroupManagerResolveUpstreamProxyReferencesBatchesDistinctReferences(t *testing.T) {
	t.Parallel()

	raw := []byte(`[
		{"url":"https://api-a.example.com","weight":100,"proxy_url":"proxy-pool:1"},
		{"url":"https://api-b.example.com","weight":100,"proxy_url":"proxy-pool:2"}
	]`)
	resolver := &groupManagerBatchProxyResolverStub{}
	settingsManager := config.NewSystemSettingsManager()
	settingsManager.SetProxyURLResolver(resolver)

	resolved := (&GroupManager{settingsManager: settingsManager}).resolveUpstreamProxyReferences(
		context.Background(),
		raw,
		make(map[string]string),
	)

	assert.Equal(t, 1, resolver.batchCalls)
	assert.Zero(t, resolver.singleCalls)
	assert.Contains(t, string(resolved), `"proxy_url":"http://proxy-pool:1.example.com:8080"`)
	assert.Contains(t, string(resolved), `"proxy_url":"http://proxy-pool:2.example.com:8080"`)
}
