package services

import (
	"fmt"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/store"
)

// BenchmarkSelectTargetWithContext benchmarks target selection with dynamic weights
func BenchmarkSelectTargetWithContext(b *testing.B) {
	selector := NewDynamicModelRedirectSelector(nil) // Benchmark with nil DWM for baseline performance

	testCases := []struct {
		name string
		rule *models.ModelRedirectRuleV2
	}{
		{
			"SingleTarget",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "gpt-4-turbo", Weight: 100},
				},
			},
		},
		{
			"TwoTargets_EqualWeight",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "gpt-4-turbo", Weight: 100},
					{Model: "gpt-4-turbo-preview", Weight: 100},
				},
			},
		},
		{
			"TwoTargets_UnequalWeight",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "gpt-4-turbo", Weight: 800},
					{Model: "gpt-4-turbo-preview", Weight: 200},
				},
			},
		},
		{
			"FiveTargets",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "target-1", Weight: 100},
					{Model: "target-2", Weight: 200},
					{Model: "target-3", Weight: 300},
					{Model: "target-4", Weight: 200},
					{Model: "target-5", Weight: 200},
				},
			},
		},
		{
			"TenTargets",
			generateTargets(10),
		},
		{
			"TwentyTargets",
			generateTargets(20),
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _, _ = selector.SelectTargetWithContext(tc.rule, 1, "gpt-4")
			}
		})
	}
}

// BenchmarkSelectTargetWithDynamicWeight benchmarks target selection with real DynamicWeightManager
func BenchmarkSelectTargetWithDynamicWeight(b *testing.B) {
	memStore := store.NewMemoryStore()
	dwm := NewDynamicWeightManager(memStore)
	selector := NewDynamicModelRedirectSelector(dwm)

	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "gpt-4-turbo", Weight: 70},
			{Model: "gpt-4-0125", Weight: 30},
		},
	}

	groupID := uint(1)
	sourceModel := "gpt-4"

	// Record some health metrics to simulate realistic scenario
	dwm.RecordModelRedirectSuccess(groupID, sourceModel, "gpt-4-turbo")
	dwm.RecordModelRedirectSuccess(groupID, sourceModel, "gpt-4-0125")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = selector.SelectTargetWithContext(rule, groupID, sourceModel)
	}
}

// BenchmarkResolveTargetModelWithDynamicWeight benchmarks full resolution
func BenchmarkResolveTargetModelWithDynamicWeight(b *testing.B) {
	selector := NewDynamicModelRedirectSelector(nil)

	v1Map := map[string]string{
		"gpt-3.5": "gpt-3.5-turbo",
	}

	v2Map := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {
			Targets: []models.ModelRedirectTarget{
				{Model: "gpt-4-turbo", Weight: 700},
				{Model: "gpt-4-turbo-preview", Weight: 300},
			},
		},
		"claude-3": {
			Targets: []models.ModelRedirectTarget{
				{Model: "claude-3-opus", Weight: 500},
				{Model: "claude-3-sonnet", Weight: 300},
				{Model: "claude-3-haiku", Weight: 200},
			},
		},
	}

	testCases := []struct {
		name  string
		model string
	}{
		{"V2_GPT4", "gpt-4"},
		{"V2_Claude3", "claude-3"},
		{"V1_GPT35", "gpt-3.5"},
		{"NoMatch", "unknown-model"},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _, _, _, _ = ResolveTargetModelWithDynamicWeight(
					tc.model, v1Map, v2Map, selector, 1,
				)
			}
		})
	}
}

// BenchmarkFilterValidTargets benchmarks target filtering
func BenchmarkFilterValidTargets(b *testing.B) {
	selector := NewDynamicModelRedirectSelector(nil)

	testCases := []struct {
		name    string
		targets []models.ModelRedirectTarget
	}{
		{
			"AllEnabled",
			[]models.ModelRedirectTarget{
				{Model: "target-1", Weight: 100},
				{Model: "target-2", Weight: 200},
				{Model: "target-3", Weight: 300},
			},
		},
		{
			"SomeDisabled",
			[]models.ModelRedirectTarget{
				{Model: "target-1", Weight: 100},
				{Model: "target-2", Weight: 200, Enabled: boolPtr(false)},
				{Model: "target-3", Weight: 300},
			},
		},
		{
			"MixedWeights",
			[]models.ModelRedirectTarget{
				{Model: "target-1", Weight: 0},
				{Model: "target-2", Weight: 200},
				{Model: "target-3", Weight: 0},
				{Model: "target-4", Weight: 400},
			},
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = selector.filterValidTargetsWithIndices(tc.targets)
			}
		})
	}
}

// BenchmarkGetModelRedirectDynamicWeights benchmarks dynamic weight info retrieval
func BenchmarkGetModelRedirectDynamicWeights(b *testing.B) {
	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "target-1", Weight: 100},
			{Model: "target-2", Weight: 200},
			{Model: "target-3", Weight: 300},
		},
	}

	b.Run("WithoutDWM", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = GetModelRedirectDynamicWeights(nil, 1, "gpt-4", rule)
		}
	})

	b.Run("WithDWM", func(b *testing.B) {
		memStore := store.NewMemoryStore()
		dwm := NewDynamicWeightManager(memStore)

		groupID := uint(1)
		sourceModel := "gpt-4"

		// Record some metrics to simulate realistic scenario
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, "gpt-4-turbo")
		dwm.RecordModelRedirectSuccess(groupID, sourceModel, "gpt-4-0125")
		dwm.RecordModelRedirectFailure(groupID, sourceModel, "gpt-4-32k", false)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = GetModelRedirectDynamicWeights(dwm, groupID, sourceModel, rule)
		}
	})
}

// BenchmarkConcurrentSelection benchmarks concurrent target selection
func BenchmarkConcurrentSelection(b *testing.B) {
	selector := NewDynamicModelRedirectSelector(nil)

	rule := &models.ModelRedirectRuleV2{
		Targets: []models.ModelRedirectTarget{
			{Model: "target-1", Weight: 100},
			{Model: "target-2", Weight: 200},
			{Model: "target-3", Weight: 300},
			{Model: "target-4", Weight: 200},
			{Model: "target-5", Weight: 200},
		},
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _ = selector.SelectTargetWithContext(rule, 1, "gpt-4")
		}
	})
}

// BenchmarkRealisticWorkload simulates realistic model redirect workload
func BenchmarkRealisticWorkload(b *testing.B) {
	selector := NewDynamicModelRedirectSelector(nil)

	// Realistic redirect configuration
	v1Map := map[string]string{
		"gpt-3.5":  "gpt-3.5-turbo",
		"gpt-3.5t": "gpt-3.5-turbo",
	}

	v2Map := map[string]*models.ModelRedirectRuleV2{
		"gpt-4": {
			Targets: []models.ModelRedirectTarget{
				{Model: "gpt-4-turbo", Weight: 700},
				{Model: "gpt-4-turbo-preview", Weight: 300},
			},
		},
		"claude-3": {
			Targets: []models.ModelRedirectTarget{
				{Model: "claude-3-opus", Weight: 500},
				{Model: "claude-3-sonnet", Weight: 300},
				{Model: "claude-3-haiku", Weight: 200},
			},
		},
		"gemini": {
			Targets: []models.ModelRedirectTarget{
				{Model: "gemini-pro", Weight: 600},
				{Model: "gemini-1.5-pro", Weight: 400},
			},
		},
		"llama": {
			Targets: []models.ModelRedirectTarget{
				{Model: "llama-3-70b", Weight: 400},
				{Model: "llama-3-8b", Weight: 300},
				{Model: "llama-2-70b", Weight: 200},
				{Model: "llama-2-13b", Weight: 100},
			},
		},
	}

	// Realistic model distribution
	modelNames := []string{
		"gpt-4", "gpt-4", "gpt-4", "gpt-4", "gpt-4", // 25% gpt-4
		"gpt-3.5", "gpt-3.5", "gpt-3.5", // 15% gpt-3.5
		"claude-3", "claude-3", "claude-3", // 15% claude-3
		"gemini", "gemini", // 10% gemini
		"llama", "llama", // 10% llama
		"unknown-1", "unknown-2", "unknown-3", // 15% unknown
		"custom-1", "custom-2", // 10% custom
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		model := modelNames[i%len(modelNames)]
		_, _, _, _, _ = ResolveTargetModelWithDynamicWeight(
			model, v1Map, v2Map, selector, 1,
		)
	}
}

// BenchmarkLargeTargetList benchmarks selection with many targets
func BenchmarkLargeTargetList(b *testing.B) {
	selector := NewDynamicModelRedirectSelector(nil)

	sizes := []int{10, 20, 50, 100}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Targets%d", size), func(b *testing.B) {
			rule := generateTargets(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _ = selector.SelectTargetWithContext(rule, 1, "test-model")
			}
		})
	}
}

// BenchmarkWeightDistributions benchmarks different weight distributions
func BenchmarkWeightDistributions(b *testing.B) {
	selector := NewDynamicModelRedirectSelector(nil)

	testCases := []struct {
		name string
		rule *models.ModelRedirectRuleV2
	}{
		{
			"Uniform",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "t1", Weight: 100},
					{Model: "t2", Weight: 100},
					{Model: "t3", Weight: 100},
					{Model: "t4", Weight: 100},
					{Model: "t5", Weight: 100},
				},
			},
		},
		{
			"Skewed_80_20",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "t1", Weight: 800},
					{Model: "t2", Weight: 200},
				},
			},
		},
		{
			"Skewed_90_10",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "t1", Weight: 900},
					{Model: "t2", Weight: 100},
				},
			},
		},
		{
			"Pyramid",
			&models.ModelRedirectRuleV2{
				Targets: []models.ModelRedirectTarget{
					{Model: "t1", Weight: 500},
					{Model: "t2", Weight: 300},
					{Model: "t3", Weight: 150},
					{Model: "t4", Weight: 50},
				},
			},
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _, _ = selector.SelectTargetWithContext(tc.rule, 1, "test-model")
			}
		})
	}
}

// Helper functions

func generateTargets(count int) *models.ModelRedirectRuleV2 {
	targets := make([]models.ModelRedirectTarget, count)
	for i := 0; i < count; i++ {
		targets[i] = models.ModelRedirectTarget{
			Model:  fmt.Sprintf("target-%d", i),
			Weight: 100 + (i * 10), // Varying weights
		}
	}
	return &models.ModelRedirectRuleV2{Targets: targets}
}

func boolPtr(b bool) *bool {
	return &b
}
