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
	"sync/atomic"
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

// ReadOnlyDB is a wrapper type for the read-only database connection.
// This allows dig to distinguish between the main DB and the read-only DB.
// For SQLite in WAL mode, this enables concurrent reads during writes.
type ReadOnlyDB struct {
	DB *gorm.DB
}

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

// groupKeyStatsCacheEntry represents a cached key statistics entry with adaptive TTL support.
// HitCount and CurrentTTL enable adaptive TTL extension for frequently accessed entries.
type groupKeyStatsCacheEntry struct {
	Stats      KeyStats
	ExpiresAt  time.Time
	HitCount   int64         // Number of cache hits since last TTL extension
	CurrentTTL time.Duration // Current TTL (may be extended based on access patterns)
}

// groupListCacheEntry represents a cached groups list with adaptive TTL support.
type groupListCacheEntry struct {
	Groups     []models.Group
	ExpiresAt  time.Time
	HitCount   int64         // Number of cache hits since last TTL extension
	CurrentTTL time.Duration // Current TTL (may be extended based on access patterns)
}

// GroupService handles business logic for group operations.
type GroupService struct {
	db                    *gorm.DB
	readDB                *gorm.DB // Separate read connection for SQLite WAL mode
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
	keyStatsCacheMaxTTL   time.Duration // Maximum TTL for adaptive extension
	groupListCache        *groupListCacheEntry
	groupListCacheMu      sync.RWMutex
	groupListCacheTTL     time.Duration
	groupListCacheMaxTTL  time.Duration // Maximum TTL for adaptive extension

	// Callbacks for binding operations (set by handler layer to avoid circular dependency)
	CheckGroupCanDeleteCallback func(ctx context.Context, groupID uint) error
	// Note: SyncGroupEnabledToSiteCallback removed - group disable does NOT cascade to site
	SyncChildGroupsEnabledCallback      func(ctx context.Context, parentGroupID uint, enabled bool) error // Sync enabled status to child groups
	InvalidateChildGroupsCacheCallback  func()                                                            // Invalidate child groups cache after updating a child group
	OnGroupDeleted                      func(groupID uint, isAggregateGroup bool)                         // Soft-delete health metrics when group is deleted
	InvalidateHubModelPoolCacheCallback func()                                                            // Invalidate Hub model pool cache when groups change
}

// NewGroupService constructs a GroupService.
func NewGroupService(
	db *gorm.DB,
	readDB ReadOnlyDB,
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
	// Use main DB as readDB if not provided (for MySQL/PostgreSQL)
	rdb := readDB.DB
	if rdb == nil {
		rdb = db
	}
	svc := &GroupService{
		db:                    db,
		readDB:                rdb,
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
		keyStatsCacheTTL:      30 * time.Second, // Base TTL for key stats cache
		keyStatsCacheMaxTTL:   2 * time.Minute,  // Max TTL after adaptive extension
		groupListCacheTTL:     30 * time.Second, // Base TTL for group list cache
		groupListCacheMaxTTL:  2 * time.Minute,  // Max TTL after adaptive extension
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
	Name                 string
	DisplayName          string
	Description          string
	GroupType            string
	Upstreams            json.RawMessage
	ChannelType          string
	Sort                 int
	TestModel            string
	ValidationEndpoint   string
	ParamOverrides       map[string]any
	Config               map[string]any
	HeaderRules          []models.HeaderRule
	ModelMapping         string // Deprecated: for backward compatibility
	ModelRedirectRules   map[string]string
	ModelRedirectRulesV2 json.RawMessage // V2: one-to-many mapping
	ModelRedirectStrict  bool
	PathRedirects        []models.PathRedirectRule
	ProxyKeys            string
	SubGroups            []SubGroupInput
}

// GroupUpdateParams captures updatable fields for a group.
type GroupUpdateParams struct {
	Name                 *string
	DisplayName          *string
	Description          *string
	GroupType            *string
	Upstreams            json.RawMessage
	HasUpstreams         bool
	ChannelType          *string
	Sort                 *int
	TestModel            string
	HasTestModel         bool
	ValidationEndpoint   *string
	ParamOverrides       map[string]any
	Config               map[string]any
	HeaderRules          *[]models.HeaderRule
	ModelMapping         *string // Deprecated: for backward compatibility
	ModelRedirectRules   map[string]string
	ModelRedirectRulesV2 json.RawMessage // V2: one-to-many mapping
	ModelRedirectStrict  *bool
	PathRedirects        []models.PathRedirectRule
	ProxyKeys            *string
	SubGroups            *[]SubGroupInput
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

	// Handle V1 to V2 migration: merge V1 rules into V2, then clear V1
	finalV2Rules, finalV1Rules := s.normalizeModelRedirectRules(modelRedirectRules, params.ModelRedirectRulesV2)

	group := models.Group{
		Name:                 name,
		DisplayName:          strings.TrimSpace(params.DisplayName),
		Description:          strings.TrimSpace(params.Description),
		GroupType:            groupType,
		Upstreams:            cleanedUpstreams,
		ChannelType:          channelType,
		Sort:                 params.Sort,
		TestModel:            testModel,
		ValidationEndpoint:   validationEndpoint,
		ParamOverrides:       params.ParamOverrides,
		Config:               cleanedConfig,
		HeaderRules:          headerRulesJSON,
		ModelMapping:         modelMapping, // Keep for backward compatibility
		ModelRedirectRules:   finalV1Rules,
		ModelRedirectRulesV2: finalV2Rules,
		ModelRedirectStrict:  params.ModelRedirectStrict,
		CustomModelNames:     datatypes.JSON("[]"), // Initialize to empty array
		PathRedirects:        pathRedirectsJSON,
		ProxyKeys:            strings.TrimSpace(params.ProxyKeys),
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

// invalidateGroupListCache clears the group list cache and notifies Hub service
func (s *GroupService) invalidateGroupListCache() {
	s.groupListCacheMu.Lock()
	s.groupListCache = nil
	s.groupListCacheMu.Unlock()
	logrus.Debug("Group list cache invalidated")

	// Invalidate Hub model pool cache when groups change
	if s.InvalidateHubModelPoolCacheCallback != nil {
		s.InvalidateHubModelPoolCacheCallback()
	}
}

// InvalidateGroupListCache exposes group list cache invalidation for other packages (e.g., handlers)
func (s *GroupService) InvalidateGroupListCache() {
	s.invalidateGroupListCache()
}

// addGroupToListCache adds a new group to the cache without invalidating it.
// This is used after creating a new group to keep the cache valid during async operations.
// If cache is empty, it tries to load all groups from DB first using readDB.
//
// Lock contention optimization: The mutex is released before DB queries to avoid blocking
// other goroutines during potentially slow operations (up to 2 seconds timeout).
func (s *GroupService) addGroupToListCache(group *models.Group) {
	s.groupListCacheMu.Lock()
	needsDBLoad := s.groupListCache == nil || len(s.groupListCache.Groups) == 0
	if !needsDBLoad {
		// Fast path: cache exists, just append and return
		s.groupListCache.Groups = append(s.groupListCache.Groups, *group)
		s.groupListCache.ExpiresAt = time.Now().Add(s.groupListCache.CurrentTTL)
		s.groupListCacheMu.Unlock()
		return
	}
	s.groupListCacheMu.Unlock()

	// Slow path: need to load from DB
	// Release lock before DB query to reduce lock contention
	var groups []models.Group
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.readDB.WithContext(ctx).Order(GroupListOrderClause).Find(&groups).Error; err != nil {
		// DB query failed, create cache with just this group
		logrus.WithError(err).Debug("Failed to load groups for cache, using single group")
		s.groupListCacheMu.Lock()
		s.groupListCache = &groupListCacheEntry{
			Groups:     []models.Group{*group},
			ExpiresAt:  time.Now().Add(s.groupListCacheTTL),
			HitCount:   0,
			CurrentTTL: s.groupListCacheTTL,
		}
		s.groupListCacheMu.Unlock()
		return
	}

	// Check if the new group is already in the list (shouldn't happen but be safe)
	found := false
	for _, g := range groups {
		if g.ID == group.ID {
			found = true
			break
		}
	}
	if !found {
		groups = append(groups, *group)
	}

	s.groupListCacheMu.Lock()
	// Re-check: another goroutine may have populated the cache while we queried DB
	// This prevents race condition where concurrent calls could lose cached groups
	if s.groupListCache != nil && len(s.groupListCache.Groups) > 0 {
		// Merge: check if our group is already there, if not append
		alreadyCached := false
		for _, g := range s.groupListCache.Groups {
			if g.ID == group.ID {
				alreadyCached = true
				break
			}
		}
		if !alreadyCached {
			s.groupListCache.Groups = append(s.groupListCache.Groups, *group)
			s.groupListCache.ExpiresAt = time.Now().Add(s.groupListCache.CurrentTTL)
		}
		s.groupListCacheMu.Unlock()
		return
	}
	s.groupListCache = &groupListCacheEntry{
		Groups:     groups,
		ExpiresAt:  time.Now().Add(s.groupListCacheTTL),
		HitCount:   0,
		CurrentTTL: s.groupListCacheTTL,
	}
	s.groupListCacheMu.Unlock()
}

// isTaskRunning checks if an import or delete task is currently running.
func (s *GroupService) isTaskRunning() bool {
	if s.keyImportSvc == nil || s.keyImportSvc.TaskService == nil {
		return false
	}
	status, err := s.keyImportSvc.TaskService.GetTaskStatus()
	if err != nil {
		return false
	}
	return status.IsRunning
}

// ListGroups returns all groups without sub-group relations.
func (s *GroupService) ListGroups(ctx context.Context) ([]models.Group, error) {
	// Check cache first with adaptive TTL support
	s.groupListCacheMu.RLock()
	if s.groupListCache != nil && time.Now().Before(s.groupListCache.ExpiresAt) {
		// Cache hit, return cached groups
		groups := make([]models.Group, len(s.groupListCache.Groups))
		copy(groups, s.groupListCache.Groups)
		s.groupListCacheMu.RUnlock()

		// Update hit count asynchronously to avoid blocking the read path
		go s.updateGroupListCacheHit()

		logrus.WithContext(ctx).Debug("Group list cache hit")
		return groups, nil
	}
	// Check if we have stale cache and a task is running
	// If so, return stale cache to avoid blocking on DB during import/delete
	hasStaleCache := s.groupListCache != nil && len(s.groupListCache.Groups) > 0
	s.groupListCacheMu.RUnlock()

	if hasStaleCache && s.isTaskRunning() {
		s.groupListCacheMu.RLock()
		stale := make([]models.Group, len(s.groupListCache.Groups))
		copy(stale, s.groupListCache.Groups)
		s.groupListCacheMu.RUnlock()
		logrus.WithContext(ctx).Debug("ListGroups returning stale cache during task execution")
		return stale, nil
	}

	// Cache miss, fetch from database with timeout for reliability
	// Group list queries should be fast with proper indexes
	// Use readDB for read operations to avoid blocking during writes (SQLite WAL mode)
	groups := make([]models.Group, 0, 100)

	queryCtx, cancel := context.WithTimeout(ctx, getDBLookupTimeout())
	defer cancel()

	if err := s.readDB.WithContext(queryCtx).Order(GroupListOrderClause).Find(&groups).Error; err != nil {
		// Use stale cache for transient errors (timeout/canceled/locked) to keep UI responsive
		// For other errors (schema issues, query bugs), return the error immediately
		if isTransientDBError(err) {
			s.groupListCacheMu.RLock()
			// Return stale cache even if expired - better than returning error
			if s.groupListCache != nil && len(s.groupListCache.Groups) > 0 {
				stale := make([]models.Group, len(s.groupListCache.Groups))
				copy(stale, s.groupListCache.Groups)
				s.groupListCacheMu.RUnlock()
				logrus.WithContext(ctx).WithError(err).Warn("ListGroups transient error - returning stale cache")
				return stale, nil
			}
			s.groupListCacheMu.RUnlock()
		}
		return nil, app_errors.ParseDBError(err)
	}

	// Update cache with base TTL
	s.groupListCacheMu.Lock()
	s.groupListCache = &groupListCacheEntry{
		Groups:     groups,
		ExpiresAt:  time.Now().Add(s.groupListCacheTTL),
		HitCount:   0,
		CurrentTTL: s.groupListCacheTTL,
	}
	s.groupListCacheMu.Unlock()

	logrus.WithContext(ctx).Debug("Group list cache updated")
	return groups, nil
}

// updateGroupListCacheHit updates hit statistics and extends TTL for frequently accessed group list.
// This implements adaptive TTL: entries with high hit counts get extended TTL up to maxTTL.
func (s *GroupService) updateGroupListCacheHit() {
	s.groupListCacheMu.Lock()
	defer s.groupListCacheMu.Unlock()

	if s.groupListCache == nil || time.Now().After(s.groupListCache.ExpiresAt) {
		return
	}

	s.groupListCache.HitCount++

	// Extend TTL if hit threshold is reached (10 hits) and not at max TTL
	// This rewards frequently accessed entries with longer cache lifetime
	const hitThreshold = 10
	const ttlMultiplier = 1.2

	if s.groupListCache.HitCount >= hitThreshold && s.groupListCache.CurrentTTL < s.groupListCacheMaxTTL {
		newTTL := time.Duration(float64(s.groupListCache.CurrentTTL) * ttlMultiplier)
		if newTTL > s.groupListCacheMaxTTL {
			newTTL = s.groupListCacheMaxTTL
		}
		s.groupListCache.CurrentTTL = newTTL
		s.groupListCache.ExpiresAt = time.Now().Add(newTTL)
		s.groupListCache.HitCount = 0 // Reset hit count after extension
	}
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

	// Validate model redirect rules format (V1)
	if params.ModelRedirectRules != nil {
		if err := validateModelRedirectRules(params.ModelRedirectRules); err != nil {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_model_redirect", map[string]any{"error": err.Error()})
		}
	}

	// Handle V1 to V2 migration: merge V1 rules into V2, then clear V1
	// This applies when either V1 or V2 rules are provided in params
	if params.ModelRedirectRules != nil || params.ModelRedirectRulesV2 != nil {
		// Get current V1 rules from params or existing group
		v1Rules := params.ModelRedirectRules
		if v1Rules == nil {
			// Convert existing group's V1 rules to map[string]string
			v1Rules = make(map[string]string)
			for k, v := range group.ModelRedirectRules {
				if str, ok := v.(string); ok {
					v1Rules[k] = str
				} else {
					// Skip non-string V1 rules during migration (should not happen in normal cases)
					logrus.WithFields(logrus.Fields{
						"group_id":   group.ID,
						"rule_key":   k,
						"value_type": fmt.Sprintf("%T", v),
					}).Debug("Skipping non-string V1 redirect rule during migration")
				}
			}
		}

		// Get V2 rules from params (may be nil)
		v2RulesJSON := params.ModelRedirectRulesV2

		finalV2Rules, finalV1Rules := s.normalizeModelRedirectRules(v1Rules, v2RulesJSON)
		group.ModelRedirectRulesV2 = finalV2Rules
		group.ModelRedirectRules = finalV1Rules
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

		// Check if cc_support is being disabled for OpenAI/Codex/Gemini groups before performing any database write.
		// If so, verify that this group is not used as a sub-group in any Anthropic aggregate groups.
		// NOTE: This guard is best-effort and not wrapped in an explicit transaction. There is a small
		// time-of-check-to-time-of-use window where aggregate membership can change concurrently, but
		// we intentionally keep lock time minimal (especially for SQLite). Any misconfiguration will
		// surface quickly via failing aggregate requests and can be corrected via configuration.
		if (group.ChannelType == "openai" || group.ChannelType == "codex" || group.ChannelType == "gemini") && group.GroupType != "aggregate" {
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

	// Invalidate child groups cache if this is a child group update.
	// This ensures GetAllChildGroups returns fresh data with updated display_name.
	if group.ParentGroupID != nil && s.InvalidateChildGroupsCacheCallback != nil {
		s.InvalidateChildGroupsCacheCallback()
	}

	return &group, nil
}

// syncChildGroupsOnParentUpdate updates all child groups when parent group's name or proxy_keys change.
// When name changes: update child groups' upstream URL.
// When proxy_keys changes: update child groups' API keys (not proxy_keys).
//
// NOTE: Similar logic exists in ChildGroupService.SyncChildGroupsOnParentUpdate. The duplication is
// intentional to avoid circular dependencies between services. This method is used inline during
// group updates, while ChildGroupService's method is exposed for external callers with transaction support.
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
			// Add new key FIRST, then delete old key to avoid leaving child without any key
			for _, childGroup := range childGroups {
				// Add new key first to ensure child group always has a working key
				if _, err := s.keyService.AddMultipleKeys(childGroup.ID, newParentFirstKey); err != nil {
					logrus.WithContext(ctx).WithError(err).WithFields(logrus.Fields{
						"childGroupID":   childGroup.ID,
						"childGroupName": childGroup.Name,
					}).Error("Failed to add new API key for child group, keeping old key")
					continue // Keep old key if new key addition fails
				}

				// Now safe to delete old key
				if oldParentFirstKey != "" {
					oldKeyHash := s.encryptionSvc.Hash(oldParentFirstKey)
					if err := s.db.WithContext(ctx).
						Where("group_id = ? AND key_hash = ?", childGroup.ID, oldKeyHash).
						Delete(&models.APIKey{}).Error; err != nil {
						logrus.WithContext(ctx).WithError(err).WithFields(logrus.Fields{
							"childGroupID": childGroup.ID,
							"operation":    "delete_old_key",
						}).Warn("Failed to delete old API key for child group")
					}
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
// NOTE: This is intentionally duplicated from child_group_service.go's getFirstProxyKey
// to avoid cross-service dependencies. Both implementations are simple and unlikely to diverge.
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
// All associated API keys are deleted synchronously within the same transaction
// to ensure foreign key constraints are satisfied.
func (s *GroupService) DeleteGroup(ctx context.Context, id uint) (retErr error) {
	// Check if group is bound to a site (must unbind first)
	if s.CheckGroupCanDeleteCallback != nil {
		if err := s.CheckGroupCanDeleteCallback(ctx, id); err != nil {
			return err
		}
	}

	// Note: We do NOT start a global task here for sync deletion to avoid degrading
	// reads for other groups. Global task is only started for async deletion (>20K keys).

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
			tx = nil
			return nil
		}
		return app_errors.ParseDBError(err)
	}

	// Handle child groups - get their IDs first
	var childGroupIDs []uint
	if err := tx.Model(&models.Group{}).Where("parent_group_id = ?", id).Pluck("id", &childGroupIDs).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	childGroupCount := int64(len(childGroupIDs))
	relatedGroupIDs := make([]uint, 0, len(childGroupIDs)+1)
	relatedGroupIDs = append(relatedGroupIDs, id)
	if len(childGroupIDs) > 0 {
		relatedGroupIDs = append(relatedGroupIDs, childGroupIDs...)
	}

	// Count keys for all related groups to decide sync vs async deletion
	var totalKeyCount int64
	if err := tx.Model(&models.APIKey{}).Where("group_id IN ?", relatedGroupIDs).Count(&totalKeyCount).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("failed to count API keys for group delete")
		totalKeyCount = 0
	}

	// ============================================================================
	// IMPORTANT: Check if any managed sites are bound to this group or its children.
	// Groups with bound sites MUST NOT be deleted - user must manually unbind first.
	// This prevents accidental deletion of groups that are actively used by site management.
	// DO NOT change this to auto-unbind without careful consideration of the implications.
	// ============================================================================
	var boundSiteCount int64
	if err := tx.Table("managed_sites").Where("bound_group_id IN ?", relatedGroupIDs).Count(&boundSiteCount).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("failed to check bound managed sites")
		// If we can't verify, be safe and block deletion
		return &app_errors.APIError{
			HTTPStatus: http.StatusInternalServerError,
			Code:       "CHECK_BOUND_SITES_FAILED",
			Message:    "Failed to check if group has bound sites. Please try again.",
		}
	}
	if boundSiteCount > 0 {
		return &app_errors.APIError{
			HTTPStatus: http.StatusConflict,
			Code:       "GROUP_HAS_BOUND_SITES",
			Message:    fmt.Sprintf("Cannot delete group: %d managed site(s) are bound to this group. Please unbind them first.", boundSiteCount),
		}
	}

	// Delete sub-group relationships for this group and its child groups.
	// This avoids orphaned rows and prevents potential foreign key constraint violations.
	if err := tx.Where("group_id IN ? OR sub_group_id IN ?", relatedGroupIDs, relatedGroupIDs).
		Delete(&models.GroupSubGroup{}).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	// Delete hourly stats for this group and its child groups.
	// GroupHourlyStat.GroupID may have implicit foreign key constraint via GORM naming convention.
	if err := tx.Where("group_id IN ?", relatedGroupIDs).Delete(&models.GroupHourlyStat{}).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("failed to delete group hourly stats")
		// Continue anyway - stats cleanup is best-effort
	}

	// Multi-threshold strategy for key deletion based on best practices:
	// - 0-5,000 keys: Fast sync delete in transaction (<10s)
	// - 5,000-20,000 keys: Sync chunked delete using RemoveAllKeys (10-60s)
	// - >20,000 keys: Async task (return task_id, delete keys then group in background)

	if totalKeyCount > int64(OptimizedSyncThreshold) {
		// Large key count - use async task to avoid HTTP timeout
		// Rollback transaction and start async deletion
		tx.Rollback()
		tx = nil

		// Start async group deletion task
		if s.keyDeleteSvc == nil || s.keyDeleteSvc.TaskService == nil {
			return &app_errors.APIError{
				HTTPStatus: http.StatusServiceUnavailable,
				Code:       "TASK_SERVICE_UNAVAILABLE",
				Message:    "Cannot delete group with large key count: task service unavailable",
			}
		}

		// Start async task - will delete keys first, then group
		_, err := s.StartDeleteGroupTask(ctx, id, relatedGroupIDs, int(totalKeyCount))
		if err != nil {
			return err
		}

		// Return special error with task_id for handler to convert to 202 Accepted
		return &app_errors.APIError{
			HTTPStatus: http.StatusAccepted,
			Code:       "GROUP_DELETE_ASYNC",
			Message:    fmt.Sprintf("Group deletion started in background. %d keys will be deleted first. Note: This group's keys may not be visible during deletion. You can check progress in the task status.", totalKeyCount),
		}
	}

	if totalKeyCount > int64(BulkSyncThreshold) {
		// Medium key count (5K-20K) - commit transaction first, then sync chunked delete
		// This avoids long-running transaction locks while staying within HTTP timeout
		//
		// Note: There is a theoretical race condition where new keys could be inserted
		// between transaction commit and group deletion. However, this window is very small
		// (milliseconds) and any orphaned keys would be cleaned up in subsequent operations.
		// Adding a "deleting" status flag would add complexity with minimal practical benefit,
		// as verified by our test suite.

		// Delete hourly stats before committing transaction
		if err := tx.Where("group_id IN ?", relatedGroupIDs).Delete(&models.GroupHourlyStat{}).Error; err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("Failed to delete group hourly stats")
			// Continue anyway - stats cleanup is best-effort
		}

		if err := tx.Commit().Error; err != nil {
			return app_errors.ErrDatabase
		}
		tx = nil

		// Use RemoveAllKeys for efficient chunked deletion with progress tracking
		// This reuses the optimized batch deletion logic (1000 per batch, 5ms delay)
		for _, groupID := range relatedGroupIDs {
			deleted, err := s.keyProvider.RemoveAllKeys(ctx, groupID, nil)
			if err != nil {
				logrus.WithContext(ctx).WithError(err).WithField("groupID", groupID).Error("Failed to delete keys for group")
				return &app_errors.APIError{
					HTTPStatus: http.StatusInternalServerError,
					Code:       "KEY_DELETE_FAILED",
					Message:    fmt.Sprintf("Failed to delete keys for group %d: %v", groupID, err),
				}
			}
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupID": groupID,
				"deleted": deleted,
			}).Info("Deleted keys for group")
		}

		// Start new transaction to delete groups
		tx = s.db.WithContext(ctx).Begin()
		if err := tx.Error; err != nil {
			return app_errors.ErrDatabase
		}
		defer func() {
			if tx != nil {
				tx.Rollback()
			}
		}()

		// Re-fetch group for logging (may have been modified)
		if err := tx.First(&group, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				tx.Rollback()
				tx = nil
				return nil
			}
			return app_errors.ParseDBError(err)
		}

		// Delete sub-group relationships again (in case they were added during key deletion)
		if err := tx.Where("group_id IN ? OR sub_group_id IN ?", relatedGroupIDs, relatedGroupIDs).
			Delete(&models.GroupSubGroup{}).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
	} else {
		// Small key count (<5K) - fast sync delete within transaction
		// Delete hourly stats first
		if err := tx.Where("group_id IN ?", relatedGroupIDs).Delete(&models.GroupHourlyStat{}).Error; err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("Failed to delete group hourly stats")
			// Continue anyway - stats cleanup is best-effort
		}

		// Use chunked deletion to avoid long-running single DELETE statement
		// Note: Use subquery approach for PostgreSQL compatibility (DELETE...LIMIT not supported)
		const deleteChunkSize = 1000
		if totalKeyCount > int64(deleteChunkSize) {
			// Chunked deletion for moderate key counts using subquery for cross-DB compatibility
			for {
				// Use subquery to select IDs, then delete by ID (works on all databases)
				var ids []uint
				if err := tx.Model(&models.APIKey{}).
					Where("group_id IN ?", relatedGroupIDs).
					Limit(deleteChunkSize).
					Pluck("id", &ids).Error; err != nil {
					return app_errors.ParseDBError(err)
				}
				if len(ids) == 0 {
					break
				}
				if err := tx.Where("id IN ?", ids).Delete(&models.APIKey{}).Error; err != nil {
					return app_errors.ParseDBError(err)
				}
			}
		} else {
			// Direct deletion for small key counts
			if err := tx.Where("group_id IN ?", relatedGroupIDs).Delete(&models.APIKey{}).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
		}
	}

	if childGroupCount > 0 {
		// Delete child groups
		if err := tx.Where("parent_group_id = ?", id).Delete(&models.Group{}).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"parentGroupID":   id,
			"childGroupCount": childGroupCount,
		}).Info("Deleted child groups along with parent")
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

	// Post-commit cleanup: clear store cache asynchronously
	go func(keyService *KeyService, groupIDs []uint) {
		if keyService == nil || keyService.KeyProvider == nil {
			return
		}
		for _, groupID := range groupIDs {
			_ = keyService.KeyProvider.RemoveOrphanedKeysFromStore(groupID)
		}
	}(s.keyService, relatedGroupIDs)

	// Invalidate caches
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}
	s.invalidateGroupListCache()
	for _, groupID := range relatedGroupIDs {
		s.InvalidateKeyStatsCache(groupID)
	}
	// Invalidate child groups cache to ensure sidebar refreshes correctly after deletion.
	// This is necessary because deleting a child group or a parent with children
	// should immediately reflect in the UI without waiting for cache expiration.
	if s.InvalidateChildGroupsCacheCallback != nil {
		s.InvalidateChildGroupsCacheCallback()
	}

	// Soft-delete health metrics for the deleted group
	// For aggregate groups, this deletes all sub-group metrics
	// For standard groups, this deletes model redirect metrics
	if s.OnGroupDeleted != nil {
		isAggregateGroup := group.GroupType == "aggregate"
		s.OnGroupDeleted(id, isAggregateGroup)
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"groupID":         id,
		"groupName":       group.Name,
		"keyCount":        totalKeyCount,
		"childGroupCount": childGroupCount,
	}).Info("Successfully deleted group")

	return nil
}

// StartDeleteGroupTask initiates an asynchronous group deletion task for groups with large key counts.
// This method is called when the key count exceeds the sync threshold (>20,000 keys).
// The task will:
// 1. Delete all keys for the group and its child groups
// 2. Delete sub-group relationships
// 3. Delete child groups
// 4. Delete the parent group
// 5. Invalidate caches
//
// Returns the task status with task_id for client polling.
func (s *GroupService) StartDeleteGroupTask(ctx context.Context, groupID uint, relatedGroupIDs []uint, totalKeys int) (*TaskStatus, error) {
	if s.keyDeleteSvc == nil || s.keyDeleteSvc.TaskService == nil {
		return nil, &app_errors.APIError{
			HTTPStatus: http.StatusServiceUnavailable,
			Code:       "TASK_SERVICE_UNAVAILABLE",
			Message:    "Task service is not available",
		}
	}

	// Get group name for task tracking
	groupName := ""
	if s.groupManager != nil {
		if g, err := s.groupManager.GetGroupByID(groupID); err == nil && g != nil {
			groupName = g.Name
		}
	}

	// Start task
	status, err := s.keyDeleteSvc.TaskService.StartTask(TaskTypeKeyDelete, groupName, totalKeys)
	if err != nil {
		return nil, err
	}

	// Run deletion in background
	go s.runAsyncGroupDeletion(groupID, relatedGroupIDs, totalKeys)

	return status, nil
}

// runAsyncGroupDeletion performs the actual group deletion in background.
// This is called by StartDeleteGroupTask and runs in a separate goroutine.
func (s *GroupService) runAsyncGroupDeletion(groupID uint, relatedGroupIDs []uint, totalKeys int) {
	// Use background context with no timeout for large deletions
	ctx := context.Background()

	// Progress callback to update task status
	progressCallback := func(deleted int64) {
		if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
			if err := s.keyDeleteSvc.TaskService.UpdateProgress(int(deleted)); err != nil {
				logrus.Warnf("Failed to update async group delete progress: %v", err)
			}
		}
	}

	// Step 1: Delete all keys for related groups
	var totalDeleted int64
	for _, gid := range relatedGroupIDs {
		deleted, err := s.keyProvider.RemoveAllKeys(ctx, gid, progressCallback)
		if err != nil {
			logrus.WithContext(ctx).WithError(err).WithField("groupID", gid).Error("Failed to delete keys in async group deletion")
			if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
				_ = s.keyDeleteSvc.TaskService.EndTask(nil, err)
			}
			return
		}
		totalDeleted += deleted
	}

	// Step 2: Delete groups in transaction
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to start transaction for async group deletion")
		if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
			_ = s.keyDeleteSvc.TaskService.EndTask(nil, err)
		}
		return
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	// Get group for logging
	var group models.Group
	if err := tx.First(&group, groupID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logrus.WithContext(ctx).WithError(err).Error("Failed to fetch group in async deletion")
		}
		// Continue anyway - group may have been deleted by another process
	}

	// Delete sub-group relationships
	if err := tx.Where("group_id IN ? OR sub_group_id IN ?", relatedGroupIDs, relatedGroupIDs).
		Delete(&models.GroupSubGroup{}).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to delete sub-group relationships in async deletion")
		if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
			_ = s.keyDeleteSvc.TaskService.EndTask(nil, err)
		}
		return
	}

	// Delete hourly stats
	if err := tx.Where("group_id IN ?", relatedGroupIDs).Delete(&models.GroupHourlyStat{}).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("Failed to delete group hourly stats in async deletion")
		// Continue anyway - stats cleanup is best-effort
	}

	// Delete child groups if any
	if len(relatedGroupIDs) > 1 {
		childGroupIDs := make([]uint, 0, len(relatedGroupIDs)-1)
		for _, gid := range relatedGroupIDs {
			if gid != groupID {
				childGroupIDs = append(childGroupIDs, gid)
			}
		}
		if len(childGroupIDs) > 0 {
			if err := tx.Where("id IN ?", childGroupIDs).Delete(&models.Group{}).Error; err != nil {
				logrus.WithContext(ctx).WithError(err).Error("Failed to delete child groups in async deletion")
				if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
					_ = s.keyDeleteSvc.TaskService.EndTask(nil, err)
				}
				return
			}
		}
	}

	// Delete the parent group
	if err := tx.Delete(&models.Group{}, groupID).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to delete parent group in async deletion")
		if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
			_ = s.keyDeleteSvc.TaskService.EndTask(nil, err)
		}
		return
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to commit async group deletion")
		if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
			_ = s.keyDeleteSvc.TaskService.EndTask(nil, err)
		}
		return
	}
	tx = nil

	// Post-commit cleanup: clear store cache asynchronously
	go func(keyService *KeyService, groupIDs []uint) {
		if keyService == nil || keyService.KeyProvider == nil {
			return
		}
		for _, gid := range groupIDs {
			_ = keyService.KeyProvider.RemoveOrphanedKeysFromStore(gid)
		}
	}(s.keyService, relatedGroupIDs)

	// Invalidate caches
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to invalidate group cache in async deletion")
	}
	s.invalidateGroupListCache()
	for _, gid := range relatedGroupIDs {
		s.InvalidateKeyStatsCache(gid)
	}
	if s.InvalidateChildGroupsCacheCallback != nil {
		s.InvalidateChildGroupsCacheCallback()
	}

	// Soft-delete health metrics
	if s.OnGroupDeleted != nil {
		isAggregateGroup := group.GroupType == "aggregate"
		s.OnGroupDeleted(groupID, isAggregateGroup)
	}

	// End task with success
	result := KeyDeleteResult{
		DeletedCount: int(totalDeleted),
		IgnoredCount: 0,
	}
	if s.keyDeleteSvc != nil && s.keyDeleteSvc.TaskService != nil {
		if err := s.keyDeleteSvc.TaskService.EndTask(result, nil); err != nil {
			logrus.WithContext(ctx).WithError(err).Error("Failed to end async group deletion task")
		}
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"groupID":      groupID,
		"groupName":    group.Name,
		"keysDeleted":  totalDeleted,
		"childGroups":  len(relatedGroupIDs) - 1,
	}).Info("Successfully completed async group deletion")
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

	// Invalidate child groups cache to ensure sidebar refreshes correctly
	if s.InvalidateChildGroupsCacheCallback != nil {
		s.InvalidateChildGroupsCacheCallback()
	}

	// Step 11: Reset in-memory key stats cache to avoid serving stale data for reused IDs
	s.keyStatsCacheMu.Lock()
	s.keyStatsCache = make(map[uint]groupKeyStatsCacheEntry)
	s.keyStatsCacheMu.Unlock()

	logrus.WithContext(ctx).Info("Successfully deleted all groups and keys")
	return nil
}

// CopyGroup duplicates a group and optionally copies active keys.
// Optimized: Key decryption is now done asynchronously to speed up HTTP response.
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

	// Get key count for sync/async decision (fast query, no decryption)
	var keyCount int64
	if option != "none" {
		query := tx.Model(&models.APIKey{}).Where("group_id = ?", sourceGroupID)
		if option == "valid_only" {
			query = query.Where("status = ?", models.KeyStatusActive)
		}
		if err := query.Count(&keyCount).Error; err != nil {
			return nil, app_errors.ParseDBError(err)
		}
	}

	// Commit the transaction immediately - group is created
	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	tx = nil

	// Update group list cache with the new group before invalidating GroupManager cache
	// This ensures ListGroups can return cached data even if DB is busy with async import
	s.addGroupToListCache(&newGroup)

	// Invalidate GroupManager cache (for GetGroupByID/GetGroupByName lookups)
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	// Multi-threshold strategy for key copying (5 tiers based on best practices):
	// - 0-1,000 keys: Fast sync copy using AddMultipleKeys (<5s, simple and fast)
	// - 1,000-5,000 keys: Bulk sync copy using BulkImportSvc (5-15s, optimized for medium batches)
	// - 5,000-10,000 keys: Large sync copy using BulkImportSvc (15-30s, large batches)
	// - 10,000-20,000 keys: Optimized sync copy (30-60s, very large batches, stays within HTTP timeout)
	// - >20,000 keys: Async copy (return immediately, process in background to avoid timeout)

	if keyCount > 0 {
		tier := GetOperationTier(keyCount)

		switch tier {
		case TierFastSync:
			// Tier 1: Small group - fast sync copy using AddMultipleKeys
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupId":  newGroup.ID,
				"keyCount": keyCount,
				"tier":     tier.String(),
			}).Info("Starting fast sync copy for small group")

			addedCount, ignoredCount, err := s.syncCopyKeys(ctx, &newGroup, sourceGroupID, option)
			if err != nil {
				logrus.WithContext(ctx).WithFields(logrus.Fields{
					"groupId":  newGroup.ID,
					"keyCount": keyCount,
				}).WithError(err).Error("failed to sync copy keys")
				// Don't fail the copy - group is already created
			} else {
				logrus.WithContext(ctx).WithFields(logrus.Fields{
					"groupId":      newGroup.ID,
					"addedCount":   addedCount,
					"ignoredCount": ignoredCount,
				}).Info("Completed fast sync copy for small group")
			}

		case TierBulkSync, TierLargeSync, TierOptimizedSync:
			// Tier 2-4: Medium to large groups - sync bulk copy using BulkImportSvc
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupId":  newGroup.ID,
				"keyCount": keyCount,
				"tier":     tier.String(),
			}).Info("Starting bulk sync copy")

			addedCount, ignoredCount, err := s.syncBulkCopyKeys(ctx, &newGroup, sourceGroupID, option)
			if err != nil {
				logrus.WithContext(ctx).WithFields(logrus.Fields{
					"groupId":  newGroup.ID,
					"keyCount": keyCount,
				}).WithError(err).Error("failed to bulk sync copy keys")
				// Don't fail the copy - group is already created
			} else {
				logrus.WithContext(ctx).WithFields(logrus.Fields{
					"groupId":      newGroup.ID,
					"addedCount":   addedCount,
					"ignoredCount": ignoredCount,
				}).Info("Completed bulk sync copy")
			}

		case TierAsync:
			// Tier 5: Very large group - async copy to avoid HTTP timeout
			if _, err := s.keyImportSvc.StartCopyTask(&newGroup, sourceGroupID, option, int(keyCount)); err != nil {
				logrus.WithContext(ctx).WithFields(logrus.Fields{
					"groupId":  newGroup.ID,
					"keyCount": keyCount,
				}).WithError(err).Error("failed to start async copy task for group copy")
				// Don't fail the copy - group is already created
			} else {
				logrus.WithContext(ctx).WithFields(logrus.Fields{
					"groupId":  newGroup.ID,
					"keyCount": keyCount,
					"tier":     tier.String(),
				}).Info("Started async copy task for very large group")
			}
		}
	} else {
		// Log when no keys to copy - helps debug empty source groups or valid_only filter results
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"groupId":       newGroup.ID,
			"sourceGroupId": sourceGroupID,
			"copyOption":    option,
		}).Debug("Skipped copy task - no keys to copy")
	}

	return &newGroup, nil
}

// syncCopyKeys performs synchronous key copying for small groups (Tier 1: ≤1000 keys).
// This provides immediate results for better user experience using simple AddKeys method.
func (s *GroupService) syncCopyKeys(ctx context.Context, targetGroup *models.Group, sourceGroupID uint, copyOption string) (addedCount, ignoredCount int, err error) {
	// Fetch source keys from database
	var sourceKeyData []struct {
		KeyValue string
	}
	query := s.db.WithContext(ctx).Model(&models.APIKey{}).Select("key_value").Where("group_id = ?", sourceGroupID)
	if copyOption == "valid_only" {
		query = query.Where("status = ?", models.KeyStatusActive)
	}
	if err := query.Scan(&sourceKeyData).Error; err != nil {
		return 0, 0, fmt.Errorf("failed to fetch source keys: %w", err)
	}

	if len(sourceKeyData) == 0 {
		return 0, 0, nil
	}

	// Decrypt keys
	decryptedKeys := make([]string, 0, len(sourceKeyData))
	for _, keyData := range sourceKeyData {
		decryptedKey, err := s.encryptionSvc.Decrypt(keyData.KeyValue)
		if err != nil {
			ignoredCount++
			continue
		}
		decryptedKeys = append(decryptedKeys, decryptedKey)
	}

	if len(decryptedKeys) == 0 {
		return 0, ignoredCount, nil
	}

	// Import keys to target group using AddMultipleKeys method (fast for small batches)
	keysText := strings.Join(decryptedKeys, "\n")
	result, err := s.keyService.AddMultipleKeys(targetGroup.ID, keysText)
	if err != nil {
		return 0, ignoredCount, fmt.Errorf("failed to import keys: %w", err)
	}

	return result.AddedCount, result.IgnoredCount + ignoredCount, nil
}

// syncBulkCopyKeys performs synchronous bulk key copying for medium/large groups (Tier 2-3: 1K-20K keys).
// This uses BulkImportService for optimized batch insertion with better performance than AddKeys.
func (s *GroupService) syncBulkCopyKeys(ctx context.Context, targetGroup *models.Group, sourceGroupID uint, copyOption string) (addedCount, ignoredCount int, err error) {
	// Guard against nil BulkImportSvc
	if s.bulkImportSvc == nil {
		// Fallback to simple sync copy if BulkImportSvc is not available
		return s.syncCopyKeys(ctx, targetGroup, sourceGroupID, copyOption)
	}

	// Fetch source keys from database
	var sourceKeyData []struct {
		KeyValue string
	}
	query := s.db.WithContext(ctx).Model(&models.APIKey{}).Select("key_value").Where("group_id = ?", sourceGroupID)
	if copyOption == "valid_only" {
		query = query.Where("status = ?", models.KeyStatusActive)
	}
	if err := query.Scan(&sourceKeyData).Error; err != nil {
		return 0, 0, fmt.Errorf("failed to fetch source keys: %w", err)
	}

	if len(sourceKeyData) == 0 {
		return 0, 0, nil
	}

	// Decrypt keys
	decryptedKeys := make([]string, 0, len(sourceKeyData))
	decryptErrors := 0
	for _, keyData := range sourceKeyData {
		decryptedKey, err := s.encryptionSvc.Decrypt(keyData.KeyValue)
		if err != nil {
			decryptErrors++
			continue
		}
		decryptedKeys = append(decryptedKeys, decryptedKey)
	}

	if len(decryptedKeys) == 0 {
		return 0, decryptErrors, nil
	}

	// Get existing key hashes for deduplication
	var existingHashes []string
	if err := s.db.WithContext(ctx).Model(&models.APIKey{}).Where("group_id = ?", targetGroup.ID).Pluck("key_hash", &existingHashes).Error; err != nil {
		return 0, decryptErrors, fmt.Errorf("failed to check existing keys: %w", err)
	}

	existingHashMap := make(map[string]bool, len(existingHashes))
	for _, h := range existingHashes {
		existingHashMap[h] = true
	}

	// Prepare keys for bulk import
	newKeysToCreate := make([]models.APIKey, 0, len(decryptedKeys))
	uniqueNewKeys := make(map[string]bool, len(decryptedKeys))
	duplicateCount := 0

	for _, keyVal := range decryptedKeys {
		trimmedKey := strings.TrimSpace(keyVal)
		if trimmedKey == "" || uniqueNewKeys[trimmedKey] {
			duplicateCount++
			continue
		}

		keyHash := s.encryptionSvc.Hash(trimmedKey)
		if existingHashMap[keyHash] {
			duplicateCount++
			continue
		}

		encryptedKey, err := s.encryptionSvc.Encrypt(trimmedKey)
		if err != nil {
			logrus.WithError(err).Debug("Failed to encrypt key, skipping")
			duplicateCount++
			continue
		}

		uniqueNewKeys[trimmedKey] = true
		newKeysToCreate = append(newKeysToCreate, models.APIKey{
			GroupID:  targetGroup.ID,
			KeyValue: encryptedKey,
			KeyHash:  keyHash,
			Status:   models.KeyStatusActive,
		})
	}

	if len(newKeysToCreate) == 0 {
		return 0, decryptErrors + duplicateCount, nil
	}

	// Use bulk import service for fast insertion
	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"groupId":  targetGroup.ID,
		"keyCount": len(newKeysToCreate),
	}).Info("Starting bulk import for copied keys")

	if err := s.bulkImportSvc.BulkInsertAPIKeys(newKeysToCreate); err != nil {
		return 0, decryptErrors + duplicateCount, fmt.Errorf("bulk import failed: %w", err)
	}

	// Load keys to memory store after successful import
	if s.keyProvider != nil {
		if err := s.keyProvider.LoadGroupKeysToStore(targetGroup.ID); err != nil {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"groupId": targetGroup.ID,
				"error":   err,
			}).Error("Failed to load keys to store after bulk import")
		}
	}

	// Invalidate cache after adding keys
	if s.keyService != nil && s.keyService.CacheInvalidationCallback != nil {
		s.keyService.CacheInvalidationCallback(targetGroup.ID)
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"groupId":      targetGroup.ID,
		"addedCount":   len(newKeysToCreate),
		"ignoredCount": decryptErrors + duplicateCount,
	}).Info("Completed bulk import for copied keys")

	return len(newKeysToCreate), decryptErrors + duplicateCount, nil
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
		Success24h int64
		Failure24h int64
		Success7d  int64
		Failure7d  int64
		Success30d int64
		Failure30d int64
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

// fetchKeyStats retrieves API key statistics for a group with adaptive caching.
// It uses parallel queries for better performance and extends TTL for frequently accessed entries.
func (s *GroupService) fetchKeyStats(ctx context.Context, groupID uint) (KeyStats, error) {
	// Check cache first with adaptive TTL support
	s.keyStatsCacheMu.RLock()
	if entry, ok := s.keyStatsCache[groupID]; ok {
		if time.Now().Before(entry.ExpiresAt) {
			stats := entry.Stats
			s.keyStatsCacheMu.RUnlock()

			// Update hit count asynchronously to avoid blocking the read path
			go s.updateKeyStatsCacheHit(groupID)

			return stats, nil
		}
	}
	s.keyStatsCacheMu.RUnlock()

	// Cache miss - query database with timeout to avoid blocking during bulk inserts
	// Key stats queries may need more time during bulk imports
	queryCtx, cancel := context.WithTimeout(ctx, 2*getDBLookupTimeout())
	defer cancel()

	// Use parallel queries for better performance - both COUNT queries can run concurrently
	var totalKeys, activeKeys int64
	var totalErr, activeErr error
	var wg sync.WaitGroup

	wg.Add(2)

	// Query total keys count
	go func() {
		defer wg.Done()
		if err := s.db.WithContext(queryCtx).Model(&models.APIKey{}).
			Where("group_id = ?", groupID).Count(&totalKeys).Error; err != nil {
			totalErr = err
		}
	}()

	// Query active keys count
	go func() {
		defer wg.Done()
		if err := s.db.WithContext(queryCtx).Model(&models.APIKey{}).
			Where("group_id = ? AND status = ?", groupID, models.KeyStatusActive).
			Count(&activeKeys).Error; err != nil {
			activeErr = err
		}
	}()

	wg.Wait()

	// Handle errors with graceful degradation
	if totalErr != nil {
		if errors.Is(totalErr, context.DeadlineExceeded) || errors.Is(totalErr, context.Canceled) {
			logrus.WithContext(ctx).WithField("groupID", groupID).Warn("Key stats total count timed out or canceled, returning empty stats")
			return KeyStats{TotalKeys: 0, ActiveKeys: 0, InvalidKeys: 0}, nil
		}
		return KeyStats{}, fmt.Errorf("failed to count total keys: %w", totalErr)
	}

	if activeErr != nil {
		if errors.Is(activeErr, context.DeadlineExceeded) || errors.Is(activeErr, context.Canceled) {
			logrus.WithContext(ctx).WithField("groupID", groupID).Warn("Key stats active count timed out or canceled, returning partial stats with unknown active/invalid breakdown")
			// Return partial stats with known total but unknown active/invalid breakdown
			return KeyStats{TotalKeys: totalKeys, ActiveKeys: 0, InvalidKeys: 0}, nil
		}
		return KeyStats{}, fmt.Errorf("failed to count active keys: %w", activeErr)
	}

	stats := KeyStats{
		TotalKeys:   totalKeys,
		ActiveKeys:  activeKeys,
		InvalidKeys: totalKeys - activeKeys,
	}

	// Update cache with base TTL
	s.keyStatsCacheMu.Lock()
	s.keyStatsCache[groupID] = groupKeyStatsCacheEntry{
		Stats:      stats,
		ExpiresAt:  time.Now().Add(s.keyStatsCacheTTL),
		HitCount:   0,
		CurrentTTL: s.keyStatsCacheTTL,
	}
	s.keyStatsCacheMu.Unlock()

	return stats, nil
}

// updateKeyStatsCacheHit updates hit statistics and extends TTL for frequently accessed entries.
// This implements adaptive TTL: entries with high hit counts get extended TTL up to maxTTL.
func (s *GroupService) updateKeyStatsCacheHit(groupID uint) {
	s.keyStatsCacheMu.Lock()
	defer s.keyStatsCacheMu.Unlock()

	entry, exists := s.keyStatsCache[groupID]
	if !exists || time.Now().After(entry.ExpiresAt) {
		return
	}

	entry.HitCount++

	// Extend TTL if hit threshold is reached (10 hits) and not at max TTL
	// This rewards frequently accessed entries with longer cache lifetime
	const hitThreshold = 10
	const ttlMultiplier = 1.2

	if entry.HitCount >= hitThreshold && entry.CurrentTTL < s.keyStatsCacheMaxTTL {
		newTTL := time.Duration(float64(entry.CurrentTTL) * ttlMultiplier)
		if newTTL > s.keyStatsCacheMaxTTL {
			newTTL = s.keyStatsCacheMaxTTL
		}
		entry.CurrentTTL = newTTL
		entry.ExpiresAt = time.Now().Add(newTTL)
		entry.HitCount = 0 // Reset hit count after extension
	}

	s.keyStatsCache[groupID] = entry
}

// InvalidateKeyStatsCache invalidates the key statistics cache for a specific group.
// It also invalidates the aggregate group stats cache since sub-group key counts affect aggregate displays.
func (s *GroupService) InvalidateKeyStatsCache(groupID uint) {
	s.keyStatsCacheMu.Lock()
	delete(s.keyStatsCache, groupID)
	s.keyStatsCacheMu.Unlock()

	// Also invalidate aggregate group stats cache for this group
	// This ensures that when keys are added/removed/status-changed,
	// the aggregate group view shows updated sub-group statistics
	if s.aggregateGroupService != nil {
		s.aggregateGroupService.InvalidateStatsCacheForGroup(groupID)
	}
}

// WarmupCache preloads frequently accessed data into cache.
// This should be called during application startup to reduce cold-start latency.
// It loads group list and key stats for all standard groups in parallel.
func (s *GroupService) WarmupCache(ctx context.Context) error {
	logrus.WithContext(ctx).Info("Starting cache warmup...")

	// First, warm up the group list cache
	groups, err := s.ListGroups(ctx)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("Failed to warm up group list cache")
		return fmt.Errorf("failed to list groups for cache warmup: %w", err)
	}

	// Warm up key stats for all standard groups in parallel
	// Use a semaphore to limit concurrent DB queries and avoid overwhelming the database
	const maxConcurrent = 10
	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var warmupErrors int64

	for _, group := range groups {
		// Skip aggregate groups as they don't have direct key stats
		if group.GroupType == "aggregate" {
			continue
		}

		wg.Add(1)
		go func(groupID uint) {
			defer wg.Done()

			// Acquire semaphore slot
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Use a timeout context for each warmup query
			warmupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			if _, err := s.fetchKeyStats(warmupCtx, groupID); err != nil {
				logrus.WithContext(ctx).WithError(err).WithField("group_id", groupID).
					Debug("Failed to warm up key stats cache for group")
				// Use atomic increment for thread-safe error counting
				atomic.AddInt64(&warmupErrors, 1)
			}
		}(group.ID)
	}

	wg.Wait()

	if warmupErrors > 0 {
		logrus.WithContext(ctx).WithField("failed_groups", warmupErrors).
			Warn("Cache warmup completed with some failures")
	} else {
		logrus.WithContext(ctx).WithField("groups_warmed", len(groups)).
			Info("Cache warmup completed successfully")
	}

	return nil
}

// GetCacheStats returns current cache statistics for monitoring.
// This is useful for observability and debugging cache performance.
func (s *GroupService) GetCacheStats() map[string]interface{} {
	s.keyStatsCacheMu.RLock()
	keyStatsCacheSize := len(s.keyStatsCache)
	s.keyStatsCacheMu.RUnlock()

	s.groupListCacheMu.RLock()
	var groupListCacheSize int
	var groupListCacheHits int64
	var groupListCacheTTL time.Duration
	if s.groupListCache != nil {
		groupListCacheSize = len(s.groupListCache.Groups)
		groupListCacheHits = s.groupListCache.HitCount
		groupListCacheTTL = s.groupListCache.CurrentTTL
	}
	s.groupListCacheMu.RUnlock()

	return map[string]interface{}{
		"key_stats_cache_entries": keyStatsCacheSize,
		"group_list_cache_size":   groupListCacheSize,
		"group_list_cache_hits":   groupListCacheHits,
		"group_list_cache_ttl_ms": groupListCacheTTL.Milliseconds(),
	}
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
	var wg sync.WaitGroup
	wg.Add(2)
	// key stats
	go func() {
		defer wg.Done()
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
		defer wg.Done()
		if errs := s.fetchRequestStats(ctx, groupID, stats); len(errs) > 0 {
			errsMu.Lock()
			allErrors = append(allErrors, errs...)
			errsMu.Unlock()
		}
	}()

	wg.Wait()

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

// ToggleGroupEnabled enables or disables a group.
// When a standard group is disabled, its child groups are also disabled.
// When a group is disabled, it will be excluded from aggregate group weight calculations.
func (s *GroupService) ToggleGroupEnabled(ctx context.Context, id uint, enabled bool) error {
	// Update directly (RowsAffected check below handles non-existent groups)
	result := s.db.WithContext(ctx).Model(&models.Group{}).Where("id = ?", id).Update("enabled", enabled)
	if result.Error != nil {
		return app_errors.ParseDBError(result.Error)
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrResourceNotFound
	}

	// Note: Group disable does NOT cascade to bound site (one-way sync: site -> groups only)

	// Sync enabled status to child groups (for standard groups with children)
	if s.SyncChildGroupsEnabledCallback != nil {
		if err := s.SyncChildGroupsEnabledCallback(ctx, id, enabled); err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("Failed to sync group enabled status to child groups")
			// Don't fail the operation, just log the warning
		}
	}

	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache")
	}

	// Invalidate group list cache after toggling group enabled status
	s.invalidateGroupListCache()

	return nil
}

// normalizeModelRedirectRules handles V1 to V2 migration and returns normalized rules.
// If V2 rules exist, V1 rules are merged into V2 (V2 takes priority), then V1 is cleared.
// If only V1 rules exist, they are converted to V2 format, then V1 is cleared.
// Returns (finalV2JSON, emptyV1Map) to ensure only V2 rules are stored.
func (s *GroupService) normalizeModelRedirectRules(v1Rules map[string]string, v2RulesJSON json.RawMessage) (datatypes.JSON, datatypes.JSONMap) {
	// Parse V2 rules from JSON
	var v2Map map[string]*models.ModelRedirectRuleV2
	if len(v2RulesJSON) > 0 {
		if err := json.Unmarshal(v2RulesJSON, &v2Map); err != nil {
			logrus.WithError(err).Warn("Failed to parse V2 model redirect rules, treating as empty")
			v2Map = nil
		}
	}

	// Merge V1 into V2 (V2 takes priority)
	mergedV2 := models.MergeV1IntoV2Rules(v1Rules, v2Map)

	// Convert merged V2 rules back to JSON
	var finalV2JSON datatypes.JSON
	if len(mergedV2) > 0 {
		jsonBytes, err := json.Marshal(mergedV2)
		if err != nil {
			logrus.WithError(err).Error("Failed to marshal merged V2 rules")
			finalV2JSON = datatypes.JSON("{}") // Fallback to empty
		} else {
			finalV2JSON = datatypes.JSON(jsonBytes)
		}
	} else {
		finalV2JSON = datatypes.JSON("{}") // Empty V2 rules
	}

	// Always clear V1 rules after migration
	return finalV2JSON, datatypes.JSONMap{}
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

	// Set User-Agent for Codex channel when fetching models
	// IMPORTANT: This UA is ONLY set for model fetching requests, NOT for normal proxy requests.
	// Normal Codex requests should use passthrough behavior (preserve client's original UA).
	// Codex CC mode (/claude path) sets UA separately in server.go via isCodexCCMode() check.
	// Codex upstream may reject model list requests without proper User-Agent.
	if group.ChannelType == "codex" {
		req.Header.Set("User-Agent", channel.CodexUserAgent)
	}

	// Set User-Agent for Anthropic channel when fetching models
	// Similar to Codex, this is ONLY for model fetching requests.
	// Normal Anthropic requests preserve client's original UA.
	// OpenAI CC mode (/claude path) sets UA separately in server.go via isCCMode() check.
	if group.ChannelType == "anthropic" {
		req.Header.Set("User-Agent", channel.ClaudeCodeUserAgent)
	}

	// NOTE: ParamOverrides are NOT applied to GET requests (like /v1/models).
	// ParamOverrides are designed to modify request body parameters (e.g., enable_thinking, temperature),
	// which only make sense for POST/PUT/PATCH requests with JSON bodies.
	// Applying them as query parameters to GET requests would be incorrect and could cause issues
	// with upstream APIs that don't expect these parameters in the URL.

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
		if len(errorPreview) > 500 {
			errorPreview = errorPreview[:500] + "..."
		}

		// Redact credentials from URL for safe logging and error messages
		// This prevents leaking sensitive information like API keys in query params or userinfo
		safeURL := selection.URL
		if u, parseErr := url.Parse(selection.URL); parseErr == nil {
			u.User = nil
			u.RawQuery = ""
			safeURL = u.String()
		}

		// Log detailed error information for debugging (use redacted URL in logs to avoid leaking secrets)
		// Error body preview is logged at Debug level only to aid troubleshooting while
		// avoiding exposure in production logs. Client-facing messages exclude error body
		// to prevent information disclosure.
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"status_code":   resp.StatusCode,
			"request_url":   safeURL,
			"channel_type":  group.ChannelType,
			"content_type":  resp.Header.Get("Content-Type"),
			"error_preview": errorPreview,
		}).Debug("FetchGroupModels: Upstream error response details")

		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"status_code":  resp.StatusCode,
			"request_url":  safeURL,
			"channel_type": group.ChannelType,
		}).Error("FetchGroupModels: Upstream returned non-OK status")

		// Provide specific error messages based on status code.
		// Use redacted URL in client-facing error messages to avoid leaking internal details.
		// Error body preview is intentionally excluded from client messages to prevent information disclosure.
		switch resp.StatusCode {
		case http.StatusBadRequest: // 400
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("bad request (URL: %s)", safeURL))
		case http.StatusUnauthorized: // 401
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("authentication failed (URL: %s): invalid or expired API key", safeURL))
		case http.StatusForbidden: // 403
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("access forbidden (URL: %s): insufficient permissions", safeURL))
		case http.StatusNotFound: // 404
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("models endpoint not found (URL: %s)", safeURL))
		case http.StatusTooManyRequests: // 429
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("rate limit exceeded (URL: %s)", safeURL))
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable: // 500, 502, 503
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("upstream server error (URL: %s)", safeURL))
		default:
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, fmt.Sprintf("upstream returned error status %d (URL: %s)", resp.StatusCode, safeURL))
		}
	}

	// Read response body with size limit to prevent memory exhaustion on large model lists
	// Use limit+1 pattern to detect oversized bodies instead of silently truncating
	const maxModelListBodySize = 10 * 1024 * 1024 // 10MB limit for model list responses
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBodySize+1))
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to read response body")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "failed to read upstream response")
	}
	if int64(len(bodyBytes)) > maxModelListBodySize {
		logrus.WithContext(ctx).WithField("limit_mb", maxModelListBodySize/(1024*1024)).
			Warn("Model list response body too large")
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "model list response too large")
	}

	// Decompress response body if needed (gzip/deflate), mirroring proxy model list handling
	// Use size-limited decompression to prevent memory exhaustion from malicious compressed payloads
	contentEncoding := resp.Header.Get("Content-Encoding")
	decompressed, err := utils.DecompressResponseWithLimit(contentEncoding, bodyBytes, maxModelListBodySize)
	if err != nil {
		// Use errors.Is() for sentinel error comparison to handle wrapped errors properly
		if errors.Is(err, utils.ErrDecompressedTooLarge) {
			logrus.WithContext(ctx).WithField("limit_mb", maxModelListBodySize/(1024*1024)).
				Warn("Decompressed model list response too large")
			return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "decompressed response too large")
		}
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
		"result_keys":   getMapKeys(result),
		"model_count":   modelCount,
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
