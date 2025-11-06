package services

import (
	"context"
	"gpt-load/internal/config"
	"gpt-load/internal/models"
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

// cleanupExpiredLogs cleans up expired request logs using batch deletion for better performance
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

	// Use batch deletion to avoid slow SQL operations
	// Delete in batches to reduce lock time and improve performance
	// Use smaller batch size and delete by ID for better performance
	batchSize := 500
	totalDeleted := int64(0)

	for {
		// First, query IDs of records to delete (using index on timestamp)
		var ids []string
		if err := s.db.Model(&models.RequestLog{}).
			Where("timestamp < ?", cutoffTime).
			Limit(batchSize).
			Pluck("id", &ids).Error; err != nil {
			logrus.WithError(err).Error("Failed to query expired request log IDs")
			return
		}

		// If no more rows to delete, break
		if len(ids) == 0 {
			break
		}

		// Delete by ID (primary key, much faster than timestamp-based delete)
		result := s.db.Where("id IN ?", ids).Delete(&models.RequestLog{})
		if result.Error != nil {
			logrus.WithError(result.Error).Error("Failed to cleanup expired request logs")
			return
		}

		deleted := result.RowsAffected
		totalDeleted += deleted

		// If deleted count is less than batch size, we're done
		if deleted < int64(batchSize) {
			break
		}

		// Small delay between batches to avoid overwhelming the database
		time.Sleep(10 * time.Millisecond)
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
