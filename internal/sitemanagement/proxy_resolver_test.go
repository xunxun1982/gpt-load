package sitemanagement

import (
	"context"
	"testing"

	"gpt-load/internal/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type siteProxyResolverStub struct {
	resolved string
	calls    []string
	contexts []context.Context
}

type proxyResolverContextKey struct{}

func (s *siteProxyResolverStub) ResolveProxyURL(ctx context.Context, raw string) (string, error) {
	s.calls = append(s.calls, raw)
	s.contexts = append(s.contexts, ctx)
	return s.resolved, nil
}

func TestAutoCheckinServiceResolvesProxyPoolReferenceForStandardClient(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewAutoCheckinService(db, nil, encSvc)
	resolver := &siteProxyResolverStub{resolved: "http://resolved-proxy.example.com:8080"}
	service.SetProxyURLResolver(resolver)

	ref := utils.BuildProxyPoolItemRef(12)
	client := service.getHTTPClient(true, ref)

	require.NotNil(t, client)
	assert.NotSame(t, service.client, client)
	assert.Equal(t, []string{ref}, resolver.calls)
	_, ok := service.proxyClients.Load(resolver.resolved)
	assert.True(t, ok)
	_, ok = service.proxyClients.Load(ref)
	assert.False(t, ok)
}

func TestAutoCheckinServiceResolvesProxyPoolReferenceForStealthClient(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewAutoCheckinService(db, nil, encSvc)
	resolver := &siteProxyResolverStub{resolved: "http://resolved-stealth-proxy.example.com:8080"}
	service.SetProxyURLResolver(resolver)

	ref := utils.BuildProxyPoolItemRef(13)
	client := service.getCheckinHTTPClient(ManagedSite{
		UseProxy:     true,
		ProxyURL:     ref,
		BypassMethod: BypassMethodStealth,
	})

	require.NotNil(t, client)
	assert.NotSame(t, service.client, client)
	assert.Equal(t, []string{ref}, resolver.calls)
	_, ok := service.stealthClientMgr.clients.Load(resolver.resolved)
	assert.True(t, ok)
	_, ok = service.stealthClientMgr.clients.Load(ref)
	assert.False(t, ok)
}

func TestBalanceServiceResolvesProxyPoolReferenceForStandardClient(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewBalanceService(db, encSvc)
	resolver := &siteProxyResolverStub{resolved: "http://resolved-balance-proxy.example.com:8080"}
	service.SetProxyURLResolver(resolver)

	ref := utils.BuildProxyPoolItemRef(14)
	ctx := context.WithValue(context.Background(), proxyResolverContextKey{}, "request-context")
	client := service.getHTTPClient(ctx, &ManagedSite{
		UseProxy: true,
		ProxyURL: ref,
	})

	require.NotNil(t, client)
	assert.NotSame(t, service.client, client)
	assert.Equal(t, []string{ref}, resolver.calls)
	require.Len(t, resolver.contexts, 1)
	assert.Equal(t, ctx, resolver.contexts[0])
	_, ok := service.proxyClients.Load(resolver.resolved)
	assert.True(t, ok)
	_, ok = service.proxyClients.Load(ref)
	assert.False(t, ok)
}
