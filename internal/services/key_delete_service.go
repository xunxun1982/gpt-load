package services

import (
	"context"
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
)

const (
	// deleteChunkSize defines the batch size for key deletion operations.
	// Uses DefaultDeleteChunkSize from thresholds.go for consistency with other batch operations.
	deleteChunkSize = DefaultDeleteChunkSize
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
	go s.runDeleteAllGroupKeys(group)
	return status, nil
}

// StartDeleteInvalidGroupKeys starts a serialized global task to delete all invalid keys in a group.
// Similar to StartDeleteAllGroupKeys but only targets invalid keys.
func (s *KeyDeleteService) StartDeleteInvalidGroupKeys(group *models.Group, total int) (*TaskStatus, error) {
	status, err := s.TaskService.StartTask(TaskTypeKeyDelete, group.Name, total)
	if err != nil {
		return nil, err
	}
	go s.runDeleteInvalidGroupKeys(group)
	return status, nil
}

// StartRestoreInvalidGroupKeys starts a serialized global task to restore all invalid keys in a group.
// Similar to StartDeleteInvalidGroupKeys but restores keys instead of deleting them.
func (s *KeyDeleteService) StartRestoreInvalidGroupKeys(group *models.Group, total int) (*TaskStatus, error) {
	status, err := s.TaskService.StartTask(TaskTypeKeyRestore, group.Name, total)
	if err != nil {
		return nil, err
	}
	go s.runRestoreInvalidGroupKeys(group)
	return status, nil
}

func (s *KeyDeleteService) runDelete(group *models.Group, keys []string) {
	// Recover from panics to prevent task from being stuck in "running" state
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic in runDelete for group %d: %v", group.ID, r)
			logrus.WithFields(logrus.Fields{
				"groupID": group.ID,
				"panic":   r,
			}).Error("Panic recovered in runDelete")
			if s.TaskService != nil {
				_ = s.TaskService.EndTask(nil, err)
			}
		}
	}()

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
func (s *KeyDeleteService) runDeleteAllGroupKeys(group *models.Group) {
	// Recover from panics to prevent task from being stuck in "running" state
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic in runDeleteAllGroupKeys for group %d: %v", group.ID, r)
			logrus.WithFields(logrus.Fields{
				"groupID": group.ID,
				"panic":   r,
			}).Error("Panic recovered in runDeleteAllGroupKeys")
			if s.TaskService != nil {
				_ = s.TaskService.EndTask(nil, err)
			}
		}
	}()

	// Use background context with no timeout for large deletions
	// The deletion itself has internal chunking and progress tracking
	ctx := context.Background()

	// Progress callback to update task status
	progressCallback := func(deleted int64) {
		if err := s.TaskService.UpdateProgress(int(deleted)); err != nil {
			logrus.Warnf("Failed to update delete-all-keys task progress for group %d: %v", group.ID, err)
		}
	}

	deleted, err := s.KeyService.KeyProvider.RemoveAllKeys(ctx, group.ID, progressCallback)
	if err != nil {
		if endErr := s.TaskService.EndTask(nil, err); endErr != nil {
			logrus.Warnf("Failed to end delete-all-keys task for group %d: %v (original: %v)", group.ID, endErr, err)
		}
		return
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

// runDeleteInvalidGroupKeys performs deletion of all invalid keys using the provider's chunked delete.
func (s *KeyDeleteService) runDeleteInvalidGroupKeys(group *models.Group) {
	// Recover from panics to prevent task from being stuck in "running" state
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic in runDeleteInvalidGroupKeys for group %d: %v", group.ID, r)
			logrus.WithFields(logrus.Fields{
				"groupID": group.ID,
				"panic":   r,
			}).Error("Panic recovered in runDeleteInvalidGroupKeys")
			if s.TaskService != nil {
				_ = s.TaskService.EndTask(nil, err)
			}
		}
	}()

	// RemoveInvalidKeys internally uses removeKeysByStatus with chunking
	// Note: RemoveInvalidKeys doesn't support progress callback yet, but the operation
	// is still chunked internally for memory efficiency
	deleted, err := s.KeyService.KeyProvider.RemoveInvalidKeys(group.ID)
	if err != nil {
		if endErr := s.TaskService.EndTask(nil, err); endErr != nil {
			logrus.Warnf("Failed to end delete-invalid-keys task for group %d: %v (original: %v)", group.ID, endErr, err)
		}
		return
	}

	// Invalidate cache after deleting keys
	if s.KeyService.CacheInvalidationCallback != nil && deleted > 0 {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	result := KeyDeleteResult{DeletedCount: int(deleted), IgnoredCount: 0}
	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Warnf("Failed to end delete-invalid-keys task with success result for group %d: %v", group.ID, endErr)
	}
}

// runRestoreInvalidGroupKeys performs restoration of all invalid keys using the provider's restore method.
func (s *KeyDeleteService) runRestoreInvalidGroupKeys(group *models.Group) {
	// Recover from panics to prevent task from being stuck in "running" state
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic in runRestoreInvalidGroupKeys for group %d: %v", group.ID, r)
			logrus.WithFields(logrus.Fields{
				"groupID": group.ID,
				"panic":   r,
			}).Error("Panic recovered in runRestoreInvalidGroupKeys")
			if s.TaskService != nil {
				_ = s.TaskService.EndTask(nil, err)
			}
		}
	}()

	// RestoreKeys internally updates status from invalid to active
	// Note: RestoreKeys doesn't support progress callback yet, but the operation
	// is still chunked internally for memory efficiency
	restored, err := s.KeyService.KeyProvider.RestoreKeys(group.ID)
	if err != nil {
		if endErr := s.TaskService.EndTask(nil, err); endErr != nil {
			logrus.Warnf("Failed to end restore-invalid-keys task for group %d: %v (original: %v)", group.ID, endErr, err)
		}
		return
	}

	// Invalidate cache after restoring keys
	if s.KeyService.CacheInvalidationCallback != nil && restored > 0 {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	// Get total keys in group for result
	totalInGroup, err := s.KeyService.getTotalKeysInGroup(group.ID)
	if err != nil {
		logrus.Warnf("Failed to get total keys in group %d: %v", group.ID, err)
		totalInGroup = 0 // Fallback to 0 if query fails
	}

	result := RestoreKeysResult{RestoredCount: int(restored), IgnoredCount: 0, TotalInGroup: totalInGroup}
	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Warnf("Failed to end restore-invalid-keys task with success result for group %d: %v", group.ID, endErr)
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
