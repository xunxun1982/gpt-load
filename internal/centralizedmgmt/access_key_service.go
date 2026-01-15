package centralizedmgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/utils"

	"gorm.io/gorm"
)

const (
	// Default TTL for access key cache
	defaultAccessKeyCacheTTL = 30 * time.Second

	// Hub access key prefix
	hubAccessKeyPrefix = "hk-"

	// Default key length (excluding prefix)
	defaultKeyLength = 48
)

// HubAccessKeyService manages Hub access keys with caching support.
// Key values are encrypted before storage and cached for fast validation.
type HubAccessKeyService struct {
	db            *gorm.DB
	encryptionSvc encryption.Service

	// Key validation cache: maps encrypted key hash -> HubAccessKey
	keyCache    map[string]*accessKeyCacheEntry
	keyCacheMu  sync.RWMutex
	keyCacheTTL time.Duration
}

// accessKeyCacheEntry holds cached access key data with expiration
type accessKeyCacheEntry struct {
	Key       *HubAccessKey
	ExpiresAt time.Time
}

// NewHubAccessKeyService creates a new HubAccessKeyService instance
func NewHubAccessKeyService(db *gorm.DB, encryptionSvc encryption.Service) *HubAccessKeyService {
	return &HubAccessKeyService{
		db:            db,
		encryptionSvc: encryptionSvc,
		keyCache:      make(map[string]*accessKeyCacheEntry),
		keyCacheTTL:   defaultAccessKeyCacheTTL,
	}
}

// CreateAccessKey creates a new Hub access key.
// If KeyValue is empty, a secure random key is generated.
// The key is encrypted before storage.
func (s *HubAccessKeyService) CreateAccessKey(ctx context.Context, params CreateAccessKeyParams) (*HubAccessKeyDTO, string, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, "", app_errors.NewValidationError("name is required")
	}

	// Check for duplicate name
	var count int64
	if err := s.db.WithContext(ctx).Model(&HubAccessKey{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, "", app_errors.ParseDBError(err)
	}
	if count > 0 {
		return nil, "", app_errors.NewValidationError("access key name already exists")
	}

	// Generate or use provided key value
	keyValue := strings.TrimSpace(params.KeyValue)
	if keyValue == "" {
		keyValue = hubAccessKeyPrefix + utils.GenerateSecureRandomString(defaultKeyLength)
	}

	// Generate hash for lookup (deterministic)
	keyHash := s.encryptionSvc.Hash(keyValue)

	// Check for duplicate key value (using hash)
	if err := s.db.WithContext(ctx).Model(&HubAccessKey{}).Where("key_hash = ?", keyHash).Count(&count).Error; err != nil {
		return nil, "", app_errors.ParseDBError(err)
	}
	if count > 0 {
		return nil, "", app_errors.NewValidationError("key value already exists")
	}

	// Encrypt the key value for storage
	encryptedKey, err := s.encryptionSvc.Encrypt(keyValue)
	if err != nil {
		return nil, "", fmt.Errorf("failed to encrypt key: %w", err)
	}

	// Serialize allowed models to JSON
	allowedModelsJSON, err := json.Marshal(params.AllowedModels)
	if err != nil {
		return nil, "", fmt.Errorf("failed to serialize allowed models: %w", err)
	}

	key := &HubAccessKey{
		Name:          name,
		KeyHash:       keyHash,
		KeyValue:      encryptedKey,
		AllowedModels: allowedModelsJSON,
		Enabled:       params.Enabled,
	}

	// Create the key using Exec to bypass GORM's default value handling
	// This ensures Enabled=false is properly stored
	result := s.db.WithContext(ctx).Exec(
		"INSERT INTO hub_access_keys (name, key_hash, key_value, allowed_models, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		key.Name, key.KeyHash, key.KeyValue, key.AllowedModels, key.Enabled, time.Now(), time.Now(),
	)
	if result.Error != nil {
		return nil, "", app_errors.ParseDBError(result.Error)
	}

	// Fetch the created key to get the ID and timestamps
	if err := s.db.WithContext(ctx).Where("key_hash = ?", keyHash).First(key).Error; err != nil {
		return nil, "", app_errors.ParseDBError(err)
	}

	// Invalidate cache after creation
	s.invalidateKeyCache(keyHash)

	dto := s.toDTO(key)
	// Return the original (unencrypted) key value only on creation
	return dto, keyValue, nil
}

// ValidateAccessKey validates an access key and returns the key record if valid.
// Uses caching to avoid repeated database lookups.
func (s *HubAccessKeyService) ValidateAccessKey(ctx context.Context, keyValue string) (*HubAccessKey, error) {
	if keyValue == "" {
		return nil, app_errors.NewValidationError("access key is required")
	}

	// Generate hash for lookup (deterministic)
	keyHash := s.encryptionSvc.Hash(keyValue)

	// Check cache first (fast path)
	s.keyCacheMu.RLock()
	if entry, ok := s.keyCache[keyHash]; ok && time.Now().Before(entry.ExpiresAt) {
		key := entry.Key
		s.keyCacheMu.RUnlock()
		if key == nil {
			return nil, app_errors.NewAuthenticationError("invalid access key")
		}
		if !key.Enabled {
			return nil, app_errors.NewAuthenticationError("access key is disabled")
		}
		return key, nil
	}
	s.keyCacheMu.RUnlock()

	// Cache miss - query database using hash
	var key HubAccessKey
	err := s.db.WithContext(ctx).Where("key_hash = ?", keyHash).First(&key).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Cache negative result to prevent repeated lookups for invalid keys
			s.cacheKey(keyHash, nil)
			return nil, app_errors.NewAuthenticationError("invalid access key")
		}
		return nil, app_errors.ParseDBError(err)
	}

	// Cache the result
	s.cacheKey(keyHash, &key)

	if !key.Enabled {
		return nil, app_errors.NewAuthenticationError("access key is disabled")
	}

	return &key, nil
}

// IsModelAllowed checks if a model is allowed by the access key.
// Empty AllowedModels means all models are allowed.
func (s *HubAccessKeyService) IsModelAllowed(key *HubAccessKey, modelName string) bool {
	if key == nil {
		return false
	}

	var allowedModels []string
	if err := json.Unmarshal(key.AllowedModels, &allowedModels); err != nil {
		return false
	}

	// Empty list means all models are allowed
	if len(allowedModels) == 0 {
		return true
	}

	// Check if model is in allowed list
	for _, m := range allowedModels {
		if m == modelName {
			return true
		}
	}

	return false
}

// ListAccessKeys returns all access keys with masked key values.
func (s *HubAccessKeyService) ListAccessKeys(ctx context.Context) ([]HubAccessKeyDTO, error) {
	var keys []HubAccessKey
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&keys).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	dtos := make([]HubAccessKeyDTO, 0, len(keys))
	for i := range keys {
		dtos = append(dtos, *s.toDTO(&keys[i]))
	}

	return dtos, nil
}

// GetAccessKey returns a single access key by ID with masked key value.
func (s *HubAccessKeyService) GetAccessKey(ctx context.Context, id uint) (*HubAccessKeyDTO, error) {
	var key HubAccessKey
	if err := s.db.WithContext(ctx).First(&key, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.NewNotFoundError("access key not found")
		}
		return nil, app_errors.ParseDBError(err)
	}

	return s.toDTO(&key), nil
}

// UpdateAccessKey updates an existing access key.
// Key value cannot be changed after creation.
func (s *HubAccessKeyService) UpdateAccessKey(ctx context.Context, id uint, params UpdateAccessKeyParams) (*HubAccessKeyDTO, error) {
	var key HubAccessKey
	if err := s.db.WithContext(ctx).First(&key, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.NewNotFoundError("access key not found")
		}
		return nil, app_errors.ParseDBError(err)
	}

	// Store key hash for cache invalidation
	keyHash := key.KeyHash

	if params.Name != nil {
		name := strings.TrimSpace(*params.Name)
		if name == "" {
			return nil, app_errors.NewValidationError("name cannot be empty")
		}
		// Check for duplicate name (exclude current key)
		if name != key.Name {
			var count int64
			if err := s.db.WithContext(ctx).Model(&HubAccessKey{}).Where("name = ? AND id != ?", name, id).Count(&count).Error; err != nil {
				return nil, app_errors.ParseDBError(err)
			}
			if count > 0 {
				return nil, app_errors.NewValidationError("access key name already exists")
			}
		}
		key.Name = name
	}

	if params.AllowedModels != nil {
		allowedModelsJSON, err := json.Marshal(params.AllowedModels)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize allowed models: %w", err)
		}
		key.AllowedModels = allowedModelsJSON
	}

	if params.Enabled != nil {
		key.Enabled = *params.Enabled
	}

	if err := s.db.WithContext(ctx).Save(&key).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Invalidate cache after update
	s.invalidateKeyCache(keyHash)

	return s.toDTO(&key), nil
}

// DeleteAccessKey deletes an access key by ID.
func (s *HubAccessKeyService) DeleteAccessKey(ctx context.Context, id uint) error {
	var key HubAccessKey
	if err := s.db.WithContext(ctx).First(&key, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return app_errors.NewNotFoundError("access key not found")
		}
		return app_errors.ParseDBError(err)
	}

	// Store key hash for cache invalidation
	keyHash := key.KeyHash

	if err := s.db.WithContext(ctx).Delete(&key).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	// Invalidate cache after deletion
	s.invalidateKeyCache(keyHash)

	return nil
}

// InvalidateAllKeyCache clears the entire key cache.
// Useful when bulk operations occur.
func (s *HubAccessKeyService) InvalidateAllKeyCache() {
	s.keyCacheMu.Lock()
	s.keyCache = make(map[string]*accessKeyCacheEntry)
	s.keyCacheMu.Unlock()
}

// toDTO converts a HubAccessKey to HubAccessKeyDTO with masked key value.
func (s *HubAccessKeyService) toDTO(key *HubAccessKey) *HubAccessKeyDTO {
	if key == nil {
		return nil
	}

	// Decrypt key to get masked version
	decryptedKey, err := s.encryptionSvc.Decrypt(key.KeyValue)
	maskedKey := "***"
	if err == nil {
		maskedKey = MaskKeyValue(decryptedKey)
	}

	// Parse allowed models
	var allowedModels []string
	if err := json.Unmarshal(key.AllowedModels, &allowedModels); err != nil {
		allowedModels = []string{}
	}

	// Determine mode
	mode := "all"
	if len(allowedModels) > 0 {
		mode = "specific"
	}

	return &HubAccessKeyDTO{
		ID:                key.ID,
		Name:              key.Name,
		MaskedKey:         maskedKey,
		AllowedModels:     allowedModels,
		AllowedModelsMode: mode,
		Enabled:           key.Enabled,
		CreatedAt:         key.CreatedAt,
		UpdatedAt:         key.UpdatedAt,
	}
}

// cacheKey stores a key in the cache with TTL
func (s *HubAccessKeyService) cacheKey(encryptedKey string, key *HubAccessKey) {
	s.keyCacheMu.Lock()
	s.keyCache[encryptedKey] = &accessKeyCacheEntry{
		Key:       key,
		ExpiresAt: time.Now().Add(s.keyCacheTTL),
	}
	s.keyCacheMu.Unlock()
}

// invalidateKeyCache removes a specific key from the cache
func (s *HubAccessKeyService) invalidateKeyCache(encryptedKey string) {
	s.keyCacheMu.Lock()
	delete(s.keyCache, encryptedKey)
	s.keyCacheMu.Unlock()
}

// GetAllowedModels returns the list of allowed models for an access key.
// Returns nil if all models are allowed.
func (s *HubAccessKeyService) GetAllowedModels(key *HubAccessKey) []string {
	if key == nil {
		return nil
	}

	var allowedModels []string
	if err := json.Unmarshal(key.AllowedModels, &allowedModels); err != nil {
		return nil
	}

	// Empty list means all models are allowed
	if len(allowedModels) == 0 {
		return nil
	}

	return allowedModels
}

// ExportAccessKeys exports all Hub access keys for backup/transfer.
// Key values remain encrypted (same as database storage) for security.
func (s *HubAccessKeyService) ExportAccessKeys(ctx context.Context) ([]HubAccessKeyExportInfo, error) {
	var keys []HubAccessKey
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("failed to export access keys: %w", err)
	}

	if len(keys) == 0 {
		return nil, nil
	}

	exports := make([]HubAccessKeyExportInfo, 0, len(keys))
	for _, key := range keys {
		// Parse allowed models from JSON
		var allowedModels []string
		if err := json.Unmarshal(key.AllowedModels, &allowedModels); err != nil {
			allowedModels = []string{}
		}

		exports = append(exports, HubAccessKeyExportInfo{
			Name:          key.Name,
			KeyValue:      key.KeyValue, // Keep encrypted
			AllowedModels: allowedModels,
			Enabled:       key.Enabled,
		})
	}

	return exports, nil
}

// ImportAccessKeys imports Hub access keys from export data.
// Validates decryption before import and generates unique names for conflicts.
func (s *HubAccessKeyService) ImportAccessKeys(ctx context.Context, tx *gorm.DB, keys []HubAccessKeyExportInfo) (imported int, skipped int, err error) {
	if len(keys) == 0 {
		return 0, 0, nil
	}

	for _, keyInfo := range keys {
		name := strings.TrimSpace(keyInfo.Name)
		if name == "" {
			skipped++
			continue
		}

		// Validate encrypted key value can be decrypted
		if keyInfo.KeyValue == "" {
			skipped++
			continue
		}

		decryptedKey, err := s.encryptionSvc.Decrypt(keyInfo.KeyValue)
		if err != nil {
			// Skip keys with decryption errors (different ENCRYPTION_KEY)
			skipped++
			continue
		}

		// Generate hash for the decrypted key
		keyHash := s.encryptionSvc.Hash(decryptedKey)

		// Check if key value already exists (by hash)
		var existingCount int64
		if err := tx.WithContext(ctx).Model(&HubAccessKey{}).Where("key_hash = ?", keyHash).Count(&existingCount).Error; err != nil {
			skipped++
			continue
		}
		if existingCount > 0 {
			// Key value already exists, skip
			skipped++
			continue
		}

		// Generate unique name if conflict exists
		uniqueName, err := s.generateUniqueName(ctx, tx, name)
		if err != nil {
			skipped++
			continue
		}

		// Serialize allowed models to JSON
		allowedModelsJSON, err := json.Marshal(keyInfo.AllowedModels)
		if err != nil {
			allowedModelsJSON = []byte("[]")
		}

		// Create the key with the encrypted value from export
		// Use Exec to bypass GORM's default value handling for Enabled field
		now := time.Now()
		result := tx.WithContext(ctx).Exec(
			"INSERT INTO hub_access_keys (name, key_hash, key_value, allowed_models, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			uniqueName, keyHash, keyInfo.KeyValue, allowedModelsJSON, keyInfo.Enabled, now, now,
		)
		if result.Error != nil {
			skipped++
			continue
		}

		imported++
	}

	// Invalidate cache after import
	s.InvalidateAllKeyCache()

	return imported, skipped, nil
}

// generateUniqueName generates a unique access key name by appending a random suffix if needed.
func (s *HubAccessKeyService) generateUniqueName(ctx context.Context, tx *gorm.DB, baseName string) (string, error) {
	name := baseName
	maxAttempts := 10

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var count int64
		if err := tx.WithContext(ctx).Model(&HubAccessKey{}).Where("name = ?", name).Count(&count).Error; err != nil {
			return "", fmt.Errorf("failed to check name: %w", err)
		}

		if count == 0 {
			return name, nil
		}

		// Generate new name with random suffix
		if len(baseName)+4 > 100 {
			baseName = baseName[:96]
		}
		name = baseName + utils.GenerateRandomSuffix()
	}

	return "", fmt.Errorf("failed to generate unique name for %s after %d attempts", baseName, maxAttempts)
}
