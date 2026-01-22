// Package router provides HTTP routing configuration for the application.
package router

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"gpt-load/internal/centralizedmgmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/handler"
	"gpt-load/internal/response"
	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// RegisterHubRoutes registers Hub API routes for centralized management.
// Hub provides a unified API endpoint for accessing models from all groups.
func RegisterHubRoutes(
	router *gin.Engine,
	hubHandler *handler.HubHandler,
	accessKeyService *centralizedmgmt.HubAccessKeyService,
	configManager types.ConfigManager,
) {
	// Hub API routes - unified endpoint for all formats
	// All routes are under /hub/v1 for consistency
	hub := router.Group("/hub/v1")
	hub.Use(HubAuthMiddleware(accessKeyService))
	{
		// Chat completions (OpenAI format)
		hub.POST("/chat/completions", hubHandler.HandleHubProxy)
		hub.POST("/completions", hubHandler.HandleHubProxy)

		// Claude format
		hub.POST("/messages", hubHandler.HandleHubProxy)
		hub.POST("/messages/count_tokens", hubHandler.HandleHubProxy)

		// Codex format
		hub.POST("/responses", hubHandler.HandleHubProxy)

		// Image generation and editing
		hub.POST("/images/generations", hubHandler.HandleHubProxy)
		hub.POST("/images/edits", hubHandler.HandleHubProxy)
		hub.POST("/images/variations", hubHandler.HandleHubProxy)

		// Audio endpoints
		hub.POST("/audio/transcriptions", hubHandler.HandleHubProxy)
		hub.POST("/audio/translations", hubHandler.HandleHubProxy)
		hub.POST("/audio/speech", hubHandler.HandleHubProxy)

		// Embeddings
		hub.POST("/embeddings", hubHandler.HandleHubProxy)

		// Moderations
		hub.POST("/moderations", hubHandler.HandleHubProxy)

		// Model list (OpenAI format)
		hub.GET("/models", hubHandler.HandleListModels)
		hub.GET("/models/:model", hubHandler.HandleListModels)

		// Gemini format - model in path
		hub.POST("/models/:model", hubHandler.HandleHubProxy)
	}

	// Gemini beta endpoint
	hubBeta := router.Group("/hub/v1beta")
	hubBeta.Use(HubAuthMiddleware(accessKeyService))
	{
		// Gemini API format: /v1beta/models/{model}:{action}
		hubBeta.POST("/models/*path", hubHandler.HandleHubProxy)
		hubBeta.GET("/models", hubHandler.HandleListModels)
	}

	// Admin routes - require AUTH_KEY authentication
	// Note: Admin routes use /hub/admin (no /v1/) to distinguish from proxy routes
	authConfig := configManager.GetAuthConfig()
	admin := router.Group("/hub/admin")
	admin.Use(HubAdminAuthMiddleware(authConfig))
	{
		// Model pool management
		admin.GET("/model-pool", hubHandler.HandleGetModelPool)
		admin.GET("/model-pool/all", hubHandler.HandleGetAllModels)
		admin.GET("/model-pool/v2", hubHandler.HandleGetModelPoolV2)
		admin.PUT("/model-pool/priority", hubHandler.HandleUpdateModelGroupPriority)
		admin.PUT("/model-pool/priorities", hubHandler.HandleBatchUpdatePriorities)

		// Custom models management for aggregate groups
		admin.GET("/custom-models", hubHandler.HandleGetAggregateGroupsCustomModels)
		admin.PUT("/custom-models", hubHandler.HandleUpdateAggregateGroupCustomModels)

		// Hub settings
		admin.GET("/settings", hubHandler.HandleGetHubSettings)
		admin.PUT("/settings", hubHandler.HandleUpdateHubSettings)

		// Access key management
		admin.GET("/access-keys", hubHandler.HandleListAccessKeys)
		admin.POST("/access-keys", hubHandler.HandleCreateAccessKey)
		admin.PUT("/access-keys/:id", hubHandler.HandleUpdateAccessKey)
		admin.DELETE("/access-keys/:id", hubHandler.HandleDeleteAccessKey)
		admin.GET("/access-keys/:id/stats", hubHandler.HandleGetAccessKeyUsageStats)
		admin.GET("/access-keys/:id/plaintext", hubHandler.HandleGetAccessKeyPlaintext)

		// Batch operations for access keys
		admin.DELETE("/access-keys/batch", hubHandler.HandleBatchDeleteAccessKeys)
		admin.PUT("/access-keys/batch/enabled", hubHandler.HandleBatchUpdateAccessKeysEnabled)
	}
}

// HubAuthMiddleware validates Hub access keys for API requests.
// It extracts the key from Authorization header and validates it.
func HubAuthMiddleware(accessKeyService *centralizedmgmt.HubAccessKeyService) gin.HandlerFunc {
	return func(c *gin.Context) {
		keyValue := extractHubAccessKey(c)
		if keyValue == "" {
			returnHubAuthError(c, http.StatusUnauthorized, "hub_key_missing", "Authorization header missing")
			c.Abort()
			return
		}

		// Validate the access key
		accessKey, err := accessKeyService.ValidateAccessKey(c.Request.Context(), keyValue)
		if err != nil {
			logrus.WithError(err).Debug("Hub access key validation failed")

			// Determine error type
			if apiErr, ok := err.(*app_errors.APIError); ok {
				if apiErr.Code == "AUTHENTICATION_ERROR" {
					if strings.Contains(apiErr.Message, "disabled") {
						returnHubAuthError(c, http.StatusUnauthorized, "hub_key_disabled", "Access key is disabled")
					} else {
						returnHubAuthError(c, http.StatusUnauthorized, "hub_key_invalid", "Invalid access key")
					}
				} else {
					returnHubAuthError(c, http.StatusInternalServerError, "hub_internal_error", "Internal error")
				}
			} else {
				returnHubAuthError(c, http.StatusUnauthorized, "hub_key_invalid", "Invalid access key")
			}
			c.Abort()
			return
		}

		// Store access key in context for downstream handlers
		c.Set("hub_access_key", accessKey)

		// Record key usage asynchronously (non-blocking)
		// Use background context since request context may be cancelled after response
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := accessKeyService.RecordKeyUsage(ctx, accessKey.ID); err != nil {
				logrus.WithError(err).WithField("key_id", accessKey.ID).Warn("Failed to record key usage")
			}
		}()

		c.Next()
	}
}

// HubAdminAuthMiddleware validates admin authentication for Hub management endpoints.
// Uses the same AUTH_KEY as other admin endpoints.
func HubAdminAuthMiddleware(authConfig types.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := extractAdminAuthKey(c)
		if key == "" {
			response.Error(c, app_errors.ErrUnauthorized)
			c.Abort()
			return
		}

		// Reject if server has no auth key configured (security: prevent empty key bypass)
		if authConfig.Key == "" {
			logrus.Warn("Hub admin auth rejected: AUTH_KEY not configured")
			response.Error(c, app_errors.ErrUnauthorized)
			c.Abort()
			return
		}

		// Constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(key), []byte(authConfig.Key)) != 1 {
			response.Error(c, app_errors.ErrUnauthorized)
			c.Abort()
			return
		}

		c.Next()
	}
}

// extractHubAccessKey extracts the Hub access key from the request.
// Supports Bearer token in Authorization header and X-Api-Key header.
func extractHubAccessKey(c *gin.Context) string {
	// Bearer token in Authorization header
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		const bearerPrefix = "Bearer "
		if strings.HasPrefix(authHeader, bearerPrefix) {
			return authHeader[len(bearerPrefix):]
		}
	}

	// X-Api-Key header
	if key := c.GetHeader("X-Api-Key"); key != "" {
		return key
	}

	// Query parameter (for compatibility)
	if key := c.Query("key"); key != "" {
		return key
	}

	return ""
}

// extractAdminAuthKey extracts the admin auth key from the request.
func extractAdminAuthKey(c *gin.Context) string {
	// Query key
	if key := c.Query("key"); key != "" {
		return key
	}

	// Bearer token
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		const bearerPrefix = "Bearer "
		if strings.HasPrefix(authHeader, bearerPrefix) {
			return authHeader[len(bearerPrefix):]
		}
	}

	// X-Api-Key
	if key := c.GetHeader("X-Api-Key"); key != "" {
		return key
	}

	return ""
}

// returnHubAuthError returns a Hub-specific authentication error response.
func returnHubAuthError(c *gin.Context, status int, code, message string) {
	c.JSON(status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"type":    "authentication_error",
		},
	})
}
