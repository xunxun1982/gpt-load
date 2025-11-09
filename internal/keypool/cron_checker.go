package keypool

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// NewCronChecker is responsible for periodically validating invalid keys.
type CronChecker struct {
	DB              *gorm.DB
	SettingsManager *config.SystemSettingsManager
	Validator       *KeyValidator
	EncryptionSvc   encryption.Service
	Store           store.Store
	stopChan        chan struct{}
	wg              sync.WaitGroup
}

// NewCronChecker creates a new CronChecker.
func NewCronChecker(
	db *gorm.DB,
	settingsManager *config.SystemSettingsManager,
	validator *KeyValidator,
	encryptionSvc encryption.Service,
	store store.Store,
) *CronChecker {
return &CronChecker{
		DB:              db,
		SettingsManager: settingsManager,
		Validator:       validator,
		EncryptionSvc:   encryptionSvc,
		Store:           store,
		stopChan:        make(chan struct{}),
	}
}

// Start begins the cron job execution.
func (s *CronChecker) Start() {
	logrus.Debug("Starting CronChecker...")
	s.wg.Add(1)
	go s.runLoop()
}

// Stop stops the cron job, respecting the context for shutdown timeout.
func (s *CronChecker) Stop(ctx context.Context) {
	close(s.stopChan)

	// Wait for the goroutine to finish, or for the shutdown to time out.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("CronChecker stopped gracefully.")
	case <-ctx.Done():
		logrus.Warn("CronChecker stop timed out.")
	}
}

func (s *CronChecker) runLoop() {
	defer s.wg.Done()

	s.submitValidationJobs()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logrus.Debug("CronChecker: Running as Master, submitting validation jobs.")
			s.submitValidationJobs()
		case <-s.stopChan:
			return
		}
	}
}

// submitValidationJobs finds groups whose keys need validation and validates them concurrently.
func (s *CronChecker) submitValidationJobs() {
	// Skip when a heavy task (import/delete) is running to avoid DB contention
	if s.isBusy() {
		logrus.Debug("CronChecker: busy mode detected (import/delete running), skipping this cycle")
		return
	}

	var groups []models.Group
	// Short timeout and minimal column selection to avoid long blocking
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := s.DB.WithContext(ctx).
		Select("id, name, config, enabled, group_type, channel_type, last_validated_at").
		Where("group_type IS NULL OR group_type != ?", "aggregate").
		Find(&groups).Error; err != nil {
		logrus.Errorf("CronChecker: Failed to get groups (timeout/error): %v", err)
		return
	}

	validationStartTime := time.Now()
	var wg sync.WaitGroup
	// Use a thread-safe map to collect group IDs that need to be updated
	var groupsToUpdateMu sync.Mutex
	groupsToUpdate := make(map[uint]struct{})

	for i := range groups {
		group := &groups[i]

		// Skip disabled groups
		if !group.Enabled {
			logrus.WithField("group_name", group.Name).Debug("CronChecker: Skipping disabled group")
			continue
		}

		group.EffectiveConfig = s.SettingsManager.GetEffectiveConfig(group.Config)
		interval := time.Duration(group.EffectiveConfig.KeyValidationIntervalMinutes) * time.Minute

		if group.LastValidatedAt == nil || validationStartTime.Sub(*group.LastValidatedAt) > interval {
			wg.Add(1)
			g := group
			go func() {
				defer wg.Done()
				s.validateGroupKeys(g, &groupsToUpdateMu, groupsToUpdate)
			}()
		}
	}

	wg.Wait()

	// Batch update all groups' last_validated_at in a single transaction
	if len(groupsToUpdate) > 0 {
		s.batchUpdateLastValidatedAt(groupsToUpdate)
	}
}

// validateGroupKeys validates all invalid keys for a single group concurrently.
// It adds the group ID to groupsToUpdate map after validation completes.
// Uses streaming batch processing for large result sets to improve performance and reduce memory usage.
func (s *CronChecker) validateGroupKeys(group *models.Group, groupsToUpdateMu *sync.Mutex, groupsToUpdate map[uint]struct{}) {
	groupProcessStart := time.Now()

	// First, check if there are any invalid keys (quick count query using index)
var invalidKeyCount int64
	cctx, ccancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer ccancel()
	if err := s.DB.WithContext(cctx).Model(&models.APIKey{}).
		Where("group_id = ? AND status = ?", group.ID, models.KeyStatusInvalid).
		Count(&invalidKeyCount).Error; err != nil {
		logrus.Errorf("CronChecker: Failed to count invalid keys for group %s: %v", group.Name, err)
		return
	}

	if invalidKeyCount == 0 {
		// Mark group for batch update
		groupsToUpdateMu.Lock()
		groupsToUpdate[group.ID] = struct{}{}
		groupsToUpdateMu.Unlock()
		logrus.Infof("CronChecker: Group '%s' has no invalid keys to check.", group.Name)
		return
	}

	// Use streaming batch processing to avoid loading all keys into memory at once
	// This significantly improves performance for large result sets by:
	// 1. Reducing memory usage
	// 2. Starting validation immediately (no need to wait for all data)
	// 3. Using smaller, faster queries instead of one large query
	batchSize := 1000
	var becameValidCount int32
	var totalChecked int32
	var keyWg sync.WaitGroup

	// Use buffered channel to allow concurrent processing while streaming data
	// Buffer size should be larger than batch size to avoid blocking
	jobs := make(chan *models.APIKey, batchSize*2)

	concurrency := group.EffectiveConfig.KeyValidationConcurrency
	for range concurrency {
		keyWg.Add(1)
		go func() {
			defer keyWg.Done()
			for {
				select {
				case key, ok := <-jobs:
					if !ok {
						return
					}

					atomic.AddInt32(&totalChecked, 1)

					// Decrypt the key before validation
					decryptedKey, err := s.EncryptionSvc.Decrypt(key.KeyValue)
					if err != nil {
						logrus.WithError(err).WithField("key_id", key.ID).Error("CronChecker: Failed to decrypt key for validation, skipping")
						continue
					}

					// Create a copy with decrypted value for validation
					keyForValidation := *key
					keyForValidation.KeyValue = decryptedKey

					isValid, _ := s.Validator.ValidateSingleKey(&keyForValidation, group)
					if isValid {
						atomic.AddInt32(&becameValidCount, 1)
					}
				case <-s.stopChan:
					return
				}
			}
		}()
	}

	// Stream data from database in batches and send to workers immediately
	// This allows validation to start processing while we're still querying
	var queryWg sync.WaitGroup
	queryWg.Add(1)
	go func() {
		defer queryWg.Done()
		defer close(jobs)

		var lastID uint = 0
		for {
			var batchKeys []models.APIKey
			// Use cursor-based pagination instead of OFFSET for better performance
			// OFFSET becomes very slow with large datasets (30+ seconds for OFFSET 3000)
			// Cursor-based pagination using "WHERE id > lastID" is much faster
			query := s.DB.Select("id, group_id, key_value, status").
				Where("group_id = ? AND status = ?", group.ID, models.KeyStatusInvalid).
				Order("id ASC").
				Limit(batchSize)

			// Add cursor condition for subsequent batches
			if lastID > 0 {
				query = query.Where("id > ?", lastID)
			}

qctx, qcancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	err := query.WithContext(qctx).Find(&batchKeys).Error
	qcancel()

			if err != nil {
				logrus.Errorf("CronChecker: Failed to get invalid keys for group %s: %v", group.Name, err)
				return
			}

			if len(batchKeys) == 0 {
				break
			}

			// Send keys to workers immediately
			for i := range batchKeys {
				select {
				case jobs <- &batchKeys[i]:
				case <-s.stopChan:
					return
				}
			}

			// Update cursor to last processed ID
			lastID = batchKeys[len(batchKeys)-1].ID

			// If we got fewer than batchSize, we've reached the end
			if len(batchKeys) < batchSize {
				break
			}
		}
	}()

	// Wait for all queries to complete and all validations to finish
	queryWg.Wait()
	keyWg.Wait()

	// Mark group for batch update
	groupsToUpdateMu.Lock()
	groupsToUpdate[group.ID] = struct{}{}
	groupsToUpdateMu.Unlock()

	duration := time.Since(groupProcessStart)
	logrus.Infof(
		"CronChecker: Group '%s' validation finished. Total checked: %d, became valid: %d. Duration: %s.",
		group.Name,
		totalChecked,
		becameValidCount,
		duration.String(),
	)
}

// batchUpdateLastValidatedAt updates last_validated_at for multiple groups in a single batch operation.
// It uses retry mechanism to handle database lock errors across different database types.
// Compatible with SQLite, MySQL, and PostgreSQL.
func (s *CronChecker) batchUpdateLastValidatedAt(groupsToUpdate map[uint]struct{}) {
	if len(groupsToUpdate) == 0 {
		return
	}

	// Convert map to slice for SQL IN clause
	// Note: GORM handles database-specific SQL syntax automatically
	groupIDs := make([]uint, 0, len(groupsToUpdate))
	for id := range groupsToUpdate {
		groupIDs = append(groupIDs, id)
	}

	now := time.Now()
	const maxRetries = 5
	const baseDelay = 50 * time.Millisecond
	const maxJitter = 200 * time.Millisecond
	// SQLite has a default limit of ~999 bound parameters per statement
	// UPDATE statement binds: 1 (last_validated_at) + N (IDs in IN clause)
	// So chunk size must be 998 to stay within the 999 parameter limit
	const sqliteMaxInParams = 999
	const sqliteChunkSize = sqliteMaxInParams - 1

	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Handle large batches by splitting into chunks for SQLite compatibility
		if len(groupIDs) > sqliteChunkSize {
			// Process in chunks to avoid SQLite parameter limit
			err = nil
			for i := 0; i < len(groupIDs); i += sqliteChunkSize {
				end := i + sqliteChunkSize
				if end > len(groupIDs) {
					end = len(groupIDs)
				}
				chunk := groupIDs[i:end]
				if chunkErr := s.DB.Model(&models.Group{}).
					Where("id IN ?", chunk).
					UpdateColumns(map[string]any{"last_validated_at": now}).Error; chunkErr != nil {
					// If chunk update fails, set error and break to retry entire batch
					err = chunkErr
					break
				}
			}
			if err == nil {
				logrus.Debugf("CronChecker: Successfully batch updated last_validated_at for %d groups (in chunks)", len(groupIDs))
				return
			}
		} else {
			// Use UpdateColumns with Where IN for batch update
			// GORM automatically handles database-specific SQL syntax differences
			// This avoids GORM's updated_at auto-update and hooks while supporting batch operations
			err = s.DB.Model(&models.Group{}).
				Where("id IN ?", groupIDs).
				UpdateColumns(map[string]any{"last_validated_at": now}).Error
			if err == nil {
				logrus.Debugf("CronChecker: Successfully batch updated last_validated_at for %d groups", len(groupIDs))
				return
			}
		}

		// Check for database lock errors across different database types
		// SQLite: "database is locked", "SQLITE_BUSY"
		// MySQL: "Lock wait timeout exceeded", "Deadlock found"
		// PostgreSQL: "deadlock detected", "could not obtain lock"
		errMsg := strings.ToLower(err.Error())
		isLockError := strings.Contains(errMsg, "database is locked") ||
			strings.Contains(errMsg, "sqlite_busy") ||
			strings.Contains(errMsg, "lock wait timeout") ||
			strings.Contains(errMsg, "deadlock") ||
			strings.Contains(errMsg, "could not obtain lock")

		if isLockError {
			// Use thread-safe global rand.Intn (concurrency-safe; Go 1.20+ also auto-seeds)
			// This provides good randomness while avoiding data race issues
			jitter := time.Duration(rand.Intn(int(maxJitter)))
			// Use exponential backoff for better performance under lock contention
			// Delays: 50ms, 100ms, 200ms, 400ms, 800ms (plus jitter)
			exponentialDelay := baseDelay * (1 << attempt)
			totalDelay := exponentialDelay + jitter
			logrus.Debugf("CronChecker: Database lock detected, retrying batch update in %v... (attempt %d/%d)", totalDelay, attempt+1, maxRetries)
			time.Sleep(totalDelay)
			continue
		}

		// For other errors, break immediately
		break
	}

	// If all retries failed, log error but don't fail the entire operation
	logrus.Errorf("CronChecker: Failed to batch update last_validated_at for %d groups after %d attempts: %v", len(groupIDs), maxRetries, err)
}

// isBusy returns true if a global import/delete task is running (read directly from store)
func (s *CronChecker) isBusy() bool {
	if s.Store == nil {
		return false
	}
	b, err := s.Store.Get("global_task")
	if err != nil {
		return false
	}
	var st struct {
		TaskType  string `json:"task_type"`
		IsRunning bool   `json:"is_running"`
	}
	if json.Unmarshal(b, &st) != nil {
		return false
	}
	if !st.IsRunning {
		return false
	}
	return st.TaskType == "KEY_IMPORT" || st.TaskType == "KEY_DELETE"
}
