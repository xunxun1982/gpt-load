package services

import (
	"context"
	"errors"
	"testing"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
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
		usesClaudeEndpoint   bool
		expected             string
	}{
		{
			name: "custom validation endpoint - non-anthropic",
			subGroup: &models.Group{
				ValidationEndpoint: "/custom/endpoint",
				ChannelType:        "openai",
			},
			aggregateChannelType: "openai",
			usesClaudeEndpoint:   false,
			expected:             "/custom/endpoint",
		},
		{
			name: "custom validation endpoint - anthropic with OpenAI+CC",
			subGroup: &models.Group{
				ValidationEndpoint: "/v1/chat/completions",
				ChannelType:        "openai",
			},
			aggregateChannelType: "anthropic",
			usesClaudeEndpoint:   true,
			expected:             "/v1/messages",
		},
		{
			name: "no custom endpoint - anthropic with OpenAI+CC",
			subGroup: &models.Group{
				ValidationEndpoint: "",
				ChannelType:        "openai",
			},
			aggregateChannelType: "anthropic",
			usesClaudeEndpoint:   true,
			expected:             "/v1/messages",
		},
		{
			name: "no custom endpoint - anthropic with OpenAI Responses CC",
			subGroup: &models.Group{
				ValidationEndpoint: "",
				ChannelType:        "openai-response",
			},
			aggregateChannelType: "anthropic",
			usesClaudeEndpoint:   true,
			expected:             "/v1/messages",
		},
		{
			name: "no custom endpoint - anthropic with Gemini CC",
			subGroup: &models.Group{
				ValidationEndpoint: "",
				ChannelType:        "gemini",
			},
			aggregateChannelType: "anthropic",
			usesClaudeEndpoint:   true,
			expected:             "/v1/messages",
		},
		{
			name: "no custom endpoint - standard OpenAI",
			subGroup: &models.Group{
				ValidationEndpoint: "",
				ChannelType:        "openai",
			},
			aggregateChannelType: "openai",
			usesClaudeEndpoint:   false,
			expected:             "/v1/chat/completions",
		},
		{
			name: "no custom endpoint - standard OpenAI Responses",
			subGroup: &models.Group{
				ValidationEndpoint: "",
				ChannelType:        "openai-response",
			},
			aggregateChannelType: "openai-response",
			usesClaudeEndpoint:   false,
			expected:             "/v1/responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := getEffectiveEndpointForAggregation(tt.subGroup, tt.aggregateChannelType, tt.usesClaudeEndpoint)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateSubGroupsAllowsAnthropicAggregateCCCompatibleChannels(t *testing.T) {
	db := setupTestDB(t)
	service := NewAggregateGroupService(db, &GroupManager{}, nil)

	groups := []models.Group{
		{
			Name:        "anthropic-native",
			DisplayName: "Anthropic Native",
			GroupType:   "standard",
			Enabled:     true,
			ChannelType: "anthropic",
			TestModel:   "claude-3-haiku-20240307",
			Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.anthropic.com","weight":1}]`)),
			Config:      datatypes.JSONMap{},
		},
		{
			Name:        "responses-cc",
			DisplayName: "Responses CC",
			GroupType:   "standard",
			Enabled:     true,
			ChannelType: "openai-response",
			TestModel:   "gpt-4.1-mini",
			Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
			Config:      datatypes.JSONMap{"cc_support": true},
		},
		{
			Name:        "gemini-cc",
			DisplayName: "Gemini CC",
			GroupType:   "standard",
			Enabled:     true,
			ChannelType: "gemini",
			TestModel:   "gemini-2.0-flash-lite",
			Upstreams:   datatypes.JSON([]byte(`[{"url":"https://generativelanguage.googleapis.com","weight":1}]`)),
			Config:      datatypes.JSONMap{"cc_support": true},
		},
	}
	require.NoError(t, db.Create(&groups).Error)

	inputs := make([]SubGroupInput, 0, len(groups))
	for _, group := range groups {
		inputs = append(inputs, SubGroupInput{GroupID: group.ID, Weight: 100})
	}

	result, err := service.ValidateSubGroups(context.Background(), "anthropic", inputs, "")
	require.NoError(t, err)
	require.Len(t, result.SubGroups, len(groups))
	assert.Equal(t, "/v1/messages", result.ValidationEndpoint)
}

func TestValidateSubGroupsRejectsDuplicateGroupIDs(t *testing.T) {
	db := setupTestDB(t)
	service := NewAggregateGroupService(db, &GroupManager{}, nil)

	subGroup := models.Group{
		Name:        "sub-duplicate-validation",
		DisplayName: "Sub Duplicate Validation",
		GroupType:   "standard",
		Enabled:     true,
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
		Config:      datatypes.JSONMap{},
	}
	require.NoError(t, db.Create(&subGroup).Error)

	_, err := service.ValidateSubGroups(context.Background(), "openai", []SubGroupInput{
		{GroupID: subGroup.ID, Weight: 100},
		{GroupID: subGroup.ID, Weight: 200},
	}, "")

	require.Error(t, err)
	var i18nErr *I18nError
	require.True(t, errors.As(err, &i18nErr))
	assert.Equal(t, app_errors.ErrValidation.Code, i18nErr.APIError.Code)
	assert.Equal(t, "validation.duplicate_sub_group", i18nErr.MessageID)
}

func TestAddSubGroupsPersistsHealthResetInterval(t *testing.T) {
	db := setupTestDB(t)
	service := NewAggregateGroupService(db, &GroupManager{}, nil)

	aggregateGroup := models.Group{
		Name:        "aggregate-add-sub-health-reset",
		DisplayName: "Aggregate Add Sub Health Reset",
		GroupType:   "aggregate",
		Enabled:     true,
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
		Config:      datatypes.JSONMap{},
	}
	subGroup := models.Group{
		Name:        "sub-add-health-reset",
		DisplayName: "Sub Add Health Reset",
		GroupType:   "standard",
		Enabled:     true,
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
		Config:      datatypes.JSONMap{},
	}
	require.NoError(t, db.Create(&aggregateGroup).Error)
	require.NoError(t, db.Create(&subGroup).Error)

	interval := int64(3600)
	err := service.AddSubGroups(context.Background(), aggregateGroup.ID, []SubGroupInput{
		{GroupID: subGroup.ID, Weight: 100, HealthResetIntervalSeconds: &interval},
	})
	require.NoError(t, err)

	var relation models.GroupSubGroup
	require.NoError(t, db.Where("group_id = ? AND sub_group_id = ?", aggregateGroup.ID, subGroup.ID).First(&relation).Error)
	assert.Equal(t, int64(3600), relation.HealthResetIntervalSeconds)
}

func TestAddSubGroupsRejectsInvalidHealthResetInterval(t *testing.T) {
	db := setupTestDB(t)
	service := NewAggregateGroupService(db, &GroupManager{}, nil)

	aggregateGroup := models.Group{
		Name:        "aggregate-add-sub-invalid-health-reset",
		DisplayName: "Aggregate Add Sub Invalid Health Reset",
		GroupType:   "aggregate",
		Enabled:     true,
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
		Config:      datatypes.JSONMap{},
	}
	subGroup := models.Group{
		Name:        "sub-add-invalid-health-reset",
		DisplayName: "Sub Add Invalid Health Reset",
		GroupType:   "standard",
		Enabled:     true,
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
		Config:      datatypes.JSONMap{},
	}
	require.NoError(t, db.Create(&aggregateGroup).Error)
	require.NoError(t, db.Create(&subGroup).Error)

	invalidInterval := int64(60)
	err := service.AddSubGroups(context.Background(), aggregateGroup.ID, []SubGroupInput{
		{GroupID: subGroup.ID, Weight: 100, HealthResetIntervalSeconds: &invalidInterval},
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&models.GroupSubGroup{}).
		Where("group_id = ? AND sub_group_id = ?", aggregateGroup.ID, subGroup.ID).
		Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestUpdateSubGroupWeightPreservesOmittedHealthResetInterval(t *testing.T) {
	db := setupTestDB(t)
	service := NewAggregateGroupService(db, &GroupManager{}, nil)

	aggregateGroup := models.Group{
		Name:        "aggregate-update-weight",
		DisplayName: "Aggregate Update Weight",
		GroupType:   "aggregate",
		Enabled:     true,
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
		Config:      datatypes.JSONMap{},
	}
	subGroup := models.Group{
		Name:        "sub-update-weight",
		DisplayName: "Sub Update Weight",
		GroupType:   "standard",
		Enabled:     true,
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":1}]`)),
		Config:      datatypes.JSONMap{},
	}
	require.NoError(t, db.Create(&aggregateGroup).Error)
	require.NoError(t, db.Create(&subGroup).Error)

	relation := models.GroupSubGroup{
		GroupID:                    aggregateGroup.ID,
		SubGroupID:                 subGroup.ID,
		Weight:                     100,
		HealthResetIntervalSeconds: 3600,
	}
	require.NoError(t, db.Create(&relation).Error)

	err := service.UpdateSubGroupWeight(context.Background(), aggregateGroup.ID, subGroup.ID, UpdateSubGroupSettingsInput{
		Weight: 200,
	})
	require.NoError(t, err)

	var updated models.GroupSubGroup
	require.NoError(t, db.Where("group_id = ? AND sub_group_id = ?", aggregateGroup.ID, subGroup.ID).First(&updated).Error)
	assert.Equal(t, 200, updated.Weight)
	assert.Equal(t, int64(3600), updated.HealthResetIntervalSeconds)

	clearInterval := int64(0)
	err = service.UpdateSubGroupWeight(context.Background(), aggregateGroup.ID, subGroup.ID, UpdateSubGroupSettingsInput{
		Weight:                     300,
		HealthResetIntervalSeconds: &clearInterval,
	})
	require.NoError(t, err)

	require.NoError(t, db.Where("group_id = ? AND sub_group_id = ?", aggregateGroup.ID, subGroup.ID).First(&updated).Error)
	assert.Equal(t, 300, updated.Weight)
	assert.Equal(t, int64(0), updated.HealthResetIntervalSeconds)

	thirtyMinutes := int64(30 * 60)
	err = service.UpdateSubGroupWeight(context.Background(), aggregateGroup.ID, subGroup.ID, UpdateSubGroupSettingsInput{
		Weight:                     400,
		HealthResetIntervalSeconds: &thirtyMinutes,
	})
	require.NoError(t, err)

	require.NoError(t, db.Where("group_id = ? AND sub_group_id = ?", aggregateGroup.ID, subGroup.ID).First(&updated).Error)
	assert.Equal(t, 400, updated.Weight)
	assert.Equal(t, int64(1800), updated.HealthResetIntervalSeconds)
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
