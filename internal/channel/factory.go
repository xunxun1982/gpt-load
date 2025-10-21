package channel

import (
	"encoding/json"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// channelConstructor defines the function signature for creating a new channel proxy.
type channelConstructor func(f *Factory, group *models.Group) (ChannelProxy, error)

var (
	// channelRegistry holds the mapping from channel type string to its constructor.
	channelRegistry = make(map[string]channelConstructor)
)

// Register adds a new channel constructor to the registry.
func Register(channelType string, constructor channelConstructor) {
	if _, exists := channelRegistry[channelType]; exists {
		panic(fmt.Sprintf("channel type '%s' is already registered", channelType))
	}
	channelRegistry[channelType] = constructor
}

// GetChannels returns a slice of all registered channel type names.
func GetChannels() []string {
	supportedTypes := make([]string, 0, len(channelRegistry))
	for t := range channelRegistry {
		supportedTypes = append(supportedTypes, t)
	}
	return supportedTypes
}

// Factory is responsible for creating channel proxies.
type Factory struct {
	settingsManager *config.SystemSettingsManager
	clientManager   *httpclient.HTTPClientManager
	channelCache    map[uint]ChannelProxy
	cacheLock       sync.Mutex
}

// NewFactory creates a new channel factory.
func NewFactory(settingsManager *config.SystemSettingsManager, clientManager *httpclient.HTTPClientManager) *Factory {
	return &Factory{
		settingsManager: settingsManager,
		clientManager:   clientManager,
		channelCache:    make(map[uint]ChannelProxy),
	}
}

// GetChannel returns a channel proxy based on the group's channel type.
func (f *Factory) GetChannel(group *models.Group) (ChannelProxy, error) {
	f.cacheLock.Lock()
	defer f.cacheLock.Unlock()

	if channel, ok := f.channelCache[group.ID]; ok {
		if !channel.IsConfigStale(group) {
			return channel, nil
		}
	}

	logrus.Debugf("Creating new channel for group %d with type '%s'", group.ID, group.ChannelType)

	constructor, ok := channelRegistry[group.ChannelType]
	if !ok {
		return nil, fmt.Errorf("unsupported channel type: %s", group.ChannelType)
	}
	channel, err := constructor(f, group)
	if err != nil {
		return nil, err
	}
	f.channelCache[group.ID] = channel
	return channel, nil
}

// newBaseChannel is a helper function to create and configure a BaseChannel.
func (f *Factory) newBaseChannel(name string, group *models.Group) (*BaseChannel, error) {
	type upstreamDef struct {
		URL      string  `json:"url"`
		Weight   int     `json:"weight"`
		ProxyURL *string `json:"proxy_url,omitempty"`
	}

	var defs []upstreamDef
	if err := json.Unmarshal(group.Upstreams, &defs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal upstreams for %s channel: %w", name, err)
	}

	if len(defs) == 0 {
		return nil, fmt.Errorf("at least one upstream is required for %s channel", name)
	}

	var upstreamInfos []UpstreamInfo
	for _, def := range defs {
		u, err := url.Parse(def.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse upstream url '%s' for %s channel: %w", def.URL, name, err)
		}
		if def.Weight <= 0 {
			continue
		}

		// Determine which proxy URL to use: upstream-specific or group-level
		proxyURL := group.EffectiveConfig.ProxyURL
		if def.ProxyURL != nil && *def.ProxyURL != "" {
			proxyURL = *def.ProxyURL
		}

		// Base configuration for regular requests, derived from the group's effective settings.
		clientConfig := &httpclient.Config{
			ConnectTimeout:        time.Duration(group.EffectiveConfig.ConnectTimeout) * time.Second,
			RequestTimeout:        time.Duration(group.EffectiveConfig.RequestTimeout) * time.Second,
			IdleConnTimeout:       time.Duration(group.EffectiveConfig.IdleConnTimeout) * time.Second,
			MaxIdleConns:          group.EffectiveConfig.MaxIdleConns,
			MaxIdleConnsPerHost:   group.EffectiveConfig.MaxIdleConnsPerHost,
			ResponseHeaderTimeout: time.Duration(group.EffectiveConfig.ResponseHeaderTimeout) * time.Second,
			ProxyURL:              proxyURL,
			DisableCompression:    false,
			WriteBufferSize:       32 * 1024,
			ReadBufferSize:        32 * 1024,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}

		// Create a dedicated configuration for streaming requests.
		streamConfig := *clientConfig
		streamConfig.RequestTimeout = 0
		streamConfig.DisableCompression = true
		streamConfig.WriteBufferSize = 0
		streamConfig.ReadBufferSize = 0
		// Use a larger, independent connection pool for streaming clients to avoid exhaustion.
		streamConfig.MaxIdleConns = max(group.EffectiveConfig.MaxIdleConns*2, 50)
		streamConfig.MaxIdleConnsPerHost = max(group.EffectiveConfig.MaxIdleConnsPerHost*2, 20)

		// Get both clients from the manager using their respective configurations.
		httpClient := f.clientManager.GetClient(clientConfig)
		streamClient := f.clientManager.GetClient(&streamConfig)

		upstreamInfos = append(upstreamInfos, UpstreamInfo{
			URL:          u,
			Weight:       def.Weight,
			ProxyURL:     def.ProxyURL,
			HTTPClient:   httpClient,
			StreamClient: streamClient,
		})
	}

	// Fallback clients for backward compatibility (use first upstream's clients or group-level config)
	var fallbackHTTPClient, fallbackStreamClient *http.Client
	if len(upstreamInfos) > 0 {
		fallbackHTTPClient = upstreamInfos[0].HTTPClient
		fallbackStreamClient = upstreamInfos[0].StreamClient
	} else {
		// Should not happen, but create default clients just in case
		clientConfig := &httpclient.Config{
			ConnectTimeout:        time.Duration(group.EffectiveConfig.ConnectTimeout) * time.Second,
			RequestTimeout:        time.Duration(group.EffectiveConfig.RequestTimeout) * time.Second,
			IdleConnTimeout:       time.Duration(group.EffectiveConfig.IdleConnTimeout) * time.Second,
			MaxIdleConns:          group.EffectiveConfig.MaxIdleConns,
			MaxIdleConnsPerHost:   group.EffectiveConfig.MaxIdleConnsPerHost,
			ResponseHeaderTimeout: time.Duration(group.EffectiveConfig.ResponseHeaderTimeout) * time.Second,
			ProxyURL:              group.EffectiveConfig.ProxyURL,
			DisableCompression:    false,
			WriteBufferSize:       32 * 1024,
			ReadBufferSize:        32 * 1024,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		streamConfig := *clientConfig
		streamConfig.RequestTimeout = 0
		streamConfig.DisableCompression = true
		streamConfig.WriteBufferSize = 0
		streamConfig.ReadBufferSize = 0
		streamConfig.MaxIdleConns = max(group.EffectiveConfig.MaxIdleConns*2, 50)
		streamConfig.MaxIdleConnsPerHost = max(group.EffectiveConfig.MaxIdleConnsPerHost*2, 20)

		fallbackHTTPClient = f.clientManager.GetClient(clientConfig)
		fallbackStreamClient = f.clientManager.GetClient(&streamConfig)
	}

	return &BaseChannel{
		Name:               name,
		Upstreams:          upstreamInfos,
		HTTPClient:         fallbackHTTPClient,
		StreamClient:       fallbackStreamClient,
		TestModel:          group.TestModel,
		ValidationEndpoint: utils.GetValidationEndpoint(group),
		channelType:        group.ChannelType,
		groupUpstreams:     group.Upstreams,
		effectiveConfig:    &group.EffectiveConfig,
	}, nil
}
