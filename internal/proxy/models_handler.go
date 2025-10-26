package proxy

import (
	"encoding/json"
	"fmt"
	"gpt-load/internal/models"
	"io"
	"net/http"
	"strconv"
	"strings"

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

// handleModelsResponse handles /models endpoint responses by adding model mapping aliases
func (ps *ProxyServer) handleModelsResponse(c *gin.Context, resp *http.Response, group *models.Group) {
	logrus.WithFields(logrus.Fields{
		"group":                group.Name,
		"model_mapping_count":  len(group.ModelMappingCache),
		"content_encoding":     resp.Header.Get("Content-Encoding"),
	}).Debug("Handling /models endpoint with model mapping")

	// Read response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logrus.WithError(err).Warn("Failed to read models response body; returning partial/original if any")
		// resp.Body is already consumed, write any partial bytes read
		if len(bodyBytes) > 0 {
			hdr := c.Writer.Header()
			hdr.Del("Content-Encoding")
			hdr.Del("ETag")
			hdr.Del("Transfer-Encoding")
			hdr.Set("Content-Length", strconv.Itoa(len(bodyBytes)))
			if _, writeErr := c.Writer.Write(bodyBytes); writeErr != nil {
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

	// Try to parse and enhance the response
	enhancedBody, err := ps.enhanceModelsResponse(bodyBytes, group)
	if err != nil {
		logrus.WithError(err).Debug("Failed to enhance models response, returning original")
		// If enhancement fails, return original response
		// Update headers to match the actual body size
		hdr := c.Writer.Header()
		hdr.Del("Content-Encoding")
		hdr.Del("ETag")
		hdr.Del("Transfer-Encoding")
		hdr.Set("Content-Length", strconv.Itoa(len(bodyBytes)))
		if _, writeErr := c.Writer.Write(bodyBytes); writeErr != nil {
			logUpstreamError("writing original models response", writeErr)
		}
		return
	}

	logrus.WithFields(logrus.Fields{
		"group":         group.Name,
		"original_size": len(bodyBytes),
		"enhanced_size": len(enhancedBody),
	}).Debug("Successfully enhanced /models response")

	// Write enhanced response
	// Update headers to match the enhanced body
	hdr := c.Writer.Header()
	hdr.Del("Content-Encoding")
	hdr.Del("ETag")
	hdr.Del("Transfer-Encoding")
	hdr.Set("Content-Type", "application/json; charset=utf-8")
	hdr.Set("Content-Length", strconv.Itoa(len(enhancedBody)))
	if _, err := c.Writer.Write(enhancedBody); err != nil {
		logUpstreamError("writing enhanced models response", err)
	}
}

// enhanceModelsResponse adds model mapping aliases to the models list
func (ps *ProxyServer) enhanceModelsResponse(bodyBytes []byte, group *models.Group) ([]byte, error) {
	// Log the raw response for debugging
	logrus.WithFields(logrus.Fields{
		"group":         group.Name,
		"response_size": len(bodyBytes),
		"response_preview": string(bodyBytes[:min(200, len(bodyBytes))]),
	}).Debug("Raw models response from upstream")

	// Try to parse as generic JSON first
	var responseData map[string]any
	if err := json.Unmarshal(bodyBytes, &responseData); err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err.Error(),
			"response_preview": string(bodyBytes[:min(500, len(bodyBytes))]),
		}).Warn("Failed to parse models response as JSON; returning original")
		return nil, err
	}

	// Extract original model names from mapping (keys)
	aliasModels := make([]string, 0, len(group.ModelMappingCache))
	for originalModel := range group.ModelMappingCache {
		aliasModels = append(aliasModels, originalModel)
	}

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
					"created":  1626777600,
					"owned_by": "alias",
				}
				dataArray = append(dataArray, newModel)
				addedCount++
				logrus.WithField("alias", alias).Debug("Added alias model to response")
			}
		}
		responseData["data"] = dataArray
		enhanced = true
		logrus.WithField("added_count", addedCount).Debug("Added alias models to OpenAI format response")
	}

	// Gemini format: {"models": [{"name": "model-name", ...}]}
	if modelsArray, ok := responseData["models"].([]any); ok {
		existingModels := make(map[string]bool)
		for _, item := range modelsArray {
			if modelObj, ok := item.(map[string]any); ok {
				if modelName, ok := modelObj["name"].(string); ok {
					existingModels[modelName] = true
				}
			}
		}

		// Add alias models that don't already exist
		for _, alias := range aliasModels {
			if !existingModels[alias] {
				newModel := map[string]any{
					"name":         alias,
					"display_name": alias,
				}
				modelsArray = append(modelsArray, newModel)
			}
		}
		responseData["models"] = modelsArray
		enhanced = true
	}

	if !enhanced {
		logrus.Debug("Unknown models response format, returning original")
		return bodyBytes, nil
	}

	// Serialize enhanced response
	return json.Marshal(responseData)
}
