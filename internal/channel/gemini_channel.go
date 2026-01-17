package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func init() {
	Register("gemini", newGeminiChannel)
}

type GeminiChannel struct {
	*BaseChannel
}

func newGeminiChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("gemini", group)
	if err != nil {
		return nil, err
	}

	return &GeminiChannel{
		BaseChannel: base,
	}, nil
}

// ModifyRequest adds the API key as a query parameter for Gemini requests.
func (ch *GeminiChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	if strings.Contains(req.URL.Path, "v1beta/openai") {
		req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
	} else {
		q := req.URL.Query()
		q.Set("key", apiKey.KeyValue)
		req.URL.RawQuery = q.Encode()
	}
}

// IsStreamRequest checks if the request is for a streaming response.
func (ch *GeminiChannel) IsStreamRequest(c *gin.Context, bodyBytes []byte) bool {
	path := c.Request.URL.Path
	if strings.HasSuffix(path, ":streamGenerateContent") {
		return true
	}

	return ch.BaseChannel.IsStreamRequest(c, bodyBytes)
}

func (ch *GeminiChannel) ExtractModel(c *gin.Context, bodyBytes []byte) string {
	// gemini format
	path := c.Request.URL.Path
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "models" && i+1 < len(parts) {
			modelPart := parts[i+1]
			return strings.Split(modelPart, ":")[0]
		}
	}

	// openai format fallback
	return ch.BaseChannel.ExtractModel(c, bodyBytes)
}

// ValidateKey checks if the given API key is valid by making a generateContent request.
// It now uses BaseChannel.SelectValidationUpstream so that upstream-specific proxy configuration
// is honored consistently with normal traffic.
func (ch *GeminiChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) (bool, error) {
	validationPath := "/v1beta/models/" + ch.TestModel + ":generateContent"
	q := url.Values{}
	q.Set("key", apiKey.KeyValue)

	selection, err := ch.SelectValidationUpstream(group, validationPath, q.Encode())
	if err != nil {
		return false, fmt.Errorf("failed to select upstream for gemini validation: %w", err)
	}
	if selection == nil || selection.URL == "" {
		return false, fmt.Errorf("failed to select upstream for gemini validation: empty result")
	}

	reqURL := selection.URL

	payload := gin.H{
		"contents": []gin.H{
			{
				"role": "user",
				"parts": []gin.H{
					{"text": "hi"},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal validation payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return false, fmt.Errorf("failed to create validation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Apply custom header rules if available
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContext(group, apiKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	client := selection.HTTPClient
	if client == nil {
		client = ch.HTTPClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send validation request: %w", err)
	}
	defer resp.Body.Close()

	// Any 2xx status code indicates the key is valid.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}

	// For non-200 responses, parse the body to provide a more specific error reason.
	errorBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, err)
	}

	// Use the new parser to extract a clean error message.
	parsedError := app_errors.ParseUpstreamError(errorBody)

	return false, fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError)
}

// ApplyModelRedirect overrides the default implementation for Gemini channel.
// Supports both V1 (one-to-one) and V2 (one-to-many) redirect rules.
// Returns the modified body bytes, the original model name (empty if no redirect), and error.
func (ch *GeminiChannel) ApplyModelRedirect(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, string, error) {
	modifiedBytes, originalModel, _, err := ch.ApplyModelRedirectWithIndex(req, bodyBytes, group)
	return modifiedBytes, originalModel, err
}

// ApplyModelRedirectWithIndex overrides the default implementation for Gemini channel.
// Supports both V1 (one-to-one) and V2 (one-to-many) redirect rules.
// Returns the modified body bytes, the original model name, the selected target index, and error.
func (ch *GeminiChannel) ApplyModelRedirectWithIndex(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, string, int, error) {
	hasV1Rules := len(group.ModelRedirectMap) > 0
	hasV2Rules := len(group.ModelRedirectMapV2) > 0
	if !hasV1Rules && !hasV2Rules {
		return bodyBytes, "", -1, nil
	}

	if strings.Contains(req.URL.Path, "v1beta/openai") {
		return ch.BaseChannel.ApplyModelRedirectWithIndex(req, bodyBytes, group)
	}

	return ch.applyNativeFormatRedirectWithIndex(req, bodyBytes, group)
}

// applyNativeFormatRedirect handles model redirection for Gemini native format.
// Supports both V1 and V2 redirect rules with V2 taking priority.
// Returns the modified body bytes, the original model name (empty if no redirect), and error.
func (ch *GeminiChannel) applyNativeFormatRedirect(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, string, error) {
	modifiedBytes, originalModel, _, err := ch.applyNativeFormatRedirectWithIndex(req, bodyBytes, group)
	return modifiedBytes, originalModel, err
}

// applyNativeFormatRedirectWithIndex handles model redirection for Gemini native format with index tracking.
// Supports both V1 and V2 redirect rules with V2 taking priority.
// Returns the modified body bytes, the original model name, the selected target index, and error.
func (ch *GeminiChannel) applyNativeFormatRedirectWithIndex(req *http.Request, bodyBytes []byte, group *models.Group) ([]byte, string, int, error) {
	path := req.URL.Path
	parts := strings.Split(path, "/")

	for i, part := range parts {
		if part == "models" && i+1 < len(parts) {
			modelPart := parts[i+1]
			originalModel := strings.Split(modelPart, ":")[0]

			// Resolve target model with index tracking
			// Pass both V1 and V2 maps for backward compatibility with un-migrated groups
			targetModel, ruleVersion, targetCount, selectedIdx, err := models.ResolveTargetModelWithIndex(
				originalModel, group.ModelRedirectMap, group.ModelRedirectMapV2, modelRedirectSelector,
			)
			if err != nil {
				return nil, "", -1, fmt.Errorf("failed to select target model: %w", err)
			}

			if targetModel != "" {
				// Apply the redirect by updating URL path
				suffix := ""
				if colonIndex := strings.Index(modelPart, ":"); colonIndex != -1 {
					suffix = modelPart[colonIndex:]
				}
				parts[i+1] = targetModel + suffix
				req.URL.Path = strings.Join(parts, "/")

				logrus.WithFields(logrus.Fields{
					"group":          group.Name,
					"original_model": originalModel,
					"target_model":   targetModel,
					"target_count":   targetCount,
					"target_index":   selectedIdx,
					"channel":        "gemini_native",
					"rule_version":   ruleVersion,
				}).Debug("Model redirected")

				return bodyBytes, originalModel, selectedIdx, nil
			}

			// No redirect rule found
			if group.ModelRedirectStrict {
				return nil, "", -1, fmt.Errorf("model '%s' is not configured in redirect rules", originalModel)
			}
			return bodyBytes, "", -1, nil
		}
	}

	return bodyBytes, "", -1, nil
}

// TransformModelList transforms the model list response based on redirect rules.
// Supports both V1 and V2 redirect rules.
func (ch *GeminiChannel) TransformModelList(req *http.Request, bodyBytes []byte, group *models.Group) (map[string]any, error) {
	var response map[string]any
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		logrus.WithError(err).Debug("Failed to parse model list response, returning empty")
		return nil, err
	}

	if modelsInterface, hasModels := response["models"]; hasModels {
		return ch.transformGeminiNativeFormat(req, response, modelsInterface, group), nil
	}

	if _, hasData := response["data"]; hasData {
		return ch.BaseChannel.TransformModelList(req, bodyBytes, group)
	}

	return response, nil
}

// transformGeminiNativeFormat transforms Gemini native format model list.
// Supports both V1 and V2 redirect rules.
func (ch *GeminiChannel) transformGeminiNativeFormat(req *http.Request, response map[string]any, modelsInterface any, group *models.Group) map[string]any {
	upstreamModels, ok := modelsInterface.([]any)
	if !ok {
		return response
	}

	// Build configured models from both V1 and V2 rules
	configuredModels := buildConfiguredGeminiModelsFromRules(group.ModelRedirectMap, group.ModelRedirectMapV2)

	// Strict mode: return only configured models (whitelist)
	if group.ModelRedirectStrict {
		response["models"] = configuredModels
		delete(response, "nextPageToken")

		logrus.WithFields(logrus.Fields{
			"group":       group.Name,
			"model_count": len(configuredModels),
			"strict_mode": true,
			"format":      "gemini_native",
		}).Debug("Model list returned (strict mode - configured models only)")

		return response
	}

	// Non-strict mode: merge upstream + configured models (upstream priority)
	var merged []any
	if isFirstPage(req) {
		merged = mergeGeminiModelLists(upstreamModels, configuredModels)
		logrus.WithFields(logrus.Fields{
			"group":            group.Name,
			"upstream_count":   len(upstreamModels),
			"configured_count": len(configuredModels),
			"merged_count":     len(merged),
			"strict_mode":      false,
			"format":           "gemini_native",
			"page":             "first",
		}).Debug("Model list merged (non-strict mode - first page)")
	} else {
		merged = upstreamModels
		logrus.WithFields(logrus.Fields{
			"group":          group.Name,
			"upstream_count": len(upstreamModels),
			"strict_mode":    false,
			"format":         "gemini_native",
			"page":           "subsequent",
		}).Debug("Model list returned (non-strict mode - subsequent page)")
	}

	response["models"] = merged
	return response
}

// buildConfiguredGeminiModelsFromRules builds a list of models from redirect rules for Gemini format.
// If V2 rules exist, only V2 source models are used (V1 is ignored for backward compatibility).
func buildConfiguredGeminiModelsFromRules(v1Map map[string]string, v2Map map[string]*models.ModelRedirectRuleV2) []any {
	sourceModels := models.CollectSourceModels(v1Map, v2Map)
	if len(sourceModels) == 0 {
		return []any{}
	}

	result := make([]any, 0, len(sourceModels))
	for _, sourceModel := range sourceModels {
		modelName := sourceModel
		if !strings.HasPrefix(sourceModel, "models/") {
			modelName = "models/" + sourceModel
		}

		// Extract clean name without "models/" prefix for displayName and description
		cleanName := strings.TrimPrefix(modelName, "models/")

		result = append(result, map[string]any{
			"name":                       modelName,
			"displayName":                cleanName,
			"description":                cleanName,
			"supportedGenerationMethods": []string{"generateContent"},
		})
	}
	return result
}

// mergeGeminiModelLists merges upstream and configured model lists for Gemini format.
// Upstream models take priority to avoid duplicates.
func mergeGeminiModelLists(upstream []any, configured []any) []any {
	upstreamNames := make(map[string]bool)
	for _, item := range upstream {
		if modelObj, ok := item.(map[string]any); ok {
			if modelName, ok := modelObj["name"].(string); ok {
				// Store both full name and clean name for matching
				upstreamNames[modelName] = true
				cleanName := strings.TrimPrefix(modelName, "models/")
				upstreamNames[cleanName] = true
				// Also store with "models/" prefix if not already present
				if !strings.HasPrefix(modelName, "models/") {
					upstreamNames["models/"+modelName] = true
				}
			}
		}
	}

	// Start with all upstream models
	result := make([]any, len(upstream))
	copy(result, upstream)

	// Add configured models that don't exist in upstream
	for _, item := range configured {
		if modelObj, ok := item.(map[string]any); ok {
			if modelName, ok := modelObj["name"].(string); ok {
				cleanName := strings.TrimPrefix(modelName, "models/")
				prefixedName := modelName
				if !strings.HasPrefix(modelName, "models/") {
					prefixedName = "models/" + modelName
				}

				// Check all possible name variations to avoid duplicates
				if !upstreamNames[modelName] && !upstreamNames[cleanName] && !upstreamNames[prefixedName] {
					result = append(result, item)
				}
			}
		}
	}

	return result
}

// isFirstPage checks if this is the first page of a Gemini paginated request
func isFirstPage(req *http.Request) bool {
	pageToken := req.URL.Query().Get("pageToken")
	return pageToken == ""
}
