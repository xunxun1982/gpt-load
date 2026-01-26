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

	// Initial delay to allow database initialization to complete
	// This prevents slow SQL during startup when DB is busy with other tasks
	select {
	case <-time.After(30 * time.Second):
	case <-s.stopCh:
		return
	}

	// Perform initial cleanup after delay
	s.cleanupExpiredLogs()

	ticker := time.NewTicker(2 * time.Hour)
	defer ticker.Stop()

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
// Optimized with increased timeout and better batch sizing for large datasets
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

	// Batch size optimized for performance to minimize lock contention and timeout risk
	// Uses LogCleanupBatchSize from thresholds.go for consistency
	const batchSize = LogCleanupBatchSize
	totalDeleted := int64(0)
	nextLogAt := int64(LargeCleanupThreshold) // Track next threshold for progress logging
	dialect := s.db.Dialector.Name()

	logrus.WithFields(logrus.Fields{
		"cutoff_time":    cutoffTime.Format(time.RFC3339),
		"retention_days": retentionDays,
		"dialect":        dialect,
	}).Debug("Starting log cleanup")

	for {
		// Increased timeout from 30s to 60s to handle large batches more reliably
		// This prevents context deadline exceeded errors on slower systems
		batchCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		var result *gorm.DB
		switch dialect {
		case "postgres":
			// PostgreSQL: Use ctid for efficient batch deletion
			result = s.db.WithContext(batchCtx).Exec(`
				DELETE FROM request_logs
				WHERE ctid IN (
					SELECT ctid FROM request_logs
					WHERE timestamp < $1
					LIMIT $2
				)
			`, cutoffTime, batchSize)
		case "mysql":
			// MySQL supports ORDER BY + LIMIT in DELETE directly
			result = s.db.WithContext(batchCtx).Exec(
				"DELETE FROM request_logs WHERE timestamp < ? ORDER BY timestamp LIMIT ?",
				cutoffTime,
				batchSize,
			)
		case "sqlite":
			// SQLite: Use direct DELETE with indexed column for better performance.
			// First get the max timestamp of records to delete in this batch.
			//
			// Note: AI suggested using rowid/primary key for more deterministic batching
			// to handle duplicate timestamps. However, our testing shows:
			// 1. Timestamps have millisecond precision, making duplicates rare
			// 2. Even with duplicates, the worst case is slightly variable batch sizes
			// 3. Using rowid would require additional index and complexity
			// 4. Current approach is simpler and performs well in production
			var maxTS time.Time
			// Increased timeout for SELECT query to 10s (from 5s) for better reliability
			subCtx, subCancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := s.db.WithContext(subCtx).Raw(
				"SELECT timestamp FROM request_logs WHERE timestamp < ? ORDER BY timestamp LIMIT 1 OFFSET ?",
				cutoffTime, batchSize-1,
			).Scan(&maxTS).Error
			subCancel()

			// Note: AI suggested separating error handling from "no records" case to avoid
			// unintentionally deleting all remaining records on query failure. However:
			// 1. All records being deleted are already expired and should be removed
			// 2. If SELECT fails, falling back to delete-all is a safe degradation
			// 3. If DELETE also fails, it will be caught by the error handling below
			// 4. This approach is more resilient than aborting the entire cleanup task
			if err != nil || maxTS.IsZero() {
				// No more records or less than batchSize records, delete all remaining
				result = s.db.WithContext(batchCtx).Exec(
					"DELETE FROM request_logs WHERE timestamp < ?",
					cutoffTime,
				)
			} else {
				// Delete records up to maxTS (inclusive)
				result = s.db.WithContext(batchCtx).Exec(
					"DELETE FROM request_logs WHERE timestamp <= ?",
					maxTS,
				)
			}
		default:
			// Fallback for unsupported dialects with explicit ID-based batching.
			// GORM's Limit() with Delete() may be silently ignored by some databases,
			// so we first select IDs then delete by ID to ensure predictable batch sizes.
			logrus.Warnf("Log cleanup using fallback deletion for unsupported dialect: %s", dialect)
			var ids []string
			err := s.db.WithContext(batchCtx).Model(&models.RequestLog{}).
				Where("timestamp < ?", cutoffTime).
				Limit(batchSize).
				Pluck("id", &ids).Error
			if err != nil {
				result = &gorm.DB{Error: err}
			} else if len(ids) == 0 {
				// No records to delete, create empty result
				result = &gorm.DB{RowsAffected: 0}
			} else {
				result = s.db.WithContext(batchCtx).Where("id IN ?", ids).Delete(&models.RequestLog{})
			}
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

		// Log progress for large cleanup operations
		// Uses LargeCleanupThreshold for consistency with other batch operations
		// Track next threshold to ensure logging even when batch sizes don't divide evenly
		if totalDeleted >= nextLogAt {
			logrus.WithField("deleted_so_far", totalDeleted).Debug("Log cleanup progress")
			nextLogAt += int64(LargeCleanupThreshold)
		}

		// If deleted count is less than batch size, we're done
		if deletedCount < int64(batchSize) {
			break
		}

		// Small delay between batches to reduce lock contention
		// Increased from 50ms to 100ms for better concurrency with other operations
		time.Sleep(100 * time.Millisecond)
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
