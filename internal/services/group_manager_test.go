package services

import (
	"context"
	"encoding/json"
	"testing"

	"gpt-load/internal/config"
	"gpt-load/internal/models"
	"gpt-load/internal/syncer"

	"github.com/sirupsen/logrus"
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

func TestGroupManagerResolveUpstreamProxyReferencesPreservesUnknownFields(t *testing.T) {
	t.Parallel()

	raw := []byte(`[{
		"url":"https://api.example.com",
		"weight":100,
		"proxy_url":"proxy-pool:1",
		"future_field":{"enabled":true},
		"request_id":9007199254740993
	}]`)
	settingsManager := config.NewSystemSettingsManager()
	settingsManager.SetProxyURLResolver(groupManagerProxyResolverStub{})

	resolved := (&GroupManager{settingsManager: settingsManager}).resolveUpstreamProxyReferences(
		context.Background(), raw, make(map[string]string),
	)

	var upstreams []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resolved, &upstreams))
	require.Len(t, upstreams, 1)
	assert.JSONEq(t, `{"enabled":true}`, string(upstreams[0]["future_field"]))
	assert.Equal(t, "9007199254740993", string(upstreams[0]["request_id"]))
	assert.Equal(t, `"http://proxy.example.com:8080"`, string(upstreams[0]["proxy_url"]))
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

func TestGroupManagerResolveUpstreamProxyReferencesTrimsReferenceCacheKey(t *testing.T) {
	t.Parallel()

	raw := []byte(`[{"url":"https://api.example.com","weight":100,"proxy_url":" proxy-pool:1 \t"}]`)
	settingsManager := config.NewSystemSettingsManager()
	settingsManager.SetProxyURLResolver(groupManagerProxyResolverStub{})

	resolved := (&GroupManager{settingsManager: settingsManager}).resolveUpstreamProxyReferences(
		context.Background(), raw, make(map[string]string),
	)

	assert.Contains(t, string(resolved), `"proxy_url":"http://proxy.example.com:8080"`)
}

func TestGroupManagerRefreshCachedUpstreamsRemovesRenamedCacheEntry(t *testing.T) {
	t.Parallel()

	originalUpstreams := []byte(`[{"url":"https://old.example.com","weight":100}]`)
	original := &models.Group{ID: 42, Name: "old-name", Upstreams: originalUpstreams}
	cacheSyncer, err := syncer.NewCacheSyncer(
		func() (groupCache, error) {
			return groupCache{
				ByName: map[string]*models.Group{original.Name: original},
				ByID:   map[uint]*models.Group{original.ID: original},
			}, nil
		},
		nil,
		"test:group-manager-rename",
		logrus.New().WithField("test", t.Name()),
		nil,
	)
	require.NoError(t, err)
	t.Cleanup(cacheSyncer.Stop)

	gm := &GroupManager{syncer: cacheSyncer}
	updatedUpstreams := []byte(`[{"url":"https://new.example.com","weight":100}]`)
	gm.RefreshCachedUpstreams(context.Background(), original.ID, "new-name", updatedUpstreams)

	cache := cacheSyncer.Get()
	_, oldNameExists := cache.ByName["old-name"]
	updatedByName := cache.ByName["new-name"]
	updatedByID := cache.ByID[original.ID]
	assert.False(t, oldNameExists)
	require.NotNil(t, updatedByName)
	assert.Same(t, updatedByName, updatedByID)
	assert.Equal(t, "new-name", updatedByName.Name)
	assert.JSONEq(t, string(updatedUpstreams), string(updatedByName.Upstreams))
	// Readers holding the previous snapshot must not observe the management update.
	assert.Equal(t, "old-name", original.Name)
	assert.JSONEq(t, string(originalUpstreams), string(original.Upstreams))
}
