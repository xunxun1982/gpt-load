package services

import (
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
)

// newTestManager creates a new SubGroupManager with a memory store for testing
func newTestManager(t *testing.T) (*SubGroupManager, *store.MemoryStore) {
	t.Helper()
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	return NewSubGroupManager(s), s
}

func TestNewSubGroupManager(t *testing.T) {
	t.Parallel()

	manager, mockStore := newTestManager(t)

	assert.NotNil(t, manager)
	assert.Equal(t, mockStore, manager.store)
	assert.NotNil(t, manager.selectors)
	assert.Len(t, manager.selectors, 0)
}

func TestSetDynamicWeightManager(t *testing.T) {
	t.Parallel()

	manager, _ := newTestManager(t)
	dwm := &DynamicWeightManager{}

	manager.SetDynamicWeightManager(dwm)

	assert.Equal(t, dwm, manager.GetDynamicWeightManager())
}

func TestSelectSubGroup_NonAggregate(t *testing.T) {
	t.Parallel()

	manager, _ := newTestManager(t)

	group := &models.Group{
		Name:      "standard-group",
		GroupType: "standard",
	}

	result, err := manager.SelectSubGroup(group)

	assert.NoError(t, err)
	assert.Empty(t, result)
}

func TestSelectSubGroup_NoSubGroups(t *testing.T) {
	t.Parallel()

	manager, _ := newTestManager(t)

	group := &models.Group{
		Name:      "aggregate-group",
		GroupType: "aggregate",
		SubGroups: []models.GroupSubGroup{},
	}

	result, err := manager.SelectSubGroup(group)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no valid sub-groups available")
	assert.Empty(t, result)
}

func TestSelectSubGroupWithRetry_WithExclusion(t *testing.T) {
	t.Parallel()

	manager, mockStore := newTestManager(t)

	// Create test group with sub-groups
	group := &models.Group{
		ID:        1,
		Name:      "aggregate-group",
		GroupType: "aggregate",
		SubGroups: []models.GroupSubGroup{
			{SubGroupID: 10, SubGroupName: "sub1", Weight: 10, SubGroupEnabled: true},
			{SubGroupID: 20, SubGroupName: "sub2", Weight: 10, SubGroupEnabled: true},
			{SubGroupID: 30, SubGroupName: "sub3", Weight: 10, SubGroupEnabled: true},
		},
	}

	// Add active keys for all sub-groups using LPush
	mockStore.LPush("group:10:active_keys", "key1")
	mockStore.LPush("group:20:active_keys", "key2")
	mockStore.LPush("group:30:active_keys", "key3")

	// Exclude sub-group 10
	excludeMap := map[uint]bool{10: true}

	name, id, err := manager.SelectSubGroupWithRetry(group, excludeMap)

	assert.NoError(t, err)
	assert.NotEmpty(t, name)
	assert.NotEqual(t, uint(10), id, "should not select excluded group 10")
	// Improved assertion with clearer failure message
	assert.True(t, id == 20 || id == 30, "expected id 20 or 30 but got %d", id)
}

func TestRebuildSelectors(t *testing.T) {
	t.Parallel()

	manager, _ := newTestManager(t)

	groups := map[string]*models.Group{
		"agg1": {
			ID:        1,
			Name:      "agg1",
			GroupType: "aggregate",
			SubGroups: []models.GroupSubGroup{
				{SubGroupID: 10, SubGroupName: "sub1", Weight: 10, SubGroupEnabled: true},
			},
		},
		"agg2": {
			ID:        2,
			Name:      "agg2",
			GroupType: "aggregate",
			SubGroups: []models.GroupSubGroup{
				{SubGroupID: 20, SubGroupName: "sub2", Weight: 10, SubGroupEnabled: true},
			},
		},
		"standard": {
			ID:        3,
			Name:      "standard",
			GroupType: "standard",
		},
	}

	manager.RebuildSelectors(groups)

	// Should have selectors for aggregate groups only
	assert.Len(t, manager.selectors, 2)
	assert.Contains(t, manager.selectors, uint(1))
	assert.Contains(t, manager.selectors, uint(2))
	assert.NotContains(t, manager.selectors, uint(3))
}

func TestSelector_SelectByWeight_SingleSubGroup(t *testing.T) {
	t.Parallel()

	_, mockStore := newTestManager(t)

	sel := &selector{
		groupID:   1,
		groupName: "test-group",
		subGroups: []subGroupItem{
			{name: "sub1", subGroupID: 10, weight: 10, enabled: true},
		},
		store: mockStore,
	}

	// Add active key using LPush
	mockStore.LPush("group:10:active_keys", "key1")

	result := sel.selectByWeight()

	assert.NotNil(t, result)
	assert.Equal(t, "sub1", result.name)
	assert.Equal(t, uint(10), result.subGroupID)
}

func TestSelector_SelectByWeight_DisabledSubGroup(t *testing.T) {
	t.Parallel()

	_, mockStore := newTestManager(t)

	sel := &selector{
		groupID:   1,
		groupName: "test-group",
		subGroups: []subGroupItem{
			{name: "sub1", subGroupID: 10, weight: 10, enabled: false},
			{name: "sub2", subGroupID: 20, weight: 10, enabled: true},
		},
		store: mockStore,
	}

	// Add active keys using LPush
	mockStore.LPush("group:10:active_keys", "key1")
	mockStore.LPush("group:20:active_keys", "key2")

	// Run multiple times to ensure disabled group is never selected
	for i := 0; i < 10; i++ {
		result := sel.selectByWeight()
		assert.NotNil(t, result)
		assert.Equal(t, "sub2", result.name)
		assert.Equal(t, uint(20), result.subGroupID)
	}
}

func TestSelector_HasActiveKeys(t *testing.T) {
	t.Parallel()

	_, mockStore := newTestManager(t)

	sel := &selector{
		groupID:   1,
		groupName: "test-group",
		store:     mockStore,
	}

	// No keys initially
	assert.False(t, sel.hasActiveKeys(10))

	// Add key using LPush
	mockStore.LPush("group:10:active_keys", "key1")
	assert.True(t, sel.hasActiveKeys(10))
}

func TestSelector_SelectNext_NoActiveKeys(t *testing.T) {
	t.Parallel()

	_, mockStore := newTestManager(t)

	sel := &selector{
		groupID:   1,
		groupName: "test-group",
		subGroups: []subGroupItem{
			{name: "sub1", subGroupID: 10, weight: 10, enabled: true},
			{name: "sub2", subGroupID: 20, weight: 10, enabled: true},
		},
		store: mockStore,
	}

	// No active keys for any sub-group
	result := sel.selectNext()

	assert.Empty(t, result)
}

func TestSelector_SelectNextWithExclusion_AllExcluded(t *testing.T) {
	t.Parallel()

	_, mockStore := newTestManager(t)

	sel := &selector{
		groupID:   1,
		groupName: "test-group",
		subGroups: []subGroupItem{
			{name: "sub1", subGroupID: 10, weight: 10, enabled: true},
			{name: "sub2", subGroupID: 20, weight: 10, enabled: true},
		},
		store: mockStore,
	}

	// Add active keys using LPush
	mockStore.LPush("group:10:active_keys", "key1")
	mockStore.LPush("group:20:active_keys", "key2")

	// Exclude all sub-groups
	excludeMap := map[uint]bool{10: true, 20: true}

	name, id := sel.selectNextWithExclusion(excludeMap)

	assert.Empty(t, name)
	assert.Equal(t, uint(0), id)
}
