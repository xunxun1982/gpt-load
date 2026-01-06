// Package mcpskills provides MCP service management and API bridge execution
package mcpskills

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// CacheCleanupService handles periodic cleanup of expired MCP tool cache entries.
// It runs as a background service and removes hard-expired cache entries daily.
type CacheCleanupService struct {
	mcpService *Service
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// NewCacheCleanupService creates a new cache cleanup service.
func NewCacheCleanupService(mcpService *Service) *CacheCleanupService {
	return &CacheCleanupService{
		mcpService: mcpService,
		stopCh:     make(chan struct{}),
	}
}

// Start starts the cache cleanup service.
func (s *CacheCleanupService) Start() {
	s.wg.Add(1)
	go s.run()
	logrus.Debug("MCP cache cleanup service started")
}

// Stop stops the cache cleanup service gracefully.
func (s *CacheCleanupService) Stop(ctx context.Context) {
	close(s.stopCh)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("MCP CacheCleanupService stopped gracefully.")
	case <-ctx.Done():
		logrus.Warn("MCP CacheCleanupService stop timed out.")
	}
}

// run executes the main cleanup loop.
func (s *CacheCleanupService) run() {
	defer s.wg.Done()

	// Initial delay to allow database initialization to complete
	// This prevents slow SQL during startup when DB is busy with other tasks
	select {
	case <-time.After(60 * time.Second):
	case <-s.stopCh:
		return
	}

	// Perform initial cleanup after delay
	s.cleanupExpiredCache()

	// Run cleanup once per day (24 hours)
	// Tool cache hard expiry is 24 hours, so daily cleanup is sufficient
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupExpiredCache()
		case <-s.stopCh:
			return
		}
	}
}

// cleanupExpiredCache removes all hard-expired tool cache entries from the database.
func (s *CacheCleanupService) cleanupExpiredCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deleted, err := s.mcpService.CleanExpiredToolCache(ctx)
	if err != nil {
		logrus.WithError(err).Warn("Failed to cleanup expired MCP tool cache")
		return
	}

	if deleted > 0 {
		logrus.WithField("deleted_count", deleted).Info("Successfully cleaned up expired MCP tool cache entries")
	} else {
		logrus.Debug("No expired MCP tool cache entries found to cleanup")
	}
}
