package services

import (
	"math"
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
			expected: 0.76, // 1.0 - (3 * 0.08) with unstable channel tolerant penalty
		},
		{
			name: "recent failure reduces score",
			metrics: &DynamicWeightMetrics{
				LastFailureAt: time.Now().Add(-1 * time.Minute),
			},
			minScore: 0.75, // Should be less than 1.0 due to recent failure (penalty ~0.12 with decay)
		},
		{
			name: "low success rate reduces score",
			metrics: &DynamicWeightMetrics{
				Requests180d:  100,
				Successes180d: 35, // 35% success rate, below 40% threshold
			},
			minScore: 0.77, // Should be penalized for low success rate (penalty 0.18)
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
		minWeight  float64
		maxWeight  float64
	}{
		{
			name:       "zero base weight returns zero",
			baseWeight: 0,
			metrics:    &DynamicWeightMetrics{},
			minWeight:  0.0,
			maxWeight:  0.0,
		},
		{
			name:       "healthy metrics maintain weight",
			baseWeight: 100,
			metrics:    &DynamicWeightMetrics{},
			minWeight:  90.0,
			maxWeight:  100.0,
		},
		{
			name:       "consecutive failures reduce weight",
			baseWeight: 100,
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 5,
			},
			minWeight: 30.0, // Health score ~0.60 (1.0 - 5*0.08 = 0.60), in medium range (0.50-0.75), quadratic: 0.60^2 = 0.36, weight ~36.0
			maxWeight: 40.0,
		},
		{
			name:       "poor health returns capped weight of 1.0",
			baseWeight: 100,
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 6, // Max penalty 0.40 (capped at 5 failures)
				Requests180d:        100,
				Successes180d:       20, // 20% success rate, penalty 0.18
				Requests7d:          10,
				Successes7d:         2,
				LastFailureAt:       time.Now().Add(-1 * time.Minute), // Recent failure ~0.11 penalty
			},
			// Total penalty ~0.69 (0.40 + 0.18 + 0.11), health ~0.31, in critical range (<=0.50)
			// Critical health: capped at 1.0
			minWeight: 1.0,
			maxWeight: 1.0,
		},
		{
			name:       "good health score (>0.75) uses linear scaling",
			baseWeight: 100,
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 3, // 3 * 0.08 = 0.24 penalty, health ~0.76
				Requests180d:        50,
				Successes180d:       45, // 90% success rate (above 40% threshold, no penalty)
				Requests7d:          5,
				Successes7d:         4,
			},
			minWeight: 70.0, // Health ~0.76, above medium threshold (0.75), linear scaling
			maxWeight: 80.0,
		},
		{
			name:       "small base weight with medium health gets minimum weight of 0.1",
			baseWeight: 1,
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 3, // Medium health
			},
			minWeight: 0.1,
			maxWeight: 1.0,
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

// TestGetEffectiveWeightForSelection tests conversion of float effective weight to integer for weighted selection
// The function multiplies by 10 to preserve 1 decimal place precision in weighted random selection.
// This is necessary because:
// 1. Effective weights are float64 with 1 decimal place (e.g., 1.5, 0.1, 36.7)
// 2. Weighted random selection requires integer weights
// 3. Direct rounding would lose precision and distort weight ratios
// Example: weights [1.5, 0.5] should maintain 3:1 ratio
//   - Direct rounding: [2, 1] = 2:1 ratio (incorrect)
//   - Multiply by 10: [15, 5] = 3:1 ratio (correct)
func TestGetEffectiveWeightForSelection(t *testing.T) {
	tests := []struct {
		name            string
		effectiveWeight float64
		expected        int
	}{
		{
			name:            "zero weight returns zero",
			effectiveWeight: 0.0,
			expected:        0,
		},
		{
			name:            "minimum weight 0.1 converts to 1",
			effectiveWeight: 0.1,
			expected:        1,
		},
		{
			name:            "weight 0.5 converts to 5 (preserves ratio)",
			effectiveWeight: 0.5,
			expected:        5,
		},
		{
			name:            "weight 1.0 converts to 10",
			effectiveWeight: 1.0,
			expected:        10,
		},
		{
			name:            "weight 1.5 converts to 15 (preserves ratio)",
			effectiveWeight: 1.5,
			expected:        15,
		},
		{
			name:            "weight 10.0 converts to 100",
			effectiveWeight: 10.0,
			expected:        100,
		},
		{
			name:            "weight 36.0 converts to 360",
			effectiveWeight: 36.0,
			expected:        360,
		},
		{
			name:            "weight 76.5 converts to 765 (preserves decimal)",
			effectiveWeight: 76.5,
			expected:        765,
		},
		{
			name:            "weight 100.0 converts to 1000",
			effectiveWeight: 100.0,
			expected:        1000,
		},
		{
			name:            "very small weight 0.05 rounds to minimum 1",
			effectiveWeight: 0.05,
			expected:        1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetEffectiveWeightForSelection(tt.effectiveWeight)
			assert.Equal(t, tt.expected, result)
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
	targetModel := "gpt-4-turbo"

	// Record success
	dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetModel)

	// Verify metrics
	metrics, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetModel)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.Requests7d)
	assert.Equal(t, int64(1), metrics.Successes7d)

	// Record failure
	dwm.RecordModelRedirectFailure(groupID, sourceModel, targetModel)

	// Verify updated metrics
	metrics, err = dwm.GetModelRedirectMetrics(groupID, sourceModel, targetModel)
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
	assert.Greater(t, weights[0].EffectiveWeight, 0.0)
	assert.Greater(t, weights[1].EffectiveWeight, 0.0)
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
	dwm.RecordModelRedirectSuccess(groupID, sourceModel, "gpt-4-turbo")
	dwm.RecordModelRedirectFailure(groupID, sourceModel, "gpt-4-0125")

	// Get dynamic weights
	weights := GetModelRedirectDynamicWeights(dwm, groupID, sourceModel, rule)

	require.Len(t, weights, 2)
	assert.Equal(t, 70, weights[0].BaseWeight)
	assert.Equal(t, 30, weights[1].BaseWeight)
	assert.Greater(t, weights[0].EffectiveWeight, 0.0)
	assert.Greater(t, weights[1].EffectiveWeight, 0.0)
	// Verify health-based weight ordering: target 0 (success) > target 1 (failure)
	assert.Greater(t, weights[0].EffectiveWeight, weights[1].EffectiveWeight,
		"Target with success should have higher effective weight than target with failure")
}

// TestDynamicWeightManager_RecordRetryAndFinalRequests tests that both retry and final requests are recorded
func TestDynamicWeightManager_RecordRetryAndFinalRequests(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupID := uint(2)

	// Record a retry request failure
	dwm.RecordSubGroupFailure(aggregateGroupID, subGroupID)

	// Verify retry failure is recorded
	metrics, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metrics.ConsecutiveFailures)
	assert.Equal(t, int64(1), metrics.Requests7d)
	assert.Equal(t, int64(0), metrics.Successes7d)
	assert.False(t, metrics.LastFailureAt.IsZero())

	// Record a final request success
	dwm.RecordSubGroupSuccess(aggregateGroupID, subGroupID)

	// Verify final success is also recorded
	metrics, err = dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), metrics.ConsecutiveFailures, "Consecutive failures should reset after success")
	assert.Equal(t, int64(2), metrics.Requests7d, "Both retry and final requests should be counted")
	assert.Equal(t, int64(1), metrics.Successes7d)
	assert.False(t, metrics.LastSuccessAt.IsZero())
}

// TestDynamicWeightManager_AggregateRetryScenario tests health tracking in aggregate retry scenario
func TestDynamicWeightManager_AggregateRetryScenario(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupA := uint(2)
	subGroupB := uint(3)

	// Simulate aggregate retry scenario:
	// 1. Sub-group A fails (retry request)
	dwm.RecordSubGroupFailure(aggregateGroupID, subGroupA)

	// 2. Sub-group B succeeds (final request)
	dwm.RecordSubGroupSuccess(aggregateGroupID, subGroupB)

	// Verify sub-group A health is affected by the failure
	metricsA, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupA)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metricsA.ConsecutiveFailures)
	assert.Equal(t, int64(1), metricsA.Requests7d)
	assert.Equal(t, int64(0), metricsA.Successes7d)

	healthScoreA := dwm.CalculateHealthScore(metricsA)
	assert.Less(t, healthScoreA, 1.0, "Sub-group A health should be less than 100% after failure")

	// Verify sub-group B health is normal
	metricsB, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupB)
	require.NoError(t, err)
	assert.Equal(t, int64(0), metricsB.ConsecutiveFailures)
	assert.Equal(t, int64(1), metricsB.Requests7d)
	assert.Equal(t, int64(1), metricsB.Successes7d)

	healthScoreB := dwm.CalculateHealthScore(metricsB)
	assert.Equal(t, 1.0, healthScoreB, "Sub-group B health should be 100% after success")

	// Verify effective weights reflect health difference
	subGroups := []SubGroupWeightInput{
		{SubGroupID: subGroupA, Weight: 100},
		{SubGroupID: subGroupB, Weight: 100},
	}
	weights := dwm.GetSubGroupDynamicWeights(aggregateGroupID, subGroups)

	require.Len(t, weights, 2)
	assert.Less(t, weights[0].EffectiveWeight, weights[1].EffectiveWeight,
		"Failed sub-group should have lower effective weight than successful sub-group")
}

// TestDynamicWeightManager_ModelRedirectRetryScenario tests health tracking in model redirect retry scenario
func TestDynamicWeightManager_ModelRedirectRetryScenario(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	groupID := uint(1)
	sourceModel := "gpt-4"
	targetModelA := "gpt-4-turbo"
	targetModelB := "gpt-4-0125"

	// Simulate model redirect retry scenario:
	// 1. Target A fails (retry request)
	dwm.RecordModelRedirectFailure(groupID, sourceModel, targetModelA)

	// 2. Target B succeeds (final request)
	dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetModelB)

	// Verify target A health is affected by the failure
	metricsA, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetModelA)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metricsA.ConsecutiveFailures)
	assert.Equal(t, int64(1), metricsA.Requests7d)
	assert.Equal(t, int64(0), metricsA.Successes7d)

	healthScoreA := dwm.CalculateHealthScore(metricsA)
	assert.Less(t, healthScoreA, 1.0, "Target A health should be less than 100% after failure")

	// Verify target B health is normal
	metricsB, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetModelB)
	require.NoError(t, err)
	assert.Equal(t, int64(0), metricsB.ConsecutiveFailures)
	assert.Equal(t, int64(1), metricsB.Requests7d)
	assert.Equal(t, int64(1), metricsB.Successes7d)

	healthScoreB := dwm.CalculateHealthScore(metricsB)
	assert.Equal(t, 1.0, healthScoreB, "Target B health should be 100% after success")

	// Verify effective weights reflect health difference
	targetModels := []string{targetModelA, targetModelB}
	targetWeights := []int{100, 100}
	effectiveWeights := dwm.GetModelRedirectEffectiveWeights(groupID, sourceModel, targetModels, targetWeights)

	require.Len(t, effectiveWeights, 2)
	assert.Less(t, effectiveWeights[0], effectiveWeights[1],
		"Failed target should have lower effective weight than successful target")
}

// TestDynamicWeightManager_MultipleRetriesHealthDecay tests health decay with multiple retry failures
func TestDynamicWeightManager_MultipleRetriesHealthDecay(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupID := uint(2)

	// Record multiple retry failures
	for i := 0; i < 5; i++ {
		dwm.RecordSubGroupFailure(aggregateGroupID, subGroupID)
	}

	// Verify health degrades with multiple failures
	metrics, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(5), metrics.ConsecutiveFailures)
	assert.Equal(t, int64(5), metrics.Requests7d)
	assert.Equal(t, int64(0), metrics.Successes7d)

	healthScore := dwm.CalculateHealthScore(metrics)
	assert.LessOrEqual(t, healthScore, 0.5, "Health should be significantly degraded after 5 consecutive failures")
	assert.GreaterOrEqual(t, healthScore, dwm.config.MinHealthScore, "Health should not go below minimum")

	// Verify effective weight is reduced
	effectiveWeight := dwm.GetEffectiveWeight(100, metrics)
	assert.LessOrEqual(t, effectiveWeight, 50.0, "Effective weight should be significantly reduced")
}

// TestDynamicWeightManager_HealthRecoveryAfterRetryFailures tests health recovery after retry failures
func TestDynamicWeightManager_HealthRecoveryAfterRetryFailures(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	aggregateGroupID := uint(1)
	subGroupID := uint(2)

	// Record retry failures
	dwm.RecordSubGroupFailure(aggregateGroupID, subGroupID)
	dwm.RecordSubGroupFailure(aggregateGroupID, subGroupID)

	// Verify health is degraded
	metrics, err := dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	healthBefore := dwm.CalculateHealthScore(metrics)
	assert.Less(t, healthBefore, 1.0, "Health should be degraded after failures")

	// Record successful requests to recover
	for i := 0; i < 10; i++ {
		dwm.RecordSubGroupSuccess(aggregateGroupID, subGroupID)
	}

	// Verify health recovers
	metrics, err = dwm.GetSubGroupMetrics(aggregateGroupID, subGroupID)
	require.NoError(t, err)
	healthAfter := dwm.CalculateHealthScore(metrics)
	assert.Greater(t, healthAfter, healthBefore, "Health should improve after successful requests")
	// Health may not fully recover to 1.0 immediately due to weighted success rate calculation
	// With 2 failures and 10 successes (83.3% success rate), health should be high but may have small penalty
	assert.GreaterOrEqual(t, healthAfter, 0.8, "Health should be mostly recovered with good success rate")
}

// TestDynamicWeightManager_HubHealthScoreCalculation tests that Hub health scores are calculated correctly
// Hub uses the same dynamic weight system as aggregate groups and model redirects
func TestDynamicWeightManager_HubHealthScoreCalculation(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	// Simulate Hub routing scenario where a group is selected and used
	// Hub doesn't directly record metrics, but the proxy server does
	// This test verifies the health calculation logic works for Hub use cases

	groupID := uint(100)
	subGroupID := uint(101)

	// Simulate multiple requests through Hub to a group
	// Some fail (retry), some succeed (final)
	dwm.RecordSubGroupFailure(groupID, subGroupID) // Retry failure
	dwm.RecordSubGroupSuccess(groupID, subGroupID) // Final success
	dwm.RecordSubGroupFailure(groupID, subGroupID) // Retry failure
	dwm.RecordSubGroupSuccess(groupID, subGroupID) // Final success

	// Verify health score reflects both retry and final requests
	metrics, err := dwm.GetSubGroupMetrics(groupID, subGroupID)
	require.NoError(t, err)
	assert.Equal(t, int64(4), metrics.Requests7d, "All requests (retry + final) should be counted")
	assert.Equal(t, int64(2), metrics.Successes7d, "Only successful requests should be counted")

	healthScore := dwm.CalculateHealthScore(metrics)
	// With 50% success rate and interleaved pattern (no consecutive failures at end),
	// health should be moderate but not severely degraded
	assert.Greater(t, healthScore, 0.0, "Health score should be positive")
	assert.LessOrEqual(t, healthScore, 1.0, "Health score should not exceed 1.0")
	assert.GreaterOrEqual(t, healthScore, 0.5, "Health should not be too low for 50% success rate")
	assert.Less(t, healthScore, 1.0, "Health should reflect failures in history")
}

// TestDynamicWeightManager_HubGroupSelectionWithHealth tests Hub group selection based on health
func TestDynamicWeightManager_HubGroupSelectionWithHealth(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	// Simulate Hub scenario with multiple groups providing the same model
	// Group A has poor health, Group B has good health
	aggregateGroupID := uint(200)
	groupA := uint(201)
	groupB := uint(202)

	// Group A: Multiple failures
	for i := 0; i < 5; i++ {
		dwm.RecordSubGroupFailure(aggregateGroupID, groupA)
	}
	dwm.RecordSubGroupSuccess(aggregateGroupID, groupA)

	// Group B: Mostly successes
	for i := 0; i < 10; i++ {
		dwm.RecordSubGroupSuccess(aggregateGroupID, groupB)
	}
	dwm.RecordSubGroupFailure(aggregateGroupID, groupB)

	// Calculate health scores
	metricsA, err := dwm.GetSubGroupMetrics(aggregateGroupID, groupA)
	require.NoError(t, err)
	healthA := dwm.CalculateHealthScore(metricsA)

	metricsB, err := dwm.GetSubGroupMetrics(aggregateGroupID, groupB)
	require.NoError(t, err)
	healthB := dwm.CalculateHealthScore(metricsB)

	// Verify Group B has significantly better health than Group A
	assert.Greater(t, healthB, healthA, "Group B should have better health than Group A")
	assert.Less(t, healthA, 0.75, "Group A should have degraded health due to failures")
	assert.GreaterOrEqual(t, healthB, 0.75, "Group B should have good health")

	// Verify effective weights reflect health difference
	subGroups := []SubGroupWeightInput{
		{SubGroupID: groupA, Weight: 100},
		{SubGroupID: groupB, Weight: 100},
	}
	weights := dwm.GetSubGroupDynamicWeights(aggregateGroupID, subGroups)

	require.Len(t, weights, 2)
	// Group B should have much higher effective weight due to better health
	// weights[0] corresponds to groupA, weights[1] corresponds to groupB (same order as input)
	effectiveWeightA := weights[0].EffectiveWeight
	effectiveWeightB := weights[1].EffectiveWeight
	assert.Greater(t, effectiveWeightB, effectiveWeightA,
		"Group B effective weight should be higher than Group A")
}

// TestDynamicWeightManager_HubRetryFailureImpact tests that Hub retry failures impact health
func TestDynamicWeightManager_HubRetryFailureImpact(t *testing.T) {
	t.Parallel() // Enable parallel execution
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	// Simulate Hub scenario where first group fails (retry), second succeeds (final)
	aggregateGroupID := uint(300)
	firstGroup := uint(301)
	secondGroup := uint(302)

	// First group fails on retry
	dwm.RecordSubGroupFailure(aggregateGroupID, firstGroup)

	// Second group succeeds on final request
	dwm.RecordSubGroupSuccess(aggregateGroupID, secondGroup)

	// Verify first group health is impacted
	metricsFirst, err := dwm.GetSubGroupMetrics(aggregateGroupID, firstGroup)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metricsFirst.Requests7d, "Retry failure should be recorded")
	assert.Equal(t, int64(0), metricsFirst.Successes7d, "No successes for first group")
	assert.Equal(t, int64(1), metricsFirst.ConsecutiveFailures, "Consecutive failures should be tracked")

	healthFirst := dwm.CalculateHealthScore(metricsFirst)
	assert.Less(t, healthFirst, 1.0, "First group health should be degraded after retry failure")

	// Verify second group health is normal
	metricsSecond, err := dwm.GetSubGroupMetrics(aggregateGroupID, secondGroup)
	require.NoError(t, err)
	assert.Equal(t, int64(1), metricsSecond.Requests7d, "Final success should be recorded")
	assert.Equal(t, int64(1), metricsSecond.Successes7d, "Success should be counted")
	assert.Equal(t, int64(0), metricsSecond.ConsecutiveFailures, "No failures for second group")

	healthSecond := dwm.CalculateHealthScore(metricsSecond)
	assert.Equal(t, 1.0, healthSecond, "Second group health should be perfect after success")

	// This test verifies that Hub routing correctly reflects group health
	// even when the overall request succeeds via retry to another group
}

// TestDynamicWeightManager_NonLinearHealthPenalty tests the non-linear penalty for low health scores
func TestDynamicWeightManager_NonLinearHealthPenalty(t *testing.T) {
	t.Parallel()
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	tests := []struct {
		name              string
		metrics           *DynamicWeightMetrics
		baseWeight        int
		expectedMinWeight float64
		expectedMaxWeight float64
		description       string
	}{
		{
			name: "poor health (consecutive failures + low success rate)",
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 6, // Max penalty 0.40 (capped at 5 failures)
				Requests180d:        100,
				Successes180d:       20, // 20% success rate, penalty 0.18
				Requests7d:          10,
				Successes7d:         2,
			},
			baseWeight:        100,
			expectedMinWeight: 1.0, // Health ~0.42 (1.0 - 0.40 - 0.18), in critical range (<=0.50), capped at 1.0
			expectedMaxWeight: 1.0,
			description:       "Health score ~0.42 in critical range, weight capped at 1.0",
		},
		{
			name: "moderate health (few failures) - moderate penalty",
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 3, // 0.24 penalty
				Requests180d:        50,
				Successes180d:       45, // 90% success rate (above 40% threshold, no penalty)
				Requests7d:          5,
				Successes7d:         4,
			},
			baseWeight:        100,
			expectedMinWeight: 70.0, // Health ~0.76, above medium threshold (0.75), linear scaling
			expectedMaxWeight: 80.0,
			description:       "Health score ~0.76 with linear scaling in good range",
		},
		{
			name: "good health (minimal failures, good success rate)",
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 2, // 0.16 penalty
				Requests180d:        50,
				Successes180d:       45, // 90% success rate
				Requests7d:          5,
				Successes7d:         4,
			},
			baseWeight:        100,
			expectedMinWeight: 75.0,
			expectedMaxWeight: 90.0,
			description:       "Health score around 0.84 with linear scaling",
		},
		{
			name: "good health (minimal failures) - minimal penalty",
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 1,
				Requests180d:        50,
				Successes180d:       48, // 96% success rate
				Requests7d:          5,
				Successes7d:         5,
			},
			baseWeight:        100,
			expectedMinWeight: 85.0,
			expectedMaxWeight: 95.0,
			description:       "Health score ~0.92 should use linear scaling",
		},
		{
			name: "small weight with medium health - minimum 0.1",
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 3,
			},
			baseWeight:        1,
			expectedMinWeight: 0.1,
			expectedMaxWeight: 1.0,
			description:       "Even with penalty, non-critical health gets minimum weight of 0.1",
		},
		{
			name: "small weight with critical health - minimum 0.1",
			metrics: &DynamicWeightMetrics{
				ConsecutiveFailures: 5,
				Requests180d:        100,
				Successes180d:       20, // 20% success rate
				Requests7d:          10,
				Successes7d:         2,
			},
			baseWeight:        1,
			expectedMinWeight: 0.1,
			expectedMaxWeight: 0.1,
			description:       "Critical health results in minimum weight of 0.1 to allow recovery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effectiveWeight := dwm.GetEffectiveWeight(tt.baseWeight, tt.metrics)

			assert.GreaterOrEqual(t, effectiveWeight, tt.expectedMinWeight,
				"Effective weight should be >= %.1f for %s", tt.expectedMinWeight, tt.description)
			assert.LessOrEqual(t, effectiveWeight, tt.expectedMaxWeight,
				"Effective weight should be <= %.1f for %s", tt.expectedMaxWeight, tt.description)

			// Log for debugging
			healthScore := dwm.CalculateHealthScore(tt.metrics)
			t.Logf("%s: health=%.2f, baseWeight=%d, effectiveWeight=%.1f",
				tt.name, healthScore, tt.baseWeight, effectiveWeight)
		})
	}
}

// TestDynamicWeightManager_HealthThresholdBehavior tests behavior at health threshold boundaries
func TestDynamicWeightManager_HealthThresholdBehavior(t *testing.T) {
	t.Parallel()
	memStore := store.NewMemoryStore()

	// Create custom config to test specific thresholds
	config := DefaultDynamicWeightConfig()
	config.CriticalHealthThreshold = 0.35 // Adjusted to be reachable with current penalty system
	config.MediumHealthThreshold = 0.65
	config.MediumHealthPenaltyExponent = 2.0

	dwm := NewDynamicWeightManagerWithConfig(memStore, config)

	// Test case 1: Just above critical threshold
	metrics1 := &DynamicWeightMetrics{
		ConsecutiveFailures: 5,
		Requests180d:        50,
		Successes180d:       35, // 70% success rate (better than threshold)
		Requests7d:          5,
		Successes7d:         3,
	}
	weight1 := dwm.GetEffectiveWeight(100, metrics1)
	health1 := dwm.CalculateHealthScore(metrics1)
	t.Logf("Just above critical: health=%.3f, weight=%.1f", health1, weight1)
	if health1 > config.CriticalHealthThreshold {
		assert.Greater(t, weight1, 1.0, "Weight should be > 1.0 when health is above critical threshold")
	} else {
		assert.LessOrEqual(t, weight1, 1.0, "Weight should be <= 1.0 when health is at or below critical threshold")
	}

	// Test case 2: At or below critical threshold
	// Added LastFailureAt to apply recent-failure penalty and push health below threshold
	metrics2 := &DynamicWeightMetrics{
		ConsecutiveFailures: 6,
		Requests180d:        100,
		Successes180d:       20, // 20% success rate
		Requests7d:          10,
		Successes7d:         2,
		LastFailureAt:       time.Now().Add(-1 * time.Minute), // Apply recent-failure penalty
	}
	weight2 := dwm.GetEffectiveWeight(100, metrics2)
	health2 := dwm.CalculateHealthScore(metrics2)
	t.Logf("At or below critical: health=%.3f, weight=%.1f", health2, weight2)
	if health2 <= config.CriticalHealthThreshold {
		// Critical health: capped at 1.0 to prevent unhealthy high-weight targets
		// from dominating healthy low-weight targets
		assert.Equal(t, 1.0, weight2, "Weight should be capped at 1.0 when health is at or below critical threshold")
	}

	// Test case 3: Just below medium threshold (should use quadratic penalty)
	// Increased ConsecutiveFailures to push health below medium threshold
	metrics3 := &DynamicWeightMetrics{
		ConsecutiveFailures: 5, // Increased to push health below medium threshold
		Requests180d:        50,
		Successes180d:       45, // 90% success rate
		Requests7d:          5,
		Successes7d:         4,
	}
	weight3 := dwm.GetEffectiveWeight(100, metrics3)
	health3 := dwm.CalculateHealthScore(metrics3)
	t.Logf("Below medium threshold: health=%.3f, weight=%.1f", health3, weight3)
	if health3 < config.MediumHealthThreshold && health3 >= config.CriticalHealthThreshold {
		expectedWeight3 := math.Round(100*health3*health3*10) / 10 // Quadratic penalty with 1 decimal place
		// Allow some tolerance due to rounding
		assert.InDelta(t, expectedWeight3, weight3, 0.5, "Should apply quadratic penalty below medium threshold")
	}

	// Test case 4: Above medium threshold (should use linear scaling)
	metrics4 := &DynamicWeightMetrics{
		ConsecutiveFailures: 1,
		Requests180d:        50,
		Successes180d:       48, // 96% success rate
		Requests7d:          5,
		Successes7d:         5,
	}
	weight4 := dwm.GetEffectiveWeight(100, metrics4)
	health4 := dwm.CalculateHealthScore(metrics4)
	t.Logf("Above medium threshold: health=%.3f, weight=%.1f", health4, weight4)
	if health4 >= config.MediumHealthThreshold {
		expectedWeight4 := math.Round(100*health4*10) / 10 // Linear scaling with 1 decimal place
		assert.InDelta(t, expectedWeight4, weight4, 0.5, "Should use linear scaling above medium threshold")
	}
}
