package services

import (
	"context"
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	deleteChunkSize = 1000
)

// KeyDeleteResult holds the result of a delete task.
type KeyDeleteResult struct {
	DeletedCount int `json:"deleted_count"`
	IgnoredCount int `json:"ignored_count"`
}

// KeyDeleteService handles the asynchronous deletion of a large number of keys.
type KeyDeleteService struct {
	TaskService *TaskService
	KeyService  *KeyService
}

// NewKeyDeleteService creates a new KeyDeleteService.
func NewKeyDeleteService(taskService *TaskService, keyService *KeyService) *KeyDeleteService {
	return &KeyDeleteService{
		TaskService: taskService,
		KeyService:  keyService,
	}
}

// StartDeleteTask initiates a new asynchronous key deletion task.
func (s *KeyDeleteService) StartDeleteTask(group *models.Group, keysText string) (*TaskStatus, error) {
	keys := s.KeyService.ParseKeysFromText(keysText)
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid keys found in the input text")
	}

	initialStatus, err := s.TaskService.StartTask(TaskTypeKeyDelete, group.Name, len(keys))
	if err != nil {
		return nil, err
	}

	go s.runDelete(group, keys)

	return initialStatus, nil
}

// StartDeleteAllGroupKeys starts a serialized global task to delete all keys in a group.
// It leverages TaskService to ensure only one heavy delete/import runs at a time.
func (s *KeyDeleteService) StartDeleteAllGroupKeys(group *models.Group, total int) (*TaskStatus, error) {
	status, err := s.TaskService.StartTask(TaskTypeKeyDelete, group.Name, total)
	if err != nil {
		return nil, err
	}
	go s.runDeleteAllGroupKeys(group, total)
	return status, nil
}

func (s *KeyDeleteService) runDelete(group *models.Group, keys []string) {
	progressCallback := func(processed int) {
		if err := s.TaskService.UpdateProgress(processed); err != nil {
			logrus.Warnf("Failed to update task progress for group %d: %v", group.ID, err)
		}
	}

	deletedCount, ignoredCount, err := s.processAndDeleteKeys(group.ID, keys, progressCallback)
	if err != nil {
		if endErr := s.TaskService.EndTask(nil, err); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v (original error: %v)", group.ID, endErr, err)
		}
		return
	}

	result := KeyDeleteResult{
		DeletedCount: deletedCount,
		IgnoredCount: ignoredCount,
	}

	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Errorf("Failed to end task with success result for group %d: %v", group.ID, endErr)
	}
}

// runDeleteAllGroupKeys performs the full-group deletion using the provider's chunked delete.
func (s *KeyDeleteService) runDeleteAllGroupKeys(group *models.Group, _ int) {
	// Create a context with timeout for the deletion operation
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	deleted, err := s.KeyService.KeyProvider.RemoveAllKeys(ctx, group.ID)
	if err != nil {
		if endErr := s.TaskService.EndTask(nil, err); endErr != nil {
			logrus.Warnf("Failed to end delete-all-keys task for group %d: %v (original: %v)", group.ID, endErr, err)
		}
		return
	}
	// Best-effort: mark progress as complete
	if progressErr := s.TaskService.UpdateProgress(int(deleted)); progressErr != nil {
		logrus.Warnf("Failed to update delete-all-keys task progress for group %d: %v", group.ID, progressErr)
	}

	// Invalidate cache after deleting keys
	if s.KeyService.CacheInvalidationCallback != nil && deleted > 0 {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	result := KeyDeleteResult{DeletedCount: int(deleted), IgnoredCount: 0}
	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Warnf("Failed to end delete-all-keys task with success result for group %d: %v", group.ID, endErr)
	}
}

// processAndDeleteKeys is the core function for deleting keys with progress tracking.
func (s *KeyDeleteService) processAndDeleteKeys(
	groupID uint,
	keys []string,
	progressCallback func(processed int),
) (deletedCount int, ignoredCount int, err error) {
	var totalDeletedCount int64
	var processedCount int

	err = utils.ProcessInChunks(keys, deleteChunkSize, func(chunk []string) error {
		deletedChunkCount, err := s.KeyService.KeyProvider.RemoveKeys(groupID, chunk)
		if err != nil {
			return err
		}

		totalDeletedCount += deletedChunkCount
		processedCount += len(chunk)

		if progressCallback != nil {
			progressCallback(processedCount)
		}
		return nil
	})

	if err != nil {
		return int(totalDeletedCount), len(keys) - int(totalDeletedCount), err
	}

	// Invalidate cache after deleting keys
	if s.KeyService.CacheInvalidationCallback != nil && totalDeletedCount > 0 {
		s.KeyService.CacheInvalidationCallback(groupID)
	}

	return int(totalDeletedCount), len(keys) - int(totalDeletedCount), nil
}
