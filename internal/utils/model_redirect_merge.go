package utils

import (
	"encoding/json"
)

// ModelRedirectTarget represents a single target in model redirect rules
type ModelRedirectTarget struct {
	Model   string `json:"model"`
	Weight  int    `json:"weight,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// ModelRedirectRule represents a model redirect rule with multiple targets
type ModelRedirectRule struct {
	Targets []ModelRedirectTarget `json:"targets"`
}

// MergeModelRedirectRulesV2 merges duplicate model redirect rules by combining targets
// When multiple rules have the same "from" model, their targets are merged into a single rule
// Duplicate targets (same model name) are deduplicated, keeping the first occurrence
// This function is used during import to automatically consolidate duplicate rules
func MergeModelRedirectRulesV2(rulesJSON []byte) ([]byte, error) {
	if len(rulesJSON) == 0 {
		return rulesJSON, nil
	}

	// Parse JSON to map
	var rulesMap map[string]ModelRedirectRule
	if err := json.Unmarshal(rulesJSON, &rulesMap); err != nil {
		return nil, err
	}

	// If empty, return as-is
	if len(rulesMap) == 0 {
		return rulesJSON, nil
	}

	// Merge targets for each "from" model
	// Use map to track seen target models for deduplication
	mergedMap := make(map[string]ModelRedirectRule)

	for from, rule := range rulesMap {
		// Use map for O(1) lookup to deduplicate targets
		seenTargets := make(map[string]bool)
		var mergedTargets []ModelRedirectTarget

		for _, target := range rule.Targets {
			modelName := target.Model
			if modelName == "" {
				continue // Skip empty model names
			}

			// Keep first occurrence of each target model
			if !seenTargets[modelName] {
				seenTargets[modelName] = true
				mergedTargets = append(mergedTargets, target)
			}
		}

		// Only add rule if it has valid targets
		if len(mergedTargets) > 0 {
			mergedMap[from] = ModelRedirectRule{
				Targets: mergedTargets,
			}
		}
	}

	// Convert back to JSON
	return json.Marshal(mergedMap)
}
