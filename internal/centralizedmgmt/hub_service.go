// Package centralizedmgmt provides centralized API management for GPT-Load.
// It aggregates models from all groups and provides a unified API endpoint.
package centralizedmgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	// Default TTL for model pool cache
	defaultModelPoolCacheTTL = 30 * time.Second

	// Maximum TTL for adaptive extension
	defaultModelPoolCacheMaxTTL = 2 * time.Minute

	// Default health score threshold for group selection
	// Groups with health score below this threshold are excluded from selection.
	// This is a hard filter, different from dynamic weight's soft penalty.
	// Lowered from 0.5 to 0.3 to align with unstable channel tolerance.
	defaultHealthScoreThreshold = 0.3

	// Hit threshold for adaptive TTL extension
	cacheHitThreshold = 10

	// TTL multiplier for adaptive extension
	cacheTTLMultiplier = 1.2
)

// ModelPoolEntry represents a model with its source groups, organized by channel type.
// Used for displaying the aggregated model pool in the UI.
// Groups are organized by channel type first for better visualization.
type ModelPoolEntry struct {
	ModelName     string                   `json:"model_name"`
	SourcesByType map[string][]ModelSource `json:"sources_by_type"` // Grouped by channel type
	TotalSources  int                      `json:"total_sources"`
}

// ModelSource represents a group that provides a model.
// Contains all information needed for group selection and display.
type ModelSource struct {
	GroupID         uint    `json:"group_id"`
	GroupName       string  `json:"group_name"`
	GroupType       string  `json:"group_type"`     // "standard" or "aggregate"
	IsChildGroup    bool    `json:"is_child_group"` // True if this is a child group of a standard group
	ChannelType     string  `json:"channel_type"`   // Channel type (e.g., "openai", "claude", etc.)
	Sort            int     `json:"sort"`
	Weight          int     `json:"weight"`
	HealthScore     float64 `json:"health_score"`
	EffectiveWeight float64 `json:"effective_weight"` // Effective weight (1 decimal place, min 0.1)
	Enabled         bool    `json:"enabled"`
}

// modelPoolCacheEntry holds cached model pool data with adaptive TTL support.
type modelPoolCacheEntry struct {
	Pool       []ModelPoolEntry
	ExpiresAt  time.Time
	HitCount   int64
	CurrentTTL time.Duration
}

// HubService manages centralized API routing and model aggregation.
// It provides a unified endpoint for accessing models from all groups.
type HubService struct {
	db                   *gorm.DB
	groupManager         *services.GroupManager
	dynamicWeightManager *services.DynamicWeightManager

	// Model pool cache with adaptive TTL
	modelPoolCache    *modelPoolCacheEntry
	modelPoolCacheMu  sync.RWMutex
	modelPoolCacheTTL time.Duration
	modelPoolMaxTTL   time.Duration

	// Health score threshold for group selection (stored as uint64 bits for atomic access)
	healthScoreThreshold atomic.Uint64

	// Only aggregate groups setting (type-safe atomic)
	onlyAggregateGroups atomic.Bool
}

// NewHubService creates a new HubService instance.
func NewHubService(
	db *gorm.DB,
	groupManager *services.GroupManager,
	dynamicWeightManager *services.DynamicWeightManager,
) *HubService {
	svc := &HubService{
		db:                   db,
		groupManager:         groupManager,
		dynamicWeightManager: dynamicWeightManager,
		modelPoolCacheTTL:    defaultModelPoolCacheTTL,
		modelPoolMaxTTL:      defaultModelPoolCacheMaxTTL,
	}
	svc.healthScoreThreshold.Store(math.Float64bits(defaultHealthScoreThreshold))
	svc.onlyAggregateGroups.Store(true) // Default: only accept aggregate groups
	return svc
}

// SetHealthScoreThreshold sets the minimum health score for group selection.
// Thread-safe using atomic operations.
func (s *HubService) SetHealthScoreThreshold(threshold float64) {
	s.healthScoreThreshold.Store(math.Float64bits(threshold))
}

// GetHealthScoreThreshold returns the current health score threshold.
// Thread-safe using atomic operations.
func (s *HubService) GetHealthScoreThreshold() float64 {
	return math.Float64frombits(s.healthScoreThreshold.Load())
}

// SetOnlyAggregateGroups sets whether to only accept aggregate groups.
// Thread-safe using atomic operations.
// Note: When called via UpdateHubSettings, cache invalidation is handled there.
// Direct callers should manually call InvalidateModelPoolCache() if needed.
func (s *HubService) SetOnlyAggregateGroups(only bool) {
	s.onlyAggregateGroups.Store(only)
}

// getOnlyAggregateGroups returns the current only aggregate groups setting.
// Thread-safe using atomic operations.
func (s *HubService) getOnlyAggregateGroups() bool {
	return s.onlyAggregateGroups.Load()
}

// GetModelPool returns the aggregated model pool from all enabled groups.
// Uses caching with adaptive TTL for performance.
func (s *HubService) GetModelPool(ctx context.Context) ([]ModelPoolEntry, error) {
	// Check cache first with adaptive TTL support
	s.modelPoolCacheMu.RLock()
	if s.modelPoolCache != nil && time.Now().Before(s.modelPoolCache.ExpiresAt) {
		// Deep copy to prevent cache corruption
		// Shallow copy would share map references, allowing callers to mutate cached data
		pool := make([]ModelPoolEntry, len(s.modelPoolCache.Pool))
		for i, entry := range s.modelPoolCache.Pool {
			// Deep copy the SourcesByType map
			sourcesByType := make(map[string][]ModelSource, len(entry.SourcesByType))
			for channelType, sources := range entry.SourcesByType {
				// Deep copy the slice
				sourcesCopy := make([]ModelSource, len(sources))
				copy(sourcesCopy, sources)
				sourcesByType[channelType] = sourcesCopy
			}
			pool[i] = ModelPoolEntry{
				ModelName:     entry.ModelName,
				SourcesByType: sourcesByType,
				TotalSources:  entry.TotalSources,
			}
		}
		s.modelPoolCacheMu.RUnlock()

		// Update hit count asynchronously to avoid blocking the read path
		go s.updateModelPoolCacheHit()

		logrus.WithContext(ctx).Debug("Model pool cache hit")
		return pool, nil
	}
	s.modelPoolCacheMu.RUnlock()

	// Cache miss - rebuild model pool
	pool, err := s.buildModelPool(ctx)
	if err != nil {
		return nil, err
	}

	// Update cache with base TTL
	s.modelPoolCacheMu.Lock()
	s.modelPoolCache = &modelPoolCacheEntry{
		Pool:       pool,
		ExpiresAt:  time.Now().Add(s.modelPoolCacheTTL),
		HitCount:   0,
		CurrentTTL: s.modelPoolCacheTTL,
	}
	s.modelPoolCacheMu.Unlock()

	logrus.WithContext(ctx).Debug("Model pool cache updated")
	return pool, nil
}

// buildModelPool aggregates models from all enabled groups, organized by channel type.
func (s *HubService) buildModelPool(ctx context.Context) ([]ModelPoolEntry, error) {
	// Get all groups from database (only V2 rules needed, V1 has been migrated)
	var groups []models.Group
	if err := s.db.WithContext(ctx).
		Select("id, name, display_name, group_type, channel_type, enabled, sort, model_redirect_rules_v2, config, parent_group_id, custom_model_names").
		Where("enabled = ?", true).
		Order("sort ASC, id ASC").
		Find(&groups).Error; err != nil {
		return nil, err
	}

	// Get only aggregate groups setting once for this build
	onlyAggregateGroups := s.getOnlyAggregateGroups()

	// Map to aggregate models by name
	modelMap := make(map[string]map[string][]ModelSource) // model_name -> channel_type -> sources

	for _, group := range groups {
		// Skip non-aggregate groups if only_aggregate_groups is enabled
		if onlyAggregateGroups && group.GroupType != "aggregate" {
			continue
		}

		// Get models for this group (from V2 redirect rules and custom model names)
		groupModels := s.getGroupModels(&group)

		// Calculate health score and effective weight
		healthScore := s.calculateGroupHealthScore(&group)
		baseWeight := 100 // Default weight for standard groups

		// Calculate effective weight with 1 decimal place precision, minimum 0.1
		// Even unhealthy groups get minimum weight to receive occasional requests
		effectiveWeight := float64(baseWeight) * healthScore
		// Round to 1 decimal place
		effectiveWeight = math.Round(effectiveWeight*10) / 10
		if effectiveWeight < 0.1 {
			effectiveWeight = 0.1
		}

		source := ModelSource{
			GroupID:         group.ID,
			GroupName:       group.Name,
			GroupType:       group.GroupType,
			IsChildGroup:    group.ParentGroupID != nil, // True if this group has a parent
			ChannelType:     group.ChannelType,
			Sort:            group.Sort,
			Weight:          baseWeight,
			HealthScore:     healthScore,
			EffectiveWeight: effectiveWeight,
			Enabled:         group.Enabled,
		}

		for _, modelName := range groupModels {
			if modelMap[modelName] == nil {
				modelMap[modelName] = make(map[string][]ModelSource)
			}
			modelMap[modelName][group.ChannelType] = append(modelMap[modelName][group.ChannelType], source)
		}
	}

	// Convert map to sorted slice
	pool := make([]ModelPoolEntry, 0, len(modelMap))
	for modelName, sourcesByType := range modelMap {
		// Sort sources within each channel type by sort field (ascending)
		for channelType := range sourcesByType {
			sources := sourcesByType[channelType]
			sort.Slice(sources, func(i, j int) bool {
				if sources[i].Sort != sources[j].Sort {
					return sources[i].Sort < sources[j].Sort
				}
				return sources[i].GroupID < sources[j].GroupID
			})
			sourcesByType[channelType] = sources
		}

		// Count total sources
		totalSources := 0
		for _, sources := range sourcesByType {
			totalSources += len(sources)
		}

		pool = append(pool, ModelPoolEntry{
			ModelName:     modelName,
			SourcesByType: sourcesByType,
			TotalSources:  totalSources,
		})
	}

	// Sort pool by model name for consistent ordering
	sort.Slice(pool, func(i, j int) bool {
		return pool[i].ModelName < pool[j].ModelName
	})

	return pool, nil
}

// getGroupModels returns the list of virtual models available from a group.
// For standard groups, returns model redirect V2 source models.
// For aggregate groups, returns the intersection of models from all sub-groups
// (only models that exist in ALL sub-groups are valid for aggregation).
func (s *HubService) getGroupModels(group *models.Group) []string {
	return s.getGroupModelsWithVisited(group, make(map[uint]struct{}))
}

// getGroupModelsWithVisited returns the list of virtual models with cycle detection.
// Uses path-scoped visited set to prevent infinite recursion on circular group references.
// The visited set is scoped to the current recursion path, allowing shared sub-groups in DAG structures.
func (s *HubService) getGroupModelsWithVisited(group *models.Group, visited map[uint]struct{}) []string {
	// Check for circular reference
	if _, seen := visited[group.ID]; seen {
		logrus.WithField("group_id", group.ID).Warn("Circular reference detected in group hierarchy")
		return nil
	}
	// Path-scoped visited: add current group to path, remove when done
	// This allows shared sub-groups in DAG structures while detecting true cycles
	visited[group.ID] = struct{}{}
	defer delete(visited, group.ID)

	if group.GroupType == "aggregate" {
		// For aggregate groups, get intersection of models from all sub-groups
		// Plus any custom model names defined for this aggregate group
		models := s.getAggregateGroupModelsWithVisited(group.ID, visited)

		// Add custom model names if defined
		customModels := s.parseCustomModelNames(group.CustomModelNames)
		if len(customModels) > 0 {
			// Combine intersection models with custom models
			modelSet := make(map[string]struct{})
			for _, m := range models {
				modelSet[m] = struct{}{}
			}
			for _, m := range customModels {
				modelSet[m] = struct{}{}
			}

			result := make([]string, 0, len(modelSet))
			for m := range modelSet {
				result = append(result, m)
			}
			sort.Strings(result)
			return result
		}

		return models
	}

	// For standard groups, only use V2 redirect rules (V1 has been migrated)
	modelSet := make(map[string]struct{})
	v2Rules := s.parseModelRedirectRulesV2(group.ModelRedirectRulesV2)
	for sourceModel := range v2Rules {
		modelSet[sourceModel] = struct{}{}
	}

	modelList := make([]string, 0, len(modelSet))
	for model := range modelSet {
		modelList = append(modelList, model)
	}
	sort.Strings(modelList)
	return modelList
}

// getAggregateGroupModels returns the intersection of models from all enabled sub-groups.
// Only models that exist in ALL sub-groups are returned, as aggregation requires
// the same virtual model to be available across all sub-groups.
func (s *HubService) getAggregateGroupModels(aggregateGroupID uint) []string {
	return s.getAggregateGroupModelsWithVisited(aggregateGroupID, make(map[uint]struct{}))
}

// getAggregateGroupModelsWithVisited returns the intersection of models with cycle detection.
// Uses visited set to prevent infinite recursion on circular group references.
func (s *HubService) getAggregateGroupModelsWithVisited(aggregateGroupID uint, visited map[uint]struct{}) []string {
	// Get sub-group relationships
	var subGroupRels []models.GroupSubGroup
	if err := s.db.Where("group_id = ? AND weight > 0", aggregateGroupID).Find(&subGroupRels).Error; err != nil {
		logrus.WithError(err).WithField("aggregate_group_id", aggregateGroupID).Warn("Failed to get sub-groups")
		return nil
	}

	if len(subGroupRels) == 0 {
		return nil
	}

	// Collect sub-group IDs, filtering out circular references
	// Fail closed on cycles for consistency with getGroupModelsWithVisited
	subGroupIDs := make([]uint, 0, len(subGroupRels))
	for _, sg := range subGroupRels {
		// Check for circular reference before querying
		if _, seen := visited[sg.SubGroupID]; seen {
			logrus.WithFields(logrus.Fields{
				"aggregate_group_id": aggregateGroupID,
				"sub_group_id":       sg.SubGroupID,
			}).Warn("Circular reference detected in sub-group hierarchy")
			// Fail closed: return nil to indicate invalid hierarchy
			// This prevents corrupted intersection results from entering selection
			return nil
		}
		subGroupIDs = append(subGroupIDs, sg.SubGroupID)
	}

	if len(subGroupIDs) == 0 {
		return nil
	}

	// Batch query all sub-groups at once to avoid N+1 queries
	var subGroups []models.Group
	if err := s.db.Select("id, name, group_type, model_redirect_rules_v2, custom_model_names, enabled").
		Where("id IN ?", subGroupIDs).
		Find(&subGroups).Error; err != nil {
		logrus.WithError(err).WithField("aggregate_group_id", aggregateGroupID).Warn("Failed to batch query sub-groups")
		return nil
	}

	// Build map for quick lookup
	subGroupMap := make(map[uint]*models.Group, len(subGroups))
	for i := range subGroups {
		subGroupMap[subGroups[i].ID] = &subGroups[i]
	}

	// Collect models from each enabled sub-group
	var allSubGroupModels []map[string]struct{}
	for _, sg := range subGroupRels {
		subGroup, exists := subGroupMap[sg.SubGroupID]
		if !exists || !subGroup.Enabled {
			continue
		}

		// Recursively get models with visited set (handles nested aggregates)
		subModels := s.getGroupModelsWithVisited(subGroup, visited)
		if len(subModels) == 0 {
			// If any sub-group has no models, intersection is empty
			return nil
		}

		modelSet := make(map[string]struct{}, len(subModels))
		for _, m := range subModels {
			modelSet[m] = struct{}{}
		}
		allSubGroupModels = append(allSubGroupModels, modelSet)
	}

	if len(allSubGroupModels) == 0 {
		return nil
	}

	// Calculate intersection: start with first sub-group's models
	intersection := allSubGroupModels[0]
	for i := 1; i < len(allSubGroupModels); i++ {
		newIntersection := make(map[string]struct{})
		for model := range intersection {
			if _, exists := allSubGroupModels[i][model]; exists {
				newIntersection[model] = struct{}{}
			}
		}
		intersection = newIntersection
		if len(intersection) == 0 {
			return nil
		}
	}

	// Convert to sorted slice
	result := make([]string, 0, len(intersection))
	for model := range intersection {
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}

// parseModelRedirectRulesV2 parses V2 model redirect rules from JSON.
func (s *HubService) parseModelRedirectRulesV2(rulesJSON []byte) map[string]*models.ModelRedirectRuleV2 {
	if len(rulesJSON) == 0 {
		return nil
	}

	// Skip empty JSON objects
	trimmed := string(rulesJSON)
	if trimmed == "{}" || trimmed == "null" {
		return nil
	}

	var rules map[string]*models.ModelRedirectRuleV2
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		return nil
	}

	return rules
}

// calculateGroupHealthScore calculates health score for a group based on request success rate.
// For standard groups, uses dynamic weight metrics (request success/failure statistics).
// For aggregate groups, calculates average health score of sub-groups.
// Health score is calculated as: weighted_success_rate with penalties for consecutive failures.
// - Returns 1.0 if group has no request history (considered healthy by default)
// - Returns value between MinHealthScore and 1.0 based on request success rate
// This is used for hub-level group selection to route requests to healthy groups.
func (s *HubService) calculateGroupHealthScore(group *models.Group) float64 {
	return s.calculateGroupHealthScoreWithVisited(group, make(map[uint]struct{}))
}

// calculateGroupHealthScoreWithVisited calculates health score with cycle detection.
// Uses path-scoped visited set to prevent infinite recursion on circular aggregate group references.
// The visited set is scoped to the current recursion path, allowing shared sub-groups in DAG structures.
func (s *HubService) calculateGroupHealthScoreWithVisited(group *models.Group, visited map[uint]struct{}) float64 {
	if group == nil {
		return 1.0
	}

	// For aggregate groups, calculate average health score of sub-groups
	if group.GroupType == "aggregate" {
		// Check for circular reference before recursing
		if _, seen := visited[group.ID]; seen {
			logrus.WithField("group_id", group.ID).Warn("Circular reference detected in aggregate group health calculation")
			return 1.0 // Fail-open on cycles
		}
		// Path-scoped visited: add current group to path, remove when done
		// This allows shared sub-groups in DAG structures while detecting true cycles
		visited[group.ID] = struct{}{}
		defer delete(visited, group.ID)
		return s.calculateAggregateGroupHealthScoreWithVisited(group.ID, visited)
	}

	// For standard groups, calculate based on request success rate using dynamic weight metrics
	if s.dynamicWeightManager == nil {
		logrus.WithField("group_id", group.ID).Warn("Dynamic weight manager not initialized, returning default health score")
		return 1.0 // Fail-open: assume healthy if manager not available
	}

	metrics, err := s.dynamicWeightManager.GetGroupMetrics(group.ID)
	if err != nil {
		logrus.WithError(err).WithField("group_id", group.ID).
			Warn("Failed to get group metrics for health score calculation")
		return 1.0 // Fail-open: assume healthy if query fails
	}

	// If no request history, consider group as healthy (1.0)
	if metrics.Requests180d == 0 {
		return 1.0
	}

	// Calculate health score using dynamic weight algorithm
	healthScore := s.dynamicWeightManager.CalculateHealthScore(metrics)
	return healthScore
}

// calculateAggregateGroupHealthScore calculates health score for an aggregate group.
// It returns the average health score of all enabled sub-groups.
// Returns 1.0 if no sub-groups are found (fail-open).
func (s *HubService) calculateAggregateGroupHealthScore(aggregateGroupID uint) float64 {
	return s.calculateAggregateGroupHealthScoreWithVisited(aggregateGroupID, make(map[uint]struct{}))
}

// calculateAggregateGroupHealthScoreWithVisited calculates health score with cycle detection.
// Uses path-scoped visited set to prevent infinite recursion on circular aggregate group references.
// The visited set is scoped to the current recursion path, allowing shared sub-groups in DAG structures.
func (s *HubService) calculateAggregateGroupHealthScoreWithVisited(aggregateGroupID uint, visited map[uint]struct{}) float64 {
	// Get sub-group relationships
	var subGroupRels []models.GroupSubGroup
	if err := s.db.Where("group_id = ? AND weight > 0", aggregateGroupID).
		Find(&subGroupRels).Error; err != nil {
		logrus.WithError(err).WithField("aggregate_group_id", aggregateGroupID).
			Warn("Failed to get sub-groups for health score calculation")
		return 1.0 // Fail-open
	}

	if len(subGroupRels) == 0 {
		return 1.0 // No sub-groups, consider healthy
	}

	// Collect sub-group IDs
	subGroupIDs := make([]uint, 0, len(subGroupRels))
	for _, sg := range subGroupRels {
		subGroupIDs = append(subGroupIDs, sg.SubGroupID)
	}

	// Load sub-groups
	var subGroups []models.Group
	if err := s.db.Where("id IN ? AND enabled = ?", subGroupIDs, true).
		Find(&subGroups).Error; err != nil {
		logrus.WithError(err).WithField("aggregate_group_id", aggregateGroupID).
			Warn("Failed to load sub-groups for health score calculation")
		return 1.0 // Fail-open
	}

	if len(subGroups) == 0 {
		return 1.0 // No enabled sub-groups, consider healthy
	}

	// Calculate weighted average health score of sub-groups
	// Weight by request count to reflect actual usage patterns:
	// - Frequently used healthy sub-groups dominate the health score
	// - Rarely used unhealthy sub-groups have minimal impact
	// - Sub-groups with no requests don't affect the score
	// This ensures aggregate health reflects actual performance, not theoretical worst-case.
	var totalWeightedHealthScore float64
	var totalWeight int64

	for _, subGroup := range subGroups {
		var subHealthScore float64
		var requestCount int64

		// If sub-group is also an aggregate group, recurse
		if subGroup.GroupType == "aggregate" {
			// Check for circular reference before recursing
			if _, seen := visited[subGroup.ID]; seen {
				logrus.WithField("sub_group_id", subGroup.ID).Warn("Circular reference detected in nested aggregate group")
				subHealthScore = 1.0 // Fail-open on cycles
				requestCount = 0    // Don't include in weighted average
			} else {
				// Path-scoped visited: add current group to path, remove when done
				visited[subGroup.ID] = struct{}{}
				subHealthScore = s.calculateAggregateGroupHealthScoreWithVisited(subGroup.ID, visited)
				delete(visited, subGroup.ID)

				// For nested aggregates, we don't have direct request count
				// Use a nominal weight of 1 if health is calculated, 0 otherwise
				// This ensures nested aggregates participate in health calculation
				// but don't dominate over sub-groups with actual metrics
				if subHealthScore > 0 {
					requestCount = 1
				}
			}
		} else {
			// For standard sub-groups, use sub-group metrics (performance within this aggregate)
			if s.dynamicWeightManager == nil {
				subHealthScore = 1.0 // Fail-open if manager not available
				requestCount = 0     // Don't include in weighted average
			} else {
				metrics, err := s.dynamicWeightManager.GetSubGroupMetrics(aggregateGroupID, subGroup.ID)
				if err != nil {
					logrus.WithError(err).WithFields(logrus.Fields{
						"aggregate_group_id": aggregateGroupID,
						"sub_group_id":       subGroup.ID,
					}).Warn("Failed to get sub-group metrics for health score calculation")
					subHealthScore = 1.0 // Fail-open
					requestCount = 0     // Don't include in weighted average
				} else if metrics.Requests180d == 0 {
					// No request history through this aggregate group
					// Don't include in weighted average (weight = 0)
					subHealthScore = 1.0
					requestCount = 0
				} else {
					// Calculate health score using dynamic weight algorithm
					subHealthScore = s.dynamicWeightManager.CalculateHealthScore(metrics)
					requestCount = metrics.Requests180d
				}
			}
		}

		// Add to weighted sum if this sub-group has been used
		if requestCount > 0 {
			totalWeightedHealthScore += subHealthScore * float64(requestCount)
			totalWeight += requestCount
		}
	}

	// If no sub-groups have request history, return 1.0 (healthy by default)
	if totalWeight == 0 {
		return 1.0
	}

	// Return weighted average
	weightedAvgHealthScore := totalWeightedHealthScore / float64(totalWeight)
	return weightedAvgHealthScore
}

// SelectGroupForModel selects the best group for a given model with relay format awareness.
// Selection algorithm with early filtering optimization:
// 1. Filter by model availability
// 2. Filter by enabled status, priority (Sort >= 1000 means disabled), and health score
// 3. Filter by channel compatibility with relay format
// 4. For Claude format, verify target channel has cc_support enabled
// 5. For aggregate groups, check preconditions (e.g., max_request_size_kb) - EARLY FILTERING
//    - Batch load preconditions for all aggregate groups (avoid N+1 queries)
//    - Filter out groups that don't meet preconditions before selection
//    - This prevents unsuitable groups from entering the selection process
// 6. Prioritize native channel type for the format
// 7. Select by sort order (priority) and weight
//
// IMPORTANT: Priority semantics - LOWER value = HIGHER priority
// - priority=1: Highest priority
// - priority=999: Lowest priority
// - priority=1000: Disabled (filtered out)
func (s *HubService) SelectGroupForModel(ctx context.Context, modelName string, relayFormat types.RelayFormat, requestSizeKB int) (*models.Group, error) {
	pool, err := s.GetModelPool(ctx)
	if err != nil {
		return nil, err
	}

	// Find the model in the pool
	var allSources []ModelSource
	for _, entry := range pool {
		if entry.ModelName == modelName {
			// Flatten sources from all channel types
			for _, sources := range entry.SourcesByType {
				allSources = append(allSources, sources...)
			}
			break
		}
	}

	if len(allSources) == 0 {
		return nil, nil // Model not found
	}

	// Get health score threshold once
	healthThreshold := s.GetHealthScoreThreshold()

	// For Claude format requests, we need to check cc_support config
	// Load group configs if this is a Claude format request to non-Anthropic channels
	needCCSupport := relayFormat == types.RelayFormatClaude
	var groupConfigs map[uint]*models.Group
	if needCCSupport {
		groupConfigs = make(map[uint]*models.Group)
		// Collect group IDs that need config check (non-Anthropic channels)
		var groupIDs []uint
		for _, source := range allSources {
			if source.ChannelType != "anthropic" {
				groupIDs = append(groupIDs, source.GroupID)
			}
		}
		// Load configs in batch for performance
		if len(groupIDs) > 0 {
			var groups []models.Group
			if err := s.db.WithContext(ctx).
				Select("id, channel_type, config").
				Where("id IN ?", groupIDs).
				Find(&groups).Error; err != nil {
				logrus.WithError(err).Warn("Failed to load group configs for CC support check")
			} else {
				for i := range groups {
					groupConfigs[groups[i].ID] = &groups[i]
				}
			}
		}
	}

	// Load preconditions for aggregate groups to check request size limits
	// OPTIMIZATION: Batch load all preconditions at once to avoid N+1 queries
	// This is an early filtering step - unsuitable groups are excluded before selection
	var groupPreconditionsMap map[uint]*models.Group
	if requestSizeKB > 0 {
		groupPreconditionsMap = make(map[uint]*models.Group)
		// Collect aggregate group IDs
		var aggregateGroupIDs []uint
		for _, source := range allSources {
			if source.GroupType == "aggregate" {
				aggregateGroupIDs = append(aggregateGroupIDs, source.GroupID)
			}
		}
		// Load preconditions in batch for performance
		if len(aggregateGroupIDs) > 0 {
			var groups []models.Group
			if err := s.db.WithContext(ctx).
				Select("id, preconditions").
				Where("id IN ?", aggregateGroupIDs).
				Find(&groups).Error; err != nil {
				logrus.WithError(err).Warn("Failed to load group preconditions")
			} else {
				for i := range groups {
					groupPreconditionsMap[groups[i].ID] = &groups[i]
				}
			}
		}
	}

	// Filter by channel compatibility and health
	// Separate native and compatible channels for priority handling
	var nativeSources []ModelSource
	var compatibleSources []ModelSource
	nativeChannel := GetNativeChannel(relayFormat)
	preconditionsMissingLogged := false // Track if we've logged the preconditions warning to avoid log spam

	for _, source := range allSources {
		// Skip disabled, unhealthy, or disabled-priority sources
		// Priority >= 1000 means disabled (as documented in function comments)
		if !source.Enabled || source.Sort >= 1000 || source.HealthScore < healthThreshold {
			continue
		}

		// Check channel compatibility
		if !IsChannelCompatible(source.ChannelType, relayFormat) {
			continue
		}

		// For Claude format requests to non-Anthropic channels, verify cc_support is enabled
		if needCCSupport && source.ChannelType != "anthropic" {
			group, ok := groupConfigs[source.GroupID]
			if !ok {
				// Config not loaded, skip this source for safety
				logrus.WithFields(logrus.Fields{
					"group_id":     source.GroupID,
					"channel_type": source.ChannelType,
				}).Debug("Skipping source: config not loaded for CC support check")
				continue
			}
			// Check if cc_support is enabled
			if !s.isGroupCCSupportEnabled(group) {
				logrus.WithFields(logrus.Fields{
					"group_id":     source.GroupID,
					"group_name":   source.GroupName,
					"channel_type": source.ChannelType,
				}).Debug("Skipping source: cc_support not enabled for Claude format request")
				continue
			}
		}

		// Check preconditions for aggregate groups - EARLY FILTERING
		// This prevents unsuitable groups from entering the selection process
		if source.GroupType == "aggregate" && requestSizeKB > 0 {
			group, ok := groupPreconditionsMap[source.GroupID]
			if !ok {
				// Fail-open: allow aggregate group if preconditions cannot be verified
				// Rationale: Temporary DB issues should not block all aggregate routing.
				// This prioritizes availability over strict precondition enforcement.
				// The downstream group selection will still apply health checks.
				// Log only once per request to avoid log spam during DB issues
				if !preconditionsMissingLogged {
					logrus.WithFields(logrus.Fields{
						"group_id":        source.GroupID,
						"group_name":      source.GroupName,
						"request_size_kb": requestSizeKB,
					}).Warn("Preconditions not loaded; allowing aggregate group (fail-open)")
					preconditionsMissingLogged = true
				}
			} else {
				maxSizeKB := group.GetMaxRequestSizeKB()
				// Skip this aggregate group if request size exceeds limit
				if maxSizeKB > 0 && requestSizeKB > maxSizeKB {
					logrus.WithFields(logrus.Fields{
						"group_id":        source.GroupID,
						"group_name":      source.GroupName,
						"request_size_kb": requestSizeKB,
						"max_size_kb":     maxSizeKB,
					}).Debug("Skipping aggregate group: request size exceeds precondition limit")
					continue
				}
			}
		}

		// Separate native and compatible channels
		if source.ChannelType == nativeChannel {
			nativeSources = append(nativeSources, source)
		} else {
			compatibleSources = append(compatibleSources, source)
		}
	}

	// Try native channels first (highest priority)
	if len(nativeSources) > 0 {
		return s.selectFromSources(nativeSources)
	}

	// Fallback to compatible channels
	if len(compatibleSources) > 0 {
		return s.selectFromSources(compatibleSources)
	}

	return nil, nil // No compatible healthy groups available
}

// selectFromSources selects the best source from a list based on sort order and weight.
// This is a helper method extracted from SelectGroupForModel for reusability.
// Selection algorithm:
// 1. Find the minimum priority value (which represents the highest priority)
// 2. Get all groups with that minimum priority
// 3. If only one group, select it
// 4. If multiple groups, use weighted random selection based on health_score
//
// IMPORTANT: Priority semantics - LOWER value = HIGHER priority
// - priority=1: Highest priority
// - priority=999: Lowest priority
// - priority=1000: Disabled (filtered out before calling this function)
func (s *HubService) selectFromSources(sources []ModelSource) (*models.Group, error) {
	if len(sources) == 0 {
		return nil, nil
	}

	// Guard against nil GroupManager
	if s.groupManager == nil {
		return nil, fmt.Errorf("groupManager is not initialized")
	}

	// Find the minimum sort value
	minSort := sources[0].Sort
	for i := 1; i < len(sources); i++ {
		if sources[i].Sort < minSort {
			minSort = sources[i].Sort
		}
	}

	// Get all sources with the minimum sort value
	var topSources []ModelSource
	for _, source := range sources {
		if source.Sort == minSort {
			topSources = append(topSources, source)
		}
	}

	// If only one source with top priority, use it
	var selectedSource ModelSource
	if len(topSources) == 1 {
		selectedSource = topSources[0]
	} else {
		// Multiple sources with same priority - use weighted random selection
		weights := make([]int, len(topSources))
		for i, source := range topSources {
			// Convert float effective weight to int for weighted selection
			weights[i] = services.GetEffectiveWeightForSelection(source.EffectiveWeight)
		}

		idx := utils.WeightedRandomSelect(weights)
		if idx < 0 {
			// Fallback to first source if weighted selection fails
			selectedSource = topSources[0]
		} else {
			selectedSource = topSources[idx]
		}
	}

	// Get the full group from GroupManager
	return s.groupManager.GetGroupByID(selectedSource.GroupID)
}

// InvalidateModelPoolCache invalidates the model pool cache.
// Should be called when groups are created, updated, or deleted.
func (s *HubService) InvalidateModelPoolCache() {
	s.modelPoolCacheMu.Lock()
	s.modelPoolCache = nil
	s.modelPoolCacheMu.Unlock()
	logrus.Debug("Model pool cache invalidated")
}

// updateModelPoolCacheHit updates hit statistics and extends TTL for frequently accessed cache.
// This implements adaptive TTL: entries with high hit counts get extended TTL up to maxTTL.
func (s *HubService) updateModelPoolCacheHit() {
	s.modelPoolCacheMu.Lock()
	defer s.modelPoolCacheMu.Unlock()

	if s.modelPoolCache == nil || time.Now().After(s.modelPoolCache.ExpiresAt) {
		return
	}

	s.modelPoolCache.HitCount++

	// Extend TTL if hit threshold is reached and not at max TTL
	if s.modelPoolCache.HitCount >= cacheHitThreshold && s.modelPoolCache.CurrentTTL < s.modelPoolMaxTTL {
		newTTL := time.Duration(float64(s.modelPoolCache.CurrentTTL) * cacheTTLMultiplier)
		if newTTL > s.modelPoolMaxTTL {
			newTTL = s.modelPoolMaxTTL
		}
		s.modelPoolCache.CurrentTTL = newTTL
		s.modelPoolCache.ExpiresAt = time.Now().Add(newTTL)
		s.modelPoolCache.HitCount = 0 // Reset hit count after extension
	}
}

// GetAvailableModels returns a list of all available model names.
// This is a convenience method for the /v1/hub/models endpoint.
func (s *HubService) GetAvailableModels(ctx context.Context) ([]string, error) {
	pool, err := s.GetModelPool(ctx)
	if err != nil {
		return nil, err
	}

	// Get health score threshold once
	healthThreshold := s.GetHealthScoreThreshold()

	availableModels := make([]string, 0, len(pool))
	for _, entry := range pool {
		// Only include models that have at least one healthy source
		hasHealthySource := false
		for _, sources := range entry.SourcesByType {
			for _, source := range sources {
				// Align with SelectGroupForModel: skip disabled-priority sources (Sort >= 1000)
				if source.Enabled && source.Sort < 1000 && source.HealthScore >= healthThreshold {
					hasHealthySource = true
					break
				}
			}
			if hasHealthySource {
				break
			}
		}
		if hasHealthySource {
			availableModels = append(availableModels, entry.ModelName)
		}
	}

	return availableModels, nil
}

// GetModelSources returns the sources for a specific model, organized by channel type.
// Returns nil if the model is not found.
// Returns a deep copy to prevent cache corruption.
func (s *HubService) GetModelSources(ctx context.Context, modelName string) (map[string][]ModelSource, error) {
	pool, err := s.GetModelPool(ctx)
	if err != nil {
		return nil, err
	}

	for _, entry := range pool {
		if entry.ModelName == modelName {
			// Return a deep copy to prevent cache corruption
			// GetModelPool already returns deep copies, so entry.SourcesByType is safe to return
			// However, we add this comment to clarify the safety guarantee
			return entry.SourcesByType, nil
		}
	}

	return nil, nil
}

// IsModelAvailable checks if a model is available in the hub.
func (s *HubService) IsModelAvailable(ctx context.Context, modelName string) (bool, error) {
	sourcesByType, err := s.GetModelSources(ctx, modelName)
	if err != nil {
		return false, err
	}

	if sourcesByType == nil {
		return false, nil
	}

	// Get health score threshold once
	healthThreshold := s.GetHealthScoreThreshold()

	// Check if at least one source is healthy
	for _, sources := range sourcesByType {
		for _, source := range sources {
			// Align with SelectGroupForModel: skip disabled-priority sources (Sort >= 1000)
			if source.Enabled && source.Sort < 1000 && source.HealthScore >= healthThreshold {
				return true, nil
			}
		}
	}

	return false, nil
}

// GetModelPoolV2 returns the model pool with priority information.
// This is used for the admin UI to display and edit model-group priorities.
func (s *HubService) GetModelPoolV2(ctx context.Context) ([]ModelPoolEntryV2, error) {
	pool, err := s.GetModelPool(ctx)
	if err != nil {
		return nil, err
	}

	// Load all groups to check for custom models
	var groups []models.Group
	if err := s.db.WithContext(ctx).
		Select("id, custom_model_names").
		Where("enabled = ?", true).
		Find(&groups).Error; err != nil {
		logrus.WithError(err).Warn("Failed to load groups for custom model check")
	}

	// Build custom model set
	customModelSet := make(map[string]bool)
	for _, group := range groups {
		customModels := s.parseCustomModelNames(group.CustomModelNames)
		for _, modelName := range customModels {
			customModelSet[modelName] = true
		}
	}

	// Load all priority configurations
	var priorities []HubModelGroupPriority
	if err := s.db.WithContext(ctx).Find(&priorities).Error; err != nil {
		logrus.WithError(err).Warn("Failed to load model group priorities")
		// Continue without priorities
	}

	// Build priority lookup map: model_name -> group_id -> priority
	priorityMap := make(map[string]map[uint]int)
	for _, p := range priorities {
		if priorityMap[p.ModelName] == nil {
			priorityMap[p.ModelName] = make(map[uint]int)
		}
		priorityMap[p.ModelName][p.GroupID] = p.Priority
	}

	// Convert to V2 format with priority info
	result := make([]ModelPoolEntryV2, 0, len(pool))
	for _, entry := range pool {
		// Flatten all sources from all channel types
		var allSources []ModelSource
		for _, sources := range entry.SourcesByType {
			allSources = append(allSources, sources...)
		}

		groups := make([]ModelGroupPriorityDTO, 0, len(allSources))
		for _, source := range allSources {
			// Get priority from map, default to 100 if not set
			priority := 100
			if modelPriorities, ok := priorityMap[entry.ModelName]; ok {
				if p, ok := modelPriorities[source.GroupID]; ok {
					priority = p
				}
			}

			groups = append(groups, ModelGroupPriorityDTO{
				GroupID:      source.GroupID,
				GroupName:    source.GroupName,
				GroupType:    source.GroupType,
				IsChildGroup: source.IsChildGroup,
				ChannelType:  source.ChannelType,
				Priority:     priority,
				HealthScore:  source.HealthScore,
				Enabled:      source.Enabled,
			})
		}

		// Sort groups by priority (lower first), then by group name
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].Priority != groups[j].Priority {
				return groups[i].Priority < groups[j].Priority
			}
			return groups[i].GroupName < groups[j].GroupName
		})

		// Check if this model is custom
		isCustom := customModelSet[entry.ModelName]

		result = append(result, ModelPoolEntryV2{
			ModelName: entry.ModelName,
			Groups:    groups,
			IsCustom:  isCustom,
		})
	}

	return result, nil
}

// UpdateModelGroupPriority updates the priority for a model-group pair.
// Valid priority range: 1-999 (lower value = higher priority).
// Priority 1000 is reserved for internal use (disabled state) and cannot be set by users.
// IMPORTANT: Priority semantics - LOWER value = HIGHER priority
// - priority=1: Highest priority
// - priority=999: Lowest priority
// - priority=1000: Reserved for disabled state (internal use only)
func (s *HubService) UpdateModelGroupPriority(ctx context.Context, modelName string, groupID uint, priority int) error {
	// Validate priority range: only 1-999 are allowed for user input
	// Priority 1000 is reserved for internal disabled state
	if priority < 1 || priority > 999 {
		return ErrInvalidPriority
	}

	// Check if record exists
	var existing HubModelGroupPriority
	err := s.db.WithContext(ctx).
		Where("model_name = ? AND group_id = ?", modelName, groupID).
		First(&existing).Error

	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}

	if err == gorm.ErrRecordNotFound {
		// Create new record
		record := HubModelGroupPriority{
			ModelName: modelName,
			GroupID:   groupID,
			Priority:  priority,
		}
		return s.db.WithContext(ctx).Create(&record).Error
	}

	// Update existing record
	return s.db.WithContext(ctx).
		Model(&existing).
		Update("priority", priority).Error
}

// BatchUpdateModelGroupPriorities updates multiple model-group priorities at once.
// Invalid priorities (outside 1-999 range) are silently skipped with a warning log,
// allowing the batch operation to partially succeed rather than failing entirely.
// This design choice enables resilient batch operations where some updates may have
// validation issues while others can proceed successfully.
//
// Design Note: Callers receive no indication of which updates were skipped.
// This is intentional to maintain API simplicity and backward compatibility.
// Skipped updates are logged with logrus.Warn for operational monitoring.
// If detailed feedback is needed in the future, consider returning a summary
// struct (e.g., {updated: N, skipped: M, skippedItems: []...}) instead of error.
func (s *HubService) BatchUpdateModelGroupPriorities(ctx context.Context, updates []UpdateModelGroupPriorityParams) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, update := range updates {
			// Validate priority range: only 1-999 are allowed for user input
			// Priority 1000 is reserved for internal disabled state
			if update.Priority < 1 || update.Priority > 999 {
				// Log skipped invalid priorities for debugging
				logrus.WithFields(logrus.Fields{
					"model_name": update.ModelName,
					"group_id":   update.GroupID,
					"priority":   update.Priority,
				}).Warn("Skipping invalid priority in batch update")
				continue
			}

			var existing HubModelGroupPriority
			err := tx.Where("model_name = ? AND group_id = ?", update.ModelName, update.GroupID).
				First(&existing).Error

			if err == gorm.ErrRecordNotFound {
				record := HubModelGroupPriority{
					ModelName: update.ModelName,
					GroupID:   update.GroupID,
					Priority:  update.Priority,
				}
				if err := tx.Create(&record).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else {
				if err := tx.Model(&existing).Update("priority", update.Priority).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// GetHubSettings returns the current Hub settings.
func (s *HubService) GetHubSettings(ctx context.Context) (*HubSettingsDTO, error) {
	var settings HubSettings
	err := s.db.WithContext(ctx).First(&settings).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// Return default settings
			return &HubSettingsDTO{
				MaxRetries:          3,
				RetryDelay:          100,
				HealthThreshold:     0.3,
				EnablePriority:      true,
				OnlyAggregateGroups: true,
			}, nil
		}
		return nil, err
	}

	return &HubSettingsDTO{
		MaxRetries:          settings.MaxRetries,
		RetryDelay:          settings.RetryDelay,
		HealthThreshold:     settings.HealthThreshold,
		EnablePriority:      settings.EnablePriority,
		OnlyAggregateGroups: settings.OnlyAggregateGroups,
	}, nil
}

// UpdateHubSettings updates the Hub settings.
// After successful DB update, also updates in-memory state and invalidates cache
// to ensure routing uses fresh values immediately.
func (s *HubService) UpdateHubSettings(ctx context.Context, dto *HubSettingsDTO) error {
	var settings HubSettings
	err := s.db.WithContext(ctx).First(&settings).Error

	if err == gorm.ErrRecordNotFound {
		// Create new settings
		settings = HubSettings{
			MaxRetries:          dto.MaxRetries,
			RetryDelay:          dto.RetryDelay,
			HealthThreshold:     dto.HealthThreshold,
			EnablePriority:      dto.EnablePriority,
			OnlyAggregateGroups: dto.OnlyAggregateGroups,
		}
		if err := s.db.WithContext(ctx).Create(&settings).Error; err != nil {
			return err
		}
		// Update in-memory state after successful DB write
		s.SetHealthScoreThreshold(dto.HealthThreshold)
		s.SetOnlyAggregateGroups(dto.OnlyAggregateGroups)
		s.InvalidateModelPoolCache()
		return nil
	}

	if err != nil {
		return err
	}

	// Update existing settings
	if err := s.db.WithContext(ctx).Model(&settings).Updates(map[string]any{
		"max_retries":           dto.MaxRetries,
		"retry_delay":           dto.RetryDelay,
		"health_threshold":      dto.HealthThreshold,
		"enable_priority":       dto.EnablePriority,
		"only_aggregate_groups": dto.OnlyAggregateGroups,
	}).Error; err != nil {
		return err
	}
	// Update in-memory state after successful DB write
	s.SetHealthScoreThreshold(dto.HealthThreshold)
	s.SetOnlyAggregateGroups(dto.OnlyAggregateGroups)
	s.InvalidateModelPoolCache()
	return nil
}

// SelectGroupForModelWithPriority selects the best group for a model using priority-based routing with relay format awareness.
// Groups are tried in priority order (lower priority value = higher priority).
// Within the same priority level, weighted random selection is used.
// Native channels for the relay format are preferred over compatible channels.
func (s *HubService) SelectGroupForModelWithPriority(ctx context.Context, modelName string, relayFormat types.RelayFormat) (*models.Group, error) {
	poolV2, err := s.GetModelPoolV2(ctx)
	if err != nil {
		return nil, err
	}

	// Find the model in the pool
	var groups []ModelGroupPriorityDTO
	for _, entry := range poolV2 {
		if entry.ModelName == modelName {
			groups = entry.Groups
			break
		}
	}

	if len(groups) == 0 {
		return nil, nil // Model not found
	}

	// Get health score threshold once
	healthThreshold := s.GetHealthScoreThreshold()

	// Filter by channel compatibility, enabled, non-zero priority, and health score
	// Separate native and compatible channels for priority handling
	var nativeGroups []ModelGroupPriorityDTO
	var compatibleGroups []ModelGroupPriorityDTO
	nativeChannel := GetNativeChannel(relayFormat)

	for _, g := range groups {
		// Skip disabled, disabled-priority, or unhealthy groups
		if !g.Enabled || g.Priority >= 1000 || g.HealthScore < healthThreshold {
			continue
		}

		// Check channel compatibility
		if !IsChannelCompatible(g.ChannelType, relayFormat) {
			continue
		}

		// Separate native and compatible channels
		if g.ChannelType == nativeChannel {
			nativeGroups = append(nativeGroups, g)
		} else {
			compatibleGroups = append(compatibleGroups, g)
		}
	}

	// Try native channels first (highest priority)
	if len(nativeGroups) > 0 {
		return s.selectFromPriorityGroups(nativeGroups)
	}

	// Fallback to compatible channels
	if len(compatibleGroups) > 0 {
		return s.selectFromPriorityGroups(compatibleGroups)
	}

	return nil, nil // No compatible healthy groups available
}

// selectFromPriorityGroups selects the best group from a list based on priority and health score.
// This is a helper method extracted for reusability.
func (s *HubService) selectFromPriorityGroups(groups []ModelGroupPriorityDTO) (*models.Group, error) {
	if len(groups) == 0 {
		return nil, nil
	}

	// Guard against nil GroupManager
	if s.groupManager == nil {
		return nil, fmt.Errorf("groupManager is not initialized")
	}

	// Find the minimum priority value (highest priority)
	minPriority := groups[0].Priority
	for i := 1; i < len(groups); i++ {
		if groups[i].Priority < minPriority {
			minPriority = groups[i].Priority
		}
	}

	// Get all groups with the minimum priority
	var topGroups []ModelGroupPriorityDTO
	for _, g := range groups {
		if g.Priority == minPriority {
			topGroups = append(topGroups, g)
		}
	}

	// Select from top priority groups
	var selectedGroupID uint
	if len(topGroups) == 1 {
		selectedGroupID = topGroups[0].GroupID
	} else {
		// Multiple groups with same priority - use weighted random selection based on health score
		weights := make([]int, len(topGroups))
		for i, g := range topGroups {
			weights[i] = int(g.HealthScore * 100)
			if weights[i] < 1 {
				weights[i] = 1
			}
		}

		idx := utils.WeightedRandomSelect(weights)
		if idx < 0 {
			selectedGroupID = topGroups[0].GroupID
		} else {
			selectedGroupID = topGroups[idx].GroupID
		}
	}

	// Get the full group from GroupManager
	return s.groupManager.GetGroupByID(selectedGroupID)
}

// parseCustomModelNames parses custom model names from JSON array.
// Returns empty slice if parsing fails or JSON is empty.
func (s *HubService) parseCustomModelNames(customModelNamesJSON []byte) []string {
	if len(customModelNamesJSON) == 0 {
		return nil
	}

	// Skip empty JSON arrays
	trimmed := string(customModelNamesJSON)
	if trimmed == "[]" || trimmed == "null" {
		return nil
	}

	var modelNames []string
	if err := json.Unmarshal(customModelNamesJSON, &modelNames); err != nil {
		logrus.WithError(err).Warn("Failed to parse custom model names")
		return nil
	}

	return modelNames
}

// isGroupCCSupportEnabled checks if cc_support is enabled for the given group.
// CC support allows Claude format requests to be converted to the target channel format.
// Only applicable to openai, gemini, and codex channel types.
func (s *HubService) isGroupCCSupportEnabled(group *models.Group) bool {
	if group == nil {
		return false
	}
	// Only openai, gemini, and codex channels support CC mode
	if group.ChannelType != "openai" && group.ChannelType != "gemini" && group.ChannelType != "codex" {
		return false
	}
	// Check cc_support flag in config
	if group.Config == nil {
		return false
	}
	raw, ok := group.Config["cc_support"]
	if !ok || raw == nil {
		return false
	}
	// Handle multiple types for flexibility
	switch v := raw.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case int:
		return v != 0
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		return lower == "true" || lower == "1" || lower == "yes" || lower == "on"
	default:
		return false
	}
}

// ErrInvalidPriority is returned when priority value is out of range.
var ErrInvalidPriority = &InvalidPriorityError{}

// InvalidPriorityError represents an invalid priority value error.
type InvalidPriorityError struct{}

func (e *InvalidPriorityError) Error() string {
	return "priority must be between 1 and 999 (1=highest, 999=lowest). Priority 1000 is reserved for internal use"
}

// InvalidGroupTypeError represents an error when trying to set custom models on a non-aggregate group.
type InvalidGroupTypeError struct {
	GroupID   uint
	GroupType string
}

func (e *InvalidGroupTypeError) Error() string {
	return fmt.Sprintf("group %d is not an aggregate group (type: %s)", e.GroupID, e.GroupType)
}

// GetAggregateGroupsCustomModels returns custom model names for all aggregate groups.
// This is used in the Hub centralized management UI to display and edit custom models.
func (s *HubService) GetAggregateGroupsCustomModels(ctx context.Context) ([]AggregateGroupCustomModels, error) {
	var groups []models.Group
	if err := s.db.WithContext(ctx).
		Select("id, name, custom_model_names").
		Where("group_type = ? AND enabled = ?", "aggregate", true).
		Order("sort ASC, name ASC").
		Find(&groups).Error; err != nil {
		return nil, err
	}

	result := make([]AggregateGroupCustomModels, 0, len(groups))
	for _, group := range groups {
		customModels := s.parseCustomModelNames(group.CustomModelNames)
		result = append(result, AggregateGroupCustomModels{
			GroupID:          group.ID,
			GroupName:        group.Name,
			CustomModelNames: customModels,
		})
	}

	return result, nil
}

// UpdateAggregateGroupCustomModels updates custom model names for an aggregate group.
// This invalidates the model pool cache to reflect changes immediately.
func (s *HubService) UpdateAggregateGroupCustomModels(ctx context.Context, params UpdateCustomModelsParams) error {
	// Verify group exists and is an aggregate group
	var group models.Group
	if err := s.db.WithContext(ctx).
		Select("id, group_type").
		Where("id = ?", params.GroupID).
		First(&group).Error; err != nil {
		return err
	}

	if group.GroupType != "aggregate" {
		return &InvalidGroupTypeError{GroupID: params.GroupID, GroupType: group.GroupType}
	}

	// Filter out empty strings and duplicates
	uniqueModels := make(map[string]struct{})
	for _, model := range params.CustomModelNames {
		trimmed := strings.TrimSpace(model)
		if trimmed != "" {
			uniqueModels[trimmed] = struct{}{}
		}
	}

	// Convert to sorted slice for consistent ordering
	cleanedModels := make([]string, 0, len(uniqueModels))
	for model := range uniqueModels {
		cleanedModels = append(cleanedModels, model)
	}
	sort.Strings(cleanedModels)

	// Serialize to JSON
	customModelsJSON, err := json.Marshal(cleanedModels)
	if err != nil {
		return err
	}

	// Update database
	if err := s.db.WithContext(ctx).
		Model(&models.Group{}).
		Where("id = ?", params.GroupID).
		Update("custom_model_names", datatypes.JSON(customModelsJSON)).Error; err != nil {
		return err
	}

	// Invalidate model pool cache
	s.InvalidateModelPoolCache()

	logrus.WithFields(logrus.Fields{
		"group_id":    params.GroupID,
		"model_count": len(cleanedModels),
	}).Info("Updated aggregate group custom models")

	return nil
}
