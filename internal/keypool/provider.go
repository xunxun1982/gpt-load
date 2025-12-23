package keypool

import (
	"context"
	"errors"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"
	"math/rand"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type KeyProvider struct {
	db              *gorm.DB
	store           store.Store
	settingsManager *config.SystemSettingsManager
	encryptionSvc   encryption.Service
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

// executeWithRetry runs a database operation with a small retry/backoff policy for lock contention.
func (p *KeyProvider) executeWithRetry(operation func(db *gorm.DB) error) error {
	const maxRetries = 3
	const baseDelay = 50 * time.Millisecond
	const maxJitter = 150 * time.Millisecond
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err = operation(p.db)
		if err == nil {
			return nil
		}

		if utils.IsDBLockError(err) {
			jitter := time.Duration(rand.Intn(int(maxJitter)))
			exponentialDelay := baseDelay * (1 << attempt)
			totalDelay := exponentialDelay + jitter
			logrus.Debugf("Database lock detected, retrying in %v... (attempt %d/%d)", totalDelay, attempt+1, maxRetries)
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

	dbUpdates := map[string]any{"failure_count": int64(0)}
	if !isActive {
		dbUpdates["status"] = models.KeyStatusActive
	}
	if err := p.executeWithRetry(func(db *gorm.DB) error {
		// Use UpdateColumns to avoid updated_at churn for hot-path stats updates.
		return db.Model(&models.APIKey{}).Where("id = ?", keyID).UpdateColumns(dbUpdates).Error
	}); err != nil {
		return fmt.Errorf("failed to update key in DB: %w", err)
	}

	if err := p.store.HSet(keyHashKey, dbUpdates); err != nil {
		return fmt.Errorf("failed to update key details in store: %w", err)
	}

	if !isActive {
		logrus.WithField("keyID", keyID).Debug("Key has recovered and is being restored to active pool")
		if err := p.store.LRem(activeKeysListKey, 0, keyID); err != nil {
			return fmt.Errorf("failed to LRem key before LPush on recovery: %w", err)
		}
		if err := p.store.LPush(activeKeysListKey, keyID); err != nil {
			return fmt.Errorf("failed to LPush key back to active list: %w", err)
		}

		if p.CacheInvalidationCallback != nil {
			p.CacheInvalidationCallback(groupID)
		}
	}

	return nil
}

func (p *KeyProvider) handleFailure(apiKey *models.APIKey, group *models.Group, keyHashKey, activeKeysListKey string) error {
	keyDetails, err := p.store.HGetAll(keyHashKey)
	if err != nil {
		return fmt.Errorf("failed to get key details from store: %w", err)
	}

	if keyDetails["status"] == models.KeyStatusInvalid {
		return nil
	}

	// Ensure EffectiveConfig is set for this group (use group-level config which may override system settings)
	// This ensures validation uses the correct group-specific blacklist_threshold, not just system settings
	// Always call GetEffectiveConfig as it is idempotent and cached, avoiding fragile sentinel checks
	group.EffectiveConfig = p.settingsManager.GetEffectiveConfig(group.Config)

	// Get the effective configuration for this group
	// This will use group-specific blacklist_threshold if set, otherwise fall back to system settings
	blacklistThreshold := group.EffectiveConfig.BlacklistThreshold

	// First update the DB, then update the store. This keeps DB as the source of truth when store updates fail.
	if err := p.executeWithRetry(func(db *gorm.DB) error {
		return db.Model(&models.APIKey{}).
			Where("id = ?", apiKey.ID).
			UpdateColumn("failure_count", gorm.Expr("failure_count + ?", 1)).Error
	}); err != nil {
		return fmt.Errorf("failed to increment failure_count in DB: %w", err)
	}

	newFailureCount, err := p.store.HIncrBy(keyHashKey, "failure_count", 1)
	if err != nil {
		return fmt.Errorf("failed to increment failure_count in store: %w", err)
	}

	shouldBlacklist := blacklistThreshold > 0 && newFailureCount >= int64(blacklistThreshold)
	if !shouldBlacklist {
		return nil
	}

	logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "threshold": blacklistThreshold}).Warn("Key has reached blacklist threshold, disabling")

	// Best-effort DB status update. Even if it fails, the key is disabled in store (selection source of truth).
	if err := p.executeWithRetry(func(db *gorm.DB) error {
		return db.Model(&models.APIKey{}).
			Where("id = ?", apiKey.ID).
			UpdateColumn("status", models.KeyStatusInvalid).Error
	}); err != nil {
		logrus.WithFields(logrus.Fields{"keyID": apiKey.ID, "error": err}).Error("Failed to mark key invalid in DB")
	}

	if err := p.store.LRem(activeKeysListKey, 0, apiKey.ID); err != nil {
		return fmt.Errorf("failed to LRem key from active list: %w", err)
	}
	if err := p.store.HSet(keyHashKey, map[string]any{"status": models.KeyStatusInvalid}); err != nil {
		return fmt.Errorf("failed to update key status to invalid in store: %w", err)
	}

	if p.CacheInvalidationCallback != nil {
		p.CacheInvalidationCallback(group.ID)
	}

	return nil
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
// Use very small DB transactions, then update cache outside the transaction to shorten lock time.
func (p *KeyProvider) AddKeys(groupID uint, keys []models.APIKey) error {
	if len(keys) == 0 {
		return nil
	}

	// Dialect-aware batch size (smaller for SQLite)
	smallBatchSize := 25 // Reduced from 50 to prevent blocking
	switch p.db.Dialector.Name() {
	case "mysql", "postgres":
		smallBatchSize = 100 // Reduced from 200 for smoother operation
	}

	for i := 0; i < len(keys); i += smallBatchSize {
		end := i + smallBatchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]

		// Step 1: Insert this batch in a short transaction
		if err := p.db.Transaction(func(tx *gorm.DB) error {
			return tx.Create(&batch).Error
		}); err != nil {
			return err
		}

		// Step 2: Update in-memory store outside the transaction (best-effort)
		for j := range batch {
			if err := p.addKeyToStore(&batch[j]); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": batch[j].ID, "error": err}).Warn("Failed to add key to store; will be refreshed on next reload")
			}
		}

		// Short delay between batches to avoid monopolizing the DB
		// Increased delay for better concurrency with other operations
		if i+smallBatchSize < len(keys) {
			time.Sleep(20 * time.Millisecond) // Increased from 10ms
		}
	}

	return nil
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

		// Invalidate cache after resetting failure counts to ensure consistency
		// This matches the behavior in ResetAllActiveKeysFailureCount
		if p.CacheInvalidationCallback != nil {
			p.CacheInvalidationCallback(groupID)
		}
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

	// Use cursor-based pagination to avoid skipping keys
	// When we update failure_count to 0, those keys are removed from the result set
	// So we use WHERE id > lastID to query the next batch, ensuring no keys are skipped
	var lastID uint = 0
	for {
		var keys []models.APIKey
		query := p.db.Select("id, group_id").
			Where("status = ?", models.KeyStatusActive).
			Where("failure_count > 0").
			Order("id ASC"). // Stable ordering for consistent results
			Limit(batchSize)

		if lastID > 0 {
			query = query.Where("id > ?", lastID)
		}

		if err := query.Find(&keys).Error; err != nil {
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

		// Update lastID for cursor-based pagination
		lastID = keys[len(keys)-1].ID

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

// RemoveAllKeys removes all keys in the group using chunked deletion with dialect-specific SQL.
// This minimizes lock holding time and works across SQLite, MySQL, and PostgreSQL.
// global deletion semaphore to serialize heavy group deletions (especially for SQLite)
var deleteSem = make(chan struct{}, 1)

func (p *KeyProvider) RemoveAllKeys(ctx context.Context, groupID uint) (int64, error) {
	const chunkSize = 500
	const maxRetries = 5

	// serialize deletions
	deleteSem <- struct{}{}
	defer func() { <-deleteSem }()

	totalDeleted := int64(0)
	dial := p.db.Dialector.Name()
	retries := 0

	for {
		// Check if context is canceled before each batch
		if err := ctx.Err(); err != nil {
			return totalDeleted, err
		}

		var res *gorm.DB
		batchCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		switch dial {
		case "sqlite":
			res = p.db.WithContext(batchCtx).Exec("DELETE FROM api_keys WHERE rowid IN (SELECT rowid FROM api_keys WHERE group_id = ? LIMIT ?)", groupID, chunkSize)
		case "mysql":
			res = p.db.WithContext(batchCtx).Exec("DELETE FROM api_keys WHERE group_id = ? ORDER BY id LIMIT ?", groupID, chunkSize)
		case "postgres":
			res = p.db.WithContext(batchCtx).Exec("WITH c AS (SELECT id FROM api_keys WHERE group_id = ? ORDER BY id LIMIT ?) DELETE FROM api_keys WHERE id IN (SELECT id FROM c)", groupID, chunkSize)
		default:
			res = p.db.WithContext(batchCtx).Where("group_id = ?", groupID).Delete(&models.APIKey{})
		}
		cancel()

		if res.Error != nil {
			// Retry with exponential backoff on transient/timeout errors
			if utils.IsTransientDBError(res.Error) {
				if retries >= maxRetries {
					return totalDeleted, res.Error
				}
				delay := time.Duration(25<<retries) * time.Millisecond
				if delay > 500*time.Millisecond {
					delay = 500 * time.Millisecond
				}
				logrus.WithError(res.Error).Debugf("Transient delete failure; retrying in %v (attempt %d/%d)", delay, retries+1, maxRetries)
				time.Sleep(delay)
				retries++
				continue
			}
			return totalDeleted, res.Error
		}
		retries = 0

		affected := res.RowsAffected
		totalDeleted += affected
		if affected == 0 || affected < chunkSize {
			break
		}

		// Short yield to let other operations proceed
		time.Sleep(10 * time.Millisecond)
	}

	// Clear active key list for the group to prevent stale usage
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
	_ = p.store.Delete(activeKeysListKey)

	return totalDeleted, nil
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

	// Step 2: Batch delete all related key hashes (optimized for large batches)
	// Process in smaller batches to avoid blocking other operations
	batchSize := 100
	for i := 0; i < len(keyIDs); i += batchSize {
		end := i + batchSize
		if end > len(keyIDs) {
			end = len(keyIDs)
		}

		// Delete batch of keys
		for _, keyID := range keyIDs[i:end] {
			// Use strconv instead of fmt.Sprintf for better performance
			keyHashKey := "key:" + strconv.FormatUint(uint64(keyID), 10)
			if err := p.store.Delete(keyHashKey); err != nil {
				// Log but don't fail the entire operation for individual key failures
				logrus.WithFields(logrus.Fields{
					"keyID": keyID,
					"error": err,
				}).Debug("Failed to delete key hash")
			}
		}

		// Small yield to avoid blocking other operations
		if i+batchSize < len(keyIDs) {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Don't log here - let the caller log with appropriate context and timing
	// This avoids duplicate logs when the caller also logs the operation

	return nil
}

// RemoveOrphanedKeysFromStore removes any orphaned keys for a group that no longer exists
// This is a best-effort cleanup operation for idempotent delete scenarios
func (p *KeyProvider) RemoveOrphanedKeysFromStore(groupID uint) error {
	// Use strconv instead of fmt.Sprintf for better performance
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"

	// Try to delete the active_keys list if it exists
	if err := p.store.Delete(activeKeysListKey); err != nil {
		// This is expected if the group was already cleaned up
		logrus.WithFields(logrus.Fields{
			"groupID": groupID,
		}).Debug("No orphaned keys found for group")
	}

	// Note: We can't clean individual key hashes without knowing the key IDs
	// They will be cleaned up on next access attempt

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

	// Increase batch size for better performance with large key sets
	batchSize := 5000 // Increased from 1000
	var batchKeys []models.APIKey
	activeKeyIDs := make([]any, 0)
	totalProcessed := 0

	err := p.db.Model(&models.APIKey{}).
		Select("id, key_value, status, failure_count, group_id, created_at"). // Only select needed fields
		Where("group_id = ?", groupID).
		FindInBatches(&batchKeys, batchSize, func(tx *gorm.DB, batch int) error {
			totalProcessed += len(batchKeys)
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
	logrus.Infof("Loaded %d keys (%d active) to store for group %d in %v", totalProcessed, len(activeKeyIDs), groupID, duration)
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
		"id":            strconv.FormatUint(uint64(key.ID), 10), // Use strconv for better performance
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
