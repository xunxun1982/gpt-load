package channel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
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
	channelType          string
	groupUpstreams       datatypes.JSON
	effectiveConfig      *types.SystemSettings
	pathRedirectsRaw     datatypes.JSON
	pathRedirectRules    []models.PathRedirectRule // Applied only for OpenAI channel
	modelRedirectRules   datatypes.JSONMap
	modelRedirectStrict  bool
}

// SelectUpstream selects an upstream using weighted random selection algorithm.
// Returns the selected UpstreamInfo which includes URL and dedicated HTTP clients.
// Returns nil if no upstream is available (all weights are zero or no upstreams configured).
func (b *BaseChannel) SelectUpstream() *UpstreamInfo {
	b.upstreamLock.Lock()
	defer b.upstreamLock.Unlock()

	if len(b.Upstreams) == 0 {
		return nil
	}

	// Fast path: single upstream
	if len(b.Upstreams) == 1 && b.Upstreams[0].Weight > 0 {
		return &b.Upstreams[0]
	}

	// Build weights array and check for positive weights
	weights := make([]int, len(b.Upstreams))
	hasPositiveWeight := false
	for i := range b.Upstreams {
		weight := b.Upstreams[i].Weight
		weights[i] = weight
		if weight > 0 {
			hasPositiveWeight = true
		}
	}

	if !hasPositiveWeight {
		return nil // all upstreams disabled (weight 0)
	}

	// Use shared weighted random selection
	idx := utils.WeightedRandomSelect(weights)
	if idx < 0 {
		return nil // no available upstream (all disabled)
	}

	return &b.Upstreams[idx]
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

	// Log selected upstream with proxy information for debugging (sanitized)
	sanitizedProxy := "none"
	if upstream.ProxyURL != nil {
		sanitizedProxy = utils.SanitizeProxyString(*upstream.ProxyURL)
	}
	logrus.WithFields(logrus.Fields{
		"channel":    b.Name,
		"group_name": groupName,
		"upstream":   upstream.URL.String(),
		"has_proxy":  upstream.ProxyURL != nil && *upstream.ProxyURL != "",
		"proxy_url":  sanitizedProxy,
	}).Debug("Selected upstream with client configuration")

	base := *upstream.URL
	proxyPrefix := "/proxy/" + groupName
	reqPath := strings.TrimPrefix(originalURL.Path, proxyPrefix)

	// Ensure reqPath starts with / for proper URL resolution
	if !strings.HasPrefix(reqPath, "/") {
		reqPath = "/" + reqPath
	}

	// Apply path redirect rules (OpenAI only; rules list is empty for other channels)
	reqPath = b.applyPathRedirects(reqPath)

	finalURL := base
	// Use url.JoinPath for safe path joining (Go 1.19+)
	joinedPath, err := url.JoinPath(base.Path, reqPath)
	if err != nil {
		return nil, fmt.Errorf("failed to join URL paths: %w", err)
	}
	finalURL.Path = joinedPath
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
	if b.Name == "openai" && !bytes.Equal(b.pathRedirectsRaw, group.PathRedirects) {
		return true
	}
	// Check for model redirect rules changes
	if !reflect.DeepEqual(b.modelRedirectRules, group.ModelRedirectRules) {
		return true
	}
	if b.modelRedirectStrict != group.ModelRedirectStrict {
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

// SelectValidationUpstream is a helper for ValidateKey that selects an upstream
// with its dedicated HTTP client (including per-upstream proxy configuration).
// This ensures that key validation uses the same proxy/upstream logic as normal requests.
func (b *BaseChannel) SelectValidationUpstream(group *models.Group, validationPath string, rawQuery string) (*UpstreamSelection, error) {
	// Build a synthetic proxy URL to reuse the upstream selection logic.
	// This ensures path redirects and proxy configuration are applied consistently.
	proxyURL := &url.URL{
		Path:     "/proxy/" + group.Name + validationPath,
		RawQuery: rawQuery,
	}

	return b.SelectUpstreamWithClients(proxyURL, group.Name)
}

// applyPathRedirects applies first matching prefix rewrite rule to the request path.
// Rules are expected to be relative to the group (i.e., without /proxy/{group}).
func (b *BaseChannel) applyPathRedirects(reqPath string) string {
	if len(b.pathRedirectRules) == 0 || reqPath == "" {
		return reqPath
	}
	normalize := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			return ""
		}
		if u, err := url.Parse(p); err == nil && u.Scheme != "" {
			p = u.Path // strip scheme/host if a full URL was provided
		}
		if strings.HasPrefix(p, "/proxy/") {
			// Remove '/proxy/{group}/' if present
			rest := strings.TrimPrefix(p, "/proxy/")
			if idx := strings.IndexByte(rest, '/'); idx >= 0 {
				p = rest[idx:]
			} else {
				p = "/"
			}
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return p
	}
	for _, r := range b.pathRedirectRules {
		from := normalize(r.From)
		to := normalize(r.To)
		if from == "" || to == "" {
			continue
		}
		if strings.HasPrefix(reqPath, from) {
			rest := reqPath[len(from):]
			if rest == "" {
				return to
			}
			if strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "?") || strings.HasPrefix(rest, "#") {
				return to + rest
			}
		}
	}
	return reqPath
}

// modelRedirectSelector is a shared selector instance for V2 rules.
// Stateless, safe for concurrent use.
var modelRedirectSelector = models.NewModelRedirectSelector(utils.WeightedRandomSelect)

// ApplyModelRedirect applies model redirection based on the group's redirect rules.
// V2 rules (one-to-many) take priority over V1 rules (one-to-one).
// Returns the modified body bytes, the original model name (empty if no redirect occurred),
// and the selected target index (-1 if no V2 redirect or not applicable).
func (b *BaseChannel) ApplyModelRedirect(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, string, error) {
	modifiedBytes, originalModel, _, err := b.ApplyModelRedirectWithIndex(req, bodyBytes, group)
	return modifiedBytes, originalModel, err
}

// ApplyModelRedirectWithIndex applies model redirection and returns the selected target index.
// V2 rules (one-to-many) take priority over V1 rules (one-to-one).
// Returns the modified body bytes, the original model name, the selected target index, and error.
// The target index is -1 if no V2 redirect occurred or not applicable.
func (b *BaseChannel) ApplyModelRedirectWithIndex(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, string, int, error) {
	if len(bodyBytes) == 0 {
		return bodyBytes, "", -1, nil
	}

	// Check if any redirect rules exist
	if len(group.ModelRedirectMap) == 0 && len(group.ModelRedirectMapV2) == 0 {
		return bodyBytes, "", -1, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		return bodyBytes, "", -1, nil
	}

	modelValue, exists := requestData["model"]
	if !exists {
		return bodyBytes, "", -1, nil
	}

	model, ok := modelValue.(string)
	if !ok {
		return bodyBytes, "", -1, nil
	}

	// Resolve target model (V2 first, then V1) with index tracking
	targetModel, ruleVersion, targetCount, selectedIdx, err := models.ResolveTargetModelWithIndex(
		model, group.ModelRedirectMap, group.ModelRedirectMapV2, modelRedirectSelector,
	)
	if err != nil {
		return nil, "", -1, fmt.Errorf("failed to select target model: %w", err)
	}

	if targetModel != "" {
		requestData["model"] = targetModel

		logrus.WithFields(logrus.Fields{
			"group":          group.Name,
			"original_model": model,
			"target_model":   targetModel,
			"target_count":   targetCount,
			"target_index":   selectedIdx,
			"rule_version":   ruleVersion,
		}).Debug("Model redirected")

		modifiedBytes, err := json.Marshal(requestData)
		if err != nil {
			return bodyBytes, "", -1, err
		}
		return modifiedBytes, model, selectedIdx, nil
	}

	// Strict mode check
	if group.ModelRedirectStrict {
		return nil, "", -1, fmt.Errorf("model '%s' is not configured in redirect rules", model)
	}

	return bodyBytes, "", -1, nil
}

// TransformModelList transforms the model list response based on redirect rules.
// Supports both V1 (one-to-one) and V2 (one-to-many) rules.
func (b *BaseChannel) TransformModelList(req *http.Request, bodyBytes []byte, group *models.Group) (map[string]any, error) {
	var response map[string]any
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		logrus.WithError(err).Debug("Failed to parse model list response, returning empty")
		return nil, err
	}

	dataInterface, exists := response["data"]
	if !exists {
		return response, nil
	}

	upstreamModels, ok := dataInterface.([]any)
	if !ok {
		return response, nil
	}

	// Build configured source models list from both V1 and V2 rules
	configuredModels := buildConfiguredModelsFromRules(group.ModelRedirectMap, group.ModelRedirectMapV2)

	// Strict mode: return only configured models (whitelist)
	if group.ModelRedirectStrict {
		response["data"] = configuredModels

		logrus.WithFields(logrus.Fields{
			"group":       group.Name,
			"model_count": len(configuredModels),
			"strict_mode": true,
		}).Debug("Model list returned (strict mode - configured models only)")

		return response, nil
	}

	// Non-strict mode: merge upstream + configured models (upstream priority)
	merged := mergeModelLists(upstreamModels, configuredModels)
	response["data"] = merged

	logrus.WithFields(logrus.Fields{
		"group":            group.Name,
		"upstream_count":   len(upstreamModels),
		"configured_count": len(configuredModels),
		"merged_count":     len(merged),
		"strict_mode":      false,
	}).Debug("Model list merged (non-strict mode)")

	return response, nil
}

// buildConfiguredModelsFromRules builds a list of models from redirect rules.
// If V2 rules exist, only V2 source models are used (V1 is ignored for backward compatibility).
func buildConfiguredModelsFromRules(v1Map map[string]string, v2Map map[string]*models.ModelRedirectRuleV2) []any {
	sourceModels := models.CollectSourceModels(v1Map, v2Map)
	if len(sourceModels) == 0 {
		return []any{}
	}

	result := make([]any, 0, len(sourceModels))
	for _, sourceModel := range sourceModels {
		result = append(result, map[string]any{
			"id":       sourceModel,
			"object":   "model",
			"created":  0,
			"owned_by": "system",
		})
	}
	return result
}

// mergeModelLists merges upstream and configured model lists
func mergeModelLists(upstream []any, configured []any) []any {
	// Create set of upstream model IDs
	upstreamIDs := make(map[string]bool)
	for _, item := range upstream {
		if modelObj, ok := item.(map[string]any); ok {
			if modelID, ok := modelObj["id"].(string); ok {
				upstreamIDs[modelID] = true
			}
		}
	}

	// Start with all upstream models
	result := make([]any, len(upstream))
	copy(result, upstream)

	// Add configured models that don't exist in upstream
	for _, item := range configured {
		if modelObj, ok := item.(map[string]any); ok {
			if modelID, ok := modelObj["id"].(string); ok {
				if !upstreamIDs[modelID] {
					result = append(result, item)
				}
			}
		}
	}

	return result
}

// IsStreamRequest checks if the request is for a streaming response.
// It checks the Accept header, query parameter, and JSON body.
func (b *BaseChannel) IsStreamRequest(c *gin.Context, bodyBytes []byte) bool {
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}

	if c.Query("stream") == "true" {
		return true
	}

	type streamPayload struct {
		Stream bool `json:"stream"`
	}
	var p streamPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Stream
	}

	return false
}

// ExtractModel extracts the model name from the request body (OpenAI format).
func (b *BaseChannel) ExtractModel(c *gin.Context, bodyBytes []byte) string {
	type modelPayload struct {
		Model string `json:"model"`
	}
	var p modelPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Model
	}
	return ""
}
