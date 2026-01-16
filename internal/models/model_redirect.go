package models

import (
	"errors"
)

// ModelRedirectTarget represents a single redirect target with weight configuration.
// Used in V2 model redirect rules for one-to-many mapping support.
type ModelRedirectTarget struct {
	Model   string `json:"model"`             // Target model name
	Weight  int    `json:"weight,omitempty"`  // Weight for selection (1-1000), default 100
	Enabled *bool  `json:"enabled,omitempty"` // Whether this target is enabled, default true
}

// ModelRedirectRuleV2 represents an enhanced model redirect rule supporting
// one-to-many mapping with weighted random selection.
type ModelRedirectRuleV2 struct {
	Targets  []ModelRedirectTarget `json:"targets"`            // List of target models
	Fallback []string              `json:"fallback,omitempty"` // Fallback models (P1 extension)
}

// IsEnabled returns whether the target is enabled.
// Returns true if Enabled is nil (default) or explicitly set to true.
func (t *ModelRedirectTarget) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

// GetWeight returns the effective weight for the target.
// Returns 100 as default if weight is 0 or not set.
func (t *ModelRedirectTarget) GetWeight() int {
	if t.Weight <= 0 {
		return 100
	}
	return t.Weight
}

// WeightedSelectFunc is a function type for weighted random selection.
// This allows dependency injection to avoid import cycles.
type WeightedSelectFunc func(weights []int) int

// ModelRedirectSelector handles target selection for V2 redirect rules.
// Uses weighted random selection algorithm via injected function.
type ModelRedirectSelector struct {
	weightedSelect WeightedSelectFunc
}

// NewModelRedirectSelector creates a new selector instance with the given weighted selection function.
// Panics if weightedSelectFn is nil to fail fast during initialization.
func NewModelRedirectSelector(weightedSelectFn WeightedSelectFunc) *ModelRedirectSelector {
	if weightedSelectFn == nil {
		panic("weightedSelectFn must not be nil")
	}
	return &ModelRedirectSelector{
		weightedSelect: weightedSelectFn,
	}
}

// SelectTarget selects a target model from the rule using weighted random selection.
// For single target (one-to-one mapping), returns directly without weight calculation.
// For multiple targets (one-to-many mapping), uses weighted random selection.
func (s *ModelRedirectSelector) SelectTarget(rule *ModelRedirectRuleV2) (string, error) {
	if rule == nil || len(rule.Targets) == 0 {
		return "", errors.New("no targets configured")
	}

	// Filter valid targets (enabled and positive weight)
	validTargets := s.filterValidTargets(rule.Targets)
	if len(validTargets) == 0 {
		return "", errors.New("no enabled targets available")
	}

	// Fast path: single target, skip weight calculation
	if len(validTargets) == 1 {
		return validTargets[0].Model, nil
	}

	// Multiple targets: weighted random selection
	return s.doWeightedSelect(validTargets)
}

// filterValidTargets returns targets that are enabled and have positive effective weight.
func (s *ModelRedirectSelector) filterValidTargets(targets []ModelRedirectTarget) []ModelRedirectTarget {
	valid := make([]ModelRedirectTarget, 0, len(targets))
	for _, t := range targets {
		if !t.IsEnabled() {
			continue
		}
		// GetWeight returns default 100 for unset/zero weight
		if t.GetWeight() > 0 {
			valid = append(valid, t)
		}
	}
	return valid
}

// doWeightedSelect performs weighted random selection on valid targets.
func (s *ModelRedirectSelector) doWeightedSelect(targets []ModelRedirectTarget) (string, error) {
	weights := make([]int, len(targets))
	for i, t := range targets {
		weights[i] = t.GetWeight()
	}

	idx := s.weightedSelect(weights)
	// Validate index bounds to prevent panic from invalid weighted selection result
	if idx < 0 || idx >= len(targets) {
		return "", errors.New("weighted selection failed")
	}

	return targets[idx].Model, nil
}

// CollectSourceModels collects all unique source model names from redirect rules.
// If V2 rules exist, only V2 source models are returned (V1 is ignored for backward compatibility).
// If no V2 rules exist, V1 source models are returned as fallback.
// Returns a slice of source model names.
func CollectSourceModels(v1Map map[string]string, v2Map map[string]*ModelRedirectRuleV2) []string {
	// V2 rules take full priority - if V2 exists, ignore V1 completely
	if len(v2Map) > 0 {
		result := make([]string, 0, len(v2Map))
		for sourceModel := range v2Map {
			result = append(result, sourceModel)
		}
		return result
	}

	// Fallback to V1 rules only when no V2 rules exist
	if len(v1Map) > 0 {
		result := make([]string, 0, len(v1Map))
		for sourceModel := range v1Map {
			result = append(result, sourceModel)
		}
		return result
	}

	return nil
}

// ResolveTargetModel finds the target model from V2 or V1 rules using the provided selector.
// Returns (targetModel, ruleVersion, targetCount, error).
// ruleVersion is "v2", "v1", or "" if not found.
// Note: selector must not be nil when V2 rules exist, otherwise returns error.
func ResolveTargetModel(sourceModel string, v1Map map[string]string, v2Map map[string]*ModelRedirectRuleV2, selector *ModelRedirectSelector) (string, string, int, error) {
	// Priority: V2 rules first
	if rule, found := v2Map[sourceModel]; found {
		if selector == nil {
			return "", "", 0, errors.New("selector required for V2 rules")
		}
		targetModel, err := selector.SelectTarget(rule)
		if err != nil {
			return "", "", 0, err
		}
		return targetModel, "v2", len(rule.Targets), nil
	}

	// Fallback to V1 rules
	if targetModel, found := v1Map[sourceModel]; found {
		return targetModel, "v1", 1, nil
	}

	return "", "", 0, nil
}

// MigrateV1ToV2Rules converts V1 redirect rules to V2 format.
// Each V1 rule (source -> target) becomes a V2 rule with single target and default weight.
// Returns nil if v1Map is empty.
func MigrateV1ToV2Rules(v1Map map[string]string) map[string]*ModelRedirectRuleV2 {
	if len(v1Map) == 0 {
		return nil
	}

	v2Map := make(map[string]*ModelRedirectRuleV2, len(v1Map))
	for sourceModel, targetModel := range v1Map {
		v2Map[sourceModel] = &ModelRedirectRuleV2{
			Targets: []ModelRedirectTarget{
				{Model: targetModel, Weight: 100},
			},
		}
	}
	return v2Map
}

// MergeV1IntoV2Rules merges V1 rules into existing V2 rules.
// V2 rules take priority - V1 rules are only added if source model doesn't exist in V2.
// Returns the merged V2 rules map.
func MergeV1IntoV2Rules(v1Map map[string]string, v2Map map[string]*ModelRedirectRuleV2) map[string]*ModelRedirectRuleV2 {
	if len(v1Map) == 0 {
		return v2Map
	}

	// Create new map if v2Map is nil
	if v2Map == nil {
		return MigrateV1ToV2Rules(v1Map)
	}

	// Copy existing V2 rules
	result := make(map[string]*ModelRedirectRuleV2, len(v2Map)+len(v1Map))
	for k, v := range v2Map {
		result[k] = v
	}

	// Add V1 rules that don't exist in V2
	for sourceModel, targetModel := range v1Map {
		if _, exists := result[sourceModel]; !exists {
			result[sourceModel] = &ModelRedirectRuleV2{
				Targets: []ModelRedirectTarget{
					{Model: targetModel, Weight: 100},
				},
			}
		}
	}

	return result
}

// ResolveTargetModelWithIndex finds the target model from V2 or V1 rules using the provided selector.
// Returns (targetModel, ruleVersion, targetCount, selectedIndex, error).
// selectedIndex is the index of the selected target in the original targets array.
// For V1 rules, selectedIndex is -1 since V1 has no target array concept.
// For not found, selectedIndex is -1.
// ruleVersion is "v2", "v1", or "" if not found.
// Note: selector must not be nil when V2 rules exist, otherwise returns error.
func ResolveTargetModelWithIndex(sourceModel string, v1Map map[string]string, v2Map map[string]*ModelRedirectRuleV2, selector *ModelRedirectSelector) (string, string, int, int, error) {
	// Priority: V2 rules first
	if rule, found := v2Map[sourceModel]; found {
		if selector == nil {
			return "", "", 0, -1, errors.New("selector required for V2 rules")
		}
		targetModel, selectedIdx, err := selector.SelectTargetWithIndex(rule)
		if err != nil {
			return "", "", 0, -1, err
		}
		return targetModel, "v2", len(rule.Targets), selectedIdx, nil
	}

	// Fallback to V1 rules (selectedIndex is -1 since V1 has no target array)
	if targetModel, found := v1Map[sourceModel]; found {
		return targetModel, "v1", 1, -1, nil
	}

	return "", "", 0, -1, nil
}

// SelectTargetWithIndex selects a target model from the rule using weighted random selection.
// Returns (targetModel, selectedIndex, error).
// selectedIndex is the index of the selected target in the original targets array.
func (s *ModelRedirectSelector) SelectTargetWithIndex(rule *ModelRedirectRuleV2) (string, int, error) {
	if rule == nil || len(rule.Targets) == 0 {
		return "", -1, errors.New("no targets configured")
	}

	// Filter valid targets (enabled and positive weight)
	validTargets, validIndices := s.filterValidTargetsWithIndices(rule.Targets)
	if len(validTargets) == 0 {
		return "", -1, errors.New("no enabled targets available")
	}

	// Fast path: single target, skip weight calculation
	if len(validTargets) == 1 {
		return validTargets[0].Model, validIndices[0], nil
	}

	// Multiple targets: weighted random selection
	return s.doWeightedSelectWithIndex(validTargets, validIndices)
}

// filterValidTargetsWithIndices returns targets that are enabled and have positive effective weight.
// Also returns the original indices for tracking.
func (s *ModelRedirectSelector) filterValidTargetsWithIndices(targets []ModelRedirectTarget) ([]ModelRedirectTarget, []int) {
	valid := make([]ModelRedirectTarget, 0, len(targets))
	indices := make([]int, 0, len(targets))
	for i, t := range targets {
		if !t.IsEnabled() {
			continue
		}
		// GetWeight returns default 100 for unset/zero weight
		if t.GetWeight() > 0 {
			valid = append(valid, t)
			indices = append(indices, i)
		}
	}
	return valid, indices
}

// doWeightedSelectWithIndex performs weighted random selection on valid targets.
// Returns (targetModel, indexInOriginalTargets, error).
// The returned index is from originalIndices, mapping back to the original targets array.
func (s *ModelRedirectSelector) doWeightedSelectWithIndex(targets []ModelRedirectTarget, originalIndices []int) (string, int, error) {
	weights := make([]int, len(targets))
	for i, t := range targets {
		weights[i] = t.GetWeight()
	}

	idx := s.weightedSelect(weights)
	// Validate index bounds to prevent panic from invalid weighted selection result
	if idx < 0 || idx >= len(targets) {
		return "", -1, errors.New("weighted selection failed")
	}

	return targets[idx].Model, originalIndices[idx], nil
}
