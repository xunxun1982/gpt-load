package channel

import (
	"encoding/json"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// inflightCall coordinates concurrent constructions for the same group.
// It ensures that only one goroutine performs the work while others wait.
type inflightCall struct {
	wg  sync.WaitGroup
	res ChannelProxy
	err error
}

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

	inFlight     map[uint]*inflightCall
	inFlightLock sync.Mutex
}

// NewFactory creates a new channel factory.
func NewFactory(settingsManager *config.SystemSettingsManager, clientManager *httpclient.HTTPClientManager) *Factory {
	return &Factory{
		settingsManager: settingsManager,
		clientManager:   clientManager,
		channelCache:    make(map[uint]ChannelProxy),
		inFlight:        make(map[uint]*inflightCall),
	}
}

// GetChannel returns a channel proxy based on the group's channel type.
// It uses caching to avoid recreating channels unnecessarily.
// The cache is automatically invalidated when configuration changes are detected.
func (f *Factory) GetChannel(group *models.Group) (ChannelProxy, error) {
	// Fast path: quick cache check
	f.cacheLock.Lock()
	existing, ok := f.channelCache[group.ID]
	f.cacheLock.Unlock()
	if ok && !existing.IsConfigStale(group) {
		logrus.Debugf("Using cached channel for group %d (%s) with type '%s'", group.ID, group.Name, group.ChannelType)
		return existing, nil
	}

	// Singleflight: ensure only one constructor runs per group.ID
	f.inFlightLock.Lock()
	if call, inProgress := f.inFlight[group.ID]; inProgress {
		f.inFlightLock.Unlock()
		// Wait for the ongoing construction
		call.wg.Wait()
		return call.res, call.err
	}
	call := &inflightCall{}
	call.wg.Add(1)
	f.inFlight[group.ID] = call
	f.inFlightLock.Unlock()

	// Perform construction outside locks
	constructor, found := channelRegistry[group.ChannelType]
	if !found {
		call.err = fmt.Errorf("unsupported channel type: %s", group.ChannelType)
		call.wg.Done()
		f.inFlightLock.Lock()
		delete(f.inFlight, group.ID)
		f.inFlightLock.Unlock()
		return nil, call.err
	}

	newCh, err := constructor(f, group)
	if err != nil {
		call.err = err
		call.wg.Done()
		f.inFlightLock.Lock()
		delete(f.inFlight, group.ID)
		f.inFlightLock.Unlock()
		return nil, err
	}

	// Install into cache with double-check and emit a single log line
	f.cacheLock.Lock()
	if current, ok := f.channelCache[group.ID]; ok && !current.IsConfigStale(group) {
		// Another goroutine installed a fresh channel while we were constructing
		call.res = current
		f.cacheLock.Unlock()
	} else {
		if ok {
			logrus.Debugf("Replaced stale channel for group %d (%s) with type '%s'", group.ID, group.Name, group.ChannelType)
		} else {
			logrus.Debugf("Created new channel for group %d (%s) with type '%s'", group.ID, group.Name, group.ChannelType)
		}
		f.channelCache[group.ID] = newCh
		call.res = newCh
		f.cacheLock.Unlock()
	}

	call.wg.Done()
	f.inFlightLock.Lock()
	delete(f.inFlight, group.ID)
	f.inFlightLock.Unlock()
	return call.res, nil
}

// InvalidateCache removes a channel from the cache, forcing it to be recreated on next access.
// This is useful when configuration changes are made that should take effect immediately.
func (f *Factory) InvalidateCache(groupID uint) {
	f.cacheLock.Lock()
	defer f.cacheLock.Unlock()
	delete(f.channelCache, groupID)
	logrus.Debugf("Invalidated channel cache for group %d", groupID)
}

// InvalidateAllCaches clears the entire channel cache.
// This forces all channels to be recreated on next access.
func (f *Factory) InvalidateAllCaches() {
	f.cacheLock.Lock()
	defer f.cacheLock.Unlock()
	count := len(f.channelCache)
	f.channelCache = make(map[uint]ChannelProxy)
	logrus.Infof("Invalidated all channel caches (%d channels cleared)", count)
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

		// Skip zero-weight upstreams entirely - no need to create clients or configure proxy
		if def.Weight <= 0 {
			logrus.WithFields(logrus.Fields{
				"group_id":   group.ID,
				"group_name": group.Name,
				"upstream":   def.URL,
				"weight":     def.Weight,
			}).Debug("Skipping zero-weight upstream (disabled)")
			continue
		}

		// Determine effective proxy URL (per-upstream overrides group-level)
		// Trim whitespace to handle common configuration issues
		proxyURL := strings.TrimSpace(group.EffectiveConfig.ProxyURL)
		if def.ProxyURL != nil && strings.TrimSpace(*def.ProxyURL) != "" {
			proxyURL = strings.TrimSpace(*def.ProxyURL)
			logrus.WithFields(logrus.Fields{
				"group_id":   group.ID,
				"group_name": group.Name,
				"upstream":   def.URL,
				"proxy":      utils.SanitizeProxyString(proxyURL),
				"weight":     def.Weight,
			}).Debug("Using per-upstream proxy (overrides group-level)")
		} else if proxyURL != "" {
			logrus.WithFields(logrus.Fields{
				"group_id":   group.ID,
				"group_name": group.Name,
				"upstream":   def.URL,
				"proxy":      utils.SanitizeProxyString(proxyURL),
				"weight":     def.Weight,
			}).Debug("Using group-level proxy")
		} else {
			logrus.WithFields(logrus.Fields{
				"group_id":   group.ID,
				"group_name": group.Name,
				"upstream":   def.URL,
				"weight":     def.Weight,
			}).Debug("No proxy configured for this upstream")
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

		// Prepare a stable pointer to the effective proxy value for logging/observability
		var proxyPtr *string
		if proxyURL != "" {
			p := proxyURL // per-iteration copy to avoid pointer aliasing across loop iterations
			proxyPtr = &p
		}
		upstreamInfos = append(upstreamInfos, UpstreamInfo{
			URL:          u,
			Weight:       def.Weight,
			ProxyURL:     proxyPtr,
			HTTPClient:   httpClient,
			StreamClient: streamClient,
		})
	}

	// Verify at least one upstream was added (all zero-weight upstreams are already filtered out)
	if len(upstreamInfos) == 0 {
		return nil, fmt.Errorf("no active upstreams (all weights <= 0) for %s channel", name)
	}

	// Fallback clients: use the first upstream (all upstreams in the list have positive weight)
	fallbackHTTPClient := upstreamInfos[0].HTTPClient
	fallbackStreamClient := upstreamInfos[0].StreamClient

	bc := &BaseChannel{
		Name:                name,
		Upstreams:           upstreamInfos,
		HTTPClient:          fallbackHTTPClient,
		StreamClient:        fallbackStreamClient,
		TestModel:           group.TestModel,
		ValidationEndpoint:  utils.GetValidationEndpoint(group),
		channelType:         group.ChannelType,
		groupUpstreams:      group.Upstreams,
		effectiveConfig:     &group.EffectiveConfig,
		modelRedirectRules:  group.ModelRedirectRules,
		modelRedirectStrict: group.ModelRedirectStrict,
	}
	// Only apply path redirects for openai channel type
	if name == "openai" {
		bc.pathRedirectsRaw = group.PathRedirects
		bc.pathRedirectRules = group.PathRedirectRuleList
	}
	return bc, nil
}
