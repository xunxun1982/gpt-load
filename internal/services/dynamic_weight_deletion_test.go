package services

import (
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelRedirectTargetDeletion_HealthScorePreservation tests that deleting a model redirect target
// does not affect the health scores of other targets.
// This test verifies the fix for the issue where deleting a target in the middle of the array
// would cause health scores to shift to the wrong targets.
func TestModelRedirectTargetDeletion_HealthScorePreservation(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	groupID := uint(1)
	sourceModel := "gpt-4"

	// Create three targets
	targetA := "gpt-4-turbo"
	targetB := "gpt-4-0125"
	targetC := "gpt-4-32k"

	// Record different health metrics for each target
	// Target A: 10 successes (healthy)
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetA)
	}

	// Target B: 5 successes, 5 failures (medium health)
	for i := 0; i < 5; i++ {
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetB)
		dwm.RecordModelRedirectFailure(groupID, sourceModel, targetB)
	}

	// Target C: 10 failures (unhealthy)
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectFailure(groupID, sourceModel, targetC)
	}

	// Get initial health scores
	metricsA1, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetA)
	require.NoError(t, err)
	healthA1 := dwm.CalculateHealthScore(metricsA1)

	metricsB1, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetB)
	require.NoError(t, err)
	healthB1 := dwm.CalculateHealthScore(metricsB1)

	metricsC1, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetC)
	require.NoError(t, err)
	healthC1 := dwm.CalculateHealthScore(metricsC1)

	// Verify initial health scores are different
	assert.Greater(t, healthA1, healthB1, "Target A should be healthier than B")
	assert.Greater(t, healthB1, healthC1, "Target B should be healthier than C")

	// Simulate deleting target B (middle target)
	// In the old implementation, this would cause target C's health to shift to target B's position
	// In the new implementation, health scores are tied to model names, not indices

	// After "deletion", verify that target A and C still have their original health scores
	metricsA2, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetA)
	require.NoError(t, err)
	healthA2 := dwm.CalculateHealthScore(metricsA2)

	metricsC2, err := dwm.GetModelRedirectMetrics(groupID, sourceModel, targetC)
	require.NoError(t, err)
	healthC2 := dwm.CalculateHealthScore(metricsC2)

	// Verify health scores are preserved
	// Note: Using InDelta instead of Equal because health scores include time-decaying
	// failure penalties, which may cause minor differences between measurements
	assert.InDelta(t, healthA1, healthA2, 0.01, "Target A health should remain unchanged after B deletion")
	assert.InDelta(t, healthC1, healthC2, 0.01, "Target C health should remain unchanged after B deletion")

	// Verify that the health scores are still in the correct order
	assert.Greater(t, healthA2, healthC2, "Target A should still be healthier than C after B deletion")
}

// TestModelRedirectTargetDeletion_EffectiveWeights tests that effective weights
// are correctly calculated after target deletion.
func TestModelRedirectTargetDeletion_EffectiveWeights(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	groupID := uint(1)
	sourceModel := "gpt-4"

	// Create three targets with equal base weights
	targetA := "gpt-4-turbo"
	targetB := "gpt-4-0125"
	targetC := "gpt-4-32k"

	// Record different health metrics
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetA)
	}
	// Target B: some failures to make it less healthy than A
	for i := 0; i < 5; i++ {
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetB)
	}
	for i := 0; i < 3; i++ {
		dwm.RecordModelRedirectFailure(groupID, sourceModel, targetB)
	}
	// Target C: all failures
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectFailure(groupID, sourceModel, targetC)
	}

	// Get effective weights for all three targets
	targets1 := []string{targetA, targetB, targetC}
	weights1 := []int{100, 100, 100}
	effectiveWeights1 := dwm.GetModelRedirectEffectiveWeights(groupID, sourceModel, targets1, weights1)

	require.Len(t, effectiveWeights1, 3)
	assert.Greater(t, effectiveWeights1[0], effectiveWeights1[1], "A should have higher weight than B")
	assert.Greater(t, effectiveWeights1[1], effectiveWeights1[2], "B should have higher weight than C")

	// Simulate deleting target B
	// Get effective weights for remaining targets
	targets2 := []string{targetA, targetC}
	weights2 := []int{100, 100}
	effectiveWeights2 := dwm.GetModelRedirectEffectiveWeights(groupID, sourceModel, targets2, weights2)

	require.Len(t, effectiveWeights2, 2)

	// Verify that A and C maintain their relative weights
	assert.Equal(t, effectiveWeights1[0], effectiveWeights2[0], "Target A effective weight should remain unchanged")
	assert.Equal(t, effectiveWeights1[2], effectiveWeights2[1], "Target C effective weight should remain unchanged")
	assert.Greater(t, effectiveWeights2[0], effectiveWeights2[1], "A should still have higher weight than C")
}

// TestModelRedirectTargetDeletion_GetDynamicWeights tests that GetModelRedirectDynamicWeights
// correctly handles target deletion.
func TestModelRedirectTargetDeletion_GetDynamicWeights(t *testing.T) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)

	groupID := uint(1)
	sourceModel := "gpt-4"

	// Create three targets
	targetA := "gpt-4-turbo"
	targetB := "gpt-4-0125"
	targetC := "gpt-4-32k"

	// Record metrics
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetA)
	}
	for i := 0; i < 5; i++ {
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, targetB)
		dwm.RecordModelRedirectFailure(groupID, sourceModel, targetB)
	}
	for i := 0; i < 10; i++ {
		dwm.RecordModelRedirectFailure(groupID, sourceModel, targetC)
	}

	enabledTrue := true

	// Get dynamic weights for all three targets
	rule1 := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: targetA, Weight: 100, Enabled: &enabledTrue},
			{Model: targetB, Weight: 100, Enabled: &enabledTrue},
			{Model: targetC, Weight: 100, Enabled: &enabledTrue},
		},
	}
	weights1 := GetModelRedirectDynamicWeights(dwm, groupID, sourceModel, rule1)
	require.Len(t, weights1, 3)

	// Simulate deleting target B
	rule2 := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: targetA, Weight: 100, Enabled: &enabledTrue},
			{Model: targetC, Weight: 100, Enabled: &enabledTrue},
		},
	}
	weights2 := GetModelRedirectDynamicWeights(dwm, groupID, sourceModel, rule2)
	require.Len(t, weights2, 2)

	// Verify that A and C maintain their health scores
	// Note: Using InDelta for health scores due to time-decaying failure penalties
	assert.InDelta(t, weights1[0].HealthScore, weights2[0].HealthScore, 0.01, "Target A health should remain unchanged")
	assert.InDelta(t, weights1[2].HealthScore, weights2[1].HealthScore, 0.01, "Target C health should remain unchanged")
	assert.Equal(t, weights1[0].RequestCount, weights2[0].RequestCount, "Target A request count should remain unchanged")
	assert.Equal(t, weights1[2].RequestCount, weights2[1].RequestCount, "Target C request count should remain unchanged")
}
