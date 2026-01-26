// Package app provides the main application logic and lifecycle management.
package app

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"gpt-load/internal/centralizedmgmt"
	"gpt-load/internal/config"
	"gpt-load/internal/db"
	dbmigrations "gpt-load/internal/db/migrations"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/i18n"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/proxy"
	"gpt-load/internal/services"
	"gpt-load/internal/sitemanagement"
	"gpt-load/internal/store"
	"gpt-load/internal/types"
	"gpt-load/internal/version"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"go.uber.org/dig"
	"gorm.io/gorm"
)

// App holds all services and manages the application lifecycle.
type App struct {
	engine                   *gin.Engine
	configManager            types.ConfigManager
	settingsManager          *config.SystemSettingsManager
	groupManager             *services.GroupManager
	childGroupService        *services.ChildGroupService
	logCleanupService        *services.LogCleanupService
	requestLogService        *services.RequestLogService
	autoCheckinService       *sitemanagement.AutoCheckinService
	balanceService           *sitemanagement.BalanceService
	cronChecker              *keypool.CronChecker
	keyPoolProvider          *keypool.KeyProvider
	proxyServer              *proxy.ProxyServer
	dynamicWeightManager     *services.DynamicWeightManager
	dynamicWeightPersistence *services.DynamicWeightPersistence
	httpClientManager        *httpclient.HTTPClientManager
	storage                  store.Store
	db                       *gorm.DB
	httpServer               *http.Server
}

// AppParams defines the dependencies for the App.
type AppParams struct {
	dig.In
	Engine                *gin.Engine
	ConfigManager         types.ConfigManager
	SettingsManager       *config.SystemSettingsManager
	GroupManager          *services.GroupManager
	GroupService          *services.GroupService
	AggregateGroupService *services.AggregateGroupService
	ChildGroupService     *services.ChildGroupService
	LogCleanupService     *services.LogCleanupService
	RequestLogService     *services.RequestLogService
	AutoCheckinService    *sitemanagement.AutoCheckinService
	BalanceService        *sitemanagement.BalanceService
	CronChecker           *keypool.CronChecker
	KeyPoolProvider       *keypool.KeyProvider
	ProxyServer           *proxy.ProxyServer
	DynamicWeightManager  *services.DynamicWeightManager
	HTTPClientManager     *httpclient.HTTPClientManager // HTTP client manager for connection pool management
	Storage               store.Store
	DB                    *gorm.DB
	HubService            *centralizedmgmt.HubService // Hub service for centralized management
}

// NewApp is the constructor for App, with dependencies injected by dig.
func NewApp(params AppParams) *App {
	// Set dynamic weight manager on proxy server for adaptive load balancing
	params.ProxyServer.SetDynamicWeightManager(params.DynamicWeightManager)

	// Set Hub model pool cache invalidation callback on GroupService
	// This ensures the Hub cache is invalidated when groups are created, updated, or deleted
	if params.GroupService != nil && params.HubService != nil {
		params.GroupService.InvalidateHubModelPoolCacheCallback = params.HubService.InvalidateModelPoolCache
	}

	// Set KeyProvider cache invalidation callback to invalidate GroupService key stats cache
	// This ensures that when keys are added, removed, or status-changed (including restore),
	// both GroupService and AggregateGroupService caches are invalidated
	if params.KeyPoolProvider != nil && params.GroupService != nil {
		params.KeyPoolProvider.CacheInvalidationCallback = params.GroupService.InvalidateKeyStatsCache
	}

	// Create persistence service for dynamic weight metrics
	var dwPersistence *services.DynamicWeightPersistence
	if params.DynamicWeightManager != nil {
		dwPersistence = services.NewDynamicWeightPersistence(params.DB, params.DynamicWeightManager)

		// Set callbacks for soft delete/restore operations on AggregateGroupService.
		// Note: AggregateGroupService and GroupService are required dependencies injected by dig.
		// If they were nil, the application would fail at startup, which is the expected behavior.
		// Adding nil guards here would hide configuration errors - fail-fast is preferred.
		params.AggregateGroupService.OnSubGroupRemoved = func(aggregateGroupID, subGroupID uint) {
			if err := dwPersistence.DeleteSubGroupMetrics(aggregateGroupID, subGroupID); err != nil {
				logrus.WithError(err).WithFields(logrus.Fields{
					"aggregate_group_id": aggregateGroupID,
					"sub_group_id":       subGroupID,
				}).Warn("Failed to soft-delete sub-group metrics")
			}
		}
		params.AggregateGroupService.OnSubGroupAdded = func(aggregateGroupID, subGroupID uint) {
			if restored, err := dwPersistence.RestoreSubGroupMetrics(aggregateGroupID, subGroupID); err != nil {
				logrus.WithError(err).WithFields(logrus.Fields{
					"aggregate_group_id": aggregateGroupID,
					"sub_group_id":       subGroupID,
				}).Warn("Failed to restore sub-group metrics")
			} else if restored {
				logrus.WithFields(logrus.Fields{
					"aggregate_group_id": aggregateGroupID,
					"sub_group_id":       subGroupID,
				}).Debug("Restored soft-deleted sub-group metrics")
			}
		}

		// Set callback for group deletion on GroupService
		params.GroupService.OnGroupDeleted = func(groupID uint, isAggregateGroup bool) {
			if isAggregateGroup {
				if err := dwPersistence.DeleteAllSubGroupMetricsForGroup(groupID); err != nil {
					logrus.WithError(err).WithField("group_id", groupID).Warn("Failed to soft-delete aggregate group metrics")
				}
			} else {
				if err := dwPersistence.DeleteAllModelRedirectMetricsForGroup(groupID); err != nil {
					logrus.WithError(err).WithField("group_id", groupID).Warn("Failed to soft-delete model redirect metrics")
				}
			}
		}
	}

	return &App{
		engine:                   params.Engine,
		configManager:            params.ConfigManager,
		settingsManager:          params.SettingsManager,
		groupManager:             params.GroupManager,
		childGroupService:        params.ChildGroupService,
		logCleanupService:        params.LogCleanupService,
		requestLogService:        params.RequestLogService,
		autoCheckinService:       params.AutoCheckinService,
		balanceService:           params.BalanceService,
		cronChecker:              params.CronChecker,
		keyPoolProvider:          params.KeyPoolProvider,
		proxyServer:              params.ProxyServer,
		dynamicWeightManager:     params.DynamicWeightManager,
		dynamicWeightPersistence: dwPersistence,
		httpClientManager:        params.HTTPClientManager,
		storage:                  params.Storage,
		db:                       params.DB,
	}
}

// Start runs the application, it is a non-blocking call.
func (a *App) Start() error {
	// Initialize i18n
	if err := i18n.Init(); err != nil {
		return fmt.Errorf("failed to initialize i18n: %w", err)
	}
	logrus.Info("i18n initialized successfully.")

	// Master node performs initialization
	if a.configManager.IsMaster() {
		logrus.Info("Starting as Master Node.")

		if err := a.storage.Clear(); err != nil {
			return fmt.Errorf("cache cleanup failed: %w", err)
		}

		// Database migration
		dbmigrations.HandleLegacyIndexes(a.db)
		if err := a.db.AutoMigrate(
			&models.SystemSetting{},
			&models.Group{},
			&models.GroupSubGroup{},
			&models.APIKey{},
			&models.RequestLog{},
			&models.GroupHourlyStat{},
			&models.DynamicWeightMetric{},
			&sitemanagement.ManagedSite{},
			&sitemanagement.ManagedSiteCheckinLog{},
			&sitemanagement.ManagedSiteSetting{},
		); err != nil {
			return fmt.Errorf("database auto-migration failed: %w", err)
		}
		// Data migration
		if err := dbmigrations.MigrateDatabase(a.db); err != nil {
			return fmt.Errorf("database data migration failed: %w", err)
		}
		logrus.Info("Database auto-migration completed.")

		// Sync child group upstream URLs to current PORT
		// This ensures all child groups use the correct port after PORT changes
		if err := a.childGroupService.SyncChildGroupUpstreams(context.Background()); err != nil {
			logrus.WithError(err).Warn("Failed to sync child group upstream URLs")
			// Non-fatal: continue startup even if sync fails
		}

		// Initialize system settings
		if err := a.settingsManager.EnsureSettingsInitialized(a.configManager.GetAuthConfig()); err != nil {
			return fmt.Errorf("failed to initialize system settings: %w", err)
		}
		logrus.Info("System settings initialized in DB.")

		a.settingsManager.Initialize(a.storage, a.groupManager, a.configManager.IsMaster())

		// Initialize group cache BEFORE starting any background services that may hold write locks.
		// This prevents startup failures on SQLite where a long-running writer (e.g., log cleanup)
		// can block the initial groups load.
		if err := a.groupManager.Initialize(); err != nil {
			return fmt.Errorf("failed to initialize group manager: %w", err)
		}

		// Load dynamic weight metrics from database and start persistence service.
		// This ensures health scores are preserved across restarts.
		if a.dynamicWeightManager != nil && a.dynamicWeightPersistence != nil {
			if err := a.dynamicWeightPersistence.LoadFromDatabase(); err != nil {
				logrus.WithError(err).Warn("Failed to load dynamic weight metrics from database (non-fatal)")
			}
			a.dynamicWeightPersistence.Start()
		}

		// Load keys from database to Redis
		if err := a.keyPoolProvider.LoadKeysFromDB(); err != nil {
			return fmt.Errorf("failed to load keys into key pool: %w", err)
		}
		logrus.Debug("API keys loaded into Redis cache by master.")

		// Services that only start on Master node
		a.requestLogService.Start()
		a.autoCheckinService.Start()
		a.balanceService.Start()
		a.logCleanupService.Start()
		a.cronChecker.Start()
	} else {
		logrus.Info("Starting as Slave Node.")
		a.settingsManager.Initialize(a.storage, a.groupManager, a.configManager.IsMaster())

		// Initialize group cache early for slave nodes as well.
		if err := a.groupManager.Initialize(); err != nil {
			return fmt.Errorf("failed to initialize group manager: %w", err)
		}
	}

	// Display configuration and start all background services
	a.configManager.DisplayServerConfig()

	// Create HTTP server
	serverConfig := a.configManager.GetEffectiveServerConfig()
	a.httpServer = &http.Server{
		Addr:           fmt.Sprintf("%s:%d", serverConfig.Host, serverConfig.Port),
		Handler:        a.engine,
		ReadTimeout:    time.Duration(serverConfig.ReadTimeout) * time.Second,
		WriteTimeout:   time.Duration(serverConfig.WriteTimeout) * time.Second,
		IdleTimeout:    time.Duration(serverConfig.IdleTimeout) * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	// Start HTTP server in a new goroutine
	go func() {
		logrus.Infof("GPT-Load proxy server started successfully on Version: %s", version.Version)
		logrus.Infof("Server address: http://%s:%d", serverConfig.Host, serverConfig.Port)
		logrus.Info("")
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Fatalf("Server startup failed: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the application.
func (a *App) Stop(ctx context.Context) {
	logrus.Info("Shutting down server...")

	serverConfig := a.configManager.GetEffectiveServerConfig()
	totalTimeout := time.Duration(serverConfig.GracefulShutdownTimeout) * time.Second

	// Dynamically calculate HTTP shutdown timeout, reserving 5 seconds for background services
	httpShutdownTimeout := totalTimeout - 5*time.Second
	httpShutdownCtx, cancelHttpShutdown := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancelHttpShutdown()

	logrus.Debugf("Attempting to gracefully shut down HTTP server (max %v)...", httpShutdownTimeout)
	httpShutdownStart := time.Now()
	if err := a.httpServer.Shutdown(httpShutdownCtx); err != nil {
		logrus.Debugf("HTTP server graceful shutdown timed out as expected, forcing remaining connections to close.")
		if closeErr := a.httpServer.Close(); closeErr != nil {
			logrus.Errorf("Error forcing HTTP server to close: %v", closeErr)
		}
	}
	logrus.Infof("HTTP server has been shut down. (took %v)", time.Since(httpShutdownStart))

	// Use the original total timeout context to continue shutting down other background services
	stoppableServices := []func(context.Context){
		a.groupManager.Stop,
		a.settingsManager.Stop,
	}

	if serverConfig.IsMaster {
		stoppableServices = append(stoppableServices,
			a.cronChecker.Stop,
			a.autoCheckinService.Stop,
			a.balanceService.Stop,
			a.logCleanupService.Stop,
			a.requestLogService.Stop,
		)
		// Stop dynamic weight persistence service
		if a.dynamicWeightPersistence != nil {
			stoppableServices = append(stoppableServices, a.dynamicWeightPersistence.Stop)
		}
	}

	// Stop KeyProvider worker pool (runs on both master and slave)
	logrus.Debug("Stopping KeyProvider worker pool...")
	keyProviderStart := time.Now()
	a.keyPoolProvider.Stop()
	logrus.Debugf("KeyProvider worker pool stopped. (took %v)", time.Since(keyProviderStart))

	var wg sync.WaitGroup
	wg.Add(len(stoppableServices))

	for _, stopFunc := range stoppableServices {
		go func(stop func(context.Context)) {
			defer wg.Done()
			stop(ctx)
		}(stopFunc)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	bgServicesStart := time.Now()
	select {
	case <-done:
		logrus.Infof("All background services stopped. (took %v)", time.Since(bgServicesStart))
	case <-ctx.Done():
		logrus.Warnf("Shutdown timed out after %v, some services may not have stopped gracefully.", time.Since(bgServicesStart))
	}

	// Close idle HTTP connections for all managed clients to free resources
	if a.httpClientManager != nil {
		logrus.Debug("Closing idle HTTP connections...")
		httpCloseStart := time.Now()
		a.httpClientManager.CloseIdleConnections()
		logrus.Debugf("Idle HTTP connections closed. (took %v)", time.Since(httpCloseStart))
	}

	// Close storage and database connections in parallel for faster shutdown
	var dbWg sync.WaitGroup
	dbCloseStart := time.Now()

	// Close storage
	if a.storage != nil {
		dbWg.Add(1)
		go func() {
			defer dbWg.Done()
			logrus.Debug("Closing storage...")
			storageStart := time.Now()
			a.storage.Close()
			logrus.Debugf("Storage closed. (took %v)", time.Since(storageStart))
		}()
	}

	// Close database connections to prevent resource leaks
	// Best practice: explicitly close connection pools during graceful shutdown
	// Note: ReadDB and main DB are closed in parallel since they are independent connections.
	// In SQLite WAL mode, readers don't block writers regardless of closure order.

	// Close read-only database connection if it's separate from main DB (SQLite WAL mode)
	if db.ReadDB != nil && db.ReadDB != a.db {
		dbWg.Add(1)
		go func() {
			defer dbWg.Done()
			// Skip WAL checkpoint for read-only connection - it doesn't write to WAL
			closeDBConnectionWithOptions(db.ReadDB, "Read database", false)
		}()
	}

	// Close main database connection
	if a.db != nil {
		dbWg.Add(1)
		go func() {
			defer dbWg.Done()
			closeDBConnection(a.db, "Main database")
		}()
	}

	dbWg.Wait()
	logrus.Debugf("All database connections closed. (took %v)", time.Since(dbCloseStart))
	logrus.Info("Server exited gracefully")
}

// closeDBConnection gracefully closes a GORM database connection with timeout.
// It first closes prepared statement cache, then forces idle connections to close,
// and finally closes the connection pool with a timeout to avoid hanging.
func closeDBConnection(gormDB *gorm.DB, name string) {
	closeDBConnectionWithOptions(gormDB, name, true)
}

// closeDBConnectionWithOptions closes a database connection with configurable WAL checkpoint.
// doCheckpoint should be true for connections that need WAL checkpoint (write connections).
func closeDBConnectionWithOptions(gormDB *gorm.DB, name string, doCheckpoint bool) {
	if gormDB == nil {
		return
	}

	totalStart := time.Now()
	logrus.Debugf("[%s] Starting database connection close...", name)

	// Close GORM prepared statement cache first to release connections
	stmtStart := time.Now()
	if stmtManager, ok := gormDB.ConnPool.(*gorm.PreparedStmtDB); ok {
		stmtManager.Close()
		logrus.Debugf("[%s] Prepared statement cache closed. (took %v)", name, time.Since(stmtStart))
	} else {
		logrus.Debugf("[%s] No prepared statement cache to close", name)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		logrus.Errorf("Error getting sql.DB for %s: %v", name, err)
		return
	}

	// Log connection pool stats before closing
	stats := sqlDB.Stats()
	logrus.Debugf("[%s] Connection pool stats: Open=%d, InUse=%d, Idle=%d, WaitCount=%d",
		name, stats.OpenConnections, stats.InUse, stats.Idle, stats.WaitCount)

	// For SQLite main DB only: Skip WAL checkpoint on shutdown for faster exit.
	// Rationale:
	// - WAL checkpoint can take 30-60 seconds after heavy write operations
	// - SQLite automatically checkpoints on next connection open
	// - OS will flush WAL to disk on process exit
	// - No data loss risk: WAL is durable and will be replayed on next open
	// - User experience: Ctrl+C should exit quickly (< 1 second)
	//
	// Alternative considered: PRAGMA wal_checkpoint(NOOP) - explicitly skips checkpoint
	// Current approach: Simply don't call checkpoint at all - same effect, cleaner code
	dialect := gormDB.Dialector.Name()
	if dialect == "sqlite" && doCheckpoint {
		logrus.Debugf("[%s] Skipping WAL checkpoint on shutdown for faster exit (WAL will be checkpointed on next startup)", name)
	}

	// Force close all idle connections immediately by setting pool size to 0
	// This triggers immediate cleanup of idle connections in the pool
	logrus.Debugf("[%s] Setting MaxIdleConns to 0...", name)
	sqlDB.SetMaxIdleConns(0)

	// Set timeouts to 0 to prevent new connections from being kept alive
	sqlDB.SetConnMaxIdleTime(0)
	sqlDB.SetConnMaxLifetime(0)

	// Log connection pool stats after forcing idle close
	stats = sqlDB.Stats()
	logrus.Debugf("[%s] After idle cleanup: Open=%d, InUse=%d, Idle=%d",
		name, stats.OpenConnections, stats.InUse, stats.Idle)

	// Close with timeout to avoid hanging on stuck connections
	// Use a goroutine with channel to implement timeout for Close()
	closeStart := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- sqlDB.Close()
	}()

	// Wait up to 1 second for graceful close, then force proceed
	select {
	case err := <-done:
		if err != nil {
			logrus.Errorf("[%s] Error closing connection: %v (took %v)", name, err, time.Since(closeStart))
		} else {
			logrus.Debugf("[%s] Connection closed successfully. (took %v)", name, time.Since(closeStart))
		}
		logrus.Debugf("[%s] Total close time: %v", name, time.Since(totalStart))
	case <-time.After(1 * time.Second):
		logrus.Warnf("[%s] Connection close timed out after 1s, proceeding anyway (background close will continue)", name)
		logrus.Debugf("[%s] Total close time (with timeout): %v", name, time.Since(totalStart))
		// Note: The goroutine will continue running in background, but we return immediately
		// This allows the program to exit quickly while SQLite finishes cleanup
		// The deferred dbWg.Done() in the caller will be called, allowing main to exit
	}
}
