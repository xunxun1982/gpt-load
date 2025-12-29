package services

import (
	"fmt"
	"gpt-load/internal/models"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// KeyImportResult holds the result of an import task.
type KeyImportResult struct {
	AddedCount   int `json:"added_count"`
	IgnoredCount int `json:"ignored_count"`
}

// KeyImportService handles the asynchronous import of a large number of keys.
type KeyImportService struct {
	TaskService   *TaskService
	KeyService    *KeyService
	BulkImportSvc *BulkImportService
}

// NewKeyImportService creates a new KeyImportService.
func NewKeyImportService(taskService *TaskService, keyService *KeyService, bulkImportSvc *BulkImportService) *KeyImportService {
	return &KeyImportService{
		TaskService:   taskService,
		KeyService:    keyService,
		BulkImportSvc: bulkImportSvc,
	}
}

// StartImportTask initiates a new asynchronous key import task.
func (s *KeyImportService) StartImportTask(group *models.Group, keysText string) (*TaskStatus, error) {
	keys := s.KeyService.ParseKeysFromText(keysText)
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid keys found in the input text")
	}

	initialStatus, err := s.TaskService.StartTask(TaskTypeKeyImport, group.Name, len(keys))
	if err != nil {
		return nil, err
	}

	go s.runImport(group, keys)

	return initialStatus, nil
}

// StartCopyTask initiates an asynchronous key copy task from source group.
// This method fetches and decrypts keys asynchronously for faster HTTP response.
func (s *KeyImportService) StartCopyTask(targetGroup *models.Group, sourceGroupID uint, copyOption string, estimatedKeyCount int) (*TaskStatus, error) {
	initialStatus, err := s.TaskService.StartTask(TaskTypeKeyImport, targetGroup.Name, estimatedKeyCount)
	if err != nil {
		return nil, err
	}

	go s.runCopyTask(targetGroup, sourceGroupID, copyOption)

	return initialStatus, nil
}

// runCopyTask performs the actual key copy operation asynchronously.
// It fetches keys from source group, decrypts them, and imports to target group.
func (s *KeyImportService) runCopyTask(targetGroup *models.Group, sourceGroupID uint, copyOption string) {
	startTime := time.Now()

	// Fetch source keys from database
	var sourceKeyData []struct {
		KeyValue string
	}
	query := s.KeyService.DB.Table("api_keys").Select("key_value").Where("group_id = ?", sourceGroupID)
	if copyOption == "valid_only" {
		query = query.Where("status = ?", models.KeyStatusActive)
	}
	if err := query.Scan(&sourceKeyData).Error; err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to fetch source keys: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", targetGroup.ID, endErr)
		}
		return
	}

	if len(sourceKeyData) == 0 {
		result := KeyImportResult{AddedCount: 0, IgnoredCount: 0}
		if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
			logrus.Errorf("Failed to end task for group %d: %v", targetGroup.ID, endErr)
		}
		return
	}

	// Update task total with actual key count
	if err := s.TaskService.UpdateProgress(0); err != nil {
		logrus.Warnf("Failed to update task progress: %v", err)
	}

	// Decrypt keys and prepare for import
	decryptedKeys := make([]string, 0, len(sourceKeyData))
	decryptErrors := 0
	for i, keyData := range sourceKeyData {
		decryptedKey, err := s.KeyService.EncryptionSvc.Decrypt(keyData.KeyValue)
		if err != nil {
			decryptErrors++
			continue
		}
		decryptedKeys = append(decryptedKeys, decryptedKey)

		// Update progress during decryption (every 500 keys)
		if (i+1)%500 == 0 {
			if err := s.TaskService.UpdateProgress(i + 1); err != nil {
				logrus.Warnf("Failed to update task progress: %v", err)
			}
		}
	}

	if decryptErrors > 0 {
		logrus.WithFields(logrus.Fields{
			"sourceGroupId": sourceGroupID,
			"targetGroupId": targetGroup.ID,
			"decryptErrors": decryptErrors,
		}).Warn("Some keys failed to decrypt during copy")
	}

	if len(decryptedKeys) == 0 {
		result := KeyImportResult{AddedCount: 0, IgnoredCount: len(sourceKeyData)}
		if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
			logrus.Errorf("Failed to end task for group %d: %v", targetGroup.ID, endErr)
		}
		return
	}

	// Use bulk import for the decrypted keys
	s.runBulkImportForCopy(targetGroup, decryptedKeys, decryptErrors, startTime)
}

// runBulkImportForCopy performs bulk import for copied keys.
func (s *KeyImportService) runBulkImportForCopy(group *models.Group, keys []string, priorIgnored int, startTime time.Time) {
	// Get existing key hashes for deduplication
	var existingHashes []string
	if err := s.KeyService.DB.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Pluck("key_hash", &existingHashes).Error; err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to check existing keys: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	existingHashMap := make(map[string]bool, len(existingHashes))
	for _, h := range existingHashes {
		existingHashMap[h] = true
	}

	// Prepare keys for bulk import
	newKeysToCreate := make([]models.APIKey, 0, len(keys))
	uniqueNewKeys := make(map[string]bool, len(keys))
	ignoredCount := priorIgnored

	for _, keyVal := range keys {
		trimmedKey := strings.TrimSpace(keyVal)
		if trimmedKey == "" || uniqueNewKeys[trimmedKey] {
			ignoredCount++
			continue
		}

		keyHash := s.KeyService.EncryptionSvc.Hash(trimmedKey)
		if existingHashMap[keyHash] {
			ignoredCount++
			continue
		}

		encryptedKey, err := s.KeyService.EncryptionSvc.Encrypt(trimmedKey)
		if err != nil {
			logrus.WithError(err).Debug("Failed to encrypt key, skipping")
			ignoredCount++
			continue
		}

		uniqueNewKeys[trimmedKey] = true
		newKeysToCreate = append(newKeysToCreate, models.APIKey{
			GroupID:  group.ID,
			KeyValue: encryptedKey,
			KeyHash:  keyHash,
			Status:   models.KeyStatusActive,
		})
	}

	if len(newKeysToCreate) == 0 {
		result := KeyImportResult{AddedCount: 0, IgnoredCount: ignoredCount}
		if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
			logrus.Errorf("Failed to end task for group %d: %v", group.ID, endErr)
		}
		return
	}

	// Use bulk import service for fast insertion
	logrus.WithFields(logrus.Fields{
		"groupId":  group.ID,
		"keyCount": len(newKeysToCreate),
	}).Info("Starting bulk import for copied keys")

	if err := s.BulkImportSvc.BulkInsertAPIKeys(newKeysToCreate); err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("bulk import failed: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	// Load keys to memory store after successful import
	if s.KeyService.KeyProvider != nil {
		if err := s.KeyService.KeyProvider.LoadGroupKeysToStore(group.ID); err != nil {
			logrus.WithFields(logrus.Fields{
				"groupId": group.ID,
				"error":   err,
			}).Error("Failed to load keys to store after bulk import")
		}
	}

	// Invalidate cache after adding keys
	if s.KeyService.CacheInvalidationCallback != nil && len(newKeysToCreate) > 0 {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	duration := time.Since(startTime)
	logrus.WithFields(logrus.Fields{
		"groupId":      group.ID,
		"addedCount":   len(newKeysToCreate),
		"ignoredCount": ignoredCount,
		"duration":     duration,
	}).Info("Completed bulk import for copy")

	result := KeyImportResult{
		AddedCount:   len(newKeysToCreate),
		IgnoredCount: ignoredCount,
	}

	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Errorf("Failed to end task with success result for group %d: %v", group.ID, endErr)
	}
}

func (s *KeyImportService) runImport(group *models.Group, keys []string) {
	// Use bulk import service if available for significantly faster imports
	if s.BulkImportSvc != nil {
		s.runBulkImport(group, keys)
		return
	}

	// Fallback to original import method
	progressCallback := func(processed int) {
		if err := s.TaskService.UpdateProgress(processed); err != nil {
			logrus.Warnf("Failed to update task progress for group %d: %v", group.ID, err)
		}
	}

	addedCount, ignoredCount, err := s.KeyService.processAndCreateKeys(group.ID, keys, progressCallback)
	if err != nil {
		if endErr := s.TaskService.EndTask(nil, err); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v (original error: %v)", group.ID, endErr, err)
		}
		return
	}

	result := KeyImportResult{
		AddedCount:   addedCount,
		IgnoredCount: ignoredCount,
	}

	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Errorf("Failed to end task with success result for group %d: %v", group.ID, endErr)
	}
}

// runBulkImport performs optimized bulk import using BulkImportService
func (s *KeyImportService) runBulkImport(group *models.Group, keys []string) {
	startTime := time.Now()

	// Get existing key hashes for deduplication
	var existingHashes []string
	if err := s.KeyService.DB.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Pluck("key_hash", &existingHashes).Error; err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to check existing keys: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	existingHashMap := make(map[string]bool, len(existingHashes))
	for _, h := range existingHashes {
		existingHashMap[h] = true
	}

	// Prepare keys for bulk import
	newKeysToCreate := make([]models.APIKey, 0, len(keys))
	uniqueNewKeys := make(map[string]bool, len(keys))
	ignoredCount := 0

	for _, keyVal := range keys {
		trimmedKey := strings.TrimSpace(keyVal)
		if trimmedKey == "" || uniqueNewKeys[trimmedKey] {
			ignoredCount++
			continue
		}

		keyHash := s.KeyService.EncryptionSvc.Hash(trimmedKey)
		if existingHashMap[keyHash] {
			ignoredCount++
			continue
		}

		encryptedKey, err := s.KeyService.EncryptionSvc.Encrypt(trimmedKey)
		if err != nil {
			logrus.WithError(err).Debug("Failed to encrypt key, skipping")
			ignoredCount++
			continue
		}

		uniqueNewKeys[trimmedKey] = true
		newKeysToCreate = append(newKeysToCreate, models.APIKey{
			GroupID:  group.ID,
			KeyValue: encryptedKey,
			KeyHash:  keyHash,
			Status:   models.KeyStatusActive,
		})

		// Update progress periodically
		if len(newKeysToCreate)%100 == 0 {
			if err := s.TaskService.UpdateProgress(len(newKeysToCreate)); err != nil {
				logrus.Warnf("Failed to update task progress: %v", err)
			}
		}
	}

	if len(newKeysToCreate) == 0 {
		result := KeyImportResult{
			AddedCount:   0,
			IgnoredCount: ignoredCount,
		}
		if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
			logrus.Errorf("Failed to end task for group %d: %v", group.ID, endErr)
		}
		return
	}

	// Use bulk import service for fast insertion
	logrus.WithFields(logrus.Fields{
		"groupId":  group.ID,
		"keyCount": len(newKeysToCreate),
	}).Info("Starting bulk import for keys")

	if err := s.BulkImportSvc.BulkInsertAPIKeys(newKeysToCreate); err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("bulk import failed: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	// Load keys to memory store after successful import
	if s.KeyService.KeyProvider != nil {
		if err := s.KeyService.KeyProvider.LoadGroupKeysToStore(group.ID); err != nil {
			logrus.WithFields(logrus.Fields{
				"groupId": group.ID,
				"error":   err,
			}).Error("Failed to load keys to store after bulk import")
		}
	}

	// Invalidate cache after adding keys
	if s.KeyService.CacheInvalidationCallback != nil && len(newKeysToCreate) > 0 {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	duration := time.Since(startTime)
	logrus.WithFields(logrus.Fields{
		"groupId":      group.ID,
		"addedCount":   len(newKeysToCreate),
		"ignoredCount": ignoredCount,
		"duration":     duration,
	}).Info("Completed bulk import")

	result := KeyImportResult{
		AddedCount:   len(newKeysToCreate),
		IgnoredCount: ignoredCount,
	}

	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Errorf("Failed to end task with success result for group %d: %v", group.ID, endErr)
	}
}
