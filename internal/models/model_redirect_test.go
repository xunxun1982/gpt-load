package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockWeightedSelect is a mock weighted selection function for testing
func mockWeightedSelect(weights []int) int {
	// Simple mock: always return first index
	if len(weights) > 0 {
		return 0
	}
	return -1
}

// mockWeightedSelectLast returns the last index for testing
func mockWeightedSelectLast(weights []int) int {
	if len(weights) > 0 {
		return len(weights) - 1
	}
	return -1
}

// TestModelRedirectTarget_IsEnabled tests IsEnabled method
func TestModelRedirectTarget_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		target   ModelRedirectTarget
		expected bool
	}{
		{
			name:     "nil enabled (default true)",
			target:   ModelRedirectTarget{Model: "gpt-4", Enabled: nil},
			expected: true,
		},
		{
			name: "explicitly enabled",
			target: ModelRedirectTarget{Model: "gpt-4", Enabled: func() *bool {
				b := true
				return &b
			}()},
			expected: true,
		},
		{
			name: "explicitly disabled",
			target: ModelRedirectTarget{Model: "gpt-4", Enabled: func() *bool {
				b := false
				return &b
			}()},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.target.IsEnabled())
		})
	}
}

// TestModelRedirectTarget_GetWeight tests GetWeight method
func TestModelRedirectTarget_GetWeight(t *testing.T) {
	tests := []struct {
		name     string
		target   ModelRedirectTarget
		expected int
	}{
		{
			name:     "zero weight (default 100)",
			target:   ModelRedirectTarget{Model: "gpt-4", Weight: 0},
			expected: 100,
		},
		{
			name:     "negative weight (default 100)",
			target:   ModelRedirectTarget{Model: "gpt-4", Weight: -10},
			expected: 100,
		},
		{
			name:     "custom weight",
			target:   ModelRedirectTarget{Model: "gpt-4", Weight: 500},
			expected: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.target.GetWeight())
		})
	}
}

// TestNewModelRedirectSelector tests selector creation
func TestNewModelRedirectSelector(t *testing.T) {
	t.Run("valid function", func(t *testing.T) {
		selector := NewModelRedirectSelector(mockWeightedSelect)
		assert.NotNil(t, selector)
	})

	t.Run("nil function panics", func(t *testing.T) {
		assert.Panics(t, func() {
			NewModelRedirectSelector(nil)
		})
	})
}

// TestModelRedirectSelector_SelectTarget tests target selection
func TestModelRedirectSelector_SelectTarget(t *testing.T) {
	selector := NewModelRedirectSelector(mockWeightedSelect)

	tests := []struct {
		name        string
		rule        *ModelRedirectRuleV2
		expectError bool
		errorMsg    string
	}{
		{
			name:        "nil rule",
			rule:        nil,
			expectError: true,
			errorMsg:    "no targets configured",
		},
		{
			name:        "empty targets",
			rule:        &ModelRedirectRuleV2{Targets: []ModelRedirectTarget{}},
			expectError: true,
			errorMsg:    "no targets configured",
		},
		{
			name: "single target",
			rule: &ModelRedirectRuleV2{
				Targets: []ModelRedirectTarget{
					{Model: "gpt-4", Weight: 100},
				},
			},
			expectError: false,
		},
		{
			name: "multiple targets",
			rule: &ModelRedirectRuleV2{
				Targets: []ModelRedirectTarget{
					{Model: "gpt-4", Weight: 100},
					{Model: "gpt-3.5-turbo", Weight: 200},
				},
			},
			expectError: false,
		},
		{
			name: "all targets disabled",
			rule: &ModelRedirectRuleV2{
				Targets: []ModelRedirectTarget{
					{Model: "gpt-4", Weight: 100, Enabled: func() *bool { b := false; return &b }()},
				},
			},
			expectError: true,
			errorMsg:    "no enabled targets available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := selector.SelectTarget(tt.rule)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, target)
			}
		})
	}
}

// TestCollectSourceModels tests source model collection
func TestCollectSourceModels(t *testing.T) {
	tests := []struct {
		name     string
		v1Map    map[string]string
		v2Map    map[string]*ModelRedirectRuleV2
		expected []string
	}{
		{
			name:     "empty maps",
			v1Map:    nil,
			v2Map:    nil,
			expected: nil,
		},
		{
			name: "v1 only",
			v1Map: map[string]string{
				"gpt-4": "gpt-4-turbo",
			},
			v2Map:    nil,
			expected: []string{"gpt-4"},
		},
		{
			name:  "v2 only",
			v1Map: nil,
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {
					Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}},
				},
			},
			expected: []string{"gpt-4"},
		},
		{
			name: "v2 takes priority",
			v1Map: map[string]string{
				"gpt-3.5": "gpt-3.5-turbo",
			},
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {
					Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}},
				},
			},
			expected: []string{"gpt-4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CollectSourceModels(tt.v1Map, tt.v2Map)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.ElementsMatch(t, tt.expected, result)
			}
		})
	}
}

// TestResolveTargetModel tests target model resolution
func TestResolveTargetModel(t *testing.T) {
	selector := NewModelRedirectSelector(mockWeightedSelect)

	tests := []struct {
		name            string
		sourceModel     string
		v1Map           map[string]string
		v2Map           map[string]*ModelRedirectRuleV2
		selector        *ModelRedirectSelector
		expectedTarget  string
		expectedVersion string
		expectedCount   int
		expectError     bool
	}{
		{
			name:            "not found",
			sourceModel:     "unknown",
			v1Map:           nil,
			v2Map:           nil,
			selector:        selector,
			expectedTarget:  "",
			expectedVersion: "",
			expectedCount:   0,
			expectError:     false,
		},
		{
			name:        "v1 found",
			sourceModel: "gpt-4",
			v1Map: map[string]string{
				"gpt-4": "gpt-4-turbo",
			},
			v2Map:           nil,
			selector:        selector,
			expectedTarget:  "gpt-4-turbo",
			expectedVersion: "v1",
			expectedCount:   1,
			expectError:     false,
		},
		{
			name:        "v2 found",
			sourceModel: "gpt-4",
			v1Map:       nil,
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {
					Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}},
				},
			},
			selector:        selector,
			expectedTarget:  "gpt-4-turbo",
			expectedVersion: "v2",
			expectedCount:   1,
			expectError:     false,
		},
		{
			name:        "v2 without selector",
			sourceModel: "gpt-4",
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {
					Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}},
				},
			},
			selector:    nil,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, version, count, err := ResolveTargetModel(tt.sourceModel, tt.v1Map, tt.v2Map, tt.selector)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedTarget, target)
				assert.Equal(t, tt.expectedVersion, version)
				assert.Equal(t, tt.expectedCount, count)
			}
		})
	}
}

// TestMigrateV1ToV2Rules tests V1 to V2 migration
func TestMigrateV1ToV2Rules(t *testing.T) {
	tests := []struct {
		name  string
		v1Map map[string]string
	}{
		{
			name:  "empty map",
			v1Map: nil,
		},
		{
			name: "single rule",
			v1Map: map[string]string{
				"gpt-4": "gpt-4-turbo",
			},
		},
		{
			name: "multiple rules",
			v1Map: map[string]string{
				"gpt-4":        "gpt-4-turbo",
				"gpt-3.5":      "gpt-3.5-turbo",
				"claude-3":     "claude-3-opus",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v2Map := MigrateV1ToV2Rules(tt.v1Map)
			if tt.v1Map == nil {
				assert.Nil(t, v2Map)
			} else {
				assert.Len(t, v2Map, len(tt.v1Map))
				for source, target := range tt.v1Map {
					rule, exists := v2Map[source]
					require.True(t, exists)
					require.Len(t, rule.Targets, 1)
					assert.Equal(t, target, rule.Targets[0].Model)
					assert.Equal(t, 100, rule.Targets[0].Weight)
				}
			}
		})
	}
}

// TestMergeV1IntoV2Rules tests merging V1 into V2 rules
func TestMergeV1IntoV2Rules(t *testing.T) {
	tests := []struct {
		name         string
		v1Map        map[string]string
		v2Map        map[string]*ModelRedirectRuleV2
		expectedKeys []string
	}{
		{
			name:         "empty v1",
			v1Map:        nil,
			v2Map:        nil,
			expectedKeys: nil,
		},
		{
			name: "v1 only",
			v1Map: map[string]string{
				"gpt-4": "gpt-4-turbo",
			},
			v2Map:        nil,
			expectedKeys: []string{"gpt-4"},
		},
		{
			name:  "v2 only",
			v1Map: nil,
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}}},
			},
			expectedKeys: []string{"gpt-4"},
		},
		{
			name: "merge without conflict",
			v1Map: map[string]string{
				"gpt-3.5": "gpt-3.5-turbo",
			},
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}}},
			},
			expectedKeys: []string{"gpt-4", "gpt-3.5"},
		},
		{
			name: "v2 takes priority on conflict",
			v1Map: map[string]string{
				"gpt-4": "gpt-4-v1",
			},
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {Targets: []ModelRedirectTarget{{Model: "gpt-4-v2"}}},
			},
			expectedKeys: []string{"gpt-4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeV1IntoV2Rules(tt.v1Map, tt.v2Map)
			if tt.expectedKeys == nil {
				assert.Nil(t, result)
			} else {
				keys := make([]string, 0, len(result))
				for k := range result {
					keys = append(keys, k)
				}
				assert.ElementsMatch(t, tt.expectedKeys, keys)
			}
		})
	}
}

// TestResolveTargetModelWithIndex tests target resolution with index
func TestResolveTargetModelWithIndex(t *testing.T) {
	selector := NewModelRedirectSelector(mockWeightedSelectLast)

	tests := []struct {
		name          string
		sourceModel   string
		v1Map         map[string]string
		v2Map         map[string]*ModelRedirectRuleV2
		expectedIndex int
	}{
		{
			name:        "v1 rule",
			sourceModel: "gpt-4",
			v1Map: map[string]string{
				"gpt-4": "gpt-4-turbo",
			},
			expectedIndex: -1, // V1 has no index concept
		},
		{
			name:        "v2 single target",
			sourceModel: "gpt-4",
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {
					Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}},
				},
			},
			expectedIndex: 0,
		},
		{
			name:        "v2 multiple targets",
			sourceModel: "gpt-4",
			v2Map: map[string]*ModelRedirectRuleV2{
				"gpt-4": {
					Targets: []ModelRedirectTarget{
						{Model: "gpt-4-turbo"},
						{Model: "gpt-4-32k"},
					},
				},
			},
			expectedIndex: 1, // mockWeightedSelectLast returns last index
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, idx, err := ResolveTargetModelWithIndex(tt.sourceModel, tt.v1Map, tt.v2Map, selector)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedIndex, idx)
		})
	}
}

// BenchmarkSelectTarget benchmarks target selection
func BenchmarkSelectTarget(b *testing.B) {
	selector := NewModelRedirectSelector(mockWeightedSelect)
	rule := &ModelRedirectRuleV2{
		Targets: []ModelRedirectTarget{
			{Model: "gpt-4", Weight: 100},
			{Model: "gpt-3.5-turbo", Weight: 200},
			{Model: "claude-3", Weight: 150},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = selector.SelectTarget(rule)
	}
}

// BenchmarkCollectSourceModels benchmarks source model collection
func BenchmarkCollectSourceModels(b *testing.B) {
	v2Map := map[string]*ModelRedirectRuleV2{
		"gpt-4":        {Targets: []ModelRedirectTarget{{Model: "gpt-4-turbo"}}},
		"gpt-3.5":      {Targets: []ModelRedirectTarget{{Model: "gpt-3.5-turbo"}}},
		"claude-3":     {Targets: []ModelRedirectTarget{{Model: "claude-3-opus"}}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CollectSourceModels(nil, v2Map)
	}
}

// BenchmarkResolveTargetModel benchmarks target resolution
func BenchmarkResolveTargetModel(b *testing.B) {
	selector := NewModelRedirectSelector(mockWeightedSelect)
	v2Map := map[string]*ModelRedirectRuleV2{
		"gpt-4": {
			Targets: []ModelRedirectTarget{
				{Model: "gpt-4-turbo", Weight: 100},
				{Model: "gpt-4-32k", Weight: 200},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = ResolveTargetModel("gpt-4", nil, v2Map, selector)
	}
}
