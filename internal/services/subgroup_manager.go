package services

import (
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"
	"hash/fnv"
	"strconv"
	"sync"

	"github.com/sirupsen/logrus"
)

// SubGroupManager manages weighted round-robin selection for all aggregate groups
type SubGroupManager struct {
	store         store.Store
	selectors     map[uint]*selector
	mu            sync.RWMutex
	dynamicWeight *DynamicWeightManager // Optional dynamic weight manager
}

// subGroupItem represents a sub-group with its weight and current weight for round-robin
type subGroupItem struct {
	name               string
	subGroupID         uint
	activeKeysKey      string
	weight             int
	minEffectiveWeight int
	currentWeight      int
	enabled            bool
}

// NewSubGroupManager creates a new sub-group manager service
func NewSubGroupManager(store store.Store) *SubGroupManager {
	return &SubGroupManager{
		store:     store,
		selectors: make(map[uint]*selector),
	}
}

// SetDynamicWeightManager sets the dynamic weight manager for adaptive load balancing.
// This is optional - if not set, static weights will be used.
func (m *SubGroupManager) SetDynamicWeightManager(dwm *DynamicWeightManager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dynamicWeight = dwm
}

// GetDynamicWeightManager returns the dynamic weight manager if set.
func (m *SubGroupManager) GetDynamicWeightManager() *DynamicWeightManager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dynamicWeight
}

// SelectSubGroup selects an appropriate sub-group for the given aggregate group
func (m *SubGroupManager) SelectSubGroup(group *models.Group) (string, error) {
	if group.GroupType != "aggregate" {
		return "", nil
	}

	selector := m.getSelector(group)
	if selector == nil {
		return "", fmt.Errorf("no valid sub-groups available for aggregate group '%s'", group.Name)
	}

	selectedName := selector.selectNext()
	if selectedName == "" {
		return "", fmt.Errorf("no sub-groups with active keys for aggregate group '%s'", group.Name)
	}

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.WithFields(logrus.Fields{
			"aggregate_group": group.Name,
			"selected_group":  selectedName,
		}).Debug("Selected sub-group from aggregate")
	}

	return selectedName, nil
}

// SelectSubGroupWithRetry selects an appropriate sub-group with exclusion list support for retry logic
// excludeSubGroupIDs: map of sub-group IDs to exclude from selection (failed sub-groups in current request)
// Returns: selected sub-group name, sub-group ID, and error
func (m *SubGroupManager) SelectSubGroupWithRetry(group *models.Group, excludeSubGroupIDs map[uint]bool) (string, uint, error) {
	if group.GroupType != "aggregate" {
		return "", 0, nil
	}

	selector := m.getSelector(group)
	if selector == nil {
		return "", 0, fmt.Errorf("no valid sub-groups available for aggregate group '%s'", group.Name)
	}

	selectedName, selectedID := selector.selectNextWithExclusion(excludeSubGroupIDs)
	if selectedName == "" {
		return "", 0, fmt.Errorf("no sub-groups with active keys for aggregate group '%s'", group.Name)
	}

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.WithFields(logrus.Fields{
			"aggregate_group": group.Name,
			"selected_group":  selectedName,
			"selected_id":     selectedID,
			"excluded_count":  len(excludeSubGroupIDs),
		}).Debug("Selected sub-group from aggregate with exclusion list")
	}

	return selectedName, selectedID, nil
}

// SelectSubGroupWithRetryAffinity selects a sub-group deterministically for a stable affinity key.
func (m *SubGroupManager) SelectSubGroupWithRetryAffinity(group *models.Group, excludeSubGroupIDs map[uint]bool, affinityKey string) (string, uint, error) {
	if group.GroupType != "aggregate" {
		return "", 0, nil
	}
	if affinityKey == "" {
		return m.SelectSubGroupWithRetry(group, excludeSubGroupIDs)
	}

	selector := m.getSelector(group)
	if selector == nil {
		return "", 0, fmt.Errorf("no valid sub-groups available for aggregate group '%s'", group.Name)
	}

	selectedName, selectedID := selector.selectByAffinityWithExclusion(excludeSubGroupIDs, affinityKey)
	if selectedName == "" {
		return "", 0, fmt.Errorf("no sub-groups with active keys for aggregate group '%s'", group.Name)
	}

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.WithFields(logrus.Fields{
			"aggregate_group": group.Name,
			"selected_group":  selectedName,
			"selected_id":     selectedID,
			"excluded_count":  len(excludeSubGroupIDs),
		}).Debug("Selected sub-group from aggregate with affinity")
	}

	return selectedName, selectedID, nil
}

// RebuildSelectors rebuild all selectors based on the incoming group
func (m *SubGroupManager) RebuildSelectors(groups map[string]*models.Group) {
	newSelectors := make(map[uint]*selector)

	// Capture dynamic weight manager under lock before creating selectors
	m.mu.RLock()
	dwm := m.dynamicWeight
	m.mu.RUnlock()

	for _, group := range groups {
		if group.GroupType == "aggregate" && len(group.SubGroups) > 0 {
			if sel := m.createSelector(group, dwm); sel != nil {
				newSelectors[group.ID] = sel
			}
		}
	}

	m.mu.Lock()
	m.selectors = newSelectors
	m.mu.Unlock()

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.WithField("new_count", len(newSelectors)).Debug("Rebuilt selectors for aggregate groups")
	}
}

// getSelector retrieves or creates a selector for the aggregate group
func (m *SubGroupManager) getSelector(group *models.Group) *selector {
	m.mu.RLock()
	if sel, exists := m.selectors[group.ID]; exists {
		m.mu.RUnlock()
		return sel
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if sel, exists := m.selectors[group.ID]; exists {
		return sel
	}

	// Capture dynamic weight manager while holding lock
	dwm := m.dynamicWeight
	sel := m.createSelector(group, dwm)
	if sel != nil {
		m.selectors[group.ID] = sel
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"group_id":        group.ID,
				"group_name":      group.Name,
				"sub_group_count": len(sel.subGroups),
			}).Debug("Created sub-group selector")
		}
	}

	return sel
}

// createSelector creates a new selector for an aggregate group.
// The dynamicWeight parameter is passed in to avoid race conditions when accessing m.dynamicWeight.
func (m *SubGroupManager) createSelector(group *models.Group, dynamicWeight *DynamicWeightManager) *selector {
	if group.GroupType != "aggregate" || len(group.SubGroups) == 0 {
		return nil
	}

	var items []subGroupItem
	for _, sg := range group.SubGroups {
		items = append(items, subGroupItem{
			name:               sg.SubGroupName,
			subGroupID:         sg.SubGroupID,
			activeKeysKey:      activeKeysListKey(sg.SubGroupID),
			weight:             sg.Weight,
			minEffectiveWeight: normalizeSubGroupMinEffectiveWeight(sg.Weight, sg.MinEffectiveWeight),
			currentWeight:      0,
			enabled:            sg.SubGroupEnabled,
		})
	}

	if len(items) == 0 {
		return nil
	}

	return &selector{
		groupID:       group.ID,
		groupName:     group.Name,
		subGroups:     items,
		weights:       make([]int, len(items)),
		attempted:     make([]uint, 0, len(items)),
		store:         m.store,
		dynamicWeight: dynamicWeight,
	}
}

// selector encapsulates the weighted round-robin algorithm for a single aggregate group
type selector struct {
	groupID       uint
	groupName     string
	subGroups     []subGroupItem
	weights       []int  // Reused under mu to avoid per-selection allocations.
	attempted     []uint // Reused under mu to avoid per-selection map allocations.
	store         store.Store
	mu            sync.Mutex
	dynamicWeight *DynamicWeightManager // Optional dynamic weight manager
}

// selectNext uses weighted round-robin algorithm to select a sub-group with active keys
func (s *selector) selectNext() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.subGroups) == 0 {
		return ""
	}

	if len(s.subGroups) == 1 {
		if !s.subGroups[0].enabled {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"group_id":   s.subGroups[0].subGroupID,
					"group_name": s.subGroups[0].name,
				}).Debug("Single sub-group is disabled")
			}
			return ""
		}
		if s.hasActiveKeys(&s.subGroups[0]) {
			return s.subGroups[0].name
		}
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"group_id":   s.subGroups[0].subGroupID,
				"group_name": s.subGroups[0].name,
			}).Debug("Single sub-group has no active keys")
		}
		return ""
	}

	attempted := s.resetAttempted()
	defer func() {
		s.attempted = attempted
	}()
	for len(attempted) < len(s.subGroups) {
		item := s.selectByWeightSkipping(nil, attempted)
		if item == nil {
			break
		}

		attempted = append(attempted, item.subGroupID)

		if !item.enabled {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"group_id":   item.subGroupID,
					"group_name": item.name,
					"attempts":   len(attempted),
				}).Debug("Sub-group is disabled, trying next")
			}
			continue
		}

		if s.hasActiveKeys(item) {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"aggregate_group": s.groupName,
					"selected_group":  item.name,
					"attempts":        len(attempted),
				}).Debug("Selected sub-group with active keys")
			}
			return item.name
		}

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"group_id":   item.subGroupID,
				"group_name": item.name,
				"attempts":   len(attempted),
			}).Debug("Sub-group has no active keys, trying next")
		}
	}

	logrus.WithFields(logrus.Fields{
		"aggregate_group":  s.groupName,
		"total_sub_groups": len(s.subGroups),
	}).Warn("No sub-groups with active keys available")

	return ""
}

// selectNextWithExclusion uses weighted round-robin algorithm with exclusion list support
// excludeIDs: map of sub-group IDs to exclude from selection
// Returns: selected sub-group name and ID
func (s *selector) selectNextWithExclusion(excludeIDs map[uint]bool) (string, uint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.subGroups) == 0 {
		return "", 0
	}

	// Fast path: single sub-group
	if len(s.subGroups) == 1 {
		item := &s.subGroups[0]
		// Check if excluded
		if excludeIDs[item.subGroupID] {
			return "", 0
		}
		// Check if enabled
		if !item.enabled {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"group_id":   item.subGroupID,
					"group_name": item.name,
				}).Debug("Single sub-group is disabled")
			}
			return "", 0
		}
		// Check if has active keys
		if s.hasActiveKeys(item) {
			return item.name, item.subGroupID
		}
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"group_id":   item.subGroupID,
				"group_name": item.name,
			}).Debug("Single sub-group has no active keys")
		}
		return "", 0
	}

	// Count available sub-groups (not excluded, enabled, has active keys)
	availableCount := 0
	for i := range s.subGroups {
		item := &s.subGroups[i]
		if !excludeIDs[item.subGroupID] && item.enabled {
			availableCount++
		}
	}

	if availableCount == 0 {
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"aggregate_group": s.groupName,
				"excluded_count":  len(excludeIDs),
			}).Debug("No available sub-groups after exclusion")
		}
		return "", 0
	}

	// Try to select a sub-group using weighted round-robin
	attempted := s.resetAttempted()
	defer func() {
		s.attempted = attempted
	}()
	for len(attempted) < len(s.subGroups) {
		item := s.selectByWeightSkipping(excludeIDs, attempted)
		if item == nil {
			break
		}

		attempted = append(attempted, item.subGroupID)

		// Skip if excluded
		if excludeIDs[item.subGroupID] {
			continue
		}

		// Skip if disabled
		if !item.enabled {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"group_id":   item.subGroupID,
					"group_name": item.name,
					"attempts":   len(attempted),
				}).Debug("Sub-group is disabled, trying next")
			}
			continue
		}

		// Check if has active keys
		if s.hasActiveKeys(item) {
			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"aggregate_group": s.groupName,
					"selected_group":  item.name,
					"selected_id":     item.subGroupID,
					"attempts":        len(attempted),
					"excluded_count":  len(excludeIDs),
				}).Debug("Selected sub-group with active keys (with exclusion)")
			}
			return item.name, item.subGroupID
		}

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"group_id":   item.subGroupID,
				"group_name": item.name,
				"attempts":   len(attempted),
			}).Debug("Sub-group has no active keys, trying next")
		}
	}

	logrus.WithFields(logrus.Fields{
		"aggregate_group":  s.groupName,
		"total_sub_groups": len(s.subGroups),
		"excluded_count":   len(excludeIDs),
	}).Warn("No sub-groups with active keys available after exclusion")

	return "", 0
}

func (s *selector) selectByAffinityWithExclusion(excludeIDs map[uint]bool, affinityKey string) (string, uint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.subGroups) == 0 {
		return "", 0
	}

	weights := s.resetWeights()
	total := 0
	for i := range s.subGroups {
		item := &s.subGroups[i]
		if !item.enabled || isExcludedSubGroup(item.subGroupID, excludeIDs, nil) || !s.hasActiveKeys(item) {
			weights[i] = 0
			continue
		}

		weight := item.weight
		if s.dynamicWeight != nil {
			metrics, _ := s.dynamicWeight.GetSubGroupMetrics(s.groupID, item.subGroupID)
			effectiveWeight := s.dynamicWeight.GetEffectiveWeightWithMinimum(item.weight, item.minEffectiveWeight, metrics)
			weight = GetEffectiveWeightForSelection(effectiveWeight)
		}
		if weight <= 0 {
			weights[i] = 0
			continue
		}
		weights[i] = weight
		total += weight
	}

	if total <= 0 {
		return "", 0
	}

	target := int(hashAffinityKey(affinityKey) % uint64(total))
	cumulative := 0
	for i, weight := range weights {
		if weight <= 0 {
			continue
		}
		cumulative += weight
		if target < cumulative {
			item := &s.subGroups[i]
			return item.name, item.subGroupID
		}
	}

	return "", 0
}

func hashAffinityKey(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

// selectByWeight implements weighted random selection algorithm.
// Disabled sub-groups are treated as having zero weight to exclude them from selection.
// If dynamic weight manager is set, effective weights are calculated based on health scores.
func (s *selector) selectByWeight() *subGroupItem {
	return s.selectByWeightSkipping(nil, nil)
}

func (s *selector) selectByWeightSkipping(excludeIDs map[uint]bool, attempted []uint) *subGroupItem {
	if len(s.subGroups) == 0 {
		return nil
	}

	// Build weights array, treating disabled sub-groups as weight 0
	weights := s.resetWeights()
	for i := range s.subGroups {
		if s.subGroups[i].enabled && !isExcludedSubGroup(s.subGroups[i].subGroupID, excludeIDs, attempted) {
			baseWeight := s.subGroups[i].weight
			// Apply dynamic weight if manager is available
			if s.dynamicWeight != nil {
				metrics, _ := s.dynamicWeight.GetSubGroupMetrics(s.groupID, s.subGroups[i].subGroupID)
				effectiveWeight := s.dynamicWeight.GetEffectiveWeightWithMinimum(baseWeight, s.subGroups[i].minEffectiveWeight, metrics)
				weights[i] = GetEffectiveWeightForSelection(effectiveWeight)
			} else {
				weights[i] = baseWeight
			}
		} else {
			weights[i] = 0
		}
	}

	// Use shared weighted random selection
	idx := utils.WeightedRandomSelect(weights)
	if idx < 0 {
		return nil
	}

	return &s.subGroups[idx]
}

// selectByWeightWithExclusion implements weighted random selection algorithm with exclusion list.
// Only considers sub-groups not in the exclusion list.
// Disabled sub-groups are treated as having zero weight to exclude them from selection.
// If dynamic weight manager is set, effective weights are calculated based on health scores.
func (s *selector) selectByWeightWithExclusion(excludeIDs map[uint]bool) *subGroupItem {
	return s.selectByWeightSkipping(excludeIDs, nil)
}

func (s *selector) resetWeights() []int {
	if cap(s.weights) < len(s.subGroups) {
		s.weights = make([]int, len(s.subGroups))
	} else {
		s.weights = s.weights[:len(s.subGroups)]
		clear(s.weights)
	}
	return s.weights
}

func (s *selector) resetAttempted() []uint {
	if cap(s.attempted) < len(s.subGroups) {
		s.attempted = make([]uint, 0, len(s.subGroups))
		return s.attempted
	}
	return s.attempted[:0]
}

func containsAttempted(attempted []uint, id uint) bool {
	for _, attemptedID := range attempted {
		if attemptedID == id {
			return true
		}
	}
	return false
}

func isExcludedSubGroup(id uint, excludeIDs map[uint]bool, attempted []uint) bool {
	if excludeIDs != nil && excludeIDs[id] {
		return true
	}
	return containsAttempted(attempted, id)
}

// hasActiveKeys checks if a sub-group has available API keys.
func (s *selector) hasActiveKeys(item *subGroupItem) bool {
	key := item.activeKeysKey
	if key == "" {
		key = activeKeysListKey(item.subGroupID)
	}
	length, err := s.store.LLen(key)
	if err != nil {
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.WithFields(logrus.Fields{
				"group_id": item.subGroupID,
				"error":    err,
			}).Debug("Error checking active keys, assuming available")
		}
		return true
	}
	return length > 0
}

func activeKeysListKey(groupID uint) string {
	return "group:" + strconv.FormatUint(uint64(groupID), 10) + ":active_keys"
}
