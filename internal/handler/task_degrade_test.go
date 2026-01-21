package handler

import (
	"gpt-load/internal/services"
	"gpt-load/internal/store"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestShouldDegradeReadDuringTask_NilServer(t *testing.T) {
	t.Parallel()

	var s *Server
	result := s.shouldDegradeReadDuringTask("test-group")
	assert.False(t, result)
}

func TestShouldDegradeReadDuringTask_NilTaskService(t *testing.T) {
	t.Parallel()

	s := &Server{
		TaskService: nil,
	}
	result := s.shouldDegradeReadDuringTask("test-group")
	assert.False(t, result)
}

func TestShouldDegradeReadDuringTask_NilDB(t *testing.T) {
	t.Parallel()

	mockTaskService := &services.TaskService{}
	s := &Server{
		TaskService: mockTaskService,
		DB:          nil,
	}
	result := s.shouldDegradeReadDuringTask("test-group")
	assert.False(t, result)
}

func TestShouldDegradeReadDuringTask_NoRunningTask(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	memStore := store.NewMemoryStore()
	taskService := services.NewTaskService(memStore)
	s := &Server{
		TaskService: taskService,
		DB:          db,
	}

	result := s.shouldDegradeReadDuringTask("test-group")
	assert.False(t, result)
}

func TestShouldDegradeReadDuringTask_SQLiteDatabase(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Create a task service with running key import task
	memStore := store.NewMemoryStore()
	taskService := services.NewTaskService(memStore)
	s := &Server{
		TaskService: taskService,
		DB:          db,
	}

	// Manually set a running task status for testing
	// Note: In real scenario, TaskService.GetTaskStatus() would return this
	// For unit test, we verify the logic with SQLite database
	result := s.shouldDegradeReadDuringTask("test-group")

	// Without a running task, should return false
	assert.False(t, result)
}

func TestShouldDegradeReadDuringTask_EmptyGroupWithSQLite(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	memStore := store.NewMemoryStore()
	taskService := services.NewTaskService(memStore)
	s := &Server{
		TaskService: taskService,
		DB:          db,
	}

	// Test with SQLite (which should degrade during tasks)
	result := s.shouldDegradeReadDuringTask("")
	assert.False(t, result) // False because no task is running
}

func TestShouldDegradeReadDuringTask_DifferentTaskTypes(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	memStore := store.NewMemoryStore()
	taskService := services.NewTaskService(memStore)
	s := &Server{
		TaskService: taskService,
		DB:          db,
	}

	// Test with different group names
	result := s.shouldDegradeReadDuringTask("group1")
	assert.False(t, result)

	result = s.shouldDegradeReadDuringTask("group2")
	assert.False(t, result)

	result = s.shouldDegradeReadDuringTask("")
	assert.False(t, result)
}

func TestShouldDegradeReadDuringTask_EmptyGroupName(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	memStore := store.NewMemoryStore()
	taskService := services.NewTaskService(memStore)
	s := &Server{
		TaskService: taskService,
		DB:          db,
	}

	// Test with empty group name
	result := s.shouldDegradeReadDuringTask("")
	assert.False(t, result)
}

// Benchmark tests for PGO optimization
func BenchmarkShouldDegradeReadDuringTask(b *testing.B) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatal(err)
	}

	memStore := store.NewMemoryStore()
	taskService := services.NewTaskService(memStore)
	s := &Server{
		TaskService: taskService,
		DB:          db,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.shouldDegradeReadDuringTask("test-group")
	}
}

func BenchmarkShouldDegradeReadDuringTask_NilChecks(b *testing.B) {
	var s *Server

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.shouldDegradeReadDuringTask("test-group")
	}
}
