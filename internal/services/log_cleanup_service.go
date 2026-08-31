package services

import (
	"context"
	"fmt"
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
	// ctx/cancel form the shutdown signal for batch cleanup: Stop() cancels
	// them so batch contexts derived from ctx (and inter-batch waits) abort
	// promptly instead of draining a large backlog first.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewLogCleanupService creates a new log cleanup service.
func NewLogCleanupService(db *gorm.DB, settingsManager *config.SystemSettingsManager) *LogCleanupService {
	// The cancellable context is the shutdown signal wired into Stop(): closing
	// stopCh alone would leave in-flight batch contexts and inter-batch delays
	// running until the batch timeout or the backlog drains.
	ctx, cancel := context.WithCancel(context.Background())
	return &LogCleanupService{
		db:              db,
		settingsManager: settingsManager,
		stopCh:          make(chan struct{}),
		ctx:             ctx,
		cancel:          cancel,
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

	// Cancel the service context so batch contexts derived from it (see
	// cleanupExpiredLogs and deleteExpiredStatsTable) abort immediately instead
	// of timing out or draining a large backlog, letting the wg.Wait() below
	// return promptly.
	s.cancel()

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
	s.cleanupExpiredStats()

	ticker := time.NewTicker(2 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupExpiredLogs()
			s.cleanupExpiredStats()
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

	totalDeleted := int64(0)
	nextLogAt := int64(LargeCleanupThreshold) // Track next threshold for progress logging
	dialect := s.db.Dialector.Name()
	batchSize := LogCleanupBatchSize
	if dialect == "sqlite" {
		batchSize = SQLiteLogCleanupBatchSize
	}

	logrus.WithFields(logrus.Fields{
		"cutoff_time":    cutoffTime.Format(time.RFC3339),
		"retention_days": retentionDays,
		"dialect":        dialect,
	}).Debug("Starting log cleanup")

	// Batch contexts and the inter-batch delay derive from the service's
	// cancellable context so Stop() cancelling s.ctx aborts them promptly,
	// keeping shutdown responsive even with a large backlog queued. Direct
	// struct construction (tests bypass NewLogCleanupService) leaves s.ctx nil:
	// fall back to Background so those callers keep working unchanged.
	batchParent := s.ctx
	if batchParent == nil {
		batchParent = context.Background()
	}

	for {
		// Timeout set to 60s for batch deletion operations
		// Note: This exceeds typical GORM recommendations (5-10s per operation) but is intentional:
		// 1. Background cleanup task with no user-facing latency requirements
		// 2. Large batches on slower systems need more time
		// 3. Testing shows 60s prevents timeout errors while maintaining reasonable progress
		// 4. The batch context derives from the service's cancellable context
		//    (batchParent, not context.Background()), so Stop() cancelling s.ctx
		//    aborts an in-flight batch promptly instead of draining the backlog.
		batchCtx, cancel := context.WithTimeout(batchParent, 60*time.Second)
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
			result = s.deleteSQLiteExpiredLogsBatch(batchCtx, cutoffTime, batchSize)
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
		// Wait for the delay unless the service is stopping: then abort promptly.
		select {
		case <-time.After(100 * time.Millisecond):
		case <-batchParent.Done():
			return
		}
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

func (s *LogCleanupService) deleteSQLiteExpiredLogsBatch(ctx context.Context, cutoffTime time.Time, batchSize int) *gorm.DB {
	return s.db.WithContext(ctx).Exec(`
		DELETE FROM request_logs
		WHERE rowid IN (
			SELECT rowid
			FROM request_logs
			WHERE timestamp < ?
			ORDER BY timestamp
			LIMIT ?
		)
	`, cutoffTime, batchSize)
}

// statsTables lists the hourly stats tables cleaned by cleanupExpiredStats.
// Both have a single-column time index (added by migration v1.30.0) so the
// time-prefixed batch deletes below use the index instead of scanning.
var statsTables = []string{"group_hourly_stats", "model_token_hourly_stats"}

// cleanupExpiredStats deletes rows older than StatsRetentionDays from the two
// hourly stats tables. Dialect branches mirror cleanupExpiredLogs; each table
// is cleaned independently with bounded batches so the time index is always
// used (the index was added by migration v1.30.0).
func (s *LogCleanupService) cleanupExpiredStats() {
	cutoffTime := time.Now().AddDate(0, 0, -StatsRetentionDays).UTC()
	dialect := s.db.Dialector.Name()
	// Stats tables use their own batch sizes: smaller than log cleanup batches so
	// each delete keeps the write lock window short even though stats rows are large.
	batchSize := HourlyStatsBatchSize
	if dialect == "sqlite" {
		batchSize = HourlyStatsBatchSizeSQLite
	}

	logrus.WithFields(logrus.Fields{
		"cutoff_time":    cutoffTime.Format(time.RFC3339),
		"retention_days": StatsRetentionDays,
		"dialect":        dialect,
	}).Debug("Starting stats cleanup")

	for _, table := range statsTables {
		s.deleteExpiredStatsTable(table, cutoffTime, dialect, batchSize)
	}
}

// deleteExpiredStatsTable deletes expired rows from one stats table using
// dialect-specific batch deletion. The table name is drawn from the fixed set
// statsTables and is never user-supplied, so fmt.Sprintf is safe here.
func (s *LogCleanupService) deleteExpiredStatsTable(table string, cutoffTime time.Time, dialect string, batchSize int) {
	totalDeleted := int64(0)
	nextLogAt := int64(LargeCleanupThreshold)

	logrus.WithFields(logrus.Fields{
		"table": table,
	}).Debug("Cleaning up expired stats table")

	// Same pattern as cleanupExpiredLogs: derive batch context from the service's
	// cancellable context so Stop() cancelling s.ctx aborts in-flight batches;
	// nil guard for tests that bypass NewLogCleanupService.
	batchParent := s.ctx
	if batchParent == nil {
		batchParent = context.Background()
	}

	for {
		batchCtx, cancel := context.WithTimeout(batchParent, 60*time.Second)
		var result *gorm.DB

		switch dialect {
		case "postgres":
			result = s.db.WithContext(batchCtx).Exec(fmt.Sprintf(`
				DELETE FROM %s
				WHERE ctid IN (
					SELECT ctid FROM %s
					WHERE time < $1
					LIMIT $2
				)
			`, table, table), cutoffTime, batchSize)
		case "mysql":
			result = s.db.WithContext(batchCtx).Exec(
				fmt.Sprintf("DELETE FROM %s WHERE time < ? ORDER BY time LIMIT ?", table),
				cutoffTime, batchSize,
			)
		case "sqlite":
			result = s.db.WithContext(batchCtx).Exec(fmt.Sprintf(`
				DELETE FROM %s
				WHERE rowid IN (
					SELECT rowid FROM %s
					WHERE time < ?
					ORDER BY time
					LIMIT ?
				)
			`, table, table), cutoffTime, batchSize)
		default:
			// Fallback for unsupported dialects: select IDs then delete.
			logrus.Warnf("Stats cleanup using fallback deletion for unsupported dialect: %s", dialect)
			var ids []uint
			err := s.db.WithContext(batchCtx).Table(table).
				Where("time < ?", cutoffTime).
				Limit(batchSize).
				Pluck("id", &ids).Error
			if err != nil {
				result = &gorm.DB{Error: err}
			} else if len(ids) == 0 {
				result = &gorm.DB{RowsAffected: 0}
			} else {
				result = s.db.WithContext(batchCtx).Table(table).
					Where("id IN ?", ids).
					Delete(nil)
			}
		}
		cancel()

		if result.Error != nil {
			if utils.IsTransientDBError(result.Error) {
				logrus.WithError(result.Error).Warnf("Cleanup of %s failed due to transient DB error", table)
				return
			}
			logrus.WithError(result.Error).Errorf("Failed to cleanup expired %s", table)
			return
		}

		deletedCount := result.RowsAffected
		totalDeleted += deletedCount

		if totalDeleted >= nextLogAt {
			logrus.WithFields(logrus.Fields{
				"table":          table,
				"deleted_so_far": totalDeleted,
			}).Debug("Stats cleanup progress")
			nextLogAt += int64(LargeCleanupThreshold)
		}

		if deletedCount < int64(batchSize) {
			break
		}

		// Wait for the inter-batch delay unless the service is stopping: then
		// abort promptly so Stop() returns without draining the backlog.
		select {
		case <-time.After(100 * time.Millisecond):
		case <-batchParent.Done():
			return
		}
	}

	if totalDeleted > 0 {
		logrus.WithFields(logrus.Fields{
			"table":         table,
			"deleted_count": totalDeleted,
			"cutoff_time":   cutoffTime.Format(time.RFC3339),
		}).Info("Successfully cleaned up expired stats")
	} else {
		logrus.Debugf("No expired %s rows found to cleanup", table)
	}
}
