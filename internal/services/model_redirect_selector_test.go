package services

import (
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDynamicModelRedirectSelector_SelectTargetWithContext tests target selection with context
func TestDynamicModelRedirectSelector_SelectTargetWithContext(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)
	selector := NewDynamicModelRedirectSelector(dwm)

	groupID := uint(1)
	sourceModel := "gpt-4"

	enabledTrue := true
	enabledFalse := false

	tests := []struct {
		name        string
		rule        *models.ModelRedirectRuleV2
		expectError bool
		expectModel string
	}{
		{
			name:        "nil rule returns error",
			rule:        nil,
			expectError: true,
		},
		{
			name: "empty targets returns error",
			rule: &models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{},
			},
			expectError: true,
		},
		{
			name: "single enabled target",
			rule: &models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "gpt-4-turbo", Weight: 100, Enabled: &enabledTrue},
				},
			},
			expectError: false,
			expectModel: "gpt-4-turbo",
		},
		{
			name: "multiple enabled targets",
			rule: &models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "gpt-4-turbo", Weight: 70, Enabled: &enabledTrue},
					{Model: "gpt-4-0125", Weight: 30, Enabled: &enabledTrue},
				},
			},
			expectError: false,
		},
		{
			name: "disabled targets are filtered",
			rule: &models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "gpt-4-turbo", Weight: 100, Enabled: &enabledFalse},
					{Model: "gpt-4-0125", Weight: 50, Enabled: &enabledTrue},
				},
			},
			expectError: false,
			expectModel: "gpt-4-0125",
		},
		{
			name: "all disabled targets returns error",
			rule: &models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "gpt-4-turbo", Weight: 100, Enabled: &enabledFalse},
					{Model: "gpt-4-0125", Weight: 50, Enabled: &enabledFalse},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, idx, err := selector.SelectTargetWithContext(tt.rule, groupID, sourceModel)
			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, model)
				assert.Equal(t, -1, idx)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, model)
				assert.GreaterOrEqual(t, idx, 0)
				if tt.expectModel != "" {
					assert.Equal(t, tt.expectModel, model)
				}
			}
		})
	}
}

// TestDynamicModelRedirectSelector_WeightedSelection tests weighted random selection
func TestDynamicModelRedirectSelector_WeightedSelection(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)
	selector := NewDynamicModelRedirectSelector(dwm)

	groupID := uint(1)
	sourceModel := "gpt-4"

	enabledTrue := true
	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "gpt-4-turbo", Weight: 70, Enabled: &enabledTrue},
			{Model: "gpt-4-0125", Weight: 30, Enabled: &enabledTrue},
		},
	}

	// Perform multiple selections to verify distribution
	selections := make(map[string]int)
	for i := 0; i < 1000; i++ {
		model, _, err := selector.SelectTargetWithContext(rule, groupID, sourceModel)
		require.NoError(t, err)
		selections[model]++
	}

	// Verify both targets were selected
	assert.Len(t, selections, 2)
	assert.Greater(t, selections["gpt-4-turbo"], 0)
	assert.Greater(t, selections["gpt-4-0125"], 0)

	// Verify distribution roughly matches weights (70:30 ratio)
	// Allow for statistical variance
	ratio := float64(selections["gpt-4-turbo"]) / float64(selections["gpt-4-0125"])
	assert.InDelta(t, 2.33, ratio, 0.5) // 70/30 = 2.33
}

// TestDynamicModelRedirectSelector_DynamicWeightAdjustment tests dynamic weight adjustment
func TestDynamicModelRedirectSelector_DynamicWeightAdjustment(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)
	selector := NewDynamicModelRedirectSelector(dwm)

	groupID := uint(1)
	sourceModel := "gpt-4"

	enabledTrue := true
	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "gpt-4-turbo", Weight: 50, Enabled: &enabledTrue},
			{Model: "gpt-4-0125", Weight: 50, Enabled: &enabledTrue},
		},
	}

	// Record failures for first target to reduce its effective weight
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectFailure(groupID, sourceModel, 0)
	}

	// Record successes for second target
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, 1)
	}

	// Perform selections and verify second target is preferred
	selections := make(map[string]int)
	for i := 0; i < 1000; i++ {
		model, _, err := selector.SelectTargetWithContext(rule, groupID, sourceModel)
		require.NoError(t, err)
		selections[model]++
	}

	// Second target should be selected more often due to better health
	assert.Greater(t, selections["gpt-4-0125"], selections["gpt-4-turbo"])
}

// TestResolveTargetModelWithDynamicWeight tests target model resolution
func TestResolveTargetModelWithDynamicWeight(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)
	selector := NewDynamicModelRedirectSelector(dwm)

	groupID := uint(1)

	v1Map := map[string]string{
		"gpt-3.5-turbo": "gpt-3.5-turbo-0125",
	}

	enabledTrue := true
	v2Map := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {
			Targets: []models.ModelRedirectTarget{
				{Model: "gpt-4-turbo", Weight: 70, Enabled: &enabledTrue},
				{Model: "gpt-4-0125", Weight: 30, Enabled: &enabledTrue},
			},
		},
	}

	tests := []struct {
		name            string
		sourceModel     string
		expectTarget    string
		expectVersion   string
		expectCount     int
		expectIndex     int
		expectError     bool
	}{
		{
			name:          "V2 rule takes priority",
			sourceModel:   "gpt-4",
			expectVersion: "v2",
			expectCount:   2,
			expectIndex:   0, // Can be 0 or 1
		},
		{
			name:          "V1 rule fallback",
			sourceModel:   "gpt-3.5-turbo",
			expectTarget:  "gpt-3.5-turbo-0125",
			expectVersion: "v1",
			expectCount:   1,
			expectIndex:   0,
		},
		{
			name:          "no matching rule",
			sourceModel:   "claude-3",
			expectTarget:  "",
			expectVersion: "",
			expectCount:   0,
			expectIndex:   -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, version, count, idx, err := ResolveTargetModelWithDynamicWeight(
				tt.sourceModel, v1Map, v2Map, selector, groupID,
			)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.expectTarget != "" {
					assert.Equal(t, tt.expectTarget, target)
				}
				assert.Equal(t, tt.expectVersion, version)
				assert.Equal(t, tt.expectCount, count)
				if tt.expectIndex >= 0 {
					assert.GreaterOrEqual(t, idx, 0)
				} else {
					assert.Equal(t, tt.expectIndex, idx)
				}
			}
		})
	}
}

// TestDynamicModelRedirectSelector_NilDynamicWeight tests selector without dynamic weight manager
func TestDynamicModelRedirectSelector_NilDynamicWeight(t *testing.T) {
	selector := NewDynamicModelRedirectSelector(nil)

	groupID := uint(1)
	sourceModel := "gpt-4"

	enabledTrue := true
	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "gpt-4-turbo", Weight: 70, Enabled: &enabledTrue},
			{Model: "gpt-4-0125", Weight: 30, Enabled: &enabledTrue},
		},
	}

	// Should still work with static weights
	model, idx, err := selector.SelectTargetWithContext(rule, groupID, sourceModel)
	assert.NoError(t, err)
	assert.NotEmpty(t, model)
	assert.GreaterOrEqual(t, idx, 0)
}

// BenchmarkDynamicModelRedirectSelector_SelectTarget benchmarks target selection
func BenchmarkDynamicModelRedirectSelector_SelectTarget(b *testing.B) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)
	selector := NewDynamicModelRedirectSelector(dwm)

	groupID := uint(1)
	sourceModel := "gpt-4"

	enabledTrue := true
	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "gpt-4-turbo", Weight: 70, Enabled: &enabledTrue},
			{Model: "gpt-4-0125", Weight: 30, Enabled: &enabledTrue},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = selector.SelectTargetWithContext(rule, groupID, sourceModel)
	}
}
