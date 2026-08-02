package channel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type gatewayProxySnapshot struct {
	id      string
	base    url.URL
	enabled bool
}

type upstreamSelectionSnapshot struct {
	upstream *UpstreamInfo
	gateway  gatewayProxySnapshot
	identity string
}

func snapshotGatewayProxy(gatewayProxyID string) gatewayProxySnapshot {
	trimmedID := strings.ToLower(strings.TrimSpace(gatewayProxyID))
	snapshot := gatewayProxySnapshot{id: trimmedID}
	if trimmedID == "" {
		return snapshot
	}

	gatewayProxyBaseURLMu.RLock()
	snapshot.base, snapshot.enabled = gatewayProxyBaseURLs[trimmedID]
	gatewayProxyBaseURLMu.RUnlock()
	return snapshot
}

func (b *BaseChannel) snapshotUpstream(upstream *UpstreamInfo) *upstreamSelectionSnapshot {
	gateway := snapshotGatewayProxy(upstream.GatewayProxy)
	return &upstreamSelectionSnapshot{
		upstream: upstream,
		gateway:  gateway,
		identity: b.upstreamIdentity(upstream, gateway),
	}
}

func (b *BaseChannel) upstreamIdentity(upstream *UpstreamInfo, gateway gatewayProxySnapshot) string {
	gatewayBaseURL := ""
	if gateway.enabled {
		gatewayBaseURL = normalizeUpstreamBaseURL(&gateway.base)
	}
	parts := []string{
		strings.ToLower(strings.TrimSpace(b.Name)),
		normalizeUpstreamBaseURL(upstream.URL),
		normalizeProxyURL(upstream.ProxyURL),
		gateway.id,
		gatewayBaseURL,
	}

	var input strings.Builder
	for _, part := range parts {
		input.WriteString(strconv.Itoa(len(part)))
		input.WriteByte(':')
		input.WriteString(part)
	}
	sum := sha256.Sum256([]byte(input.String()))
	return hex.EncodeToString(sum[:])
}

// ResolveUpstreamByIdentity resolves an active upstream from the current configuration.
func (b *BaseChannel) ResolveUpstreamByIdentity(identity string, originalURL *url.URL, groupName string) (*UpstreamSelection, error) {
	snapshot, err := b.resolveUpstreamSnapshot(identity)
	if err != nil {
		return nil, err
	}
	return b.buildUpstreamSelection(snapshot, originalURL, groupName)
}

func (b *BaseChannel) resolveUpstreamSnapshot(identity string) (*upstreamSelectionSnapshot, error) {
	b.upstreamLock.Lock()
	defer b.upstreamLock.Unlock()

	for i := range b.Upstreams {
		upstream := &b.Upstreams[i]
		if upstream.Weight <= 0 {
			continue
		}
		snapshot := b.snapshotUpstream(upstream)
		if snapshot.identity == identity {
			return snapshot, nil
		}
	}
	return nil, fmt.Errorf("upstream identity not found for channel %s", b.Name)
}

func (b *BaseChannel) buildUpstreamSelection(snapshot *upstreamSelectionSnapshot, originalURL *url.URL, groupName string) (*UpstreamSelection, error) {
	upstream := snapshot.upstream
	base := *upstream.URL
	proxyPrefix := "/proxy/" + groupName
	reqPath := strings.TrimPrefix(originalURL.Path, proxyPrefix)
	if !strings.HasPrefix(reqPath, "/") {
		reqPath = "/" + reqPath
	}
	reqPath = b.applyPathRedirects(reqPath)

	finalURL := base
	joinedPath, err := url.JoinPath(base.Path, reqPath)
	if err != nil {
		return nil, fmt.Errorf("failed to join URL paths: %w", err)
	}
	finalURL.Path = joinedPath
	finalURL.RawQuery = originalURL.RawQuery
	gatewayProxy := ""
	if snapshot.gateway.id != "" {
		directURL := finalURL.String()
		routedURL, err := buildGatewayProxyURLWithSnapshot(snapshot.gateway.id, b.Name, finalURL, snapshot.gateway)
		if err != nil {
			return nil, err
		}
		if routedURL.String() != directURL {
			gatewayProxy = snapshot.gateway.id
		}
		finalURL = routedURL
	}

	return &UpstreamSelection{
		Identity:     snapshot.identity,
		URL:          finalURL.String(),
		HTTPClient:   upstream.HTTPClient,
		StreamClient: upstream.StreamClient,
		ProxyURL:     upstream.ProxyURL,
		GatewayProxy: gatewayProxy,
	}, nil
}

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
