package keypool

import (
	"errors"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type KeyProvider struct {
	db                        *gorm.DB
	store                     store.Store
	settingsManager           *config.SystemSettingsManager
	encryptionSvc             encryption.Service
	// CacheInvalidationCallback is an optional callback for cache invalidation.
	// Note: This callback will be invoked from a goroutine (spawned in UpdateStatus),
	// so implementers must handle concurrent access if the callback accesses shared state.
	CacheInvalidationCallback func(groupID uint)
}

// NewProvider creates a new KeyProvider instance.
func NewProvider(db *gorm.DB, store store.Store, settingsManager *config.SystemSettingsManager, encryptionSvc encryption.Service) *KeyProvider {
	return &KeyProvider{
		db:              db,
		store:           store,
		settingsManager: settingsManager,
		encryptionSvc:   encryptionSvc,
	}
}

// SelectKey atomically selects and rotates an available APIKey for the specified group.
func (p *KeyProvider) SelectKey(groupID uint) (*models.APIKey, error) {
	// Use strconv instead of fmt.Sprintf for better performance in hot path
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"

	// 1. Atomically rotate the key ID from the list
	keyIDStr, err := p.store.Rotate(activeKeysListKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, app_errors.ErrNoActiveKeys
		}
		return nil, fmt.Errorf("failed to rotate key from store: %w", err)
	}

	keyID, err := strconv.ParseUint(keyIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse key ID '%s': %w", keyIDStr, err)
	}

	// 2. Get key details from HASH
	// Use strconv instead of fmt.Sprintf for better performance in hot path
	keyHashKey := "key:" + keyIDStr
	keyDetails, err := p.store.HGetAll(keyHashKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get key details for key ID %d: %w", keyID, err)
	}

	// 3. Manually unmarshal the map into an APIKey struct
	failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)
	createdAt, _ := strconv.ParseInt(keyDetails["created_at"], 10, 64)

	// Decrypt the key value for use by channels
	encryptedKeyValue := keyDetails["key_string"]
	decryptedKeyValue, err := p.encryptionSvc.Decrypt(encryptedKeyValue)
	if err != nil {
		// If decryption fails, try to use the value as-is (backward compatibility for unencrypted keys)
		logrus.WithFields(logrus.Fields{
			"keyID": keyID,
			"error": err,
		}).Debug("Failed to decrypt key value, using as-is for backward compatibility")
		decryptedKeyValue = encryptedKeyValue
	}

	apiKey := &models.APIKey{
		ID:           uint(keyID),
		KeyValue:     decryptedKeyValue,
		Status:       keyDetails["status"],
		FailureCount: failureCount,
		GroupID:      groupID,
		CreatedAt:    time.Unix(createdAt, 0),
	}

	return apiKey, nil
}

// UpdateStatus asynchronously submits a key status update task.
func (p *KeyProvider) UpdateStatus(apiKey *models.APIKey, group *models.Group, isSuccess bool, errorMessage string) {
	go func() {
		// Use strconv instead of fmt.Sprintf for better performance
		keyHashKey := "key:" + strconv.FormatUint(uint64(apiKey.ID), 10)
		activeKeysListKey := "group:" + strconv.FormatUint(uint64(group.ID), 10) + ":active_keys"

		if isSuccess {
			if err := p.handleSuccess(apiKey.ID, keyHashKey, activeKeysListKey, group.ID); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "error": err}).Error("Failed to handle key success")
			}
		} else {
			if app_errors.IsUnCounted(errorMessage) {
				logrus.WithFields(logrus.Fields{
					"keyID": apiKey.ID,
					"error": errorMessage,
				}).Debug("Uncounted error, skipping failure handling")
			} else {
				if err := p.handleFailure(apiKey, group, keyHashKey, activeKeysListKey); err != nil {
					logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "error": err}).Error("Failed to handle key failure")
				}
			}
		}
	}()
}

// executeTransactionWithRetry wraps a database transaction with a retry mechanism.
func (p *KeyProvider) executeTransactionWithRetry(operation func(tx *gorm.DB) error) error {
	const maxRetries = 3
	const baseDelay = 50 * time.Millisecond
	const maxJitter = 150 * time.Millisecond
	var err error

	for i := range maxRetries {
		err = p.db.Transaction(operation)
		if err == nil {
			return nil
		}

		if strings.Contains(err.Error(), "database is locked") {
			// Use thread-safe global rand.Intn (concurrency-safe; Go 1.20+ also auto-seeds)
			// This avoids data race issues when multiple goroutines call executeTransactionWithRetry concurrently
			jitter := time.Duration(rand.Intn(int(maxJitter)))
			totalDelay := baseDelay + jitter
			logrus.Debugf("Database is locked, retrying in %v... (attempt %d/%d)", totalDelay, i+1, maxRetries)
			time.Sleep(totalDelay)
			continue
		}

		break
	}

	return err
}

func (p *KeyProvider) handleSuccess(keyID uint, keyHashKey, activeKeysListKey string, groupID uint) error {
	keyDetails, err := p.store.HGetAll(keyHashKey)
	if err != nil {
		return fmt.Errorf("failed to get key details from store: %w", err)
	}

	failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)
	isActive := keyDetails["status"] == models.KeyStatusActive

	if failureCount == 0 && isActive {
		return nil
	}

	return p.executeTransactionWithRetry(func(tx *gorm.DB) error {
		var key models.APIKey
		if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&key, keyID).Error; err != nil {
			return fmt.Errorf("failed to lock key %d for update: %w", keyID, err)
		}

		updates := map[string]any{"failure_count": 0}
		if !isActive {
			updates["status"] = models.KeyStatusActive
		}

		if err := tx.Model(&models.APIKey{}).Where("id = ?", keyID).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to update key in DB: %w", err)
		}

		if err := p.store.HSet(keyHashKey, updates); err != nil {
			return fmt.Errorf("failed to update key details in store: %w", err)
		}

		if !isActive {
			logrus.WithField("keyID", keyID).Debug("Key has recovered and is being restored to active pool.")
			if err := p.store.LRem(activeKeysListKey, 0, keyID); err != nil {
				return fmt.Errorf("failed to LRem key before LPush on recovery: %w", err)
			}
			if err := p.store.LPush(activeKeysListKey, keyID); err != nil {
				return fmt.Errorf("failed to LPush key back to active list: %w", err)
			}

			// Invalidate cache after key status change
			if p.CacheInvalidationCallback != nil {
				p.CacheInvalidationCallback(groupID)
			}
		}

		return nil
	})
}

func (p *KeyProvider) handleFailure(apiKey *models.APIKey, group *models.Group, keyHashKey, activeKeysListKey string) error {
	keyDetails, err := p.store.HGetAll(keyHashKey)
	if err != nil {
		return fmt.Errorf("failed to get key details from store: %w", err)
	}

	if keyDetails["status"] == models.KeyStatusInvalid {
		return nil
	}

	failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)

	// Ensure EffectiveConfig is set for this group (use group-level config which may override system settings)
	// This ensures validation uses the correct group-specific blacklist_threshold, not just system settings
	if group.EffectiveConfig.AppUrl == "" {
		group.EffectiveConfig = p.settingsManager.GetEffectiveConfig(group.Config)
	}

	// Get the effective configuration for this group
	// This will use group-specific blacklist_threshold if set, otherwise fall back to system settings
	blacklistThreshold := group.EffectiveConfig.BlacklistThreshold

	return p.executeTransactionWithRetry(func(tx *gorm.DB) error {
		var key models.APIKey
		if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&key, apiKey.ID).Error; err != nil {
			return fmt.Errorf("failed to lock key %d for update: %w", apiKey.ID, err)
		}

		newFailureCount := failureCount + 1

		updates := map[string]any{"failure_count": newFailureCount}
		shouldBlacklist := blacklistThreshold > 0 && newFailureCount >= int64(blacklistThreshold)
		if shouldBlacklist {
			updates["status"] = models.KeyStatusInvalid
		}

		if err := tx.Model(&models.APIKey{}).Where("id = ?", apiKey.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to update key stats in DB: %w", err)
		}

		if _, err := p.store.HIncrBy(keyHashKey, "failure_count", 1); err != nil {
			return fmt.Errorf("failed to increment failure count in store: %w", err)
		}

		if shouldBlacklist {
			logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "threshold": blacklistThreshold}).Warn("Key has reached blacklist threshold, disabling.")
			if err := p.store.LRem(activeKeysListKey, 0, apiKey.ID); err != nil {
				return fmt.Errorf("failed to LRem key from active list: %w", err)
			}
			if err := p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusInvalid}); err != nil {
				return fmt.Errorf("failed to update key status to invalid in store: %w", err)
			}

			// Invalidate cache after key status change
			if p.CacheInvalidationCallback != nil {
				p.CacheInvalidationCallback(group.ID)
			}
		}

		return nil
	})
}

// LoadKeysFromDB loads all groups and keys from the database and populates the Store.
func (p *KeyProvider) LoadKeysFromDB() error {
	startTime := time.Now()

	// Get total count first for progress reporting
	var totalCount int64
	if err := p.db.Model(&models.APIKey{}).Count(&totalCount).Error; err != nil {
		return fmt.Errorf("failed to count keys: %w", err)
	}

	logrus.Infof("Loading %d keys from database to store...", totalCount)

	// Use cursor-based pagination instead of FindInBatches to reduce lock time
	// This allows other operations to proceed between batches
	allActiveKeyIDs := make(map[uint][]any)
	batchSize := 1000
	var lastID uint = 0
	processedKeys := 0
	lastLoggedPercent := 0

	for {
		var batchKeys []models.APIKey

		// Use cursor-based query to minimize lock time
		query := p.db.Model(&models.APIKey{}).
			Order("id ASC").
			Limit(batchSize)

		if lastID > 0 {
			query = query.Where("id > ?", lastID)
		}

		if err := query.Find(&batchKeys).Error; err != nil {
			return fmt.Errorf("failed to load keys batch: %w", err)
		}

		if len(batchKeys) == 0 {
			break
		}

		// Process batch and write to store
		var pipeline store.Pipeliner
		if redisStore, ok := p.store.(store.RedisPipeliner); ok {
			pipeline = redisStore.Pipeline()
		}

		for i := range batchKeys {
			key := &batchKeys[i]
			keyHashKey := "key:" + strconv.FormatUint(uint64(key.ID), 10)
			keyDetails := p.apiKeyToMap(key)

			if pipeline != nil {
				pipeline.HSet(keyHashKey, keyDetails)
			} else {
				if err := p.store.HSet(keyHashKey, keyDetails); err != nil {
					logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Warn("Failed to HSet key details")
				}
			}

			if key.Status == models.KeyStatusActive {
				allActiveKeyIDs[key.GroupID] = append(allActiveKeyIDs[key.GroupID], key.ID)
			}
		}

		if pipeline != nil {
			if err := pipeline.Exec(); err != nil {
				return fmt.Errorf("failed to execute pipeline: %w", err)
			}
		}

		processedKeys += len(batchKeys)
		lastID = batchKeys[len(batchKeys)-1].ID

		// Log progress at 25%, 50%, 75% milestones only
		if totalCount > 0 {
			currentPercent := (processedKeys * 100) / int(totalCount)
			if currentPercent >= lastLoggedPercent+25 && currentPercent < 100 {
				logrus.Infof("Loading progress: %d%% (%d/%d keys)", currentPercent, processedKeys, totalCount)
				lastLoggedPercent = currentPercent
			}
		}

		// If we got fewer than batchSize, we're done
		if len(batchKeys) < batchSize {
			break
		}
	}

	// Update active_keys list for all groups
	logrus.Info("Updating active key lists for all groups...")
	for groupID, activeIDs := range allActiveKeyIDs {
		if len(activeIDs) > 0 {
			activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
			p.store.Delete(activeKeysListKey)
			if err := p.store.LPush(activeKeysListKey, activeIDs...); err != nil {
				logrus.WithFields(logrus.Fields{"groupID": groupID, "error": err}).Error("Failed to LPush active keys for group")
			}
		}
	}

	duration := time.Since(startTime)
	logrus.Infof("Successfully loaded %d keys to store in %v", processedKeys, duration)
	return nil
}

// AddKeys batch adds new keys to the pool and database.
func (p *KeyProvider) AddKeys(groupID uint, keys []models.APIKey) error {
	if len(keys) == 0 {
		return nil
	}

	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&keys).Error; err != nil {
			return err
		}

		for _, key := range keys {
			if err := p.addKeyToStore(&key); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to add key to store after DB creation, rolling back transaction")
				return err
			}
		}
		return nil
	})

	return err
}

// RemoveKeys batch removes keys from the pool and database.
func (p *KeyProvider) RemoveKeys(groupID uint, keyValues []string) (int64, error) {
	if len(keyValues) == 0 {
		return 0, nil
	}

	var keysToDelete []models.APIKey
	var deletedCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		var keyHashes []string
		for _, keyValue := range keyValues {
			keyHash := p.encryptionSvc.Hash(keyValue)
			if keyHash != "" {
				keyHashes = append(keyHashes, keyHash)
			}
		}

		if len(keyHashes) == 0 {
			return nil
		}

		if err := tx.Where("group_id = ? AND key_hash IN ?", groupID, keyHashes).Find(&keysToDelete).Error; err != nil {
			return err
		}

		if len(keysToDelete) == 0 {
			return nil
		}

		keyIDsToDelete := pluckIDs(keysToDelete)

		result := tx.Where("id IN ?", keyIDsToDelete).Delete(&models.APIKey{})
		if result.Error != nil {
			return result.Error
		}
		deletedCount = result.RowsAffected

		for _, key := range keysToDelete {
			if err := p.removeKeyFromStore(key.ID, key.GroupID); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to remove key from store after DB deletion, rolling back transaction")
				return err
			}
		}

		return nil
	})

	return deletedCount, err
}

// RestoreKeys restores all invalid keys in the group.
func (p *KeyProvider) RestoreKeys(groupID uint) (int64, error) {
	var invalidKeys []models.APIKey
	var restoredCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ? AND status = ?", groupID, models.KeyStatusInvalid).Find(&invalidKeys).Error; err != nil {
			return err
		}

		if len(invalidKeys) == 0 {
			return nil
		}

		updates := map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": 0,
		}
		result := tx.Model(&models.APIKey{}).Where("group_id = ? AND status = ?", groupID, models.KeyStatusInvalid).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		restoredCount = result.RowsAffected

		for _, key := range invalidKeys {
			key.Status = models.KeyStatusActive
			key.FailureCount = 0
			if err := p.addKeyToStore(&key); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to restore key in store after DB update, rolling back transaction")
				return err
			}
		}
		return nil
	})

	return restoredCount, err
}

// RestoreMultipleKeys restores the specified keys.
func (p *KeyProvider) RestoreMultipleKeys(groupID uint, keyValues []string) (int64, error) {
	if len(keyValues) == 0 {
		return 0, nil
	}

	var keysToRestore []models.APIKey
	var restoredCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		var keyHashes []string
		for _, keyValue := range keyValues {
			keyHash := p.encryptionSvc.Hash(keyValue)
			if keyHash != "" {
				keyHashes = append(keyHashes, keyHash)
			}
		}

		if len(keyHashes) == 0 {
			return nil
		}

		if err := tx.Where("group_id = ? AND key_hash IN ? AND status = ?", groupID, keyHashes, models.KeyStatusInvalid).Find(&keysToRestore).Error; err != nil {
			return err
		}

		if len(keysToRestore) == 0 {
			return nil
		}

		keyIDsToRestore := pluckIDs(keysToRestore)

		updates := map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": 0,
		}
		result := tx.Model(&models.APIKey{}).Where("id IN ?", keyIDsToRestore).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		restoredCount = result.RowsAffected

		for _, key := range keysToRestore {
			key.Status = models.KeyStatusActive
			key.FailureCount = 0
			if err := p.addKeyToStore(&key); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to restore key in store after DB update")
				return err
			}
		}

		return nil
	})

	return restoredCount, err
}

// ResetGroupActiveKeysFailureCount resets failure_count to 0 for all active keys in a specific group.
// This is useful when importing a group, treating it as a fresh import.
func (p *KeyProvider) ResetGroupActiveKeysFailureCount(groupID uint) (int64, error) {
	// Update failure_count to 0 in database for all active keys in this group
	result := p.db.Model(&models.APIKey{}).
		Where("group_id = ? AND status = ? AND failure_count > 0", groupID, models.KeyStatusActive).
		Update("failure_count", 0)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to reset failure_count in database: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return 0, nil
	}

	// Get all active keys to update Redis store
	var keys []models.APIKey
	if err := p.db.Select("id").
		Where("group_id = ? AND status = ?", groupID, models.KeyStatusActive).
		Find(&keys).Error; err != nil {
		logrus.WithError(err).Warn("Failed to query keys for store update, but database update succeeded")
		return result.RowsAffected, nil // Return success even if store update fails
	}

	// Update failure_count in Redis store for each key
	for _, key := range keys {
		keyHashKey := "key:" + strconv.FormatUint(uint64(key.ID), 10)
		if err := p.store.HSet(keyHashKey, map[string]any{"failure_count": 0}); err != nil {
			logrus.WithFields(logrus.Fields{
				"keyID": key.ID,
				"error": err,
			}).Warn("Failed to reset failure_count in store, continuing with other keys")
			// Continue processing other keys even if one fails
		}
	}

	if result.RowsAffected > 0 {
		logrus.Infof("Reset failure_count for %d active keys in group %d", result.RowsAffected, groupID)
	}

	return result.RowsAffected, nil
}

// ResetAllActiveKeysFailureCount resets failure_count to 0 for all active keys across all groups.
// This is useful when system configuration changes (e.g., blacklist_threshold) and we want to
// reset the failure history to avoid immediate blacklisting with new thresholds.
func (p *KeyProvider) ResetAllActiveKeysFailureCount() (int64, error) {
	var totalReset int64

	// Process in batches to avoid memory issues and improve performance
	batchSize := 1000
	offset := 0

	for {
		var keys []models.APIKey
		if err := p.db.Select("id, group_id").
			Where("status = ?", models.KeyStatusActive).
			Where("failure_count > 0").
			Limit(batchSize).
			Offset(offset).
			Find(&keys).Error; err != nil {
			return totalReset, fmt.Errorf("failed to query active keys: %w", err)
		}

		if len(keys) == 0 {
			break
		}

		// Batch update failure_count to 0 in database
		keyIDs := make([]uint, len(keys))
		groupIDMap := make(map[uint]struct{})
		for i, key := range keys {
			keyIDs[i] = key.ID
			groupIDMap[key.GroupID] = struct{}{}
		}

		result := p.db.Model(&models.APIKey{}).
			Where("id IN ?", keyIDs).
			Update("failure_count", 0)
		if result.Error != nil {
			return totalReset, fmt.Errorf("failed to reset failure_count in database: %w", result.Error)
		}

		batchReset := result.RowsAffected
		totalReset += batchReset

		// Update failure_count in Redis store for each key
		for _, key := range keys {
			keyHashKey := "key:" + strconv.FormatUint(uint64(key.ID), 10)
			if err := p.store.HSet(keyHashKey, map[string]any{"failure_count": 0}); err != nil {
				logrus.WithFields(logrus.Fields{
					"keyID": key.ID,
					"error": err,
				}).Warn("Failed to reset failure_count in store, continuing with other keys")
				// Continue processing other keys even if one fails
			}
		}

		// Invalidate cache for affected groups
		for groupID := range groupIDMap {
			if p.CacheInvalidationCallback != nil {
				p.CacheInvalidationCallback(groupID)
			}
		}

		offset += len(keys)

		// If we got fewer than batchSize, we've reached the end
		if len(keys) < batchSize {
			break
		}
	}

	if totalReset > 0 {
		logrus.Infof("Reset failure_count for %d active keys", totalReset)
	}

	return totalReset, nil
}

// RemoveInvalidKeys removes all invalid keys in the group.
func (p *KeyProvider) RemoveInvalidKeys(groupID uint) (int64, error) {
	return p.removeKeysByStatus(groupID, models.KeyStatusInvalid)
}

// RemoveAllKeys removes all keys in the group.
func (p *KeyProvider) RemoveAllKeys(groupID uint) (int64, error) {
	return p.removeKeysByStatus(groupID)
}

// removeKeysByStatus is a generic function to remove keys by status.
// If no status is provided, it removes all keys in the group.
func (p *KeyProvider) removeKeysByStatus(groupID uint, status ...string) (int64, error) {
	var keysToRemove []models.APIKey
	var removedCount int64

	err := p.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("group_id = ?", groupID)
		if len(status) > 0 {
			query = query.Where("status IN ?", status)
		}

		if err := query.Find(&keysToRemove).Error; err != nil {
			return err
		}

		if len(keysToRemove) == 0 {
			return nil
		}

		deleteQuery := tx.Where("group_id = ?", groupID)
		if len(status) > 0 {
			deleteQuery = deleteQuery.Where("status IN ?", status)
		}
		result := deleteQuery.Delete(&models.APIKey{})
		if result.Error != nil {
			return result.Error
		}
		removedCount = result.RowsAffected

		for _, key := range keysToRemove {
			if err := p.removeKeyFromStore(key.ID, key.GroupID); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Error("Failed to remove key from store after DB deletion, rolling back transaction")
				return err
			}
		}
		return nil
	})

	return removedCount, err
}

// RemoveKeysFromStore directly removes the specified keys from the in-memory store without database operations.
// This method is suitable for scenarios where keys are already deleted from the database but need to be cleaned from the memory store.
func (p *KeyProvider) RemoveKeysFromStore(groupID uint, keyIDs []uint) error {
	if len(keyIDs) == 0 {
		return nil
	}

	// Use strconv instead of fmt.Sprintf for better performance
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"

	// Step 1: Delete the entire active_keys list
	if err := p.store.Delete(activeKeysListKey); err != nil {
		logrus.WithFields(logrus.Fields{
			"groupID": groupID,
			"error":   err,
		}).Error("Failed to delete active keys list")
		return err
	}

	// Step 2: Batch delete all related key hashes
	for _, keyID := range keyIDs {
		// Use strconv instead of fmt.Sprintf for better performance
		keyHashKey := "key:" + strconv.FormatUint(uint64(keyID), 10)
		if err := p.store.Delete(keyHashKey); err != nil {
			logrus.WithFields(logrus.Fields{
				"keyID": keyID,
				"error": err,
			}).Error("Failed to delete key hash")
		}
	}

	logrus.WithFields(logrus.Fields{
		"groupID":  groupID,
		"keyCount": len(keyIDs),
	}).Info("Successfully cleaned up group keys from store")

	return nil
}

// ClearAllKeys removes all keys from the in-memory store.
// This is a dangerous operation intended for debugging and testing purposes only.
// It clears all key-related data from the cache without touching the database.
//
// The operation performs the following steps:
// 1. Clears the entire store (all keys and group lists)
//
// This method should only be called when DEBUG_MODE is enabled and typically
// after all groups and keys have been deleted from the database.
//
// Returns an error if the store clear operation fails.
func (p *KeyProvider) ClearAllKeys() error {
	logrus.Warn("ClearAllKeys called - this will remove ALL keys from memory store")

	// Clear the entire store
	// This removes all keys, active key lists, and any other cached data
	if err := p.store.Clear(); err != nil {
		logrus.WithError(err).Error("Failed to clear key store")
		return fmt.Errorf("failed to clear key store: %w", err)
	}

	logrus.Info("Successfully cleared all keys from store")
	return nil
}

// addKeyToStore is a helper to add a single key to the cache.
func (p *KeyProvider) addKeyToStore(key *models.APIKey) error {
	// 1. Store key details in HASH
	// Use strconv instead of fmt.Sprintf for better performance
	keyHashKey := "key:" + strconv.FormatUint(uint64(key.ID), 10)
	keyDetails := p.apiKeyToMap(key)
	if err := p.store.HSet(keyHashKey, keyDetails); err != nil {
		return fmt.Errorf("failed to HSet key details for key %d: %w", key.ID, err)
	}

	// 2. If active, add to the active LIST
	if key.Status == models.KeyStatusActive {
		// Use strconv instead of fmt.Sprintf for better performance
		activeKeysListKey := "group:" + strconv.FormatUint(uint64(key.GroupID), 10) + ":active_keys"
		if err := p.store.LRem(activeKeysListKey, 0, key.ID); err != nil {
			return fmt.Errorf("failed to LRem key %d before LPush for group %d: %w", key.ID, key.GroupID, err)
		}
		if err := p.store.LPush(activeKeysListKey, key.ID); err != nil {
			return fmt.Errorf("failed to LPush key %d to group %d: %w", key.ID, key.GroupID, err)
		}
	}
	return nil
}

// LoadGroupKeysToStore loads all keys for a specific group from database to Redis store
// This is optimized for bulk loading after import operations
func (p *KeyProvider) LoadGroupKeysToStore(groupID uint) error {
	startTime := time.Now()

	// Load keys in batches for better performance
	batchSize := 1000
	var batchKeys []models.APIKey
	activeKeyIDs := make([]any, 0)

	err := p.db.Model(&models.APIKey{}).
		Where("group_id = ?", groupID).
		FindInBatches(&batchKeys, batchSize, func(tx *gorm.DB, batch int) error {
			// Use pipeline for better Redis performance
			var pipeline store.Pipeliner
			if redisStore, ok := p.store.(store.RedisPipeliner); ok {
				pipeline = redisStore.Pipeline()
			}

			for i := range batchKeys {
				key := &batchKeys[i]
				keyHashKey := "key:" + strconv.FormatUint(uint64(key.ID), 10)
				keyDetails := p.apiKeyToMap(key)

				if pipeline != nil {
					pipeline.HSet(keyHashKey, keyDetails)
				} else {
					if err := p.store.HSet(keyHashKey, keyDetails); err != nil {
						logrus.WithFields(logrus.Fields{"keyID": key.ID, "error": err}).Warn("Failed to HSet key details")
					}
				}

				if key.Status == models.KeyStatusActive {
					activeKeyIDs = append(activeKeyIDs, key.ID)
				}
			}

			if pipeline != nil {
				if err := pipeline.Exec(); err != nil {
					return fmt.Errorf("failed to execute pipeline for batch %d: %w", batch, err)
				}
			}
			return nil
		}).Error

	if err != nil {
		return fmt.Errorf("failed to load keys for group %d: %w", groupID, err)
	}

	// Update active keys list
	if len(activeKeyIDs) > 0 {
		activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
		p.store.Delete(activeKeysListKey)
		if err := p.store.LPush(activeKeysListKey, activeKeyIDs...); err != nil {
			return fmt.Errorf("failed to update active keys list for group %d: %w", groupID, err)
		}
	}

	duration := time.Since(startTime)
	logrus.Infof("Loaded %d keys to store for group %d in %v", len(activeKeyIDs), groupID, duration)
	return nil
}

// removeKeyFromStore is a helper to remove a single key from the cache.
func (p *KeyProvider) removeKeyFromStore(keyID, groupID uint) error {
	// Use strconv instead of fmt.Sprintf for better performance
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
	if err := p.store.LRem(activeKeysListKey, 0, keyID); err != nil {
		logrus.WithFields(logrus.Fields{"keyID": keyID, "groupID": groupID, "error": err}).Error("Failed to LRem key from active list")
	}

	// Use strconv instead of fmt.Sprintf for better performance
	keyHashKey := "key:" + strconv.FormatUint(uint64(keyID), 10)
	if err := p.store.Delete(keyHashKey); err != nil {
		return fmt.Errorf("failed to delete key HASH for key %d: %w", keyID, err)
	}
	return nil
}

// apiKeyToMap converts an APIKey model to a map for HSET.
func (p *KeyProvider) apiKeyToMap(key *models.APIKey) map[string]any {
	return map[string]any{
		"id":            fmt.Sprint(key.ID),
		"key_string":    key.KeyValue,
		"status":        key.Status,
		"failure_count": key.FailureCount,
		"group_id":      key.GroupID,
		"created_at":    key.CreatedAt.Unix(),
	}
}

// pluckIDs extracts IDs from a slice of APIKey.
func pluckIDs(keys []models.APIKey) []uint {
	ids := make([]uint, len(keys))
	for i, key := range keys {
		ids[i] = key.ID
	}
	return ids
}
