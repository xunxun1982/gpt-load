// Package handler provides HTTP handlers for the application
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gpt-load/internal/centralizedmgmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/proxy"
	"gpt-load/internal/response"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// HubHandler handles Hub API endpoints for centralized management.
// It provides unified API access to all groups and manages Hub access keys.
type HubHandler struct {
	hubService       *centralizedmgmt.HubService
	accessKeyService *centralizedmgmt.HubAccessKeyService
	proxyServer      *proxy.ProxyServer
	groupManager     *services.GroupManager
}

// NewHubHandler creates a new HubHandler instance.
func NewHubHandler(
	hubService *centralizedmgmt.HubService,
	accessKeyService *centralizedmgmt.HubAccessKeyService,
	proxyServer *proxy.ProxyServer,
	groupManager *services.GroupManager,
) *HubHandler {
	return &HubHandler{
		hubService:       hubService,
		accessKeyService: accessKeyService,
		proxyServer:      proxyServer,
		groupManager:     groupManager,
	}
}

// HandleHubProxy handles /hub/v1/* proxy requests.
// It validates the access key, selects the best group for the requested model,
// and forwards the request to the existing proxy server.
func (h *HubHandler) HandleHubProxy(c *gin.Context) {
	ctx := c.Request.Context()

	// Get validated access key from context (set by HubAuthMiddleware)
	accessKeyVal, exists := c.Get("hub_access_key")
	if !exists {
		h.returnHubError(c, http.StatusUnauthorized, "hub_key_missing", "Hub access key not found in context")
		return
	}
	accessKey, ok := accessKeyVal.(*centralizedmgmt.HubAccessKey)
	if !ok || accessKey == nil {
		h.returnHubError(c, http.StatusUnauthorized, "hub_key_invalid", "Invalid access key in context")
		return
	}

	// Extract model from request body
	modelName, err := h.extractModelFromRequest(c)
	if err != nil {
		h.returnHubError(c, http.StatusBadRequest, "hub_invalid_request", err.Error())
		return
	}

	// Check if model is allowed by access key
	if !h.accessKeyService.IsModelAllowed(accessKey, modelName) {
		h.returnHubError(c, http.StatusForbidden, "hub_model_not_allowed", "Model not allowed by access key")
		return
	}

	// Check if model is available in the hub
	available, err := h.hubService.IsModelAvailable(ctx, modelName)
	if err != nil {
		logrus.WithError(err).Error("Failed to check model availability")
		h.returnHubError(c, http.StatusInternalServerError, "hub_internal_error", "Failed to check model availability")
		return
	}
	if !available {
		h.returnHubError(c, http.StatusNotFound, "hub_model_not_found", "Model not available in any group")
		return
	}

	// Select the best group for the model
	group, err := h.hubService.SelectGroupForModel(ctx, modelName)
	if err != nil {
		logrus.WithError(err).Error("Failed to select group for model")
		h.returnHubError(c, http.StatusInternalServerError, "hub_group_selection_failed", "Failed to select group")
		return
	}
	if group == nil {
		h.returnHubError(c, http.StatusServiceUnavailable, "hub_no_healthy_group", "No healthy groups available for model")
		return
	}

	// Set group_name parameter for proxy server
	c.Params = append(c.Params, gin.Param{Key: "group_name", Value: group.Name})

	// Rewrite path from /hub/v1/* to /proxy/{group}/v1/*
	originalPath := c.Request.URL.Path
	newPath := h.rewriteHubPath(originalPath, group.Name)
	c.Request.URL.Path = newPath

	logrus.WithFields(logrus.Fields{
		"model":         modelName,
		"group":         group.Name,
		"original_path": originalPath,
		"new_path":      newPath,
	}).Debug("Hub routing request to group")

	// Forward to proxy server
	h.proxyServer.HandleProxy(c)
}

// HandleListModels handles /hub/v1/models endpoint.
// Returns a list of available models in OpenAI format.
func (h *HubHandler) HandleListModels(c *gin.Context) {
	ctx := c.Request.Context()

	// Get validated access key from context
	accessKeyVal, exists := c.Get("hub_access_key")
	if !exists {
		h.returnHubError(c, http.StatusUnauthorized, "hub_key_missing", "Hub access key not found")
		return
	}
	accessKey, ok := accessKeyVal.(*centralizedmgmt.HubAccessKey)
	if !ok || accessKey == nil {
		h.returnHubError(c, http.StatusUnauthorized, "hub_key_invalid", "Invalid access key")
		return
	}

	// Get all available models
	models, err := h.hubService.GetAvailableModels(ctx)
	if err != nil {
		logrus.WithError(err).Error("Failed to get available models")
		h.returnHubError(c, http.StatusInternalServerError, "hub_internal_error", "Failed to get models")
		return
	}

	// Filter models by access key permissions
	allowedModels := h.accessKeyService.GetAllowedModels(accessKey)
	var filteredModels []string
	if allowedModels == nil {
		// All models allowed
		filteredModels = models
	} else {
		// Filter to only allowed models
		allowedSet := make(map[string]bool, len(allowedModels))
		for _, m := range allowedModels {
			allowedSet[m] = true
		}
		for _, m := range models {
			if allowedSet[m] {
				filteredModels = append(filteredModels, m)
			}
		}
	}

	// Return in OpenAI format
	modelList := make([]map[string]any, 0, len(filteredModels))
	for _, m := range filteredModels {
		modelList = append(modelList, map[string]any{
			"id":       m,
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": "hub",
		})
	}

	c.JSON(http.StatusOK, map[string]any{
		"object": "list",
		"data":   modelList,
	})
}

// HandleGetModelPool handles /hub/admin/model-pool endpoint.
// Returns the aggregated model pool for admin display.
func (h *HubHandler) HandleGetModelPool(c *gin.Context) {
	ctx := c.Request.Context()

	pool, err := h.hubService.GetModelPool(ctx)
	if err != nil {
		logrus.WithError(err).Error("Failed to get model pool")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to get model pool"))
		return
	}

	// Return in paginated format expected by frontend
	total := len(pool)
	response.Success(c, map[string]any{
		"models":      pool,
		"total":       total,
		"page":        1,
		"page_size":   total,
		"total_pages": 1,
	})
}

// HandleGetAllModels handles /hub/admin/model-pool/all endpoint.
// Returns all models without pagination for dropdowns/selectors.
func (h *HubHandler) HandleGetAllModels(c *gin.Context) {
	ctx := c.Request.Context()

	pool, err := h.hubService.GetModelPool(ctx)
	if err != nil {
		logrus.WithError(err).Error("Failed to get model pool")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to get model pool"))
		return
	}

	response.Success(c, pool)
}

// HandleListAccessKeys handles GET /hub/admin/access-keys endpoint.
func (h *HubHandler) HandleListAccessKeys(c *gin.Context) {
	ctx := c.Request.Context()

	keys, err := h.accessKeyService.ListAccessKeys(ctx)
	if HandleServiceError(c, err) {
		return
	}

	// Return in paginated format expected by frontend
	total := len(keys)
	response.Success(c, map[string]any{
		"access_keys": keys,
		"total":       total,
		"page":        1,
		"page_size":   total,
		"total_pages": 1,
	})
}

// CreateAccessKeyRequest defines the request body for creating an access key.
type CreateAccessKeyRequest struct {
	Name          string   `json:"name" binding:"required"`
	KeyValue      string   `json:"key_value,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	Enabled       bool     `json:"enabled"`
}

// HandleCreateAccessKey handles POST /hub/admin/access-keys endpoint.
func (h *HubHandler) HandleCreateAccessKey(c *gin.Context) {
	ctx := c.Request.Context()

	var req CreateAccessKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	params := centralizedmgmt.CreateAccessKeyParams{
		Name:          req.Name,
		KeyValue:      req.KeyValue,
		AllowedModels: req.AllowedModels,
		Enabled:       req.Enabled,
	}

	dto, keyValue, err := h.accessKeyService.CreateAccessKey(ctx, params)
	if HandleServiceError(c, err) {
		return
	}

	// Return the created key with the original key value (only shown once)
	result := map[string]any{
		"access_key": dto,
		"key_value":  keyValue, // Only returned on creation
	}
	response.Success(c, result)
}

// UpdateAccessKeyRequest defines the request body for updating an access key.
type UpdateAccessKeyRequest struct {
	Name          *string  `json:"name,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	Enabled       *bool    `json:"enabled,omitempty"`
}

// HandleUpdateAccessKey handles PUT /hub/admin/access-keys/:id endpoint.
func (h *HubHandler) HandleUpdateAccessKey(c *gin.Context) {
	ctx := c.Request.Context()

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "Invalid access key ID"))
		return
	}

	var req UpdateAccessKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	params := centralizedmgmt.UpdateAccessKeyParams{
		Name:          req.Name,
		AllowedModels: req.AllowedModels,
		Enabled:       req.Enabled,
	}

	dto, err := h.accessKeyService.UpdateAccessKey(ctx, uint(id), params)
	if HandleServiceError(c, err) {
		return
	}

	response.Success(c, dto)
}

// HandleDeleteAccessKey handles DELETE /hub/admin/access-keys/:id endpoint.
func (h *HubHandler) HandleDeleteAccessKey(c *gin.Context) {
	ctx := c.Request.Context()

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "Invalid access key ID"))
		return
	}

	if err := h.accessKeyService.DeleteAccessKey(ctx, uint(id)); HandleServiceError(c, err) {
		return
	}

	response.Success(c, nil)
}

// extractModelFromRequest extracts the model name from the request body.
// Supports OpenAI, Claude, Codex, and Gemini formats.
func (h *HubHandler) extractModelFromRequest(c *gin.Context) (string, error) {
	// For GET requests (like /models), no model extraction needed
	if c.Request.Method == http.MethodGet {
		return "", nil
	}

	// Read body without consuming it
	bodyBytes, err := c.GetRawData()
	if err != nil {
		return "", err
	}

	// Restore body for downstream handlers
	c.Request.Body = newBodyReader(bodyBytes)

	if len(bodyBytes) == 0 {
		return "", nil
	}

	// Parse JSON to extract model
	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return "", err
	}

	// Try different model field names
	if model, ok := body["model"].(string); ok && model != "" {
		return model, nil
	}

	// Gemini format: model is in URL path
	path := c.Request.URL.Path
	if strings.Contains(path, "/models/") {
		// Extract model from path like /hub/v1/models/gemini-pro:generateContent
		parts := strings.Split(path, "/models/")
		if len(parts) > 1 {
			modelPart := parts[1]
			// Remove action suffix like :generateContent
			if idx := strings.Index(modelPart, ":"); idx > 0 {
				return modelPart[:idx], nil
			}
			return modelPart, nil
		}
	}

	return "", nil
}

// rewriteHubPath rewrites the hub path to proxy path.
// /hub/v1/chat/completions -> /proxy/{group}/v1/chat/completions
// /hub/v1/messages -> /proxy/{group}/v1/messages (Claude)
// /hub/v1/responses -> /proxy/{group}/v1/responses (Codex)
func (h *HubHandler) rewriteHubPath(path, groupName string) string {
	// Remove /hub prefix and add /proxy/{group} prefix
	if strings.HasPrefix(path, "/hub/v1") {
		return "/proxy/" + groupName + strings.TrimPrefix(path, "/hub")
	}
	return "/proxy/" + groupName + path
}

// returnHubError returns a Hub-specific error response.
func (h *HubHandler) returnHubError(c *gin.Context, status int, code, message string) {
	c.JSON(status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"type":    h.getErrorType(status),
		},
	})
}

// getErrorType returns the error type based on HTTP status code.
func (h *HubHandler) getErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "authentication_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	case status >= 500:
		return "server_error"
	default:
		return "api_error"
	}
}


// bodyReader wraps a byte slice to implement io.ReadCloser.
type bodyReader struct {
	*strings.Reader
}

func newBodyReader(data []byte) *bodyReader {
	return &bodyReader{Reader: strings.NewReader(string(data))}
}

func (b *bodyReader) Close() error {
	return nil
}


// HandleGetModelPoolV2 handles GET /hub/admin/model-pool/v2 endpoint.
// Returns the model pool with priority information for admin display.
func (h *HubHandler) HandleGetModelPoolV2(c *gin.Context) {
	ctx := c.Request.Context()

	pool, err := h.hubService.GetModelPoolV2(ctx)
	if err != nil {
		logrus.WithError(err).Error("Failed to get model pool v2")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to get model pool"))
		return
	}

	total := len(pool)
	response.Success(c, map[string]any{
		"models":      pool,
		"total":       total,
		"page":        1,
		"page_size":   total,
		"total_pages": 1,
	})
}

// UpdateModelGroupPriorityRequest defines the request body for updating model-group priority.
type UpdateModelGroupPriorityRequest struct {
	ModelName string `json:"model_name" binding:"required"`
	GroupID   uint   `json:"group_id" binding:"required"`
	Priority  int    `json:"priority"`
}

// HandleUpdateModelGroupPriority handles PUT /hub/admin/model-pool/priority endpoint.
func (h *HubHandler) HandleUpdateModelGroupPriority(c *gin.Context) {
	ctx := c.Request.Context()

	var req UpdateModelGroupPriorityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := h.hubService.UpdateModelGroupPriority(ctx, req.ModelName, req.GroupID, req.Priority); err != nil {
		if _, ok := err.(*centralizedmgmt.InvalidPriorityError); ok {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, err.Error()))
			return
		}
		logrus.WithError(err).Error("Failed to update model group priority")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to update priority"))
		return
	}

	response.Success(c, nil)
}

// BatchUpdatePriorityRequest defines the request body for batch updating priorities.
type BatchUpdatePriorityRequest struct {
	Updates []centralizedmgmt.UpdateModelGroupPriorityParams `json:"updates" binding:"required"`
}

// HandleBatchUpdatePriorities handles PUT /hub/admin/model-pool/priorities endpoint.
func (h *HubHandler) HandleBatchUpdatePriorities(c *gin.Context) {
	ctx := c.Request.Context()

	var req BatchUpdatePriorityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := h.hubService.BatchUpdateModelGroupPriorities(ctx, req.Updates); err != nil {
		logrus.WithError(err).Error("Failed to batch update priorities")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to update priorities"))
		return
	}

	response.Success(c, nil)
}

// HandleGetHubSettings handles GET /hub/admin/settings endpoint.
func (h *HubHandler) HandleGetHubSettings(c *gin.Context) {
	ctx := c.Request.Context()

	settings, err := h.hubService.GetHubSettings(ctx)
	if err != nil {
		logrus.WithError(err).Error("Failed to get hub settings")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to get settings"))
		return
	}

	response.Success(c, settings)
}

// UpdateHubSettingsRequest defines the request body for updating Hub settings.
type UpdateHubSettingsRequest struct {
	MaxRetries      int     `json:"max_retries"`
	RetryDelay      int     `json:"retry_delay"`
	HealthThreshold float64 `json:"health_threshold"`
	EnablePriority  bool    `json:"enable_priority"`
}

// HandleUpdateHubSettings handles PUT /hub/admin/settings endpoint.
func (h *HubHandler) HandleUpdateHubSettings(c *gin.Context) {
	ctx := c.Request.Context()

	var req UpdateHubSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	dto := &centralizedmgmt.HubSettingsDTO{
		MaxRetries:      req.MaxRetries,
		RetryDelay:      req.RetryDelay,
		HealthThreshold: req.HealthThreshold,
		EnablePriority:  req.EnablePriority,
	}

	if err := h.hubService.UpdateHubSettings(ctx, dto); err != nil {
		logrus.WithError(err).Error("Failed to update hub settings")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to update settings"))
		return
	}

	response.Success(c, nil)
}
