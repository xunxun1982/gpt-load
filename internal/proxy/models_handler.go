package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gpt-load/internal/channel"
	"gpt-load/internal/models"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isModelsEndpoint checks if the request path is a models endpoint
func (ps *ProxyServer) isModelsEndpoint(path string) bool {
	// Trim trailing slash to handle paths like "/v1/models/"
	p := strings.TrimRight(path, "/")
	// HasSuffix("/models") matches "/models", "/v1/models", "/v1beta/models", etc.
	return strings.HasSuffix(p, "/models")
}

// copyUpstreamHeaders copies headers from upstream, excluding hop-by-hop and Content-Length.
func copyUpstreamHeaders(dst http.Header, src http.Header) {
	for k, vv := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Connection", "Keep-Alive", "Proxy-Authenticate",
			"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		default:
			dst.Del(k)
			for _, v := range vv {
				dst.Add(k, v)
			}
		}
	}
}

// writePassthroughModelsResponse forwards upstream headers/body (keeps Content-Encoding/Type).
func writePassthroughModelsResponse(c *gin.Context, resp *http.Response, body []byte) error {
	hdr := c.Writer.Header()
	copyUpstreamHeaders(hdr, resp.Header)
	// We aggregated the body; ensure no chunked conflict and set correct length.
	hdr.Del("Transfer-Encoding")
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	c.Writer.WriteHeader(resp.StatusCode)
	_, err := c.Writer.Write(body)
	return err
}

// writeEnhancedModelsResponse writes a mutated body, dropping encodings/validators and setting JSON Content-Type.
func writeEnhancedModelsResponse(c *gin.Context, resp *http.Response, body []byte, contentType string) error {
	hdr := c.Writer.Header()
	copyUpstreamHeaders(hdr, resp.Header)
	hdr.Del("Content-Encoding")
	hdr.Del("ETag")
	hdr.Del("Transfer-Encoding")
	if contentType != "" {
		hdr.Set("Content-Type", contentType)
	}
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	c.Writer.WriteHeader(resp.StatusCode)
	_, err := c.Writer.Write(body)
	return err
}

// handleModelsResponse handles /models endpoint responses by adding model mapping aliases
// This handler is used when model_mapping is configured. It applies strict mode filtering
// (if enabled) before adding aliases to ensure external applications respect the whitelist.
func (ps *ProxyServer) handleModelsResponse(c *gin.Context, resp *http.Response, group *models.Group, channelHandler channel.ChannelProxy) {
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	logrus.WithFields(logrus.Fields{
		"group":               group.Name,
		"model_mapping_count": len(group.ModelMappingCache),
		"strict_mode":         group.ModelRedirectStrict,
		"content_encoding":    resp.Header.Get("Content-Encoding"),
	}).Debug("Handling /models endpoint with model mapping")

	// Read response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logrus.WithError(err).Warn("Failed to read models response body; returning partial/original if any")
		// resp.Body is already consumed, write any partial bytes read
		if len(bodyBytes) > 0 {
			if writeErr := writePassthroughModelsResponse(c, resp, bodyBytes); writeErr != nil {
				logUpstreamError("writing partial models response", writeErr)
			}
		}
		return
	}

	logrus.WithFields(logrus.Fields{
		"body_size":   len(bodyBytes),
		"first_bytes": fmt.Sprintf("%x", bodyBytes[:min(10, len(bodyBytes))]),
	}).Debug("Read response body")

	// Upstream returns plain body (Accept-Encoding removed). No decompression needed.

	// Apply strict mode filtering first (if enabled) via TransformModelList
	// This ensures external applications respect the model whitelist
	transformedResponse, err := channelHandler.TransformModelList(c.Request, bodyBytes, group)
	if err != nil {
		// Fail closed in strict mode: deny model list on transform error to enforce whitelist
		if group.ModelRedirectStrict {
			logrus.WithError(err).Warn("Strict mode transform failed; denying model list")
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "model list transform failed"})
			return
		}
		logrus.WithError(err).Debug("Failed to transform model list, returning original")
		if writeErr := writePassthroughModelsResponse(c, resp, bodyBytes); writeErr != nil {
			logUpstreamError("writing original models response after transform error", writeErr)
		}
		return
	}

	// Marshal transformed response back to bytes for alias enhancement
	transformedBytes, err := json.Marshal(transformedResponse)
	if err != nil {
		// Fail closed in strict mode: deny model list on marshal error to enforce whitelist
		if group.ModelRedirectStrict {
			logrus.WithError(err).Warn("Strict mode marshal failed; denying model list")
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "model list transform failed"})
			return
		}
		logrus.WithError(err).Warn("Failed to marshal transformed model list")
		if writeErr := writePassthroughModelsResponse(c, resp, bodyBytes); writeErr != nil {
			logUpstreamError("writing original models response after marshal error", writeErr)
		}
		return
	}

	// Try to parse and enhance the response with model mapping aliases
	enhancedBody, err := ps.enhanceModelsResponse(transformedBytes, group)
	if err != nil {
		logrus.WithError(err).Debug("Failed to enhance models response, returning transformed")
		// If enhancement fails, return transformed response (with strict mode applied)
		if writeErr := writeEnhancedModelsResponse(c, resp, transformedBytes, "application/json; charset=utf-8"); writeErr != nil {
			logUpstreamError("writing transformed models response", writeErr)
		}
		return
	}

	// If no change from alias enhancement, forward transformed (avoid misleading "enhanced" log)
	if len(enhancedBody) == len(transformedBytes) && bytes.Equal(enhancedBody, transformedBytes) {
		logrus.Debug("No alias enhancement applied; forwarding transformed body")
		if err := writeEnhancedModelsResponse(c, resp, transformedBytes, "application/json; charset=utf-8"); err != nil {
			logUpstreamError("writing transformed models response", err)
		}
		return
	}

	logrus.WithFields(logrus.Fields{
		"group":            group.Name,
		"original_size":    len(bodyBytes),
		"transformed_size": len(transformedBytes),
		"enhanced_size":    len(enhancedBody),
		"strict_mode":      group.ModelRedirectStrict,
	}).Debug("Successfully transformed and enhanced /models response")

	// Write enhanced response with JSON content type
	if err := writeEnhancedModelsResponse(c, resp, enhancedBody, "application/json; charset=utf-8"); err != nil {
		logUpstreamError("writing enhanced models response", err)
	}
}

// enhanceModelsResponse adds model mapping aliases to the models list
func (ps *ProxyServer) enhanceModelsResponse(bodyBytes []byte, group *models.Group) ([]byte, error) {
	// Log the raw response for debugging
	logrus.WithFields(logrus.Fields{
		"group":            group.Name,
		"response_size":    len(bodyBytes),
		"response_preview": string(bodyBytes[:min(200, len(bodyBytes))]),
	}).Debug("Raw models response from upstream")

	// Try to parse as generic JSON first
	var responseData map[string]any
	if err := json.Unmarshal(bodyBytes, &responseData); err != nil {
		logrus.WithFields(logrus.Fields{
			"error":            err.Error(),
			"response_preview": string(bodyBytes[:min(500, len(bodyBytes))]),
		}).Warn("Failed to parse models response as JSON; returning original")
		return nil, err
	}

	// Extract original model names from mapping (keys)
	aliasModels := make([]string, 0, len(group.ModelMappingCache))
	for originalModel := range group.ModelMappingCache {
		aliasModels = append(aliasModels, originalModel)
	}
	// Sort to ensure deterministic ordering (map iteration is random)
	sort.Strings(aliasModels)

	logrus.WithFields(logrus.Fields{
		"group":         group.Name,
		"alias_models":  aliasModels,
		"mapping_cache": group.ModelMappingCache,
	}).Debug("Enhancing models response with aliases")

	// Try different response formats
	enhanced := false

	// OpenAI format: {"data": [{"id": "model-name", ...}], "object": "list"}
	if dataArray, ok := responseData["data"].([]any); ok {
		existingModels := make(map[string]bool)
		for _, item := range dataArray {
			if modelObj, ok := item.(map[string]any); ok {
				if modelID, ok := modelObj["id"].(string); ok {
					existingModels[modelID] = true
				}
			}
		}

		logrus.WithFields(logrus.Fields{
			"existing_count": len(existingModels),
			"alias_count":    len(aliasModels),
		}).Debug("Processing OpenAI format models response")

		// Add alias models that don't already exist
		addedCount := 0
		for _, alias := range aliasModels {
			if !existingModels[alias] {
				newModel := map[string]any{
					"id":       alias,
					"object":   "model",
					"created":  time.Now().Unix(),
					"owned_by": "alias",
				}
				dataArray = append(dataArray, newModel)
				addedCount++
				logrus.WithField("alias", alias).Debug("Added alias model to response")
			}
		}
		if addedCount > 0 {
			responseData["data"] = dataArray
			enhanced = true
		}
		logrus.WithField("added_count", addedCount).Debug("Added alias models to OpenAI format response")
	} else if modelsArray, ok := responseData["models"].([]any); ok {
		// Gemini format: {"models": [{"name": "model-name", ...}]}
		// Use else if to process only one format (avoid dual-format enhancement)
		existingModels := make(map[string]bool)
		for _, item := range modelsArray {
			if modelObj, ok := item.(map[string]any); ok {
				if modelName, ok := modelObj["name"].(string); ok {
					existingModels[modelName] = true
				}
			}
		}

		// Add alias models that don't already exist
		addedCount := 0
		for _, alias := range aliasModels {
			if !existingModels[alias] {
				newModel := map[string]any{
					"name":         alias,
					"display_name": alias,
				}
				modelsArray = append(modelsArray, newModel)
				addedCount++
			}
		}
		if addedCount > 0 {
			responseData["models"] = modelsArray
			enhanced = true
		}
	}

	if !enhanced {
		logrus.Debug("Unknown models response format, returning original")
		return bodyBytes, nil
	}

	// Serialize enhanced response
	enhancedBytes, err := json.Marshal(responseData)
	if err != nil {
		logrus.WithError(err).Warn("Failed to marshal enhanced models response")
		return bodyBytes, nil
	}
	return enhancedBytes, nil
}
