// Package proxy provides high-performance OpenAI multi-key proxy server
package proxy

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/failover"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/services"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const (
	maxUpstreamErrorBodySize     = 64 * 1024
	maxProxyBodyPreallocBytes    = 2 * 1024 * 1024
	maxEstimatedTokenBodyBytes   = 256 * 1024
	quotaExhaustedRatePressure   = int64(4)
	requestLogUserAgentMaxRunes  = 512
	retryBackoffRampRetries      = 100
	retryDelayJitterRatio        = 0.30
	statusClientClosedRequest    = 499
	maxRetryConfigRetries        = 5000
	maxSubRetryConfigRetries     = 500
	minCodexAffinityAttempts     = 1
	defaultCodexAffinityAttempts = 5
)

const (
	codexAggregateAffinityTTL        = time.Hour
	codexAggregateAffinityMaxEntries = 10240
)

var quotaExhaustedRateMarkers = []string{
	// Keep generic rate_limit_exceeded / too many requests as light throttling.
	"api_key_quota_exhausted",
	"insufficient_quota",
	"quota exhausted",
	"limit exhausted",
	"quota exceeded",
	"exceeded your current quota",
	"限额已用完",
	"配额已用完",
	"配额用尽",
	"额度已用完",
}

func shouldFailoverOnStatusCode(statusCode int, group *models.Group) bool {
	if group == nil || group.FailoverStatusCodeMatcher.IsZero() {
		return failover.DefaultStatusCodeMatcher().Match(statusCode)
	}
	return group.FailoverStatusCodeMatcher.Match(statusCode)
}

func retryAfterRateLimitPressureFromHeader(header string, now time.Time) int64 {
	header = strings.TrimSpace(header)
	if header == "" {
		return 1
	}

	var waitSeconds int64
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
		waitSeconds = seconds
	} else if retryAt, err := http.ParseTime(header); err == nil {
		waitSeconds = int64(math.Ceil(retryAt.Sub(now).Seconds()))
	}
	if waitSeconds <= 0 {
		return 1
	}

	switch {
	case waitSeconds >= 3600:
		return 5
	case waitSeconds >= 300:
		return 4
	default:
		return 3
	}
}

func setRateLimitPressureContextForAttempt(c *gin.Context, resp *http.Response, now time.Time) {
	if c.Keys != nil {
		delete(c.Keys, ctxKeyRateLimitPressure)
		delete(c.Keys, "response_body")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return
	}
	c.Set(ctxKeyRateLimitPressure, retryAfterRateLimitPressureFromHeader(resp.Header.Get("Retry-After"), now))
}

func retryDelayForAttempt(cfg types.SystemSettings, retryCount int) time.Duration {
	if cfg.RetryDelayMs <= 0 {
		return 0
	}

	// Retry-After is intentionally not folded into this user setting: retry_delay_ms
	// must preserve the old zero-delay default, while rate-limit pressure is handled separately.
	delayMs := int64(cfg.RetryDelayMs)
	maxDelayMs := int64((time.Duration(1<<63-1) / time.Millisecond))
	if delayMs > maxDelayMs {
		delayMs = maxDelayMs
	}
	baseDelay := time.Duration(delayMs) * time.Millisecond
	if !cfg.RetryBackoffEnabled {
		return baseDelay
	}

	maxExtraDelay := retryBackoffMaxExtra(baseDelay, cfg.RetryBackoffMaxPercent)
	if maxExtraDelay <= 0 {
		return baseDelay
	}

	extraDelay := retryBackoffExtraForAttempt(retryCount, maxExtraDelay)
	delay := baseDelay + extraDelay
	maxDelay := baseDelay + maxExtraDelay
	jitterLimit := time.Duration(float64(baseDelay) * retryDelayJitterRatio)
	if jitterLimit > maxExtraDelay {
		jitterLimit = maxExtraDelay
	}
	if jitterLimit <= 0 {
		return delay
	}

	jitter := time.Duration(rand.Float64()*float64(2*jitterLimit)) - jitterLimit
	if jitter > 0 {
		if delay > maxDelay-jitter {
			return maxDelay
		}
		return delay + jitter
	}
	if delay < -jitter {
		return 0
	}
	return delay + jitter
}

func retryBackoffMaxExtra(baseDelay time.Duration, percent int) time.Duration {
	if baseDelay <= 0 || percent <= 0 {
		return 0
	}
	if percent > 100000 {
		percent = 100000
	}
	maxExtra := float64(baseDelay) * float64(percent) / 100
	if maxExtra > float64(time.Duration(1<<63-1)-baseDelay) {
		return time.Duration(1<<63-1) - baseDelay
	}
	return time.Duration(maxExtra)
}

func retryBackoffExtraForAttempt(retryCount int, maxExtraDelay time.Duration) time.Duration {
	if maxExtraDelay <= 0 {
		return 0
	}
	retryAttempt := retryCount + 1
	if retryAttempt >= retryBackoffRampRetries {
		return maxExtraDelay
	}
	if retryAttempt <= 0 {
		return 0
	}
	ratio := float64(retryAttempt) / retryBackoffRampRetries
	return time.Duration(float64(maxExtraDelay) * (math.Pow(2, ratio) - 1))
}

func markAggregateSubGroupFinal(c *gin.Context) func() {
	if c == nil {
		return func() {}
	}
	c.Set(ctxKeyAggregateSubGroupFinal, true)
	return func() {
		c.Set(ctxKeyAggregateSubGroupFinal, false)
	}
}

func isAggregateSubGroupFinal(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get(ctxKeyAggregateSubGroupFinal)
	if !exists {
		return false
	}
	enabled, _ := value.(bool)
	return enabled
}

func codexAggregateAffinityKey(c *gin.Context, group *models.Group, bodyBytes []byte) string {
	if value := codexAggregateAffinityThreadHeaderKey(c, group); value != "" {
		return value
	}

	var payload map[string]any
	err := json.Unmarshal(bodyBytes, &payload)
	return codexAggregateAffinityKeyFromPayload(c, group, payload, err == nil)
}

func codexAggregateAffinityThreadHeaderKey(c *gin.Context, group *models.Group) string {
	if !codexAggregateAffinityEnabled(c, group) {
		return ""
	}
	// A Codex session can contain multiple project threads, so prefer the per-thread identity.
	return firstNonEmptyHeader(c, "Thread-Id")
}

func codexAggregateAffinityFallbackHeaderKey(c *gin.Context, group *models.Group) string {
	if !codexAggregateAffinityEnabled(c, group) {
		return ""
	}
	if value := firstNonEmptyHeader(c, "Session-Id", "X-Client-Request-Id"); value != "" {
		return value
	}
	if value := firstNonEmptyHeader(c, "Session_ID", "session_id"); value != "" {
		return value
	}
	if value := firstNonEmptyHeader(c, "X-Session-ID", "x-session-id"); value != "" {
		return value
	}
	return firstNonEmptyHeader(c, "Conversation_ID", "conversation_id")
}

func codexAggregateAffinityEnabled(c *gin.Context, group *models.Group) bool {
	return c != nil && c.Request != nil && group != nil &&
		c.Request.Method == http.MethodPost &&
		isOpenAIResponsesEndpoint(c.Request.URL.Path) &&
		group.GroupType == "aggregate" &&
		group.ChannelType == "openai-response" &&
		getGroupConfigBool(group, "codex_affinity_enabled")
}

func codexAggregateAffinityKeyFromPayload(c *gin.Context, group *models.Group, payload map[string]any, payloadOK bool) string {
	if !codexAggregateAffinityEnabled(c, group) {
		return ""
	}
	if value := codexAggregateAffinityThreadHeaderKey(c, group); value != "" {
		return value
	}

	metadata, hasMetadata := payload["client_metadata"].(map[string]any)
	if payloadOK && hasMetadata {
		if value := stringFromJSONMap(metadata, "thread_id"); value != "" {
			return value
		}
	}
	if value := codexAggregateAffinityFallbackHeaderKey(c, group); value != "" {
		return value
	}
	if !payloadOK {
		return ""
	}
	if hasMetadata {
		if value := stringFromJSONMap(metadata, "session_id"); value != "" {
			return value
		}
		if value := stringFromJSONMap(metadata, "x-codex-window-id"); value != "" {
			return value
		}
		if value := codexTurnMetadataAffinityKey(stringFromJSONMap(metadata, "x-codex-turn-metadata")); value != "" {
			return value
		}
	}
	if value := codexTurnMetadataAffinityKey(firstNonEmptyHeader(c, "X-Codex-Turn-Metadata")); value != "" {
		return value
	}
	if value := firstNonEmptyHeader(c, "X-Codex-Window-Id", "x-codex-window-id"); value != "" {
		return value
	}
	if value := stringFromJSONMap(payload, "prompt_cache_key"); value != "" {
		return value
	}
	return ""
}

func codexTurnMetadataAffinityKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if value := stringFromJSONMap(payload, "prompt_cache_key"); value != "" {
		return value
	}
	return stringFromJSONMap(payload, "window_id")
}

func stringFromJSONMap(payload map[string]any, key string) string {
	if value, ok := payload[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func stripCodexAffinityFallbackEncryptedReasoning(bodyBytes []byte) ([]byte, bool, error) {
	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return bodyBytes, false, err
	}

	changed := stripResponsesEncryptedReasoningInclude(payload)
	if stripReasoningInputItems(payload) {
		changed = true
	}
	if !changed {
		return bodyBytes, false, nil
	}

	result, err := json.Marshal(payload)
	if err != nil {
		return bodyBytes, false, err
	}
	return result, true, nil
}

func stripResponsesEncryptedReasoningInclude(payload map[string]any) bool {
	rawInclude, ok := payload["include"]
	if !ok {
		return false
	}
	include, ok := rawInclude.([]any)
	if !ok {
		return false
	}

	filtered := make([]any, 0, len(include))
	removed := false
	for _, item := range include {
		if text, ok := item.(string); ok && text == responsesEncryptedReasoning {
			removed = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !removed {
		return false
	}
	if len(filtered) == 0 {
		delete(payload, "include")
	} else {
		payload["include"] = filtered
	}
	return true
}

func stripReasoningInputItems(payload map[string]any) bool {
	switch input := payload["input"].(type) {
	case []any:
		filtered := make([]any, 0, len(input))
		removed := false
		for _, item := range input {
			if isReasoningResponseItem(item) {
				removed = true
				continue
			}
			filtered = append(filtered, item)
		}
		if removed {
			payload["input"] = filtered
		}
		return removed
	case map[string]any:
		if isReasoningResponseItem(input) {
			payload["input"] = []any{}
			return true
		}
	}
	return false
}

func isReasoningResponseItem(item any) bool {
	itemMap, ok := item.(map[string]any)
	if !ok {
		return false
	}
	itemType, _ := itemMap["type"].(string)
	return strings.TrimSpace(itemType) == "reasoning"
}

type codexAggregateAffinityCacheEntry struct {
	key        string
	subGroupID uint
	expiresAt  time.Time
}

// Uses the standard-library list instead of a third-party LRU to keep Go
// dependencies unchanged while preserving O(1) promotion and eviction. The
// single lock is intentional; shard this only after a benchmark proves cache
// contention on real traffic.
type codexAggregateAffinityCache struct {
	mu      sync.RWMutex
	entries map[string]*list.Element
	order   *list.List
	ttl     time.Duration
	maxSize int
}

func newCodexAggregateAffinityCache(ttl time.Duration, maxSize int) *codexAggregateAffinityCache {
	if ttl <= 0 {
		ttl = codexAggregateAffinityTTL
	}
	if maxSize <= 0 {
		maxSize = codexAggregateAffinityMaxEntries
	}
	return &codexAggregateAffinityCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

func (cache *codexAggregateAffinityCache) get(key string, now time.Time) (uint, bool) {
	if cache == nil || key == "" {
		return 0, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()

	element, ok := cache.entries[key]
	if !ok {
		return 0, false
	}
	entry := element.Value.(*codexAggregateAffinityCacheEntry)
	if !entry.expiresAt.After(now) {
		cache.removeElementLocked(element)
		return 0, false
	}

	cache.order.MoveToFront(element)
	return entry.subGroupID, true
}

func (cache *codexAggregateAffinityCache) set(key string, subGroupID uint, now time.Time) {
	if cache == nil || key == "" || subGroupID == 0 {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if element, ok := cache.entries[key]; ok {
		entry := element.Value.(*codexAggregateAffinityCacheEntry)
		entry.subGroupID = subGroupID
		entry.expiresAt = now.Add(cache.ttl)
		cache.order.MoveToFront(element)
		return
	}

	cache.removeExpiredLocked(now)

	entry := &codexAggregateAffinityCacheEntry{
		key:        key,
		subGroupID: subGroupID,
		expiresAt:  now.Add(cache.ttl),
	}
	cache.entries[key] = cache.order.PushFront(entry)
	if len(cache.entries) > cache.maxSize {
		cache.removeOldestLocked()
	}
}

func (cache *codexAggregateAffinityCache) removeOldestLocked() {
	element := cache.order.Back()
	if element != nil {
		cache.removeElementLocked(element)
	}
}

func (cache *codexAggregateAffinityCache) removeExpiredLocked(now time.Time) {
	for element := cache.order.Back(); element != nil; {
		entry := element.Value.(*codexAggregateAffinityCacheEntry)
		if entry.expiresAt.After(now) {
			return
		}
		previous := element.Prev()
		cache.removeElementLocked(element)
		element = previous
	}
}

func (cache *codexAggregateAffinityCache) removeElementLocked(element *list.Element) {
	entry := element.Value.(*codexAggregateAffinityCacheEntry)
	delete(cache.entries, entry.key)
	cache.order.Remove(element)
}

func codexAggregateAffinityCacheKey(groupID uint, affinityKey string, model string) string {
	affinityKey = strings.TrimSpace(affinityKey)
	model = strings.TrimSpace(model)
	if groupID == 0 || affinityKey == "" {
		return ""
	}

	// Client identifiers are routing hints only; group and model keep every binding within its route scope.
	h := sha256.New()
	var numberBuffer [20]byte
	_, _ = h.Write(strconv.AppendUint(numberBuffer[:0], uint64(groupID), 10))
	_, _ = io.WriteString(h, "\x00")
	writeField := func(value string) {
		_, _ = h.Write(strconv.AppendInt(numberBuffer[:0], int64(len(value)), 10))
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, value)
	}
	writeField(model)
	writeField(affinityKey)
	return hex.EncodeToString(h.Sum(nil))
}

func modelFromRequestBody(bodyBytes []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}

func firstNonEmptyHeader(c *gin.Context, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(c.Request.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func waitBeforeRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx == nil || ctx.Err() == nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func quotaExhaustedRateLimitPressureFromContext(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	responseBody, exists := c.Get("response_body")
	if !exists {
		return 0
	}
	body, ok := responseBody.(string)
	if !ok {
		return 0
	}
	body = strings.ToLower(body)
	for _, marker := range quotaExhaustedRateMarkers {
		if strings.Contains(body, marker) {
			return quotaExhaustedRatePressure
		}
	}
	return 0
}

func logicalStatusFromContext(c *gin.Context) (int, string, bool) {
	if c == nil {
		return 0, "", false
	}
	value, exists := c.Get(ctxKeyUpstreamLogicalStatusCode)
	if !exists {
		return 0, "", false
	}
	statusCode, ok := value.(int)
	if !ok || statusCode <= 0 {
		return 0, "", false
	}
	message, _ := c.Get(ctxKeyUpstreamLogicalErrorMessage)
	messageStr, _ := message.(string)
	return statusCode, strings.TrimSpace(messageStr), true
}

func effectiveNonStreamRequestContext(parent context.Context, cfg types.SystemSettings) (context.Context, context.CancelFunc) {
	if cfg.NonStreamRequestTimeout > 0 {
		return context.WithTimeout(parent, time.Duration(cfg.NonStreamRequestTimeout)*time.Second)
	}
	if cfg.RequestTimeout > 0 {
		return context.WithTimeout(parent, time.Duration(cfg.RequestTimeout)*time.Second)
	}
	return context.WithCancel(parent)
}

// Context keys used for function call middleware.
const (
	ctxKeyTriggerSignal               = "fc_trigger_signal"
	ctxKeyFunctionCallEnabled         = "fc_enabled"
	ctxKeyRateLimitPressure           = "rate_limit_pressure"
	ctxKeyAggregateSubGroupFinal      = "aggregate_sub_group_final"
	ctxKeyUpstreamLogicalStatusCode   = "upstream_logical_status_code"
	ctxKeyUpstreamLogicalErrorMessage = "upstream_logical_error_message"
	ctxKeyUpstreamUserAgent           = "upstream_user_agent"
	ctxKeyResponsesStatusUnverified   = "responses_status_unverified"
	ctxKeyResponseProcessingFailed    = "response_processing_failed"
)

// ProxyServer represents the proxy server
type ProxyServer struct {
	keyProvider          *keypool.KeyProvider
	groupManager         *services.GroupManager
	subGroupManager      *services.SubGroupManager
	settingsManager      *config.SystemSettingsManager
	channelFactory       *channel.Factory
	requestLogService    *services.RequestLogService
	encryptionSvc        encryption.Service
	dynamicWeightManager *services.DynamicWeightManager // Optional dynamic weight manager for adaptive load balancing
	codexAffinityCache   *codexAggregateAffinityCache
}

// retryContext holds the retry state for a single request
// This context is created per request and lives only for the request's lifetime
type retryContext struct {
	excludedSubGroups              map[uint]bool // Sub-group IDs that have failed in the current request (only for current aggregate group)
	attemptCount                   int           // Current attempt count (aggregate-level sub-group switches)
	originalBodyBytes              []byte        // Original request body (before any sub-group mapping)
	originalPath                   string        // Original request path (for CC support restoration)
	subGroupKeyRetryMap            map[uint]int  // Tracks key retry count for each sub-group (sub-group ID -> retry count)
	forcedSubGroupID               uint          // Keeps key-level retries on the selected sub-group until its retry budget is exhausted
	codexAffinityKey               string        // Stable Codex affinity key for this aggregate request
	codexAffinityCacheKey          string        // Precomputed bounded cache key reused across retries and binding
	codexAffinityPrimarySubGroupID uint          // Stable primary sub-group for Codex affinity requests
	codexAffinityAttemptCount      int           // Actual requests sent to the affinity primary before degradation
	codexAffinityDegraded          bool          // Keeps encrypted reasoning stripped after leaving the affinity stage
	codexParsedPayload             map[string]any
	codexParsedPayloadSet          bool
	codexParsedModel               string
	codexParsedModelSet            bool
	lifecycleCtx                   context.Context
	lifecycleCancel                context.CancelFunc
	lifecycleConfig                types.SystemSettings
	lifecycleConfigSet             bool
	lifecycleStartTime             time.Time
	lifecycleStreamMode            bool
}

func (rc *retryContext) codexRequestPayload(bodyBytes []byte) (map[string]any, bool) {
	if rc == nil {
		var payload map[string]any
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			return nil, false
		}
		return payload, true
	}
	if !rc.codexParsedPayloadSet {
		source := rc.originalBodyBytes
		if len(source) == 0 {
			source = bodyBytes
		}
		if len(source) > 0 {
			var payload map[string]any
			if err := json.Unmarshal(source, &payload); err == nil {
				rc.codexParsedPayload = payload
			}
		}
		rc.codexParsedPayloadSet = true
	}
	return rc.codexParsedPayload, rc.codexParsedPayload != nil
}

func (rc *retryContext) codexRequestModel(bodyBytes []byte) string {
	if rc == nil {
		return modelFromRequestBody(bodyBytes)
	}
	if !rc.codexParsedModelSet {
		if rc.codexParsedPayloadSet && rc.codexParsedPayload != nil {
			rc.codexParsedModel = stringFromJSONMap(rc.codexParsedPayload, "model")
			rc.codexParsedModelSet = true
			return rc.codexParsedModel
		}
		source := rc.originalBodyBytes
		if len(source) == 0 {
			source = bodyBytes
		}
		rc.codexParsedModel = modelFromRequestBody(source)
		rc.codexParsedModelSet = true
	}
	return rc.codexParsedModel
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
		parsedURL.User = url.User("***")
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

// clearForceProtocolContext prevents one aggregate sub-group attempt from
// leaking forced CC/Codex/function-call state into the next selected sub-group.
func clearForceProtocolContext(c *gin.Context) {
	if c == nil || c.Keys == nil {
		return
	}
	delete(c.Keys, ctxKeyCCEnabled)
	delete(c.Keys, ctxKeyOriginalFormat)
	delete(c.Keys, ctxKeyOpenAIResponseCC)
	delete(c.Keys, ctxKeyGeminiCC)
	delete(c.Keys, ctxKeyCodexEnabled)
	delete(c.Keys, ctxKeyCodexUpstreamFormat)
	delete(c.Keys, ctxKeyOpenAIToolNameReverseMap)
	delete(c.Keys, ctxKeyCodexToolNameReverseMap)
	delete(c.Keys, ctxKeyCodexToolContext)
	delete(c.Keys, ctxKeyFunctionCallEnabled)
	delete(c.Keys, ctxKeyTriggerSignal)
	delete(c.Keys, "cc_was_claude_path")
	delete(c.Keys, "codex_was_codex_path")
}

func forcedAggregateSubGroup(group *models.Group, forcedID uint, excluded map[uint]bool) (string, uint, bool) {
	if group == nil || forcedID == 0 || excluded[forcedID] {
		return "", 0, false
	}
	for _, sg := range group.SubGroups {
		if sg.SubGroupID == forcedID && sg.SubGroupEnabled {
			return sg.SubGroupName, sg.SubGroupID, sg.SubGroupName != ""
		}
	}
	return "", 0, false
}

func sanitizeInternalErrorMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return message
	}
	return utils.SanitizeErrorBody(message)
}

func sanitizeInternalError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(sanitizeInternalErrorMessage(err.Error()))
}

// parseRetryConfigInt returns a clamped retry value and whether the configured
// value used a supported integer representation. Missing values are invalid here.
func parseRetryConfigInt(cfg map[string]any, key string) (int, bool) {
	if cfg == nil {
		return 0, false
	}

	val, ok := cfg[key]
	if !ok {
		return 0, false
	}

	maxRetries := maxRetryConfigRetries
	switch key {
	case "sub_max_retries":
		maxRetries = maxSubRetryConfigRetries
	case "codex_affinity_max_retries":
		maxRetries = config.MaxCodexAffinityAttempts
	}

	var retries int64
	// JSON, database adapters, and typed callers can expose different numeric types.
	switch v := val.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v {
			logrus.WithFields(logrus.Fields{
				"config_key": key,
				"value":      v,
			}).Warn("Retry config value must be a finite integer")
			return 0, false
		}
		if v < 0 {
			return 0, true
		}
		if v > float64(maxRetries) {
			return maxRetries, true
		}
		retries = int64(v)
	case int:
		retries = int64(v)
	case int64:
		retries = v
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			retries = parsed
		} else {
			logrus.WithFields(logrus.Fields{
				"config_key": key,
				"value":      v,
				"error":      err,
			}).Warn("Failed to parse json.Number for retry config value")
			return 0, false
		}
	case string:
		if parsed, err := strconv.Atoi(v); err == nil {
			retries = int64(parsed)
		} else {
			logrus.WithFields(logrus.Fields{
				"config_key": key,
				"value":      v,
				"error":      err,
			}).Warn("Failed to parse string for retry config value")
			return 0, false
		}
	default:
		logrus.WithFields(logrus.Fields{
			"config_key": key,
			"value":      val,
			"type":       fmt.Sprintf("%T", val),
		}).Warn("Unexpected type for retry config value")
		return 0, false
	}

	if retries < 0 {
		return 0, true
	}
	if retries > int64(maxRetries) {
		return maxRetries, true
	}

	return int(retries), true
}

// parseMaxRetries extracts and validates max_retries from group config
// Returns a value clamped to the range [0, 5000].
func parseMaxRetries(config map[string]any) int {
	retries, _ := parseRetryConfigInt(config, "max_retries")
	return retries
}

// parseSubMaxRetries extracts and validates sub_max_retries from group config.
// Returns the clamped value and whether the parent explicitly configured it.
func parseSubMaxRetries(config map[string]any) (int, bool) {
	if config == nil {
		return 0, false
	}
	_, ok := config["sub_max_retries"]
	if !ok {
		return 0, false
	}
	retries, _ := parseRetryConfigInt(config, "sub_max_retries")
	return retries, true
}

// parseCodexAffinityMaxAttempts returns the total affinity attempt limit,
// including the first request. Missing values use 5; invalid persisted values
// fail closed to one attempt so malformed config cannot amplify upstream load.
func parseCodexAffinityMaxAttempts(cfg map[string]any) int {
	if cfg == nil {
		return defaultCodexAffinityAttempts
	}
	if _, ok := cfg["codex_affinity_max_retries"]; !ok {
		return defaultCodexAffinityAttempts
	}
	attempts, valid := parseRetryConfigInt(cfg, "codex_affinity_max_retries")
	if !valid || attempts < minCodexAffinityAttempts {
		return minCodexAffinityAttempts
	}
	if attempts > config.MaxCodexAffinityAttempts {
		return config.MaxCodexAffinityAttempts
	}
	return attempts
}

func subGroupKeyMaxRetries(subGroupCfg types.SystemSettings, parentSubMaxRetries int, parentSubMaxRetriesSet bool) int {
	maxRetries := subGroupCfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if parentSubMaxRetriesSet && maxRetries > parentSubMaxRetries {
		return parentSubMaxRetries
	}
	// Retry counts are only ceilings; the per-request lifecycle context remains
	// the hard wall for client.Do calls and retry sleeps.
	return maxRetries
}

// isForceFunctionCallEnabled checks whether the force_function_call flag is enabled
// for the given group. The middleware is limited to known non-Gemini tool schemas
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

	switch group.ChannelType {
	case "openai", "openai-response", "anthropic":
	default:
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

// getParallelToolCallsConfig returns the parallel_tool_calls configuration for the group.
// Returns:
//   - *bool: pointer to the configured value, or nil if not configured
//
// When nil is returned, the caller should either:
//   - Not include the parameter (let upstream use its default, typically true)
//   - Use a sensible default based on the use case
//
// This configuration is useful for:
//   - Disabling parallel tool calls for models like gpt-4.1-nano that may have issues
//   - Ensuring single tool call per request for simpler client handling
//   - Compatibility with upstreams that don't support parallel tool calls
func getParallelToolCallsConfig(group *models.Group) *bool {
	if group == nil || group.Config == nil {
		return nil
	}

	raw, ok := group.Config["parallel_tool_calls"]
	if !ok || raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case bool:
		return &v
	case *bool:
		return v
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		switch lower {
		case "true", "1", "yes", "on":
			result := true
			return &result
		case "false", "0", "no", "off":
			result := false
			return &result
		}
	case float64:
		result := v != 0
		return &result
	case int:
		result := v != 0
		return &result
	case int64:
		result := v != 0
		return &result
	}

	return nil
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

// isOpenAIResponsesEndpoint checks whether the current request targets the
// OpenAI Responses API endpoint used by the openai-response channel.
func isOpenAIResponsesEndpoint(path string) bool {
	if path == "/v1/responses" {
		return true
	}
	return strings.HasSuffix(path, "/v1/responses")
}

func isOpenAIResponsesCodexEndpoint(path string) bool {
	return isOpenAIResponsesEndpoint(path) ||
		path == "/v1/responses/compact" ||
		strings.HasSuffix(path, "/v1/responses/compact")
}

func rewriteCodexResponsesPathToUpstream(path, upstreamPath string) string {
	if strings.HasSuffix(path, "/v1/responses/compact") {
		return strings.TrimSuffix(path, "/v1/responses/compact") + upstreamPath
	}
	return strings.Replace(path, "/v1/responses", upstreamPath, 1)
}

func isFunctionCallRewriteEndpoint(group *models.Group, path, method string) bool {
	if group == nil {
		return false
	}
	switch group.ChannelType {
	case "openai":
		return isChatCompletionsEndpoint(path, method)
	case "openai-response":
		return method == http.MethodPost && isOpenAIResponsesEndpoint(path)
	case "anthropic":
		return isAnthropicMessagesEndpoint(path, method)
	default:
		return false
	}
}

// isOpenAIResponseForcedStream returns true if OpenAI Responses forced streaming was applied.
// This indicates the response handler should collect stream and return non-stream.
func isOpenAIResponseForcedStream(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeyOpenAIResponseForcedStream); ok {
		if enabled, ok := v.(bool); ok && enabled {
			return true
		}
	}
	return false
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

func (ps *ProxyServer) handleTokenCount(c *gin.Context, group *models.Group, bodyBytes []byte) bool {
	if c == nil || c.Request == nil || group == nil {
		return false
	}
	if c.Request.Method != http.MethodPost {
		return false
	}
	if !isCCSupportEnabled(group) {
		return false
	}

	path := c.Request.URL.Path
	// Path is already rewritten from /claude/v1/messages/count_tokens to /v1/messages/count_tokens
	// or /v1beta/messages/count_tokens (for Gemini CC) by rewriteClaudePathToOpenAIGeneric()
	// or rewriteClaudePathToGemini() before this function is called.
	// This works for OpenAI CC mode, OpenAI Responses CC mode, and Gemini CC mode (/claude entry point).
	if !strings.HasSuffix(path, "/v1/messages/count_tokens") &&
		!strings.HasSuffix(path, "/v1beta/messages/count_tokens") {
		return false
	}

	// Local heuristic estimation: count runes and assume ~4 runes per token.
	// This endpoint is intercepted locally and not forwarded to upstream.
	// Supports: OpenAI channel CC mode, OpenAI Responses CC mode, Gemini channel CC mode (/claude entry).
	estimatedTokens := estimateTokensForClaudeCountTokens(bodyBytes)

	// Apply multiplier (billing adjustment).
	multiplier := getTokenMultiplier()
	rawAdjusted := float64(estimatedTokens) * multiplier
	adjustedTokens := 0
	if !math.IsNaN(rawAdjusted) && !math.IsInf(rawAdjusted, 0) && rawAdjusted > 0 {
		adjustedTokens = int(math.Ceil(rawAdjusted))
	}
	if adjustedTokens <= 0 {
		if estimatedTokens > 0 {
			adjustedTokens = estimatedTokens
		} else {
			adjustedTokens = 1
		}
	}

	// Claude /v1/messages/count_tokens returns only input_tokens.
	c.JSON(http.StatusOK, gin.H{"input_tokens": adjustedTokens})
	return true
}

// handleEventLoggingBatch handles Claude Code event logging batch endpoint.
// This endpoint is intercepted locally and not forwarded to upstream.
// Supports:
// 1. OpenAI channel CC mode - intercepts /claude/api/event_logging/batch
// 2. OpenAI Responses CC mode (/claude entry) - intercepts /claude/api/event_logging/batch
// 3. Anthropic channel (intercept_event_log enabled) - intercepts /api/event_logging/batch
// Returns: {"accepted_count": X, "rejected_count": 0} where X is the number of events.
func (ps *ProxyServer) handleEventLoggingBatch(c *gin.Context, group *models.Group, bodyBytes []byte) bool {
	if c == nil || c.Request == nil || group == nil {
		return false
	}
	if c.Request.Method != http.MethodPost {
		return false
	}

	path := c.Request.URL.Path

	// Check if this is a CC support case (OpenAI, OpenAI Responses, or Gemini channel with /claude/api/event_logging/batch)
	// isCCSupportEnabled() returns true for OpenAI, OpenAI Responses, and Gemini channels when cc_support is enabled.
	isCCCase := isCCSupportEnabled(group) && strings.HasSuffix(path, "/claude/api/event_logging/batch")

	// Check if this is an Anthropic intercept case (/api/event_logging/batch without /claude/ prefix)
	isAnthropicCase := isInterceptEventLogEnabled(group) && strings.HasSuffix(path, "/api/event_logging/batch") && !strings.Contains(path, "/claude/")

	if !isCCCase && !isAnthropicCase {
		return false
	}

	// Parse request body to count events.
	// Note: JSON unmarshal errors are intentionally not logged here for consistency
	// with handleTokenCount and other similar handlers. This is a high-frequency
	// endpoint where debug logging would add overhead without significant value.
	// On parse failure, eventsCount defaults to 0 which is acceptable behavior.
	//
	// Note: We intentionally do NOT cap the events array size here because:
	// 1. Request body size is already limited at HTTP server level (nginx/LB)
	// 2. Memory is already allocated when bodyBytes is read, capping array length won't help
	// 3. We only count len(Events), not iterate/process them, so no additional memory
	// 4. Capping would return incorrect accepted_count, misleading the client
	var reqBody struct {
		Events []json.RawMessage `json:"events"`
	}

	eventsCount := 0
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &reqBody); err == nil {
			eventsCount = len(reqBody.Events)
		}
	}

	// Return response in Claude Code expected format
	c.JSON(http.StatusOK, gin.H{
		"accepted_count": eventsCount,
		"rejected_count": 0,
	})
	return true
}

// rewritePathForGeminiCC rewrites the request path for Gemini CC mode.
// It constructs the Gemini API path format: /v1beta/models/{model}:{endpoint}
// where endpoint is either "generateContent" or "streamGenerateContent".
// The model name defaults to "gemini-2.5-pro" (current stable version) if not
// specified in context via "gemini_cc_model" key.
// Note: "gemini-pro" is deprecated and should not be used as fallback.
func (ps *ProxyServer) rewritePathForGeminiCC(c *gin.Context) string {
	// Default to gemini-2.5-pro (current stable version as of 2025)
	// Previous default "gemini-pro" is deprecated per Google AI documentation
	modelName := "gemini-2.5-pro"
	if ccModel, exists := c.Get("gemini_cc_model"); exists {
		if model, ok := ccModel.(string); ok && model != "" {
			modelName = model
		}
	}

	// Determine endpoint based on streaming mode
	endpoint := "generateContent"
	if streamMode, exists := c.Get("gemini_stream_mode"); exists {
		if isStreamMode, ok := streamMode.(bool); ok && isStreamMode {
			endpoint = "streamGenerateContent"
		}
	}

	return fmt.Sprintf("/v1beta/models/%s:%s", modelName, endpoint)
}

func estimateTokensForClaudeCountTokens(bodyBytes []byte) int {
	if len(bodyBytes) == 0 {
		return 0
	}

	var req ClaudeRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return utils.EstimateTokensFromBytes(bodyBytes)
	}

	var sb strings.Builder
	if strings.TrimSpace(req.Prompt) != "" {
		sb.WriteString(req.Prompt)
		sb.WriteByte('\n')
	}
	if len(req.System) > 0 {
		sb.WriteString(extractTextFromClaudeRaw(req.System))
		sb.WriteByte('\n')
	}
	for _, msg := range req.Messages {
		if len(msg.Content) == 0 {
			continue
		}
		sb.WriteString(extractTextFromClaudeRaw(msg.Content))
		sb.WriteByte('\n')
	}
	if len(req.Tools) > 0 {
		if b, err := json.Marshal(req.Tools); err == nil {
			sb.Write(b)
		}
	}

	return utils.EstimateTokensFromString(sb.String())
}

func extractTextFromClaudeRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	var blocks []ClaudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil && len(blocks) > 0 {
		var sb strings.Builder
		for _, block := range blocks {
			switch block.Type {
			case "text":
				sb.WriteString(block.Text)
			case "tool_use":
				sb.WriteString("<invoke name=\"")
				sb.WriteString(block.Name)
				sb.WriteString("\">")
				if len(block.Input) > 0 {
					sb.Write(block.Input)
				}
				sb.WriteString("</invoke>")
			case "tool_result":
				sb.WriteString("<tool_result>")
				if len(block.Content) > 0 {
					sb.Write(block.Content)
				}
				sb.WriteString("</tool_result>")
			}
		}
		return sb.String()
	}

	return string(raw)
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
		keyProvider:          keyProvider,
		groupManager:         groupManager,
		subGroupManager:      subGroupManager,
		settingsManager:      settingsManager,
		channelFactory:       channelFactory,
		requestLogService:    requestLogService,
		encryptionSvc:        encryptionSvc,
		dynamicWeightManager: nil, // Set via SetDynamicWeightManager if needed
		codexAffinityCache:   newCodexAggregateAffinityCache(codexAggregateAffinityTTL, codexAggregateAffinityMaxEntries),
	}, nil
}

// SetDynamicWeightManager sets the dynamic weight manager for adaptive load balancing.
// This is optional - if not set, static weights will be used.
func (ps *ProxyServer) SetDynamicWeightManager(dwm *services.DynamicWeightManager) {
	ps.dynamicWeightManager = dwm
	// Also set it on the sub-group manager for consistent behavior
	if ps.subGroupManager != nil {
		ps.subGroupManager.SetDynamicWeightManager(dwm)
	}
}

// GetDynamicWeightManager returns the dynamic weight manager if set.
func (ps *ProxyServer) GetDynamicWeightManager() *services.DynamicWeightManager {
	return ps.dynamicWeightManager
}

// HandleProxy is the main entry point for proxy requests, refactored based on the stable .bak logic.
func (ps *ProxyServer) HandleProxy(c *gin.Context) {
	startTime := time.Now()
	groupName := c.Param("group_name")

	originalGroup, err := ps.groupManager.GetGroupByName(groupName)
	if err != nil {
		// Note: ProxyAuth middleware already logs this error, so we don't log again here
		response.Error(c, app_errors.ParseDBError(err))
		return
	}

	// Check if group is enabled
	if !originalGroup.Enabled {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("Group '%s' is disabled", groupName)))
		ps.logEarlyError(c, originalGroup, startTime, http.StatusBadRequest, fmt.Errorf("group disabled: %s", groupName))
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

	group := originalGroup

	var channelHandler channel.ChannelProxy
	if originalGroup.GroupType != "aggregate" {
		channelHandler, err = ps.channelFactory.GetChannel(group)
		if err != nil {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to get channel for group '%s': %v", groupName, err)))
			ps.logEarlyError(c, group, startTime, http.StatusInternalServerError, fmt.Errorf("failed to get channel: %v", err))
			return
		}
	}

	// Read request body using the tiered buffer pool to reduce reallocations.
	// Do not trust Content-Length for large preallocations; the body reader still enforces the real size.
	var buf *bytes.Buffer
	if contentLength := c.Request.ContentLength; contentLength > 0 && contentLength <= maxProxyBodyPreallocBytes {
		buf = utils.GetBufferWithCapacity(int(contentLength))
	} else {
		buf = utils.GetBuffer()
	}
	defer utils.PutBuffer(buf)

	_, err = buf.ReadFrom(c.Request.Body)
	if err != nil {
		logrus.Errorf("Failed to read request body: %v", err)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "Failed to read request body"))
		ps.logEarlyError(c, group, startTime, http.StatusBadRequest, fmt.Errorf("failed to read request body: %v", err))
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

	// Check preconditions for aggregate groups
	// Preconditions must be met before the request can enter the aggregate group
	if originalGroup.GroupType == "aggregate" {
		maxSizeKB := originalGroup.GetMaxRequestSizeKB()
		// Use ceiling division to avoid allowing payloads slightly over the limit
		// Example: 1025 bytes should count as 2 KB, not 1 KB
		requestSizeKB := (len(bodyBytes) + 1023) / 1024

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"aggregate_group":    originalGroup.Name,
				"group_id":           originalGroup.ID,
				"request_size_kb":    requestSizeKB,
				"request_size_bytes": len(bodyBytes),
				"max_size_kb":        maxSizeKB,
				"preconditions":      originalGroup.Preconditions,
			}).Debug("Checking aggregate group preconditions")
		}

		if maxSizeKB > 0 && requestSizeKB > maxSizeKB {
			logrus.WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"request_size_kb": requestSizeKB,
				"max_size_kb":     maxSizeKB,
			}).Warn("Request size exceeds aggregate group precondition limit")
			response.Error(c, app_errors.NewAPIError(
				app_errors.ErrBadRequest,
				fmt.Sprintf("Request size (%d KB) exceeds aggregate group limit (%d KB)", requestSizeKB, maxSizeKB),
			))
			ps.logEarlyError(c, originalGroup, startTime, http.StatusBadRequest,
				fmt.Errorf("request size %d KB exceeds limit %d KB", requestSizeKB, maxSizeKB))
			return
		}
	}

	// Handle event logging batch endpoint interception.
	// For CC support (OpenAI): intercepts /claude/api/event_logging/batch
	// For Anthropic: intercepts /api/event_logging/batch when intercept_event_log is enabled
	// This must be checked before path rewriting since CC uses /claude/api/ path.
	if ps.handleEventLoggingBatch(c, group, bodyBytes) {
		return
	}

	// For GET requests (like /v1/models), skip body processing
	var finalBodyBytes []byte
	var isStream bool

	// Handle CC support path rewriting for all requests (including GET)
	// This must happen before body processing to ensure correct path for upstream
	wasClaudePath := isClaudePath(c.Request.URL.Path, group.Name)
	if isCCSupportEnabled(group) && wasClaudePath {
		originalPath := c.Request.URL.Path
		originalQuery := c.Request.URL.RawQuery

		// Use channel-specific path rewriting
		// Gemini uses /v1beta, others use /v1
		if group.ChannelType == "gemini" {
			c.Request.URL.Path = rewriteClaudePathToGemini(c.Request.URL.Path)
		} else {
			c.Request.URL.Path = rewriteClaudePathToOpenAIGeneric(c.Request.URL.Path)
		}

		// Sanitize query parameters for CC support (e.g., remove beta=true)
		// These are Claude-specific and should not be passed to OpenAI-style upstreams
		sanitizeCCQueryParams(c.Request.URL)
		c.Set("cc_was_claude_path", true)
		logrus.WithFields(logrus.Fields{
			"group":           group.Name,
			"channel_type":    group.ChannelType,
			"original_path":   originalPath,
			"new_path":        c.Request.URL.Path,
			"original_query":  originalQuery,
			"sanitized_query": c.Request.URL.RawQuery,
		}).Debug("CC support: rewritten Claude path for channel type and sanitized query params")
	}

	wasCodexPath := isCodexPath(c.Request.URL.Path, group.Name)
	if isCodexEndpointSupported(group) && wasCodexPath {
		originalPath := c.Request.URL.Path
		c.Request.URL.Path = rewriteCodexPathToOpenAIGeneric(c.Request.URL.Path)
		c.Set("codex_was_codex_path", true)
		logrus.WithFields(logrus.Fields{
			"group":         group.Name,
			"channel_type":  group.ChannelType,
			"original_path": originalPath,
			"new_path":      c.Request.URL.Path,
		}).Debug("Force Codex: rewritten Codex path for channel type")
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
			isStream = isGenericStreamRequest(c, finalBodyBytes)
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

			// Apply CC support: convert Claude requests to target format
			// Note: Path has already been rewritten from /claude/v1/messages to /v1/messages
			// We check for /v1/messages (after rewrite) and CC support enabled
			// Query params are already sanitized in the path rewriting block above (sanitizeCCQueryParams)
			// when wasClaudePath is true. Non-Claude paths don't need sanitization as they are
			// direct API calls that should preserve their original query parameters.
			// Also check /v1beta/messages for Gemini CC conversion (path may be rewritten to /v1beta/messages)
			if isCCSupportEnabled(group) && wasClaudePath && (strings.HasSuffix(c.Request.URL.Path, "/v1/messages") || strings.HasSuffix(c.Request.URL.Path, "/v1beta/messages")) {
				// Handle channel-specific CC support conversions
				switch group.ChannelType {
				case "openai-response":
					// Handle OpenAI Responses CC support (Claude -> Responses API)
					convertedBody, converted, ccErr := ps.applyCodexCCRequestConversion(c, group, finalBodyBytes)
					if ccErr != nil {
						logrus.WithError(ccErr).WithFields(logrus.Fields{
							"group": group.Name,
							"path":  c.Request.URL.Path,
						}).Error("Failed to convert Claude request to OpenAI Responses format")
						response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("OpenAI Responses CC conversion failed: %v", ccErr)))
						return
					} else if converted {
						finalBodyBytes = convertedBody
						// Re-apply param overrides after CC conversion to allow overriding
						// converted parameters (e.g., reasoning.effort for OpenAI Responses API).
						// This enables users to force specific values like {"reasoning": {"effort": "xhigh"}}.
						finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
						if err != nil {
							logrus.WithError(err).Warn("Failed to re-apply param overrides after OpenAI Responses CC conversion")
						}
						// Rewrite path from /v1/messages to /v1/responses for OpenAI Responses
						c.Request.URL.Path = strings.Replace(c.Request.URL.Path, "/v1/messages", "/v1/responses", 1)
						logrus.WithFields(logrus.Fields{
							"group":        group.Name,
							"channel_type": group.ChannelType,
							"new_path":     c.Request.URL.Path,
						}).Debug("OpenAI Responses CC support: converted Claude request to Responses format")
					}
				case "gemini":
					// Handle Gemini channel CC support (Claude -> Gemini API)
					convertedBody, converted, ccErr := ps.applyGeminiCCRequestConversion(c, group, finalBodyBytes)
					if ccErr != nil {
						logrus.WithError(ccErr).WithFields(logrus.Fields{
							"group": group.Name,
							"path":  c.Request.URL.Path,
						}).Error("Failed to convert Claude request to Gemini format")
						response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("Gemini CC conversion failed: %v", ccErr)))
						return
					} else if converted {
						finalBodyBytes = convertedBody
						// Re-apply param overrides after CC conversion
						finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
						if err != nil {
							logrus.WithError(err).Warn("Failed to re-apply param overrides after Gemini CC conversion")
						}
						// Rewrite path from /v1/messages to Gemini generateContent endpoint
						c.Request.URL.Path = ps.rewritePathForGeminiCC(c)
						logrus.WithFields(logrus.Fields{
							"group":        group.Name,
							"channel_type": group.ChannelType,
							"new_path":     c.Request.URL.Path,
						}).Debug("Gemini CC support: converted Claude request to Gemini format")
					}
				default:
					// Handle OpenAI channel CC support (Claude -> OpenAI Chat Completions)
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
						// Re-apply param overrides after CC conversion to allow overriding
						// converted parameters (e.g., reasoning_effort for OpenAI API).
						finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
						if err != nil {
							logrus.WithError(err).Warn("Failed to re-apply param overrides after OpenAI CC conversion")
						}
						// Rewrite path from /v1/messages to /v1/chat/completions
						c.Request.URL.Path = strings.Replace(c.Request.URL.Path, "/v1/messages", "/v1/chat/completions", 1)
						logrus.WithFields(logrus.Fields{
							"group":        group.Name,
							"channel_type": group.ChannelType,
							"new_path":     c.Request.URL.Path,
						}).Debug("CC support: converted Claude request to OpenAI format")
					}
				}
			}

			if group.ChannelType == "openai-response" && wasCodexPath && isOpenAIResponsesCodexEndpoint(c.Request.URL.Path) {
				c.Set("codex_was_codex_path", true)
				c.Set(ctxKeyCodexEnabled, true)
				setCodexUpstreamFormat(c, codexUpstreamResponses)
			}
			if isCodexSupportEnabled(group) && wasCodexPath && isOpenAIResponsesCodexEndpoint(c.Request.URL.Path) {
				convertedBody, converted, codexErr := ps.applyForceCodexRequestConversion(c, group, finalBodyBytes)
				if codexErr != nil {
					logrus.WithError(codexErr).WithFields(logrus.Fields{
						"group": group.Name,
						"path":  c.Request.URL.Path,
					}).Error("Failed to convert Codex request")
					response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("Codex conversion failed: %v", codexErr)))
					return
				} else if converted {
					finalBodyBytes = convertedBody
					finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
					if err != nil {
						logrus.WithError(err).Warn("Failed to re-apply param overrides after Codex conversion")
					}
					switch group.ChannelType {
					case "openai":
						c.Request.URL.Path = rewriteCodexResponsesPathToUpstream(c.Request.URL.Path, "/v1/chat/completions")
					case "anthropic":
						c.Request.URL.Path = rewriteCodexResponsesPathToUpstream(c.Request.URL.Path, "/v1/messages")
					}
					logrus.WithFields(logrus.Fields{
						"group":        group.Name,
						"channel_type": group.ChannelType,
						"new_path":     c.Request.URL.Path,
					}).Debug("Force Codex: converted Responses request to upstream format")
				}
			}

			// Apply parallel_tool_calls config for OpenAI channel when force_function_call is NOT enabled.
			// When force_function_call is enabled, native tools are removed and replaced with prompt-based
			// tool injection, so parallel_tool_calls is not applicable.
			// This must be applied before applyFunctionCallRequestRewrite to ensure the parameter is set
			// before tools are potentially removed.
			if group.ChannelType == "openai" && !isForceFunctionCallEnabled(group) && isChatCompletionsEndpoint(c.Request.URL.Path, c.Request.Method) {
				finalBodyBytes, err = ps.applyParallelToolCallsConfig(finalBodyBytes, group)
				if err != nil {
					logrus.WithError(err).Warn("Failed to apply parallel_tool_calls config")
				}
			}

			// Apply function call request rewrite for eligible channel endpoints.
			if isForceFunctionCallEnabled(group) && isFunctionCallRewriteEndpoint(group, c.Request.URL.Path, c.Request.Method) {
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

			if group.ChannelType == "gemini" {
				c.Request.URL.Path = applyGeminiNativeStreamPathOverride(
					c.Request.URL.Path,
					getGroupConfigBool(group, "force_stream"),
					getGroupConfigBool(group, "force_non_stream"),
				)
			}
			// Native Gemini selects streaming via endpoint suffix, not a JSON stream field.
			if group.ChannelType != "gemini" || !isGeminiNativeGenerateContentPath(c.Request.URL.Path) {
				finalBodyBytes, err = ps.applyStreamOverrideConfig(finalBodyBytes, group, allowsMissingStreamOverride(c.Request.URL.Path, c.Request.Method))
				if err != nil {
					logrus.WithError(err).Warn("Failed to apply stream override config")
				}
			}
			if group.ChannelType == "openai-response" && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
				finalBodyBytes, err = ps.applyResponsesIncludeConfig(finalBodyBytes, group)
				if err != nil {
					logrus.WithError(err).Warn("Failed to apply Responses include config")
				}
			}

			isStream = channelHandler.IsStreamRequest(c, finalBodyBytes)
			if codexDegradationMitigationShouldEnable(c, group, originalGroup, finalBodyBytes, isStream) {
				finalBodyBytes, err = prepareCodexDegradationMitigationInitialPayload(finalBodyBytes)
				if err != nil {
					logrus.WithError(err).Warn("Failed to prepare Codex degradation mitigation payload")
				} else {
					c.Set(ctxKeyCodexDegradationMitigation, true)
					isStream = channelHandler.IsStreamRequest(c, finalBodyBytes)
				}
			} else {
				c.Set(ctxKeyCodexDegradationMitigation, false)
			}

			// Apply forced streaming for direct OpenAI Responses requests (non-CC mode).
			// Codex-compatible upstreams require stream: true for reliable responses.
			// If client requests non-stream, we force stream: true to upstream and collect response.
			if group.ChannelType == "openai-response" && !isOpenAIResponseCCMode(c) &&
				!getGroupConfigBool(group, "force_non_stream") && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
				modifiedBody, wasNonStream := channel.ForceStreamRequest(finalBodyBytes)
				if wasNonStream {
					finalBodyBytes = modifiedBody
					c.Set(ctxKeyOpenAIResponseForcedStream, true)
					logrus.WithFields(logrus.Fields{
						"group":        group.Name,
						"channel_type": group.ChannelType,
						"path":         c.Request.URL.Path,
					}).Debug("Codex forced streaming: converted non-stream request to stream")
					// Keep isStream as false so response handler knows to collect and convert
				}
			}
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
	lifecycleCtx, lifecycleCancel := requestLifecycleContext(c.Request.Context(), cfg, isStream)
	defer lifecycleCancel()
	ps.executeRequestWithRetryLifecycle(c, channelHandler, originalGroup, group, bodyBytes, isStream, startTime, retryCount, lifecycleCtx)
}

func requestLifecycleContext(parent context.Context, cfg types.SystemSettings, isStream bool) (context.Context, context.CancelFunc) {
	return requestLifecycleContextAt(parent, cfg, isStream, time.Now())
}

func isGenericStreamRequest(c *gin.Context, bodyBytes []byte) bool {
	if strings.HasSuffix(c.Request.URL.Path, ":streamGenerateContent") {
		return true
	}

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

func requestLifecycleContextAt(parent context.Context, cfg types.SystemSettings, isStream bool, start time.Time) (context.Context, context.CancelFunc) {
	timeout := lifecycleTimeoutSeconds(cfg, isStream)
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, start.Add(time.Duration(timeout)*time.Second))
}

func (rc *retryContext) ensureLifecycleContext(parent context.Context, isStream bool) context.CancelFunc {
	if rc.lifecycleCtx != nil && rc.lifecycleStreamMode == isStream {
		return nil
	}
	if rc.lifecycleCancel != nil {
		rc.lifecycleCancel()
		rc.lifecycleCancel = nil
	}
	if rc.lifecycleStartTime.IsZero() {
		rc.lifecycleStartTime = time.Now()
	}
	rc.lifecycleCtx, rc.lifecycleCancel = requestLifecycleContextAt(parent, rc.lifecycleConfig, isStream, rc.lifecycleStartTime)
	rc.lifecycleStreamMode = isStream
	return rc.lifecycleCancel
}

func lifecycleTimeoutSeconds(cfg types.SystemSettings, isStream bool) int {
	if isStream {
		return cfg.StreamRequestTimeout
	}
	if cfg.NonStreamRequestTimeout > 0 {
		return cfg.NonStreamRequestTimeout
	}
	return cfg.RequestTimeout
}

func (ps *ProxyServer) aggregateRetryLifecycleConfig(originalGroup *models.Group) types.SystemSettings {
	cfg := originalGroup.EffectiveConfig
	nonStreamTimeout := lifecycleTimeoutSeconds(cfg, false)
	streamTimeout := lifecycleTimeoutSeconds(cfg, true)
	for _, relation := range originalGroup.SubGroups {
		if !relation.SubGroupEnabled {
			continue
		}
		subGroup, err := ps.groupManager.GetGroupByID(relation.SubGroupID)
		if err != nil {
			continue
		}
		subNonStreamTimeout := lifecycleTimeoutSeconds(subGroup.EffectiveConfig, false)
		if subNonStreamTimeout > 0 && (nonStreamTimeout <= 0 || subNonStreamTimeout < nonStreamTimeout) {
			nonStreamTimeout = subNonStreamTimeout
		}
		subStreamTimeout := lifecycleTimeoutSeconds(subGroup.EffectiveConfig, true)
		if subStreamTimeout > 0 && (streamTimeout <= 0 || subStreamTimeout < streamTimeout) {
			streamTimeout = subStreamTimeout
		}
	}
	if nonStreamTimeout > 0 {
		cfg.NonStreamRequestTimeout = nonStreamTimeout
		cfg.RequestTimeout = nonStreamTimeout
	}
	if streamTimeout > 0 {
		cfg.StreamRequestTimeout = streamTimeout
	}
	return cfg
}

func writeRetryLifecycleError(c *gin.Context, statusCode int, err error) {
	internalError := sanitizeInternalErrorMessage(err.Error())
	if isCCEnabled(c) {
		returnClaudeError(c, statusCode, internalError)
		return
	}
	response.Error(c, app_errors.NewAPIErrorWithUpstream(statusCode, "UPSTREAM_ERROR", internalError))
}

func retryLifecycleErrorStatus(ctx context.Context) (int, error) {
	ctxErr := ctx.Err()
	if ctxErr == nil {
		ctxErr = context.Canceled
	}
	// Retry sleeps share the client.Do lifecycle budget. Deadline expiry is a
	// server-side timeout; only parent request cancellation should be logged as 499.
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return http.StatusInternalServerError, ctxErr
	}
	return statusClientClosedRequest, ctxErr
}

func (ps *ProxyServer) executeRequestWithRetryLifecycle(
	c *gin.Context,
	channelHandler channel.ChannelProxy,
	originalGroup *models.Group,
	group *models.Group,
	bodyBytes []byte,
	isStream bool,
	startTime time.Time,
	retryCount int,
	lifecycleCtx context.Context,
) {
	cfg := group.EffectiveConfig

	// Store group in context for response handlers to access
	c.Set("group", group)
	if c.Keys != nil {
		delete(c.Keys, ctxKeyUpstreamUserAgent)
	}

	apiKey, err := ps.keyProvider.SelectKey(group.ID)
	if err != nil {
		logrus.Errorf("Failed to select a key for group %s on attempt %d: %v", group.Name, retryCount+1, err)
		response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, err.Error()))
		ps.logRequest(c, originalGroup, group, nil, startTime, http.StatusServiceUnavailable, err, isStream, "", nil, "", channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}

	// Select upstream with its dedicated HTTP clients
	upstreamSelection, err := channelHandler.SelectUpstreamWithClients(c.Request.URL, originalGroup.Name)
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, fmt.Sprintf("Failed to select upstream: %v", err)))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, fmt.Errorf("failed to select upstream: %v", err), isStream, "", nil, "", channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}
	if upstreamSelection == nil || upstreamSelection.URL == "" {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to select upstream: empty result"))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, errors.New("failed to select upstream: empty result"), isStream, "", nil, "", channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}

	req, err := http.NewRequestWithContext(lifecycleCtx, c.Request.Method, upstreamSelection.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		logrus.Errorf("Failed to create upstream request: %v", err)
		response.Error(c, app_errors.ErrInternalServer)
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, fmt.Errorf("failed to create request: %v", err), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}
	req.ContentLength = int64(len(bodyBytes))

	req.Header = c.Request.Header.Clone()

	// Clean up client auth headers
	utils.CleanClientAuthHeaders(req)

	// Apply anonymization: remove tracking and proxy-revealing headers
	utils.CleanAnonymizationHeaders(req)

	// Apply model redirection with index tracking for dynamic weight metrics
	// Skip for CC mode as redirection is already handled in CC conversion
	// This prevents strict mode errors when using Claude model names with CC
	finalBodyBytes := bodyBytes
	var originalModel string
	var targetIdx int = -1
	if !isCCEnabled(c) {
		var err error
		finalBodyBytes, originalModel, targetIdx, err = channelHandler.ApplyModelRedirectWithIndex(req, bodyBytes, group)
		if err != nil {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error()))
			ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusBadRequest, err, isStream, "", nil, "", channelHandler, bodyBytes, models.RequestTypeFinal)
			return
		}

		// Store original model and target index in context for logging and dynamic weight metrics
		if originalModel != "" {
			setModelRedirectContext(c, originalModel, targetIdx, true)
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
	}

	// Log request
	channelHandler.ModifyRequest(req, apiKey, group)

	// Apply custom header rules
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContextFromGin(c, group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	if rewrittenBody := applySimulatedClientHeaders(req, group, isStream); rewrittenBody != nil {
		bodyBytes = rewrittenBody
	}

	// Set headers for OpenAI Responses CC mode AFTER header rules to ensure upstream compatibility.
	// NOTE: This intentionally overrides any custom headers set by header rules.
	// Reason: some Responses upstreams validate Codex CLI-compatible headers.
	// IMPORTANT: These headers are ONLY set when CC mode is enabled (/claude path with cc_support=true).
	// Normal OpenAI Responses requests (non-CC) should use passthrough behavior (preserve client's original headers).
	// Model fetching sets UA separately in group_service.go FetchGroupModels().
	if isOpenAIResponseCCMode(c) {
		if rewrittenBody := applyCodexCompatibleHeaders(req, group, true); rewrittenBody != nil {
			bodyBytes = rewrittenBody
		}
		req.Header.Set("Connection", "Keep-Alive")
	}

	removeAcceptEncodingForProxyParsing(req, c, group)
	setUpstreamUserAgentForLog(c, group, req)

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
		logrus.Errorf("CRITICAL: upstreamSelection returned nil client for group %s, upstream %s", group.Name, utils.SanitizeRequestURLForLog(upstreamSelection.URL))
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Internal error: nil HTTP client"))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, errors.New("nil HTTP client"), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, channelHandler, bodyBytes, models.RequestTypeFinal)
		return
	}

	// Log which client is being used for debugging proxy issues.
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.WithFields(logrus.Fields{
			"group":     group.Name,
			"upstream":  utils.SanitizeRequestURLForLog(upstreamSelection.URL),
			"has_proxy": upstreamSelection.ProxyURL != nil && *upstreamSelection.ProxyURL != "",
			"proxy_url": safeProxyURL(upstreamSelection.ProxyURL),
			"is_stream": isStream,
		}).Debug("Using HTTP client for request")
	}

	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	setRateLimitPressureContextForAttempt(c, resp, time.Now())

	// Unified error handling for retries.
	if err != nil || (resp != nil && shouldFailoverOnStatusCode(resp.StatusCode, group)) {
		if ps.shouldAbortOnIgnorableError(c, err) {
			logrus.Debugf("Client-side ignorable error for key %s, aborting retries: %v", utils.MaskAPIKey(apiKey.KeyValue), err)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, 499, sanitizeInternalError(err), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, channelHandler, bodyBytes, models.RequestTypeFinal)
			return
		}

		var statusCode int
		var parsedError string
		var internalError string

		if err != nil {
			statusCode = 500
			parsedError = sanitizeInternalErrorMessage(err.Error())
			internalError = parsedError
			logrus.Debugf("Request failed (attempt %d/%d) for key %s: %s", retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), internalError)
		} else {
			// HTTP-level error (status >= 400)
			statusCode = resp.StatusCode
			// Limit error body read to a fixed size to prevent memory exhaustion
			errorBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBodySize))
			if readErr != nil {
				logrus.Errorf("Failed to read error body: %v", readErr)
				errorBody = []byte("Failed to read error body")
			}

			errorBody = decompressUpstreamErrorBody(resp, errorBody)
			_ = resp.Body.Close()
			resp.Body = http.NoBody

			// Store error response body in context for logging.
			// Per AI review: sanitize sensitive data before storing to prevent
			// accidental leakage of API keys, tokens, or PII in logs.
			// Use TruncateString for UTF-8 safe truncation.
			if len(errorBody) > 0 {
				sanitized := utils.SanitizeErrorBody(string(errorBody))
				c.Set("response_body", utils.TruncateString(sanitized, maxResponseCaptureBytes))
			}

			parsedError = app_errors.ParseUpstreamError(errorBody)
			internalError = sanitizeInternalErrorMessage(parsedError)
			logrus.Debugf("Request failed with status %d (attempt %d/%d) for key %s. Parsed Error: %s", statusCode, retryCount+1, cfg.MaxRetries, utils.MaskAPIKey(apiKey.KeyValue), internalError)
		}

		// Update key status with parsed error information
		ps.keyProvider.UpdateStatus(apiKey, group, false, internalError)

		// Check if this is the last retry attempt
		isLastAttempt := retryCount >= cfg.MaxRetries
		requestType := models.RequestTypeRetry
		if isLastAttempt {
			requestType = models.RequestTypeFinal
		}

		ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(internalError), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, channelHandler, bodyBytes, requestType)

		// If this is the last attempt, return error directly without recursion
		if isLastAttempt {
			// For CC mode (Claude Code), return Claude-formatted error response
			// to ensure the client can properly parse and display the error message.
			if isCCEnabled(c) {
				returnClaudeError(c, statusCode, internalError)
				return
			}
			response.Error(c, app_errors.NewAPIErrorWithUpstream(statusCode, "UPSTREAM_ERROR", internalError))
			return
		}

		if !waitBeforeRetry(lifecycleCtx, retryDelayForAttempt(cfg, retryCount)) {
			statusCode, ctxErr := retryLifecycleErrorStatus(lifecycleCtx)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, sanitizeInternalError(ctxErr), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, channelHandler, bodyBytes, models.RequestTypeFinal)
			writeRetryLifecycleError(c, statusCode, ctxErr)
			return
		}

		ps.executeRequestWithRetryLifecycle(c, channelHandler, originalGroup, group, bodyBytes, isStream, startTime, retryCount+1, lifecycleCtx)
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
			ccEnabled := isCCEnabled(c)
			codexCCMode := isOpenAIResponseCCMode(c)
			geminiCCMode := isGeminiCCMode(c)
			forceCodexMode := isCodexEnabled(c)
			logrus.WithFields(logrus.Fields{
				"cc_enabled":     ccEnabled,
				"codex_cc_mode":  codexCCMode,
				"gemini_cc_mode": geminiCCMode,
				"force_codex":    forceCodexMode,
				"is_stream":      isStream,
			}).Debug("Response handler selection")
			if ccEnabled {
				if codexCCMode {
					ps.handleCodexCCStreamingResponse(c, resp)
				} else if geminiCCMode {
					ps.handleGeminiCCStreamingResponse(c, resp)
				} else {
					ps.handleCCStreamingResponse(c, resp)
				}
			} else if forceCodexMode {
				ps.handleForceCodexStreamingResponse(c, resp)
			} else if codexDegradationMitigationEnabled(c) {
				codexMitigationRoundTrip := func(continuationBody []byte) (*http.Response, error) {
					continuationReq, err := codexMitigationRequestWithBody(req.Context(), req, continuationBody)
					if err != nil {
						return nil, err
					}
					return client.Do(continuationReq)
				}
				ps.handleCodexDegradationMitigationStreamingResponse(c, resp, bodyBytes, group, originalGroup, codexMitigationRoundTrip)
			} else if isFunctionCallEnabled(c) {
				ps.handleFunctionCallStreamingResponse(c, resp)
			} else {
				ps.handleStreamingResponse(c, resp)
			}
		} else {
			// For non-streaming chat completions with function call enabled, use
			// the function-call aware response handler.
			ccEnabled := isCCEnabled(c)
			codexCCMode := isOpenAIResponseCCMode(c)
			geminiCCMode := isGeminiCCMode(c)
			codexForcedStream := isOpenAIResponseForcedStream(c)
			forceCodexMode := isCodexEnabled(c)
			logrus.WithFields(logrus.Fields{
				"cc_enabled":          ccEnabled,
				"codex_cc_mode":       codexCCMode,
				"gemini_cc_mode":      geminiCCMode,
				"codex_forced_stream": codexForcedStream,
				"force_codex":         forceCodexMode,
				"is_stream":           isStream,
			}).Debug("Response handler selection")
			if ccEnabled {
				if codexCCMode {
					ps.handleCodexCCNormalResponse(c, resp)
				} else if geminiCCMode {
					ps.handleGeminiCCNormalResponse(c, resp)
				} else {
					ps.handleCCNormalResponse(c, resp)
				}
			} else if forceCodexMode {
				ps.handleForceCodexNormalResponse(c, resp)
			} else if codexForcedStream {
				// Codex forced streaming: collect stream response and return as non-stream
				ps.handleCodexForcedStreamResponse(c, resp)
			} else if isFunctionCallEnabled(c) {
				ps.handleFunctionCallNormalResponseByChannel(c, resp, group)
			} else {
				ps.handleNormalResponse(c, resp)
			}
		}
	}

	ps.logRequest(c, originalGroup, group, apiKey, startTime, resp.StatusCode, nil, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, channelHandler, bodyBytes, models.RequestTypeFinal)
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
	// The parent aggregate handler can be nil; all sub-group request operations
	// must use the subGroupChannelHandler selected below.
	// Restore original path for retry attempts to allow each sub-group to apply its own CC support
	// This is necessary because different sub-groups may have different CC support settings.
	restoreOriginalPath(c, retryCtx)
	clearForceProtocolContext(c)

	// Get max retries from aggregate group config
	maxRetries := parseMaxRetries(originalGroup.Config)

	if !retryCtx.lifecycleConfigSet {
		// Merge parent and enabled sub-group timeouts once; context creation waits
		// until the selected sub-group finalizes the effective stream mode.
		retryCtx.lifecycleConfig = ps.aggregateRetryLifecycleConfig(originalGroup)
		retryCtx.lifecycleConfigSet = true
		retryCtx.lifecycleStartTime = time.Now()
	}

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

	// Get sub-group key retry upper bound. This limits retries inside the selected
	// sub-group only; aggregate-level sub-group switches are controlled by max_retries.
	subMaxRetries, subMaxRetriesSet := parseSubMaxRetries(originalGroup.Config)
	codexAffinityEnabled := codexAggregateAffinityEnabled(c, originalGroup)
	codexAffinityMaxAttempts := parseCodexAffinityMaxAttempts(originalGroup.Config)

	logrus.WithFields(logrus.Fields{
		"aggregate_group":             originalGroup.Name,
		"max_retries":                 maxRetries,
		"sub_max_retries":             subMaxRetries,
		"sub_max_retries_set":         subMaxRetriesSet,
		"attempt_count":               retryCtx.attemptCount,
		"codex_affinity_attempt":      retryCtx.codexAffinityAttemptCount,
		"codex_affinity_max_attempts": codexAffinityMaxAttempts,
		"codex_affinity_degraded":     retryCtx.codexAffinityDegraded,
	}).Debug("Aggregate retry configuration")

	// Pre-check: if this is the first attempt, check if there are any valid sub-groups
	if retryCtx.attemptCount == 0 {
		availableCount := ps.countAvailableSubGroups(originalGroup, nil)
		if availableCount == 0 {
			// No valid sub-groups available, return error immediately without retry
			logrus.WithField("aggregate_group", originalGroup.Name).
				Warn("No valid sub-groups available, skipping retry")
			response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, "No valid sub-groups available"))
			ps.logRequest(c, originalGroup, originalGroup, nil, startTime, http.StatusServiceUnavailable,
				errors.New("no valid sub-groups"), isStream, "", nil, "", channelHandler, bodyBytes, models.RequestTypeFinal)
			return
		}
	}

	// Select sub-group with exclusion list support. Key-level retries are pinned
	// to the previously selected sub-group; aggregate-level retries still use
	// the weighted selector.
	subGroupName, subGroupID, forced := forcedAggregateSubGroup(originalGroup, retryCtx.forcedSubGroupID, retryCtx.excludedSubGroups)
	if forced {
		retryCtx.forcedSubGroupID = 0
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"selected_group":  subGroupName,
			"selected_id":     subGroupID,
		}).Debug("Reusing selected sub-group for key-level retry")
	} else {
		retryCtx.forcedSubGroupID = 0
		var err error
		if codexAffinityEnabled && !retryCtx.codexAffinityDegraded {
			affinityKey := codexAggregateAffinityThreadHeaderKey(c, originalGroup)
			if affinityKey == "" {
				payload, payloadOK := retryCtx.codexRequestPayload(bodyBytes)
				affinityKey = codexAggregateAffinityKeyFromPayload(c, originalGroup, payload, payloadOK)
			}
			retryCtx.codexAffinityKey = affinityKey
			if affinityKey != "" {
				model := retryCtx.codexRequestModel(bodyBytes)
				if retryCtx.codexAffinityCacheKey == "" {
					retryCtx.codexAffinityCacheKey = codexAggregateAffinityCacheKey(originalGroup.ID, affinityKey, model)
				}
				cacheKey := retryCtx.codexAffinityCacheKey
				if cachedSubGroupID, ok := ps.codexAffinityCache.get(cacheKey, time.Now()); ok {
					var cached bool
					subGroupName, subGroupID, cached = forcedAggregateSubGroup(originalGroup, cachedSubGroupID, retryCtx.excludedSubGroups)
					// Cache hits bypass the selector, so re-check the active-key list.
					// Do not cache this result; stale positives would reintroduce bad routing.
					if cached {
						retryCtx.codexAffinityPrimarySubGroupID = cachedSubGroupID
					}
					if cached && ps.subGroupManager.HasActiveKeys(cachedSubGroupID) {
						logrus.WithFields(logrus.Fields{
							"aggregate_group": originalGroup.Name,
							"selected_group":  subGroupName,
							"selected_id":     subGroupID,
							"model":           model,
						}).Debug("Selected Codex aggregate sub-group from affinity cache")
					} else {
						if cached {
							retryCtx.codexAffinityDegraded = true
						}
						subGroupName = ""
						subGroupID = 0
					}
				}
				if subGroupID == 0 {
					subGroupName, subGroupID, err = ps.subGroupManager.SelectSubGroupWithRetry(originalGroup, retryCtx.excludedSubGroups)
				}
			} else {
				subGroupName, subGroupID, err = ps.subGroupManager.SelectSubGroupWithRetry(originalGroup, retryCtx.excludedSubGroups)
			}
		} else {
			subGroupName, subGroupID, err = ps.subGroupManager.SelectSubGroupWithRetry(originalGroup, retryCtx.excludedSubGroups)
		}
		if err != nil {
			// All sub-groups are unavailable (runtime error)
			logrus.WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"error":           err,
				"excluded_count":  len(retryCtx.excludedSubGroups),
			}).Error("Failed to select sub-group from aggregate")
			response.Error(c, app_errors.NewAPIError(app_errors.ErrNoKeysAvailable, "No available sub-groups"))
			ps.logEarlyError(c, originalGroup, startTime, http.StatusServiceUnavailable, fmt.Errorf("no available sub-groups: %v", err))
			return
		}
	}
	if codexAffinityEnabled &&
		subGroupID != 0 && retryCtx.codexAffinityPrimarySubGroupID == 0 {
		// Simulated Codex identity is generated after routing, so track the first
		// actual target even when the inbound request has no affinity identifier.
		retryCtx.codexAffinityPrimarySubGroupID = subGroupID
	}

	// Get the selected sub-group
	group, err := ps.groupManager.GetGroupByName(subGroupName)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		ps.logEarlyError(c, originalGroup, startTime, http.StatusNotFound, fmt.Errorf("sub-group not found: %s", subGroupName))
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
		ps.logEarlyError(c, group, startTime, http.StatusInternalServerError, fmt.Errorf("failed to get channel: %v", err))
		return
	}

	// Store current sub-group ID for failure handling
	c.Set("current_sub_group_id", subGroupID)
	codexAffinityFallback := retryCtx.codexAffinityDegraded
	clearModelRedirectContext(c)

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

	// Handle event logging batch endpoint interception for aggregate sub-group.
	// For CC support (OpenAI): intercepts /claude/api/event_logging/batch
	// For Anthropic: intercepts /api/event_logging/batch when intercept_event_log is enabled
	if ps.handleEventLoggingBatch(c, group, finalBodyBytes) {
		return
	}

	// Apply CC support for eligible OpenAI sub-groups.
	// Clear any stale CC state from previous sub-group attempts.
	c.Set(ctxKeyCCEnabled, false)
	c.Set(ctxKeyCodexEnabled, false)
	c.Set(ctxKeyCodexUpstreamFormat, "")
	// Use originalGroup.Name for path check since request path is /proxy/{aggregate_group}/claude/v1/...
	wasClaudePath := isClaudePath(c.Request.URL.Path, originalGroup.Name)
	wasCodexPath := isCodexPath(c.Request.URL.Path, originalGroup.Name)

	// Handle CC support path rewriting for sub-groups
	// This rewrites /claude/ paths to standard OpenAI paths. For groups named "claude",
	// OpenAI-style paths like /proxy/claude/v1/messages are not treated as CC paths.
	if isCCSupportEnabled(group) && wasClaudePath {
		originalPath := c.Request.URL.Path
		originalQuery := c.Request.URL.RawQuery

		// Use channel-specific path rewriting
		// Gemini uses /v1beta, others use /v1
		if group.ChannelType == "gemini" {
			c.Request.URL.Path = rewriteClaudePathToGemini(c.Request.URL.Path)
		} else {
			c.Request.URL.Path = rewriteClaudePathToOpenAIGeneric(c.Request.URL.Path)
		}

		// Sanitize query parameters for CC support (e.g., remove beta=true)
		// These are Claude-specific and should not be passed to OpenAI-style upstreams
		sanitizeCCQueryParams(c.Request.URL)
		c.Set("cc_was_claude_path", true)
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"sub_group":       group.Name,
			"channel_type":    group.ChannelType,
			"original_path":   originalPath,
			"new_path":        c.Request.URL.Path,
			"original_query":  originalQuery,
			"sanitized_query": c.Request.URL.RawQuery,
		}).Debug("CC support: rewritten Claude path for sub-group channel type and sanitized query params")
	}

	if isCodexEndpointSupported(group) && wasCodexPath {
		originalPath := c.Request.URL.Path
		c.Request.URL.Path = rewriteCodexPathToOpenAIGeneric(c.Request.URL.Path)
		c.Set("codex_was_codex_path", true)
		logrus.WithFields(logrus.Fields{
			"aggregate_group": originalGroup.Name,
			"sub_group":       group.Name,
			"channel_type":    group.ChannelType,
			"original_path":   originalPath,
			"new_path":        c.Request.URL.Path,
		}).Debug("Force Codex: rewritten Codex path for sub-group channel type")
	}

	// Convert Claude messages request to target format (OpenAI, OpenAI Responses, or Gemini)
	// Note: Path has already been rewritten from /claude/v1/messages to /v1/messages (or /v1beta/messages for Gemini)
	// Clear any stale OpenAI Responses CC state from previous sub-group attempts.
	c.Set(ctxKeyOpenAIResponseCC, false)
	c.Set(ctxKeyGeminiCC, false)
	// Check for both /v1/messages (OpenAI, OpenAI Responses, Anthropic) and /v1beta/messages (Gemini)
	isMessagesEndpoint := strings.HasSuffix(c.Request.URL.Path, "/v1/messages") ||
		strings.HasSuffix(c.Request.URL.Path, "/v1beta/messages")
	shouldConvertCCForSubGroup := isCCSupportEnabled(group) && isMessagesEndpoint &&
		(wasClaudePath || originalGroup.ChannelType == "anthropic")
	if shouldConvertCCForSubGroup {
		// Handle channel-specific CC support conversions
		switch group.ChannelType {
		case "openai-response":
			// Handle OpenAI Responses CC support (Claude -> Responses API)
			// Sanitize query parameters for Responses CC (remove Claude-specific params like beta=true)
			// This is needed even if path wasn't /claude/ since Anthropic aggregate may send directly to /v1/messages
			sanitizeCCQueryParams(c.Request.URL)

			// Debug log: input body before conversion
			// Only log body preview when EnableRequestBodyLogging is enabled to avoid leaking sensitive data
			logFields := logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"path":            c.Request.URL.Path,
				"input_body_len":  len(finalBodyBytes),
			}
			if group.EffectiveConfig.EnableRequestBodyLogging {
				// Per AI review: use TruncateString for UTF-8 safe truncation and SanitizeErrorBody
				// to prevent leaking secrets/PII. Sanitize first, then truncate.
				inputPreview := utils.TruncateString(utils.SanitizeErrorBody(string(finalBodyBytes)), 1000)
				logFields["input_body_preview"] = inputPreview
			}
			logrus.WithFields(logFields).Debug("OpenAI Responses CC: Starting conversion for aggregate sub-group")

			convertedBody, converted, ccErr := ps.applyCodexCCRequestConversion(c, group, finalBodyBytes)
			if ccErr != nil {
				logrus.WithError(ccErr).WithFields(logrus.Fields{
					"aggregate_group": originalGroup.Name,
					"sub_group":       group.Name,
					"path":            c.Request.URL.Path,
				}).Error("Failed to convert Claude request to OpenAI Responses format for sub-group")
				response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("OpenAI Responses CC conversion failed: %v", ccErr)))
				return
			} else if converted {
				// Debug log: output body after conversion
				// Only log body preview when EnableRequestBodyLogging is enabled to avoid leaking sensitive data
				outFields := logrus.Fields{
					"aggregate_group": originalGroup.Name,
					"sub_group":       group.Name,
					"output_body_len": len(convertedBody),
				}
				if group.EffectiveConfig.EnableRequestBodyLogging {
					// Per AI review: use TruncateString for UTF-8 safe truncation and SanitizeErrorBody
					// to prevent leaking secrets/PII. Sanitize first, then truncate.
					outputPreview := utils.TruncateString(utils.SanitizeErrorBody(string(convertedBody)), 1000)
					outFields["output_body_preview"] = outputPreview
				}
				logrus.WithFields(outFields).Debug("OpenAI Responses CC: Conversion completed for aggregate sub-group")

				finalBodyBytes = convertedBody
				// Re-apply param overrides after CC conversion to allow overriding
				// converted parameters (e.g., reasoning.effort for OpenAI Responses API).
				finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
				if err != nil {
					logrus.WithError(err).Warn("Failed to re-apply param overrides after OpenAI Responses CC conversion for sub-group")
				}
				// Rewrite path from /v1/messages to /v1/responses for OpenAI Responses
				c.Request.URL.Path = strings.Replace(c.Request.URL.Path, "/v1/messages", "/v1/responses", 1)
				logrus.WithFields(logrus.Fields{
					"aggregate_group": originalGroup.Name,
					"sub_group":       group.Name,
					"channel_type":    group.ChannelType,
					"new_path":        c.Request.URL.Path,
				}).Debug("OpenAI Responses CC support: converted Claude request for sub-group")
			}
		case "gemini":
			// Handle Gemini channel CC support (Claude -> Gemini API)
			sanitizeCCQueryParams(c.Request.URL)

			convertedBody, converted, ccErr := ps.applyGeminiCCRequestConversion(c, group, finalBodyBytes)
			if ccErr != nil {
				logrus.WithError(ccErr).WithFields(logrus.Fields{
					"aggregate_group": originalGroup.Name,
					"sub_group":       group.Name,
					"path":            c.Request.URL.Path,
				}).Error("Failed to convert Claude request to Gemini format for sub-group")
				response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("Gemini CC conversion failed: %v", ccErr)))
				return
			} else if converted {
				finalBodyBytes = convertedBody
				// Re-apply param overrides after CC conversion
				finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
				if err != nil {
					logrus.WithError(err).Warn("Failed to re-apply param overrides after Gemini CC conversion for sub-group")
				}
				// Rewrite path from /v1/messages to Gemini generateContent endpoint
				c.Request.URL.Path = ps.rewritePathForGeminiCC(c)
				logrus.WithFields(logrus.Fields{
					"aggregate_group": originalGroup.Name,
					"sub_group":       group.Name,
					"channel_type":    group.ChannelType,
					"new_path":        c.Request.URL.Path,
				}).Debug("Gemini CC support: converted Claude request for sub-group")
			}
		default:
			// Handle OpenAI channel CC support (Claude -> OpenAI Chat Completions)
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
				// Re-apply param overrides after CC conversion to allow overriding
				// converted parameters (e.g., reasoning_effort for OpenAI API).
				finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
				if err != nil {
					logrus.WithError(err).Warn("Failed to re-apply param overrides after OpenAI CC conversion for sub-group")
				}
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
	}

	shouldUseCodexEndpointForSubGroup := isCodexEndpointSupported(group) &&
		(wasCodexPath || originalGroup.ChannelType == "openai-response")
	if group.ChannelType == "openai-response" && shouldUseCodexEndpointForSubGroup && wasCodexPath && isOpenAIResponsesCodexEndpoint(c.Request.URL.Path) {
		c.Set("codex_was_codex_path", true)
		c.Set(ctxKeyCodexEnabled, true)
		setCodexUpstreamFormat(c, codexUpstreamResponses)
	}
	if isCodexSupportEnabled(group) && shouldUseCodexEndpointForSubGroup && isOpenAIResponsesCodexEndpoint(c.Request.URL.Path) {
		convertedBody, converted, codexErr := ps.applyForceCodexRequestConversion(c, group, finalBodyBytes)
		if codexErr != nil {
			logrus.WithError(codexErr).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"path":            c.Request.URL.Path,
			}).Error("Failed to convert Codex request for sub-group")
			response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("Codex conversion failed: %v", codexErr)))
			return
		} else if converted {
			finalBodyBytes = convertedBody
			finalBodyBytes, err = ps.applyParamOverrides(finalBodyBytes, group)
			if err != nil {
				logrus.WithError(err).Warn("Failed to re-apply param overrides after Codex conversion for sub-group")
			}
			switch group.ChannelType {
			case "openai":
				c.Request.URL.Path = rewriteCodexResponsesPathToUpstream(c.Request.URL.Path, "/v1/chat/completions")
			case "anthropic":
				c.Request.URL.Path = rewriteCodexResponsesPathToUpstream(c.Request.URL.Path, "/v1/messages")
			}
			logrus.WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"channel_type":    group.ChannelType,
				"new_path":        c.Request.URL.Path,
			}).Debug("Force Codex: converted Responses request for sub-group")
		}
	}

	// Apply parallel_tool_calls config for OpenAI sub-groups when force_function_call is NOT enabled.
	// This mirrors the behavior in the main HandleProxy path for standard groups.
	// When force_function_call is enabled, native tools are removed and replaced with prompt-based
	// tool injection, so parallel_tool_calls is not applicable.
	if group.ChannelType == "openai" && !isForceFunctionCallEnabled(group) && isChatCompletionsEndpoint(c.Request.URL.Path, c.Request.Method) {
		finalBodyBytes, err = ps.applyParallelToolCallsConfig(finalBodyBytes, group)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
			}).Warn("Failed to apply parallel_tool_calls config for sub-group")
		}
	}

	// Apply function call request rewrite for eligible sub-group endpoints.
	// Clear any stale function call state from previous sub-group attempts
	// so that downstream response handlers do not see outdated flags.
	c.Set(ctxKeyFunctionCallEnabled, false)
	c.Set(ctxKeyTriggerSignal, "")
	if isForceFunctionCallEnabled(group) && isFunctionCallRewriteEndpoint(group, c.Request.URL.Path, c.Request.Method) {
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

	if group.ChannelType == "gemini" {
		c.Request.URL.Path = applyGeminiNativeStreamPathOverride(
			c.Request.URL.Path,
			getGroupConfigBool(group, "force_stream"),
			getGroupConfigBool(group, "force_non_stream"),
		)
	}
	// Native Gemini selects streaming via endpoint suffix, not a JSON stream field.
	if group.ChannelType != "gemini" || !isGeminiNativeGenerateContentPath(c.Request.URL.Path) {
		finalBodyBytes, err = ps.applyStreamOverrideConfig(finalBodyBytes, group, allowsMissingStreamOverride(c.Request.URL.Path, c.Request.Method))
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
			}).Warn("Failed to apply stream override config for sub-group")
		}
	}
	if group.ChannelType == "openai-response" && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
		finalBodyBytes, err = ps.applyResponsesIncludeConfig(finalBodyBytes, group)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
			}).Warn("Failed to apply Responses include config for sub-group")
		}
	}
	isStream = subGroupChannelHandler.IsStreamRequest(c, finalBodyBytes)
	if codexDegradationMitigationShouldEnable(c, group, originalGroup, finalBodyBytes, isStream) {
		finalBodyBytes, err = prepareCodexDegradationMitigationInitialPayload(finalBodyBytes)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
			}).Warn("Failed to prepare Codex degradation mitigation payload for sub-group")
		} else {
			c.Set(ctxKeyCodexDegradationMitigation, true)
			isStream = subGroupChannelHandler.IsStreamRequest(c, finalBodyBytes)
		}
	} else {
		c.Set(ctxKeyCodexDegradationMitigation, false)
	}
	if codexAffinityFallback {
		c.Set(ctxKeyCodexDegradationMitigation, false)
		// Do not treat every invalid_responses_request as encrypted-reasoning incompatibility:
		// that code also covers malformed tools and other schema errors, so only strip
		// Responses encrypted reasoning after affinity retry has failed over to another sub-group.
		strippedBody, stripped, stripErr := stripCodexAffinityFallbackEncryptedReasoning(finalBodyBytes)
		if stripErr != nil {
			logrus.WithError(stripErr).WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
			}).Warn("Failed to strip encrypted reasoning for Codex affinity fallback sub-group")
			response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, "Invalid request body for Codex affinity fallback"))
			ps.logEarlyError(c, originalGroup, startTime, http.StatusBadRequest, errors.New("invalid JSON request body for Codex affinity fallback"))
			return
		} else if stripped {
			finalBodyBytes = strippedBody
			logrus.WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"primary_id":      retryCtx.codexAffinityPrimarySubGroupID,
			}).Debug("Stripped encrypted reasoning for Codex affinity fallback sub-group")
		}
	}

	// Apply forced streaming for direct OpenAI Responses sub-group requests (non-CC mode).
	// Clear any stale forced stream state from previous sub-group attempts.
	c.Set(ctxKeyOpenAIResponseForcedStream, false)
	if group.ChannelType == "openai-response" && !isOpenAIResponseCCMode(c) &&
		!getGroupConfigBool(group, "force_non_stream") && isOpenAIResponsesEndpoint(c.Request.URL.Path) {
		modifiedBody, wasNonStream := channel.ForceStreamRequest(finalBodyBytes)
		if wasNonStream {
			finalBodyBytes = modifiedBody
			c.Set(ctxKeyOpenAIResponseForcedStream, true)
			logrus.WithFields(logrus.Fields{
				"aggregate_group": originalGroup.Name,
				"sub_group":       group.Name,
				"channel_type":    group.ChannelType,
				"path":            c.Request.URL.Path,
			}).Debug("Codex forced streaming: converted non-stream request to stream for sub-group")
			// Keep isStream as false so response handler knows to collect and convert
		}
	}

	if lifecycleCancel := retryCtx.ensureLifecycleContext(c.Request.Context(), isStream); lifecycleCancel != nil {
		defer lifecycleCancel()
	}

	// Store group in context for response handlers to access
	c.Set("group", group)
	if c.Keys != nil {
		delete(c.Keys, ctxKeyUpstreamUserAgent)
	}

	apiKey, err := ps.keyProvider.SelectKey(group.ID)
	if err != nil {
		logrus.Errorf("Failed to select a key for group %s on attempt %d: %v", group.Name, retryCtx.attemptCount+1, err)
		if codexAffinityEnabled && !retryCtx.codexAffinityDegraded &&
			retryCtx.codexAffinityPrimarySubGroupID == subGroupID {
			// This is not a primary upstream attempt: SelectKey failed before client.Do,
			// so no affinity attempt is consumed. Degrade now so fallback strips reasoning.
			retryCtx.codexAffinityDegraded = true
		}

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
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, fmt.Errorf("failed to select upstream: %v", err), isStream, "", nil, "", subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
		return
	}
	if upstreamSelection == nil || upstreamSelection.URL == "" {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to select upstream: empty result"))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, errors.New("failed to select upstream: empty result"), isStream, "", nil, "", subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
		return
	}

	req, err := http.NewRequestWithContext(retryCtx.lifecycleCtx, c.Request.Method, upstreamSelection.URL, bytes.NewReader(finalBodyBytes))
	if err != nil {
		logrus.Errorf("Failed to create upstream request: %v", err)
		response.Error(c, app_errors.ErrInternalServer)
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, fmt.Errorf("failed to create request: %v", err), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
		return
	}
	req.ContentLength = int64(len(finalBodyBytes))

	req.Header = c.Request.Header.Clone()

	// Clean up client auth headers
	utils.CleanClientAuthHeaders(req)

	// Apply anonymization: remove tracking and proxy-revealing headers
	utils.CleanAnonymizationHeaders(req)

	// Apply model redirection for aggregate sub-group with index tracking for dynamic weight metrics
	// Skip for CC mode as redirection is already handled in CC conversion
	// This prevents strict mode errors when using Claude model names with CC
	redirectedBody := finalBodyBytes
	var redirectOriginalModel string
	var targetIdx int = -1
	if !isCCEnabled(c) {
		var err error
		redirectedBody, redirectOriginalModel, targetIdx, err = subGroupChannelHandler.ApplyModelRedirectWithIndex(req, finalBodyBytes, group)
		if err != nil {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error()))
			ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusBadRequest, err, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
			return
		}

		// Store original model and target index in context for logging and dynamic weight metrics
		// Only update if not already set by model mapping
		if redirectOriginalModel != "" {
			setModelRedirectContext(c, redirectOriginalModel, targetIdx, true)
		}

		if !bytes.Equal(redirectedBody, finalBodyBytes) {
			finalBodyBytes = redirectedBody
			req.Body = io.NopCloser(bytes.NewReader(finalBodyBytes))
			req.ContentLength = int64(len(finalBodyBytes))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(finalBodyBytes)), nil
			}
		}
	}

	subGroupChannelHandler.ModifyRequest(req, apiKey, group)

	// Apply custom header rules
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContextFromGin(c, group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	if rewrittenBody := applySimulatedClientHeaders(req, group, isStream); rewrittenBody != nil {
		finalBodyBytes = rewrittenBody
	}

	// Set headers for OpenAI Responses CC mode AFTER header rules to ensure upstream compatibility.
	// NOTE: This intentionally overrides any custom headers set by header rules.
	// Reason: some Responses upstreams validate Codex CLI-compatible headers.
	// IMPORTANT: These headers are ONLY set when CC mode is enabled (/claude path with cc_support=true).
	// Normal OpenAI Responses requests (non-CC) should use passthrough behavior (preserve client's original headers).
	// Model fetching sets UA separately in group_service.go FetchGroupModels().
	if isOpenAIResponseCCMode(c) {
		if rewrittenBody := applyCodexCompatibleHeaders(req, group, true); rewrittenBody != nil {
			finalBodyBytes = rewrittenBody
		}
		req.Header.Set("Connection", "Keep-Alive")
	}

	removeAcceptEncodingForProxyParsing(req, c, group)
	setUpstreamUserAgentForLog(c, group, req)

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
		logrus.Errorf("CRITICAL: upstreamSelection returned nil client for sub-group %s, upstream %s", group.Name, utils.SanitizeRequestURLForLog(upstreamSelection.URL))
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Internal error: nil HTTP client"))
		ps.logRequest(c, originalGroup, group, apiKey, startTime, http.StatusInternalServerError, errors.New("nil HTTP client"), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
		return
	}

	// Log which client is being used for debugging proxy issues
	logrus.WithFields(logrus.Fields{
		"group":     group.Name,
		"upstream":  utils.SanitizeRequestURLForLog(upstreamSelection.URL),
		"has_proxy": upstreamSelection.ProxyURL != nil && *upstreamSelection.ProxyURL != "",
		"proxy_url": safeProxyURL(upstreamSelection.ProxyURL),
		"is_stream": isStream,
	}).Debug("Using HTTP client for aggregate sub-group request")

	isCodexAffinityPrimaryAttempt := codexAffinityEnabled &&
		!retryCtx.codexAffinityDegraded &&
		retryCtx.codexAffinityPrimarySubGroupID == subGroupID
	if isCodexAffinityPrimaryAttempt {
		// Count only attempts that reach the actual upstream client call.
		retryCtx.codexAffinityAttemptCount++
	}
	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	setRateLimitPressureContextForAttempt(c, resp, time.Now())

	// Unified error handling for retries.
	if err != nil || (resp != nil && shouldFailoverOnStatusCode(resp.StatusCode, group)) {
		if ps.shouldAbortOnIgnorableError(c, err) {
			logrus.Debugf("Client-side ignorable error for key %s, aborting retries: %v", utils.MaskAPIKey(apiKey.KeyValue), err)
			ps.logRequest(c, originalGroup, group, apiKey, startTime, 499, sanitizeInternalError(err), isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
			return
		}

		var statusCode int
		var parsedError string
		var internalError string

		if err != nil {
			statusCode = 500
			parsedError = sanitizeInternalErrorMessage(err.Error())
			internalError = parsedError
			logrus.Debugf("Request failed (attempt %d/%d) for key %s: %s", retryCtx.attemptCount+1, maxRetries, utils.MaskAPIKey(apiKey.KeyValue), internalError)
		} else {
			// HTTP-level error (status >= 400)
			statusCode = resp.StatusCode
			// Limit error body read to a fixed size to prevent memory exhaustion
			errorBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBodySize))
			if readErr != nil {
				logrus.Errorf("Failed to read error body: %v", readErr)
				errorBody = []byte("Failed to read error body")
			}

			errorBody = decompressUpstreamErrorBody(resp, errorBody)
			_ = resp.Body.Close()
			resp.Body = http.NoBody

			// Store sanitized error response body in context for logging.
			// Per AI review: sanitize to prevent leaking secrets/PII in logs.
			// Use TruncateString for UTF-8 safe truncation.
			if len(errorBody) > 0 {
				sanitized := utils.SanitizeErrorBody(string(errorBody))
				c.Set("response_body", utils.TruncateString(sanitized, maxResponseCaptureBytes))
			}

			parsedError = app_errors.ParseUpstreamError(errorBody)
			internalError = sanitizeInternalErrorMessage(parsedError)
			logrus.Debugf("Request failed with status %d (attempt %d/%d) for key %s. Parsed Error: %s", statusCode, retryCtx.attemptCount+1, maxRetries, utils.MaskAPIKey(apiKey.KeyValue), internalError)
		}

		// Update key status
		ps.keyProvider.UpdateStatus(apiKey, group, false, internalError)

		if isCodexAffinityPrimaryAttempt {
			if retryCtx.codexAffinityAttemptCount < codexAffinityMaxAttempts {
				ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(internalError), isStream,
					upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeRetry)

				logrus.WithFields(logrus.Fields{
					"aggregate_group":             originalGroup.Name,
					"sub_group":                   group.Name,
					"sub_group_id":                subGroupID,
					"codex_affinity_attempt":      retryCtx.codexAffinityAttemptCount,
					"codex_affinity_max_attempts": codexAffinityMaxAttempts,
				}).Debug("Retrying Codex affinity primary sub-group")

				if !waitBeforeRetry(retryCtx.lifecycleCtx, retryDelayForAttempt(group.EffectiveConfig, retryCtx.codexAffinityAttemptCount-1)) {
					statusCode, ctxErr := retryLifecycleErrorStatus(retryCtx.lifecycleCtx)
					ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, sanitizeInternalError(ctxErr), isStream,
						upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
					writeRetryLifecycleError(c, statusCode, ctxErr)
					return
				}

				retryCtx.forcedSubGroupID = subGroupID
				ps.executeRequestWithAggregateRetry(c, channelHandler, originalGroup, retryCtx.originalBodyBytes, isStream, startTime, retryCtx)
				return
			}

			retryCtx.codexAffinityDegraded = true
			logrus.WithFields(logrus.Fields{
				"aggregate_group":             originalGroup.Name,
				"sub_group":                   group.Name,
				"sub_group_id":                subGroupID,
				"codex_affinity_attempt":      retryCtx.codexAffinityAttemptCount,
				"codex_affinity_max_attempts": codexAffinityMaxAttempts,
			}).Debug("Codex affinity attempts exhausted, applying aggregate failover budget")

			ps.handleAggregateSubGroupFailure(c, subGroupChannelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, retryCtx, maxRetries, statusCode, errors.New(internalError), apiKey)
			return
		}

		// Check sub-group's key retry limit
		subGroupCfg := group.EffectiveConfig
		subGroupKeyRetryCount := retryCtx.subGroupKeyRetryMap[subGroupID]
		subGroupMaxRetries := subGroupKeyMaxRetries(subGroupCfg, subMaxRetries, subMaxRetriesSet)

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
			ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, errors.New(internalError), isStream,
				upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeRetry)

			logrus.WithFields(logrus.Fields{
				"sub_group":             group.Name,
				"sub_group_key_retry":   subGroupKeyRetryCount + 1,
				"sub_group_max_retries": subGroupMaxRetries,
			}).Debug("Retrying with another key in the same sub-group")

			// Note: we intentionally do not exclude the previously failed key at this
			// layer. Key-level health and blacklisting are handled centrally by
			// KeyProvider.UpdateStatus and the underlying store.Rotate logic. The
			// per-request retry here simply gives the rotation logic another chance
			// to pick a different healthy key when available, while keeping
			// semantics consistent with non-aggregate retry paths.

			// Restore original path for retry (CC support may have modified it)
			restoreOriginalPath(c, retryCtx)

			if !waitBeforeRetry(retryCtx.lifecycleCtx, retryDelayForAttempt(subGroupCfg, subGroupKeyRetryCount)) {
				statusCode, ctxErr := retryLifecycleErrorStatus(retryCtx.lifecycleCtx)
				ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, sanitizeInternalError(ctxErr), isStream,
					upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
				writeRetryLifecycleError(c, statusCode, ctxErr)
				return
			}

			// Retry with same sub-group but different key (SelectKey will choose a different one)
			retryCtx.forcedSubGroupID = subGroupID
			ps.executeRequestWithAggregateRetry(c, channelHandler, originalGroup, retryCtx.originalBodyBytes, isStream, startTime, retryCtx)
			return
		}

		// Sub-group key retries exhausted, handle aggregate-level retry (switch to next sub-group)
		logrus.WithFields(logrus.Fields{
			"sub_group":             group.Name,
			"sub_group_key_retries": subGroupKeyRetryCount,
			"sub_group_max_retries": subGroupMaxRetries,
		}).Debug("Sub-group key retries exhausted, switching to next sub-group")

		ps.handleAggregateSubGroupFailure(c, subGroupChannelHandler, originalGroup, group, finalBodyBytes, isStream, startTime, retryCtx, maxRetries, statusCode, errors.New(internalError), apiKey)
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
			ccEnabled := isCCEnabled(c)
			codexCCMode := isOpenAIResponseCCMode(c)
			geminiCCMode := isGeminiCCMode(c)
			forceCodexMode := isCodexEnabled(c)
			logrus.WithFields(logrus.Fields{
				"cc_enabled":     ccEnabled,
				"codex_cc_mode":  codexCCMode,
				"gemini_cc_mode": geminiCCMode,
				"force_codex":    forceCodexMode,
				"is_stream":      isStream,
			}).Debug("Aggregate response handler selection")
			if ccEnabled {
				if codexCCMode {
					ps.handleCodexCCStreamingResponse(c, resp)
				} else if geminiCCMode {
					ps.handleGeminiCCStreamingResponse(c, resp)
				} else {
					ps.handleCCStreamingResponse(c, resp)
				}
			} else if forceCodexMode {
				ps.handleForceCodexStreamingResponse(c, resp)
			} else if codexDegradationMitigationEnabled(c) {
				codexMitigationRoundTrip := func(continuationBody []byte) (*http.Response, error) {
					continuationReq, err := codexMitigationRequestWithBody(req.Context(), req, continuationBody)
					if err != nil {
						return nil, err
					}
					return client.Do(continuationReq)
				}
				ps.handleCodexDegradationMitigationStreamingResponse(c, resp, finalBodyBytes, group, originalGroup, codexMitigationRoundTrip)
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
				"strict_mode":         group.ModelRedirectStrict,
			}).Debug("Detected /models endpoint with model mapping, applying enhancement")
			ps.handleModelsResponse(c, resp, group, subGroupChannelHandler)
		} else {
			ccEnabled := isCCEnabled(c)
			codexCCMode := isOpenAIResponseCCMode(c)
			geminiCCMode := isGeminiCCMode(c)
			codexForcedStream := isOpenAIResponseForcedStream(c)
			forceCodexMode := isCodexEnabled(c)
			logrus.WithFields(logrus.Fields{
				"cc_enabled":          ccEnabled,
				"codex_cc_mode":       codexCCMode,
				"gemini_cc_mode":      geminiCCMode,
				"codex_forced_stream": codexForcedStream,
				"force_codex":         forceCodexMode,
				"is_stream":           isStream,
			}).Debug("Aggregate response handler selection")
			if ccEnabled {
				if codexCCMode {
					ps.handleCodexCCNormalResponse(c, resp)
				} else if geminiCCMode {
					ps.handleGeminiCCNormalResponse(c, resp)
				} else {
					ps.handleCCNormalResponse(c, resp)
				}
			} else if forceCodexMode {
				ps.handleForceCodexNormalResponse(c, resp)
			} else if codexForcedStream {
				// Codex forced streaming: collect stream response and return as non-stream
				ps.handleCodexForcedStreamResponse(c, resp)
			} else if isFunctionCallEnabled(c) {
				ps.handleFunctionCallNormalResponseByChannel(c, resp, group)
			} else {
				ps.handleNormalResponse(c, resp)
			}
		}
	}

	ps.logRequest(c, originalGroup, group, apiKey, startTime, resp.StatusCode, nil, isStream, upstreamSelection.URL, upstreamSelection.ProxyURL, upstreamSelection.GatewayProxy, subGroupChannelHandler, finalBodyBytes, models.RequestTypeFinal)
	if retryCtx.codexAffinityCacheKey != "" &&
		resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices &&
		c.Writer.Status() >= http.StatusOK && c.Writer.Status() < http.StatusMultipleChoices {
		_, _, logicalFailure := logicalStatusFromContext(c)
		_, statusUnverified := c.Get(ctxKeyResponsesStatusUnverified)
		_, processingFailed := c.Get(ctxKeyResponseProcessingFailed)
		if !logicalFailure && !statusUnverified && !processingFailed {
			ps.codexAffinityCache.set(retryCtx.codexAffinityCacheKey, subGroupID, time.Now())
		}
	}
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
	if retryCtx.lifecycleCtx != nil && retryCtx.lifecycleCtx.Err() != nil {
		statusCode, ctxErr := retryLifecycleErrorStatus(retryCtx.lifecycleCtx)
		clearAggregateSubGroupFinal := markAggregateSubGroupFinal(c)
		ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, sanitizeInternalError(ctxErr), isStream, "", nil, "", channelHandler, bodyBytes, models.RequestTypeFinal)
		clearAggregateSubGroupFinal()
		writeRetryLifecycleError(c, statusCode, ctxErr)
		return
	}

	// Get current sub-group ID
	if subGroupID, exists := c.Get("current_sub_group_id"); exists {
		subGroupIDUint, ok := subGroupID.(uint)
		if !ok {
			logrus.WithField("sub_group_id", subGroupID).
				Error("Invalid sub-group ID type in context")
			return
		}

		// Count available sub-groups before excluding the current failure.
		availableCount := ps.countAvailableSubGroups(originalGroup, retryCtx.excludedSubGroups)

		// Exclude every failed sub-group in the current cycle, including the last
		// one. Once none remain, the existing cycle reset below makes all groups
		// eligible again instead of pinning the remaining retries to the last group.
		retryCtx.excludedSubGroups[subGroupIDUint] = true
		logrus.WithFields(logrus.Fields{
			"sub_group_id":    subGroupIDUint,
			"excluded_count":  len(retryCtx.excludedSubGroups),
			"available_count": max(availableCount-1, 0),
		}).Debug("Added failed sub-group to exclusion list")
	}

	// Check if this is the last attempt
	isLastAttempt := retryCtx.attemptCount >= maxRetries
	requestType := models.RequestTypeRetry
	if isLastAttempt {
		requestType = models.RequestTypeFinal
	}

	clearAggregateSubGroupFinal := markAggregateSubGroupFinal(c)
	ps.logRequest(c, originalGroup, group, apiKey, startTime, statusCode, err, isStream, "", nil, "", channelHandler, bodyBytes, requestType)
	clearAggregateSubGroupFinal()

	// If this is the last attempt, return error
	if isLastAttempt {
		// For CC mode (Claude Code), return Claude-formatted error response
		// to ensure the client can properly parse and display the error message.
		if isCCEnabled(c) {
			returnClaudeError(c, statusCode, err.Error())
			return
		}
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
	retryCtx.forcedSubGroupID = 0
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

// logEarlyError logs errors that occur before the main request processing begins.
// This ensures that early failures (e.g., group not found, disabled, channel errors) are recorded.
func (ps *ProxyServer) logEarlyError(c *gin.Context, group *models.Group, startTime time.Time, statusCode int, err error) {
	if ps.requestLogService == nil {
		return
	}

	var groupID uint
	var groupName string
	if group != nil {
		groupID = group.ID
		groupName = group.Name
	}

	var errMsg string
	if err != nil {
		errMsg = err.Error()
	}

	// Use sanitized URL to prevent auth token leakage in logs
	ps.requestLogService.RecordError(groupID, groupName, c.ClientIP(), utils.TruncateString(utils.SanitizeURLForLog(c.Request.URL), 500), errMsg, statusCode, time.Since(startTime).Milliseconds())
}

func setUpstreamUserAgentForLog(c *gin.Context, group *models.Group, req *http.Request) {
	if group == nil || !group.EffectiveConfig.EnableRequestBodyLogging {
		return
	}
	if ua := strings.TrimSpace(req.UserAgent()); ua != "" {
		c.Set(ctxKeyUpstreamUserAgent, ua)
	}
}

func formatUpstreamAddrForLog(upstreamAddr string, proxyURL *string, gatewayProxy string) string {
	safeUpstreamAddr := utils.SanitizeRequestURLForLog(upstreamAddr)
	trimmedGatewayProxy := strings.TrimSpace(gatewayProxy)
	if trimmedGatewayProxy != "" {
		var b strings.Builder
		b.Grow(len(safeUpstreamAddr) + len(trimmedGatewayProxy) + 18)
		b.WriteString(safeUpstreamAddr)
		b.WriteString(" (gateway proxy: ")
		b.WriteString(trimmedGatewayProxy)
		b.WriteByte(')')
		return b.String()
	}

	safe := safeProxyURL(proxyURL)
	if safe == "none" {
		return safeUpstreamAddr
	}

	var b strings.Builder
	b.Grow(len(safeUpstreamAddr) + len(safe) + 17)
	b.WriteString(safeUpstreamAddr)
	b.WriteString(" (manual proxy: ")
	b.WriteString(safe)
	b.WriteByte(')')
	return b.String()
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
	gatewayProxy string,
	channelHandler channel.ChannelProxy,
	bodyBytes []byte,
	requestType string,
) {
	if ps.requestLogService == nil {
		return
	}

	var requestBodyToLog, responseBodyToLog, userAgent, upstreamUserAgent string

	if group.EffectiveConfig.EnableRequestBodyLogging {
		requestBodyToLog = sanitizeAndTruncateBytesForLog(bodyBytes, maxResponseCaptureBytes)
		userAgent = sanitizeAndTruncateStringForLog(c.Request.UserAgent(), requestLogUserAgentMaxRunes)
		if ua, exists := c.Get(ctxKeyUpstreamUserAgent); exists {
			if uaStr, ok := ua.(string); ok {
				upstreamUserAgent = sanitizeAndTruncateStringForLog(uaStr, requestLogUserAgentMaxRunes)
			}
		}

		// Get captured response body from context (if available)
		if responseBody, exists := c.Get("response_body"); exists {
			if responseBodyStr, ok := responseBody.(string); ok {
				responseBodyToLog = sanitizeAndTruncateStringForLog(responseBodyStr, maxResponseCaptureBytes)
			}
		}
	}

	duration := time.Since(startTime).Milliseconds()

	upstreamAddrForLog := formatUpstreamAddrForLog(upstreamAddr, proxyURL, gatewayProxy)

	logEntry := &models.RequestLog{
		GroupID:                group.ID,
		GroupName:              group.Name,
		IsSuccess:              finalError == nil && statusCode < 400,
		SourceIP:               c.ClientIP(),
		StatusCode:             statusCode,
		RequestPath:            utils.TruncateString(utils.SanitizeURLForLog(c.Request.URL), 500), // Sanitize to prevent auth token leakage
		Duration:               duration,
		UserAgent:              userAgent,
		UpstreamUserAgent:      upstreamUserAgent,
		SimulatedClientEnabled: channel.IsSimulatedClientEnabled(group),
		RequestType:            requestType,
		IsStream:               isStream,
		UpstreamAddr:           utils.TruncateString(upstreamAddrForLog, 500),
		RequestBody:            requestBodyToLog,
		ResponseBody:           responseBodyToLog,
	}

	if logicalStatusCode, logicalErrorMessage, ok := logicalStatusFromContext(c); ok {
		logEntry.IsSuccess = false
		logEntry.StatusCode = logicalStatusCode
		if logicalErrorMessage != "" {
			logEntry.ErrorMessage = logicalErrorMessage
		} else if finalError != nil {
			logEntry.ErrorMessage = finalError.Error()
		}
	}

	// Set parent group
	if originalGroup != nil && originalGroup.GroupType == "aggregate" && originalGroup.ID != group.ID {
		logEntry.ParentGroupID = originalGroup.ID
		logEntry.ParentGroupName = originalGroup.Name
	}

	if channelHandler != nil && bodyBytes != nil {
		logEntry.Model = channelHandler.ExtractModel(c, bodyBytes)
	}
	if logEntry.Model == "" && ps.isModelsEndpoint(c.Request.URL.Path) {
		model, mappedModel := modelListRedirectLogModels(group)
		if model != "" {
			logEntry.Model = model
			if mappedModel != "" && mappedModel != model {
				logEntry.MappedModel = mappedModel
			}
		}
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

	if finalError != nil && logEntry.ErrorMessage == "" {
		logEntry.ErrorMessage = finalError.Error()
	}

	// Only successful final requests enter token stats; failed upstream 4xx/5xx responses are excluded.
	if logEntry.RequestType == models.RequestTypeFinal && logEntry.IsSuccess {
		if usage, source, ok := getTokenUsage(c); ok {
			logEntry.InputTokens = usage.InputTokens
			logEntry.OutputTokens = usage.OutputTokens
			logEntry.TotalTokens = usage.TotalTokens
			logEntry.CacheReadTokens = usage.CacheReadTokens
			logEntry.CacheWriteTokens = usage.CacheWriteTokens
			logEntry.ThinkingTokens = usage.ThinkingTokens
			logEntry.TokenUsageSource = source
		} else {
			outputTokens := getEstimatedOutputTokens(c)
			if len(bodyBytes) <= maxEstimatedTokenBodyBytes {
				inputTokens := int64(utils.EstimateTokensFromBytes(bodyBytes))
				totalTokens := inputTokens + outputTokens
				if totalTokens > 0 {
					logEntry.InputTokens = inputTokens
					logEntry.OutputTokens = outputTokens
					logEntry.TotalTokens = totalTokens
					logEntry.TokenUsageSource = models.TokenUsageSourceEstimated
				}
			} else if outputTokens > 0 {
				logEntry.OutputTokens = outputTokens
				logEntry.TotalTokens = outputTokens
				logEntry.TokenUsageSource = models.TokenUsageSourceEstimated
			}
		}
	}
	clearTokenUsage(c)

	// Debug log for request recording
	if !logEntry.IsSuccess {
		logrus.WithFields(logrus.Fields{
			"group_name":   logEntry.GroupName,
			"status_code":  logEntry.StatusCode,
			"is_success":   logEntry.IsSuccess,
			"request_path": logEntry.RequestPath,
			"duration_ms":  logEntry.Duration,
			"request_type": logEntry.RequestType,
			"error_msg":    logEntry.ErrorMessage,
		}).Debug("Recording failed request log")
	} else {
		logrus.WithFields(logrus.Fields{
			"group_name":   logEntry.GroupName,
			"status_code":  logEntry.StatusCode,
			"request_path": logEntry.RequestPath,
			"duration_ms":  logEntry.Duration,
			"request_type": logEntry.RequestType,
		}).Debug("Recording request log")
	}

	if err := ps.requestLogService.Record(logEntry); err != nil {
		logrus.Errorf("Failed to record request log: %v", err)
	}

	// Record dynamic weight metrics for aggregate sub-groups
	// Record both final and retry requests to accurately track sub-group health.
	// This ensures that failed sub-group attempts are reflected in health scores,
	// even when the overall aggregate request succeeds via retry to another sub-group.
	if ps.dynamicWeightManager != nil {
		ps.recordDynamicWeightMetrics(c, originalGroup, group, logEntry.IsSuccess, logEntry.StatusCode, logEntry.RequestType)
	}
}

// recordDynamicWeightMetrics records success/failure metrics for dynamic weight calculation.
// This is called after each request to update the health scores for sub-groups and model redirects.
//
// Performance note: These metric writes run inline on the request goroutine. If Redis is slow
// or unavailable, it can add tail latency or reduce throughput. The current implementation
// prioritizes simplicity and correctness. For production deployments with strict latency SLAs,
// consider async/buffering and ensure strict client timeouts in the store implementation.
func (ps *ProxyServer) recordDynamicWeightMetrics(c *gin.Context, originalGroup, group *models.Group, isSuccess bool, statusCode int, requestType string) {
	if ps.dynamicWeightManager == nil {
		return
	}

	// Determine if this is a rate limit error (429)
	// Rate limit errors receive lighter penalties as they indicate temporary throttling
	// rather than service unavailability
	isRateLimit := !isSuccess && statusCode == 429
	rateLimitPressure := int64(1)
	if isRateLimit {
		// Only the standard Retry-After header increases throttling pressure.
		// Response bodies can contain natural-language retry text even on unrelated paths.
		if value, exists := c.Get(ctxKeyRateLimitPressure); exists {
			if pressure, ok := value.(int64); ok && pressure > rateLimitPressure {
				rateLimitPressure = pressure
			}
		}
		if quotaPressure := quotaExhaustedRateLimitPressureFromContext(c); quotaPressure > rateLimitPressure {
			rateLimitPressure = quotaPressure
		}
	}

	// Record sub-group metrics for aggregate groups
	if originalGroup != nil && originalGroup.GroupType == "aggregate" && originalGroup.ID != group.ID &&
		(requestType == models.RequestTypeFinal || isAggregateSubGroupFinal(c)) {
		if isSuccess {
			ps.dynamicWeightManager.RecordSubGroupSuccess(originalGroup.ID, group.ID)
		} else {
			ps.dynamicWeightManager.RecordSubGroupFailure(originalGroup.ID, group.ID, isRateLimit, rateLimitPressure)
		}

		logrus.WithFields(logrus.Fields{
			"aggregate_group_id": originalGroup.ID,
			"sub_group_id":       group.ID,
			"is_success":         isSuccess,
			"is_rate_limit":      isRateLimit,
			"status_code":        statusCode,
			"request_type":       requestType,
		}).Debug("Recorded dynamic weight metrics for sub-group")
	}

	// Record group-level metrics for standard groups (used for Hub health score)
	// Only record for the final group that handled the request
	if group != nil && group.GroupType == "standard" {
		if isSuccess {
			ps.dynamicWeightManager.RecordGroupSuccess(group.ID)
		} else {
			ps.dynamicWeightManager.RecordGroupFailure(group.ID, isRateLimit, rateLimitPressure)
		}

		logrus.WithFields(logrus.Fields{
			"group_id":      group.ID,
			"is_success":    isSuccess,
			"is_rate_limit": isRateLimit,
			"status_code":   statusCode,
		}).Debug("Recorded dynamic weight metrics for standard group")
	}

	// Record model redirect metrics if a redirect occurred.
	// Use the dedicated redirect source model because original_model may be a
	// user-facing model-mapping alias used only for request logs.
	if redirectSourceModel, exists := c.Get(ctxKeyModelRedirectSourceModel); exists {
		if originalModelStr, ok := redirectSourceModel.(string); ok && originalModelStr != "" {
			// Get the selected target index from context if available
			if targetIdx, exists := c.Get(ctxKeyModelRedirectTargetIndex); exists {
				if targetIdxInt, ok := targetIdx.(int); ok && targetIdxInt >= 0 {
					// Get target model name from the redirect rule
					var targetModel string
					if rule, found := group.ModelRedirectMapV2[originalModelStr]; found {
						if targetIdxInt < len(rule.Targets) {
							targetModel = rule.Targets[targetIdxInt].Model
						}
					}

					// Only record if we have a valid target model
					if targetModel != "" {
						if isSuccess {
							ps.dynamicWeightManager.RecordModelRedirectSuccess(group.ID, originalModelStr, targetModel)
						} else {
							ps.dynamicWeightManager.RecordModelRedirectFailure(group.ID, originalModelStr, targetModel, isRateLimit, rateLimitPressure)
						}

						logrus.WithFields(logrus.Fields{
							"group_id":      group.ID,
							"source_model":  originalModelStr,
							"target_model":  targetModel,
							"target_index":  targetIdxInt,
							"is_success":    isSuccess,
							"is_rate_limit": isRateLimit,
							"status_code":   statusCode,
						}).Debug("Recorded dynamic weight metrics for model redirect")
					}
				}
			}
		}
	}

}
