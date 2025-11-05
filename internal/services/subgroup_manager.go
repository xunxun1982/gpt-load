package services

import (
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"
	"sync"

	"github.com/sirupsen/logrus"
)

// SubGroupManager manages weighted round-robin selection for all aggregate groups
type SubGroupManager struct {
	store     store.Store
	selectors map[uint]*selector
	mu        sync.RWMutex
}

// subGroupItem represents a sub-group with its weight and current weight for round-robin
type subGroupItem struct {
	name          string
	subGroupID    uint
	weight        int
	currentWeight int
	enabled       bool
}

// NewSubGroupManager creates a new sub-group manager service
func NewSubGroupManager(store store.Store) *SubGroupManager {
	return &SubGroupManager{
		store:     store,
		selectors: make(map[uint]*selector),
	}
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

	logrus.WithFields(logrus.Fields{
		"aggregate_group": group.Name,
		"selected_group":  selectedName,
	}).Debug("Selected sub-group from aggregate")

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

	logrus.WithFields(logrus.Fields{
		"aggregate_group": group.Name,
		"selected_group":  selectedName,
		"selected_id":     selectedID,
		"excluded_count":  len(excludeSubGroupIDs),
	}).Debug("Selected sub-group from aggregate with exclusion list")

	return selectedName, selectedID, nil
}

// RebuildSelectors rebuild all selectors based on the incoming group
func (m *SubGroupManager) RebuildSelectors(groups map[string]*models.Group) {
	newSelectors := make(map[uint]*selector)

	for _, group := range groups {
		if group.GroupType == "aggregate" && len(group.SubGroups) > 0 {
			if sel := m.createSelector(group); sel != nil {
				newSelectors[group.ID] = sel
			}
		}
	}

	m.mu.Lock()
	m.selectors = newSelectors
	m.mu.Unlock()

	logrus.WithField("new_count", len(newSelectors)).Debug("Rebuilt selectors for aggregate groups")
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

	sel := m.createSelector(group)
	if sel != nil {
		m.selectors[group.ID] = sel
		logrus.WithFields(logrus.Fields{
			"group_id":        group.ID,
			"group_name":      group.Name,
			"sub_group_count": len(sel.subGroups),
		}).Debug("Created sub-group selector")
	}

	return sel
}

// createSelector creates a new selector for an aggregate group
func (m *SubGroupManager) createSelector(group *models.Group) *selector {
	if group.GroupType != "aggregate" || len(group.SubGroups) == 0 {
		return nil
	}

	var items []subGroupItem
	for _, sg := range group.SubGroups {
		items = append(items, subGroupItem{
			name:          sg.SubGroupName,
			subGroupID:    sg.SubGroupID,
			weight:        sg.Weight,
			currentWeight: 0,
			enabled:       sg.SubGroupEnabled,
		})
	}

	if len(items) == 0 {
		return nil
	}

	return &selector{
		groupID:   group.ID,
		groupName: group.Name,
		subGroups: items,
		store:     m.store,
	}
}

// selector encapsulates the weighted round-robin algorithm for a single aggregate group
type selector struct {
	groupID   uint
	groupName string
	subGroups []subGroupItem
	store     store.Store
	mu        sync.Mutex
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
			logrus.WithFields(logrus.Fields{
				"group_id":   s.subGroups[0].subGroupID,
				"group_name": s.subGroups[0].name,
			}).Debug("Single sub-group is disabled")
			return ""
		}
		if s.hasActiveKeys(s.subGroups[0].subGroupID) {
			return s.subGroups[0].name
		}
		logrus.WithFields(logrus.Fields{
			"group_id":   s.subGroups[0].subGroupID,
			"group_name": s.subGroups[0].name,
		}).Debug("Single sub-group has no active keys")
		return ""
	}

	attempted := make(map[uint]bool)
	for len(attempted) < len(s.subGroups) {
		item := s.selectByWeight()
		if item == nil {
			break
		}

		if attempted[item.subGroupID] {
			continue
		}
		attempted[item.subGroupID] = true

		if !item.enabled {
			logrus.WithFields(logrus.Fields{
				"group_id":   item.subGroupID,
				"group_name": item.name,
				"attempts":   len(attempted),
			}).Debug("Sub-group is disabled, trying next")
			continue
		}

		if s.hasActiveKeys(item.subGroupID) {
			logrus.WithFields(logrus.Fields{
				"aggregate_group": s.groupName,
				"selected_group":  item.name,
				"attempts":        len(attempted),
			}).Debug("Selected sub-group with active keys")
			return item.name
		}

		logrus.WithFields(logrus.Fields{
			"group_id":   item.subGroupID,
			"group_name": item.name,
			"attempts":   len(attempted),
		}).Debug("Sub-group has no active keys, trying next")
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
			logrus.WithFields(logrus.Fields{
				"group_id":   item.subGroupID,
				"group_name": item.name,
			}).Debug("Single sub-group is disabled")
			return "", 0
		}
		// Check if has active keys
		if s.hasActiveKeys(item.subGroupID) {
			return item.name, item.subGroupID
		}
		logrus.WithFields(logrus.Fields{
			"group_id":   item.subGroupID,
			"group_name": item.name,
		}).Debug("Single sub-group has no active keys")
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
		logrus.WithFields(logrus.Fields{
			"aggregate_group": s.groupName,
			"excluded_count":  len(excludeIDs),
		}).Debug("No available sub-groups after exclusion")
		return "", 0
	}

	// Try to select a sub-group using weighted round-robin
	attempted := make(map[uint]bool)
	for len(attempted) < len(s.subGroups) {
		item := s.selectByWeightWithExclusion(excludeIDs)
		if item == nil {
			break
		}

		if attempted[item.subGroupID] {
			continue
		}
		attempted[item.subGroupID] = true

		// Skip if excluded
		if excludeIDs[item.subGroupID] {
			continue
		}

		// Skip if disabled
		if !item.enabled {
			logrus.WithFields(logrus.Fields{
				"group_id":   item.subGroupID,
				"group_name": item.name,
				"attempts":   len(attempted),
			}).Debug("Sub-group is disabled, trying next")
			continue
		}

		// Check if has active keys
		if s.hasActiveKeys(item.subGroupID) {
			logrus.WithFields(logrus.Fields{
				"aggregate_group": s.groupName,
				"selected_group":  item.name,
				"selected_id":     item.subGroupID,
				"attempts":        len(attempted),
				"excluded_count":  len(excludeIDs),
			}).Debug("Selected sub-group with active keys (with exclusion)")
			return item.name, item.subGroupID
		}

		logrus.WithFields(logrus.Fields{
			"group_id":   item.subGroupID,
			"group_name": item.name,
			"attempts":   len(attempted),
		}).Debug("Sub-group has no active keys, trying next")
	}

	logrus.WithFields(logrus.Fields{
		"aggregate_group":  s.groupName,
		"total_sub_groups": len(s.subGroups),
		"excluded_count":   len(excludeIDs),
	}).Warn("No sub-groups with active keys available after exclusion")

	return "", 0
}

// selectByWeight implements weighted random selection algorithm
func (s *selector) selectByWeight() *subGroupItem {
	if len(s.subGroups) == 0 {
		return nil
	}

	// Build weights array
	weights := make([]int, len(s.subGroups))
	for i := range s.subGroups {
		weights[i] = s.subGroups[i].weight
	}

	// Use shared weighted random selection
	idx := utils.WeightedRandomSelect(weights)
	if idx < 0 {
		return nil
	}

	return &s.subGroups[idx]
}

// selectByWeightWithExclusion implements weighted random selection algorithm with exclusion list
// Only considers sub-groups not in the exclusion list
func (s *selector) selectByWeightWithExclusion(excludeIDs map[uint]bool) *subGroupItem {
	if len(s.subGroups) == 0 {
		return nil
	}

	// Build weights array (0 for excluded sub-groups)
	weights := make([]int, len(s.subGroups))
	for i := range s.subGroups {
		if excludeIDs[s.subGroups[i].subGroupID] {
			weights[i] = 0 // Exclude by setting weight to 0
		} else {
			weights[i] = s.subGroups[i].weight
		}
	}

	// Use shared weighted random selection
	idx := utils.WeightedRandomSelect(weights)
	if idx < 0 {
		return nil
	}

	return &s.subGroups[idx]
}

// hasActiveKeys checks if a sub-group has available API keys
func (s *selector) hasActiveKeys(groupID uint) bool {
	key := fmt.Sprintf("group:%d:active_keys", groupID)
	length, err := s.store.LLen(key)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"group_id": groupID,
			"error":    err,
		}).Debug("Error checking active keys, assuming available")
		return true
	}
	return length > 0
}
