package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/keypool"
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
	channelFactory        *channel.Factory
	keyProvider           *keypool.KeyProvider
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
	channelFactory *channel.Factory,
	keyProvider *keypool.KeyProvider,
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
		channelFactory:        channelFactory,
		keyProvider:           keyProvider,
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
	// Set callback to invalidate group list cache when GroupManager cache is invalidated
	if svc.groupManager != nil {
		svc.groupManager.CacheInvalidationCallback = svc.InvalidateGroupListCache
	}
	return svc
}

// GroupCreateParams captures all fields required to create a group.
type GroupCreateParams struct {
	Name                string
	DisplayName         string
	Description         string
	GroupType           string
	Upstreams           json.RawMessage
	ChannelType         string
	Sort                int
	TestModel           string
	ValidationEndpoint  string
	ParamOverrides      map[string]any
	Config              map[string]any
	HeaderRules         []models.HeaderRule
	ModelMapping        string // Deprecated: for backward compatibility
	ModelRedirectRules  map[string]string
	ModelRedirectStrict bool
	PathRedirects       []models.PathRedirectRule
	ProxyKeys           string
	SubGroups           []SubGroupInput
}

// GroupUpdateParams captures updatable fields for a group.
type GroupUpdateParams struct {
	Name                *string
	DisplayName         *string
	Description         *string
	GroupType           *string
	Upstreams           json.RawMessage
	HasUpstreams        bool
	ChannelType         *string
	Sort                *int
	TestModel           string
	HasTestModel        bool
	ValidationEndpoint  *string
	ParamOverrides      map[string]any
	Config              map[string]any
	HeaderRules         *[]models.HeaderRule
	ModelMapping        *string // Deprecated: for backward compatibility
	ModelRedirectRules  map[string]string
	ModelRedirectStrict *bool
	PathRedirects       []models.PathRedirectRule
	ProxyKeys           *string
	SubGroups           *[]SubGroupInput
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
	if err := validateParamOverrides(params.ParamOverrides); err != nil {
		return nil, err
	}

	headerRulesJSON, err := s.normalizeHeaderRules(params.HeaderRules)
	if err != nil {
		return nil, err
	}
	if headerRulesJSON == nil {
		headerRulesJSON = datatypes.JSON("[]")
	}

	// Migration: If ModelMapping is provided but ModelRedirectRules is empty, migrate from old format
	modelMapping := strings.TrimSpace(params.ModelMapping)
	modelRedirectRules := params.ModelRedirectRules

	if modelMapping != "" && len(modelRedirectRules) == 0 {
		// Validate old format
		if err := utils.ValidateModelMapping(modelMapping); err != nil {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_mapping", map[string]any{"error": err.Error()})
		}
		// Migrate to new format
		migrated, err := utils.MigrateModelMappingToRedirectRules(modelMapping)
		if err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("Failed to migrate ModelMapping to ModelRedirectRules")
		} else {
			modelRedirectRules = migrated
		}
	}

	// Validate model redirect rules for aggregate groups
	if groupType == "aggregate" && len(modelRedirectRules) > 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.aggregate_no_model_redirect", nil)
	}

	// Validate model redirect rules format
	if err := validateModelRedirectRules(modelRedirectRules); err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_redirect", map[string]any{"error": err.Error()})
	}

	// Normalize path redirects (OpenAI only; stored regardless, applied at runtime per channel)
	pathRedirectsJSON, err := s.normalizePathRedirects(params.PathRedirects)
	if err != nil {
		return nil, err
	}

	group := models.Group{
		Name:                name,
		DisplayName:         strings.TrimSpace(params.DisplayName),
		Description:         strings.TrimSpace(params.Description),
		GroupType:           groupType,
		Upstreams:           cleanedUpstreams,
		ChannelType:         channelType,
		Sort:                params.Sort,
		TestModel:           testModel,
		ValidationEndpoint:  validationEndpoint,
		ParamOverrides:      params.ParamOverrides,
		Config:              cleanedConfig,
		HeaderRules:         headerRulesJSON,
		ModelMapping:        modelMapping, // Keep for backward compatibility
		ModelRedirectRules:  convertToJSONMap(modelRedirectRules),
		ModelRedirectStrict: params.ModelRedirectStrict,
		PathRedirects:       pathRedirectsJSON,
		ProxyKeys:           strings.TrimSpace(params.ProxyKeys),
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

// InvalidateGroupListCache exposes group list cache invalidation for other packages (e.g., handlers)
func (s *GroupService) InvalidateGroupListCache() {
	s.invalidateGroupListCache()
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

	// Cache miss, fetch from database with timeout for reliability
	// Group list queries should be fast with proper indexes
	groups := make([]models.Group, 0, 100)

	queryCtx, cancel := context.WithTimeout(ctx, getDBLookupTimeout())
	defer cancel()

	if err := s.db.WithContext(queryCtx).Order(GroupListOrderClause).Find(&groups).Error; err != nil {
		// Only use stale cache for transient errors (timeout/canceled) to keep UI responsive
		// For other errors (schema issues, query bugs), return the error immediately
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			s.groupListCacheMu.RLock()
			if s.groupListCache != nil {
				stale := make([]models.Group, len(s.groupListCache.Groups))
				copy(stale, s.groupListCache.Groups)
				s.groupListCacheMu.RUnlock()
				logrus.WithContext(ctx).WithError(err).Warn("ListGroups timeout/canceled - returning stale cache")
				return stale, nil
			}
			s.groupListCacheMu.RUnlock()
		}
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

// CountChildGroups returns the count of child groups for a parent group.
// Used for warning before deletion.
func (s *GroupService) CountChildGroups(ctx context.Context, parentGroupID uint) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&models.Group{}).
		Where("parent_group_id = ?", parentGroupID).
		Count(&count).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}
	return count, nil
}

// UpdateGroup validates and updates an existing group.
func (s *GroupService) UpdateGroup(ctx context.Context, id uint, params GroupUpdateParams) (*models.Group, error) {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Track old values for child group sync
	oldName := group.Name
	oldProxyKeys := group.ProxyKeys

	// Perform all validation before the final database write to minimize lock time.
	// This is especially important for SQLite which uses database-level locking.

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
		if err := validateParamOverrides(params.ParamOverrides); err != nil {
			return nil, err
		}
		group.ParamOverrides = params.ParamOverrides
	}

	// Validate model redirect rules for aggregate groups
	if group.GroupType == "aggregate" && params.ModelRedirectRules != nil && len(params.ModelRedirectRules) > 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.aggregate_no_model_redirect", nil)
	}

	// Validate model redirect rules format
	if params.ModelRedirectRules != nil {
		if err := validateModelRedirectRules(params.ModelRedirectRules); err != nil {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_redirect", map[string]any{"error": err.Error()})
		}
		group.ModelRedirectRules = convertToJSONMap(params.ModelRedirectRules)
	}

	if params.ModelRedirectStrict != nil {
		group.ModelRedirectStrict = *params.ModelRedirectStrict
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

		// Check if cc_support is being disabled for OpenAI groups before performing any database write.
		// If so, verify that this group is not used as a sub-group in any Anthropic aggregate groups.
		// NOTE: This guard is best-effort and not wrapped in an explicit transaction. There is a small
		// time-of-check-to-time-of-use window where aggregate membership can change concurrently, but
		// we intentionally keep lock time minimal (especially for SQLite). Any misconfiguration will
		// surface quickly via failing aggregate requests and can be corrected via configuration.
		if group.ChannelType == "openai" && group.GroupType != "aggregate" {
			// Note: models.Group.Config is stored as datatypes.JSONMap while validateAndCleanConfig
			// returns a map[string]any. We intentionally convert cleanedConfig to JSONMap here to
			// keep the helper signature strongly typed and avoid accidental misuse.
			oldCCEnabled := isConfigCCSupportEnabled(group.Config)
			newCCEnabled := isConfigCCSupportEnabled(datatypes.JSONMap(cleanedConfig))

			// CC support is being disabled (true -> false)
			if oldCCEnabled && !newCCEnabled {
				// Get parent aggregate groups
				parentGroups, err := s.aggregateGroupService.GetParentAggregateGroups(ctx, group.ID)
				if err != nil {
					return nil, app_errors.ParseDBError(err)
				}

				// Batch fetch channel types for all parent groups to avoid N+1 queries
				anthropicParents := make([]string, 0)
				if len(parentGroups) > 0 {
					parentIDs := make([]uint, 0, len(parentGroups))
					for _, parent := range parentGroups {
						parentIDs = append(parentIDs, parent.GroupID)
					}

					var parentGroupModels []models.Group
					if err := s.db.WithContext(ctx).Select("id", "channel_type").Where("id IN ?", parentIDs).Find(&parentGroupModels).Error; err != nil {
						return nil, app_errors.ParseDBError(err)
					}

					channelTypeMap := make(map[uint]string, len(parentGroupModels))
					for _, pg := range parentGroupModels {
						channelTypeMap[pg.ID] = pg.ChannelType
					}

					for _, parent := range parentGroups {
						if channelTypeMap[parent.GroupID] == "anthropic" {
							anthropicParents = append(anthropicParents, parent.Name)
						}
					}
				}

				// If used by Anthropic aggregate groups, disallow disabling CC support
				if len(anthropicParents) > 0 {
					return nil, NewI18nError(app_errors.ErrValidation, "validation.cc_support_cannot_disable_used_by_anthropic",
						map[string]any{"groups": strings.Join(anthropicParents, ", ")})
				}
			}
		}

		// Persist validated config as JSONMap for consistency with models.Group.
		group.Config = datatypes.JSONMap(cleanedConfig)
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

	// Update path redirects if provided
	if params.PathRedirects != nil {
		pathRedirectsJSON, err := s.normalizePathRedirects(params.PathRedirects)
		if err != nil {
			return nil, err
		}
		group.PathRedirects = pathRedirectsJSON
	}

	// Perform the actual database write - minimizes lock time
	if err := s.db.WithContext(ctx).Save(&group).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Sync child groups if parent's name or proxy_keys changed
	// Only sync for standard groups that are not child groups themselves
	if group.GroupType == "standard" && group.ParentGroupID == nil {
		nameChanged := oldName != group.Name
		proxyKeysChanged := oldProxyKeys != group.ProxyKeys

		if nameChanged || proxyKeysChanged {
			// Sync child groups inline to avoid circular dependency with ChildGroupService
			if err := s.syncChildGroupsOnParentUpdate(ctx, &group, oldName, oldProxyKeys); err != nil {
				logrus.WithContext(ctx).WithError(err).Error("failed to sync child groups after parent update")
				// Don't fail the update, just log the error
			}
		}
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	// Invalidate group list cache after updating a group
	s.invalidateGroupListCache()

	return &group, nil
}

// syncChildGroupsOnParentUpdate updates all child groups when parent group's name or proxy_keys change.
// When name changes: update child groups' upstream URL.
// When proxy_keys changes: update child groups' API keys (not proxy_keys).
func (s *GroupService) syncChildGroupsOnParentUpdate(ctx context.Context, parentGroup *models.Group, oldName string, oldProxyKeys string) error {
	// Check if there are any child groups
	var childGroups []models.Group
	if err := s.db.WithContext(ctx).
		Where("parent_group_id = ?", parentGroup.ID).
		Find(&childGroups).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if len(childGroups) == 0 {
		return nil
	}

	nameChanged := oldName != parentGroup.Name
	proxyKeysChanged := oldProxyKeys != parentGroup.ProxyKeys

	// Update upstream URL if parent name changed
	if nameChanged {
		port := utils.ParseInteger(os.Getenv("PORT"), 3001)
		upstreamURL := fmt.Sprintf("http://127.0.0.1:%d/proxy/%s", port, parentGroup.Name)
		upstreams := []map[string]interface{}{
			{
				"url":    upstreamURL,
				"weight": 1,
			},
		}
		upstreamsJSON, err := json.Marshal(upstreams)
		if err != nil {
			return app_errors.ErrInternalServer
		}

		if err := s.db.WithContext(ctx).
			Model(&models.Group{}).
			Where("parent_group_id = ?", parentGroup.ID).
			Update("upstreams", datatypes.JSON(upstreamsJSON)).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
	}

	// Update API keys if parent proxy_keys changed
	if proxyKeysChanged && s.keyService != nil {
		newParentFirstKey := getFirstProxyKeyFromString(parentGroup.ProxyKeys)
		oldParentFirstKey := getFirstProxyKeyFromString(oldProxyKeys)

		if newParentFirstKey != "" && newParentFirstKey != oldParentFirstKey {
			// For each child group, update the API key
			for _, childGroup := range childGroups {
				// Remove old key if exists
				if oldParentFirstKey != "" {
					oldKeyHash := s.encryptionSvc.Hash(oldParentFirstKey)
					s.db.WithContext(ctx).
						Where("group_id = ? AND key_hash = ?", childGroup.ID, oldKeyHash).
						Delete(&models.APIKey{})
				}

				// Add new key using KeyService
				_, err := s.keyService.AddMultipleKeys(childGroup.ID, newParentFirstKey)
				if err != nil {
					logrus.WithContext(ctx).WithError(err).WithFields(logrus.Fields{
						"childGroupID":   childGroup.ID,
						"childGroupName": childGroup.Name,
					}).Error("Failed to update API key for child group")
				}
			}
		}
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"parentGroupID":    parentGroup.ID,
		"parentGroupName":  parentGroup.Name,
		"childGroupCount":  len(childGroups),
		"nameChanged":      nameChanged,
		"proxyKeysChanged": proxyKeysChanged,
	}).Info("Synced child groups after parent update")

	return nil
}

// getFirstProxyKeyFromString extracts the first proxy key from a comma-separated list.
func getFirstProxyKeyFromString(proxyKeys string) string {
	if proxyKeys == "" {
		return ""
	}
	keys := strings.Split(proxyKeys, ",")
	if len(keys) > 0 {
		return strings.TrimSpace(keys[0])
	}
	return ""
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

	// Handle child groups - delete them along with parent
	var childGroupIDs []uint
	if err := tx.Model(&models.Group{}).Where("parent_group_id = ?", id).Pluck("id", &childGroupIDs).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	childGroupCount := int64(len(childGroupIDs))
	if childGroupCount > 0 {
		// Delete API keys for all child groups
		if err := tx.Where("group_id IN ?", childGroupIDs).Delete(&models.APIKey{}).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		// Delete child groups
		if err := tx.Where("parent_group_id = ?", id).Delete(&models.Group{}).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"parentGroupID":    id,
			"childGroupCount":  childGroupCount,
		}).Info("Deleted child groups along with parent")
	}

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
		"groupID":         id,
		"groupName":       group.Name,
		"keyCount":        keyCount,
		"childGroupCount": childGroupCount,
	}).Info("Successfully deleted group")

	return nil
}

// DeleteAllGroups removes all groups and their associated resources.
// This is a dangerous operation intended for debugging and testing purposes only.
// It should only be accessible when DEBUG_MODE environment variable is enabled.
//
// The operation performs the following steps:
// 1. Deletes all sub-group relationships
// 2. Deletes all API keys (with SQLite sequence reset)
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

	// Step 3: Begin transaction
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
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
		if s.db.Dialector.Name() == "sqlite" {
			if err := tx.Exec("DELETE FROM sqlite_sequence WHERE name='api_keys'").Error; err != nil {
				logrus.WithContext(ctx).WithError(err).Warn("failed to reset api_keys sequence, continuing anyway")
			}
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
	if s.db.Dialector.Name() == "sqlite" {
		if err := tx.Exec("DELETE FROM sqlite_sequence WHERE name='groups'").Error; err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("failed to reset groups sequence, continuing anyway")
		}
	}

	// Step 7: Commit the transaction
	if err := tx.Commit().Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to commit transaction")
		return app_errors.ErrDatabase
	}
	tx = nil

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

func (s *GroupService) GetGroupStats(ctx context.Context, groupID uint) (*GroupStats, error) {
	var group models.Group
	// Try cache first to avoid DB under heavy writes
	if cached, err := s.groupManager.GetGroupByID(groupID); err == nil && cached != nil {
		group = *cached
	} else {
		// Short DB lookup with small, configurable timeout
		qctx, cancel := context.WithTimeout(ctx, getDBLookupTimeout())
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

	queryCtx, cancel := context.WithTimeout(ctx, getDBLookupTimeout())
	defer cancel()

	if err := s.db.WithContext(queryCtx).Raw(query,
		time24hAgo, time24hAgo,
		time7dAgo, time7dAgo,
		groupID, time30dAgo, endTime,
	).Scan(&result).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logrus.WithContext(ctx).WithField("groupID", groupID).
				Warn("request stats query timed out or canceled, returning empty stats")
			return RequestStats{}, RequestStats{}, RequestStats{}, nil
		}
		return RequestStats{}, RequestStats{}, RequestStats{}, err
	}

	stats24h = calculateRequestStats(result.Success24h+result.Failure24h, result.Failure24h)
	stats7d = calculateRequestStats(result.Success7d+result.Failure7d, result.Failure7d)
	stats30d = calculateRequestStats(result.Success30d+result.Failure30d, result.Failure30d)
	return stats24h, stats7d, stats30d, nil
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
	// Key stats queries may need more time during bulk imports
	queryCtx, cancel := context.WithTimeout(ctx, 2*getDBLookupTimeout())
	defer cancel()

	// Use index-friendly COUNT queries to leverage composite indexes and avoid full table scans

	// Use two index-friendly COUNT queries instead of a single aggregation to leverage composite indexes
	var totalKeys int64
	if err := s.db.WithContext(queryCtx).Model(&models.APIKey{}).Where("group_id = ?", groupID).Count(&totalKeys).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logrus.WithContext(ctx).WithField("groupID", groupID).Warn("Key stats total count timed out or canceled, returning empty stats")
			return KeyStats{TotalKeys: 0, ActiveKeys: 0, InvalidKeys: 0}, nil
		}
		return KeyStats{}, fmt.Errorf("failed to count total keys: %w", err)
	}

	var activeKeys int64
	if err := s.db.WithContext(queryCtx).Model(&models.APIKey{}).Where("group_id = ? AND status = ?", groupID, models.KeyStatusActive).Count(&activeKeys).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logrus.WithContext(ctx).WithField("groupID", groupID).Warn("Key stats active count timed out or canceled, returning partial stats with unknown active/invalid breakdown")
			// Return partial stats with known total but unknown active/invalid breakdown
			return KeyStats{TotalKeys: totalKeys, ActiveKeys: 0, InvalidKeys: 0}, nil
		}
		return KeyStats{}, fmt.Errorf("failed to count active keys: %w", err)
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

	// Backward compatibility: migrate legacy function calling key to the new function call key.
	if legacyValue, ok := configMap["force_function_calling"]; ok {
		if _, hasNewKey := configMap["force_function_call"]; !hasNewKey {
			configMap["force_function_call"] = legacyValue
		}
		delete(configMap, "force_function_calling")
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

// normalizePathRedirects validates and normalizes path redirect rules.
// Keeps only non-empty pairs, trims spaces, and deduplicates by `from` keeping first occurrence.
func (s *GroupService) normalizePathRedirects(rules []models.PathRedirectRule) (datatypes.JSON, error) {
	if len(rules) == 0 {
		return datatypes.JSON("[]"), nil
	}

	// normalizePathForMatching applies the same canonicalization used at request time
	normalizePathForMatching := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			return ""
		}
		// Strip scheme/host if a full URL was provided
		if u, err := url.Parse(p); err == nil && u.Scheme != "" {
			p = u.Path
		}
		// Remove '/proxy/{group}/' prefix if present
		if strings.HasPrefix(p, "/proxy/") {
			rest := strings.TrimPrefix(p, "/proxy/")
			if idx := strings.IndexByte(rest, '/'); idx >= 0 {
				p = rest[idx:]
			} else {
				p = "/"
			}
		}
		// Ensure leading slash
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return p
	}

	seen := make(map[string]bool)
	normalized := make([]models.PathRedirectRule, 0, len(rules))
	for _, r := range rules {
		from := strings.TrimSpace(r.From)
		to := strings.TrimSpace(r.To)
		if from == "" || to == "" {
			continue
		}
		// Cap to reasonable length to avoid accidental huge payloads
		if len(from) > 512 || len(to) > 512 {
			continue
		}
		// Use the same normalization as runtime matching to dedupe
		key := normalizePathForMatching(from)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, models.PathRedirectRule{From: from, To: to})
	}
	if len(normalized) == 0 {
		return datatypes.JSON("[]"), nil
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "error.invalid_config_format", map[string]any{"error": err.Error()})
	}
	return datatypes.JSON(b), nil
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

// isConfigCCSupportEnabled checks whether cc_support is enabled in a config map.
// Accepted value types are: bool, numeric values (treated as enabled when non-zero), and
// string values like "true"/"1"/"yes"/"on" for backward compatibility with runtime checks.
// NOTE: This helper intentionally mirrors the runtime isCCSupportEnabled parsing logic instead
// of using a shared cross-package utility to keep validation self-contained and avoid extra
// coupling between proxy and services layers.
func isConfigCCSupportEnabled(config datatypes.JSONMap) bool {
	if config == nil {
		return false
	}

	raw, ok := config["cc_support"]
	if !ok || raw == nil {
		return false
	}

	switch v := raw.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		return lower == "true" || lower == "1" || lower == "yes" || lower == "on"
	default:
		rv := reflect.ValueOf(raw)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return rv.Int() != 0
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return rv.Uint() != 0
		case reflect.Float32, reflect.Float64:
			return rv.Float() != 0
		default:
			return false
		}
	}
}

func validateParamOverrides(overrides map[string]any) error {
	if len(overrides) == 0 {
		return nil
	}

	checkBool := func(key string) error {
		raw, ok := overrides[key]
		if !ok || raw == nil {
			return nil
		}
		if _, ok := raw.(bool); ok {
			return nil
		}
		return NewI18nError(app_errors.ErrValidation, "validation.invalid_param_overrides", map[string]any{
			"field":    key,
			"expected": "bool",
			"got":      paramOverrideTypeName(raw),
		})
	}

	checkNumber := func(key string) error {
		raw, ok := overrides[key]
		if !ok || raw == nil {
			return nil
		}
		if isNumberLike(raw) {
			return nil
		}
		return NewI18nError(app_errors.ErrValidation, "validation.invalid_param_overrides", map[string]any{
			"field":    key,
			"expected": "number",
			"got":      paramOverrideTypeName(raw),
		})
	}

	checkInteger := func(key string) error {
		raw, ok := overrides[key]
		if !ok || raw == nil {
			return nil
		}
		if isIntegerLike(raw) {
			return nil
		}
		return NewI18nError(app_errors.ErrValidation, "validation.invalid_param_overrides", map[string]any{
			"field":    key,
			"expected": "integer",
			"got":      paramOverrideTypeName(raw),
		})
	}

	checkStringOrStringArray := func(key string) error {
		raw, ok := overrides[key]
		if !ok || raw == nil {
			return nil
		}
		if _, ok := raw.(string); ok {
			return nil
		}
		if arr, ok := raw.([]any); ok {
			for _, item := range arr {
				if item == nil {
					continue
				}
				if _, ok := item.(string); !ok {
					return NewI18nError(app_errors.ErrValidation, "validation.invalid_param_overrides", map[string]any{
						"field":    key,
						"expected": "string_or_string_array",
						"got":      paramOverrideTypeName(raw),
					})
				}
			}
			return nil
		}
		return NewI18nError(app_errors.ErrValidation, "validation.invalid_param_overrides", map[string]any{
			"field":    key,
			"expected": "string_or_string_array",
			"got":      paramOverrideTypeName(raw),
		})
	}

	// Boolean
	if err := checkBool("stream"); err != nil {
		return err
	}

	// Numbers (float)
	for _, k := range []string{"temperature", "top_p", "presence_penalty", "frequency_penalty"} {
		if err := checkNumber(k); err != nil {
			return err
		}
	}

	// Integers
	for _, k := range []string{"max_tokens", "max_tokens_to_sample", "max_output_tokens", "n", "seed"} {
		if err := checkInteger(k); err != nil {
			return err
		}
	}

	// String or []string
	if err := checkStringOrStringArray("stop"); err != nil {
		return err
	}

	return nil
}

func isNumberLike(v any) bool {
	if v == nil {
		return false
	}
	if _, ok := v.(float64); ok {
		return true
	}
	if _, ok := v.(float32); ok {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	case reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func isIntegerLike(v any) bool {
	if v == nil {
		return false
	}
	if f, ok := v.(float64); ok {
		return f == math.Trunc(f)
	}
	if f, ok := v.(float32); ok {
		return float64(f) == math.Trunc(float64(f))
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

func paramOverrideTypeName(v any) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case bool:
		return "bool"
	case string:
		return "string"
	case float64, float32:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
			reflect.Float32, reflect.Float64:
			return "number"
		case reflect.Slice, reflect.Array:
			return "array"
		case reflect.Map, reflect.Struct:
			return "object"
		default:
			return rv.Kind().String()
		}
	}
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

// convertToJSONMap converts a map[string]string to datatypes.JSONMap
func convertToJSONMap(input map[string]string) datatypes.JSONMap {
	if len(input) == 0 {
		return datatypes.JSONMap{}
	}

	result := make(datatypes.JSONMap)
	for k, v := range input {
		trimmedKey := strings.TrimSpace(k)
		trimmedValue := strings.TrimSpace(v)
		if trimmedKey == "" || trimmedValue == "" {
			continue
		}
		result[trimmedKey] = trimmedValue
	}
	return result
}

// validateModelRedirectRules validates the format and content of model redirect rules
func validateModelRedirectRules(rules map[string]string) error {
	if len(rules) == 0 {
		return nil
	}

	for key, value := range rules {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("model name cannot be empty")
		}
	}

	return nil
}

// FetchGroupModels fetches available models from the upstream service for a specific group
// It considers proxy settings and channel-specific API requirements
func (s *GroupService) FetchGroupModels(ctx context.Context, groupID uint) (map[string]any, error) {
	logrus.WithContext(ctx).WithField("group_id", groupID).Debug("Starting to fetch models for group")

	// Prefer cached group with effective config and parsed path/model redirect rules
	var group *models.Group
	if s.groupManager != nil {
		if cached, err := s.groupManager.GetGroupByID(groupID); err == nil {
			group = cached
		} else {
			logrus.WithContext(ctx).WithError(err).Warn("Failed to load group from cache for model fetch, falling back to database")
		}
	}

	if group == nil {
		// Fallback: load from database for compatibility
		var dbGroup models.Group
		if err := s.db.WithContext(ctx).First(&dbGroup, groupID).Error; err != nil {
			logrus.WithContext(ctx).WithError(err).Error("Failed to fetch group from database")
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, app_errors.ErrResourceNotFound
			}
			return nil, app_errors.ParseDBError(err)
		}
		group = &dbGroup
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"group_id":     group.ID,
		"group_name":   group.Name,
		"group_type":   group.GroupType,
		"channel_type": group.ChannelType,
	}).Debug("Group loaded successfully")

	// Only standard groups can fetch models
	if group.GroupType == "aggregate" {
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "cannot fetch models for aggregate group")
	}

	// Get channel proxy to reuse upstream selection, proxy configuration and path redirect rules
	channelProxy, err := s.channelFactory.GetChannel(group)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to get channel proxy")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to initialize channel proxy")
	}

	// Construct base models endpoint path based on channel type (before path redirects)
	var modelsPath string
	switch group.ChannelType {
	case "openai":
		modelsPath = "/v1/models"
	case "gemini":
		modelsPath = "/v1beta/models"
	case "anthropic":
		modelsPath = "/v1/models"
	default:
		modelsPath = "/v1/models"
	}

	logrus.WithContext(ctx).WithField("models_path", modelsPath).Debug("Determined models endpoint path")

	// Build a synthetic proxy URL so we can reuse SelectUpstreamWithClients
	// This ensures path redirects (e.g. /v1 -> /api/paas/v4) and per-upstream proxies are applied consistently.
	proxyURL := &url.URL{Path: "/proxy/" + group.Name + modelsPath}
	selection, err := channelProxy.SelectUpstreamWithClients(proxyURL, group.Name)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to select upstream for model list fetch")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to select upstream for models endpoint")
	}
	if selection == nil || selection.URL == "" {
		logrus.WithContext(ctx).Error("SelectUpstreamWithClients returned empty result for model list fetch")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "no active upstreams available for models endpoint")
	}

	logrus.WithContext(ctx).WithField("full_url", selection.URL).Debug("Built full request URL via channel proxy")

	// Select an active key from cache (Redis/Memory) using existing key rotation logic
	// This reuses the in-memory key pool loaded at startup, avoiding database queries
	selectedKey, err := s.keyProvider.SelectKey(group.ID)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to select API key from cache")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "no active API keys available in this group")
	}

	// Mask key for secure logging (avoid leaking short keys in logs)
	maskedKey := selectedKey.KeyValue
	if len(maskedKey) > 16 {
		// Typical case: show first 8 and last 4 characters
		maskedKey = maskedKey[:8] + "****" + maskedKey[len(maskedKey)-4:]
	} else if len(maskedKey) > 8 {
		// Shorter keys: only show the first 4 characters
		maskedKey = maskedKey[:4] + "****"
	} else {
		// Very short keys: fully mask
		maskedKey = "****"
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"key_id":     selectedKey.ID,
		"key_status": selectedKey.Status,
		"masked_key": maskedKey,
	}).Debug("Selected API key from cache for upstream request")

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", selection.URL, nil)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to create HTTP request")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to create HTTP request")
	}

	// Apply channel-specific authentication headers using existing channel implementation
	// This delegates to OpenAIChannel, GeminiChannel, or AnthropicChannel ModifyRequest methods
	channelProxy.ModifyRequest(req, selectedKey, group)

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"channel_type": group.ChannelType,
		"masked_key":   maskedKey,
	}).Debug("Applied channel-specific authentication via ChannelProxy.ModifyRequest")

	// Apply custom header rules for this group to the models request
	if len(group.HeaderRuleList) > 0 {
		headerCtx := utils.NewHeaderVariableContext(group, selectedKey)
		utils.ApplyHeaderRules(req, group.HeaderRuleList, headerCtx)
	}

	// Apply parameter overrides as query parameters for the models request
	if len(group.ParamOverrides) > 0 {
		clonedURL := *req.URL
		query := clonedURL.Query()
		for key, value := range group.ParamOverrides {
			if strings.TrimSpace(key) == "" {
				continue
			}
			if value == nil {
				// Treat explicit null as no-op to avoid sending "<nil>" as query value
				continue
			}
			// ParamOverrides intentionally overrides any existing query parameter with the same key
			query.Set(key, fmt.Sprint(value))
		}
		clonedURL.RawQuery = query.Encode()
		req.URL = &clonedURL
	}

	// Use the upstream-specific HTTP client configured by the channel (includes proxy and timeouts)
	httpClient := selection.HTTPClient
	if httpClient == nil {
		// Defensive fallback: use channel-level client or a minimal default client
		httpClient = channelProxy.GetHTTPClient()
		if httpClient == nil {
			httpClient = &http.Client{Timeout: 30 * time.Second}
		}
	}

	logrus.WithContext(ctx).Debug("Sending HTTP request to upstream")

	// Execute HTTP request
	resp, err := httpClient.Do(req)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to execute HTTP request")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to connect to upstream server: network error")
	}
	defer resp.Body.Close()

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"status_code":    resp.StatusCode,
		"content_type":   resp.Header.Get("Content-Type"),
		"content_length": resp.Header.Get("Content-Length"),
	}).Debug("Received HTTP response")

	// Check response status and provide user-friendly error messages
	if resp.StatusCode != http.StatusOK {
		// Read error response body for better diagnostics
		// Limit error response body size to avoid excessive memory usage on misconfigured upstreams.
		// Note: we only use a short preview for logging, so truncation is acceptable here.
		const maxErrorBodySize = 1 * 1024 * 1024 // 1MB limit for error body preview
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		errorPreview := string(errorBody)
		if len(errorPreview) > 200 {
			errorPreview = errorPreview[:200] + "..."
		}

		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"status_code":   resp.StatusCode,
			"error_body":    errorPreview,
			"content_type":  resp.Header.Get("Content-Type"),
		}).Error("Upstream returned non-OK status")

		// Provide specific error messages based on status code.
		// Note: we intentionally map upstream HTTP errors to ErrBadRequest in this admin API.
		// More granular error kinds (e.g. ErrUpstreamError, ErrUnauthorized) were suggested by AI review,
		// but are not adopted here to avoid changing existing client error handling semantics.
		switch resp.StatusCode {
		case http.StatusBadRequest: // 400
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("bad request: %s", errorPreview))
		case http.StatusUnauthorized: // 401
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "authentication failed: invalid or expired API key")
		case http.StatusForbidden: // 403
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "access forbidden: insufficient permissions")
		case http.StatusNotFound: // 404
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "models endpoint not found on upstream server")
		case http.StatusTooManyRequests: // 429
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "rate limit exceeded on upstream server")
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable: // 500, 502, 503
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "upstream server error, please try again later")
		default:
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("upstream returned error status: %d", resp.StatusCode))
		}
	}

	// Read response body with size limit to prevent memory exhaustion on large model lists
	const maxModelListBodySize = 10 * 1024 * 1024 // 10MB limit for model list responses
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBodySize))
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to read response body")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to read upstream response")
	}

	// Decompress response body if needed (gzip/deflate), mirroring proxy model list handling
	contentEncoding := resp.Header.Get("Content-Encoding")
	decompressed, err := utils.DecompressResponse(contentEncoding, bodyBytes)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("Failed to decompress response body, using raw bytes")
		decompressed = bodyBytes
	}

	// Log first 500 chars of (decompressed) response for debugging
	bodyPreview := string(decompressed)
	if len(bodyPreview) > 500 {
		bodyPreview = bodyPreview[:500] + "..."
	}
	logrus.WithContext(ctx).WithField("body_preview", bodyPreview).Debug("Response body preview")

	// Parse upstream model list response directly without applying redirect rules.
	// This is intentional: FetchGroupModels is used by the "Fetch Models" button in the
	// model redirect configuration UI, which should only show upstream models.
	// Redirect rules are applied separately when external clients access the model list API.
	var result map[string]any
	if err := json.Unmarshal(decompressed, &result); err != nil {
		// Show first 100 chars of body for error diagnosis
		bodyStart := bodyPreview
		if len(bodyStart) > 100 {
			bodyStart = bodyStart[:100]
		}
		logrus.WithContext(ctx).WithError(err).WithField("body_start", bodyStart).Error("Failed to parse upstream model list response")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to parse upstream response: invalid JSON format")
	}

	// Log model count for debugging
	modelCount := 0
	if data, ok := result["data"].([]any); ok {
		modelCount = len(data)
	} else if models, ok := result["models"].([]any); ok {
		modelCount = len(models)
	}
	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"result_keys":  getMapKeys(result),
		"model_count":  modelCount,
		"upstream_only": true,
	}).Debug("Successfully decoded upstream model list response (redirect rules not applied)")

	return result, nil
}

// getMapKeys returns the keys of a map for logging purposes
func getMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
