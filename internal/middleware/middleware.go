// Package middleware provides HTTP middleware for the application
package middleware

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/response"
	"gpt-load/internal/services"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Logger creates a high-performance logging middleware
func Logger(config types.LogConfig) gin.HandlerFunc {
	return func(c *gin.Context) {

		start := time.Now()
		path := c.Request.URL.Path

		// Process request first, so auth middleware can sanitize sensitive params
		c.Next()

		// Calculate response time
		latency := time.Since(start)

		// Get basic information
		method := c.Request.Method
		statusCode := c.Writer.Status()

		// Sanitize URL to prevent sensitive data (auth tokens, keys) from appearing in logs
		// This handles both proxy routes (where extractAuthKey removes key param) and
		// non-proxy routes (like /api/logs/export) where key param may still be present
		fullPath := utils.SanitizeURLForLog(c.Request.URL)

		// Get key information (if exists)
		keyInfo := ""
		if keyIndex, exists := c.Get("keyIndex"); exists {
			if keyPreview, exists := c.Get("keyPreview"); exists {
				// Use strings.Builder for better performance in hot path
				var b strings.Builder
				b.WriteString(" - Key[")
				// Use strconv for int type, fallback to fmt.Sprint for other types
				if idx, ok := keyIndex.(int); ok {
					b.WriteString(strconv.Itoa(idx))
				} else {
					b.WriteString(fmt.Sprint(keyIndex))
				}
				b.WriteString("] ")
				b.WriteString(fmt.Sprint(keyPreview))
				keyInfo = b.String()
			}
		}

		// Get retry information (if exists)
		retryInfo := ""
		if retryCount, exists := c.Get("retryCount"); exists {
			// Use strings.Builder for better performance in hot path
			var b strings.Builder
			b.WriteString(" - Retry[")
			// Use strconv for int type, fallback to fmt.Sprint for other types
			if count, ok := retryCount.(int); ok {
				b.WriteString(strconv.Itoa(count))
			} else {
				b.WriteString(fmt.Sprint(retryCount))
			}
			b.WriteByte(']')
			retryInfo = b.String()
		}

		// Filter health check and other monitoring endpoint logs to reduce noise
		if isMonitoringEndpoint(path) {
			// Only log errors for monitoring endpoints
			if statusCode >= 400 {
				logrus.Warnf("%s %s - %d - %v", method, fullPath, statusCode, latency)
			}
			return
		}

		// Choose log level based on status code
		if statusCode >= 500 {
			logrus.Errorf("%s %s - %d - %v%s%s", method, fullPath, statusCode, latency, keyInfo, retryInfo)
		} else if statusCode >= 400 {
			logrus.Warnf("%s %s - %d - %v%s%s", method, fullPath, statusCode, latency, keyInfo, retryInfo)
		} else {
			logrus.Infof("%s %s - %d - %v%s%s", method, fullPath, statusCode, latency, keyInfo, retryInfo)
		}
	}
}

// CORS creates a CORS middleware with efficient preflight handling
func CORS(config types.CORSConfig) gin.HandlerFunc {
	// Pre-compute joined strings for better performance
	allowedMethods := strings.Join(config.AllowedMethods, ", ")
	allowedHeaders := strings.Join(config.AllowedHeaders, ", ")

	// Create a map for faster origin lookup
	allowedOriginsMap := make(map[string]bool, len(config.AllowedOrigins))
	hasWildcard := false
	for _, origin := range config.AllowedOrigins {
		if origin == "*" {
			hasWildcard = true
		} else {
			allowedOriginsMap[origin] = true
		}
	}
	// Clear map only when wildcard is used without credentials.
	// When credentials are allowed, we still need the explicit allowlist for origin validation.
	if hasWildcard && !config.AllowCredentials {
		allowedOriginsMap = nil
	}
	// Warn on misconfiguration: wildcard origin with credentials enabled effectively disables CORS.
	if config.AllowCredentials && len(config.AllowedOrigins) == 1 && config.AllowedOrigins[0] == "*" {
		logrus.Warn("CORS configuration uses AllowedOrigins=['*'] with AllowCredentials=true; this blocks all credentialed CORS requests. Configure explicit origins instead.")
	}

	return func(c *gin.Context) {
		if !config.Enabled {
			c.Next()
			return
		}

		origin := c.Request.Header.Get("Origin")

		// Fast path: handle preflight requests immediately
		if c.Request.Method == "OPTIONS" {
			// Check if origin is allowed
			if isOriginAllowed(origin, hasWildcard, config.AllowCredentials, allowedOriginsMap) {
				// Set Access-Control-Allow-Origin header
				setAllowOriginHeader(c, origin, hasWildcard, config.AllowCredentials)

				// Set CORS headers for preflight only when origin is allowed
				c.Header("Access-Control-Allow-Methods", allowedMethods)
				c.Header("Access-Control-Allow-Headers", allowedHeaders)
				if config.AllowCredentials {
					c.Header("Access-Control-Allow-Credentials", "true")
				}
				// Add cache control for preflight to reduce requests
				c.Header("Access-Control-Max-Age", "86400") // 24 hours
			}

			c.AbortWithStatus(204)
			return
		}

		// For actual requests, check origin and set headers
		if isOriginAllowed(origin, hasWildcard, config.AllowCredentials, allowedOriginsMap) {
			// Set Access-Control-Allow-Origin header
			setAllowOriginHeader(c, origin, hasWildcard, config.AllowCredentials)

			// Set other CORS headers only for allowed origins
			c.Header("Access-Control-Allow-Methods", allowedMethods)
			c.Header("Access-Control-Allow-Headers", allowedHeaders)
			if config.AllowCredentials {
				c.Header("Access-Control-Allow-Credentials", "true")
			}
		}

		c.Next()
	}
}

// isOriginAllowed checks if the origin is allowed based on CORS configuration
func isOriginAllowed(origin string, hasWildcard, allowCredentials bool, allowedOriginsMap map[string]bool) bool {
	if hasWildcard && !allowCredentials {
		// Wildcard is only valid when credentials are not allowed
		return true
	}
	// Origin must be in the explicit allowlist when credentials are enabled
	return allowedOriginsMap[origin]
}

// setAllowOriginHeader sets the Access-Control-Allow-Origin header and Vary header if needed
func setAllowOriginHeader(c *gin.Context, origin string, hasWildcard, allowCredentials bool) {
	if hasWildcard && !allowCredentials {
		c.Header("Access-Control-Allow-Origin", "*")
	} else {
		c.Header("Access-Control-Allow-Origin", origin)
		// Ensure caches differentiate responses by origin when echoing specific origins
		addVaryOriginHeader(c)
	}
}

// addVaryOriginHeader adds "Origin" to the Vary header if not already present
func addVaryOriginHeader(c *gin.Context) {
	vary := c.Writer.Header().Get("Vary")
	if vary == "" {
		c.Header("Vary", "Origin")
		return
	}

	// Check if "Origin" is already present as a distinct token
	// Split by comma and check each token for exact match
	// For typical Vary headers (1-3 values), this is more efficient and maintainable
	// than multiple string checks, with negligible allocation overhead
	varyHeaders := strings.Split(vary, ",")
	for _, h := range varyHeaders {
		if strings.TrimSpace(h) == "Origin" {
			return
		}
	}

	c.Header("Vary", vary+", Origin")
}

// Auth creates an authentication middleware
func Auth(authConfig types.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if isMonitoringEndpoint(path) {
			c.Next()
			return
		}

		key := extractAuthKey(c)

		isValid := key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(authConfig.Key)) == 1

		if !isValid {
			response.Error(c, app_errors.ErrUnauthorized)
			c.Abort()
			return
		}

		c.Next()
	}
}

// ProxyAuth validates proxy authentication and logs failed attempts
func ProxyAuth(gm *services.GroupManager, requestLogService *services.RequestLogService) gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()
		groupName := c.Param("group_name")

		// Check key
		key := extractAuthKey(c)
		if key == "" {
			logrus.Debugf("[ProxyAuth] No auth key provided for path: %s", c.Request.URL.Path)
			response.Error(c, app_errors.ErrUnauthorized)
			if requestLogService != nil {
				// groupID=0 because group lookup hasn't occurred yet
				// Use sanitized URL to prevent auth token leakage in logs
				requestLogService.RecordError(0, groupName, c.ClientIP(), utils.SanitizeURLForLog(c.Request.URL), "no auth key provided", http.StatusUnauthorized, time.Since(startTime).Milliseconds())
			}
			c.Abort()
			return
		}

		group, err := gm.GetGroupByName(groupName)
		if err != nil {
			logrus.Debugf("[ProxyAuth] Failed to get group %s: %v", groupName, err)
			response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Failed to retrieve proxy group"))
			if requestLogService != nil {
				// groupID=0 because group lookup failed, but we log the attempted groupName for diagnostics
				// Use sanitized URL to prevent auth token leakage in logs
				requestLogService.RecordError(0, groupName, c.ClientIP(), utils.SanitizeURLForLog(c.Request.URL), fmt.Sprintf("group lookup failed: %v", err), http.StatusInternalServerError, time.Since(startTime).Milliseconds())
			}
			c.Abort()
			return
		}

		// Check both key collections to prevent timing attacks
		_, existsInEffective := group.EffectiveConfig.ProxyKeysMap[key]
		_, existsInGroup := group.ProxyKeysMap[key]

		if existsInEffective || existsInGroup {
			c.Next()
			return
		}

		logrus.Debugf("[ProxyAuth] Invalid proxy key for group %s, path: %s", group.Name, c.Request.URL.Path)
		response.Error(c, app_errors.ErrUnauthorized)
		if requestLogService != nil {
			// Use sanitized URL to prevent auth token leakage in logs
			requestLogService.RecordError(group.ID, group.Name, c.ClientIP(), utils.SanitizeURLForLog(c.Request.URL), "invalid proxy key", http.StatusUnauthorized, time.Since(startTime).Milliseconds())
		}
		c.Abort()
	}
}

// ProxyRouteDispatcher dispatches special routes before proxy authentication
func ProxyRouteDispatcher(serverHandler interface{ GetIntegrationInfo(*gin.Context) }) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Param("path") == "/api/integration/info" {
			serverHandler.GetIntegrationInfo(c)
			c.Abort()
			return
		}

		c.Next()
	}
}

// Recovery creates a recovery middleware with custom error handling
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logrus.Errorf("Panic recovered: %v", recovered)
		response.Error(c, app_errors.ErrInternalServer)
		c.Abort()
	})
}

// RateLimiter creates a simple rate limiting middleware
func RateLimiter(config types.PerformanceConfig) gin.HandlerFunc {
	// Simple semaphore-based rate limiting
	semaphore := make(chan struct{}, config.MaxConcurrentRequests)

	return func(c *gin.Context) {
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
			c.Next()
		default:
			response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "Too many concurrent requests"))
			c.Abort()
		}
	}
}

// ErrorHandler creates an error handling middleware
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Handle any errors that occurred during request processing
		if len(c.Errors) > 0 {
			err := c.Errors.Last().Err

			// Check if it's our custom error type
			if apiErr, ok := err.(*app_errors.APIError); ok {
				response.Error(c, apiErr)
				return
			}

			// Handle other errors
			logrus.Errorf("Unhandled error: %v", err)
			response.Error(c, app_errors.ErrInternalServer)
		}
	}
}

// isMonitoringEndpoint checks if the path is a monitoring endpoint
func isMonitoringEndpoint(path string) bool {
	monitoringPaths := []string{"/health"}
	for _, monitoringPath := range monitoringPaths {
		if path == monitoringPath {
			return true
		}
	}
	return false
}

// extractAuthKey extracts a auth key.
func extractAuthKey(c *gin.Context) string {
	// Query key
	if key := c.Query("key"); key != "" {
		query := c.Request.URL.Query()
		query.Del("key")
		c.Request.URL.RawQuery = query.Encode()
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

	// X-Goog-Api-Key
	if key := c.GetHeader("X-Goog-Api-Key"); key != "" {
		return key
	}

	return ""
}

// StaticCache creates a middleware for caching static resources
func StaticCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if isStaticResource(path) {
			c.Header("Cache-Control", "public, max-age=2592000, immutable")
			c.Header("Expires", time.Now().AddDate(1, 0, 0).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
		}

		c.Next()
	}
}

// isStaticResource checks if the path is a static resource.
func isStaticResource(path string) bool {
	staticPrefixes := []string{"/assets/"}
	staticSuffixes := []string{
		".js", ".css", ".ico", ".png", ".jpg", ".jpeg",
		".gif", ".svg", ".woff", ".woff2", ".ttf", ".eot",
		".webp", ".avif", ".map",
	}

	// Check path prefix
	for _, prefix := range staticPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	// Check file extension
	for _, suffix := range staticSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}

	return false
}

// SecurityHeaders creates a middleware to add security-related headers
// Implements security best practices to prevent browser attacks
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Prevent MIME type sniffing attacks
		c.Header("X-Content-Type-Options", "nosniff")

		// Control referrer information leakage
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")

		// Restrict browser features to prevent abuse
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")

		// Prevent clickjacking attacks while allowing same-origin embedding if needed
		c.Header("X-Frame-Options", "SAMEORIGIN")

		c.Next()
	}
}

// RequestBodySizeLimit creates a middleware to limit request body size
// Protects against memory exhaustion from large or malicious uploads
// Default limit: 150MB (configurable via MAX_REQUEST_BODY_SIZE_MB env var)
// Supports large bulk key imports (500k+ keys in a single file)
func RequestBodySizeLimit(maxBytes int64) gin.HandlerFunc {
	if maxBytes <= 0 {
		maxBytes = 150 << 20 // 150MB default
	}

	// Log threshold: warn when request body exceeds 33% of max or 50MB, whichever is smaller
	// This scales with maxBytes configuration while avoiding excessive logging
	warnThreshold := maxBytes / 3
	if warnThreshold > 50<<20 {
		warnThreshold = 50 << 20
	}

	return func(c *gin.Context) {
		// Early rejection: check Content-Length header before reading body
		if c.Request.ContentLength > maxBytes && c.Request.ContentLength != -1 {
			logrus.WithFields(logrus.Fields{
				"path":           c.Request.URL.Path,
				"content_length": c.Request.ContentLength,
				"max_bytes":      maxBytes,
			}).Warn("Request body size exceeds limit")
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}

		// Log large requests for monitoring (debug level to reduce noise during normal bulk operations)
		if c.Request.ContentLength > warnThreshold && c.Request.ContentLength != -1 {
			logrus.WithFields(logrus.Fields{
				"path":           c.Request.URL.Path,
				"content_length": c.Request.ContentLength,
				"max_bytes":      maxBytes,
			}).Debug("Processing large request body")
		}

		// Wrap request body with size limiter
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()

		// Check if MaxBytesError occurred during request processing
		// Iterate all errors to ensure we don't miss MaxBytesError if other errors are appended
		for _, err := range c.Errors {
			var mbErr *http.MaxBytesError
			if errors.As(err.Err, &mbErr) {
				logrus.WithFields(logrus.Fields{
					"path":           c.Request.URL.Path,
					"content_length": c.Request.ContentLength,
					"max_bytes":      maxBytes,
				}).Warn("Request body exceeded limit during processing")
				c.AbortWithStatus(http.StatusRequestEntityTooLarge)
				break
			}
		}
	}
}
