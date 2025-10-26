package channel

import (
	"bytes"
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"gorm.io/datatypes"
)

// UpstreamInfo holds the information for a single upstream server, including its weight.
type UpstreamInfo struct {
	URL           *url.URL
	Weight        int
	CurrentWeight int
	ProxyURL      *string      // Optional proxy URL for this upstream
	HTTPClient    *http.Client // Dedicated HTTP client for this upstream
	StreamClient  *http.Client // Dedicated stream client for this upstream
}

// BaseChannel provides common functionality for channel proxies.
type BaseChannel struct {
	Name               string
	Upstreams          []UpstreamInfo
	HTTPClient         *http.Client
	StreamClient       *http.Client
	TestModel          string
	ValidationEndpoint string
	upstreamLock       sync.Mutex

	// Cached fields from the group for stale check
	channelType     string
	groupUpstreams  datatypes.JSON
	effectiveConfig *types.SystemSettings
}

// SelectUpstream selects an upstream using a smooth weighted round-robin algorithm.
// Returns the selected UpstreamInfo which includes URL and dedicated HTTP clients.
// Returns nil if no upstream is available (all weights are zero or no upstreams configured).
func (b *BaseChannel) SelectUpstream() *UpstreamInfo {
	b.upstreamLock.Lock()
	defer b.upstreamLock.Unlock()

	if len(b.Upstreams) == 0 {
		return nil
	}
	if len(b.Upstreams) == 1 && b.Upstreams[0].Weight > 0 {
		return &b.Upstreams[0]
	}

	totalWeight := 0
	var best *UpstreamInfo

	for i := range b.Upstreams {
		up := &b.Upstreams[i]
		if up.Weight <= 0 {
			continue // skip disabled upstreams
		}
		totalWeight += up.Weight
		up.CurrentWeight += up.Weight

		if best == nil || up.CurrentWeight > best.CurrentWeight {
			best = up
		}
	}

	if best == nil {
		return nil // no available upstream (all disabled)
	}

	best.CurrentWeight -= totalWeight
	return best
}

// getUpstreamURL selects an upstream URL using a smooth weighted round-robin algorithm.
// Deprecated: Use SelectUpstream() instead to get the full UpstreamInfo with dedicated clients.
func (b *BaseChannel) getUpstreamURL() *url.URL {
	upstream := b.SelectUpstream()
	if upstream == nil {
		return nil
	}
	return upstream.URL
}

// SelectUpstreamWithClients selects an upstream and returns its URL with dedicated HTTP clients.
func (b *BaseChannel) SelectUpstreamWithClients(originalURL *url.URL, groupName string) (*UpstreamSelection, error) {
	upstream := b.SelectUpstream()
	if upstream == nil {
		return nil, fmt.Errorf("no upstream available for channel %s (all disabled or none configured)", b.Name)
	}

	base := *upstream.URL
	proxyPrefix := "/proxy/" + groupName
	reqPath := strings.TrimPrefix(originalURL.Path, proxyPrefix)

	// Ensure reqPath starts with / for proper URL resolution
	if !strings.HasPrefix(reqPath, "/") {
		reqPath = "/" + reqPath
	}

	// Build final URL using path.Join for RFC 3986 compliance
	finalURL := base
	// Use url.JoinPath for safe path joining (Go 1.19+)
	finalURL.Path = strings.TrimRight(base.Path, "/") + reqPath
	finalURL.RawQuery = originalURL.RawQuery

	return &UpstreamSelection{
		URL:          finalURL.String(),
		HTTPClient:   upstream.HTTPClient,
		StreamClient: upstream.StreamClient,
		ProxyURL:     upstream.ProxyURL,
	}, nil
}

// BuildUpstreamURL constructs the target URL for the upstream service.
// Deprecated: Use SelectUpstreamWithClients instead.
func (b *BaseChannel) BuildUpstreamURL(originalURL *url.URL, groupName string) (string, error) {
	base := b.getUpstreamURL()
	if base == nil {
		return "", fmt.Errorf("no upstream URL configured for channel %s", b.Name)
	}

	finalURL := *base
	proxyPrefix := "/proxy/" + groupName
	requestPath := originalURL.Path
	requestPath = strings.TrimPrefix(requestPath, proxyPrefix)

	finalURL.Path = strings.TrimRight(finalURL.Path, "/") + requestPath

	finalURL.RawQuery = originalURL.RawQuery

	return finalURL.String(), nil
}

// IsConfigStale checks if the channel's configuration is stale compared to the provided group.
func (b *BaseChannel) IsConfigStale(group *models.Group) bool {
	if b.channelType != group.ChannelType {
		return true
	}
	if b.TestModel != group.TestModel {
		return true
	}
	if b.ValidationEndpoint != utils.GetValidationEndpoint(group) {
		return true
	}
	if !bytes.Equal(b.groupUpstreams, group.Upstreams) {
		return true
	}
	if !reflect.DeepEqual(b.effectiveConfig, &group.EffectiveConfig) {
		return true
	}
	return false
}

// GetHTTPClient returns the client for standard requests.
func (b *BaseChannel) GetHTTPClient() *http.Client {
	return b.HTTPClient
}

// GetStreamClient returns the client for streaming requests.
func (b *BaseChannel) GetStreamClient() *http.Client {
	return b.StreamClient
}
