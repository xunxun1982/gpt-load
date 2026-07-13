package services

import (
	"encoding/json"
	"fmt"
	"gpt-load/internal/channel"
	"gpt-load/internal/encryption"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/tokenusage"
	"gpt-load/internal/utils"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	// maxRequestKeys defines the maximum number of keys that can be processed in a single synchronous request.
	// Uses BulkSyncThreshold from thresholds.go for consistency with other batch operations.
	maxRequestKeys = BulkSyncThreshold
)

// AddKeysResult holds the result of adding multiple keys.
type AddKeysResult struct {
	AddedCount   int   `json:"added_count"`
	IgnoredCount int   `json:"ignored_count"`
	TotalInGroup int64 `json:"total_in_group"`
}

// DeleteKeysResult holds the result of deleting multiple keys.
type DeleteKeysResult struct {
	DeletedCount int   `json:"deleted_count"`
	IgnoredCount int   `json:"ignored_count"`
	TotalInGroup int64 `json:"total_in_group"`
}

// RestoreKeysResult holds the result of restoring multiple keys.
type RestoreKeysResult struct {
	RestoredCount int   `json:"restored_count"`
	IgnoredCount  int   `json:"ignored_count"`
	TotalInGroup  int64 `json:"total_in_group"`
}

// KeyService provides services related to API keys.
type KeyService struct {
	DB                        *gorm.DB
	readDB                    *gorm.DB
	KeyProvider               *keypool.KeyProvider
	KeyValidator              *keypool.KeyValidator
	EncryptionSvc             encryption.Service
	RequestLogService         *RequestLogService
	CacheInvalidationCallback func(groupID uint) // Optional callback for cache invalidation

	// Lightweight last-page cache for listing keys under load
	pageCache    map[string]keyPageCacheEntry
	pageCacheMu  sync.RWMutex
	pageCacheTTL time.Duration
}

type keyPageCacheEntry struct {
	Items     []models.APIKey
	ExpiresAt time.Time
}

// insertChunkSize returns an insert/list chunk size tuned by database dialect
func (s *KeyService) insertChunkSize() int {
	switch s.DB.Dialector.Name() {
	case "sqlite":
		return 100 // Reduced from 200 for smoother operation
	case "mysql", "postgres":
		return 300 // Reduced from 500 for better concurrency
	default:
		return 200 // Reduced from 300
	}
}

// NewKeyService creates a new KeyService.
func NewKeyService(db *gorm.DB, readDB ReadOnlyDB, keyProvider *keypool.KeyProvider, keyValidator *keypool.KeyValidator, encryptionSvc encryption.Service, requestLogService *RequestLogService) *KeyService {
	rdb := readDB.DB
	if rdb == nil {
		rdb = db
	}
	return &KeyService{
		DB:                db,
		readDB:            rdb,
		KeyProvider:       keyProvider,
		KeyValidator:      keyValidator,
		EncryptionSvc:     encryptionSvc,
		RequestLogService: requestLogService,
		pageCache:         make(map[string]keyPageCacheEntry),
		pageCacheTTL:      2 * time.Second,
	}
}

// AddMultipleKeys handles the business logic of creating new keys from a text block.
// deprecated: use KeyImportService for large imports
func (s *KeyService) AddMultipleKeys(groupID uint, keysText string) (*AddKeysResult, error) {
	keys := s.ParseKeysFromText(keysText)
	if len(keys) > maxRequestKeys {
		return nil, fmt.Errorf("batch size exceeds the limit of %d keys, got %d", maxRequestKeys, len(keys))
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid keys found in the input text")
	}

	addedCount, ignoredCount, err := s.processAndCreateKeys(groupID, keys, nil)
	if err != nil {
		return nil, err
	}

	totalInGroup, err := s.getTotalKeysInGroup(groupID)
	if err != nil {
		return nil, err
	}

	return &AddKeysResult{
		AddedCount:   addedCount,
		IgnoredCount: ignoredCount,
		TotalInGroup: totalInGroup,
	}, nil
}

// processAndCreateKeys is the lowest-level reusable function for adding keys.
func (s *KeyService) processAndCreateKeys(
	groupID uint,
	keys []string,
	progressCallback func(processed int),
) (addedCount int, ignoredCount int, err error) {
	// 1. Get existing key hashes in the group for deduplication (optimized)
	// Calculate hashes for new keys first to avoid loading all existing keys
	keyToHashMap := make(map[string]string, len(keys))
	chunkHashes := make([]string, 0, len(keys))
	for _, k := range keys {
		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			h := s.EncryptionSvc.Hash(trimmed)
			keyToHashMap[trimmed] = h
			chunkHashes = append(chunkHashes, h)
		}
	}

	var existingInBatch []string
	if len(chunkHashes) > 0 {
		if err := utils.ProcessInChunks(chunkHashes, s.insertChunkSize(), func(chunk []string) error {
			var batch []string
			if err := s.DB.Model(&models.APIKey{}).
				Where("group_id = ?", groupID).
				Where("key_hash IN ?", chunk).
				Pluck("key_hash", &batch).Error; err != nil {
				return err
			}
			existingInBatch = append(existingInBatch, batch...)
			return nil
		}); err != nil {
			return 0, 0, err
		}
	}

	existingHashMap := make(map[string]bool, len(existingInBatch))
	for _, h := range existingInBatch {
		existingHashMap[h] = true
	}

	// 2. Prepare new keys for creation
	newKeysToCreate := make([]models.APIKey, 0, len(keys))
	uniqueNewKeys := make(map[string]bool, len(keys))

	for _, keyVal := range keys {
		trimmedKey := strings.TrimSpace(keyVal)
		if trimmedKey == "" || uniqueNewKeys[trimmedKey] || !s.isValidKeyFormat(trimmedKey) {
			continue
		}

		// Generate hash for deduplication check
		keyHash, ok := keyToHashMap[trimmedKey]
		if !ok {
			keyHash = s.EncryptionSvc.Hash(trimmedKey)
		}

		if existingHashMap[keyHash] {
			continue
		}

		encryptedKey, err := s.EncryptionSvc.Encrypt(trimmedKey)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"group_id": groupID,
				"key_hash": keyHash,
			}).Error("Failed to encrypt key, skipping")
			continue
		}

		uniqueNewKeys[trimmedKey] = true
		newKeysToCreate = append(newKeysToCreate, models.APIKey{
			GroupID:  groupID,
			KeyValue: encryptedKey,
			KeyHash:  keyHash,
			Status:   models.KeyStatusActive,
		})
	}

	if len(newKeysToCreate) == 0 {
		return 0, len(keys), nil
	}

	// 3. Use KeyProvider to add keys in chunks (dialect-aware chunk size)
	err = utils.ProcessInChunks(newKeysToCreate, s.insertChunkSize(), func(chunk []models.APIKey) error {
		if err := s.KeyProvider.AddKeys(groupID, chunk); err != nil {
			return err
		}
		addedCount += len(chunk)

		if progressCallback != nil {
			progressCallback(addedCount)
		}
		return nil
	})
	if err != nil {
		return addedCount, len(keys) - addedCount, err
	}

	// Invalidate cache after adding keys
	if s.CacheInvalidationCallback != nil && addedCount > 0 {
		s.CacheInvalidationCallback(groupID)
	}

	return addedCount, len(keys) - addedCount, nil
}

// ParseKeysFromText parses a string of keys from various formats into a string slice.
// This function is exported to be shared with the handler layer.
func (s *KeyService) ParseKeysFromText(text string) []string {
	var keys []string

	// First, try to parse as a JSON array of strings
	if json.Unmarshal([]byte(text), &keys) == nil && len(keys) > 0 {
		return s.filterValidKeys(keys)
	}

	// Generic parsing: split text by delimiters without using complex regular expressions
	splitKeys := utils.DelimitersPattern.Split(strings.TrimSpace(text), -1)

	for _, key := range splitKeys {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}

	return s.filterValidKeys(keys)
}

// filterValidKeys validates and filters potential API keys
func (s *KeyService) filterValidKeys(keys []string) []string {
	validKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if s.isValidKeyFormat(key) {
			validKeys = append(validKeys, key)
		}
	}
	return validKeys
}

// isValidKeyFormat performs basic validation on key format.
// NOTE: We intentionally accept any non-empty key (after trimming) to support a wide
// range of upstream key formats. Call sites already trim keys for deduplication, but
// we still trim defensively here instead of inlining this logic to keep a single
// validation point and avoid subtle behavior changes if new callers are added.
func (s *KeyService) isValidKeyFormat(key string) bool {
	return strings.TrimSpace(key) != ""
}

// RestoreMultipleKeys handles the business logic of restoring keys from a text block.
func (s *KeyService) RestoreMultipleKeys(groupID uint, keysText string) (*RestoreKeysResult, error) {
	keysToRestore := s.ParseKeysFromText(keysText)
	if len(keysToRestore) > maxRequestKeys {
		return nil, fmt.Errorf("batch size exceeds the limit of %d keys, got %d", maxRequestKeys, len(keysToRestore))
	}
	if len(keysToRestore) == 0 {
		return nil, fmt.Errorf("no valid keys found in the input text")
	}

	var totalRestoredCount int64
	err := utils.ProcessInChunks(keysToRestore, s.insertChunkSize(), func(chunk []string) error {
		restoredCount, err := s.KeyProvider.RestoreMultipleKeys(groupID, chunk)
		if err != nil {
			return err
		}
		totalRestoredCount += restoredCount
		return nil
	})
	if err != nil {
		return nil, err
	}

	ignoredCount := len(keysToRestore) - int(totalRestoredCount)

	totalInGroup, err := s.getTotalKeysInGroup(groupID)
	if err != nil {
		return nil, err
	}

	return &RestoreKeysResult{
		RestoredCount: int(totalRestoredCount),
		IgnoredCount:  ignoredCount,
		TotalInGroup:  totalInGroup,
	}, nil
}

// RestoreAllInvalidKeys sets the status of all 'inactive' keys in a group to 'active'.
func (s *KeyService) RestoreAllInvalidKeys(groupID uint) (int64, error) {
	return s.KeyProvider.RestoreKeys(groupID)
}

// ClearAllInvalidKeys deletes all 'inactive' keys from a group.
func (s *KeyService) ClearAllInvalidKeys(groupID uint) (int64, error) {
	return s.KeyProvider.RemoveInvalidKeys(groupID)
}

// ResetGroupActiveKeysFailureCount resets failure_count to 0 for all active keys in a specific group.
// This is useful when importing a group, treating it as a fresh import.
func (s *KeyService) ResetGroupActiveKeysFailureCount(groupID uint) (int64, error) {
	return s.KeyProvider.ResetGroupActiveKeysFailureCount(groupID)
}

// ResetAllActiveKeysFailureCount resets failure_count to 0 for all active keys across all groups.
// This is useful when system configuration changes (e.g., blacklist_threshold) and we want to
// reset the failure history to avoid immediate blacklisting with new thresholds.
func (s *KeyService) ResetAllActiveKeysFailureCount() (int64, error) {
	return s.KeyProvider.ResetAllActiveKeysFailureCount()
}

// DeleteMultipleKeys handles the business logic of deleting keys from a text block.
func (s *KeyService) DeleteMultipleKeys(groupID uint, keysText string) (*DeleteKeysResult, error) {
	keysToDelete := s.ParseKeysFromText(keysText)
	if len(keysToDelete) > maxRequestKeys {
		return nil, fmt.Errorf("batch size exceeds the limit of %d keys, got %d", maxRequestKeys, len(keysToDelete))
	}
	if len(keysToDelete) == 0 {
		return nil, fmt.Errorf("no valid keys found in the input text")
	}

	var totalDeletedCount int64
	err := utils.ProcessInChunks(keysToDelete, s.insertChunkSize(), func(chunk []string) error {
		deletedCount, err := s.KeyProvider.RemoveKeys(groupID, chunk)
		if err != nil {
			return err
		}
		totalDeletedCount += deletedCount
		return nil
	})
	if err != nil {
		return nil, err
	}

	ignoredCount := len(keysToDelete) - int(totalDeletedCount)

	totalInGroup, err := s.getTotalKeysInGroup(groupID)
	if err != nil {
		return nil, err
	}

	return &DeleteKeysResult{
		DeletedCount: int(totalDeletedCount),
		IgnoredCount: ignoredCount,
		TotalInGroup: totalInGroup,
	}, nil
}

// ListKeysInGroupQuery builds a query to list all keys within a specific group, filtered by status.
func (s *KeyService) ListKeysInGroupQuery(groupID uint, statusFilter string, searchHash string) *gorm.DB {
	query := s.readDB.Model(&models.APIKey{}).Where("group_id = ?", groupID)

	if statusFilter != "" {
		query = query.Where("status = ?", statusFilter)
	}

	if searchHash != "" {
		query = query.Where("key_hash = ?", searchHash)
	}

	if s.readDB.Dialector.Name() == "postgres" {
		query = query.Order("last_used_at DESC NULLS LAST, updated_at DESC, id DESC")
	} else {
		query = query.Order("last_used_at IS NULL ASC, last_used_at DESC, updated_at DESC, id DESC")
	}

	return query
}

// BuildPageCacheKey composes a cache key for a keys list request
func (s *KeyService) BuildPageCacheKey(groupID uint, statusFilter, searchHash string, page, pageSize int) string {
	return fmt.Sprintf("g:%d|st:%s|sh:%s|p:%d|ps:%d", groupID, statusFilter, searchHash, page, pageSize)
}

// GetCachedPage returns a cached page if available and not expired
func (s *KeyService) GetCachedPage(cacheKey string) ([]models.APIKey, bool) {
	now := time.Now()

	s.pageCacheMu.RLock()
	entry, ok := s.pageCache[cacheKey]
	expired := ok && now.After(entry.ExpiresAt)
	s.pageCacheMu.RUnlock()

	if !ok {
		return nil, false
	}
	if expired {
		// Remove expired entry to prevent memory leak
		s.pageCacheMu.Lock()
		// Re-check under write lock to avoid deleting newly refreshed entries
		entry, ok = s.pageCache[cacheKey]
		if ok && time.Now().After(entry.ExpiresAt) {
			delete(s.pageCache, cacheKey)
			s.pageCacheMu.Unlock()
			return nil, false
		}
		s.pageCacheMu.Unlock()

		// If entry was refreshed by another goroutine, return it
		if !ok {
			return nil, false
		}
	}
	return entry.Items, true
}

// SetCachedPage caches a page of keys for a short TTL
func (s *KeyService) SetCachedPage(cacheKey string, items []models.APIKey) {
	s.pageCacheMu.Lock()
	s.pageCache[cacheKey] = keyPageCacheEntry{Items: items, ExpiresAt: time.Now().Add(s.pageCacheTTL)}
	s.pageCacheMu.Unlock()
}

// TestMultipleKeys handles a one-off validation test for multiple keys.
func (s *KeyService) TestMultipleKeys(group *models.Group, keysText string) ([]keypool.KeyTestResult, error) {
	keysToTest := s.ParseKeysFromText(keysText)
	if len(keysToTest) > maxRequestKeys {
		return nil, fmt.Errorf("batch size exceeds the limit of %d keys, got %d", maxRequestKeys, len(keysToTest))
	}
	if len(keysToTest) == 0 {
		return nil, fmt.Errorf("no valid keys found in the input text")
	}

	start := time.Now()
	isSingleManualTest := len(keysToTest) == 1
	allResults := make([]keypool.KeyTestResult, 0, len(keysToTest))
	if isSingleManualTest {
		result, err := s.KeyValidator.TestSingleKeyWithTrace(group, keysToTest[0])
		if err != nil {
			s.recordKeyValidationLog(group, keysToTest[0], false, http.StatusInternalServerError, time.Since(start), err.Error(), nil)
			return nil, err
		}
		allResults = append(allResults, result)
	} else {
		err := utils.ProcessInChunks(keysToTest, s.insertChunkSize(), func(chunk []string) error {
			results, err := s.KeyValidator.TestMultipleKeys(group, chunk)
			if err != nil {
				return err
			}
			allResults = append(allResults, results...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	if isSingleManualTest && len(allResults) == 1 {
		result := allResults[0]
		s.recordKeyValidationLog(group, keysToTest[0], result.IsValid, validationLogStatusCode(result), time.Since(start), result.Error, result.ValidationTrace)
	}

	return allResults, nil
}

func validationLogStatusCode(result keypool.KeyTestResult) int {
	if result.IsValid {
		return http.StatusOK
	}
	if strings.Contains(result.Error, "does not exist") {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func (s *KeyService) recordKeyValidationLog(group *models.Group, keyValue string, isSuccess bool, statusCode int, duration time.Duration, errorMessage string, trace *channel.ValidationTrace) {
	if s.RequestLogService == nil || group == nil {
		return
	}

	keyHash := s.EncryptionSvc.Hash(keyValue)
	encryptedKeyValue, err := s.EncryptionSvc.Encrypt(keyValue)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"group_id": group.ID,
			"key_hash": keyHash,
		}).Error("Failed to encrypt key value for validation logging")
		encryptedKeyValue = "failed-to-encryption"
	}

	safeErrorMessage := utils.TruncateString(utils.SanitizeErrorBody(errorMessage), 1000)
	logEntry := &models.RequestLog{
		GroupID:      group.ID,
		GroupName:    group.Name,
		KeyValue:     encryptedKeyValue,
		KeyHash:      keyHash,
		IsSuccess:    isSuccess,
		StatusCode:   statusCode,
		RequestPath:  "/keys/test-multiple",
		Duration:     duration.Milliseconds(),
		RequestType:  models.RequestTypeValidation,
		ErrorMessage: safeErrorMessage,
	}
	applyValidationTraceToLog(logEntry, trace)
	if err := s.RequestLogService.Record(logEntry); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"group_id": group.ID,
			"key_hash": keyHash,
		}).Warn("Failed to record key validation log")
	}
}

func applyValidationTraceToLog(logEntry *models.RequestLog, trace *channel.ValidationTrace) {
	if logEntry == nil || trace == nil {
		return
	}
	if trace.RequestPath != "" {
		logEntry.RequestPath = trace.RequestPath
	}
	logEntry.UpstreamAddr = trace.UpstreamAddr
	logEntry.UpstreamUserAgent = trace.UpstreamUserAgent
	logEntry.RequestBody = trace.RequestBody
	logEntry.ResponseBody = trace.ResponseBody

	// Error responses are kept for diagnosis but do not show token usage because
	// upstream billing semantics are ambiguous for rejected validation requests.
	if trace.ResponseError {
		return
	}
	if trace.HasReportedUsage {
		applyValidationTokenUsage(logEntry, trace.ReportedTokenUsage, models.TokenUsageSourceUpstream)
		return
	}
	if trace.EstimatedInputTokens > 0 {
		applyValidationTokenUsage(logEntry, tokenusage.Usage{
			InputTokens: trace.EstimatedInputTokens,
			TotalTokens: trace.EstimatedInputTokens,
		}, models.TokenUsageSourceEstimated)
	}
}

func applyValidationTokenUsage(logEntry *models.RequestLog, usage tokenusage.Usage, source string) {
	if logEntry == nil || usage.IsZero() {
		return
	}
	usage = usage.Normalize()
	logEntry.InputTokens = usage.InputTokens
	logEntry.OutputTokens = usage.OutputTokens
	logEntry.TotalTokens = usage.TotalTokens
	logEntry.CacheReadTokens = usage.CacheReadTokens
	logEntry.CacheWriteTokens = usage.CacheWriteTokens
	logEntry.ThinkingTokens = usage.ThinkingTokens
	logEntry.TokenUsageSource = source
}

// StreamKeysToWriter fetches keys from the database in batches and writes them to the provided writer.
func (s *KeyService) StreamKeysToWriter(groupID uint, statusFilter string, writer io.Writer) error {
	query := s.DB.Model(&models.APIKey{}).Where("group_id = ?", groupID).Select("id, key_value")

	switch statusFilter {
	case models.KeyStatusActive, models.KeyStatusInvalid:
		query = query.Where("status = ?", statusFilter)
	case "all":
	default:
		return fmt.Errorf("invalid status filter: %s", statusFilter)
	}

	var keys []models.APIKey
	err := query.FindInBatches(&keys, s.insertChunkSize(), func(tx *gorm.DB, batch int) error {
		for _, key := range keys {
			decryptedKey, err := s.EncryptionSvc.Decrypt(key.KeyValue)
			if err != nil {
				logrus.WithError(err).WithField("key_id", key.ID).Error("Failed to decrypt key for streaming, skipping")
				continue
			}
			if _, err := writer.Write([]byte(decryptedKey + "\n")); err != nil {
				return err
			}
		}
		return nil
	}).Error

	return err
}

// getTotalKeysInGroup returns the total number of keys in a group.
// This uses a simple indexed COUNT(*) on group_id and is safe for SQLite, MySQL, and PostgreSQL.
// Keeping this logic centralized ensures consistent behavior and makes it easier to tune in the future.
func (s *KeyService) getTotalKeysInGroup(groupID uint) (int64, error) {
	var totalInGroup int64
	if err := s.DB.Model(&models.APIKey{}).
		Where("group_id = ?", groupID).
		Count(&totalInGroup).Error; err != nil {
		return 0, err
	}
	return totalInGroup, nil
}
