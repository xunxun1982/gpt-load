package services

import (
	"testing"
	"time"

	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewTaskService tests task service creation
func TestNewTaskService(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	assert.NotNil(t, svc)
	assert.NotNil(t, svc.store)
}

// TestStartTask tests starting a new task
func TestStartTask(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start a task
	status, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.True(t, status.IsRunning)
	assert.Equal(t, TaskTypeKeyImport, status.TaskType)
	assert.Equal(t, "test-group", status.GroupName)
	assert.Equal(t, 100, status.Total)
	assert.Equal(t, 0, status.Processed)
}

// TestStartTaskWhenAlreadyRunning tests starting a task when one is already running
func TestStartTaskWhenAlreadyRunning(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start first task
	_, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// Try to start second task
	_, err = svc.StartTask(TaskTypeKeyValidation, "test-group-2", 50)
	assert.Error(t, err)
	assert.Equal(t, ErrTaskAlreadyRunning, err)
}

// TestGetTaskStatus tests getting task status
func TestGetTaskStatus(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Get status when no task is running
	status, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.False(t, status.IsRunning)

	// Start a task
	_, err = svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// Get status of running task
	status, err = svc.GetTaskStatus()
	require.NoError(t, err)
	assert.True(t, status.IsRunning)
	assert.Equal(t, TaskTypeKeyImport, status.TaskType)
}

// TestUpdateProgress tests updating task progress
func TestUpdateProgress(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start a task
	_, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// Update progress
	err = svc.UpdateProgress(50)
	require.NoError(t, err)

	// Verify progress
	status, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.Equal(t, 50, status.Processed)

	// Update progress again
	err = svc.UpdateProgress(75)
	require.NoError(t, err)

	// Verify updated progress
	status, err = svc.GetTaskStatus()
	require.NoError(t, err)
	assert.Equal(t, 75, status.Processed)
}

// TestEndTask tests ending a task
func TestEndTask(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start a task
	_, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// Update progress
	err = svc.UpdateProgress(100)
	require.NoError(t, err)

	// Add a small delay to ensure duration > 0
	time.Sleep(10 * time.Millisecond)

	// End task successfully
	result := map[string]interface{}{"added": 100}
	err = svc.EndTask(result, nil)
	require.NoError(t, err)

	// Verify task is no longer running
	status, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.False(t, status.IsRunning)
	assert.NotNil(t, status.FinishedAt)
	assert.NotNil(t, status.Result)
	assert.Greater(t, status.DurationSeconds, 0.0)
}

// TestEndTaskWithError tests ending a task with an error
func TestEndTaskWithError(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start a task
	_, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// End task with error
	taskErr := assert.AnError
	err = svc.EndTask(nil, taskErr)
	require.NoError(t, err)

	// Verify task ended with error
	status, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.False(t, status.IsRunning)
	assert.NotEmpty(t, status.Error)
	assert.Nil(t, status.Result)
}

// TestTaskLifecycle tests complete task lifecycle
func TestTaskLifecycle(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// 1. No task running initially
	status, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.False(t, status.IsRunning)

	// 2. Start a task
	startStatus, err := svc.StartTask(TaskTypeKeyValidation, "test-group", 50)
	require.NoError(t, err)
	assert.True(t, startStatus.IsRunning)
	assert.Equal(t, 0, startStatus.Processed)

	// 3. Update progress multiple times
	for i := 10; i <= 50; i += 10 {
		err = svc.UpdateProgress(i)
		require.NoError(t, err)

		status, err = svc.GetTaskStatus()
		require.NoError(t, err)
		assert.Equal(t, i, status.Processed)
	}

	// Add a small delay to ensure duration > 0
	time.Sleep(10 * time.Millisecond)

	// 4. End task
	result := map[string]interface{}{"validated": 50, "invalid": 5}
	err = svc.EndTask(result, nil)
	require.NoError(t, err)

	// 5. Verify final status
	finalStatus, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.False(t, finalStatus.IsRunning)
	assert.Equal(t, 50, finalStatus.Processed)
	assert.NotNil(t, finalStatus.Result)
	assert.NotNil(t, finalStatus.FinishedAt)
	assert.Greater(t, finalStatus.DurationSeconds, 0.0)
}

// TestMultipleTaskTypes tests different task types
func TestMultipleTaskTypes(t *testing.T) {
	taskTypes := []string{
		TaskTypeKeyValidation,
		TaskTypeKeyImport,
		TaskTypeKeyDelete,
	}

	for _, taskType := range taskTypes {
		t.Run(taskType, func(t *testing.T) {
			memStore := store.NewMemoryStore()
			svc := NewTaskService(memStore)

			status, err := svc.StartTask(taskType, "test-group", 100)
			require.NoError(t, err)
			assert.Equal(t, taskType, status.TaskType)

			err = svc.EndTask(nil, nil)
			require.NoError(t, err)
		})
	}
}

// TestUpdateProgressWhenNoTaskRunning tests updating progress when no task is running
func TestUpdateProgressWhenNoTaskRunning(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Try to update progress when no task is running
	err := svc.UpdateProgress(50)
	require.NoError(t, err) // Should not error, just no-op
}

// TestEndTaskWhenNoTaskRunning tests ending a task when no task is running
func TestEndTaskWhenNoTaskRunning(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Try to end task when no task is running
	err := svc.EndTask(nil, nil)
	require.NoError(t, err) // Should not error, just no-op
}

// TestTaskStatusDuration tests duration calculation
func TestTaskStatusDuration(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start a task
	_, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// End task
	err = svc.EndTask(nil, nil)
	require.NoError(t, err)

	// Verify duration is calculated
	status, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.Greater(t, status.DurationSeconds, 0.0)
	assert.GreaterOrEqual(t, status.DurationSeconds, 0.1) // At least 100ms
}

// TestTaskStatusSerialization tests task status serialization
func TestTaskStatusSerialization(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start a task
	_, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// Update progress
	err = svc.UpdateProgress(50)
	require.NoError(t, err)

	// Get status
	status1, err := svc.GetTaskStatus()
	require.NoError(t, err)

	// Get status again (should deserialize from store)
	status2, err := svc.GetTaskStatus()
	require.NoError(t, err)

	// Verify both statuses are identical
	assert.Equal(t, status1.TaskType, status2.TaskType)
	assert.Equal(t, status1.IsRunning, status2.IsRunning)
	assert.Equal(t, status1.Processed, status2.Processed)
	assert.Equal(t, status1.Total, status2.Total)
}

// TestConcurrentTaskOperations tests concurrent access to task service
func TestConcurrentTaskOperations(t *testing.T) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	// Start a task
	_, err := svc.StartTask(TaskTypeKeyImport, "test-group", 100)
	require.NoError(t, err)

	// Concurrent progress updates
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(progress int) {
			_ = svc.UpdateProgress(progress * 10)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify task is still running
	status, err := svc.GetTaskStatus()
	require.NoError(t, err)
	assert.True(t, status.IsRunning)
}

// BenchmarkStartTask benchmarks starting a task
func BenchmarkStartTask(b *testing.B) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.StartTask(TaskTypeKeyImport, "test-group", 100)
		_ = svc.EndTask(nil, nil)
	}
}

// BenchmarkUpdateProgress benchmarks progress updates
func BenchmarkUpdateProgress(b *testing.B) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	_, _ = svc.StartTask(TaskTypeKeyImport, "test-group", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = svc.UpdateProgress(i % 100)
	}
}

// BenchmarkGetTaskStatus benchmarks getting task status
func BenchmarkGetTaskStatus(b *testing.B) {
	memStore := store.NewMemoryStore()
	svc := NewTaskService(memStore)

	_, _ = svc.StartTask(TaskTypeKeyImport, "test-group", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetTaskStatus()
	}
}
