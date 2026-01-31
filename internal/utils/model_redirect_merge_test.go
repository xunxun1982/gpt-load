package utils

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeModelRedirectRulesV2(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    string
		expectError bool
	}{
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "empty JSON object",
			input:    "{}",
			expected: "{}",
		},
		{
			name: "single rule no merge needed",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				}
			}`,
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				}
			}`,
		},
		{
			name: "deduplicate targets within same rule",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100},
						{"model": "gpt-4-turbo", "weight": 50},
						{"model": "gpt-4-0125", "weight": 100}
					]
				}
			}`,
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100},
						{"model": "gpt-4-0125", "weight": 100}
					]
				}
			}`,
		},
		{
			name: "multiple rules with different from models",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				},
				"gpt-3.5": {
					"targets": [
						{"model": "gpt-3.5-turbo", "weight": 100}
					]
				}
			}`,
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				},
				"gpt-3.5": {
					"targets": [
						{"model": "gpt-3.5-turbo", "weight": 100}
					]
				}
			}`,
		},
		{
			name: "targets with enabled field",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100, "enabled": true},
						{"model": "gpt-4-turbo", "weight": 50, "enabled": false},
						{"model": "gpt-4-0125", "weight": 100}
					]
				}
			}`,
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100, "enabled": true},
						{"model": "gpt-4-0125", "weight": 100}
					]
				}
			}`,
		},
		{
			name: "skip empty model names",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "", "weight": 100},
						{"model": "gpt-4-turbo", "weight": 100},
						{"model": "", "weight": 50}
					]
				}
			}`,
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				}
			}`,
		},
		{
			name: "rule with all empty targets removed",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "", "weight": 100},
						{"model": "", "weight": 50}
					]
				},
				"gpt-3.5": {
					"targets": [
						{"model": "gpt-3.5-turbo", "weight": 100}
					]
				}
			}`,
			expected: `{
				"gpt-3.5": {
					"targets": [
						{"model": "gpt-3.5-turbo", "weight": 100}
					]
				}
			}`,
		},
		{
			name:        "invalid JSON",
			input:       `{"invalid": }`,
			expectError: true,
		},
		{
			name: "trim whitespace from keys and target models",
			input: `{
				" gpt-4 ": {
					"targets": [
						{"model": " gpt-4-turbo ", "weight": 100},
						{"model": "gpt-4-0125", "weight": 50}
					]
				},
				"gpt-3.5": {
					"targets": [
						{"model": "  gpt-3.5-turbo  ", "weight": 100}
					]
				}
			}`,
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100},
						{"model": "gpt-4-0125", "weight": 50}
					]
				},
				"gpt-3.5": {
					"targets": [
						{"model": "gpt-3.5-turbo", "weight": 100}
					]
				}
			}`,
		},
		{
			name: "merge duplicate keys after trimming whitespace",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				},
				" gpt-4 ": {
					"targets": [
						{"model": "gpt-4-0125", "weight": 50}
					]
				}
			}`,
			// Note: Order of targets may vary due to map iteration order
			// We verify the content in a separate assertion below
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100},
						{"model": "gpt-4-0125", "weight": 50}
					]
				}
			}`,
		},
		{
			name: "skip empty keys after trimming",
			input: `{
				"  ": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				},
				"gpt-3.5": {
					"targets": [
						{"model": "gpt-3.5-turbo", "weight": 100}
					]
				}
			}`,
			expected: `{
				"gpt-3.5": {
					"targets": [
						{"model": "gpt-3.5-turbo", "weight": 100}
					]
				}
			}`,
		},
		{
			name: "deterministic merge with multiple duplicate keys",
			input: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-turbo", "weight": 100}
					]
				},
				" gpt-4": {
					"targets": [
						{"model": "gpt-4-0125", "weight": 50}
					]
				},
				"gpt-4 ": {
					"targets": [
						{"model": "gpt-4-vision", "weight": 30}
					]
				}
			}`,
			// With sorted keys, " gpt-4" comes first, then "gpt-4", then "gpt-4 "
			// All normalize to "gpt-4", so targets are merged in that order
			expected: `{
				"gpt-4": {
					"targets": [
						{"model": "gpt-4-0125", "weight": 50},
						{"model": "gpt-4-turbo", "weight": 100},
						{"model": "gpt-4-vision", "weight": 30}
					]
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MergeModelRedirectRulesV2([]byte(tt.input))

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// For empty input, expect empty output
			if tt.input == "" {
				assert.Empty(t, result)
				return
			}

			// Parse result to verify structure
			var resultObj map[string]interface{}
			err = json.Unmarshal(result, &resultObj)
			require.NoError(t, err, "result JSON should be valid")

			// For the "merge duplicate keys" test, verify content without order dependency
			if tt.name == "merge duplicate keys after trimming whitespace" {
				// Verify we have exactly one key "gpt-4"
				assert.Len(t, resultObj, 1)
				assert.Contains(t, resultObj, "gpt-4")

				// Verify targets contain both models
				gpt4Rule := resultObj["gpt-4"].(map[string]interface{})
				targets := gpt4Rule["targets"].([]interface{})
				assert.Len(t, targets, 2)

				// Extract model names from targets
				modelNames := make(map[string]bool)
				for _, tgt := range targets {
					target := tgt.(map[string]interface{})
					modelNames[target["model"].(string)] = true
				}
				assert.True(t, modelNames["gpt-4-turbo"], "should contain gpt-4-turbo")
				assert.True(t, modelNames["gpt-4-0125"], "should contain gpt-4-0125")
				return
			}

			// For other tests, compare as objects (ignore formatting)
			var expectedObj map[string]interface{}
			err = json.Unmarshal([]byte(tt.expected), &expectedObj)
			require.NoError(t, err, "expected JSON should be valid")

			assert.Equal(t, expectedObj, resultObj)
		})
	}
}

func TestMergeModelRedirectRulesV2_Performance(t *testing.T) {
	// Test with large number of rules to ensure O(n) performance
	rulesMap := make(map[string]ModelRedirectRule)

	// Create 1000 rules with 10 targets each
	for i := 0; i < 1000; i++ {
		from := fmt.Sprintf("model-%d", i)
		targets := make([]ModelRedirectTarget, 10)
		for j := 0; j < 10; j++ {
			targets[j] = ModelRedirectTarget{
				Model:  fmt.Sprintf("target-%d-%d", i, j),
				Weight: 100,
			}
		}
		rulesMap[from] = ModelRedirectRule{Targets: targets}
	}

	inputJSON, err := json.Marshal(rulesMap)
	require.NoError(t, err)

	// Run merge operation
	result, err := MergeModelRedirectRulesV2(inputJSON)
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	// Verify result is valid JSON
	var resultMap map[string]ModelRedirectRule
	err = json.Unmarshal(result, &resultMap)
	require.NoError(t, err)
	assert.Len(t, resultMap, 1000)
}

func BenchmarkMergeModelRedirectRulesV2(b *testing.B) {
	// Benchmark with realistic data size
	rulesMap := make(map[string]ModelRedirectRule)
	for i := 0; i < 100; i++ {
		from := fmt.Sprintf("model-%d", i)
		targets := make([]ModelRedirectTarget, 5)
		for j := 0; j < 5; j++ {
			targets[j] = ModelRedirectTarget{
				Model:  fmt.Sprintf("target-%d-%d", i, j),
				Weight: 100,
			}
		}
		rulesMap[from] = ModelRedirectRule{Targets: targets}
	}

	inputJSON, _ := json.Marshal(rulesMap)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MergeModelRedirectRulesV2(inputJSON)
	}
}

// TestMergeModelRedirectRulesV2_Deterministic verifies that the merge operation
// produces consistent results across multiple runs, even with duplicate keys
func TestMergeModelRedirectRulesV2_Deterministic(t *testing.T) {
	// Input with keys that normalize to the same value
	input := `{
		"gpt-4": {
			"targets": [
				{"model": "gpt-4-turbo", "weight": 100}
			]
		},
		" gpt-4": {
			"targets": [
				{"model": "gpt-4-0125", "weight": 50}
			]
		},
		"gpt-4 ": {
			"targets": [
				{"model": "gpt-4-vision", "weight": 30}
			]
		}
	}`

	// Run merge operation multiple times
	var firstResult []byte
	for i := 0; i < 100; i++ {
		result, err := MergeModelRedirectRulesV2([]byte(input))
		require.NoError(t, err)

		if i == 0 {
			firstResult = result
		} else {
			// Verify result is identical to first run
			assert.Equal(t, string(firstResult), string(result),
				"Result should be deterministic across runs (iteration %d)", i)
		}
	}

	// Verify the result structure
	var resultMap map[string]ModelRedirectRule
	err := json.Unmarshal(firstResult, &resultMap)
	require.NoError(t, err)

	// Should have exactly one key "gpt-4"
	assert.Len(t, resultMap, 1)
	assert.Contains(t, resultMap, "gpt-4")

	// Should have all three targets in deterministic order
	rule := resultMap["gpt-4"]
	assert.Len(t, rule.Targets, 3)

	// Verify order is consistent (based on sorted key order: " gpt-4", "gpt-4", "gpt-4 ")
	assert.Equal(t, "gpt-4-0125", rule.Targets[0].Model)
	assert.Equal(t, "gpt-4-turbo", rule.Targets[1].Model)
	assert.Equal(t, "gpt-4-vision", rule.Targets[2].Model)
}
