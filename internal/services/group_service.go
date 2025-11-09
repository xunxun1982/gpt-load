package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// I18nError represents an error that carries translation metadata.
type I18nError struct {
	APIError  *app_errors.APIError
	MessageID string
	Template  map[string]any
}

// Error implements the error interface.
func (e *I18nError) Error() string {
	if e == nil || e.APIError == nil {
		return ""
	}
	return e.APIError.Error()
}

// NewI18nError is a helper to create an I18n-enabled error.
func NewI18nError(apiErr *app_errors.APIError, msgID string, template map[string]any) *I18nError {
	return &I18nError{
		APIError:  apiErr,
		MessageID: msgID,
		Template:  template,
	}
}

// groupKeyStatsCacheEntry represents a cached key statistics entry with expiration
type groupKeyStatsCacheEntry struct {
	Stats     KeyStats
	ExpiresAt time.Time
}

// groupListCacheEntry represents a cached groups list with expiration
type groupListCacheEntry struct {
	Groups    []models.Group
	ExpiresAt time.Time
}

// GroupService handles business logic for group operations.
type GroupService struct {
	db                    *gorm.DB
	settingsManager       *config.SystemSettingsManager
	groupManager          *GroupManager
	keyService            *KeyService
	keyImportSvc          *KeyImportService
	keyDeleteSvc          *KeyDeleteService
	bulkImportSvc         *BulkImportService
	encryptionSvc         encryption.Service
	aggregateGroupService *AggregateGroupService
	channelRegistry       []string
	keyStatsCache         map[uint]groupKeyStatsCacheEntry
	keyStatsCacheMu       sync.RWMutex
	keyStatsCacheTTL      time.Duration
	groupListCache        *groupListCacheEntry
	groupListCacheMu      sync.RWMutex
	groupListCacheTTL     time.Duration
}

// NewGroupService constructs a GroupService.
func NewGroupService(
	db *gorm.DB,
	settingsManager *config.SystemSettingsManager,
	groupManager *GroupManager,
	keyService *KeyService,
	keyImportSvc *KeyImportService,
	keyDeleteSvc *KeyDeleteService,
	bulkImportSvc *BulkImportService,
	encryptionSvc encryption.Service,
	aggregateGroupService *AggregateGroupService,
) *GroupService {
	svc := &GroupService{
		db:                    db,
		settingsManager:       settingsManager,
		groupManager:          groupManager,
		keyService:            keyService,
		keyImportSvc:          keyImportSvc,
		keyDeleteSvc:          keyDeleteSvc,
		bulkImportSvc:         bulkImportSvc,
		encryptionSvc:         encryptionSvc,
		aggregateGroupService: aggregateGroupService,
		keyStatsCache:         make(map[uint]groupKeyStatsCacheEntry),
		keyStatsCacheTTL:      30 * time.Second, // Reduced from 3 minutes to 30 seconds for fresher data
		groupListCacheTTL:     30 * time.Second, // Increased from 2 seconds to balance freshness and performance
		channelRegistry:       channel.GetChannels(),
	}
	if svc.keyService != nil {
		svc.keyService.CacheInvalidationCallback = svc.InvalidateKeyStatsCache
	}
	return svc
}

// GroupCreateParams captures all fields required to create a group.
type GroupCreateParams struct {
	Name               string
	DisplayName        string
	Description        string
	GroupType          string
	Upstreams          json.RawMessage
	ChannelType        string
	Sort               int
	TestModel          string
	ValidationEndpoint string
	ParamOverrides     map[string]any
	Config             map[string]any
	HeaderRules        []models.HeaderRule
	ModelMapping       string
	ProxyKeys          string
	SubGroups          []SubGroupInput
}

// GroupUpdateParams captures updatable fields for a group.
type GroupUpdateParams struct {
	Name               *string
	DisplayName        *string
	Description        *string
	GroupType          *string
	Upstreams          json.RawMessage
	HasUpstreams       bool
	ChannelType        *string
	Sort               *int
	TestModel          string
	HasTestModel       bool
	ValidationEndpoint *string
	ParamOverrides     map[string]any
	Config             map[string]any
	HeaderRules        *[]models.HeaderRule
	ModelMapping       *string
	ProxyKeys          *string
	SubGroups          *[]SubGroupInput
}

// KeyStats captures aggregated API key statistics for a group.
type KeyStats struct {
	TotalKeys   int64 `json:"total_keys"`
	ActiveKeys  int64 `json:"active_keys"`
	InvalidKeys int64 `json:"invalid_keys"`
}

// RequestStats captures request success and failure ratios over a time window.
type RequestStats struct {
	TotalRequests  int64   `json:"total_requests"`
	FailedRequests int64   `json:"failed_requests"`
	FailureRate    float64 `json:"failure_rate"`
}

// GroupStats aggregates all per-group metrics for dashboard usage.
type GroupStats struct {
	KeyStats    KeyStats     `json:"key_stats"`
	Stats24Hour RequestStats `json:"stats_24_hour"`
	Stats7Day   RequestStats `json:"stats_7_day"`
	Stats30Day  RequestStats `json:"stats_30_day"`
}

// ConfigOption describes a configurable override exposed to clients.
type ConfigOption struct {
	Key          string
	Name         string
	Description  string
	DefaultValue any
}

// CreateGroup validates and persists a new group.
func (s *GroupService) CreateGroup(ctx context.Context, params GroupCreateParams) (*models.Group, error) {
	name := strings.TrimSpace(params.Name)
	if !isValidGroupName(name) {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_group_name", nil)
	}

	channelType := strings.TrimSpace(params.ChannelType)
	if !s.isValidChannelType(channelType) {
		supported := strings.Join(s.channelRegistry, ", ")
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_channel_type", map[string]any{"types": supported})
	}

	groupType := strings.TrimSpace(params.GroupType)
	if groupType == "" {
		groupType = "standard"
	}
	if groupType != "standard" && groupType != "aggregate" {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_group_type", nil)
	}

	var cleanedUpstreams datatypes.JSON
	var testModel string
	var validationEndpoint string

	switch groupType {
	case "aggregate":
		validationEndpoint = ""
		cleanedUpstreams = datatypes.JSON("[]")
		testModel = "-"
	case "standard":
		testModel = strings.TrimSpace(params.TestModel)
		if testModel == "" {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.test_model_required", nil)
		}
		cleaned, err := s.validateAndCleanUpstreams(params.Upstreams)
		if err != nil {
			return nil, err
		}
		cleanedUpstreams = cleaned

		validationEndpoint = strings.TrimSpace(params.ValidationEndpoint)
		if !isValidValidationEndpoint(validationEndpoint) {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_test_path", nil)
		}
	}

	cleanedConfig, err := s.validateAndCleanConfig(params.Config)
	if err != nil {
		return nil, err
	}

	headerRulesJSON, err := s.normalizeHeaderRules(params.HeaderRules)
	if err != nil {
		return nil, err
	}
	if headerRulesJSON == nil {
		headerRulesJSON = datatypes.JSON("[]")
	}

	// Validate model mapping if provided
	modelMapping := strings.TrimSpace(params.ModelMapping)
	if modelMapping != "" {
		if err := utils.ValidateModelMapping(modelMapping); err != nil {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_mapping", map[string]any{"error": err.Error()})
		}
	}

	group := models.Group{
		Name:               name,
		DisplayName:        strings.TrimSpace(params.DisplayName),
		Description:        strings.TrimSpace(params.Description),
		GroupType:          groupType,
		Upstreams:          cleanedUpstreams,
		ChannelType:        channelType,
		Sort:               params.Sort,
		TestModel:          testModel,
		ValidationEndpoint: validationEndpoint,
		ParamOverrides:     params.ParamOverrides,
		Config:             cleanedConfig,
		HeaderRules:        headerRulesJSON,
		ModelMapping:       modelMapping,
		ProxyKeys:          strings.TrimSpace(params.ProxyKeys),
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, app_errors.ErrDatabase
	}

	if err := tx.Create(&group).Error; err != nil {
		tx.Rollback()
		return nil, app_errors.ParseDBError(err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	// Invalidate group list cache after creating a new group
	s.invalidateGroupListCache()

	return &group, nil
}

// invalidateGroupListCache clears the group list cache
func (s *GroupService) invalidateGroupListCache() {
	s.groupListCacheMu.Lock()
	s.groupListCache = nil
	s.groupListCacheMu.Unlock()
	logrus.Debug("Group list cache invalidated")
}

// ListGroups returns all groups without sub-group relations.
func (s *GroupService) ListGroups(ctx context.Context) ([]models.Group, error) {
	// Check cache first
	s.groupListCacheMu.RLock()
	if s.groupListCache != nil && time.Now().Before(s.groupListCache.ExpiresAt) {
		// Cache hit, return cached groups
		groups := make([]models.Group, len(s.groupListCache.Groups))
		copy(groups, s.groupListCache.Groups)
		s.groupListCacheMu.RUnlock()
		logrus.WithContext(ctx).Debug("Group list cache hit")
		return groups, nil
	}
	s.groupListCacheMu.RUnlock()

// Cache miss, fetch from database without timeout for reliability
// Group list queries should be fast with proper indexes
groups := make([]models.Group, 0, 100)
	if err := s.db.WithContext(ctx).Order("sort asc, id desc").Find(&groups).Error; err != nil {
		// On failure, return stale cache if available to keep UI responsive
		s.groupListCacheMu.RLock()
		if s.groupListCache != nil {
			stale := make([]models.Group, len(s.groupListCache.Groups))
			copy(stale, s.groupListCache.Groups)
			s.groupListCacheMu.RUnlock()
			logrus.WithContext(ctx).WithError(err).Warn("ListGroups DB error - returning stale cache")
			return stale, nil
		}
		s.groupListCacheMu.RUnlock()
		return nil, app_errors.ParseDBError(err)
	}

	// Update cache
	s.groupListCacheMu.Lock()
	s.groupListCache = &groupListCacheEntry{
		Groups:    groups,
		ExpiresAt: time.Now().Add(s.groupListCacheTTL),
	}
	s.groupListCacheMu.Unlock()

	logrus.WithContext(ctx).Debug("Group list cache updated")
	return groups, nil
}

// UpdateGroup validates and updates an existing group.
func (s *GroupService) UpdateGroup(ctx context.Context, id uint, params GroupUpdateParams) (*models.Group, error) {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	defer tx.Rollback()

	if params.Name != nil {
		cleanedName := strings.TrimSpace(*params.Name)
		if !isValidGroupName(cleanedName) {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_group_name", nil)
		}
		group.Name = cleanedName
	}

	if params.DisplayName != nil {
		group.DisplayName = strings.TrimSpace(*params.DisplayName)
	}

	if params.Description != nil {
		group.Description = strings.TrimSpace(*params.Description)
	}

	if params.HasUpstreams {
		cleanedUpstreams, err := s.validateAndCleanUpstreams(params.Upstreams)
		if err != nil {
			return nil, err
		}
		group.Upstreams = cleanedUpstreams
	}

	// Check if this group is used as a sub-group in aggregate groups before allowing critical changes
	// Only perform the check if the new values are actually changing to avoid unnecessary COUNT queries
	if group.GroupType != "aggregate" {
		channelTypeChanged := params.ChannelType != nil && strings.TrimSpace(*params.ChannelType) != group.ChannelType
		validationEndpointChanged := params.ValidationEndpoint != nil && strings.TrimSpace(*params.ValidationEndpoint) != group.ValidationEndpoint

		if channelTypeChanged || validationEndpointChanged {
			count, err := s.aggregateGroupService.CountAggregateGroupsUsingSubGroup(ctx, group.ID)
			if err != nil {
				return nil, err
			}

			if count > 0 {
				// If referenced by aggregate groups, disallow these specific changes
				return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_referenced_cannot_modify",
					map[string]any{"count": count})
			}
		}
	}

	if params.ChannelType != nil && group.GroupType != "aggregate" {
		cleanedChannelType := strings.TrimSpace(*params.ChannelType)
		if !s.isValidChannelType(cleanedChannelType) {
			supported := strings.Join(s.channelRegistry, ", ")
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_channel_type", map[string]any{"types": supported})
		}
		group.ChannelType = cleanedChannelType
	}

	if params.Sort != nil {
		group.Sort = *params.Sort
	}

	if params.HasTestModel {
		cleanedTestModel := strings.TrimSpace(params.TestModel)
		if cleanedTestModel == "" {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.test_model_empty", nil)
		}
		group.TestModel = cleanedTestModel
	}

	if params.ParamOverrides != nil {
		group.ParamOverrides = params.ParamOverrides
	}

	if params.ValidationEndpoint != nil {
		validationEndpoint := strings.TrimSpace(*params.ValidationEndpoint)
		if !isValidValidationEndpoint(validationEndpoint) {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_test_path", nil)
		}
		group.ValidationEndpoint = validationEndpoint
	}

	if params.Config != nil {
		cleanedConfig, err := s.validateAndCleanConfig(params.Config)
		if err != nil {
			return nil, err
		}
		group.Config = cleanedConfig
	}

	if params.ProxyKeys != nil {
		group.ProxyKeys = strings.TrimSpace(*params.ProxyKeys)
	}

	if params.HeaderRules != nil {
		headerRulesJSON, err := s.normalizeHeaderRules(*params.HeaderRules)
		if err != nil {
			return nil, err
		}
		if headerRulesJSON == nil {
			headerRulesJSON = datatypes.JSON("[]")
		}
		group.HeaderRules = headerRulesJSON
	}

	if params.ModelMapping != nil {
		modelMapping := strings.TrimSpace(*params.ModelMapping)
		if modelMapping != "" {
			if err := utils.ValidateModelMapping(modelMapping); err != nil {
				return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_mapping", map[string]any{"error": err.Error()})
			}
		}
		group.ModelMapping = modelMapping
	}

	if err := tx.Save(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ErrDatabase
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	// Invalidate group list cache after updating a group
	s.invalidateGroupListCache()

	return &group, nil
}

// DeleteGroup removes a group and associated resources.
// This operation is idempotent - deleting a non-existent group returns success.
func (s *GroupService) DeleteGroup(ctx context.Context, id uint) error {
	// Start transaction
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	// Get the group for logging
	var group models.Group
	if err := tx.First(&group, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Group doesn't exist - idempotent delete returns success
			tx.Rollback()
			return nil
		}
		return app_errors.ParseDBError(err)
	}

	// Count keys for logging (fast query with index)
	var keyCount int64
	tx.Model(&models.APIKey{}).Where("group_id = ?", id).Count(&keyCount)

	// Delete sub-group relationships
	if err := tx.Where("group_id = ? OR sub_group_id = ?", id, id).Delete(&models.GroupSubGroup{}).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	// Delete all API keys for this group in a single operation
	if err := tx.Where("group_id = ?", id).Delete(&models.APIKey{}).Error; err != nil {
		return app_errors.ErrDatabase
	}

	// Delete the group
	if err := tx.Delete(&models.Group{}, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return app_errors.ErrDatabase
	}
	tx = nil

	// Clear memory store for this group after database commit
	// Use a goroutine to avoid blocking the response
	if keyCount > 0 {
		go func() {
			// Use a timeout context for background deletion
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if _, err := s.keyService.KeyProvider.RemoveAllKeys(cleanupCtx, id); err != nil {
				logrus.WithFields(logrus.Fields{
					"groupID": id,
					"error":   err,
				}).Error("failed to remove keys from memory store")
			}
		}()
	}

	// Invalidate caches
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}
	s.invalidateGroupListCache()
	s.InvalidateKeyStatsCache(id)

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"groupID":   id,
		"groupName": group.Name,
		"keyCount":  keyCount,
	}).Info("Successfully deleted group")

	return nil
}

// deleteKeysInBatches deletes keys in small batches with separate transactions
// This avoids long-running transactions that block other operations
func (s *GroupService) deleteKeysInBatches(ctx context.Context, keyIDs []uint) int64 {
	if len(keyIDs) == 0 {
		return 0
	}

	const batchSize = 50
	totalDeleted := int64(0)

	for i := 0; i < len(keyIDs); i += batchSize {
		end := i + batchSize
		if end > len(keyIDs) {
			end = len(keyIDs)
		}
		batchIDs := keyIDs[i:end]

		// Each batch in its own transaction
		var deleted int64
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Where("id IN ?", batchIDs).Delete(&models.APIKey{})
			if result.Error != nil {
				return result.Error
			}
			deleted = result.RowsAffected
			return nil
		})

		if err != nil {
			logrus.WithContext(ctx).WithError(err).WithField("batch", i/batchSize).Warn("Failed to delete batch of keys")
			continue
		}

		totalDeleted += deleted

		// Small delay between batches to allow other operations
		if i+batchSize < len(keyIDs) {
			time.Sleep(10 * time.Millisecond)
		}
	}

	return totalDeleted
}

// DeleteAllGroups removes all groups and their associated resources.
// This is a dangerous operation intended for debugging and testing purposes only.
// It should only be accessible when DEBUG_MODE environment variable is enabled.
//
// The operation performs the following steps:
// 1. Deletes all sub-group relationships
// 2. Deletes all API keys in batches to avoid long-running transactions
// 3. Deletes all groups
// 4. Clears the key store cache
// 5. Invalidates the group cache
//
// Returns an error if any database operation fails.
func (s *GroupService) DeleteAllGroups(ctx context.Context) error {
	logrus.WithContext(ctx).Warn("DeleteAllGroups called - this will remove ALL groups and keys")

	// Step 1: Get total key count before deletion (for logging only)
	var totalKeys int64
	if err := s.db.WithContext(ctx).Model(&models.APIKey{}).Count(&totalKeys).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to count API keys")
		return app_errors.ParseDBError(err)
	}

	logrus.WithContext(ctx).WithField("totalKeys", totalKeys).Info("Starting deletion of all groups and keys")

	// Step 2: Optimize SQLite for bulk deletion BEFORE starting transaction
	// Note: PRAGMA synchronous cannot be changed inside a transaction in SQLite
	// We only disable foreign keys which provides significant performance improvement
	// and is safe because we're deleting all related data anyway
	originalForeignKeys := true
	fkDisabled := false
	var fkResult int
	if err := s.db.WithContext(ctx).Raw("PRAGMA foreign_keys").Scan(&fkResult).Error; err == nil {
		originalForeignKeys = fkResult == 1
	}

	// Disable foreign keys for better performance
	if err := s.db.WithContext(ctx).Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("failed to disable foreign keys, continuing anyway")
	} else {
		fkDisabled = true
	}

	// Step 3: Begin transaction
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		// Restore foreign keys if transaction fails to start (only if we successfully disabled them)
		if fkDisabled && originalForeignKeys {
			s.db.WithContext(ctx).Exec("PRAGMA foreign_keys = ON")
		}
		return app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
			// Restore foreign keys on rollback (only if we successfully disabled them)
			if fkDisabled && originalForeignKeys {
				s.db.WithContext(ctx).Exec("PRAGMA foreign_keys = ON")
			}
		}
	}()

	// Step 4: Delete all sub-group relationships
	if err := tx.Exec("DELETE FROM group_sub_groups").Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to delete sub-group relationships")
		return app_errors.ParseDBError(err)
	}

	// Step 5: Delete all API keys using optimized DELETE
	// For SQLite, a simple DELETE FROM is the fastest way to remove all rows
	if totalKeys > 0 {
		result := tx.Exec("DELETE FROM api_keys")
		if result.Error != nil {
			logrus.WithContext(ctx).WithError(result.Error).Error("failed to delete all API keys")
			return app_errors.ParseDBError(result.Error)
		}
		logrus.WithContext(ctx).WithField("deletedKeys", result.RowsAffected).Info("Deleted all API keys")

		// Reset auto-increment counter (SQLite-specific)
		// This is optional but keeps the database clean for future inserts
		// Note: This will fail silently on non-SQLite databases, which is acceptable for debug-only operations
		if err := tx.Exec("DELETE FROM sqlite_sequence WHERE name='api_keys'").Error; err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("failed to reset api_keys sequence, continuing anyway")
		}
	}

	// Step 6: Delete all groups
	result := tx.Exec("DELETE FROM groups")
	if result.Error != nil {
		logrus.WithContext(ctx).WithError(result.Error).Error("failed to delete all groups")
		return app_errors.ParseDBError(result.Error)
	}
	logrus.WithContext(ctx).WithField("deletedGroups", result.RowsAffected).Info("Deleted all groups")

	// Reset auto-increment counter (SQLite-specific)
	// This is optional but keeps the database clean for future inserts
	// Note: This will fail silently on non-SQLite databases, which is acceptable for debug-only operations
	if err := tx.Exec("DELETE FROM sqlite_sequence WHERE name='groups'").Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("failed to reset groups sequence, continuing anyway")
	}

	// Step 7: Commit the transaction
	if err := tx.Commit().Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to commit transaction")
		// Foreign keys will be restored by defer
		return app_errors.ErrDatabase
	}
	tx = nil

	// Step 8: Restore foreign keys setting (only if we successfully disabled them)
	if fkDisabled && originalForeignKeys {
		if err := s.db.WithContext(ctx).Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("failed to restore foreign keys setting")
		}
	}

	// Step 9: Clear the key store cache
	// This removes all keys from memory to ensure consistency
	if err := s.keyService.KeyProvider.ClearAllKeys(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to clear key store cache")
		// Continue even if cache clear fails, as database is already updated
	}

	// Step 10: Invalidate the group cache to force reload
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
		// Continue even if cache invalidation fails
	}

	// Invalidate group list cache after deleting all groups
	s.invalidateGroupListCache()

	// Step 11: Reset in-memory key stats cache to avoid serving stale data for reused IDs
	s.keyStatsCacheMu.Lock()
	s.keyStatsCache = make(map[uint]groupKeyStatsCacheEntry)
	s.keyStatsCacheMu.Unlock()

	logrus.WithContext(ctx).Info("Successfully deleted all groups and keys")
	return nil
}

// CopyGroup duplicates a group and optionally copies active keys.
func (s *GroupService) CopyGroup(ctx context.Context, sourceGroupID uint, copyKeysOption string) (*models.Group, error) {
	option := strings.TrimSpace(copyKeysOption)
	if option == "" {
		option = "all"
	}
	if option != "none" && option != "valid_only" && option != "all" {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_copy_keys_value", nil)
	}

	var sourceGroup models.Group
	if err := s.db.WithContext(ctx).First(&sourceGroup, sourceGroupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	newGroup := sourceGroup
	newGroup.ID = 0
	// Generate unique name with suffix
	uniqueName := s.generateUniqueGroupNameForCopy(ctx, sourceGroup.Name, tx)
	newGroup.Name = uniqueName

	// Set display name with the same suffix as the name
	// Only append suffix if the unique name actually starts with the original name
	// (it may be truncated if the original name is too long)
	suffix := ""
	if strings.HasPrefix(uniqueName, sourceGroup.Name) {
		suffix = uniqueName[len(sourceGroup.Name):]
	}

	if sourceGroup.DisplayName != "" {
		if suffix != "" {
			newGroup.DisplayName = sourceGroup.DisplayName + suffix
		} else {
			// If the name was truncated, use the unique name directly
			newGroup.DisplayName = sourceGroup.DisplayName + " Copy"
		}
	} else {
		newGroup.DisplayName = uniqueName
	}

	newGroup.CreatedAt = time.Time{}
	newGroup.UpdatedAt = time.Time{}
	newGroup.LastValidatedAt = nil

	// Create the new group
	if err := tx.Create(&newGroup).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Fetch source keys to copy if needed (quick query)
	var sourceKeyValues []string
	if option != "none" {
		// Only fetch key_value field to reduce memory and query time
		var sourceKeyData []struct {
			KeyValue string
		}
		query := tx.Table("api_keys").Select("key_value").Where("group_id = ?", sourceGroupID)
		if option == "valid_only" {
			query = query.Where("status = ?", models.KeyStatusActive)
		}
		if err := query.Scan(&sourceKeyData).Error; err != nil {
			return nil, app_errors.ParseDBError(err)
		}

		// Decrypt keys (fast operation)
		for _, keyData := range sourceKeyData {
			decryptedKey, err := s.encryptionSvc.Decrypt(keyData.KeyValue)
			if err != nil {
				logrus.WithContext(ctx).Debug("failed to decrypt key during group copy, skipping")
				continue
			}
			sourceKeyValues = append(sourceKeyValues, decryptedKey)
		}
	}

	// Commit the transaction immediately - group is created
	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	tx = nil

	// Invalidate caches
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}
	s.invalidateGroupListCache()

	// Start async copy task if we have keys to copy
	// This returns immediately and shows progress in UI
	if len(sourceKeyValues) > 0 {
		// Use import service for async copy with progress tracking
		keysText := strings.Join(sourceKeyValues, "\n")
		if _, err := s.keyImportSvc.StartImportTask(&newGroup, keysText); err != nil {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupId":  newGroup.ID,
				"keyCount": len(sourceKeyValues),
			}).WithError(err).Error("failed to start async import task for group copy")
			// Don't fail the copy - group is already created
		} else {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupId":  newGroup.ID,
				"keyCount": len(sourceKeyValues),
			}).Info("Started async import task for group copy")
		}
	}

	return &newGroup, nil
}

// GetGroupStats returns aggregated usage statistics for a group.
func (s *GroupService) GetGroupStats(ctx context.Context, groupID uint) (*GroupStats, error) {
	var group models.Group
// Try cache first to avoid DB under heavy writes
if cached, err := s.groupManager.GetGroupByID(groupID); err == nil && cached != nil {
	group = *cached
} else {
	// Short DB lookup with small timeout
	qctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := s.db.WithContext(qctx).Where("id = ?", groupID).Limit(1).Find(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
}
if group.ID == 0 {
	return nil, app_errors.ErrResourceNotFound
}

	// Select different statistics logic based on group type
	if group.GroupType == "aggregate" {
		return s.getAggregateGroupStats(ctx, groupID)
	}

	return s.getStandardGroupStats(ctx, groupID)
}

// queryGroupHourlyStats queries aggregated hourly statistics from group_hourly_stats table
func (s *GroupService) queryGroupHourlyStats(ctx context.Context, groupID uint, hours int) (RequestStats, error) {
	var result struct {
		SuccessCount int64
		FailureCount int64
	}

	now := time.Now()
	currentHour := now.Truncate(time.Hour)
	endTime := currentHour.Add(time.Hour) // Include current hour
	startTime := endTime.Add(-time.Duration(hours) * time.Hour)

	if err := s.db.WithContext(ctx).Model(&models.GroupHourlyStat{}).
		Select("SUM(success_count) as success_count, SUM(failure_count) as failure_count").
		Where("group_id = ? AND time >= ? AND time < ?", groupID, startTime, endTime).
		Scan(&result).Error; err != nil {
		return RequestStats{}, err
	}

	return calculateRequestStats(result.SuccessCount+result.FailureCount, result.FailureCount), nil
}

// queryMultipleTimeRangeStats queries statistics for multiple time periods in a single SQL query
// This is optimized to use CASE WHEN statements to reduce the number of database queries
func (s *GroupService) queryMultipleTimeRangeStats(ctx context.Context, groupID uint) (stats24h, stats7d, stats30d RequestStats, err error) {
	var result struct {
		Success24h  int64
		Failure24h  int64
		Success7d   int64
		Failure7d   int64
		Success30d  int64
		Failure30d  int64
	}

	now := time.Now()
	currentHour := now.Truncate(time.Hour)
	endTime := currentHour.Add(time.Hour) // Include current hour

	// Calculate time boundaries
	time24hAgo := endTime.Add(-24 * time.Hour)
	time7dAgo := endTime.Add(-7 * 24 * time.Hour)
	time30dAgo := endTime.Add(-30 * 24 * time.Hour)

// Single query with CASE WHEN for all three time ranges
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN time >= ? THEN success_count ELSE 0 END), 0) as success24h,
			COALESCE(SUM(CASE WHEN time >= ? THEN failure_count ELSE 0 END), 0) as failure24h,
			COALESCE(SUM(CASE WHEN time >= ? THEN success_count ELSE 0 END), 0) as success7d,
			COALESCE(SUM(CASE WHEN time >= ? THEN failure_count ELSE 0 END), 0) as failure7d,
			COALESCE(SUM(success_count), 0) as success30d,
			COALESCE(SUM(failure_count), 0) as failure30d
		FROM group_hourly_stats
		WHERE group_id = ? AND time >= ? AND time < ?
	`

	// Add a timeout to avoid blocking during heavy writes
queryCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	type qres struct{ err error }
	done := make(chan qres, 1)
	go func() {
		err := s.db.WithContext(queryCtx).Raw(query,
			time24hAgo, time24hAgo,
			time7dAgo, time7dAgo,
			groupID, time30dAgo, endTime,
		).Scan(&result).Error
		done <- qres{err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return RequestStats{}, RequestStats{}, RequestStats{}, r.err
		}
		stats24h = calculateRequestStats(result.Success24h+result.Failure24h, result.Failure24h)
		stats7d = calculateRequestStats(result.Success7d+result.Failure7d, result.Failure7d)
		stats30d = calculateRequestStats(result.Success30d+result.Failure30d, result.Failure30d)
		return stats24h, stats7d, stats30d, nil
	case <-queryCtx.Done():
		// Timeout: return zero stats to avoid blocking UI
		logrus.WithContext(ctx).WithField("groupID", groupID).Warn("Stats query timed out, returning zeros")
		return RequestStats{}, RequestStats{}, RequestStats{}, nil
	}
}

// fetchKeyStats retrieves API key statistics for a group with caching
func (s *GroupService) fetchKeyStats(ctx context.Context, groupID uint) (KeyStats, error) {
	// Check cache first
	s.keyStatsCacheMu.RLock()
	if entry, ok := s.keyStatsCache[groupID]; ok {
		if time.Now().Before(entry.ExpiresAt) {
			s.keyStatsCacheMu.RUnlock()
			return entry.Stats, nil
		}
	}
	s.keyStatsCacheMu.RUnlock()

	// Cache miss - query database with timeout to avoid blocking during bulk inserts
	// Create a context with timeout for the query
	// Use a longer timeout to ensure data is fetched properly during import operations
queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Use index-friendly COUNT queries to leverage composite indexes and avoid full table scans

	// Use two index-friendly COUNT queries instead of a single aggregation to leverage composite indexes
	var totalKeys int64
	if err := s.db.WithContext(queryCtx).Model(&models.APIKey{}).Where("group_id = ?", groupID).Count(&totalKeys).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logrus.WithContext(ctx).WithField("groupID", groupID).Warn("Key stats total count timed out, returning empty stats")
			return KeyStats{TotalKeys: 0, ActiveKeys: 0, InvalidKeys: 0}, nil
		}
		return KeyStats{}, fmt.Errorf("failed to count total keys: %w", err)
	}

	var activeKeys int64
	if err := s.db.WithContext(queryCtx).Model(&models.APIKey{}).Where("group_id = ? AND status = ?", groupID, models.KeyStatusActive).Count(&activeKeys).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logrus.WithContext(ctx).WithField("groupID", groupID).Warn("Key stats active count timed out, returning partial stats")
			activeKeys = 0
		} else {
			return KeyStats{}, fmt.Errorf("failed to count active keys: %w", err)
		}
	}

	stats := KeyStats{
		TotalKeys:   totalKeys,
		ActiveKeys:  activeKeys,
		InvalidKeys: totalKeys - activeKeys,
	}

	// Update cache
	s.keyStatsCacheMu.Lock()
	s.keyStatsCache[groupID] = groupKeyStatsCacheEntry{
		Stats:     stats,
		ExpiresAt: time.Now().Add(s.keyStatsCacheTTL),
	}
	s.keyStatsCacheMu.Unlock()

	return stats, nil
}

// InvalidateKeyStatsCache invalidates the key statistics cache for a specific group
func (s *GroupService) InvalidateKeyStatsCache(groupID uint) {
	s.keyStatsCacheMu.Lock()
	delete(s.keyStatsCache, groupID)
	s.keyStatsCacheMu.Unlock()
}

// fetchRequestStats retrieves request statistics for multiple time periods
func (s *GroupService) fetchRequestStats(ctx context.Context, groupID uint, stats *GroupStats) []error {
	// Use the optimized single query to get all time range statistics
	stats24h, stats7d, stats30d, err := s.queryMultipleTimeRangeStats(ctx, groupID)
	if err != nil {
		return []error{fmt.Errorf("failed to get time range stats: %w", err)}
	}

	// Assign the results to the stats structure
	stats.Stats24Hour = stats24h
	stats.Stats7Day = stats7d
	stats.Stats30Day = stats30d

	return nil
}

func (s *GroupService) getStandardGroupStats(ctx context.Context, groupID uint) (*GroupStats, error) {
	stats := &GroupStats{}
	var allErrors []error
	var errsMu sync.Mutex

	// Run key stats and request stats concurrently to avoid additive timeouts
	done := make(chan struct{}, 2)
	// key stats
	go func() {
		defer func() { done <- struct{}{} }()
		keyStats, err := s.fetchKeyStats(ctx, groupID)
		if err != nil {
			errsMu.Lock()
			allErrors = append(allErrors, err)
			errsMu.Unlock()
			logrus.WithContext(ctx).WithError(err).Warn("failed to fetch key stats, continuing with request stats")
			return
		}
		stats.KeyStats = keyStats
	}()
	// request stats (24h/7d/30d)
	go func() {
		defer func() { done <- struct{}{} }()
		if errs := s.fetchRequestStats(ctx, groupID, stats); len(errs) > 0 {
			errsMu.Lock()
			allErrors = append(allErrors, errs...)
			errsMu.Unlock()
		}
	}()
	// Wait for both
	<-done
	<-done

	if len(allErrors) > 0 {
		logrus.WithContext(ctx).WithError(allErrors[0]).Error("errors occurred while fetching group stats")
		if stats.Stats24Hour.TotalRequests > 0 || stats.Stats7Day.TotalRequests > 0 || stats.Stats30Day.TotalRequests > 0 {
			return stats, nil
		}
		return nil, NewI18nError(app_errors.ErrDatabase, "database.group_stats_failed", nil)
	}
	return stats, nil
}

func (s *GroupService) getAggregateGroupStats(ctx context.Context, groupID uint) (*GroupStats, error) {
	stats := &GroupStats{}

	// Aggregate groups only need request statistics, not key statistics
	if errs := s.fetchRequestStats(ctx, groupID, stats); len(errs) > 0 {
		logrus.WithContext(ctx).WithError(errs[0]).Error("errors occurred while fetching aggregate group stats")
		// Return partial stats if we have some data
		if stats.Stats24Hour.TotalRequests > 0 || stats.Stats7Day.TotalRequests > 0 || stats.Stats30Day.TotalRequests > 0 {
			return stats, nil
		}
		return nil, NewI18nError(app_errors.ErrDatabase, "database.group_stats_failed", nil)
	}

	return stats, nil
}

// GetGroupConfigOptions returns metadata describing available overrides.
func (s *GroupService) GetGroupConfigOptions() ([]ConfigOption, error) {
	defaultSettings := utils.DefaultSystemSettings()
	settingDefinitions := utils.GenerateSettingsMetadata(&defaultSettings)
	defMap := make(map[string]models.SystemSettingInfo)
	for _, def := range settingDefinitions {
		defMap[def.Key] = def
	}

	currentSettings := s.settingsManager.GetSettings()
	currentSettingsValue := reflect.ValueOf(currentSettings)
	currentSettingsType := currentSettingsValue.Type()
	jsonToFieldMap := make(map[string]string)
	for i := 0; i < currentSettingsType.NumField(); i++ {
		field := currentSettingsType.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag != "" {
			jsonToFieldMap[jsonTag] = field.Name
		}
	}

	groupConfigType := reflect.TypeOf(models.GroupConfig{})
	var options []ConfigOption
	for i := 0; i < groupConfigType.NumField(); i++ {
		field := groupConfigType.Field(i)
		jsonTag := field.Tag.Get("json")
		key := strings.Split(jsonTag, ",")[0]
		if key == "" || key == "-" {
			continue
		}

		definition, ok := defMap[key]
		if !ok {
			continue
		}

		var defaultValue any
		if fieldName, ok := jsonToFieldMap[key]; ok {
			defaultValue = currentSettingsValue.FieldByName(fieldName).Interface()
		}

		options = append(options, ConfigOption{
			Key:          key,
			Name:         definition.Name,
			Description:  definition.Description,
			DefaultValue: defaultValue,
		})
	}

	return options, nil
}

// validateAndCleanConfig verifies GroupConfig overrides.
func (s *GroupService) validateAndCleanConfig(configMap map[string]any) (map[string]any, error) {
	if configMap == nil {
		return nil, nil
	}

	var tempGroupConfig models.GroupConfig
	groupConfigType := reflect.TypeOf(tempGroupConfig)
	validFields := make(map[string]bool)
	for i := 0; i < groupConfigType.NumField(); i++ {
		jsonTag := groupConfigType.Field(i).Tag.Get("json")
		fieldName := strings.Split(jsonTag, ",")[0]
		if fieldName != "" && fieldName != "-" {
			validFields[fieldName] = true
		}
	}

	for key := range configMap {
		if !validFields[key] {
			message := fmt.Sprintf("unknown config field: '%s'", key)
			return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": message})
		}
	}

	if err := s.settingsManager.ValidateGroupConfigOverrides(configMap); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	configBytes, err := json.Marshal(configMap)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	var validatedConfig models.GroupConfig
	if err := json.Unmarshal(configBytes, &validatedConfig); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	validatedBytes, err := json.Marshal(validatedConfig)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	var finalMap map[string]any
	if err := json.Unmarshal(validatedBytes, &finalMap); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}

	return finalMap, nil
}

// normalizeHeaderRules deduplicates and normalises header rules.
func (s *GroupService) normalizeHeaderRules(rules []models.HeaderRule) (datatypes.JSON, error) {
	if len(rules) == 0 {
		return nil, nil
	}

	normalized := make([]models.HeaderRule, 0, len(rules))
	seenKeys := make(map[string]bool)

	for _, rule := range rules {
		key := strings.TrimSpace(rule.Key)
		if key == "" {
			continue
		}
		canonicalKey := http.CanonicalHeaderKey(key)
		if seenKeys[canonicalKey] {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.duplicate_header", map[string]any{"key": canonicalKey})
		}
		seenKeys[canonicalKey] = true
		normalized = append(normalized, models.HeaderRule{Key: canonicalKey, Value: rule.Value, Action: rule.Action})
	}

	if len(normalized) == 0 {
		return nil, nil
	}

	headerRulesBytes, err := json.Marshal(normalized)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrInternalServer, "error.process_header_rules", map[string]any{"error": err.Error()})
	}

	return datatypes.JSON(headerRulesBytes), nil
}

// validateAndCleanUpstreams validates upstream definitions.
func (s *GroupService) validateAndCleanUpstreams(upstreams json.RawMessage) (datatypes.JSON, error) {
	if len(upstreams) == 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "upstreams field is required"})
	}

	var defs []struct {
		URL      string  `json:"url"`
		Weight   int     `json:"weight"`
		ProxyURL *string `json:"proxy_url,omitempty"`
	}
	if err := json.Unmarshal(upstreams, &defs); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": err.Error()})
	}

	if len(defs) == 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "at least one upstream is required"})
	}

	hasActiveUpstream := false
	for i := range defs {
		defs[i].URL = strings.TrimSpace(defs[i].URL)
		if defs[i].URL == "" {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "upstream URL cannot be empty"})
		}
		if !strings.HasPrefix(defs[i].URL, "http://") && !strings.HasPrefix(defs[i].URL, "https://") {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": fmt.Sprintf("invalid URL format for upstream: %s", defs[i].URL)})
		}
		if defs[i].Weight < 0 {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "upstream weight must be a non-negative integer"})
		}
		if defs[i].Weight > 0 {
			hasActiveUpstream = true
		}
		// Clean proxy_url if present
		if defs[i].ProxyURL != nil {
			trimmed := strings.TrimSpace(*defs[i].ProxyURL)
			if trimmed == "" {
				defs[i].ProxyURL = nil
			} else {
				defs[i].ProxyURL = &trimmed
			}
		}
	}

	if !hasActiveUpstream {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": "at least one upstream must have a weight greater than 0"})
	}

	cleanedUpstreams, err := json.Marshal(defs)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_upstreams", map[string]any{"error": err.Error()})
	}

	return datatypes.JSON(cleanedUpstreams), nil
}

func calculateRequestStats(total, failed int64) RequestStats {
	stats := RequestStats{
		TotalRequests:  total,
		FailedRequests: failed,
	}
	if total > 0 {
		rate := float64(failed) / float64(total)
		stats.FailureRate = math.Round(rate*10000) / 10000
	}
	return stats
}

// generateUniqueGroupNameForCopy generates a unique group name for copy operations
// For copy operations, we don't try the original name because it definitely exists
// We directly append a random 4-character suffix to create a new unique name
// Format: {baseName}{random4} (e.g., "siliconflow" becomes "siliconflowa8f3")
// It accepts an optional db parameter to use a transaction, otherwise uses the default connection
// Optimized: Check each candidate name individually instead of loading all names
// This uses the UNIQUE index on name column for fast lookups, avoiding full table scan
// Performance: O(k) where k is the number of attempts (usually 1), instead of O(n) where n is total groups
func (s *GroupService) generateUniqueGroupNameForCopy(ctx context.Context, baseName string, db ...*gorm.DB) string {
	var queryDB *gorm.DB
	if len(db) > 0 && db[0] != nil {
		// Use provided database connection (e.g., transaction)
		queryDB = db[0].WithContext(ctx)
	} else {
		// Use default database connection
		queryDB = s.db.WithContext(ctx)
	}

	// For copy operations, we don't try the original name (it definitely exists)
	// Generate a name with random suffix directly (no underscore, no timestamp)
	// Ensure the name doesn't exceed database limits (usually 100-255 chars)
	trimmedBaseName := baseName
	if len(baseName)+4 > 100 {
		trimmedBaseName = baseName[:96] // Leave room for 4-char suffix
	}

	maxAttempts := 10
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Generate name with random suffix (4 chars)
		// e.g., "siliconflow" becomes "siliconflowa8f3"
		groupName := trimmedBaseName + utils.GenerateRandomSuffix()

		// Check if this name already exists
		var existingGroup models.Group
		if err := queryDB.Where("name = ?", groupName).First(&existingGroup).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				// Name doesn't exist, use it
				logrus.WithContext(ctx).Debugf("Generated unique group name for copy: %s (original: %s)", groupName, baseName)
				return groupName
			}
			// On other errors, log and continue to try alternatives
			logrus.WithContext(ctx).WithError(err).Warn("failed to check if group name exists, trying alternatives")
		}

		// Name exists, try again with a new random suffix
	}

	// Final fallback: use timestamp suffix if all random attempts failed
	groupName := fmt.Sprintf("%s%d", trimmedBaseName, time.Now().Unix())
	logrus.WithContext(ctx).Warnf("Failed to generate unique group name after %d attempts, using timestamp suffix", maxAttempts)
	return groupName
}

// isValidGroupName validates the group name.
func isValidGroupName(name string) bool {
	if name == "" {
		return false
	}
	match, _ := regexp.MatchString("^[a-z0-9_-]{1,100}$", name)
	return match
}

// isValidValidationEndpoint validates custom validation endpoint path.
func isValidValidationEndpoint(endpoint string) bool {
	if endpoint == "" {
		return true
	}
	if !strings.HasPrefix(endpoint, "/") {
		return false
	}
	if strings.Contains(endpoint, "://") {
		return false
	}
	return true
}

// isValidChannelType checks channel type against registered channels.
func (s *GroupService) isValidChannelType(channelType string) bool {
	for _, t := range s.channelRegistry {
		if t == channelType {
			return true
		}
	}
	return false
}

// ToggleGroupEnabled enables or disables a group
func (s *GroupService) ToggleGroupEnabled(ctx context.Context, id uint, enabled bool) error {
	// Update directly (RowsAffected check below handles non-existent groups)
	result := s.db.WithContext(ctx).Model(&models.Group{}).Where("id = ?", id).Update("enabled", enabled)
	if result.Error != nil {
		return app_errors.ParseDBError(result.Error)
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrResourceNotFound
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	// Invalidate group list cache after toggling group enabled status
	s.invalidateGroupListCache()

	return nil
}
