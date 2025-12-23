package router

import (
	"embed"
	"gpt-load/internal/handler"
	"gpt-load/internal/i18n"
	"gpt-load/internal/middleware"
	"gpt-load/internal/proxy"
	"gpt-load/internal/services"
	"gpt-load/internal/types"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"

	"github.com/gin-gonic/gin"
)

type embedFileSystem struct {
	http.FileSystem
}

func (e embedFileSystem) Exists(prefix string, path string) bool {
	_, err := e.Open(path)
	return err == nil
}

func EmbedFolder(fsEmbed embed.FS, targetPath string) static.ServeFileSystem {
	efs, err := fs.Sub(fsEmbed, targetPath)
	if err != nil {
		panic(err)
	}
	return embedFileSystem{
		FileSystem: http.FS(efs),
	}
}

func NewRouter(
	serverHandler *handler.Server,
	proxyServer *proxy.ProxyServer,
	configManager types.ConfigManager,
	groupManager *services.GroupManager,
	requestLogService *services.RequestLogService,
	buildFS embed.FS,
	indexPage []byte,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	// Register global middleware
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())
	router.Use(middleware.Logger(configManager.GetLogConfig()))
	router.Use(middleware.CORS(configManager.GetCORSConfig()))
	router.Use(middleware.RateLimiter(configManager.GetPerformanceConfig()))
	router.Use(middleware.SecurityHeaders())
	startTime := time.Now()
	router.Use(func(c *gin.Context) {
		c.Set("serverStartTime", startTime)
		c.Next()
	})

	// Register routes
	registerSystemRoutes(router, serverHandler)
	registerAPIRoutes(router, serverHandler, configManager)
	registerProxyRoutes(router, proxyServer, groupManager, serverHandler, requestLogService)
	registerFrontendRoutes(router, buildFS, indexPage)

	return router
}

// registerSystemRoutes registers system-level routes
func registerSystemRoutes(router *gin.Engine, serverHandler *handler.Server) {
	router.GET("/health", serverHandler.Health)
}

// registerAPIRoutes registers API routes
func registerAPIRoutes(
	router *gin.Engine,
	serverHandler *handler.Server,
	configManager types.ConfigManager,
) {
	api := router.Group("/api")
	api.Use(i18n.Middleware())

	authConfig := configManager.GetAuthConfig()

	// Public routes
	registerPublicAPIRoutes(api, serverHandler)

	// Protected routes
	protectedAPI := api.Group("")
	protectedAPI.Use(middleware.Auth(authConfig))
	registerProtectedAPIRoutes(protectedAPI, serverHandler, configManager)
}

// registerPublicAPIRoutes registers public API routes
func registerPublicAPIRoutes(api *gin.RouterGroup, serverHandler *handler.Server) {
	api.POST("/auth/login", serverHandler.Login)
	api.GET("/integration/info", serverHandler.GetIntegrationInfo)
}

// registerProtectedAPIRoutes registers protected API routes
func registerProtectedAPIRoutes(api *gin.RouterGroup, serverHandler *handler.Server, configManager types.ConfigManager) {
	api.GET("/channel-types", serverHandler.CommonHandler.GetChannelTypes)

	groups := api.Group("/groups")
	{
		groups.POST("", serverHandler.CreateGroup)
		groups.GET("", serverHandler.ListGroups)
		groups.GET("/list", serverHandler.List)
		groups.GET("/config-options", serverHandler.GetGroupConfigOptions)
		groups.PUT("/:id", serverHandler.UpdateGroup)
		groups.DELETE("/:id", serverHandler.DeleteGroup)
		groups.GET("/:id/stats", serverHandler.GetGroupStats)
		groups.POST("/:id/copy", serverHandler.CopyGroup)
		groups.PUT("/:id/toggle-enabled", serverHandler.ToggleGroupEnabled)
		groups.GET("/:id/export", serverHandler.ExportGroup)
		groups.POST("/import", serverHandler.ImportGroup)

		groups.GET("/:id/sub-groups", serverHandler.GetSubGroups)
		groups.POST("/:id/sub-groups", serverHandler.AddSubGroups)
		groups.PUT("/:id/sub-groups/:subGroupId/weight", serverHandler.UpdateSubGroupWeight)
		groups.DELETE("/:id/sub-groups/:subGroupId", serverHandler.DeleteSubGroup)
		groups.GET("/:id/parent-aggregate-groups", serverHandler.GetParentAggregateGroups)
		groups.GET("/:id/models", serverHandler.GetGroupModels)

		// Child group routes
		groups.POST("/:id/child-groups", serverHandler.CreateChildGroup)
		groups.GET("/:id/child-groups", serverHandler.GetChildGroups)
		groups.GET("/:id/parent-group", serverHandler.GetParentGroup)
		groups.GET("/:id/child-group-count", serverHandler.GetChildGroupCount)
		groups.GET("/all-child-groups", serverHandler.GetAllChildGroups)

		// Debug-only endpoint: Delete all groups
		// This dangerous operation is only available when DEBUG_MODE environment variable is enabled
		// It should NEVER be enabled in production environments
		if configManager.IsDebugMode() {
			groups.DELETE("/debug/delete-all", serverHandler.DeleteAllGroups)
		}
	}

	// Key Management Routes
	keys := api.Group("/keys")
	{
		keys.GET("", serverHandler.ListKeysInGroup)
		keys.GET("/export", serverHandler.ExportKeys)
		keys.POST("/add-multiple", serverHandler.AddMultipleKeys)
		keys.POST("/add-async", serverHandler.AddMultipleKeysAsync)
		keys.POST("/delete-multiple", serverHandler.DeleteMultipleKeys)
		keys.POST("/delete-async", serverHandler.DeleteMultipleKeysAsync)
		keys.POST("/restore-multiple", serverHandler.RestoreMultipleKeys)
		keys.POST("/restore-all-invalid", serverHandler.RestoreAllInvalidKeys)
		keys.POST("/clear-all-invalid", serverHandler.ClearAllInvalidKeys)
		keys.POST("/clear-all", serverHandler.ClearAllKeys)
		keys.POST("/validate-group", serverHandler.ValidateGroupKeys)
		keys.POST("/test-multiple", serverHandler.TestMultipleKeys)
		keys.PUT("/:id/notes", serverHandler.UpdateKeyNotes)
	}

	// Tasks
	api.GET("/tasks/status", serverHandler.GetTaskStatus)

	// Dashboard and logs
	dashboard := api.Group("/dashboard")
	{
		dashboard.GET("/stats", serverHandler.Stats)
		dashboard.GET("/chart", serverHandler.Chart)
		dashboard.GET("/encryption-status", serverHandler.EncryptionStatus)
	}

	// Logs
	logs := api.Group("/logs")
	{
		logs.GET("", serverHandler.GetLogs)
		logs.GET("/export", serverHandler.ExportLogs)
	}

	// Settings
	settings := api.Group("/settings")
	{
		settings.GET("", serverHandler.GetSettings)
		settings.PUT("", serverHandler.UpdateSettings)
	}

	// System-wide import/export
	system := api.Group("/system")
	{
		system.GET("/export", serverHandler.ExportAll)
		system.POST("/import", serverHandler.ImportAll)
		system.POST("/import-settings", serverHandler.ImportSystemSettings)
		system.POST("/import-groups-batch", serverHandler.ImportGroupsBatch)
		system.GET("/environment", serverHandler.GetEnvironmentInfo)
	}
}

// registerProxyRoutes registers proxy routes
func registerProxyRoutes(
	router *gin.Engine,
	proxyServer *proxy.ProxyServer,
	groupManager *services.GroupManager,
	serverHandler *handler.Server,
	requestLogService *services.RequestLogService,
) {
	proxyGroup := router.Group("/proxy/:group_name")

	proxyGroup.Use(middleware.ProxyRouteDispatcher(serverHandler))
	proxyGroup.Use(middleware.ProxyAuth(groupManager, requestLogService))

	proxyGroup.Any("/*path", proxyServer.HandleProxy)

	// Claude endpoint for CC support - routes to the same handler
	// Path format: /proxy/{group}/claude/v1/messages
	// The handler will detect /claude path and convert Claude requests to OpenAI format
}

// registerFrontendRoutes registers frontend routes
func registerFrontendRoutes(router *gin.Engine, buildFS embed.FS, indexPage []byte) {
	router.Use(gzip.Gzip(gzip.DefaultCompression))
	router.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "Method not allowed"})
	})

	// Use static resource cache middleware
	router.Use(middleware.StaticCache())

	router.Use(static.Serve("/", EmbedFolder(buildFS, "web/dist")))
	router.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/proxy") {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not Found"})
			return
		}
		// HTML pages are not cached to ensure updates take effect immediately
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexPage)
	})
}
