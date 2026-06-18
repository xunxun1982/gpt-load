package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

const (
	ctxKeyModelRedirectSourceModel = "model_redirect_source_model"
	ctxKeyModelRedirectTargetIndex = "model_redirect_target_index"
	requestLogModelMaxLength       = 255
	responsesEncryptedReasoning    = "reasoning.encrypted_content"
)

func setModelRedirectContext(c *gin.Context, originalModel string, targetIdx int, preserveOriginal bool) {
	if originalModel == "" {
		clearModelRedirectContext(c)
		return
	}
	if preserveOriginal {
		if _, exists := c.Get("original_model"); !exists {
			c.Set("original_model", originalModel)
		}
	} else {
		c.Set("original_model", originalModel)
	}
	// Keep redirect metrics independent from original_model, which is also used
	// for model-mapping log output and may contain a user-facing alias.
	c.Set(ctxKeyModelRedirectSourceModel, originalModel)
	if targetIdx >= 0 {
		c.Set(ctxKeyModelRedirectTargetIndex, targetIdx)
	} else {
		c.Set(ctxKeyModelRedirectTargetIndex, -1)
	}
}

const (
	simulatedClientOff        = "off"
	simulatedClientCodex      = "codex"
	simulatedClientClaudeCode = "claude_code"
)

var simulatedClaudeCodeBetaTokens = []string{
	"claude-code-20250219",
	"interleaved-thinking-2025-05-14",
	"redact-thinking-2026-02-12",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"mid-conversation-system-2026-04-07",
	"effort-2025-11-24",
}

func simulatedClientMode(group *models.Group) string {
	if group == nil || group.Config == nil {
		return simulatedClientOff
	}
	raw, ok := group.Config["simulated_client"]
	if !ok {
		return simulatedClientOff
	}
	mode, ok := raw.(string)
	if !ok {
		return simulatedClientOff
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", simulatedClientOff:
		return simulatedClientOff
	case simulatedClientCodex, simulatedClientClaudeCode:
		return mode
	default:
		return simulatedClientOff
	}
}

func applySimulatedClientHeaders(req *http.Request, group *models.Group, isStream bool) {
	if req == nil {
		return
	}

	switch simulatedClientMode(group) {
	case simulatedClientCodex:
		applyCodexCompatibleHeaders(req, group, isStream)
	case simulatedClientClaudeCode:
		req.Header.Set("User-Agent", buildClaudeCodeUserAgent(simulatedClientVersion(group, "simulated_claude_code_version", channel.DefaultClaudeCodeVersion)))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-App", "cli")
		req.Header.Set("anthropic-version", "2023-06-01")
		// Runtime session and transport negotiation headers are preserved from clients, not synthesized here.
		req.Header.Set("anthropic-beta", mergeCommaHeaderTokens(req.Header.Get("anthropic-beta"), simulatedClaudeCodeBetaTokens))
		req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
		req.Header.Set("X-Stainless-Lang", "js")
		req.Header.Set("X-Stainless-Package-Version", "0.94.0")
		req.Header.Set("X-Stainless-OS", "Windows")
		req.Header.Set("X-Stainless-Arch", "x64")
		req.Header.Set("X-Stainless-Runtime", "node")
		req.Header.Set("X-Stainless-Runtime-Version", "v24.3.0")
		req.Header.Set("X-Stainless-Retry-Count", "0")
		req.Header.Set("X-Stainless-Timeout", "600")
	}
}

func applyCodexCompatibleHeaders(req *http.Request, group *models.Group, isStream bool) {
	if req == nil {
		return
	}

	version := simulatedClientVersion(group, "simulated_codex_version", channel.DefaultCodexVersion)
	req.Header.Set("User-Agent", buildCodexUserAgent(version))
	req.Header.Set("Version", version)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Content-Type", "application/json")
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	} else if strings.TrimSpace(req.Header.Get("Accept")) == "" {
		req.Header.Set("Accept", "application/json")
	}
}

func mergeCommaHeaderTokens(existing string, required []string) string {
	tokens := make([]string, 0, len(required)+4)
	seen := make(map[string]struct{}, len(required)+4)

	for _, token := range strings.Split(existing, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		key := strings.ToLower(token)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tokens = append(tokens, token)
	}

	for _, token := range required {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		key := strings.ToLower(token)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tokens = append(tokens, token)
	}

	return strings.Join(tokens, ",")
}

func simulatedClientVersion(group *models.Group, key, fallback string) string {
	if group == nil || group.Config == nil {
		return fallback
	}
	version, ok := group.Config[key].(string)
	if !ok {
		return fallback
	}
	version = strings.TrimSpace(version)
	if !isSimpleClientVersion(version) {
		return fallback
	}
	return version
}

func isSimpleClientVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func buildCodexUserAgent(version string) string {
	return channel.BuildCodexUserAgent(version)
}

func buildClaudeCodeUserAgent(version string) string {
	return channel.BuildClaudeCodeUserAgent(version)
}

func clearModelRedirectContext(c *gin.Context) {
	delete(c.Keys, "original_model")
	delete(c.Keys, ctxKeyModelRedirectSourceModel)
	delete(c.Keys, ctxKeyModelRedirectTargetIndex)
}

// modelListRedirectLogModels returns display-only model labels for /models logs.
// It lists configured targets instead of selecting one, because model-list requests
// do not route to a single redirected model.
func modelListRedirectLogModels(group *models.Group) (string, string) {
	if group == nil {
		return "", ""
	}

	sourceModels := models.CollectSourceModels(group.ModelRedirectMap, group.ModelRedirectMapV2)
	if len(sourceModels) == 0 {
		return "", ""
	}
	sort.Strings(sourceModels)

	var requestedModels, targetModels []string
	if len(group.ModelRedirectMapV2) > 0 {
		for _, sourceModel := range sourceModels {
			rule := group.ModelRedirectMapV2[sourceModel]
			if rule == nil {
				continue
			}
			validTargets := make([]string, 0, len(rule.Targets))
			for _, target := range rule.Targets {
				if target.Model == "" || !target.IsEnabled() {
					continue
				}
				appendUnique(&validTargets, target.Model)
			}
			if len(validTargets) == 0 {
				continue
			}
			appendUnique(&requestedModels, sourceModel)
			for _, targetModel := range validTargets {
				appendUnique(&targetModels, targetModel)
			}
		}
	} else {
		for _, sourceModel := range sourceModels {
			targetModel := group.ModelRedirectMap[sourceModel]
			if targetModel == "" {
				continue
			}
			appendUnique(&requestedModels, sourceModel)
			appendUnique(&targetModels, targetModel)
		}
	}

	if len(requestedModels) == 0 || len(targetModels) == 0 {
		return "", ""
	}
	return utils.TruncateString(strings.Join(targetModels, ", "), requestLogModelMaxLength),
		utils.TruncateString(strings.Join(requestedModels, ", "), requestLogModelMaxLength)
}

func appendUnique(values *[]string, value string) {
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}

func (ps *ProxyServer) applyParamOverrides(bodyBytes []byte, group *models.Group) ([]byte, error) {
	if len(group.ParamOverrides) == 0 || len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.Warnf("failed to unmarshal request body for param override, passing through: %v", err)
		return bodyBytes, nil
	}

	// Apply each override and log for debugging.
	// Per AI review: sanitize param values to prevent leaking secrets/PII in logs.
	for key, value := range group.ParamOverrides {
		requestData[key] = value
		// Only log value when request body logging is enabled, and sanitize + truncate it.
		valueStr := "[REDACTED]"
		if group.EffectiveConfig.EnableRequestBodyLogging {
			if valueJSON, err := json.Marshal(value); err == nil {
				// Truncate after sanitization to avoid very large debug logs
				valueStr = utils.TruncateString(utils.SanitizeErrorBody(string(valueJSON)), 500)
			}
		}
		logrus.WithFields(logrus.Fields{
			"group":       group.Name,
			"param_key":   key,
			"param_value": valueStr,
		}).Debug("Applied param override")
	}

	return json.Marshal(requestData)
}

// applyParallelToolCallsConfig applies the parallel_tool_calls configuration to the request.
// This is only applied when:
//   - The request contains tools (native tool calling)
//   - force_function_call is NOT enabled (prompt-based tool calling removes native tools)
//   - The group has parallel_tool_calls configured (not nil)
//
// When parallel_tool_calls is not configured, the parameter is not added to the request,
// allowing the upstream API to use its default behavior (typically true for OpenAI).
//
// Use cases:
//   - Set to false for gpt-4.1-nano which may have issues with parallel tool calls
//   - Set to false for simpler client handling (one tool call per response)
//   - Set to false for upstreams that don't support parallel tool calls
func (ps *ProxyServer) applyParallelToolCallsConfig(bodyBytes []byte, group *models.Group) ([]byte, error) {
	if len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	// Get parallel_tool_calls config; nil means not configured
	parallelConfig := getParallelToolCallsConfig(group)
	if parallelConfig == nil {
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.Warnf("failed to unmarshal request body for parallel_tool_calls, passing through: %v", err)
		return bodyBytes, nil
	}

	// Only apply if request has tools (native tool calling)
	if _, hasTools := requestData["tools"]; !hasTools {
		return bodyBytes, nil
	}

	// Apply the configuration
	requestData["parallel_tool_calls"] = *parallelConfig

	logrus.WithFields(logrus.Fields{
		"group":               group.Name,
		"parallel_tool_calls": *parallelConfig,
	}).Debug("Applied parallel_tool_calls config")

	// Marshal and return; on error, pass through original body for graceful degradation
	// (consistent with unmarshal error handling above)
	result, err := json.Marshal(requestData)
	if err != nil {
		logrus.Warnf("failed to marshal request body after parallel_tool_calls config, passing through: %v", err)
		return bodyBytes, nil
	}
	return result, nil
}

func (ps *ProxyServer) applyStreamOverrideConfig(bodyBytes []byte, group *models.Group, allowMissingStream bool) ([]byte, error) {
	if len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	forceStream := getGroupConfigBool(group, "force_stream")
	forceNonStream := getGroupConfigBool(group, "force_non_stream")
	if !forceStream && !forceNonStream {
		return bodyBytes, nil
	}
	if forceStream && forceNonStream {
		logrus.WithField("group", group.Name).Warn("stream override skipped because force_stream and force_non_stream are both enabled")
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.Warnf("failed to unmarshal request body for stream override, passing through: %v", err)
		return bodyBytes, nil
	}
	// Known stream-capable endpoints may need an explicit field; custom schemas only get existing fields overwritten.
	if _, exists := requestData["stream"]; !exists && !allowMissingStream {
		return bodyBytes, nil
	}

	streamValue := forceStream
	requestData["stream"] = streamValue

	result, err := json.Marshal(requestData)
	if err != nil {
		logrus.Warnf("failed to marshal request body after stream override, passing through: %v", err)
		return bodyBytes, nil
	}
	return result, nil
}

func allowsMissingStreamOverride(path, method string) bool {
	return isChatCompletionsEndpoint(path, method) || isOpenAIResponsesEndpoint(path)
}

func (ps *ProxyServer) applyResponsesIncludeConfig(bodyBytes []byte, group *models.Group) ([]byte, error) {
	if len(bodyBytes) == 0 || !getGroupConfigBool(group, "responses_include_encrypted_reasoning") {
		return bodyBytes, nil
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.Warnf("failed to unmarshal request body for Responses include config, passing through: %v", err)
		return bodyBytes, nil
	}

	includeValues := make([]any, 0, 2)
	if rawInclude, ok := requestData["include"]; ok {
		if existing, ok := rawInclude.([]any); ok {
			includeValues = append(includeValues, existing...)
		}
	}

	for _, value := range includeValues {
		if text, ok := value.(string); ok && text == responsesEncryptedReasoning {
			requestData["include"] = includeValues
			result, err := json.Marshal(requestData)
			if err != nil {
				logrus.Warnf("failed to marshal request body after Responses include config, passing through: %v", err)
				return bodyBytes, nil
			}
			return result, nil
		}
	}

	includeValues = append(includeValues, responsesEncryptedReasoning)
	requestData["include"] = includeValues

	result, err := json.Marshal(requestData)
	if err != nil {
		logrus.Warnf("failed to marshal request body after Responses include config, passing through: %v", err)
		return bodyBytes, nil
	}
	return result, nil
}

func applyGeminiNativeStreamPathOverride(path string, forceStream, forceNonStream bool) string {
	if forceStream && forceNonStream {
		return path
	}
	if forceStream && strings.HasSuffix(path, ":generateContent") {
		return strings.TrimSuffix(path, ":generateContent") + ":streamGenerateContent"
	}
	if forceNonStream && strings.HasSuffix(path, ":streamGenerateContent") {
		return strings.TrimSuffix(path, ":streamGenerateContent") + ":generateContent"
	}
	return path
}

func isGeminiNativeGenerateContentPath(path string) bool {
	return strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent")
}

// applyModelMapping applies model name mapping based on group configuration.
// It modifies the request body to replace the model name if a mapping is configured.
// Returns the modified body bytes and the original model name (empty if no mapping occurred).
func (ps *ProxyServer) applyModelMapping(bodyBytes []byte, group *models.Group) ([]byte, string) {
	originalModel := ""

	// Fast path: no model mapping configured
	if group.ModelMapping == "" && len(group.ModelMappingCache) == 0 {
		return bodyBytes, originalModel
	}

	if len(bodyBytes) == 0 {
		return bodyBytes, originalModel
	}

	var requestData map[string]any
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		logrus.WithError(err).Warn("Failed to unmarshal request body for model mapping, passing through")
		return bodyBytes, originalModel
	}

	// Extract original model name
	modelValue, ok := requestData["model"]
	if !ok {
		return bodyBytes, originalModel
	}

	originalModel, ok = modelValue.(string)
	if !ok || originalModel == "" {
		return bodyBytes, originalModel
	}

	// Apply model mapping using cached map if available
	var mappedModel string
	var mapped bool
	var err error

	if len(group.ModelMappingCache) > 0 {
		mappedModel, mapped, err = utils.ApplyModelMappingFromMap(originalModel, group.ModelMappingCache)
	} else {
		mappedModel, mapped, err = utils.ApplyModelMapping(originalModel, group.ModelMapping)
	}

	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"group":          group.Name,
			"original_model": originalModel,
		}).Warn("Failed to apply model mapping, using original model")
		return bodyBytes, originalModel
	}

	// If model was mapped, update the request body
	if mapped && mappedModel != originalModel {
		requestData["model"] = mappedModel
		modifiedBytes, err := json.Marshal(requestData)
		if err != nil {
			logrus.WithError(err).Warn("Failed to marshal request body after model mapping, using original")
			return bodyBytes, originalModel
		}

		logrus.WithFields(logrus.Fields{
			"group":          group.Name,
			"original_model": originalModel,
			"mapped_model":   mappedModel,
		}).Debug("Applied model mapping")

		return modifiedBytes, originalModel
	}

	return bodyBytes, originalModel
}

// logUpstreamError provides a centralized way to log errors from upstream interactions.
func logUpstreamError(context string, err error) {
	if err == nil {
		return
	}
	if app_errors.IsIgnorableError(err) {
		logrus.Debugf("Ignorable upstream error in %s: %v", context, err)
	} else {
		logrus.Errorf("Upstream error in %s: %v", context, err)
	}
}

// Deprecated: handleGzipCompression is no longer needed.
// Go's http.Client (DisableCompression == false) auto-adds Accept-Encoding and
// transparently decompresses non-streaming responses. This helper and its single
// remaining call site are intentionally kept for backward compatibility and to
// avoid surprising behavior changes, even though automated reviews may suggest
// removing them.
func handleGzipCompression(_ *http.Response, bodyBytes []byte) []byte {
	// When DisableCompression is false (default for non-streaming requests),
	// Go's http.Client automatically:
	// 1. Adds "Accept-Encoding: gzip" to requests
	// 2. Decompresses response bodies
	// 3. Removes "Content-Encoding" header from responses
	// Therefore, this function will never see compressed data and can be safely removed.
	return bodyBytes
}
