package handler

import (
	"sync"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/response"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
)

// taskStatusCache provides short-term caching for task status to reduce store access frequency.
// This is especially important during heavy DB operations (e.g., deleting 50K keys) where
// frequent polling (every 2s) can cause timeouts for other queries.
type taskStatusCache struct {
	mu       sync.RWMutex
	status   *services.TaskStatus
	cachedAt time.Time
	cacheTTL time.Duration // Short TTL (500ms) to balance freshness and DB load
}

var (
	globalTaskStatusCache = &taskStatusCache{
		cacheTTL: 500 * time.Millisecond, // 500ms cache reduces polling impact by 75% (2s → 500ms effective rate)
	}
)

// get returns cached status if valid, otherwise returns nil
// Returns a copy to prevent callers from mutating cached data
// Note: Shallow copy is safe because:
//  1. Result field stores value-only structs (KeyImportResult, KeyDeleteResult, ManualValidationResult)
//     All these types contain only primitive fields (int, string) with no reference types
//  2. FinishedAt is *time.Time, but time.Time is immutable - copying the pointer is safe
//  3. All other fields (string, bool, int, time.Time) are value types
func (c *taskStatusCache) get() *services.TaskStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.status != nil && time.Since(c.cachedAt) < c.cacheTTL {
		// Return a copy to prevent external mutation
		copy := *c.status
		return &copy
	}
	return nil
}

// set updates the cache with new status
// Stores a copy to prevent caller from mutating cached data
// Note: Shallow copy is safe - see get() method for detailed safety analysis
func (c *taskStatusCache) set(status *services.TaskStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store a copy to prevent caller from mutating cached data
	if status != nil {
		copy := *status
		c.status = &copy
	} else {
		c.status = nil
	}
	c.cachedAt = time.Now()
}

// GetTaskStatus handles requests for the status of the global long-running task.
// Uses short-term caching (500ms) to reduce store access during frequent polling.
// Cache automatically expires after 500ms to ensure reasonably fresh data.
func (s *Server) GetTaskStatus(c *gin.Context) {
	// Try cache first
	if cached := globalTaskStatusCache.get(); cached != nil {
		response.Success(c, cached)
		return
	}

	// Cache miss - fetch from store
	taskStatus, err := s.TaskService.GetTaskStatus()
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrInternalServer, "task.get_status_failed")
		return
	}

	// Update cache
	globalTaskStatusCache.set(taskStatus)

	response.Success(c, taskStatus)
}
