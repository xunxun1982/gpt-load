package services

import (
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/assert"
)

func TestNewAggregateGroupService(t *testing.T) {
	t.Parallel()

	groupManager := &GroupManager{}
	dynamicWeightManager := &DynamicWeightManager{}

	service := NewAggregateGroupService(nil, groupManager, dynamicWeightManager)

	assert.NotNil(t, service)
	assert.Equal(t, groupManager, service.groupManager)
	assert.Equal(t, dynamicWeightManager, service.dynamicWeightManager)
	assert.NotNil(t, service.statsCache)
	assert.Equal(t, 5*time.Minute, service.statsCacheTTL)
}

func TestIsGroupCCSupportEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		group    *models.Group
		expected bool
	}{
		{
			name:     "nil group",
			group:    nil,
			expected: false,
		},
		{
			name:     "nil config",
			group:    &models.Group{Config: nil},
			expected: false,
		},
		{
			name: "cc_support true (bool)",
			group: &models.Group{
				Config: map[string]any{"cc_support": true},
			},
			expected: true,
		},
		{
			name: "cc_support false (bool)",
			group: &models.Group{
				Config: map[string]any{"cc_support": false},
			},
			expected: false,
		},
		{
			name: "cc_support 1 (float64)",
			group: &models.Group{
				Config: map[string]any{"cc_support": float64(1)},
			},
			expected: true,
		},
		{
			name: "cc_support 0 (float64)",
			group: &models.Group{
				Config: map[string]any{"cc_support": float64(0)},
			},
			expected: false,
		},
		{
			name: "cc_support 1 (int)",
			group: &models.Group{
				Config: map[string]any{"cc_support": 1},
			},
			expected: true,
		},
		{
			name: "cc_support 0 (int)",
			group: &models.Group{
				Config: map[string]any{"cc_support": 0},
			},
			expected: false,
		},
		{
			name: "cc_support missing",
			group: &models.Group{
				Config: map[string]any{"other_key": "value"},
			},
			expected: false,
		},
		{
			name: "cc_support string true",
			group: &models.Group{
				Config: map[string]any{"cc_support": "true"},
			},
			expected: true,
		},
		{
			name: "cc_support string TRUE (case insensitive)",
			group: &models.Group{
				Config: map[string]any{"cc_support": "TRUE"},
			},
			expected: true,
		},
		{
			name: "cc_support string 1",
			group: &models.Group{
				Config: map[string]any{"cc_support": "1"},
			},
			expected: true,
		},
		{
			name: "cc_support string yes",
			group: &models.Group{
				Config: map[string]any{"cc_support": "yes"},
			},
			expected: true,
		},
		{
			name: "cc_support string on",
			group: &models.Group{
				Config: map[string]any{"cc_support": "on"},
			},
			expected: true,
		},
		{
			name: "cc_support string false",
			group: &models.Group{
				Config: map[string]any{"cc_support": "false"},
			},
			expected: false,
		},
		{
			name: "cc_support string with spaces",
			group: &models.Group{
				Config: map[string]any{"cc_support": " true "},
			},
			expected: true,
		},
		{
			name: "cc_support unsupported type (slice)",
			group: &models.Group{
				Config: map[string]any{"cc_support": []int{1}},
			},
			expected: false,
		},
		{
			name: "cc_support unsupported type (map)",
			group: &models.Group{
				Config: map[string]any{"cc_support": map[string]string{"key": "value"}},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isGroupCCSupportEnabled(tt.group)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetEffectiveEndpointForAggregation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		subGroup             *models.Group
		aggregateChannelType string
		isOpenAIWithCC       bool
		expected             string
	}{
		{
			name: "custom validation endpoint - non-anthropic",
			subGroup: &models.Group{
				ValidationEndpoint: "/custom/endpoint",
				ChannelType:        "openai",
			},
			aggregateChannelType: "openai",
			isOpenAIWithCC:       false,
			expected:             "/custom/endpoint",
		},
		{
			name: "custom validation endpoint - anthropic with OpenAI+CC",
			subGroup: &models.Group{
				ValidationEndpoint: "/v1/chat/completions",
				ChannelType:        "openai",
			},
			aggregateChannelType: "anthropic",
			isOpenAIWithCC:       true,
			expected:             "/v1/messages",
		},
		{
			name: "no custom endpoint - anthropic with OpenAI+CC",
			subGroup: &models.Group{
				ValidationEndpoint: "",
				ChannelType:        "openai",
			},
			aggregateChannelType: "anthropic",
			isOpenAIWithCC:       true,
			expected:             "/v1/messages",
		},
		{
			name: "no custom endpoint - standard OpenAI",
			subGroup: &models.Group{
				ValidationEndpoint: "",
				ChannelType:        "openai",
			},
			aggregateChannelType: "openai",
			isOpenAIWithCC:       false,
			expected:             "/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := getEffectiveEndpointForAggregation(tt.subGroup, tt.aggregateChannelType, tt.isOpenAIWithCC)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateCacheKey(t *testing.T) {
	t.Parallel()

	service := NewAggregateGroupService(nil, &GroupManager{}, nil)

	tests := []struct {
		name     string
		groupIDs []uint
		expected string
	}{
		{
			name:     "single ID",
			groupIDs: []uint{1},
			expected: "1",
		},
		{
			name:     "multiple IDs sorted",
			groupIDs: []uint{3, 1, 2},
			expected: "1,2,3",
		},
		{
			name:     "already sorted",
			groupIDs: []uint{1, 2, 3},
			expected: "1,2,3",
		},
		{
			name:     "empty",
			groupIDs: []uint{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := service.generateCacheKey(tt.groupIDs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContainsGroupID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cacheKey   string
		groupIDStr string
		expected   bool
	}{
		{
			name:       "single ID match",
			cacheKey:   "1",
			groupIDStr: "1",
			expected:   true,
		},
		{
			name:       "single ID no match",
			cacheKey:   "1",
			groupIDStr: "2",
			expected:   false,
		},
		{
			name:       "multiple IDs - first",
			cacheKey:   "1,2,3",
			groupIDStr: "1",
			expected:   true,
		},
		{
			name:       "multiple IDs - middle",
			cacheKey:   "1,2,3",
			groupIDStr: "2",
			expected:   true,
		},
		{
			name:       "multiple IDs - last",
			cacheKey:   "1,2,3",
			groupIDStr: "3",
			expected:   true,
		},
		{
			name:       "false positive prevention - 1 vs 10",
			cacheKey:   "10,20,30",
			groupIDStr: "1",
			expected:   false,
		},
		{
			name:       "false positive prevention - 2 vs 20",
			cacheKey:   "10,20,30",
			groupIDStr: "2",
			expected:   false,
		},
		{
			name:       "empty cache key",
			cacheKey:   "",
			groupIDStr: "1",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := containsGroupID(tt.cacheKey, tt.groupIDStr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInvalidateStatsCacheForGroup(t *testing.T) {
	t.Parallel()

	service := NewAggregateGroupService(nil, &GroupManager{}, nil)

	// Populate cache with test data
	// Note: Direct cache manipulation is acceptable in unit tests for simplicity.
	// If statsCache becomes concurrent-safe or access is restructured, update this test.
	service.statsCache["1,2,3"] = keyStatsCacheEntry{
		results:   map[uint]keyStatsResult{1: {GroupID: 1}},
		expiresAt: time.Now().Add(time.Hour),
	}
	service.statsCache["4,5,6"] = keyStatsCacheEntry{
		results:   map[uint]keyStatsResult{4: {GroupID: 4}},
		expiresAt: time.Now().Add(time.Hour),
	}
	service.statsCache["1,7,8"] = keyStatsCacheEntry{
		results:   map[uint]keyStatsResult{1: {GroupID: 1}},
		expiresAt: time.Now().Add(time.Hour),
	}

	// Invalidate group 1
	service.InvalidateStatsCacheForGroup(1)

	// Check that entries containing group 1 are removed
	assert.NotContains(t, service.statsCache, "1,2,3")
	assert.NotContains(t, service.statsCache, "1,7,8")
	// Entry not containing group 1 should remain
	assert.Contains(t, service.statsCache, "4,5,6")
}
