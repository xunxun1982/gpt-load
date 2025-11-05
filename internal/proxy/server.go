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
	excludedSubGroups  map[uint]bool // Sub-group IDs that have failed in the current request (only for current aggregate group)
	attemptCount       int           // Current attempt count
	originalBodyBytes  []byte        // Original request body (before any sub-group mapping)
}

// parseMaxRetries extracts and validates max_retries from group config
// Returns a value clamped to the range [0, 5]
func parseMaxRetries(config map[string]any) int {
	if config == nil {
		return 0
	}

	val, ok := config["max_retries"]
	if !ok {
		return 0
	}

	maxRetries := 0
	// Try different type assertions
	switch v := val.(type) {
	case float64:
		maxRetries = int(v)
	case int:
		maxRetries = v
	case int64:
		maxRetries = int(v)
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			maxRetries = int(parsed)
		} else {
			logrus.WithFields(logrus.Fields{
				"value": v,
				"error": err,
			}).Warn("Failed to parse json.Number for max_retries")
		}
	case string:
		if parsed, err := strconv.Atoi(v); err == nil {
			maxRetries = parsed
		} else {
			logrus.WithFields(logrus.Fields{
				"value": v,
				"error": err,
			}).Warn("Failed to parse string for max_retries")
		}
	default:
		logrus.WithFields(logrus.Fields{
			"value": val,
			"type":  fmt.Sprintf("%T", val),
		}).Warn("Unexpected type for max_retries")
	}

	// Clamp to 0-5 range
	if maxRetries < 0 {
		return 0
	}
	if maxRetries > 5 {
		return 5
	}

	return maxRetries
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
			excludedSubGroups: make(map[uint]bool, len(originalGroup.SubGroups)),
			attemptCount:      0,
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

	// Read request body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logrus.Errorf("Failed to read request body: %v", err)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "Failed to read request body"))
		return
	}
	c.Request.Body.Close()

	// For GET requests (like /v1/models), skip body processing
	var finalBodyBytes []byte
	var isStream bool

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

	// Clean up client auth key
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	req.Header.Del("X-Goog-Api-Key")
	req.Header.Del("Proxy-Authorization")

	// For /models with mapping configured, remove Accept-Encoding so upstream returns plain (non-gzip) body
	// This ensures we can read/modify the response safely.
	if (len(group.ModelMappingCache) > 0 || group.ModelMapping != "") && ps.isModelsEndpoint(c.Request.URL.Path) {
		req.Header.Del("Accept-Encoding")
		logrus.Debug("Removed Accept-Encoding header for /models endpoint to avoid gzip compression")
	}

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

	// Defensive nil-check with backward-compatible fallback
	if client == nil {
		if isStream {
			client = channelHandler.GetStreamClient()
		} else {
			client = channelHandler.GetHTTPClient()
		}
	}

	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Unified error handling for retries. Exclude 404 from being a retryable error.
	if err != nil || (resp != nil && resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound) {
		if err != nil && app_errors.IsIgnorableError(err) {
			logrus.Debugf("Client-side ignorable error for key %s, aborting retries: %v", utils.MaskAPIKey(apiKey.KeyValue), err)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, 499, err, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, channelHandler, bodyBytes, models.RequestTypeFinal)
			return
		}

		var statusCode int
		var errorMessage string
		var parsedError string

		if err != nil {
			statusCode = 500
			errorMessage = err.Error()
			parsedError = errorMessage
			logrus.Debugf("Request failed (attempt %d/%d) for key %s: %v", retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), err)
		} else {
			// HTTP-level error (status >= 400)
			statusCode = resp.StatusCode
			errorBody, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				logrus.Errorf("Failed to read error body: %v", readErr)
				errorBody = []byte("Failed to read error body")
			}

			errorBody = handleGzipCompression(resp, errorBody)
			errorMessage = string(errorBody)
			parsedError = app_errors.ParseUpstreamError(errorBody)
			logrus.Debugf("Request failed with status %d (attempt %d/%d) for key %s. Parsed Error: %s", statusCode, retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), parsedError)
		}

		// 使用解析后的错误信息更新密钥状态
		ps.keyProvider.UpdateStatus(apiKey, group, false, parsedError)

		// 判断是否为最后一次尝试
		isLastAttempt := retryCount >= cfg.MaxRetries
		requestType := models.RequestTypeRetry
		if isLastAttempt {
			requestType = models.RequestTypeFinal
		}

		ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(parsedError), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, channelHandler, bodyBytes, requestType)

		// 如果是最后一次尝试，直接返回错误，不再递归
		if isLastAttempt {
			var errorJSON map[string]any
			if err := json.Unmarshal([]byte(errorMessage), &errorJSON); err == nil {
				c.JSON(statusCode, errorJSON)
			} else {
				response.Error(c, app_errors.NewAPIErrorWithUpstream(statusCode, "UPSTREAM_ERROR", errorMessage))
			}
			return
		}

		ps.executeRequestWithRetry(c, channelHandler, originalGroup, group, bodyBytes, isStream, startTime, retryCount+1)
		return
	}

	// ps.keyProvider.UpdateStatus(apiKey, group, true) // 请求成功不再重置成功次数，减少IO消耗
	logrus.Debugf("Request for group %s succeeded on attempt %d with key %s", group.Name, retryCount+1, utils.MaskAPIKey(apiKey.KeyValue))

	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}
	c.Status(resp.StatusCode)

	// Fast path: handle response based on type
	if isStream {
		ps.handleStreamingResponse(c, resp)
	} else if (len(group.ModelMappingCache) > 0 || group.ModelMapping != "") && ps.isModelsEndpoint(c.Request.URL.Path) {
		// Special handling for /models endpoint with model mapping enabled
		// Only check endpoint path if model mapping is configured (performance optimization)
		// Clear stale headers before enhancement
		c.Writer.Header().Del("Content-Length")
		c.Writer.Header().Del("ETag")
		c.Writer.Header().Del("Transfer-Encoding")
		logrus.WithFields(logrus.Fields{
			"group":                group.Name,
			"path":                 c.Request.URL.Path,
			"model_mapping_count":  len(group.ModelMappingCache),
		}).Debug("Detected /models endpoint with model mapping, applying enhancement")
		ps.handleModelsResponse(c, resp, group)
	} else {
		ps.handleNormalResponse(c, resp)
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
	// Get max retries from aggregate group config (default 0)
	maxRetries := parseMaxRetries(originalGroup.Config)

	logrus.WithFields(logrus.Fields{
		"aggregate_group": originalGroup.Name,
		"max_retries":     maxRetries,
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

	cfg := group.EffectiveConfig

	apiKey, err := ps.keyProvider.SelectKey(group.ID)
	if err != nil {
		logrus.Errorf("Failed to select a key for group %s on attempt %d: %v", group.Name, retryCtx.attemptCount+1, err)

		// Handle sub-group failure
		ps.handleAggregateSubGroupFailure(c, subGroupChannelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, retryCtx, maxRetries, http.StatusServiceUnavailable, err)
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

	// Clean up client auth key
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	req.Header.Del("X-Goog-Api-Key")
	req.Header.Del("Proxy-Authorization")

	// For /models with mapping configured, remove Accept-Encoding
	if (len(group.ModelMappingCache) > 0 || group.ModelMapping != "") && ps.isModelsEndpoint(c.Request.URL.Path) {
		req.Header.Del("Accept-Encoding")
		logrus.Debug("Removed Accept-Encoding header for /models endpoint to avoid gzip compression")
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

	// Defensive nil-check with backward-compatible fallback
	if client == nil {
		if isStream {
			client = subGroupChannelHandler.GetStreamClient()
		} else {
			client = subGroupChannelHandler.GetHTTPClient()
		}
	}

	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}

	// Unified error handling for retries. Exclude 404 from being a retryable error.
	if err != nil || (resp != nil && resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound) {
		if err != nil && app_errors.IsIgnorableError(err) {
			logrus.Debugf("Client-side ignorable error for key %s, aborting retries: %v", utils.MaskAPIKey(apiKey.KeyValue), err)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, 499, err, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
			return
		}

		var statusCode int
		var errorMessage string
		var parsedError string

		if err != nil {
			statusCode = 500
			errorMessage = err.Error()
			parsedError = errorMessage
			logrus.Debugf("Request failed (attempt %d/%d) for key %s: %v", retryCtx.attemptCount+1, maxRetries, utils.MaskAPIKey(apiKey.KeyValue), err)
		} else {
			// HTTP-level error (status >= 400)
			statusCode = resp.StatusCode
			errorBody, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				logrus.Errorf("Failed to read error body: %v", readErr)
				errorBody = []byte("Failed to read error body")
			}

			errorBody = handleGzipCompression(resp, errorBody)
			errorMessage = string(errorBody)
			parsedError = app_errors.ParseUpstreamError(errorBody)
			logrus.Debugf("Request failed with status %d (attempt %d/%d) for key %s. Parsed Error: %s", statusCode, retryCtx.attemptCount+1, maxRetries, utils.MaskAPIKey(apiKey.KeyValue), parsedError)
		}

		// Update key status
		ps.keyProvider.UpdateStatus(apiKey, group, false, parsedError)

		// Handle sub-group failure
		ps.handleAggregateSubGroupFailure(c, subGroupChannelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, retryCtx, maxRetries, statusCode, errors.New(parsedError))
		return
	}

	// Request succeeded
	logrus.Debugf("Request for aggregate group %s succeeded on attempt %d with sub-group %s", originalGroup.Name, retryCtx.attemptCount+1, group.Name)

	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}
	c.Status(resp.StatusCode)

	// Fast path: handle response based on type
	if isStream {
		ps.handleStreamingResponse(c, resp)
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
		ps.handleNormalResponse(c, resp)
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

	ps.logRequest(c, originalGroup, group, nil, startTime, statusCode, err, isStream, "", nil, channelHandler, bodyBytes, requestType)

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

	var requestBodyToLog, userAgent string

	if group.EffectiveConfig.EnableRequestBodyLogging {
		requestBodyToLog = utils.TruncateString(string(bodyBytes), 65000)
		userAgent = c.Request.UserAgent()
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
		// 加密密钥值用于日志存储
		encryptedKeyValue, err := ps.encryptionSvc.Encrypt(apiKey.KeyValue)
		if err != nil {
			logrus.WithError(err).Error("Failed to encrypt key value for logging")
			logEntry.KeyValue = "failed-to-encryption"
		} else {
			logEntry.KeyValue = encryptedKeyValue
		}
		// 添加 KeyHash 用于反查
		logEntry.KeyHash = ps.encryptionSvc.Hash(apiKey.KeyValue)
	}

	if finalError != nil {
		logEntry.ErrorMessage = finalError.Error()
	}

	if err := ps.requestLogService.Record(logEntry); err != nil {
		logrus.Errorf("Failed to record request log: %v", err)
	}
}
