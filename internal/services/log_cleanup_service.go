package services

import (
	"context"
	"gpt-load/internal/config"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// LogCleanupService handles cleanup of expired request logs.
type LogCleanupService struct {
	db              *gorm.DB
	settingsManager *config.SystemSettingsManager
	stopCh          chan struct{}
	wg              sync.WaitGroup
}

// NewLogCleanupService creates a new log cleanup service.
func NewLogCleanupService(db *gorm.DB, settingsManager *config.SystemSettingsManager) *LogCleanupService {
	return &LogCleanupService{
		db:              db,
		settingsManager: settingsManager,
		stopCh:          make(chan struct{}),
	}
}

// Start starts the log cleanup service.
func (s *LogCleanupService) Start() {
	s.wg.Add(1)
	go s.run()
	logrus.Debug("Log cleanup service started")
}

// Stop stops the log cleanup service gracefully.
func (s *LogCleanupService) Stop(ctx context.Context) {
	close(s.stopCh)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("LogCleanupService stopped gracefully.")
	case <-ctx.Done():
		logrus.Warn("LogCleanupService stop timed out.")
	}
}

// run executes the main cleanup loop.
func (s *LogCleanupService) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(2 * time.Hour)
	defer ticker.Stop()

	// Perform initial cleanup on startup
	s.cleanupExpiredLogs()

	for {
		select {
		case <-ticker.C:
			s.cleanupExpiredLogs()
		case <-s.stopCh:
			return
		}
	}
}

// cleanupExpiredLogs cleans up expired request logs using direct time-based batch deletion for better performance
// This approach uses timestamp index directly instead of querying IDs first, which is much faster
func (s *LogCleanupService) cleanupExpiredLogs() {
	// Get log retention days configuration
	settings := s.settingsManager.GetSettings()
	retentionDays := settings.RequestLogRetentionDays

	if retentionDays <= 0 {
		logrus.Debug("Log retention is disabled (retention_days <= 0)")
		return
	}

	// Calculate cutoff time
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays).UTC()

	// Batch size optimized for MySQL performance (typically 1000-5000 rows per batch).
	const batchSize = 2000
	totalDeleted := int64(0)
	dialect := s.db.Dialector.Name()

	for {
		batchCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var result *gorm.DB
		switch dialect {
		case "postgres":
			// PostgreSQL does not support LIMIT in DELETE directly.
			result = s.db.WithContext(batchCtx).Exec(`
				WITH c AS (
					SELECT id
					FROM request_logs
					WHERE timestamp < ?
					ORDER BY timestamp
					LIMIT ?
				)
				DELETE FROM request_logs
				WHERE id IN (SELECT id FROM c)
			`, cutoffTime, batchSize)
		case "mysql":
			// MySQL supports ORDER BY + LIMIT in DELETE.
			result = s.db.WithContext(batchCtx).Exec(
				"DELETE FROM request_logs WHERE timestamp < ? ORDER BY timestamp LIMIT ?",
				cutoffTime,
				batchSize,
			)
		case "sqlite":
			// Use rowid to apply LIMIT efficiently.
			result = s.db.WithContext(batchCtx).Exec(
				"DELETE FROM request_logs WHERE rowid IN (SELECT rowid FROM request_logs WHERE timestamp < ? LIMIT ?)",
				cutoffTime,
				batchSize,
			)
		default:
			// Fallback for unsupported dialects. GORM's Limit() works but may not be optimal.
			// Log warning if an unexpected dialect is encountered.
			logrus.Warnf("Log cleanup using fallback deletion for unsupported dialect: %s", dialect)
			result = s.db.WithContext(batchCtx).Where("timestamp < ?", cutoffTime).Limit(batchSize).Delete(&models.RequestLog{})
		}
		cancel()

		if result.Error != nil {
			if utils.IsTransientDBError(result.Error) {
				logrus.WithError(result.Error).Warn("Cleanup of expired request logs failed due to transient DB error")
				return
			}
			logrus.WithError(result.Error).Error("Failed to cleanup expired request logs")
			return
		}

		deletedCount := result.RowsAffected
		totalDeleted += deletedCount

		// If deleted count is less than batch size, we're done.
		if deletedCount < int64(batchSize) {
			break
		}

		// Small delay between batches to avoid overwhelming the database and reduce lock contention
		time.Sleep(50 * time.Millisecond)
	}

	if totalDeleted > 0 {
		logrus.WithFields(logrus.Fields{
			"deleted_count":  totalDeleted,
			"cutoff_time":    cutoffTime.Format(time.RFC3339),
			"retention_days": retentionDays,
		}).Info("Successfully cleaned up expired request logs")
	} else {
		logrus.Debug("No expired request logs found to cleanup")
	}
}
