// Package services provides business logic services for the application.
package services

import (
	"errors"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
)

// DynamicModelRedirectSelector handles target selection for V2 redirect rules with dynamic weight support.
// It extends the basic ModelRedirectSelector with health-based weight adjustment.
type DynamicModelRedirectSelector struct {
	dynamicWeight *DynamicWeightManager
}

// NewDynamicModelRedirectSelector creates a new selector with dynamic weight support.
// If dynamicWeight is nil, static weights will be used.
func NewDynamicModelRedirectSelector(dynamicWeight *DynamicWeightManager) *DynamicModelRedirectSelector {
	return &DynamicModelRedirectSelector{
		dynamicWeight: dynamicWeight,
	}
}

// SelectTargetWithContext selects a target model from the rule using weighted random selection.
// groupID and sourceModel are used for dynamic weight lookup.
// Returns (targetModel, selectedIndex, error).
func (s *DynamicModelRedirectSelector) SelectTargetWithContext(
	rule *models.ModelRedirectRuleV2,
	groupID uint,
	sourceModel string,
) (string, int, error) {
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

	// Multiple targets: weighted random selection with dynamic weights
	selectedIdx, err := s.doWeightedSelectWithContext(validTargets, validIndices, groupID, sourceModel)
	if err != nil {
		return "", -1, err
	}

	return validTargets[selectedIdx].Model, validIndices[selectedIdx], nil
}

// filterValidTargetsWithIndices returns targets that are enabled and have positive effective weight.
// Also returns the original indices for tracking.
func (s *DynamicModelRedirectSelector) filterValidTargetsWithIndices(targets []models.ModelRedirectTarget) ([]models.ModelRedirectTarget, []int) {
	valid := make([]models.ModelRedirectTarget, 0, len(targets))
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

// doWeightedSelectWithContext performs weighted random selection with dynamic weight adjustment.
func (s *DynamicModelRedirectSelector) doWeightedSelectWithContext(
	targets []models.ModelRedirectTarget,
	_ []int,
	groupID uint,
	sourceModel string,
) (int, error) {
	weights := make([]int, len(targets))

	for i, t := range targets {
		baseWeight := t.GetWeight()

		// Apply dynamic weight if manager is available
		if s.dynamicWeight != nil {
			targetModel := t.Model
			metrics, err := s.dynamicWeight.GetModelRedirectMetrics(groupID, sourceModel, targetModel)
			if err != nil {
				// Log at debug level - falling back to base weight is acceptable behavior
				logrus.WithError(err).WithFields(logrus.Fields{
					"group_id":     groupID,
					"source_model": sourceModel,
					"target_model": targetModel,
				}).Debug("Failed to get model redirect metrics, using base weight")
			}
			effectiveWeight := s.dynamicWeight.GetEffectiveWeight(baseWeight, metrics)
			weights[i] = GetEffectiveWeightForSelection(effectiveWeight)
		} else {
			weights[i] = baseWeight
		}
	}

	idx := utils.WeightedRandomSelect(weights)
	// Validate index bounds to prevent panic from invalid weighted selection result
	if idx < 0 || idx >= len(targets) {
		return -1, errors.New("weighted selection failed")
	}

	return idx, nil
}

// ResolveTargetModelWithDynamicWeight finds the target model from V2 or V1 rules using dynamic weights.
// Returns (targetModel, ruleVersion, targetCount, selectedIndex, error).
// selectedIndex is the index of the selected target in the targets array (0 for V1 rules since they have a single target, -1 for no match).
// When no matching redirect rule is found, returns empty strings with nil error.
// Callers should check if targetModel is empty to determine if a redirect occurred.
func ResolveTargetModelWithDynamicWeight(
	sourceModel string,
	v1Map map[string]string,
	v2Map map[string]*models.ModelRedirectRuleV2,
	selector *DynamicModelRedirectSelector,
	groupID uint,
) (string, string, int, int, error) {
	// Priority: V2 rules first
	if rule, found := v2Map[sourceModel]; found {
		if selector == nil {
			return "", "", 0, -1, errors.New("selector required for V2 rules")
		}
		targetModel, selectedIdx, err := selector.SelectTargetWithContext(rule, groupID, sourceModel)
		if err != nil {
			return "", "", 0, -1, err
		}
		return targetModel, "v2", len(rule.Targets), selectedIdx, nil
	}

	// Fallback to V1 rules
	if targetModel, found := v1Map[sourceModel]; found {
		return targetModel, "v1", 1, 0, nil
	}

	return "", "", 0, -1, nil
}

// GetModelRedirectDynamicWeights returns dynamic weight info for all targets of a redirect rule.
func GetModelRedirectDynamicWeights(
	dwm *DynamicWeightManager,
	groupID uint,
	sourceModel string,
	rule *models.ModelRedirectRuleV2,
) []models.DynamicWeightInfo {
	if rule == nil || len(rule.Targets) == 0 {
		return nil
	}

	result := make([]models.DynamicWeightInfo, len(rule.Targets))
	for i, target := range rule.Targets {
		baseWeight := target.GetWeight()
		if !target.IsEnabled() {
			baseWeight = 0
		}

		var metrics *DynamicWeightMetrics
		var healthScore float64 = 1.0
		var effectiveWeight float64 = float64(baseWeight)

		if dwm != nil {
			targetModel := target.Model
			metrics, _ = dwm.GetModelRedirectMetrics(groupID, sourceModel, targetModel)
			healthScore = dwm.CalculateHealthScore(metrics)
			effectiveWeight = dwm.GetEffectiveWeight(baseWeight, metrics)
		}

		info := models.DynamicWeightInfo{
			BaseWeight:      baseWeight,
			HealthScore:     healthScore,
			EffectiveWeight: effectiveWeight,
		}

		if metrics != nil {
			info.RequestCount = metrics.Requests180d
			if metrics.Requests180d > 0 {
				// Use weighted success rate calculation
				info.SuccessRate = dwm.CalculateWeightedSuccessRate(metrics)
			}
			if !metrics.LastFailureAt.IsZero() {
				ts := metrics.LastFailureAt.Format(time.RFC3339)
				info.LastFailureAt = &ts
			}
			if !metrics.LastSuccessAt.IsZero() {
				ts := metrics.LastSuccessAt.Format(time.RFC3339)
				info.LastSuccessAt = &ts
			}
		}

		result[i] = info
	}

	return result
}
