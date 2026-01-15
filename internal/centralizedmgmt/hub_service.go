// Package centralizedmgmt provides centralized API management for GPT-Load.
// It aggregates models from all groups and provides a unified API endpoint.
package centralizedmgmt

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	// Default TTL for model pool cache
	defaultModelPoolCacheTTL = 30 * time.Second

	// Maximum TTL for adaptive extension
	defaultModelPoolCacheMaxTTL = 2 * time.Minute

	// Default health score threshold for group selection
	defaultHealthScoreThreshold = 0.5

	// Hit threshold for adaptive TTL extension
	cacheHitThreshold = 10

	// TTL multiplier for adaptive extension
	cacheTTLMultiplier = 1.2
)

// ModelPoolEntry represents a model with its source groups.
// Used for displaying the aggregated model pool in the UI.
type ModelPoolEntry struct {
	ModelName string        `json:"model_name"`
	Sources   []ModelSource `json:"sources"`
}

// ModelSource represents a group that provides a model.
// Contains all information needed for group selection and display.
type ModelSource struct {
	GroupID         uint    `json:"group_id"`
	GroupName       string  `json:"group_name"`
	GroupType       string  `json:"group_type"`    // "standard" or "aggregate"
	IsChildGroup    bool    `json:"is_child_group"` // True if this is a child group of a standard group
	ChannelType     string  `json:"channel_type"`  // Channel type (e.g., "openai", "claude", etc.)
	Sort            int     `json:"sort"`
	Weight          int     `json:"weight"`
	HealthScore     float64 `json:"health_score"`
	EffectiveWeight int     `json:"effective_weight"`
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

	// Health score threshold for group selection
	healthScoreThreshold float64
}

// NewHubService creates a new HubService instance.
func NewHubService(
	db *gorm.DB,
	groupManager *services.GroupManager,
	dynamicWeightManager *services.DynamicWeightManager,
) *HubService {
	return &HubService{
		db:                   db,
		groupManager:         groupManager,
		dynamicWeightManager: dynamicWeightManager,
		modelPoolCacheTTL:    defaultModelPoolCacheTTL,
		modelPoolMaxTTL:      defaultModelPoolCacheMaxTTL,
		healthScoreThreshold: defaultHealthScoreThreshold,
	}
}

// SetHealthScoreThreshold sets the minimum health score for group selection.
func (s *HubService) SetHealthScoreThreshold(threshold float64) {
	s.healthScoreThreshold = threshold
}

// GetModelPool returns the aggregated model pool from all enabled groups.
// Uses caching with adaptive TTL for performance.
func (s *HubService) GetModelPool(ctx context.Context) ([]ModelPoolEntry, error) {
	// Check cache first with adaptive TTL support
	s.modelPoolCacheMu.RLock()
	if s.modelPoolCache != nil && time.Now().Before(s.modelPoolCache.ExpiresAt) {
		pool := make([]ModelPoolEntry, len(s.modelPoolCache.Pool))
		copy(pool, s.modelPoolCache.Pool)
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

// buildModelPool aggregates models from all enabled groups.
func (s *HubService) buildModelPool(ctx context.Context) ([]ModelPoolEntry, error) {
	// Get all groups from database (only V2 rules needed, V1 has been migrated)
	var groups []models.Group
	if err := s.db.WithContext(ctx).
		Select("id, name, display_name, group_type, channel_type, enabled, sort, model_redirect_rules_v2, config, parent_group_id").
		Where("enabled = ?", true).
		Order("sort ASC, id ASC").
		Find(&groups).Error; err != nil {
		return nil, err
	}

	// Map to aggregate models by name
	modelMap := make(map[string][]ModelSource)

	for _, group := range groups {
		// Get models for this group (from V2 redirect rules)
		groupModels := s.getGroupModels(&group)

		// Calculate health score and effective weight
		healthScore := 1.0
		baseWeight := 100 // Default weight for standard groups

		// For aggregate groups, we don't have a single weight
		// For standard groups, use default weight
		if s.dynamicWeightManager != nil {
			// Use a simple health score based on group metrics
			// For hub purposes, we use a simplified calculation
			healthScore = s.calculateGroupHealthScore(&group)
		}

		effectiveWeight := int(float64(baseWeight) * healthScore)
		if effectiveWeight < 1 && healthScore >= s.healthScoreThreshold {
			effectiveWeight = 1
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
			modelMap[modelName] = append(modelMap[modelName], source)
		}
	}

	// Convert map to sorted slice
	pool := make([]ModelPoolEntry, 0, len(modelMap))
	for modelName, sources := range modelMap {
		// Sort sources by sort field (ascending)
		sort.Slice(sources, func(i, j int) bool {
			if sources[i].Sort != sources[j].Sort {
				return sources[i].Sort < sources[j].Sort
			}
			return sources[i].GroupID < sources[j].GroupID
		})

		pool = append(pool, ModelPoolEntry{
			ModelName: modelName,
			Sources:   sources,
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
	if group.GroupType == "aggregate" {
		// For aggregate groups, get intersection of models from all sub-groups
		return s.getAggregateGroupModels(group.ID)
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
	// Get sub-group relationships
	var subGroupRels []models.GroupSubGroup
	if err := s.db.Where("group_id = ? AND weight > 0", aggregateGroupID).Find(&subGroupRels).Error; err != nil {
		logrus.WithError(err).WithField("aggregate_group_id", aggregateGroupID).Warn("Failed to get sub-groups")
		return nil
	}

	if len(subGroupRels) == 0 {
		return nil
	}

	// Collect models from each enabled sub-group
	var allSubGroupModels []map[string]struct{}
	for _, sg := range subGroupRels {
		var subGroup models.Group
		if err := s.db.Select("id, name, group_type, model_redirect_rules_v2, enabled").
			First(&subGroup, sg.SubGroupID).Error; err != nil {
			continue
		}

		if !subGroup.Enabled {
			continue
		}

		// Recursively get models (handles nested aggregates)
		subModels := s.getGroupModels(&subGroup)
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

// calculateGroupHealthScore calculates a simplified health score for a group.
// This is used for hub-level group selection, not for sub-group selection within aggregates.
func (s *HubService) calculateGroupHealthScore(group *models.Group) float64 {
	if s.dynamicWeightManager == nil {
		return 1.0
	}

	// For now, return 1.0 as we don't have group-level metrics
	// In the future, this could aggregate metrics from all keys in the group
	return 1.0
}

// SelectGroupForModel selects the best group for a given model.
// Selection is based on: enabled status, health score >= threshold, sort order (priority), and weight.
func (s *HubService) SelectGroupForModel(ctx context.Context, modelName string) (*models.Group, error) {
	pool, err := s.GetModelPool(ctx)
	if err != nil {
		return nil, err
	}

	// Find the model in the pool
	var sources []ModelSource
	for _, entry := range pool {
		if entry.ModelName == modelName {
			sources = entry.Sources
			break
		}
	}

	if len(sources) == 0 {
		return nil, nil // Model not found
	}

	// Filter by enabled and health score threshold
	var validSources []ModelSource
	for _, source := range sources {
		if source.Enabled && source.HealthScore >= s.healthScoreThreshold {
			validSources = append(validSources, source)
		}
	}

	if len(validSources) == 0 {
		return nil, nil // No healthy groups available
	}

	// Sources are already sorted by sort field
	// Find the minimum sort value
	minSort := validSources[0].Sort

	// Get all sources with the minimum sort value
	var topSources []ModelSource
	for _, source := range validSources {
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
			weights[i] = source.EffectiveWeight
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
	group, err := s.groupManager.GetGroupByID(selectedSource.GroupID)
	if err != nil {
		return nil, err
	}

	return group, nil
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

	models := make([]string, 0, len(pool))
	for _, entry := range pool {
		// Only include models that have at least one healthy source
		hasHealthySource := false
		for _, source := range entry.Sources {
			if source.Enabled && source.HealthScore >= s.healthScoreThreshold {
				hasHealthySource = true
				break
			}
		}
		if hasHealthySource {
			models = append(models, entry.ModelName)
		}
	}

	return models, nil
}

// GetModelSources returns the sources for a specific model.
// Returns nil if the model is not found.
func (s *HubService) GetModelSources(ctx context.Context, modelName string) ([]ModelSource, error) {
	pool, err := s.GetModelPool(ctx)
	if err != nil {
		return nil, err
	}

	for _, entry := range pool {
		if entry.ModelName == modelName {
			return entry.Sources, nil
		}
	}

	return nil, nil
}

// IsModelAvailable checks if a model is available in the hub.
func (s *HubService) IsModelAvailable(ctx context.Context, modelName string) (bool, error) {
	sources, err := s.GetModelSources(ctx, modelName)
	if err != nil {
		return false, err
	}

	if sources == nil {
		return false, nil
	}

	// Check if at least one source is healthy
	for _, source := range sources {
		if source.Enabled && source.HealthScore >= s.healthScoreThreshold {
			return true, nil
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
		groups := make([]ModelGroupPriorityDTO, 0, len(entry.Sources))
		for _, source := range entry.Sources {
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

		result = append(result, ModelPoolEntryV2{
			ModelName: entry.ModelName,
			Groups:    groups,
		})
	}

	return result, nil
}

// UpdateModelGroupPriority updates the priority for a model-group pair.
// Priority 0 means disabled (skip this group for this model).
func (s *HubService) UpdateModelGroupPriority(ctx context.Context, modelName string, groupID uint, priority int) error {
	// Validate priority range
	if priority < 0 || priority > 999 {
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
func (s *HubService) BatchUpdateModelGroupPriorities(ctx context.Context, updates []UpdateModelGroupPriorityParams) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, update := range updates {
			if update.Priority < 0 || update.Priority > 999 {
				continue // Skip invalid priorities
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
				MaxRetries:      3,
				RetryDelay:      100,
				HealthThreshold: 0.5,
				EnablePriority:  true,
			}, nil
		}
		return nil, err
	}

	return &HubSettingsDTO{
		MaxRetries:      settings.MaxRetries,
		RetryDelay:      settings.RetryDelay,
		HealthThreshold: settings.HealthThreshold,
		EnablePriority:  settings.EnablePriority,
	}, nil
}

// UpdateHubSettings updates the Hub settings.
func (s *HubService) UpdateHubSettings(ctx context.Context, dto *HubSettingsDTO) error {
	var settings HubSettings
	err := s.db.WithContext(ctx).First(&settings).Error

	if err == gorm.ErrRecordNotFound {
		// Create new settings
		settings = HubSettings{
			MaxRetries:      dto.MaxRetries,
			RetryDelay:      dto.RetryDelay,
			HealthThreshold: dto.HealthThreshold,
			EnablePriority:  dto.EnablePriority,
		}
		return s.db.WithContext(ctx).Create(&settings).Error
	}

	if err != nil {
		return err
	}

	// Update existing settings
	return s.db.WithContext(ctx).Model(&settings).Updates(map[string]any{
		"max_retries":      dto.MaxRetries,
		"retry_delay":      dto.RetryDelay,
		"health_threshold": dto.HealthThreshold,
		"enable_priority":  dto.EnablePriority,
	}).Error
}

// SelectGroupForModelWithPriority selects the best group for a model using priority-based routing.
// Groups are tried in priority order (lower priority value = higher priority).
// Within the same priority level, weighted random selection is used.
func (s *HubService) SelectGroupForModelWithPriority(ctx context.Context, modelName string) (*models.Group, error) {
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

	// Filter by enabled, non-zero priority, and health score threshold
	var validGroups []ModelGroupPriorityDTO
	for _, g := range groups {
		if g.Enabled && g.Priority > 0 && g.HealthScore >= s.healthScoreThreshold {
			validGroups = append(validGroups, g)
		}
	}

	if len(validGroups) == 0 {
		return nil, nil // No healthy groups available
	}

	// Groups are already sorted by priority
	// Find the minimum priority value (highest priority)
	minPriority := validGroups[0].Priority

	// Get all groups with the minimum priority
	var topGroups []ModelGroupPriorityDTO
	for _, g := range validGroups {
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

// ErrInvalidPriority is returned when priority value is out of range.
var ErrInvalidPriority = &InvalidPriorityError{}

// InvalidPriorityError represents an invalid priority value error.
type InvalidPriorityError struct{}

func (e *InvalidPriorityError) Error() string {
	return "priority must be between 0 and 999"
}
