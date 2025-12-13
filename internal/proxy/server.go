// Package proxy provides high-performance OpenAI multi-key proxy server
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/services"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const maxUpstreamErrorBodySize = 64 * 1024 // 64KB

// Context keys used for function call middleware.
const (
	ctxKeyTriggerSignal       = "fc_trigger_signal"
	ctxKeyFunctionCallEnabled = "fc_enabled"
)

// ProxyServer represents the proxy server
type ProxyServer struct {
	keyProvider       *keypool.KeyProvider
	groupManager      *services.GroupManager
	subGroupManager   *services.SubGroupManager
	settingsManager   *config.SystemSettingsManager
	channelFactory    *channel.Factory
	requestLogService *services.RequestLogService
	encryptionSvc     encryption.Service
}

// retryContext holds the retry state for a single request
// This context is created per request and lives only for the request's lifetime
type retryContext struct {
	excludedSubGroups   map[uint]bool // Sub-group IDs that have failed in the current request (only for current aggregate group)
	attemptCount        int           // Current attempt count (aggregate-level sub-group switches)
	originalBodyBytes   []byte        // Original request body (before any sub-group mapping)
	originalPath        string        // Original request path (for CC support restoration)
	subGroupKeyRetryMap map[uint]int  // Tracks key retry count for each sub-group (sub-group ID -> retry count)
}

// safeProxyURL returns the proxy URL value with credentials redacted for safe logging.
// Returns "none" when the pointer is nil or the underlying string is empty.
// If the URL contains user credentials (user:pass@host), they are redacted to prevent
// password leakage in logs.
func safeProxyURL(proxyURL *string) string {
	if proxyURL == nil || *proxyURL == "" {
		return "none"
	}

	// Parse URL to redact credentials
	parsedURL, err := url.Parse(*proxyURL)
	if err != nil {
		// If parsing fails, return a redacted version to be safe
		return "[invalid-url]"
	}

	// If URL has user credentials, redact them
	if parsedURL.User != nil {
		username := parsedURL.User.Username()
		if username != "" {
			// Redact password but keep username (first 3 chars) for debugging
			if len(username) > 3 {
				username = username[:3] + "***"
			} else {
				username = "***"
			}
			parsedURL.User = url.User(username)
		}
	}

	return parsedURL.String()
}

// restoreOriginalPath restores the original request path for retry attempts.
// This is used by aggregate retry logic to ensure each sub-group can apply its
// own CC support and path rewriting without inheriting state from previous
// attempts.
func restoreOriginalPath(c *gin.Context, retryCtx *retryContext) {
	if retryCtx == nil {
		return
	}
	if retryCtx.originalPath != "" && c.Request.URL.Path != retryCtx.originalPath {
		c.Request.URL.Path = retryCtx.originalPath
	}
}

// parseRetryConfigInt extracts and validates a retry-related integer config value.
// Returns a value clamped to the range [0, 100].
func parseRetryConfigInt(config map[string]any, key string) int {
	if config == nil {
		return 0
	}

	val, ok := config[key]
	if !ok {
		return 0
	}

	retries := 0
	// Try different type assertions
	switch v := val.(type) {
	case float64:
		retries = int(v)
	case int:
		retries = v
	case int64:
		retries = int(v)
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			retries = int(parsed)
		} else {
			logrus.WithFields(logrus.Fields{
				"config_key": key,
				"value":      v,
				"error":      err,
			}).Warn("Failed to parse json.Number for retry config value")
		}
	case string:
		if parsed, err := strconv.Atoi(v); err == nil {
			retries = parsed
		} else {
			logrus.WithFields(logrus.Fields{
				"config_key": key,
				"value":      v,
				"error":      err,
			}).Warn("Failed to parse string for retry config value")
		}
	default:
		logrus.WithFields(logrus.Fields{
			"config_key": key,
			"value":      val,
			"type":       fmt.Sprintf("%T", val),
		}).Warn("Unexpected type for retry config value")
	}

	// Clamp to reasonable range: 0-100
	// Note: Previously limited to 5, but users may need higher retry counts
	// for high-availability scenarios with many upstreams or sub-groups.
	if retries < 0 {
		return 0
	}
	if retries > 100 {
		return 100
	}

	return retries
}

// parseMaxRetries extracts and validates max_retries from group config
// Returns a value clamped to the range [0, 100]
func parseMaxRetries(config map[string]any) int {
	return parseRetryConfigInt(config, "max_retries")
}

// parseSubMaxRetries extracts and validates sub_max_retries from group config
// Returns a value clamped to the range [0, 100]
func parseSubMaxRetries(config map[string]any) int {
	return parseRetryConfigInt(config, "sub_max_retries")
}

// isForceFunctionCallEnabled checks whether the force_function_call flag is enabled
// for the given group. This flag is currently only meaningful for OpenAI channel groups
// and is stored in the group-level JSON config rather than global system settings.
//
// NOTE: ForceFunctionCall is a group-only override key and is not part of the
// typed SystemSettings / EffectiveConfig. We intentionally read it from the raw
// group.Config map to avoid introducing a separate system-wide knob and to stay
// compatible with imported configs that may include this key only at group level.
func isForceFunctionCallEnabled(group *models.Group) bool {
	if group == nil || group.Config == nil {
		return false
	}

	// Only enable function call middleware for OpenAI channel groups.
	if group.ChannelType != "openai" {
		return false
	}

	raw, ok := group.Config["force_function_call"]
	if !ok || raw == nil {
		// Backward compatibility: honor legacy key if present so that existing
		// groups using force_function_calling continue to behave correctly
		// until their configs are saved with the new key.
		if legacy, legacyOk := group.Config["force_function_calling"]; legacyOk && legacy != nil {
			raw = legacy
		} else {
			return false
		}
	}

	switch v := raw.(type) {
	case bool:
		return v
	case *bool:
		if v != nil {
			return *v
		}
	case string:
		// Best-effort string parsing to be tolerant to imported configs.
		lower := strings.ToLower(strings.TrimSpace(v))
		return lower == "true" || lower == "1" || lower == "yes" || lower == "on"
	case float64:
		// Accept numeric JSON values (0/1) from legacy or imported configs.
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	}

	return false
}

// isChatCompletionsEndpoint checks whether the current request targets the
// OpenAI-style chat completions endpoint.
func isChatCompletionsEndpoint(path, method string) bool {
	if method != http.MethodPost {
		return false
	}
	// Router formats path as /proxy/{group}/v1/chat/completions. We also accept
	// bare /v1/chat/completions for direct upstream-style integration.
	if path == "/v1/chat/completions" {
		return true
	}
	return strings.HasSuffix(path, "/v1/chat/completions")
}

// isFunctionCallEnabled returns true if the function-call middleware
// was successfully applied for the current request. It reads a boolean flag
// from Gin context and treats missing or non-bool values as false.
func isFunctionCallEnabled(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyFunctionCallEnabled); ok {
		if enabled, ok := v.(bool); ok && enabled {
			return true
		}
	}
	return false
}

// NewProxyServer creates a new proxy server
func NewProxyServer(
	keyProvider *keypool.KeyProvider,
	groupManager *services.GroupManager,
	subGroupManager *services.SubGroupManager,
	settingsManager *config.SystemSettingsManager,
	channelFactory *channel.Factory,
	requestLogService *services.RequestLogService,
	encryptionSvc encryption.Service,
) (*ProxyServer, error) {
	return &ProxyServer{
		keyProvider:       keyProvider,
		groupManager:      groupManager,
		subGroupManager:   subGroupManager,
		settingsManager:   settingsManager,
		channelFactory:    channelFactory,
		requestLogService: requestLogService,
		encryptionSvc:     encryptionSvc,
	}, nil
}

// HandleProxy is the main entry point for proxy requests, refactored based on the stable .bak logic.
func (ps *ProxyServer) HandleProxy(c *gin.Context) {
	startTime := time.Now()
	groupName := c.Param("group_name")

	originalGroup, err := ps.groupManager.GetGroupByName(groupName)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	// Check if group is enabled
	if !originalGroup.Enabled {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("Group '%s' is disabled", groupName)))
		return
	}

	// For aggregate groups, initialize retry context
	var retryCtx *retryContext
	if originalGroup.GroupType == "aggregate" {
		// Pre-allocate map with capacity equal to number of sub-groups for performance
		retryCtx = &retryContext{
			excludedSubGroups:   make(map[uint]bool, len(originalGroup.SubGroups)),
			attemptCount:        0,
			originalPath:        c.Request.URL.Path, // Save original path for retry restoration
			subGroupKeyRetryMap: make(map[uint]int, len(originalGroup.SubGroups)),
		}
	}

	// Select sub-group if this is an aggregate group
	subGroupName, err := ps.subGroupManager.SelectSubGroup(originalGroup)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"error":           err,
		}).Error("Failed to select sub-group from aggregate")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, "No available sub-groups"))
		return
	}

	group := originalGroup
	if subGroupName != "" {
		group, err = ps.groupManager.GetGroupByName(subGroupName)
		if err != nil {
			response.Error(c, app_errors.ParseDBError(err))
			return
		}
	}

	channelHandler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to get channel for group '%s': %v", groupName, err)))
		return
	}

	// Read request body using buffer pool to reduce GC overhead
	buf := utils.GetBuffer()
	defer utils.PutBuffer(buf)

	_, err = buf.ReadFrom(c.Request.Body)
	if err != nil {
		logrus.Errorf("Failed to read request body: %v", err)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "Failed to read request body"))
		return
	}
	c.Request.Body.Close()

	// Use the buffer bytes directly to avoid allocation.
	// SAFETY: This is safe because:
	// 1. PutBuffer() calls buf.Reset() which clears the buffer contents
	// 2. executeRequestWithRetry() is synchronous and doesn't spawn goroutines that retain bodyBytes
	// 3. The buffer is returned to pool only after HandleProxy returns (via defer)
	// 4. No downstream handlers store the bodyBytes slice beyond the request scope
	bodyBytes := buf.Bytes()

	// For GET requests (like /v1/models), skip body processing
	var finalBodyBytes []byte
	var isStream bool

	// Handle CC support path rewriting for all requests (including GET)
	// This must happen before body processing to ensure correct path for upstream
	wasClaudePath := isClaudePath(c.Request.URL.Path, group.Name)
	if isCCSupportEnabled(group) && wasClaudePath {
		originalPath := c.Request.URL.Path
		originalQuery := c.Request.URL.RawQuery
		c.Request.URL.Path = rewriteClaudePathToOpenAIGeneric(c.Request.URL.Path)
		// Sanitize query parameters for CC support (e.g., remove beta=true)
		// These are Claude-specific and should not be passed to OpenAI-style upstreams
		sanitizeCCQueryParams(c.Request.URL)
		c.Set("cc_was_claude_path", true)
		logrus.WithFields(logrus.Fields{
			"group":           group.Name,
			"original_path":   originalPath,
			"new_path":        c.Request.URL.Path,
			"original_query":  originalQuery,
			"sanitized_query": c.Request.URL.RawQuery,
		}).Debug("CC support: rewritten Claude path to OpenAI path and sanitized query params")
	}

	if c.Request.Method == "GET" || len(bodyBytes) == 0 {
		finalBodyBytes = bodyBytes
		isStream = false
	} else {
		// For aggregate groups, skip model mapping and param overrides at this level
		// They will be applied per sub-group in executeRequestWithAggregateRetry
		if originalGroup.GroupType == "aggregate" && retryCtx != nil {
			finalBodyBytes = bodyBytes
			retryCtx.originalBodyBytes = bodyBytes // Save original body for retries
			isStream = channelHandler.IsStreamRequest(c, finalBodyBytes)
		} else {
			// Apply model mapping first (before param overrides to allow overriding the mapped model if needed)
			bodyBytesAfterMapping, originalModel := ps.applyModelMapping(bodyBytes, group)

			// Store original model only if mapping changed the payload
			if originalModel != "" && !bytes.Equal(bodyBytesAfterMapping, bodyBytes) {
				c.Set("original_model", originalModel)
			}

			finalBodyBytes, err = ps.applyParamOverrides(bodyBytesAfterMapping, group)
			if err != nil {
				response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to apply parameter overrides: %v", err)))
				return
			}

			// Handle Claude count_tokens endpoint (CC only).
			if ps.handleTokenCount(c, group, finalBodyBytes) {
				return
			}

			// Apply CC support: convert Claude requests to OpenAI format
			// Note: Path has already been rewritten from /claude/v1/messages to /v1/messages
			// We check for /v1/messages (after rewrite) and CC support enabled
			if isCCSupportEnabled(group) && strings.HasSuffix(c.Request.URL.Path, "/v1/messages") {
				convertedBody, converted, ccErr := ps.applyCCRequestConversionDirect(c, group, finalBodyBytes)
				if ccErr != nil {
					logrus.WithError(ccErr).WithFields(logrus.Fields{
						"group": group.Name,
						"path":  c.Request.URL.Path,
					}).Error("Failed to convert Claude request to OpenAI format")
					response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("CC conversion failed: %v", ccErr)))
					return
				} else if converted {
					finalBodyBytes = convertedBody
					// Rewrite path from /v1/messages to /v1/chat/completions
					c.Request.URL.Path = strings.Replace(c.Request.URL.Path, "/v1/messages", "/v1/chat/completions", 1)
					logrus.WithFields(logrus.Fields{
						"group":        group.Name,
						"channel_type": group.ChannelType,
						"new_path":     c.Request.URL.Path,
					}).Debug("CC support: converted Claude request to OpenAI format")
				}
			}

			// Apply function call request rewrite for eligible OpenAI groups.
			if isForceFunctionCallEnabled(group) && isChatCompletionsEndpoint(c.Request.URL.Path, c.Request.Method) {
				rewrittenBody, triggerSignal, fcErr := ps.applyFunctionCallRequestRewrite(c, group, finalBodyBytes)
				if fcErr != nil {
					logrus.WithError(fcErr).WithFields(logrus.Fields{
						"group": group.Name,
						"path":  c.Request.URL.Path,
					}).Warn("Failed to apply function call request rewrite, falling back to original body")
				} else if len(rewrittenBody) > 0 && triggerSignal != "" {
					finalBodyBytes = rewrittenBody
					c.Set(ctxKeyTriggerSignal, triggerSignal)
					c.Set(ctxKeyFunctionCallEnabled, true)
					logrus.WithFields(logrus.Fields{
						"group":          group.Name,
						"channel_type":   group.ChannelType,
						"trigger_signal": triggerSignal,
					}).Debug("Function call request rewrite applied")
				}
			}

			isStream = channelHandler.IsStreamRequest(c, finalBodyBytes)
		}
	}

	// Use new retry logic for aggregate groups, old logic for standard groups
	if originalGroup.GroupType == "aggregate" && retryCtx != nil {
		ps.executeRequestWithAggregateRetry(c, channelHandler, originalGroup, finalBodyBytes, isStream, startTime, retryCtx)
	} else {
		ps.executeRequestWithRetry(c, channelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, 0)
	}
}

// executeRequestWithRetry is the core recursive function for handling requests and retries.
func (ps *ProxyServer) executeRequestWithRetry(
	c *gin.Context,
	channelHandler channel.ChannelProxy,
	originalGroup *models.Group,
	group *models.Group,
	bodyBytes []byte,
	isStream bool,
	startTime time.Time,
	retryCount int,
) {
	cfg := group.EffectiveConfig

	// Store group in context for response handlers to access
	c.Set("group", group)

	apiKey, err := ps.keyProvider.SelectKey(group.ID)
	if err != nil {
		logrus.Errorf("Failed to select a key for group %s on attempt %d: %v", group.Name, retryCount+1, err)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, err.Error()))
		ps.logRequest(c, originalGroup, group, nil, startTime, http.StatusServiceUnavailable, err, isStream, "", nil, channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}

	// Select upstream with its dedicated HTTP clients
	upstreamSelection, err := channelHandler.SelectUpstreamWithClients(c.Request.URL, originalGroup.Name)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to select upstream: %v", err)))
		return
	}
	if upstreamSelection == nil || upstreamSelection.URL == "" {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to select upstream: empty result"))
		return
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if isStream {
		ctx, cancel = context.WithCancel(c.Request.Context())
	} else {
		timeout := time.Duration(cfg.RequestTimeout) * time.Second
		ctx, cancel = context.WithTimeout(c.Request.Context(), timeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, c.Request.Method, upstreamSelection.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		logrus.Errorf("Failed to create upstream request: %v", err)
		response.Error(c, app_errors.ErrInternalServer)
		return
	}
	req.ContentLength = int64(len(bodyBytes))

	req.Header = c.Request.Header.Clone()

	// Clean up client auth headers
	utils.CleanClientAuthHeaders(req)

	// Apply anonymization: remove tracking and proxy-revealing headers
	utils.CleanAnonymizationHeaders(req)

	// For /models with mapping configured, remove Accept-Encoding so upstream returns plain (non-gzip) body
	// This ensures we can read/modify the response safely.
	if (len(group.ModelMappingCache) > 0 || group.ModelMapping != "") && ps.isModelsEndpoint(c.Request.URL.Path) {
		req.Header.Del("Accept-Encoding")
		logrus.Debug("Removed Accept-Encoding header for /models endpoint to avoid gzip compression")
	}

	// Apply model redirection
	finalBodyBytes, err := channelHandler.ApplyModelRedirect(req, bodyBytes, group)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error()))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusBadRequest, err, isStream, "", nil, channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}

	// Update request body if it was modified by redirection
	if !bytes.Equal(finalBodyBytes, bodyBytes) {
		req.Body = io.NopCloser(bytes.NewReader(finalBodyBytes))
		req.ContentLength = int64(len(finalBodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(finalBodyBytes)), nil
		}
		bodyBytes = finalBodyBytes
	}

	// Log request
	channelHandler.ModifyRequest(req, apiKey, group)

	// Apply custom header rules
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContextFromGin(c, group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	// Use the upstream-specific client (with its dedicated proxy configuration)
	var client *http.Client
	if isStream {
		client = upstreamSelection.StreamClient
		req.Header.Set("X-Accel-Buffering", "no")
	} else {
		client = upstreamSelection.HTTPClient
	}

	// Defensive nil-check - this should never happen as SelectUpstreamWithClients always returns valid clients
	if client == nil {
		logrus.Errorf("CRITICAL: upstreamSelection returned nil client for group %s, upstream %s", group.Name, upstreamSelection.URL)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Internal error: nil HTTP client"))
		return
	}

	// Log which client is being used for debugging proxy issues
	logrus.WithFields(logrus.Fields{
		"group":     group.Name,
		"upstream":  upstreamSelection.URL,
		"has_proxy": upstreamSelection.ProxyURL != nil && *upstreamSelection.ProxyURL != "",
		"proxy_url": safeProxyURL(upstreamSelection.ProxyURL),
		"is_stream": isStream,
	}).Debug("Using HTTP client for request")

	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Unified error handling for retries. Exclude 404 from being a retryable error.
	if err != nil || (resp != nil && resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound) {
		if ps.shouldAbortOnIgnorableError(c, err) {
			logrus.Debugf("Client-side ignorable error for key %s, aborting retries: %v", utils.MaskAPIKey(apiKey.KeyValue), err)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, 499, err, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, channelHandler, bodyBytes, models.RequestTypeFinal)
			return
		}

		var statusCode int
		var parsedError string

		if err != nil {
			statusCode = 500
			parsedError = err.Error()
			logrus.Debugf("Request failed (attempt %d/%d) for key %s: %v", retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), err)
		} else {
			// HTTP-level error (status >= 400)
			statusCode = resp.StatusCode
			// Limit error body read to a fixed size to prevent memory exhaustion
			errorBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBodySize))
			if readErr != nil {
				logrus.Errorf("Failed to read error body: %v", readErr)
				errorBody = []byte("Failed to read error body")
			}

			errorBody = handleGzipCompression(resp, errorBody)
			parsedError = app_errors.ParseUpstreamError(errorBody)
			logrus.Debugf("Request failed with status %d (attempt %d/%d) for key %s. Parsed Error: %s", statusCode, retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), parsedError)
		}

		// Update key status with parsed error information
		ps.keyProvider.UpdateStatus(apiKey, group, false, parsedError)

		// Check if this is the last retry attempt
		isLastAttempt := retryCount >= cfg.MaxRetries
		requestType := models.RequestTypeRetry
		if isLastAttempt {
			requestType = models.RequestTypeFinal
		}

		ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(parsedError), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, channelHandler, bodyBytes, requestType)

		// If this is the last attempt, return error directly without recursion
		if isLastAttempt {
			response.Error(c, app_errors.NewAPIErrorWithUpstream(statusCode, "UPSTREAM_ERROR", parsedError))
			return
		}

		ps.executeRequestWithRetry(c, channelHandler, originalGroup, group, bodyBytes, isStream, startTime, retryCount+1)
		return
	}

	// Success no longer resets success count to reduce IO overhead
	logrus.Debugf("Request for group %s succeeded on attempt %d with key %s", group.Name, retryCount+1, utils.MaskAPIKey(apiKey.KeyValue))

	// Check if this is a model list request (needs special handling)
	if shouldInterceptModelList(c.Request.URL.Path, c.Request.Method) {
		ps.handleModelListResponse(c, resp, group, channelHandler)
	} else {
		for key, values := range resp.Header {
			for _, value := range values {
				c.Header(key, value)
			}
		}
		c.Status(resp.StatusCode)

		if isStream {
			// For streaming chat completions with function call enabled, use the
			// function-call aware streaming handler. Other streaming requests keep
			// the existing behavior.
			if isCCEnabled(c) {
				ps.handleCCStreamingResponse(c, resp)
			} else if isFunctionCallEnabled(c) {
				ps.handleFunctionCallStreamingResponse(c, resp)
			} else {
				ps.handleStreamingResponse(c, resp)
			}
		} else {
			// For non-streaming chat completions with function call enabled, use
			// the function-call aware response handler.
			if isCCEnabled(c) {
				ps.handleCCNormalResponse(c, resp)
			} else if isFunctionCallEnabled(c) {
				ps.handleFunctionCallNormalResponse(c, resp)
			} else {
				ps.handleNormalResponse(c, resp)
			}
		}
	}

	ps.logRequest(c, originalGroup, group, apiKey, startTime, resp.StatusCode, nil, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, channelHandler, bodyBytes, models.RequestTypeFinal)
}

// executeRequestWithAggregateRetry handles requests for aggregate groups with intelligent retry logic
// It supports exclusion list management and sub-group selection based on weights
func (ps *ProxyServer) executeRequestWithAggregateRetry(
	c *gin.Context,
	channelHandler channel.ChannelProxy,
	originalGroup *models.Group,
	bodyBytes []byte,
	isStream bool,
	startTime time.Time,
	retryCtx *retryContext,
) {
	// Restore original path for retry attempts to allow each sub-group to apply its own CC support
	// This is necessary because different sub-groups may have different CC support settings.
	restoreOriginalPath(c, retryCtx)

	// Get max retries from aggregate group config
	maxRetries := parseMaxRetries(originalGroup.Config)

	// When aggregate group has no explicit max_retries configured (key not present),
	// provide an intelligent default based on sub-group count to prevent immediate
	// failure on the first sub-group. For explicit max_retries: 0 we preserve the
	// "no aggregate retries" semantics for backward compatibility.
	_, hasMaxRetriesKey := originalGroup.Config["max_retries"]
	if !hasMaxRetriesKey && maxRetries == 0 && len(originalGroup.SubGroups) > 1 {
		// Default: try each sub-group once (subgroup_count - 1 retries)
		maxRetries = len(originalGroup.SubGroups) - 1
		logrus.WithFields(logrus.Fields{
			"aggregate_group":     originalGroup.Name,
			"sub_group_count":     len(originalGroup.SubGroups),
			"default_max_retries": maxRetries,
		}).Debug("Aggregate group has no explicit max_retries config, using sub-group count as default")
	}

	// Get sub-group level max retries. When set to 0 or omitted, it does not override
	// the aggregate group's max_retries. For positive values, it acts as an upper
	// bound for aggregate retries to keep total attempts small.
	subMaxRetries := parseSubMaxRetries(originalGroup.Config)
	if subMaxRetries > 0 && maxRetries > subMaxRetries {
		maxRetries = subMaxRetries
	}

	logrus.WithFields(logrus.Fields{
		"aggregate_group": originalGroup.Name,
		"max_retries":     maxRetries,
		"sub_max_retries": subMaxRetries,
		"attempt_count":   retryCtx.attemptCount,
	}).Debug("Aggregate retry configuration")

	// Pre-check: if this is the first attempt, check if there are any valid sub-groups
	if retryCtx.attemptCount == 0 {
		availableCount := ps.countAvailableSubGroups(originalGroup, make(map[uint]bool))
		if availableCount == 0 {
			// No valid sub-groups available, return error immediately without retry
			logrus.WithField("aggregate_group", originalGroup.Name).
				Warn("No valid sub-groups available, skipping retry")
			response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, "No valid sub-groups available"))
			ps.logRequest(c, originalGroup, originalGroup, nil, startTime, http.StatusServiceUnavailable,
				errors.New("no valid sub-groups"), isStream, "", nil, channelHandler, bodyBytes, models.RequestTypeFinal)
			return
		}
	}

	// Select sub-group with exclusion list support
	subGroupName, subGroupID, err := ps.subGroupManager.SelectSubGroupWithRetry(originalGroup, retryCtx.excludedSubGroups)
	if err != nil {
		// All sub-groups are unavailable (runtime error)
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"error":           err,
			"excluded_count":  len(retryCtx.excludedSubGroups),
		}).Error("Failed to select sub-group from aggregate")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, "No available sub-groups"))
		return
	}

	// Get the selected sub-group
	group, err := ps.groupManager.GetGroupByName(subGroupName)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	// Create channel handler for the selected sub-group
	// This is important because different sub-groups may have different channel types
	subGroupChannelHandler, err := ps.channelFactory.GetChannel(group)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"sub_group":       group.Name,
			"error":           err,
		}).Error("Failed to get channel for sub-group")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to get channel for sub-group '%s': %v", group.Name, err)))
		return
	}

	// Store current sub-group ID for failure handling
	c.Set("current_sub_group_id", subGroupID)

	// Apply model mapping for the selected sub-group
	finalBodyBytes, originalModel := ps.applyModelMapping(bodyBytes, group)
	if originalModel != "" && !bytes.Equal(finalBodyBytes, bodyBytes) {
		c.Set("original_model", originalModel)
	}

	// Apply parameter overrides for the selected sub-group
	finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"sub_group":       group.Name,
		}).Warn("Failed to apply parameter overrides for sub-group, using original body")
		finalBodyBytes = bodyBytes
	}

	// Handle Claude count_tokens endpoint for aggregate sub-group (CC only).
	if ps.handleTokenCount(c, group, finalBodyBytes) {
		return
	}

	// Apply CC support for eligible OpenAI sub-groups.
	// Clear any stale CC state from previous sub-group attempts.
	c.Set(ctxKeyCCEnabled, false)
	wasClaudePath := isClaudePath(c.Request.URL.Path, group.Name)

	// Handle CC support path rewriting for sub-groups
	// This rewrites /claude/ paths to standard OpenAI paths. For groups named "claude",
	// OpenAI-style paths like /proxy/claude/v1/messages are not treated as CC paths.
	if isCCSupportEnabled(group) && wasClaudePath {
		originalPath := c.Request.URL.Path
		originalQuery := c.Request.URL.RawQuery
		c.Request.URL.Path = rewriteClaudePathToOpenAIGeneric(c.Request.URL.Path)
		// Sanitize query parameters for CC support (e.g., remove beta=true)
		// These are Claude-specific and should not be passed to OpenAI-style upstreams
		sanitizeCCQueryParams(c.Request.URL)
		c.Set("cc_was_claude_path", true)
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"sub_group":       group.Name,
			"original_path":   originalPath,
			"new_path":        c.Request.URL.Path,
			"original_query":  originalQuery,
			"sanitized_query": c.Request.URL.RawQuery,
		}).Debug("CC support: rewritten Claude path for sub-group and sanitized query params")
	}

	// Convert Claude messages request to OpenAI format
	// Note: Path has already been rewritten from /claude/v1/messages to /v1/messages
	if isCCSupportEnabled(group) && strings.HasSuffix(c.Request.URL.Path, "/v1/messages") {
		convertedBody, converted, ccErr := ps.applyCCRequestConversionDirect(c, group, finalBodyBytes)
		if ccErr != nil {
			logrus.WithError(ccErr).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"path":            c.Request.URL.Path,
			}).Error("Failed to convert Claude request for sub-group")
			// For aggregate groups, we might want to try another sub-group, but conversion failure usually implies
			// malformed input which will fail for all. So we return error.
			response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("CC conversion failed: %v", ccErr)))
			return
		} else if converted {
			finalBodyBytes = convertedBody
			// Rewrite path from /v1/messages to /v1/chat/completions
			c.Request.URL.Path = strings.Replace(c.Request.URL.Path, "/v1/messages", "/v1/chat/completions", 1)
			logrus.WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"channel_type":    group.ChannelType,
				"new_path":        c.Request.URL.Path,
			}).Debug("CC support: converted Claude request for sub-group")
		}
	}

	// Apply function call request rewrite for eligible OpenAI sub-groups.
	// Clear any stale function call state from previous sub-group attempts
	// so that downstream response handlers do not see outdated flags.
	c.Set(ctxKeyFunctionCallEnabled, false)
	c.Set(ctxKeyTriggerSignal, "")
	if isForceFunctionCallEnabled(group) && isChatCompletionsEndpoint(c.Request.URL.Path, c.Request.Method) {
		rewrittenBody, triggerSignal, fcErr := ps.applyFunctionCallRequestRewrite(c, group, finalBodyBytes)
		if fcErr != nil {
			logrus.WithError(fcErr).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"path":            c.Request.URL.Path,
			}).Warn("Failed to apply function call request rewrite for sub-group, falling back to original body")
		} else if len(rewrittenBody) > 0 && triggerSignal != "" {
			finalBodyBytes = rewrittenBody
			c.Set(ctxKeyTriggerSignal, triggerSignal)
			c.Set(ctxKeyFunctionCallEnabled, true)
			logrus.WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"channel_type":    group.ChannelType,
				"trigger_signal":  triggerSignal,
			}).Debug("Function call request rewrite applied for sub-group")
		}
	}

	cfg := group.EffectiveConfig

	// Store group in context for response handlers to access
	c.Set("group", group)

	apiKey, err := ps.keyProvider.SelectKey(group.ID)
	if err != nil {
		logrus.Errorf("Failed to select a key for group %s on attempt %d: %v", group.Name, retryCtx.attemptCount+1, err)

		// Handle sub-group failure
		ps.handleAggregateSubGroupFailure(c, subGroupChannelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, retryCtx, maxRetries, http.StatusServiceUnavailable, err, nil)
		return
	}

	// Create a new URL with the sub-group name instead of aggregate group name
	// Replace /proxy/{aggregate_group}/ with /proxy/{sub_group}/
	// Note: Path format is guaranteed by the router, no validation needed here for performance
	subGroupURL := *c.Request.URL
	subGroupURL.Path = strings.Replace(c.Request.URL.Path, "/proxy/"+originalGroup.Name+"/", "/proxy/"+group.Name+"/", 1)

	logrus.WithFields(logrus.Fields{
		"original_path":   c.Request.URL.Path,
		"subgroup_path":   subGroupURL.Path,
		"aggregate_group": originalGroup.Name,
		"sub_group":       group.Name,
	}).Debug("Rewriting URL path for sub-group")

	// Select upstream with its dedicated HTTP clients
	upstreamSelection, err := subGroupChannelHandler.SelectUpstreamWithClients(&subGroupURL, group.Name)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to select upstream: %v", err)))
		return
	}
	if upstreamSelection == nil || upstreamSelection.URL == "" {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to select upstream: empty result"))
		return
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if isStream {
		ctx, cancel = context.WithCancel(c.Request.Context())
	} else {
		timeout := time.Duration(cfg.RequestTimeout) * time.Second
		ctx, cancel = context.WithTimeout(c.Request.Context(), timeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, c.Request.Method, upstreamSelection.URL, bytes.NewReader(finalBodyBytes))
	if err != nil {
		logrus.Errorf("Failed to create upstream request: %v", err)
		response.Error(c, app_errors.ErrInternalServer)
		return
	}
	req.ContentLength = int64(len(finalBodyBytes))

	req.Header = c.Request.Header.Clone()

	// Clean up client auth headers
	utils.CleanClientAuthHeaders(req)

	// Apply anonymization: remove tracking and proxy-revealing headers
	utils.CleanAnonymizationHeaders(req)

	// For /models with mapping configured, remove Accept-Encoding
	if (len(group.ModelMappingCache) > 0 || group.ModelMapping != "") && ps.isModelsEndpoint(c.Request.URL.Path) {
		req.Header.Del("Accept-Encoding")
		logrus.Debug("Removed Accept-Encoding header for /models endpoint to avoid gzip compression")
	}

	// Apply model redirection for aggregate sub-group before modifying the request
	redirectedBody, err := subGroupChannelHandler.ApplyModelRedirect(req, finalBodyBytes, group)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error()))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusBadRequest, err, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
		return
	}
	if !bytes.Equal(redirectedBody, finalBodyBytes) {
		finalBodyBytes = redirectedBody
		req.Body = io.NopCloser(bytes.NewReader(finalBodyBytes))
		req.ContentLength = int64(len(finalBodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(finalBodyBytes)), nil
		}
	}

	subGroupChannelHandler.ModifyRequest(req, apiKey, group)

	// Apply custom header rules
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContextFromGin(c, group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	// Use the upstream-specific client
	var client *http.Client
	if isStream {
		client = upstreamSelection.StreamClient
		req.Header.Set("X-Accel-Buffering", "no")
	} else {
		client = upstreamSelection.HTTPClient
	}

	// Defensive nil-check - this should never happen as SelectUpstreamWithClients always returns valid clients
	if client == nil {
		logrus.Errorf("CRITICAL: upstreamSelection returned nil client for sub-group %s, upstream %s", group.Name, upstreamSelection.URL)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Internal error: nil HTTP client"))
		return
	}

	// Log which client is being used for debugging proxy issues
	logrus.WithFields(logrus.Fields{
		"group":     group.Name,
		"upstream":  upstreamSelection.URL,
		"has_proxy": upstreamSelection.ProxyURL != nil && *upstreamSelection.ProxyURL != "",
		"proxy_url": safeProxyURL(upstreamSelection.ProxyURL),
		"is_stream": isStream,
	}).Debug("Using HTTP client for aggregate sub-group request")

	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Unified error handling for retries. Exclude 404 from being a retryable error.
	if err != nil || (resp != nil && resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound) {
		if ps.shouldAbortOnIgnorableError(c, err) {
			logrus.Debugf("Client-side ignorable error for key %s, aborting retries: %v", utils.MaskAPIKey(apiKey.KeyValue), err)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, 499, err, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
			return
		}

		var statusCode int
		var parsedError string

		if err != nil {
			statusCode = 500
			parsedError = err.Error()
			logrus.Debugf("Request failed (attempt %d/%d) for key %s: %v", retryCtx.attemptCount+1, maxRetries, utils.MaskAPIKey(apiKey.KeyValue), err)
		} else {
			// HTTP-level error (status >= 400)
			statusCode = resp.StatusCode
			// Limit error body read to a fixed size to prevent memory exhaustion
			errorBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBodySize))
			if readErr != nil {
				logrus.Errorf("Failed to read error body: %v", readErr)
				errorBody = []byte("Failed to read error body")
			}

			errorBody = handleGzipCompression(resp, errorBody)
			parsedError = app_errors.ParseUpstreamError(errorBody)
			logrus.Debugf("Request failed with status %d (attempt %d/%d) for key %s. Parsed Error: %s", statusCode, retryCtx.attemptCount+1, maxRetries, utils.MaskAPIKey(apiKey.KeyValue), parsedError)
		}

		// Update key status
		ps.keyProvider.UpdateStatus(apiKey, group, false, parsedError)

		// Check sub-group's key retry limit
		subGroupCfg := group.EffectiveConfig
		subGroupKeyRetryCount := retryCtx.subGroupKeyRetryMap[subGroupID]
		// Clamp sub-group max retries defensively. We intentionally do not reuse
		// parseRetryConfigInt here because this value comes from EffectiveConfig
		// (already parsed from raw config) and we want to avoid changing its
		// existing parsing semantics while still enforcing a hard upper bound.
		subGroupMaxRetries := subGroupCfg.MaxRetries
		if subGroupMaxRetries < 0 {
			subGroupMaxRetries = 0
		} else if subGroupMaxRetries > 100 {
			subGroupMaxRetries = 100
		}

		// Determine if sub-group has exhausted its key retries
		isSubGroupKeyRetryExhausted := subGroupKeyRetryCount >= subGroupMaxRetries

		// Log detailed retry status
		logrus.WithFields(logrus.Fields{
			"aggregate_group":       originalGroup.Name,
			"sub_group":             group.Name,
			"sub_group_key_retry":   subGroupKeyRetryCount,
			"sub_group_max_retries": subGroupMaxRetries,
			"aggregate_attempt":     retryCtx.attemptCount,
			"aggregate_max_retries": maxRetries,
			"status_code":           statusCode,
			"key_retries_exhausted": isSubGroupKeyRetryExhausted,
		}).Debug("Sub-group request failed, checking retry strategy")

		// If sub-group still has key retries left, retry with a different key in the same sub-group
		if !isSubGroupKeyRetryExhausted {
			// Increment sub-group key retry count
			retryCtx.subGroupKeyRetryMap[subGroupID]++

			// Log retry request for sub-group key retry
			ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(parsedError), isStream,
				upstreamSelection.URL, upstreamSelection.ProxyURL, subGroupChannelHandler, finalBodyBytes, models.RequestTypeRetry)

			logrus.WithFields(logrus.Fields{
				"sub_group":             group.Name,
				"sub_group_key_retry":   subGroupKeyRetryCount + 1,
				"sub_group_max_retries": subGroupMaxRetries,
			}).Debug("Retrying with another key; sub-group may be re-selected if not excluded")

			// Note: we intentionally do not exclude the previously failed key at this
			// layer. Key-level health and blacklisting are handled centrally by
			// KeyProvider.UpdateStatus and the underlying store.Rotate logic. The
			// per-request retry here simply gives the rotation logic another chance
			// to pick a different healthy key when available, while keeping
			// semantics consistent with non-aggregate retry paths.

			// Restore original path for retry (CC support may have modified it)
			restoreOriginalPath(c, retryCtx)

			// Retry with same sub-group but different key (SelectKey will choose a different one)
			ps.executeRequestWithAggregateRetry(c, channelHandler, originalGroup, retryCtx.originalBodyBytes, isStream, startTime, retryCtx)
			return
		}

		// Sub-group key retries exhausted, handle aggregate-level retry (switch to next sub-group)
		logrus.WithFields(logrus.Fields{
			"sub_group":             group.Name,
			"sub_group_key_retries": subGroupKeyRetryCount,
			"sub_group_max_retries": subGroupMaxRetries,
		}).Debug("Sub-group key retries exhausted, switching to next sub-group")

		ps.handleAggregateSubGroupFailure(c, subGroupChannelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, retryCtx, maxRetries, statusCode, errors.New(parsedError), apiKey)
		return
	}

	// Request succeeded
	logrus.Debugf("Request for aggregate group %s succeeded on attempt %d with sub-group %s", originalGroup.Name, retryCtx.attemptCount+1, group.Name)

	if shouldInterceptModelList(c.Request.URL.Path, c.Request.Method) {
		ps.handleModelListResponse(c, resp, group, subGroupChannelHandler)
	} else {
		for key, values := range resp.Header {
			for _, value := range values {
				c.Header(key, value)
			}
		}
		c.Status(resp.StatusCode)

		// Fast path: handle response based on type. We intentionally keep the
		// routing logic aligned with the non-aggregate path so that
		// function-call and CC support behavior is consistent between normal and
		// aggregate groups.
		if isStream {
			if isCCEnabled(c) {
				ps.handleCCStreamingResponse(c, resp)
			} else if isFunctionCallEnabled(c) {
				ps.handleFunctionCallStreamingResponse(c, resp)
			} else {
				ps.handleStreamingResponse(c, resp)
			}
		} else if (len(group.ModelMappingCache) > 0 || group.ModelMapping != "") && ps.isModelsEndpoint(c.Request.URL.Path) {
			c.Writer.Header().Del("Content-Length")
			c.Writer.Header().Del("ETag")
			c.Writer.Header().Del("Transfer-Encoding")
			logrus.WithFields(logrus.Fields{
				"group":               group.Name,
				"path":                c.Request.URL.Path,
				"model_mapping_count": len(group.ModelMappingCache),
			}).Debug("Detected /models endpoint with model mapping, applying enhancement")
			ps.handleModelsResponse(c, resp, group)
		} else {
			if isCCEnabled(c) {
				ps.handleCCNormalResponse(c, resp)
			} else if isFunctionCallEnabled(c) {
				ps.handleFunctionCallNormalResponse(c, resp)
			} else {
				ps.handleNormalResponse(c, resp)
			}
		}
	}

	ps.logRequest(c, originalGroup, group, apiKey, startTime, resp.StatusCode, nil, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
}

// countAvailableSubGroups counts the number of available sub-groups
// Excludes: disabled (enabled=false) and sub-groups in the exclusion list
// Note: Actual key availability is checked during sub-group selection
func (ps *ProxyServer) countAvailableSubGroups(group *models.Group, excludedIDs map[uint]bool) int {
	count := 0
	for _, sg := range group.SubGroups {
		// Skip disabled sub-groups
		if !sg.SubGroupEnabled {
			continue
		}
		// Skip sub-groups in exclusion list
		if excludedIDs[sg.SubGroupID] {
			continue
		}
		count++
	}
	return count
}

// handleAggregateSubGroupFailure handles failure of a sub-group in aggregate retry logic
func (ps *ProxyServer) handleAggregateSubGroupFailure(
	c *gin.Context,
	channelHandler channel.ChannelProxy,
	originalGroup *models.Group,
	group *models.Group,
	bodyBytes []byte,
	isStream bool,
	startTime time.Time,
	retryCtx *retryContext,
	maxRetries int,
	statusCode int,
	err error,
	apiKey *models.APIKey,
) {
	// Get current sub-group ID
	if subGroupID, exists := c.Get("current_sub_group_id"); exists {
		subGroupIDUint, ok := subGroupID.(uint)
		if !ok {
			logrus.WithField("sub_group_id", subGroupID).
				Error("Invalid sub-group ID type in context")
			return
		}

		// Count available sub-groups (excluding disabled and no active keys)
		availableCount := ps.countAvailableSubGroups(originalGroup, retryCtx.excludedSubGroups)

		// Only exclude the failed sub-group if there are more than 1 available
		// If only 1 available, don't exclude it (no other choice)
		if availableCount > 1 {
			retryCtx.excludedSubGroups[subGroupIDUint] = true
			logrus.WithFields(logrus.Fields{
				"sub_group_id":    subGroupIDUint,
				"excluded_count":  len(retryCtx.excludedSubGroups),
				"available_count": availableCount - 1,
			}).Debug("Added failed sub-group to exclusion list")
		} else {
			logrus.WithField("sub_group_id", subGroupIDUint).
				Debug("Not excluding last available sub-group")
		}
	}

	// Check if this is the last attempt
	isLastAttempt := retryCtx.attemptCount >= maxRetries
	requestType := models.RequestTypeRetry
	if isLastAttempt {
		requestType = models.RequestTypeFinal
	}

	ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, err, isStream, "", nil, channelHandler, bodyBytes, requestType)

	// If this is the last attempt, return error
	if isLastAttempt {
		response.Error(c, app_errors.NewAPIErrorWithUpstream(statusCode, "UPSTREAM_ERROR", err.Error()))
		return
	}

	// Check if all available sub-groups have failed
	availableCount := ps.countAvailableSubGroups(originalGroup, retryCtx.excludedSubGroups)
	if availableCount == 0 {
		// All sub-groups failed, reset exclusion list for next retry cycle
		logrus.WithField("aggregate_group", originalGroup.Name).
			Debug("All sub-groups failed, resetting exclusion list for next retry cycle")
		// Clear the map instead of allocating a new one
		for k := range retryCtx.excludedSubGroups {
			delete(retryCtx.excludedSubGroups, k)
		}
	}

	// Increment attempt count and retry
	retryCtx.attemptCount++
	// Use original body bytes for retry to allow new sub-group to apply its own mapping
	ps.executeRequestWithAggregateRetry(c, channelHandler, originalGroup, retryCtx.originalBodyBytes, isStream, startTime, retryCtx)
}

// shouldAbortOnIgnorableError checks if an error is ignorable (e.g. client disconnected)
// and verifies if the client context is actually canceled.
// Returns true if the request should be aborted, false if it should be retried.
func (ps *ProxyServer) shouldAbortOnIgnorableError(c *gin.Context, err error) bool {
	if err != nil && app_errors.IsIgnorableError(err) {
		if c.Request.Context().Err() != nil {
			return true
		}
		// If client is still connected, this is likely an upstream error (e.g. upstream reset connection), so we should retry.
		logrus.Debugf("Ignorable error detected but client is still connected, treating as upstream error and retrying. Error: %v", err)
	}
	return false
}

// logRequest is a helper function to create and record a request log.
func (ps *ProxyServer) logRequest(
	c *gin.Context,
	originalGroup *models.Group,
	group *models.Group,
	apiKey *models.APIKey,
	startTime time.Time,
	statusCode int,
	finalError error,
	isStream bool,
	upstreamAddr string,
	proxyURL *string,
	channelHandler channel.ChannelProxy,
	bodyBytes []byte,
	requestType string,
) {
	if ps.requestLogService == nil {
		return
	}

	var requestBodyToLog, responseBodyToLog, userAgent string

	if group.EffectiveConfig.EnableRequestBodyLogging {
		requestBodyToLog = utils.TruncateString(string(bodyBytes), maxResponseCaptureBytes)
		userAgent = c.Request.UserAgent()

		// Get captured response body from context (if available)
		if responseBody, exists := c.Get("response_body"); exists {
			if responseBodyStr, ok := responseBody.(string); ok {
				responseBodyToLog = utils.TruncateString(responseBodyStr, maxResponseCaptureBytes)
			}
		}
	}

	duration := time.Since(startTime).Milliseconds()

	// Format upstream address with proxy info if available
	upstreamAddrWithProxy := upstreamAddr
	if proxyURL != nil && *proxyURL != "" {
		// Use strings.Builder for better performance in hot path
		var b strings.Builder
		b.Grow(len(upstreamAddr) + len(*proxyURL) + 10) // Pre-allocate capacity
		b.WriteString(upstreamAddr)
		b.WriteString(" (proxy: ")
		b.WriteString(*proxyURL)
		b.WriteByte(')')
		upstreamAddrWithProxy = b.String()
	}

	logEntry := &models.RequestLog{
		GroupID:      group.ID,
		GroupName:    group.Name,
		IsSuccess:    finalError == nil && statusCode < 400,
		SourceIP:     c.ClientIP(),
		StatusCode:   statusCode,
		RequestPath:  utils.TruncateString(c.Request.URL.String(), 500),
		Duration:     duration,
		UserAgent:    userAgent,
		RequestType:  requestType,
		IsStream:     isStream,
		UpstreamAddr: utils.TruncateString(upstreamAddrWithProxy, 500),
		RequestBody:  requestBodyToLog,
		ResponseBody: responseBodyToLog,
	}

	// Set parent group
	if originalGroup != nil && originalGroup.GroupType == "aggregate" && originalGroup.ID != group.ID {
		logEntry.ParentGroupID = originalGroup.ID
		logEntry.ParentGroupName = originalGroup.Name
	}

	if channelHandler != nil && bodyBytes != nil {
		logEntry.Model = channelHandler.ExtractModel(c, bodyBytes)
	}

	// Get original model from context (before mapping)
	if originalModel, exists := c.Get("original_model"); exists {
		if originalModelStr, ok := originalModel.(string); ok && originalModelStr != "" {
			// Store original only when it differs from the actual upstream model
			// Note: MappedModel stores the user's requested model alias (before mapping)
			// while Model stores the actual model sent to upstream (after mapping)
			if logEntry.Model != "" && logEntry.Model != originalModelStr {
				logEntry.MappedModel = originalModelStr
			}
		}
	}

	if apiKey != nil {
		// Encrypt key value for log storage
		encryptedKeyValue, err := ps.encryptionSvc.Encrypt(apiKey.KeyValue)
		if err != nil {
			logrus.WithError(err).Error("Failed to encrypt key value for logging")
			logEntry.KeyValue = "failed-to-encryption"
		} else {
			logEntry.KeyValue = encryptedKeyValue
		}
		// Add KeyHash for reverse lookup
		logEntry.KeyHash = ps.encryptionSvc.Hash(apiKey.KeyValue)
	}

	if finalError != nil {
		logEntry.ErrorMessage = finalError.Error()
	}

	if err := ps.requestLogService.Record(logEntry); err != nil {
		logrus.Errorf("Failed to record request log: %v", err)
	}
}
