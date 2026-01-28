package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gpt-load/internal/services"
	"gpt-load/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetTaskStatusCache resets the global task status cache to ensure test isolation
func resetTaskStatusCache() {
	globalTaskStatusCache.mu.Lock()
	globalTaskStatusCache.status = nil
	globalTaskStatusCache.cachedAt = time.Time{}
	globalTaskStatusCache.mu.Unlock()
}

func TestGetTaskStatus_NilService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetTaskStatusCache()

	server := &Server{TaskService: nil}
	router := gin.New()
	router.GET("/task/status", server.GetTaskStatus)

	req := httptest.NewRequest(http.MethodGet, "/task/status", nil)
	w := httptest.NewRecorder()

	// Will panic if TaskService is nil
	assert.Panics(t, func() {
		router.ServeHTTP(w, req)
	})
}

func TestGetTaskStatus_Caching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetTaskStatusCache()
	t.Cleanup(resetTaskStatusCache)

	// Create in-memory store and task service
	memStore := store.NewMemoryStore()
	taskService := services.NewTaskService(memStore)

	server := &Server{TaskService: taskService}
	router := gin.New()
	router.GET("/task/status", server.GetTaskStatus)

	// Start a task
	_, err := taskService.StartTask(services.TaskTypeKeyImport, "test-group", 1000)
	require.NoError(t, err)

	// First request - should hit store
	req1 := httptest.NewRequest(http.MethodGet, "/task/status", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Second request immediately after - should hit cache
	req2 := httptest.NewRequest(http.MethodGet, "/task/status", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	// Both responses should be identical
	assert.Equal(t, w1.Body.String(), w2.Body.String())

	// Wait for cache to expire (TTL + buffer for reliability)
	// Use cache TTL instead of magic number to ensure test reliability
	time.Sleep(globalTaskStatusCache.cacheTTL + 150*time.Millisecond)

	// Update task progress
	err = taskService.UpdateProgress(500)
	require.NoError(t, err)

	// Third request after cache expiry - should fetch updated status
	req3 := httptest.NewRequest(http.MethodGet, "/task/status", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusOK, w3.Code)

	// Response should be different from first two (progress updated)
	assert.NotEqual(t, w1.Body.String(), w3.Body.String())
	assert.Contains(t, w3.Body.String(), "500") // Check for updated progress
}
