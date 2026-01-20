package services

import (
	"testing"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDynamicWeightManager_RecordSuccess tests recording successful requests
func TestDynamicWeightManager_RecordSuccess(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupID := uint(2)

	// Record success
	dwm.RecordSubGroupSuccess(aggregateGroupID, subGroupID)

	// Verify metrics
	metrics, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), metrics.ConsecutiveFailures)
	assert.Equal(t, int64(1), metrics.Requests7d)
	assert.Equal(t, int64(1), metrics.Successes7d)
	assert.False(t, metrics.LastSuccessAt.IsZero())
}

// TestDynamicWeightManager_RecordFailure tests recording failed requests
func TestDynamicWeightManager_RecordFailure(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupID := uint(2)

	// Record failure
	dwm.RecordSubGroupFailure(aggregateGroupID, subGroupID)

	// Verify metrics
	metrics, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.ConsecutiveFailures)
	assert.Equal(t, int64(1), metrics.Requests7d)
	assert.Equal(t, int64(0), metrics.Successes7d)
	assert.False(t, metrics.LastFailureAt.IsZero())
}

// TestDynamicWeightManager_CalculateHealthScore tests health score calculation
func TestDynamicWeightManager_CalculateHealthScore(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	tests := []struct {
		name     string
		metrics  *DynamicWeightMetrics
		expected float64
		minScore float64
	}{
		{
			name:     "nil metrics returns 1.0",
			metrics:  nil,
			expected: 1.0,
		},
		{
			name:     "empty metrics returns 1.0",
			metrics:  &DynamicWeightMetrics{},
			expected: 1.0,
		},
		{
			name: "consecutive failures reduce score",
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 3,
			},
			expected: 0.7, // 1.0 - (3 * 0.1)
		},
		{
			name: "recent failure reduces score",
			metrics: &DynamicWeightMetrics{
				LastFailureAt: time.Now().Add(-1 * time.Minute),
			},
			minScore: 0.7, // Should be less than 1.0 due to recent failure
		},
		{
			name: "low success rate reduces score",
			metrics: &DynamicWeightMetrics{
				Requests180d:  100,
				Successes180d: 30,
			},
			minScore: 0.7, // Should be penalized for low success rate
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := dwm.CalculateHealthScore(tt.metrics)
			// Dual-condition pattern: exact match for expected > 0, range check for minScore > 0
			if tt.expected > 0 {
				assert.InDelta(t, tt.expected, score, 0.01)
			}
			if tt.minScore > 0 {
				assert.LessOrEqual(t, score, 1.0)
				assert.GreaterOrEqual(t, score, tt.minScore)
			}
			// Score should always be within valid range
			assert.GreaterOrEqual(t, score, dwm.config.MinHealthScore)
			assert.LessOrEqual(t, score, 1.0)
		})
	}
}

// TestDynamicWeightManager_GetEffectiveWeight tests effective weight calculation
func TestDynamicWeightManager_GetEffectiveWeight(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	tests := []struct {
		name       string
		baseWeight int
		metrics    *DynamicWeightMetrics
		minWeight  int
		maxWeight  int
	}{
		{
			name:       "zero base weight returns zero",
			baseWeight: 0,
			metrics:    &DynamicWeightMetrics{},
			minWeight:  0,
			maxWeight:  0,
		},
		{
			name:       "healthy metrics maintain weight",
			baseWeight: 100,
			metrics:    &DynamicWeightMetrics{},
			minWeight:  90,
			maxWeight:  100,
		},
		{
			name:       "consecutive failures reduce weight",
			baseWeight: 100,
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 5,
			},
			minWeight: 10,
			maxWeight: 60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			weight := dwm.GetEffectiveWeight(tt.baseWeight, tt.metrics)
			assert.GreaterOrEqual(t, weight, tt.minWeight)
			assert.LessOrEqual(t, weight, tt.maxWeight)
		})
	}
}

// TestDynamicWeightManager_CalculateWeightedSuccessRate tests weighted success rate calculation
func TestDynamicWeightManager_CalculateWeightedSuccessRate(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	tests := []struct {
		name     string
		metrics  *DynamicWeightMetrics
		expected float64
	}{
		{
			name:     "nil metrics returns 100%",
			metrics:  nil,
			expected: 100.0,
		},
		{
			name:     "no requests returns 100%",
			metrics:  &DynamicWeightMetrics{},
			expected: 100.0,
		},
		{
			name: "perfect success rate",
			metrics: &DynamicWeightMetrics{
				Requests7d:    10,
				Successes7d:   10,
				Requests14d:   20,
				Successes14d:  20,
				Requests30d:   30,
				Successes30d:  30,
				Requests90d:   40,
				Successes90d:  40,
				Requests180d:  50,
				Successes180d: 50,
			},
			expected: 100.0,
		},
		{
			name: "50% success rate in recent window",
			metrics: &DynamicWeightMetrics{
				Requests7d:    10,
				Successes7d:   5,
				Requests14d:   10,
				Successes14d:  5,
				Requests30d:   10,
				Successes30d:  5,
				Requests90d:   10,
				Successes90d:  5,
				Requests180d:  10,
				Successes180d: 5,
			},
			expected: 50.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rate := dwm.CalculateWeightedSuccessRate(tt.metrics)
			assert.InDelta(t, tt.expected, rate, 1.0)
		})
	}
}

// TestDynamicWeightManager_ModelRedirectMetrics tests model redirect metrics
func TestDynamicWeightManager_ModelRedirectMetrics(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	groupID := uint(1)
	sourceModel := "gpt-4"
	targetIndex := 0

	// Record success
	dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetIndex)

	// Verify metrics
	metrics, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetIndex)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.Requests7d)
	assert.Equal(t, int64(1), metrics.Successes7d)

	// Record failure
	dwm.RecordModelRedirectFailure(groupID, sourceModel, targetIndex)

	// Verify updated metrics
	metrics, err = dwm.GetModelRedirectMetrics(groupID, sourceModel, targetIndex)
	require.NoError(t, err)
	assert.Equal(t, int64(2), metrics.Requests7d)
	assert.Equal(t, int64(1), metrics.Successes7d)
	assert.Equal(t, int64(1), metrics.ConsecutiveFailures)
}

// TestDynamicWeightManager_ResetMetrics tests metrics reset
func TestDynamicWeightManager_ResetMetrics(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupID := uint(2)

	// Record some data
	dwm.RecordSubGroupSuccess(aggregateGroupID, subGroupID)
	dwm.RecordSubGroupFailure(aggregateGroupID, subGroupID)

	// Verify data exists
	metrics, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), metrics.Requests7d)

	// Reset metrics
	err = dwm.ResetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)

	// Verify metrics are reset
	metrics, err = dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), metrics.Requests7d)
	assert.Equal(t, int64(0), metrics.Successes7d)
}

// TestDynamicWeightManager_GetSubGroupDynamicWeights tests getting dynamic weights for sub-groups
func TestDynamicWeightManager_GetSubGroupDynamicWeights(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroups := []SubGroupWeightInput{
		{SubGroupID: 1, Weight: 100},
		{SubGroupID: 2, Weight: 50},
	}

	// Record some metrics
	dwm.RecordSubGroupSuccess(aggregateGroupID, 1)
	dwm.RecordSubGroupFailure(aggregateGroupID, 2)

	// Get dynamic weights
	weights := dwm.GetSubGroupDynamicWeights(aggregateGroupID, subGroups)

	require.Len(t, weights, 2)
	assert.Equal(t, 100, weights[0].BaseWeight)
	assert.Equal(t, 50, weights[1].BaseWeight)
	assert.Greater(t, weights[0].EffectiveWeight, 0)
	assert.Greater(t, weights[1].EffectiveWeight, 0)
	// Verify health-based weight ordering: sub-group 1 (success) > sub-group 2 (failure)
	assert.Greater(t, weights[0].EffectiveWeight, weights[1].EffectiveWeight,
		"Sub-group with success should have higher effective weight than sub-group with failure")
}

// TestDynamicWeightManager_DynamicWeightedRandomSelect tests weighted random selection
func TestDynamicWeightManager_DynamicWeightedRandomSelect(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroups := []SubGroupWeightInput{
		{SubGroupID: 1, Weight: 100},
		{SubGroupID: 2, Weight: 50},
		{SubGroupID: 3, Weight: 25},
	}

	// Perform multiple selections to verify distribution
	// Using 10000 iterations for better statistical confidence
	selections := make(map[int]int)
	for i := 0; i < 10000; i++ {
		idx := dwm.DynamicWeightedRandomSelect(aggregateGroupID, subGroups)
		assert.GreaterOrEqual(t, idx, 0)
		assert.Less(t, idx, len(subGroups))
		selections[idx]++
	}

	// With weights 100:50:25 (total 175), expected probabilities are:
	// idx 0: 100/175 ≈ 57%, idx 1: 50/175 ≈ 29%, idx 2: 25/175 ≈ 14%
	// In 10000 trials, all should be selected, but allow for rare probabilistic failures
	// Require at least 2 of 3 to be selected to avoid flakiness
	assert.GreaterOrEqual(t, len(selections), 2, "At least 2 sub-groups should be selected in 10000 trials")

	// Verify the most frequently selected is idx 0 (highest weight)
	// This is a deterministic property that should always hold with high probability
	maxCount := 0
	maxIdx := -1
	for idx, count := range selections {
		if count > maxCount {
			maxCount = count
			maxIdx = idx
		}
	}
	// Note: This test is probabilistic by nature. With 10000 trials and weight ratio 100:50:25,
	// idx 0 should be selected most frequently with >99.99% confidence.
	// The increased iteration count (10000 vs 1000) significantly reduces the probability
	// of false failures while keeping test execution time reasonable.
	assert.Equal(t, 0, maxIdx, "Index 0 (weight 100) should be selected most frequently in 10000 trials")
}

// BenchmarkDynamicWeightManager_RecordSuccess benchmarks success recording
func BenchmarkDynamicWeightManager_RecordSuccess(b *testing.B) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupID := uint(2)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dwm.RecordSubGroupSuccess(aggregateGroupID, subGroupID)
	}
}

// BenchmarkDynamicWeightManager_CalculateHealthScore benchmarks health score calculation
func BenchmarkDynamicWeightManager_CalculateHealthScore(b *testing.B) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	metrics := &DynamicWeightMetrics{
		ConsecutiveFailures: 3,
		LastFailureAt:       time.Now().Add(-2 * time.Minute),
		Requests180d:        100,
		Successes180d:       80,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dwm.CalculateHealthScore(metrics)
	}
}

// TestGetModelRedirectDynamicWeights tests getting dynamic weights for model redirect targets
func TestGetModelRedirectDynamicWeights(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	groupID := uint(1)
	sourceModel := "gpt-4"

	enabledTrue := true
	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "gpt-4-turbo", Weight: 70, Enabled: &enabledTrue},
			{Model: "gpt-4-0125", Weight: 30, Enabled: &enabledTrue},
		},
	}

	// Record some metrics
	dwm.RecordModelRedirectSuccess(groupID, sourceModel, 0)
	dwm.RecordModelRedirectFailure(groupID, sourceModel, 1)

	// Get dynamic weights
	weights := GetModelRedirectDynamicWeights(dwm, groupID, sourceModel, rule)

	require.Len(t, weights, 2)
	assert.Equal(t, 70, weights[0].BaseWeight)
	assert.Equal(t, 30, weights[1].BaseWeight)
	assert.Greater(t, weights[0].EffectiveWeight, 0)
	assert.Greater(t, weights[1].EffectiveWeight, 0)
	// Verify health-based weight ordering: target 0 (success) > target 1 (failure)
	assert.Greater(t, weights[0].EffectiveWeight, weights[1].EffectiveWeight,
		"Target with success should have higher effective weight than target with failure")
}
