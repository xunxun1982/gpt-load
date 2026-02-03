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
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// statusUpdateTask represents a key status update task for the worker pool.
type statusUpdateTask struct {
	apiKey       *models.APIKey
	group        *models.Group
	isSuccess    bool
	errorMessage string
}

type KeyProvider struct {
	db              *gorm.DB
	store           store.Store
	settingsManager *config.SystemSettingsManager
	encryptionSvc   encryption.Service
	// CacheInvalidationCallback is an optional callback for cache invalidation.
	// Note: This callback will be invoked from a goroutine (spawned in UpdateStatus),
	// so implementers must handle concurrent access if the callback accesses shared state.
	CacheInvalidationCallback func(groupID uint)

	// Worker pool for status updates to control concurrency
	statusUpdateChan chan statusUpdateTask
	workerWg         sync.WaitGroup
	stopOnce         sync.Once
	stopChan         chan struct{}
}

// NewProvider creates a new KeyProvider instance with worker pool.
func NewProvider(db *gorm.DB, store store.Store, settingsManager *config.SystemSettingsManager, encryptionSvc encryption.Service) *KeyProvider {
	// Calculate worker count based on CPU cores
	workerCount := runtime.NumCPU() * 2
	if workerCount < 4 {
		workerCount = 4
	}
	if workerCount > 16 {
		workerCount = 16
	}

	p := &KeyProvider{
		db:               db,
		store:            store,
		settingsManager:  settingsManager,
		encryptionSvc:    encryptionSvc,
		statusUpdateChan: make(chan statusUpdateTask, 1000), // Buffered channel for backpressure
		stopChan:         make(chan struct{}),
	}

	// Start worker pool
	for i := 0; i < workerCount; i++ {
		p.workerWg.Add(1)
		go p.statusUpdateWorker()
	}

	logrus.Debugf("KeyProvider initialized with %d status update workers", workerCount)
	return p
}

// statusUpdateWorker processes status update tasks from the channel.
func (p *KeyProvider) statusUpdateWorker() {
	defer p.workerWg.Done()

	for {
		select {
		case task, ok := <-p.statusUpdateChan:
			if !ok {
				return
			}
			p.processStatusUpdate(task)
		case <-p.stopChan:
			// Drain remaining tasks before exiting
			for {
				select {
				case task, ok := <-p.statusUpdateChan:
					if !ok {
						return
					}
					p.processStatusUpdate(task)
				default:
					return
				}
			}
		}
	}
}

// processStatusUpdate handles a single status update task.
func (p *KeyProvider) processStatusUpdate(task statusUpdateTask) {
	// Use strconv instead of fmt.Sprintf for better performance
	keyHashKey := "key:" + strconv.FormatUint(uint64(task.apiKey.ID), 10)
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(task.group.ID), 10) + ":active_keys"

	if task.isSuccess {
		if err := p.handleSuccess(task.apiKey.ID, keyHashKey, activeKeysListKey, task.group.ID); err != nil {
			logrus.WithFields(logrus.Fields{"keyID": task.apiKey.ID, "error": err}).Error("Failed to handle key success")
		}
	} else {
		if app_errors.IsUnCounted(task.errorMessage) {
			logrus.WithFields(logrus.Fields{
				"keyID": task.apiKey.ID,
				"error": task.errorMessage,
			}).Debug("Uncounted error, skipping failure handling")
		} else {
			if err := p.handleFailure(task.apiKey, task.group, keyHashKey, activeKeysListKey); err != nil {
				logrus.WithFields(logrus.Fields{"keyID": task.apiKey.ID, "error": err}).Error("Failed to handle key failure")
			}
		}
	}
}

// Stop gracefully shuts down the worker pool.
func (p *KeyProvider) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopChan)
		// Wait for workers to finish with timeout
		done := make(chan struct{})
		go func() {
			p.workerWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			logrus.Debug("KeyProvider worker pool stopped gracefully")
		case <-time.After(5 * time.Second):
			logrus.Warn("KeyProvider worker pool stop timed out")
		}
	})
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

// UpdateStatus submits a key status update task to the worker pool.
// Uses bounded concurrency to prevent resource exhaustion.
// Logs a warning when channel is full to enable monitoring of backpressure.
// Note: Synchronous fallback blocks callers (proxy error handlers) on store operations,
// which may increase client response latency under sustained load. This is acceptable
// as it provides backpressure to prevent unbounded goroutine creation.
func (p *KeyProvider) UpdateStatus(apiKey *models.APIKey, group *models.Group, isSuccess bool, errorMessage string) {
	task := statusUpdateTask{
		apiKey:       apiKey,
		group:        group,
		isSuccess:    isSuccess,
		errorMessage: errorMessage,
	}

	select {
	case p.statusUpdateChan <- task:
		// Task submitted successfully
	default:
		// Channel full, process synchronously to avoid data loss
		// Note: Using sync processing instead of spawning goroutine to prevent
		// unbounded goroutine creation when channel is persistently full
		// Log warning to enable monitoring of channel overflow events
		logrus.WithFields(logrus.Fields{
			"key_id":    apiKey.ID,
			"group_id":  group.ID,
			"is_success": isSuccess,
		}).Warn("Status update channel full (1000 capacity), processing synchronously - may increase client latency")
		p.processStatusUpdate(task)
	}
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
	// Design note: If DB succeeds but store fails, there will be temporary divergence between DB and store.
	// This is acceptable because:
	// 1. DB is the authoritative source of truth
	// 2. Store is a cache for fast key selection
	// 3. On startup, LoadKeysFromDB() resyncs store from DB
	// 4. The divergence only affects failure_count, not key availability
	// Adding a compensating mechanism (periodic sync) was considered but rejected as over-engineering
	// for this use case where eventual consistency is sufficient.
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
// Uses parallel loading with work-stealing to improve startup performance for large datasets.
func (p *KeyProvider) LoadKeysFromDB() error {
	startTime := time.Now()

	// Get total count and ID range for parallel loading
	var totalCount int64
	var minID, maxID uint
	if err := p.db.Model(&models.APIKey{}).Count(&totalCount).Error; err != nil {
		return fmt.Errorf("failed to count keys: %w", err)
	}

	if totalCount == 0 {
		logrus.Info("No keys to load from database")
		return nil
	}

	// Get min and max ID for range partitioning
	if err := p.db.Model(&models.APIKey{}).Select("MIN(id) as min_id, MAX(id) as max_id").Row().Scan(&minID, &maxID); err != nil {
		return fmt.Errorf("failed to get ID range: %w", err)
	}

	logrus.Infof("Loading %d keys from database to store (ID range: %d-%d)...", totalCount, minID, maxID)

	// Determine number of parallel workers based on CPU cores and database type
	// SQLite: Use fewer workers to avoid lock contention (single-writer model)
	// MySQL/PostgreSQL: Use more workers for better parallelism
	dbType := p.db.Dialector.Name()
	numWorkers := runtime.NumCPU() / 2
	if numWorkers < 1 {
		numWorkers = 1
	}

	// Database-specific worker count optimization
	switch dbType {
	case "sqlite", "sqlite3":
		// SQLite has single-writer model, use minimal workers to avoid lock contention
		// Reduced from 4 to 2 to minimize lock contention during batch reads
		if numWorkers > 2 {
			numWorkers = 2
		}
		logrus.Debugf("Using %d workers for SQLite (reduced to avoid lock contention)", numWorkers)
	case "mysql", "postgres", "postgresql", "pgx":
		// MySQL and PostgreSQL handle concurrent reads well
		if numWorkers > 8 {
			numWorkers = 8
		}
		logrus.Debugf("Using %d workers for %s", numWorkers, dbType)
	default:
		// Unknown database, use conservative settings
		if numWorkers > 4 {
			numWorkers = 4
		}
		logrus.Debugf("Using %d workers for unknown database type %s", numWorkers, dbType)
	}

	// Task chunk size: each task processes a range of IDs
	// Smaller chunks enable better load balancing through work-stealing
	// Reduced from 30000 to 10000 for finer-grained task distribution
	const taskChunkSize uint = 10000

	// Create task queue with all ID ranges
	type task struct {
		startID uint
		endID   uint
	}
	taskQueue := make(chan task, 100) // Buffered channel to avoid blocking

	// Create context for canceling workers on first error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Generate tasks
	go func() {
		defer close(taskQueue)
		for start := minID; start <= maxID; start += taskChunkSize {
			end := start + taskChunkSize - 1
			if end > maxID {
				end = maxID
			}
			select {
			case <-ctx.Done():
				return
			case taskQueue <- task{startID: start, endID: end}:
			}
		}
	}()

	// Shared data structures with mutex protection
	var mu sync.Mutex
	allActiveKeyIDs := make(map[uint][]any)
	allGroupIDs := make(map[uint]struct{}) // Track all groups seen, even those with no active keys
	var processedKeys int64                // Use atomic operations
	lastLoggedPercent := 0

	// Error channel to collect errors from workers
	errChan := make(chan error, numWorkers)
	var wg sync.WaitGroup

	// Batch size for each worker
	// Reduced from 10000 to 2000 to minimize single query execution time
	// This reduces slow SQL warnings (queries taking >200ms) during startup
	batchSize := 2000

	// Launch parallel workers with work-stealing
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		workerID := i

		go func(workerID int) {
			defer wg.Done()

			tasksProcessed := 0
			for t := range taskQueue {
				tasksProcessed++
				logrus.Debugf("Worker %d: processing task %d (ID range %d-%d)", workerID, tasksProcessed, t.startID, t.endID)

				// Guard against uint underflow when startID is 0
				lastID := t.startID
				if lastID > 0 {
					lastID--
				}
				firstQuery := true

				for {
					var batchKeys []models.APIKey

					// Use cursor-based query to minimize lock time
					// Only select necessary fields to reduce data transfer and improve performance
					query := p.db.Model(&models.APIKey{}).
						Select("id", "key_value", "status", "failure_count", "group_id", "created_at").
						Where("id > ? AND id <= ?", lastID, t.endID).
						Order("id ASC").
						Limit(batchSize)

					if err := query.Find(&batchKeys).Error; err != nil {
						errChan <- fmt.Errorf("worker %d failed to load keys batch: %w", workerID, err)
						cancel() // Cancel other workers on error
						return
					}

					// Early exit optimization: if first query returns 0 rows, skip this task
					// This avoids scanning large empty ID ranges (deleted keys)
					if firstQuery && len(batchKeys) == 0 {
						logrus.Debugf("Worker %d: task %d has no data, skipping", workerID, tasksProcessed)
						break
					}
					firstQuery = false

					if len(batchKeys) == 0 {
						break
					}

					// Process batch and write to store
					var pipeline store.Pipeliner
					if redisStore, ok := p.store.(store.RedisPipeliner); ok {
						pipeline = redisStore.Pipeline()
					}

					// Local active key IDs for this batch
					localActiveKeyIDs := make(map[uint][]any)
					localGroupIDs := make(map[uint]struct{}) // Track all groups in this batch

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

						// Track all groups seen, regardless of key status
						localGroupIDs[key.GroupID] = struct{}{}

						if key.Status == models.KeyStatusActive {
							localActiveKeyIDs[key.GroupID] = append(localActiveKeyIDs[key.GroupID], key.ID)
						}
					}

					if pipeline != nil {
						if err := pipeline.Exec(); err != nil {
							errChan <- fmt.Errorf("worker %d failed to execute pipeline: %w", workerID, err)
							cancel() // Cancel other workers on error
							return
						}
					}

					// Merge local active key IDs and group IDs into global maps with mutex protection
					mu.Lock()
					for groupID, activeIDs := range localActiveKeyIDs {
						allActiveKeyIDs[groupID] = append(allActiveKeyIDs[groupID], activeIDs...)
					}
					for groupID := range localGroupIDs {
						allGroupIDs[groupID] = struct{}{}
					}
					mu.Unlock()

					// Update progress atomically
					currentProcessed := atomic.AddInt64(&processedKeys, int64(len(batchKeys)))

					// Log progress at 10%, 20%, 30%... milestones for better user feedback
					if totalCount > 0 {
						currentPercent := (int(currentProcessed) * 100) / int(totalCount)
						mu.Lock()
						if currentPercent >= lastLoggedPercent+10 && currentPercent < 100 {
							logrus.Infof("Loading progress: %d%% (%d/%d keys)", currentPercent, currentProcessed, totalCount)
							lastLoggedPercent = currentPercent
						}
						mu.Unlock()
					}

					lastID = batchKeys[len(batchKeys)-1].ID

					// If we got fewer than batchSize or reached endID, we're done with this task
					if len(batchKeys) < batchSize || lastID >= t.endID {
						break
					}
				}
			}

			logrus.Debugf("Worker %d: completed loading (%d tasks processed)", workerID, tasksProcessed)
		}(workerID)
	}

	// Wait for all workers to complete
	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	// Update active_keys list for all groups
	// Clear lists for all groups seen, then populate only those with active keys
	// This ensures groups with no active keys have empty lists (preventing stale data)
	logrus.Info("Updating active key lists for all groups...")
	for groupID := range allGroupIDs {
		activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
		_ = p.store.Delete(activeKeysListKey) // Clear existing list

		// Only populate if there are active keys for this group
		if activeIDs := allActiveKeyIDs[groupID]; len(activeIDs) > 0 {
			if err := p.store.LPush(activeKeysListKey, activeIDs...); err != nil {
				logrus.WithFields(logrus.Fields{"groupID": groupID, "error": err}).Error("Failed to LPush active keys for group")
			}
		}
	}

	duration := time.Since(startTime)
	logrus.Infof("Successfully loaded %d keys to store in %v (using %d parallel workers with work-stealing)", processedKeys, duration, numWorkers)
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

		// Step 2: Update in-memory store outside the transaction using batch method
		if err := p.addKeysToCacheBatch(groupID, batch); err != nil {
			logrus.WithFields(logrus.Fields{"batchSize": len(batch), "error": err}).Warn("Failed to add batch to store; will be refreshed on next reload")
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

	// Invalidate cache after restoring keys (status changed from invalid to active)
	if err == nil && restoredCount > 0 && p.CacheInvalidationCallback != nil {
		p.CacheInvalidationCallback(groupID)
	}

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

	// Invalidate cache after restoring keys (status changed from invalid to active)
	if err == nil && restoredCount > 0 && p.CacheInvalidationCallback != nil {
		p.CacheInvalidationCallback(groupID)
	}

	return restoredCount, err
}

// ResetGroupActiveKeysFailureCount resets failure_count to 0 for all active keys in a specific group.
// This is useful when importing a group, treating it as a fresh import.
// Note: For newly imported keys, failure_count is already 0, so this is a no-op in that case.
func (p *KeyProvider) ResetGroupActiveKeysFailureCount(groupID uint) (int64, error) {
	// Update failure_count to 0 in database for all active keys with failure_count > 0
	result := p.db.Model(&models.APIKey{}).
		Where("group_id = ? AND status = ? AND failure_count > 0", groupID, models.KeyStatusActive).
		Update("failure_count", 0)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to reset failure_count in database: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return 0, nil
	}

	// Database was updated, now sync Redis store
	// Query all active keys and set failure_count to 0 in Redis
	// This is a best-effort operation - if it fails, the database is still correct
	batchSize := 5000
	var lastID uint = 0

	for {
		var batchKeys []struct{ ID uint }
		if err := p.db.Model(&models.APIKey{}).
			Select("id").
			Where("group_id = ? AND status = ? AND id > ?", groupID, models.KeyStatusActive, lastID).
			Order("id ASC").
			Limit(batchSize).
			Find(&batchKeys).Error; err != nil {
			logrus.WithError(err).Warn("Failed to query keys for store update, but database update succeeded")
			break
		}

		if len(batchKeys) == 0 {
			break
		}

		for _, key := range batchKeys {
			keyHashKey := "key:" + strconv.FormatUint(uint64(key.ID), 10)
			if err := p.store.HSet(keyHashKey, map[string]any{"failure_count": 0}); err != nil {
				logrus.WithFields(logrus.Fields{
					"keyID": key.ID,
					"error": err,
				}).Warn("Failed to reset failure_count in store, continuing with other keys")
			}
			lastID = key.ID
		}

		if len(batchKeys) < batchSize {
			break
		}
	}

	logrus.Infof("Reset failure_count for %d active keys in group %d", result.RowsAffected, groupID)

	if p.CacheInvalidationCallback != nil {
		p.CacheInvalidationCallback(groupID)
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
// Optimized for large-scale deletions (500K+ records) with progress tracking.
// global deletion semaphore to serialize heavy group deletions (especially for SQLite)
var deleteSem = make(chan struct{}, 1)

func (p *KeyProvider) RemoveAllKeys(ctx context.Context, groupID uint, progressCallback func(deleted int64)) (int64, error) {
	const maxRetries = 3

	// Batch operation thresholds (aligned with services/thresholds.go)
	// Note: Cannot import services package due to circular dependency
	// (services imports keypool, keypool would import services)
	// AI Review Note: Suggested extracting to internal/constants/thresholds.go to avoid duplication.
	// Decision: Keep duplication to avoid circular dependency complexity. The constants are stable
	// and rarely change. Manual synchronization is acceptable given the low maintenance cost.
	// These values MUST match internal/services/thresholds.go for consistency.
	const (
		BulkSyncThreshold         = 5000   // Must match services.BulkSyncThreshold
		OptimizedSyncThreshold    = 20000  // Must match services.OptimizedSyncThreshold
		MassiveAsyncThreshold     = 100000 // Must match services.MassiveAsyncThreshold
		MaxSQLiteBatchSizeMassive = 10000
		MaxMySQLBatchSize         = 10000
	)

	// Serialize deletions, but honor ctx cancellation
	select {
	case deleteSem <- struct{}{}:
		defer func() { <-deleteSem }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	totalDeleted := int64(0)
	retries := 0
	lastLoggedPercent := 0

	// Get total count for progress reporting and dynamic batch sizing (with timeout to avoid blocking)
	var totalCount int64
	countCtx, countCancel := context.WithTimeout(ctx, 2*time.Second)
	defer countCancel()
	if err := p.db.WithContext(countCtx).Model(&models.APIKey{}).Where("group_id = ?", groupID).Count(&totalCount).Error; err != nil {
		logrus.WithError(err).Warn("Failed to get total count for progress tracking, continuing without progress")
		totalCount = 0
	}

	// Dynamic batch size and timeout based on total count for optimal performance
	// Strategy: Balance total deletion time with concurrent operation responsiveness
	// - Small datasets (≤5K): Small batches for quick completion and good responsiveness
	// - Medium datasets (≤20K): Medium batches for balanced performance
	// - Large datasets (20K-100K): Large batches to minimize total time for async operations
	// - Massive datasets (>100K): Very large batches optimized for 500K+ operations
	//
	// Note: After SQL optimization (query IDs first, then delete by primary key),
	// large batches no longer cause severe performance degradation.
	// The main concern is now lock contention, addressed by inter-batch delays.
	var chunkSize int
	var batchTimeout time.Duration
	var batchDelay time.Duration

	// Get database type for dialect-specific optimizations
	dbDialect := p.db.Dialector.Name()

	if totalCount <= int64(BulkSyncThreshold) {
		// Tier 1-2: Small batches for fast sync operations
		// Target: Quick completion (<15s) with minimal lock contention
		chunkSize = 1000
		batchTimeout = 2000 * time.Millisecond
		batchDelay = 20 * time.Millisecond
	} else if totalCount <= int64(OptimizedSyncThreshold) {
		// Tier 3-4: Medium batches for large sync operations
		// Target: Reasonable completion time (15-60s) with acceptable lock contention
		chunkSize = 2000
		batchTimeout = 3000 * time.Millisecond
		batchDelay = 30 * time.Millisecond
	} else if totalCount <= int64(MassiveAsyncThreshold) {
		// Tier 5: Large batches for async operations (20K-100K keys)
		// Target: Minimize total time for background tasks
		// Example: 39414 keys = 8 batches instead of 40 batches with 1000/batch
		chunkSize = 5000
		batchTimeout = 5000 * time.Millisecond
		batchDelay = 50 * time.Millisecond
	} else {
		// Tier 6: Massive batches for very large async operations (>100K keys)
		// Optimized for 500K+ operations with minimal transaction overhead
		// Example: 500K keys = 50 batches with 10K/batch (vs 100 batches with 5K/batch)
		// SQLite uses smaller batches due to single-writer model
		if dbDialect == "sqlite" {
			chunkSize = MaxSQLiteBatchSizeMassive // 10000
			batchTimeout = 8000 * time.Millisecond
			batchDelay = 100 * time.Millisecond // Longer delay for SQLite to allow concurrent reads
		} else {
			// MySQL/PostgreSQL can handle larger batches efficiently
			chunkSize = MaxMySQLBatchSize // 10000 (same as MaxPostgresBatchSize)
			batchTimeout = 10000 * time.Millisecond
			batchDelay = 20 * time.Millisecond // Shorter delay for MySQL/PostgreSQL
		}
	}

	if totalCount > 0 {
		estimatedBatches := (totalCount + int64(chunkSize) - 1) / int64(chunkSize)
		logrus.Infof("Starting deletion of %d keys in group %d (batch size: %d, estimated batches: %d)", totalCount, groupID, chunkSize, estimatedBatches)
	}

	var totalRetries int // Track total retry attempts for final logging

	for {
		// Check if context is canceled before each batch
		if err := ctx.Err(); err != nil {
			logrus.Infof("Deletion canceled after deleting %d keys", totalDeleted)
			return totalDeleted, err
		}

		var res *gorm.DB
		var ids []uint
		batchCtx, cancel := context.WithTimeout(ctx, batchTimeout)

		// Fetch IDs first for all dialects to enable consistent cache cleanup
		// This ensures we can delete both DB records and cache entries
		if err := p.db.WithContext(batchCtx).Model(&models.APIKey{}).
			Select("id").
			Where("group_id = ?", groupID).
			Order("id ASC").
			Limit(chunkSize).
			Pluck("id", &ids).Error; err != nil {
			cancel()
			if utils.IsTransientDBError(err) {
				if retries >= maxRetries {
					logrus.WithError(err).Errorf("Max retries reached after deleting %d keys (total retries: %d)", totalDeleted, totalRetries)
					return totalDeleted, err
				}
				delay := time.Duration(50<<retries) * time.Millisecond
				if delay > 1000*time.Millisecond {
					delay = 1000 * time.Millisecond
				}
				// Use Warn level for transient errors so users know what's happening
				logrus.WithError(err).Warnf("Transient query error during deletion (progress: %d/%d keys); retrying in %v (attempt %d/%d)", totalDeleted, totalCount, delay, retries+1, maxRetries)
				time.Sleep(delay)
				retries++
				totalRetries++
				continue
			}
			logrus.WithError(err).Errorf("Failed to query IDs after deleting %d keys", totalDeleted)
			return totalDeleted, err
		}
		if len(ids) == 0 {
			cancel()
			break
		}

		// Delete by ID list using primary key index (fast for all databases)
		res = p.db.WithContext(batchCtx).Where("id IN ?", ids).Delete(&models.APIKey{})
		cancel()

		if res.Error != nil {
			// Retry with exponential backoff on transient/timeout errors
			if utils.IsTransientDBError(res.Error) {
				if retries >= maxRetries {
					logrus.WithError(res.Error).Errorf("Max retries reached after deleting %d keys (total retries: %d)", totalDeleted, totalRetries)
					return totalDeleted, res.Error
				}
				delay := time.Duration(50<<retries) * time.Millisecond
				if delay > 1000*time.Millisecond {
					delay = 1000 * time.Millisecond
				}
				// Use Warn level for transient errors so users know what's happening
				logrus.WithError(res.Error).Warnf("Transient delete error (progress: %d/%d keys); retrying in %v (attempt %d/%d)", totalDeleted, totalCount, delay, retries+1, maxRetries)
				time.Sleep(delay)
				retries++
				totalRetries++
				continue
			}
			logrus.WithError(res.Error).Errorf("Deletion failed after deleting %d keys", totalDeleted)
			return totalDeleted, res.Error
		}

		// Log successful recovery after retries
		if retries > 0 {
			logrus.Infof("Recovered from transient errors after %d retries (progress: %d/%d keys)", retries, totalDeleted, totalCount)
		}
		retries = 0

		affected := res.RowsAffected
		totalDeleted += affected

		// Best-effort cache cleanup: delete key hashes from store
		// This prevents memory bloat from deleted keys remaining in Redis/MemoryStore
		for _, id := range ids {
			keyHashKey := "key:" + strconv.FormatUint(uint64(id), 10)
			if err := p.store.Delete(keyHashKey); err != nil {
				logrus.WithFields(logrus.Fields{
					"keyID": id,
					"error": err,
				}).Warn("Failed to delete key hash from store")
				// Continue with other keys even if one fails
			}
		}

		// Progress callback for task tracking
		if progressCallback != nil {
			progressCallback(totalDeleted)
		}

		// Log progress at 10% intervals
		if totalCount > 0 {
			currentPercent := int((totalDeleted * 100) / totalCount)
			if currentPercent >= lastLoggedPercent+10 && currentPercent < 100 {
				logrus.Infof("Deletion progress: %d%% (%d/%d keys)", currentPercent, totalDeleted, totalCount)
				lastLoggedPercent = currentPercent
			}
		}

		// Stop when no more keys to fetch (use fetched ID count, not RowsAffected)
		// This prevents early termination under concurrent deletes
		if len(ids) < chunkSize {
			break
		}

		// Increased delay between batches to allow other operations to execute
		// This is critical for SQLite to prevent monopolizing the database lock
		time.Sleep(batchDelay)
	}

	// Clear active key list for the group to prevent stale usage
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
	_ = p.store.Delete(activeKeysListKey)

	// Log completion with retry statistics if any retries occurred
	if totalRetries > 0 {
		logrus.Infof("Completed deletion of %d keys in group %d (recovered from %d transient errors)", totalDeleted, groupID, totalRetries)
	} else {
		logrus.Infof("Completed deletion of %d keys in group %d", totalDeleted, groupID)
	}
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

// addKeysToCacheBatch batch adds keys to cache (optimized for bulk import scenarios).
// Uses Redis Pipeline for efficient batch operations when available.
func (p *KeyProvider) addKeysToCacheBatch(groupID uint, keys []models.APIKey) error {
	if len(keys) == 0 {
		return nil
	}

	// 1. Batch HSet key details
	if pipeliner, ok := p.store.(store.RedisPipeliner); ok {
		// Redis: Use Pipeline for batch operations
		pipe := pipeliner.Pipeline()
		for i := range keys {
			keyHashKey := "key:" + strconv.FormatUint(uint64(keys[i].ID), 10)
			pipe.HSet(keyHashKey, p.apiKeyToMap(&keys[i]))
		}
		if err := pipe.Exec(); err != nil {
			return fmt.Errorf("failed to batch HSet keys: %w", err)
		}
	} else {
		// MemoryStore: Fallback to individual HSet operations
		for i := range keys {
			keyHashKey := "key:" + strconv.FormatUint(uint64(keys[i].ID), 10)
			if err := p.store.HSet(keyHashKey, p.apiKeyToMap(&keys[i])); err != nil {
				return fmt.Errorf("failed to HSet key %d: %w", keys[i].ID, err)
			}
		}
	}

	// 2. Collect active key IDs
	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
	activeKeyIDs := make([]any, 0, len(keys))
	for i := range keys {
		if keys[i].Status == models.KeyStatusActive {
			activeKeyIDs = append(activeKeyIDs, keys[i].ID)
		}
	}

	// 3. Batch LPush active keys
	// De-duplicate any existing entries to avoid skewed rotation on retry/partial failure
	// Note: LRem/LPush operations are not pipelined because the current Pipeliner interface
	// only supports HSet. Extending the interface would add complexity for minimal benefit
	// in this specific use case (list operations are typically fast).
	if len(activeKeyIDs) > 0 {
		// Remove existing entries before LPush to prevent duplicates
		for _, id := range activeKeyIDs {
			if err := p.store.LRem(activeKeysListKey, 0, id); err != nil {
				return fmt.Errorf("failed to LRem key %v before LPush: %w", id, err)
			}
		}
		if err := p.store.LPush(activeKeysListKey, activeKeyIDs...); err != nil {
			return fmt.Errorf("failed to batch LPush keys to group %d: %w", groupID, err)
		}
	}

	return nil
}

// LoadGroupKeysToStore loads all keys for a specific group from database to Redis store
// This is optimized for bulk loading after import operations
func (p *KeyProvider) LoadGroupKeysToStore(groupID uint) error {
	startTime := time.Now()

	// Batch size for database queries
	batchSize := 5000
	// Batch size for Redis LPush operations to avoid memory issues with large key sets
	redisBatchSize := 10000
	var batchKeys []models.APIKey
	activeKeyIDs := make([]any, 0, redisBatchSize)
	totalProcessed := 0
	totalActiveKeys := 0

	activeKeysListKey := "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
	// Delete existing active keys list before loading
	p.store.Delete(activeKeysListKey)

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
					// Flush active key IDs to Redis in batches to avoid memory buildup
					// No need for LRem here since we deleted the entire list at the start of this function
					if len(activeKeyIDs) >= redisBatchSize {
						if err := p.store.LPush(activeKeysListKey, activeKeyIDs...); err != nil {
							logrus.WithError(err).Warnf("Failed to LPush batch of active keys for group %d", groupID)
						}
						totalActiveKeys += len(activeKeyIDs)
						activeKeyIDs = activeKeyIDs[:0] // Reset slice, reuse underlying array
					}
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

	// Flush remaining active key IDs
	// No need for LRem here since we deleted the entire list at the start of this function
	if len(activeKeyIDs) > 0 {
		if err := p.store.LPush(activeKeysListKey, activeKeyIDs...); err != nil {
			return fmt.Errorf("failed to update active keys list for group %d: %w", groupID, err)
		}
		totalActiveKeys += len(activeKeyIDs)
	}

	duration := time.Since(startTime)
	logrus.Infof("Loaded %d keys (%d active) to store for group %d in %v", totalProcessed, totalActiveKeys, groupID, duration)
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
