package proxy

import (
	"encoding/json"
	"net/http"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
)

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
