package services

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// SubGroupInput defines the input payload for aggregate group member configuration.
type SubGroupInput struct {
	GroupID uint `json:"group_id"`
	Weight  int  `json:"weight"`
}

// AggregateValidationResult captures the normalized aggregate group parameters.
type AggregateValidationResult struct {
	ValidationEndpoint string
	SubGroups          []models.GroupSubGroup
}

// AggregateGroupService encapsulates aggregate group specific behaviours.
type AggregateGroupService struct {
	db           *gorm.DB
	groupManager *GroupManager
	// Cache for key statistics to reduce database queries
	statsCache    map[string]keyStatsCacheEntry
	statsCacheMu  sync.RWMutex
	statsCacheTTL time.Duration
}

// keyStatsCacheEntry stores cached key statistics with expiration time
type keyStatsCacheEntry struct {
	results   map[uint]keyStatsResult
	expiresAt time.Time
}

// NewAggregateGroupService constructs an AggregateGroupService instance.
func NewAggregateGroupService(db *gorm.DB, groupManager *GroupManager) *AggregateGroupService {
	return &AggregateGroupService{
		db:            db,
		groupManager:  groupManager,
		statsCache:    make(map[string]keyStatsCacheEntry),
		statsCacheTTL: 5 * time.Minute, // Cache for 5 minutes
	}
}

// ValidateSubGroups validates sub-groups with an optional existing validation endpoint for consistency check.
func (s *AggregateGroupService) ValidateSubGroups(ctx context.Context, channelType string, inputs []SubGroupInput, existingEndpoint string) (*AggregateValidationResult, error) {
	if len(inputs) == 0 {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_groups_required", nil)
	}

	subGroupIDs := make([]uint, 0, len(inputs))
	for _, input := range inputs {
		if input.GroupID == 0 {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_sub_group_id", nil)
		}
		if input.Weight < 0 {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_weight_negative", nil)
		}
		if input.Weight > 1000 {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_weight_max_exceeded", nil)
		}
		subGroupIDs = append(subGroupIDs, input.GroupID)
	}

	var subGroupModels []models.Group
	if err := s.db.WithContext(ctx).Where("id IN ?", subGroupIDs).Find(&subGroupModels).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if len(subGroupModels) != len(subGroupIDs) {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_not_found", nil)
	}

	subGroupMap := make(map[uint]models.Group, len(subGroupModels))
	var validationEndpoint string

	// If there's an existing endpoint, use it as the expected endpoint
	if existingEndpoint != "" {
		validationEndpoint = existingEndpoint
	}

	for _, sg := range subGroupModels {
		if sg.GroupType == "aggregate" {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_cannot_be_aggregate", nil)
		}
		if sg.ChannelType != channelType {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_channel_mismatch", nil)
		}

		// If no existing endpoint, use the first sub-group's effective endpoint
		if validationEndpoint == "" {
			validationEndpoint = utils.GetValidationEndpoint(&sg)
		} else if validationEndpoint != utils.GetValidationEndpoint(&sg) {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_validation_endpoint_mismatch", nil)
		}
		subGroupMap[sg.ID] = sg
	}

	resultSubGroups := make([]models.GroupSubGroup, 0, len(inputs))
	for _, input := range inputs {
		if _, ok := subGroupMap[input.GroupID]; !ok {
			return nil, NewI18nError(app_errors.ErrValidation, "validation.sub_group_not_found", nil)
		}
		resultSubGroups = append(resultSubGroups, models.GroupSubGroup{
			SubGroupID: input.GroupID,
			Weight:     input.Weight,
		})
	}

	return &AggregateValidationResult{
		ValidationEndpoint: validationEndpoint,
		SubGroups:          resultSubGroups,
	}, nil
}

// GetSubGroups returns sub groups for an aggregate group with complete information
func (s *AggregateGroupService) GetSubGroups(ctx context.Context, groupID uint) ([]models.SubGroupInfo, error) {
	_, err := FindAggregateGroupByID(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}

	var groupSubGroups []models.GroupSubGroup
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&groupSubGroups).Error; err != nil {
		return nil, err
	}

	if len(groupSubGroups) == 0 {
		return []models.SubGroupInfo{}, nil
	}

	subGroupIDs := make([]uint, 0, len(groupSubGroups))
	weightMap := make(map[uint]int, len(groupSubGroups))

	for _, gsg := range groupSubGroups {
		subGroupIDs = append(subGroupIDs, gsg.SubGroupID)
		weightMap[gsg.SubGroupID] = gsg.Weight
	}

	var subGroupModels []models.Group
	if err := s.db.WithContext(ctx).Where("id IN ?", subGroupIDs).Find(&subGroupModels).Error; err != nil {
		return nil, err
	}

	keyStatsMap := s.fetchSubGroupsKeyStats(ctx, subGroupIDs)

	subGroups := make([]models.SubGroupInfo, 0, len(subGroupModels))
	for _, subGroup := range subGroupModels {
		stats := keyStatsMap[subGroup.ID]

		if stats.Err != nil {
			logrus.WithContext(ctx).WithError(stats.Err).
				WithField("group_id", subGroup.ID).
				Warn("failed to fetch key stats for sub-group, using zero values")
		}

		subGroups = append(subGroups, models.SubGroupInfo{
			Group:       subGroup,
			Weight:      weightMap[subGroup.ID],
			TotalKeys:   stats.TotalKeys,
			ActiveKeys:  stats.ActiveKeys,
			InvalidKeys: stats.InvalidKeys,
		})
	}

	return subGroups, nil
}

// AddSubGroups adds new sub groups to an aggregate group
func (s *AggregateGroupService) AddSubGroups(ctx context.Context, groupID uint, inputs []SubGroupInput) error {
	group, err := FindAggregateGroupByID(ctx, s.db, groupID)
	if err != nil {
		return err
	}

	// Check if there are existing sub groups and get their validation endpoint
	var existingEndpoint string
	var existingSubGroups []models.GroupSubGroup
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&existingSubGroups).Error; err != nil {
		return err
	}

	if len(existingSubGroups) > 0 {
		var existingGroup models.Group
		if err := s.db.WithContext(ctx).Where("id = ?", existingSubGroups[0].SubGroupID).Limit(1).Find(&existingGroup).Error; err == nil && existingGroup.ID != 0 {
			existingEndpoint = utils.GetValidationEndpoint(&existingGroup)
		}
	}

	// Validate sub groups with existing endpoint for consistency
	result, err := s.ValidateSubGroups(ctx, group.ChannelType, inputs, existingEndpoint)
	if err != nil {
		return err
	}

	// Check for duplicates with existing sub groups
	existingSubGroupIDs := make(map[uint]bool, len(existingSubGroups))
	for _, sg := range existingSubGroups {
		existingSubGroupIDs[sg.SubGroupID] = true
	}

	for _, newSg := range result.SubGroups {
		if existingSubGroupIDs[newSg.SubGroupID] {
			return NewI18nError(app_errors.ErrBadRequest, "group.sub_group_already_exists",
				map[string]any{"sub_group_id": newSg.SubGroupID})
		}
	}

	// Add new sub groups using batch insert for better performance
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Set groupID for all sub groups
		for i := range result.SubGroups {
			result.SubGroups[i].GroupID = groupID
		}
		// Use CreateInBatches with fixed batch size for better performance and robustness
		// Fixed batch size ensures consistent behavior even with large sub-group counts
		const batchSize = 100
		if err := tx.CreateInBatches(result.SubGroups, batchSize).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		return nil
	})

	if err != nil {
		return err
	}

	// Trigger cache update
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache after adding sub groups")
	}

	return nil
}

// UpdateSubGroupWeight updates the weight of a specific sub group
func (s *AggregateGroupService) UpdateSubGroupWeight(ctx context.Context, groupID, subGroupID uint, weight int) error {
	_, err := FindAggregateGroupByID(ctx, s.db, groupID)
	if err != nil {
		return err
	}

	if weight < 0 {
		return NewI18nError(app_errors.ErrValidation, "validation.sub_group_weight_negative", nil)
	}

	if weight > 1000 {
		return NewI18nError(app_errors.ErrValidation, "validation.sub_group_weight_max_exceeded", nil)
	}

	// Check if sub-group relationship exists
	var existingRecord models.GroupSubGroup
	if err := s.db.WithContext(ctx).Where("group_id = ? AND sub_group_id = ?", groupID, subGroupID).Limit(1).Find(&existingRecord).Error; err != nil {
		return err
	}
	if existingRecord.GroupID == 0 {
		return NewI18nError(app_errors.ErrResourceNotFound, "group.sub_group_not_found", nil)
	}

	result := s.db.WithContext(ctx).
		Model(&models.GroupSubGroup{}).
		Where("group_id = ? AND sub_group_id = ?", groupID, subGroupID).
		Update("weight", weight)

	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return NewI18nError(app_errors.ErrResourceNotFound, "group.sub_group_not_found", nil)
	}

	// Trigger cache update
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache after updating sub group weight")
	}

	return nil
}

// DeleteSubGroup removes a sub group from an aggregate group
func (s *AggregateGroupService) DeleteSubGroup(ctx context.Context, groupID, subGroupID uint) error {
	_, err := FindAggregateGroupByID(ctx, s.db, groupID)
	if err != nil {
		return err
	}

	result := s.db.WithContext(ctx).
		Where("group_id = ? AND sub_group_id = ?", groupID, subGroupID).
		Delete(&models.GroupSubGroup{})

	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return NewI18nError(app_errors.ErrResourceNotFound, "group.sub_group_not_found", nil)
	}

	// Trigger cache update
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache after deleting sub group")
	}

	return nil
}

// CountAggregateGroupsUsingSubGroup returns the number of aggregate groups that use the specified group as a sub-group.
// The query is bounded by a short timeout to avoid blocking standard group updates when the database is under pressure.
// On timeout or cancellation, the function degrades gracefully by logging a warning and treating the count as zero.
func (s *AggregateGroupService) CountAggregateGroupsUsingSubGroup(ctx context.Context, subGroupID uint) (int64, error) {
	// Use a short timeout to avoid slow COUNT queries blocking group updates
	queryCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()

	var count int64
	err := s.db.WithContext(queryCtx).
		Model(&models.GroupSubGroup{}).
		Where("sub_group_id = ?", subGroupID).
		Count(&count).Error

	if err != nil {
		// Gracefully degrade on timeout/cancellation to keep updates fast
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logrus.WithContext(ctx).WithError(err).
				WithField("sub_group_id", subGroupID).
				Warn("CountAggregateGroupsUsingSubGroup timed out, treating as zero references")
			return 0, nil
		}
		return 0, app_errors.ParseDBError(err)
	}

	return count, nil
}

// GetParentAggregateGroups returns the aggregate groups that use the specified group as a sub-group
func (s *AggregateGroupService) GetParentAggregateGroups(ctx context.Context, subGroupID uint) ([]models.ParentAggregateGroupInfo, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()

	var groupSubGroups []models.GroupSubGroup
	if err := s.db.WithContext(queryCtx).Where("sub_group_id = ?", subGroupID).Find(&groupSubGroups).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// Gracefully degrade: return empty list instead of blocking
			return []models.ParentAggregateGroupInfo{}, nil
		}
		return nil, app_errors.ParseDBError(err)
	}

	if len(groupSubGroups) == 0 {
		return []models.ParentAggregateGroupInfo{}, nil
	}

	aggregateGroupIDs := make([]uint, 0, len(groupSubGroups))
	weightMap := make(map[uint]int, len(groupSubGroups))

	for _, gsg := range groupSubGroups {
		aggregateGroupIDs = append(aggregateGroupIDs, gsg.GroupID)
		weightMap[gsg.GroupID] = gsg.Weight
	}

	var aggregateGroupModels []models.Group
	if err := s.db.WithContext(queryCtx).Where("id IN ?", aggregateGroupIDs).Find(&aggregateGroupModels).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return []models.ParentAggregateGroupInfo{}, nil
		}
		return nil, app_errors.ParseDBError(err)
	}

	parentGroups := make([]models.ParentAggregateGroupInfo, 0, len(aggregateGroupModels))
	for _, group := range aggregateGroupModels {
		parentGroups = append(parentGroups, models.ParentAggregateGroupInfo{
			GroupID:     group.ID,
			Name:        group.Name,
			DisplayName: group.DisplayName,
			Weight:      weightMap[group.ID],
		})
	}

	return parentGroups, nil
}

// keyStatsResult stores key statistics for a single group
type keyStatsResult struct {
	GroupID     uint
	TotalKeys   int64
	ActiveKeys  int64
	InvalidKeys int64
	Err         error
}

// fetchSubGroupsKeyStats batch fetches key statistics for multiple sub-groups using a single SQL query
// Results are cached for 5 minutes to reduce database load
func (s *AggregateGroupService) fetchSubGroupsKeyStats(ctx context.Context, groupIDs []uint) map[uint]keyStatsResult {
	results := make(map[uint]keyStatsResult)

	if len(groupIDs) == 0 {
		return results
	}

	// Generate cache key from sorted group IDs
	cacheKey := s.generateCacheKey(groupIDs)

	// Check cache first
	s.statsCacheMu.RLock()
	entry, cached := s.statsCache[cacheKey]
	isFresh := cached && time.Now().Before(entry.expiresAt)
	s.statsCacheMu.RUnlock()

	if isFresh {
		// Cache hit - return cached results
		// Deep copy to avoid race conditions
		cachedResults := make(map[uint]keyStatsResult, len(entry.results))
		for k, v := range entry.results {
			cachedResults[k] = v
		}
		return cachedResults
	}

	if cached {
		// Cache expired, clear it under write lock
		s.statsCacheMu.Lock()
		if current, ok := s.statsCache[cacheKey]; ok && current.expiresAt.Equal(entry.expiresAt) {
			delete(s.statsCache, cacheKey)
		}
		s.statsCacheMu.Unlock()
	}

	// Initialize results map with all group IDs to ensure all groups are represented
	for _, gid := range groupIDs {
		results[gid] = keyStatsResult{GroupID: gid}
	}

	// Use a single SQL query with GROUP BY to fetch all statistics at once
	// This reduces database round trips from 2N to 1
	type statsRow struct {
		GroupID    uint  `gorm:"column:group_id"`
		TotalKeys  int64 `gorm:"column:total_keys"`
		ActiveKeys int64 `gorm:"column:active_keys"`
	}

	var statsRows []statsRow
	err := s.db.WithContext(ctx).Raw(`
		SELECT
			group_id,
			COUNT(*) as total_keys,
			SUM(CASE WHEN status = ? THEN 1 ELSE 0 END) as active_keys
		FROM api_keys
		WHERE group_id IN ?
		GROUP BY group_id
	`, models.KeyStatusActive, groupIDs).Scan(&statsRows).Error

	if err != nil {
		// If query fails, mark all results with error
		for gid := range results {
			result := results[gid]
			result.Err = err
			results[gid] = result
		}
		return results
	}

	// Update results with fetched statistics
	for _, row := range statsRows {
		result := results[row.GroupID]
		result.TotalKeys = row.TotalKeys
		result.ActiveKeys = row.ActiveKeys
		result.InvalidKeys = row.TotalKeys - row.ActiveKeys
		results[row.GroupID] = result
	}

	// Cache the results (deep copy to avoid reference issues)
	cachedResults := make(map[uint]keyStatsResult, len(results))
	for k, v := range results {
		cachedResults[k] = v
	}

	s.statsCacheMu.Lock()
	s.statsCache[cacheKey] = keyStatsCacheEntry{
		results:   cachedResults,
		expiresAt: time.Now().Add(s.statsCacheTTL),
	}
	// Clean up expired entries when cache grows to prevent memory leak
	// Only cleanup when cache size exceeds threshold to reduce write-lock contention
	if len(s.statsCache) > 50 {
		s.cleanupExpiredCacheEntries()
	}
	// If cache is still large after cleanup, log a warning
	if len(s.statsCache) > 100 {
		logrus.Debugf("AggregateGroupService: Cache size is %d after cleanup, consider tuning cache TTL", len(s.statsCache))
	}
	s.statsCacheMu.Unlock()

	return results
}

// generateCacheKey creates a cache key from sorted group IDs
func (s *AggregateGroupService) generateCacheKey(groupIDs []uint) string {
	var keyBuilder strings.Builder
	keyBuilder.Grow(len(groupIDs) * 10) // Estimate 10 chars per ID

	// Sort IDs for consistent cache keys using standard library sort
	sorted := make([]uint, len(groupIDs))
	copy(sorted, groupIDs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	for i, id := range sorted {
		if i > 0 {
			keyBuilder.WriteByte(',')
		}
		keyBuilder.WriteString(strconv.FormatUint(uint64(id), 10))
	}
	return keyBuilder.String()
}

// cleanupExpiredCacheEntries removes expired entries from the cache
func (s *AggregateGroupService) cleanupExpiredCacheEntries() {
	now := time.Now()
	for key, entry := range s.statsCache {
		if now.After(entry.expiresAt) {
			delete(s.statsCache, key)
		}
	}
}
