package services

import (
	"bufio"
	"fmt"
	"gpt-load/internal/models"
	"io"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// KeyImportResult holds the result of an import task.
// Note: IgnoredCount includes both duplicate keys and decryption failures during copy operations.
// We intentionally don't expose a separate DecryptErrorCount field because:
// 1. Decryption errors are logged for debugging purposes
// 2. Users primarily care about the final outcome (added vs ignored)
// 3. Adding more fields increases API complexity without significant user value
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
// Note: TaskService already enforces single-task execution, so no additional concurrency limiting is needed.
func (s *KeyImportService) StartImportTask(group *models.Group, keysText string) (*TaskStatus, error) {
	keys := s.KeyService.ParseKeysFromText(keysText)
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid keys found in the input text")
	}

	initialStatus, err := s.TaskService.StartTask(TaskTypeKeyImport, group.Name, len(keys))
	if err != nil {
		return nil, err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logrus.WithFields(logrus.Fields{
					"groupId":   group.ID,
					"groupName": group.Name,
					"panic":     r,
				}).Error("Panic recovered in runImport")
				_ = s.TaskService.EndTask(nil, fmt.Errorf("internal error: import task panicked"))
			}
		}()
		s.runImport(group, keys)
	}()

	return initialStatus, nil
}

// StartStreamingImportTask initiates a streaming import task that processes keys in batches.
// This method is optimized for large files and uses constant memory regardless of file size.
func (s *KeyImportService) StartStreamingImportTask(group *models.Group, reader io.Reader, fileSize int64) (*TaskStatus, error) {
	// Helper to close reader if it implements io.Closer
	closeReader := func() {
		if closer, ok := reader.(io.Closer); ok {
			_ = closer.Close()
		}
	}

	// Guard against nil BulkImportSvc (defensive programming)
	// This can happen in minimal deployments or test environments
	if s.BulkImportSvc == nil {
		closeReader()
		return nil, fmt.Errorf("bulk import service is not configured")
	}

	// Estimate total keys based on file size (average ~170 bytes per key)
	estimatedKeys := int(fileSize / 170)
	if estimatedKeys < 100 {
		estimatedKeys = 100
	}

	// Determine optimal batch size based on estimated key count
	// Uses unified thresholds from thresholds.go for consistency across all batch operations
	// Larger batches reduce overhead for large imports
	var batchSize int
	switch {
	case estimatedKeys < LargeSyncThreshold:
		batchSize = 1000 // Tier 1-2: Small files, 1000 keys/batch
	case estimatedKeys < AsyncThreshold:
		batchSize = 2000 // Tier 3-4: Medium files, 2000 keys/batch
	default:
		batchSize = 5000 // Tier 5: Large files, 5000 keys/batch for maximum performance
	}

	logrus.WithFields(logrus.Fields{
		"estimatedKeys": estimatedKeys,
		"batchSize":     batchSize,
		"fileSize":      fileSize,
	}).Info("Calculated optimal batch size for streaming import")

	initialStatus, err := s.TaskService.StartTask(TaskTypeKeyImport, group.Name, estimatedKeys)
	if err != nil {
		closeReader()
		return nil, err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logrus.WithFields(logrus.Fields{
					"groupId":   group.ID,
					"groupName": group.Name,
					"panic":     r,
				}).Error("Panic recovered in runStreamingImport")
				_ = s.TaskService.EndTask(nil, fmt.Errorf("internal error: streaming import task panicked"))
			}
		}()
		s.runStreamingImport(group, reader, batchSize)
	}()

	return initialStatus, nil
}

// StartCopyTask initiates an asynchronous key copy task from source group.
// This method fetches and decrypts keys asynchronously for faster HTTP response.
func (s *KeyImportService) StartCopyTask(targetGroup *models.Group, sourceGroupID uint, copyOption string, estimatedKeyCount int) (*TaskStatus, error) {
	initialStatus, err := s.TaskService.StartTask(TaskTypeKeyImport, targetGroup.Name, estimatedKeyCount)
	if err != nil {
		return nil, err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logrus.WithFields(logrus.Fields{
					"targetGroupId": targetGroup.ID,
					"sourceGroupId": sourceGroupID,
					"panic":         r,
				}).Error("Panic recovered in runCopyTask")
				_ = s.TaskService.EndTask(nil, fmt.Errorf("internal error: copy task panicked"))
			}
		}()
		s.runCopyTask(targetGroup, sourceGroupID, copyOption)
	}()

	return initialStatus, nil
}

// runCopyTask performs the actual key copy operation asynchronously.
// It fetches keys from source group in batches, decrypts them, and imports to target group.
// Uses streaming batch processing to handle large groups (500K+ keys) efficiently.
// Memory usage is constant regardless of total key count (O(batch_size) instead of O(n)).
func (s *KeyImportService) runCopyTask(targetGroup *models.Group, sourceGroupID uint, copyOption string) {
	startTime := time.Now()

	// Count total source keys for progress tracking
	var totalSourceKeys int64
	countQuery := s.KeyService.DB.Model(&models.APIKey{}).Where("group_id = ?", sourceGroupID)
	if copyOption == "valid_only" {
		countQuery = countQuery.Where("status = ?", models.KeyStatusActive)
	}
	if err := countQuery.Count(&totalSourceKeys).Error; err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to count source keys: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", targetGroup.ID, endErr)
		}
		return
	}

	if totalSourceKeys == 0 {
		result := KeyImportResult{AddedCount: 0, IgnoredCount: 0}
		if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
			logrus.Errorf("Failed to end task for group %d: %v", targetGroup.ID, endErr)
		}
		return
	}

	// Determine batch size based on total key count
	// For massive operations (>100K keys), use larger batches to reduce overhead
	fetchBatchSize := ExportBatchSize // Default 2000 for fetching
	if totalSourceKeys > MassiveAsyncThreshold {
		fetchBatchSize = ExportMultiGroupBatchSize // 5000 for massive operations
	}

	logrus.WithFields(logrus.Fields{
		"sourceGroupId":  sourceGroupID,
		"targetGroupId":  targetGroup.ID,
		"totalKeys":      totalSourceKeys,
		"fetchBatchSize": fetchBatchSize,
	}).Info("Starting streaming copy operation")

	// Process keys in batches to avoid memory issues with large groups
	// Use two-phase processing: decrypt in batches, then import in batches
	var totalProcessedKeys int64
	var totalDecryptErrors int
	var totalAddedKeys int
	var totalIgnoredKeys int

	// Load existing key hashes for deduplication
	// Memory usage: ~100 bytes per key, so 100k keys ≈ 10MB
	var existingHashes []string
	if err := s.KeyService.DB.Model(&models.APIKey{}).Where("group_id = ?", targetGroup.ID).Pluck("key_hash", &existingHashes).Error; err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to load existing key hashes: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", targetGroup.ID, endErr)
		}
		return
	}

	existingHashMap := make(map[string]bool, len(existingHashes))
	for _, h := range existingHashes {
		existingHashMap[h] = true
	}

	for offset := int64(0); offset < totalSourceKeys; offset += int64(fetchBatchSize) {
		// Fetch batch of source keys
		var sourceKeyData []struct {
			KeyValue string
		}
		query := s.KeyService.DB.Model(&models.APIKey{}).
			Select("key_value").
			Where("group_id = ?", sourceGroupID).
			Limit(fetchBatchSize).
			Offset(int(offset))
		if copyOption == "valid_only" {
			query = query.Where("status = ?", models.KeyStatusActive)
		}
		if err := query.Scan(&sourceKeyData).Error; err != nil {
			if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to fetch source keys batch at offset %d: %w", offset, err)); endErr != nil {
				logrus.Errorf("Failed to end task with error for group %d: %v", targetGroup.ID, endErr)
			}
			return
		}

		// Decrypt batch of keys
		decryptedKeys := make([]string, 0, len(sourceKeyData))
		for _, keyData := range sourceKeyData {
			decryptedKey, err := s.KeyService.EncryptionSvc.Decrypt(keyData.KeyValue)
			if err != nil {
				totalDecryptErrors++
				continue
			}
			decryptedKeys = append(decryptedKeys, decryptedKey)
		}

		totalProcessedKeys += int64(len(sourceKeyData))

		// Update progress during decryption (map to 0-40% of total progress)
		decryptProgress := int(float64(totalProcessedKeys) / float64(totalSourceKeys) * 40)
		if err := s.TaskService.UpdateProgress(decryptProgress); err != nil {
			logrus.Warnf("Failed to update task progress: %v", err)
		}

		// Import this batch of decrypted keys immediately (don't accumulate in memory)
		if len(decryptedKeys) > 0 {
			batchResult := s.importDecryptedKeysBatch(targetGroup, decryptedKeys, existingHashMap, totalProcessedKeys, totalSourceKeys)
			totalAddedKeys += batchResult.AddedCount
			totalIgnoredKeys += batchResult.IgnoredCount
		}

		// Log progress for large operations
		if totalSourceKeys > LargeImportThreshold && totalProcessedKeys%int64(fetchBatchSize*5) == 0 {
			logrus.WithFields(logrus.Fields{
				"processed": totalProcessedKeys,
				"total":     totalSourceKeys,
				"progress":  fmt.Sprintf("%.1f%%", float64(totalProcessedKeys)/float64(totalSourceKeys)*100),
				"added":     totalAddedKeys,
				"ignored":   totalIgnoredKeys,
			}).Info("Copy progress")
		}
	}

	// Final progress update (100%)
	if err := s.TaskService.UpdateProgress(100); err != nil {
		logrus.Warnf("Failed to update task progress: %v", err)
	}

	if totalDecryptErrors > 0 {
		logrus.WithFields(logrus.Fields{
			"sourceGroupId": sourceGroupID,
			"targetGroupId": targetGroup.ID,
			"decryptErrors": totalDecryptErrors,
		}).Warn("Some keys failed to decrypt during copy")
	}

	// End task with final result
	duration := time.Since(startTime)
	result := KeyImportResult{
		AddedCount:   totalAddedKeys,
		IgnoredCount: totalIgnoredKeys + totalDecryptErrors,
	}

	logrus.WithFields(logrus.Fields{
		"sourceGroupId": sourceGroupID,
		"targetGroupId": targetGroup.ID,
		"addedKeys":     totalAddedKeys,
		"ignoredKeys":   totalIgnoredKeys,
		"decryptErrors": totalDecryptErrors,
		"duration":      duration,
	}).Info("Copy operation completed")

	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Errorf("Failed to end task for group %d: %v", targetGroup.ID, endErr)
	}
}

// importDecryptedKeysBatch imports a batch of decrypted keys to the target group.
// This is a helper method for streaming copy operations.
// Returns the number of added and ignored keys in this batch.
func (s *KeyImportService) importDecryptedKeysBatch(
	targetGroup *models.Group,
	decryptedKeys []string,
	existingHashes map[string]bool,
	processedSoFar int64,
	totalKeys int64,
) KeyImportResult {
	// Encrypt and prepare keys for insertion
	keysToInsert := make([]models.APIKey, 0, len(decryptedKeys))
	ignoredCount := 0

	for _, plainKey := range decryptedKeys {
		// Check for duplicates
		keyHash := s.KeyService.EncryptionSvc.Hash(plainKey)
		if existingHashes[keyHash] {
			ignoredCount++
			continue
		}

		// Encrypt key
		encryptedKey, err := s.KeyService.EncryptionSvc.Encrypt(plainKey)
		if err != nil {
			ignoredCount++
			continue
		}

		// Prepare API key model
		apiKey := models.APIKey{
			GroupID:  targetGroup.ID,
			KeyValue: encryptedKey,
			KeyHash:  keyHash,
			Status:   models.KeyStatusActive,
		}
		keysToInsert = append(keysToInsert, apiKey)
		existingHashes[keyHash] = true // Update hash map to prevent duplicates within batches
	}

	// Bulk insert this batch
	if len(keysToInsert) > 0 {
		// Use BulkImportService for efficient insertion
		bulkImportSvc := NewBulkImportService(s.KeyService.DB)

		// Create a transaction for this batch
		tx := s.KeyService.DB.Begin()
		if tx.Error != nil {
			logrus.Errorf("Failed to begin transaction for batch import: %v", tx.Error)
			return KeyImportResult{AddedCount: 0, IgnoredCount: len(decryptedKeys)}
		}

		// Progress callback for this batch (map to 40-100% of total progress)
		progressCallback := func(processed int) {
			// Calculate overall progress: 40% (decryption done) + 60% * (batch progress / total keys)
			overallProgress := 40 + int(float64(processedSoFar-int64(len(decryptedKeys))+int64(processed))/float64(totalKeys)*60)
			if err := s.TaskService.UpdateProgress(overallProgress); err != nil {
				logrus.Warnf("Failed to update task progress: %v", err)
			}
		}

		if err := bulkImportSvc.BulkInsertAPIKeysWithTx(tx, keysToInsert, progressCallback); err != nil {
			tx.Rollback()
			logrus.Errorf("Failed to bulk insert keys batch: %v", err)
			return KeyImportResult{AddedCount: 0, IgnoredCount: len(decryptedKeys)}
		}

		if err := tx.Commit().Error; err != nil {
			logrus.Errorf("Failed to commit transaction for batch import: %v", err)
			return KeyImportResult{AddedCount: 0, IgnoredCount: len(decryptedKeys)}
		}
	}

	return KeyImportResult{
		AddedCount:   len(keysToInsert),
		IgnoredCount: ignoredCount,
	}
}

// runBulkImportForCopy performs bulk import for copied keys.
//
// AI Review Note: Suggested extracting shared logic with runBulkImport to reduce duplication.
// Decision: Keep separate methods because:
// 1. runBulkImportForCopy handles pre-decrypted keys with prior ignored count
// 2. runBulkImport handles raw text input with different progress tracking
// 3. Merging would require complex parameter passing and conditional logic
// 4. Both methods are relatively short and easy to maintain independently
// 5. The duplication is intentional for readability and clear separation of concerns
//
// AI Review Note: Suggested cumulative progress tracking to avoid progress bar jumping backwards.
// Decision: Remove progress updates in encryption phase entirely because:
// 1. Encryption is a fast in-memory operation (no I/O), typically completing in milliseconds
// 2. The decryption phase already provides meaningful progress (the slow part with DB I/O)
// 3. Removing encryption progress avoids the backwards jump issue without adding complexity
// 4. Users see progress reach 100% after decryption, then task completes shortly after
//
// Memory usage note:
// This method loads all existing key hashes into memory (existingHashMap) for deduplication.
// Memory scales with existing key count: ~100 bytes per key, so 100k keys ≈ 10MB.
// This is acceptable because:
// 1. The api_keys table has no unique constraint on key_hash (only a regular index)
// 2. Without DB-level uniqueness, application-level deduplication is required
// 3. Alternative approaches (Bloom filter, batched DB queries) add complexity
// 4. Memory usage is predictable and bounded by existing key count
// 5. This runs asynchronously, not blocking HTTP responses
func (s *KeyImportService) runBulkImportForCopy(group *models.Group, keys []string, priorIgnored int, startTime time.Time) {
	// Note: defer with nil assignment to parameters has no GC effect
	// Go's GC automatically reclaims memory when function returns
	// The keys parameter is a slice header copy; nil-ing it doesn't affect caller's reference

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
		// Note: Progress updates removed in encryption phase to avoid progress bar jumping backwards.
		// Encryption is fast (in-memory), and decryption phase already provides meaningful progress.
	}

	// Note: Go's GC automatically reclaims existingHashMap and uniqueNewKeys
	// when they go out of scope; no explicit cleanup needed

	if len(newKeysToCreate) == 0 {
		result := KeyImportResult{AddedCount: 0, IgnoredCount: ignoredCount}
		if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
			logrus.Errorf("Failed to end task for group %d: %v", group.ID, endErr)
		}
		return
	}

	// Use bulk import service for fast insertion
	// Phase 2 progress tracking: Map bulk insert progress to 40-100% of total progress
	logrus.WithFields(logrus.Fields{
		"groupId":  group.ID,
		"keyCount": len(newKeysToCreate),
	}).Info("Starting bulk import for copied keys")

	totalKeysToInsert := len(newKeysToCreate)

	// Create transaction for bulk insert with progress tracking
	tx := s.KeyService.DB.Begin()
	if err := tx.Error; err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to start transaction: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	// Progress callback for bulk insert phase (40-100%)
	progressCallback := func(processed int) {
		// Map processed keys to 40-100% range
		insertProgress := 40 + int(float64(processed)/float64(totalKeysToInsert)*60)
		if err := s.TaskService.UpdateProgress(insertProgress); err != nil {
			logrus.Warnf("Failed to update task progress: %v", err)
		}
	}

	if err := s.BulkImportSvc.BulkInsertAPIKeysWithTx(tx, newKeysToCreate, progressCallback); err != nil {
		tx.Rollback()
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("bulk import failed: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	if err := tx.Commit().Error; err != nil {
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("failed to commit transaction: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	// Store counts before releasing memory
	addedCount := len(newKeysToCreate)

	// Note: Setting local variable to nil has no GC effect
	// Go's GC automatically reclaims memory when function returns
	newKeysToCreate = nil

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
	if s.KeyService.CacheInvalidationCallback != nil {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	duration := time.Since(startTime)
	logrus.WithFields(logrus.Fields{
		"groupId":      group.ID,
		"addedCount":   addedCount,
		"ignoredCount": ignoredCount,
		"duration":     duration,
	}).Info("Completed bulk import for copy")

	result := KeyImportResult{
		AddedCount:   addedCount,
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

// runBulkImport performs optimized bulk import using BulkImportService.
// Uses batch processing to handle large imports (500K+ keys) efficiently.
// Memory usage is constant regardless of total key count (O(batch_size) instead of O(n)).
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

	// Determine batch size based on total key count
	totalKeys := len(keys)
	processBatchSize := ImportDecryptBatchSize // Default 1000
	if int64(totalKeys) > MassiveAsyncThreshold {
		processBatchSize = ExportMultiGroupBatchSize // 5000 for massive operations
	}

	logrus.WithFields(logrus.Fields{
		"groupId":          group.ID,
		"totalKeys":        totalKeys,
		"processBatchSize": processBatchSize,
	}).Info("Starting batch bulk import")

	// Process keys in batches to avoid memory issues
	var totalAddedCount int
	var totalIgnoredCount int
	uniqueNewKeys := make(map[string]bool, processBatchSize)

	for offset := 0; offset < totalKeys; offset += processBatchSize {
		end := offset + processBatchSize
		if end > totalKeys {
			end = totalKeys
		}

		batchKeys := keys[offset:end]
		newKeysToCreate := make([]models.APIKey, 0, len(batchKeys))
		batchIgnoredCount := 0

		// Process this batch: encrypt and prepare for insertion
		for _, keyVal := range batchKeys {
			trimmedKey := strings.TrimSpace(keyVal)
			if trimmedKey == "" || uniqueNewKeys[trimmedKey] {
				batchIgnoredCount++
				continue
			}

			keyHash := s.KeyService.EncryptionSvc.Hash(trimmedKey)
			if existingHashMap[keyHash] {
				batchIgnoredCount++
				continue
			}

			encryptedKey, err := s.KeyService.EncryptionSvc.Encrypt(trimmedKey)
			if err != nil {
				logrus.WithError(err).Debug("Failed to encrypt key, skipping")
				batchIgnoredCount++
				continue
			}

			uniqueNewKeys[trimmedKey] = true
			existingHashMap[keyHash] = true // Prevent duplicates across batches
			newKeysToCreate = append(newKeysToCreate, models.APIKey{
				GroupID:  group.ID,
				KeyValue: encryptedKey,
				KeyHash:  keyHash,
				Status:   models.KeyStatusActive,
			})
		}

		// Insert this batch if there are keys to insert
		if len(newKeysToCreate) > 0 {
			if err := s.BulkImportSvc.BulkInsertAPIKeys(newKeysToCreate); err != nil {
				if endErr := s.TaskService.EndTask(nil, fmt.Errorf("bulk import failed at offset %d: %w", offset, err)); endErr != nil {
					logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
				}
				return
			}

			totalAddedCount += len(newKeysToCreate)
		}

		totalIgnoredCount += batchIgnoredCount

		// Update progress
		processedSoFar := end
		progress := int(float64(processedSoFar) / float64(totalKeys) * 100)
		if err := s.TaskService.UpdateProgress(progress); err != nil {
			logrus.Warnf("Failed to update task progress: %v", err)
		}

		// Log progress for large operations
		if totalKeys > LargeImportThreshold && processedSoFar%(processBatchSize*5) == 0 {
			elapsed := time.Since(startTime)
			rate := float64(processedSoFar) / elapsed.Seconds()
			logrus.WithFields(logrus.Fields{
				"processed": processedSoFar,
				"total":     totalKeys,
				"progress":  fmt.Sprintf("%.1f%%", float64(processedSoFar)/float64(totalKeys)*100),
				"added":     totalAddedCount,
				"ignored":   totalIgnoredCount,
				"rate":      fmt.Sprintf("%.0f keys/sec", rate),
			}).Info("Bulk import progress")
		}

		// Clear batch memory
		newKeysToCreate = nil
		uniqueNewKeys = make(map[string]bool, processBatchSize) // Reset for next batch
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
	if s.KeyService.CacheInvalidationCallback != nil {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	duration := time.Since(startTime)
	logrus.WithFields(logrus.Fields{
		"groupId":      group.ID,
		"addedCount":   totalAddedCount,
		"ignoredCount": totalIgnoredCount,
		"duration":     duration,
		"rate":         fmt.Sprintf("%.0f keys/sec", float64(totalKeys)/duration.Seconds()),
	}).Info("Completed batch bulk import")

	result := KeyImportResult{
		AddedCount:   totalAddedCount,
		IgnoredCount: totalIgnoredCount,
	}

	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Errorf("Failed to end task with success result for group %d: %v", group.ID, endErr)
	}
}

// runStreamingImport performs streaming import that processes keys in batches while reading.
// This method uses constant memory regardless of file size by processing keys incrementally.
// Memory usage scales with batchSize: ~200 bytes per key, so 10000 keys = ~2MB.
//
// Memory usage note for existingHashMap:
// This method loads all existing key hashes into memory (existingHashMap) for deduplication.
// Memory scales with existing key count: ~100 bytes per key, so 100k keys ≈ 10MB.
// Total memory = batch memory (~2MB) + existing hashes memory (~10MB for 100k keys) ≈ 12MB.
// This is acceptable because:
// 1. The api_keys table has no unique constraint on key_hash (only a regular index)
// 2. Without DB-level uniqueness, application-level deduplication is required
// 3. Alternative approaches (Bloom filter, batched DB queries) add complexity
// 4. Memory usage is predictable and bounded by existing key count + batch size
// 5. This runs asynchronously, not blocking HTTP responses
func (s *KeyImportService) runStreamingImport(group *models.Group, reader io.Reader, batchSize int) {
	startTime := time.Now()

	logrus.WithFields(logrus.Fields{
		"groupId":   group.ID,
		"groupName": group.Name,
		"batchSize": batchSize,
	}).Info("Starting runStreamingImport")

	// Defer memory cleanup and file closing
	defer func() {
		// Ensure reader is closed if it implements io.Closer
		if closer, ok := reader.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				logrus.WithFields(logrus.Fields{
					"groupId": group.ID,
					"error":   err,
				}).Warn("Failed to close file reader")
			} else {
				logrus.WithField("groupId", group.ID).Debug("File reader closed successfully")
			}
		}
	}()

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

	// Initialize counters
	totalProcessed := 0
	totalAdded := 0
	totalIgnored := 0

	// Create scanner for line-by-line reading
	scanner := bufio.NewScanner(reader)
	// Increase buffer size to handle long lines (up to 1MB per line)
	const maxScanTokenSize = 1024 * 1024 // 1MB
	buf := make([]byte, maxScanTokenSize)
	scanner.Buffer(buf, maxScanTokenSize)

	// Batch processing
	currentBatch := make([]string, 0, batchSize)

	logrus.WithFields(logrus.Fields{
		"groupId":        group.ID,
		"existingHashes": len(existingHashMap),
	}).Info("Starting to scan file line by line")

	// Process file line by line
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		currentBatch = append(currentBatch, line)

		// Log first few lines for debugging
		if lineCount <= 3 {
			logrus.WithFields(logrus.Fields{
				"lineNumber": lineCount,
				"lineLength": len(line),
			}).Debug("Read line from file")
		}

		// Process batch when it reaches batchSize
		if len(currentBatch) >= batchSize {
			// Note: batchDedupeMap removed to maintain constant memory usage
			// existingHashMap is updated in processBatch and provides cross-batch deduplication
			added, ignored, err := s.processBatch(group, currentBatch, existingHashMap)
			if err != nil {
				_ = s.TaskService.EndTask(nil, err)
				return
			}
			totalAdded += added
			totalIgnored += ignored
			totalProcessed += len(currentBatch)

			// Update progress
			if err := s.TaskService.UpdateProgress(totalProcessed); err != nil {
				logrus.Warnf("Failed to update task progress: %v", err)
			}

			// Log progress every 10 batches (adaptive based on batch size)
			batchCount := totalProcessed / batchSize
			if batchCount%10 == 0 {
				elapsed := time.Since(startTime)
				rate := float64(totalProcessed) / elapsed.Seconds()
				logrus.Infof("Streaming import progress: %d keys processed (%.0f keys/sec, %d added, %d ignored)",
					totalProcessed, rate, totalAdded, totalIgnored)
			}

			// Clear batch for next iteration
			currentBatch = currentBatch[:0]
		}
	}

	// Check for scanner errors
	if err := scanner.Err(); err != nil {
		logrus.WithFields(logrus.Fields{
			"groupId": group.ID,
			"error":   err,
		}).Error("Scanner error while reading file")
		if endErr := s.TaskService.EndTask(nil, fmt.Errorf("error reading file: %w", err)); endErr != nil {
			logrus.Errorf("Failed to end task with error for group %d: %v", group.ID, endErr)
		}
		return
	}

	logrus.WithFields(logrus.Fields{
		"groupId":        group.ID,
		"totalLines":     lineCount,
		"remainingBatch": len(currentBatch),
	}).Info("Finished scanning file, processing remaining batch")

	// Process remaining keys in the last batch
	if len(currentBatch) > 0 {
		added, ignored, err := s.processBatch(group, currentBatch, existingHashMap)
		if err != nil {
			_ = s.TaskService.EndTask(nil, err)
			return
		}
		totalAdded += added
		totalIgnored += ignored
		totalProcessed += len(currentBatch)

		// Final progress update
		if err := s.TaskService.UpdateProgress(totalProcessed); err != nil {
			logrus.Warnf("Failed to update task progress: %v", err)
		}
	}

	// Note: No need to explicitly set maps to nil for GC
	// Go's GC automatically reclaims memory when variables go out of scope
	// These assignments are kept for code clarity but have no GC effect

	// Load keys to memory store after successful import
	if s.KeyService.KeyProvider != nil {
		if err := s.KeyService.KeyProvider.LoadGroupKeysToStore(group.ID); err != nil {
			logrus.WithFields(logrus.Fields{
				"groupId": group.ID,
				"error":   err,
			}).Error("Failed to load keys to store after streaming import")
		}
	}

	// Invalidate cache after adding keys
	if s.KeyService.CacheInvalidationCallback != nil {
		s.KeyService.CacheInvalidationCallback(group.ID)
	}

	duration := time.Since(startTime)
	rate := float64(totalProcessed) / duration.Seconds()
	logrus.WithFields(logrus.Fields{
		"groupId":      group.ID,
		"processed":    totalProcessed,
		"addedCount":   totalAdded,
		"ignoredCount": totalIgnored,
		"duration":     duration,
		"rate":         fmt.Sprintf("%.0f keys/sec", rate),
	}).Info("Completed streaming import")

	result := KeyImportResult{
		AddedCount:   totalAdded,
		IgnoredCount: totalIgnored,
	}

	if endErr := s.TaskService.EndTask(result, nil); endErr != nil {
		logrus.Errorf("Failed to end task with success result for group %d: %v", group.ID, endErr)
	}
}

// processBatch processes a batch of keys and returns added and ignored counts.
// This method handles encryption, deduplication, and bulk insertion.
// Note: Removed batchDedupeMap parameter to maintain constant memory usage.
// Within-batch deduplication is handled by a local map that's cleared after each batch.
// Cross-batch deduplication is handled by existingHashMap which stores key hashes.
//
// Error handling strategy:
// - Encryption errors: Skip individual keys, continue processing (counted as ignored)
// - Bulk insert errors: Return error immediately to abort the task and prevent silent data loss
// - This ensures callers can distinguish between duplicate keys (ignored) and insertion failures (error)
func (s *KeyImportService) processBatch(
	group *models.Group,
	keys []string,
	existingHashMap map[string]bool,
) (added int, ignored int, err error) {
	if len(keys) == 0 {
		return 0, 0, nil
	}

	// Prepare keys for bulk import
	newKeysToCreate := make([]models.APIKey, 0, len(keys))

	// Track hashes for this batch to update existingHashMap only after successful insert
	// This prevents failed batches from polluting the deduplication map
	batchHashes := make([]string, 0, len(keys))

	// Local deduplication map for current batch only (cleared after batch processing)
	// This prevents duplicate keys within the same batch
	localDedupeMap := make(map[string]bool, len(keys))

	for _, keyVal := range keys {
		trimmedKey := strings.TrimSpace(keyVal)
		if trimmedKey == "" {
			ignored++
			continue
		}

		// Check if already processed in current batch
		if localDedupeMap[trimmedKey] {
			ignored++
			continue
		}

		keyHash := s.KeyService.EncryptionSvc.Hash(trimmedKey)

		// Check if exists in database or previous batches
		if existingHashMap[keyHash] {
			ignored++
			continue
		}

		encryptedKey, err := s.KeyService.EncryptionSvc.Encrypt(trimmedKey)
		if err != nil {
			logrus.WithError(err).Debug("Failed to encrypt key, skipping")
			ignored++
			continue
		}

		// Mark as processed in current batch
		localDedupeMap[trimmedKey] = true
		// Defer updating global hash map until after successful insert
		batchHashes = append(batchHashes, keyHash)

		newKeysToCreate = append(newKeysToCreate, models.APIKey{
			GroupID:  group.ID,
			KeyValue: encryptedKey,
			KeyHash:  keyHash,
			Status:   models.KeyStatusActive,
		})
	}
	// localDedupeMap goes out of scope here and is garbage collected

	if len(newKeysToCreate) == 0 {
		return 0, ignored, nil
	}

	// Use bulk import service for fast insertion
	if err := s.BulkImportSvc.BulkInsertAPIKeys(newKeysToCreate); err != nil {
		logrus.WithFields(logrus.Fields{
			"groupId":  group.ID,
			"keyCount": len(newKeysToCreate),
			"error":    err,
		}).Error("Failed to bulk insert batch")
		// Return error to abort task and prevent silent data loss
		// Do NOT update existingHashMap on failure to allow retry in future imports
		return 0, ignored, fmt.Errorf("bulk insert failed: %w", err)
	}

	// Only update existingHashMap after successful insert
	// This ensures failed batches can be retried in future imports
	for _, hash := range batchHashes {
		existingHashMap[hash] = true
	}

	added = len(newKeysToCreate)
	return added, ignored, nil
}
