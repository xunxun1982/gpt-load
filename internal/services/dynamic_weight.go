// Package services provides business logic services for the application.
package services

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sync"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
)

// DynamicWeightMetrics holds the metrics used for dynamic weight calculation.
// These metrics are stored in the key-value store and used to calculate health scores.
// Time-windowed statistics enable weighted health calculation favoring recent data.
type DynamicWeightMetrics struct {
	ConsecutiveFailures int64     `json:"consecutive_failures"`
	LastFailureAt       time.Time `json:"last_failure_at"`
	LastSuccessAt       time.Time `json:"last_success_at"`

	// Time-windowed statistics (cumulative)
	Requests7d    int64 `json:"requests_7d"`
	Successes7d   int64 `json:"successes_7d"`
	Requests14d   int64 `json:"requests_14d"`
	Successes14d  int64 `json:"successes_14d"`
	Requests30d   int64 `json:"requests_30d"`
	Successes30d  int64 `json:"successes_30d"`
	Requests90d   int64 `json:"requests_90d"`
	Successes90d  int64 `json:"successes_90d"`
	Requests180d  int64 `json:"requests_180d"`
	Successes180d int64 `json:"successes_180d"`

	LastRolloverAt time.Time `json:"last_rollover_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DynamicWeightConfig holds configuration for dynamic weight calculation.
type DynamicWeightConfig struct {
	ConsecutiveFailurePenalty    float64
	MaxConsecutiveFailurePenalty float64
	RecentFailurePenalty         float64
	RecentFailureCooldown        time.Duration
	LowSuccessRatePenalty        float64
	LowSuccessRateThreshold      float64
	MinRequestsForSuccessRate    int64
	MinHealthScore               float64
	MetricsTTL                   time.Duration
	TimeWindowConfigs            []models.TimeWindowConfig

	// Health score threshold below which effective weight is reduced to 10% of base weight
	// (capped at 1, min 0.1) to prevent unhealthy high-weight targets from dominating
	// healthy low-weight targets
	CriticalHealthThreshold float64
	// Health score threshold for applying aggressive penalty
	// Targets with health score between CriticalHealthThreshold and MediumHealthThreshold
	// will have their weight reduced more aggressively using quadratic penalty
	MediumHealthThreshold float64
	// Penalty multiplier for medium health scores (0.3-0.7 range)
	// Applied as: effectiveWeight = baseWeight * (healthScore ^ MediumHealthPenaltyExponent)
	MediumHealthPenaltyExponent float64
}

// DefaultDynamicWeightConfig returns the default configuration.
// Optimized for unstable channels that may experience intermittent failures.
// Tolerates patterns like "5-6 consecutive failures followed by 1 success" while still
// penalizing persistently poor performance. Balances between availability and quality.
func DefaultDynamicWeightConfig() *DynamicWeightConfig {
	return &DynamicWeightConfig{
		// Consecutive failure penalty: 0.08 per failure
		// Tolerates up to 5-6 failures before significant penalty (5 × 0.08 = 0.40 max)
		// More forgiving than strict circuit breaker patterns for unstable channels
		ConsecutiveFailurePenalty: 0.08,
		// Max penalty at 0.40 (reached after 5 consecutive failures)
		// Allows unstable channels to still receive some traffic for recovery
		MaxConsecutiveFailurePenalty: 0.40,
		// Recent failure penalty: 0.12 with time decay
		// Moderate penalty that decays over cooldown period
		RecentFailurePenalty: 0.12,
		// 8-minute cooldown for recent failure penalty
		// Longer cooldown gives unstable channels more time to stabilize
		RecentFailureCooldown: 8 * time.Minute,
		// Low success rate penalty: 0.18
		// Applied when success rate falls below threshold
		LowSuccessRatePenalty: 0.18,
		// 40% success rate threshold
		// Tolerates intermittent failures (e.g., 5-6 failures + 1 success ≈ 14-17% raw rate,
		// but with other successful requests mixed in, actual rate may be 30-50%)
		LowSuccessRateThreshold: 40.0,
		// Minimum 5 requests before evaluating success rate
		// Prevents premature penalties on low sample sizes
		MinRequestsForSuccessRate: 5,
		// Minimum health score of 0.1
		MinHealthScore: 0.1,
		// Metrics TTL of 180 days
		MetricsTTL: 180 * 24 * time.Hour,
		// Time window configs for weighted success rate calculation
		TimeWindowConfigs: models.DefaultTimeWindowConfigs(),
		// Critical health threshold: 0.50
		// Below this, effective weight is capped at 1 (min 0.1) to prevent unhealthy
		// high-weight targets from dominating healthy low-weight targets
		CriticalHealthThreshold: 0.50,
		// Medium health threshold: 0.75
		// Between 0.50 and 0.75, apply quadratic penalty for traffic reduction
		// Balances between availability and quality for moderately unhealthy targets
		MediumHealthThreshold: 0.75,
		// Quadratic penalty exponent for medium health range
		// effectiveWeight = baseWeight * (healthScore ^ 2.0)
		// Example: health=0.5 -> weight multiplier=0.25
		MediumHealthPenaltyExponent: 2.0,
	}
}

// DirtyKeyCallback is called when a metrics key is modified and needs persistence.
type DirtyKeyCallback func(key string)

// DynamicWeightManager manages dynamic weight calculation for sub-groups and model redirects.
type DynamicWeightManager struct {
	store         store.Store
	config        *DynamicWeightConfig
	mu            sync.RWMutex
	dirtyCallback DirtyKeyCallback
}

// NewDynamicWeightManager creates a new dynamic weight manager with default configuration.
func NewDynamicWeightManager(s store.Store) *DynamicWeightManager {
	return NewDynamicWeightManagerWithConfig(s, nil)
}

// NewDynamicWeightManagerWithConfig creates a new dynamic weight manager with custom configuration.
func NewDynamicWeightManagerWithConfig(s store.Store, config *DynamicWeightConfig) *DynamicWeightManager {
	if config == nil {
		config = DefaultDynamicWeightConfig()
	}
	return &DynamicWeightManager{
		store:  s,
		config: config,
	}
}

// SetDirtyCallback sets the callback function for dirty key notifications.
// This should be called after persistence service is initialized.
func (m *DynamicWeightManager) SetDirtyCallback(callback DirtyKeyCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirtyCallback = callback
}

// GetConfig returns the current configuration.
func (m *DynamicWeightManager) GetConfig() *DynamicWeightConfig {
	return m.config
}

// SubGroupMetricsKey returns the store key for sub-group metrics.
func SubGroupMetricsKey(aggregateGroupID, subGroupID uint) string {
	return fmt.Sprintf("dw:sg:%d:%d", aggregateGroupID, subGroupID)
}

// ModelRedirectMetricsKey returns the store key for model redirect metrics.
// Uses target model name instead of index to prevent health score misalignment
// when targets are deleted from the middle of the array.
func ModelRedirectMetricsKey(groupID uint, sourceModel string, targetModel string) string {
	encodedSource := url.PathEscape(sourceModel)
	encodedTarget := url.PathEscape(targetModel)
	return fmt.Sprintf("dw:mr:%d:%s:%s", groupID, encodedSource, encodedTarget)
}

// GetSubGroupMetrics retrieves metrics for a sub-group.
func (m *DynamicWeightManager) GetSubGroupMetrics(aggregateGroupID, subGroupID uint) (*DynamicWeightMetrics, error) {
	key := SubGroupMetricsKey(aggregateGroupID, subGroupID)
	return m.getMetrics(key)
}

// GetModelRedirectMetrics retrieves metrics for a model redirect target.
func (m *DynamicWeightManager) GetModelRedirectMetrics(groupID uint, sourceModel string, targetModel string) (*DynamicWeightMetrics, error) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetModel)
	return m.getMetrics(key)
}

// getMetrics retrieves metrics from the store.
func (m *DynamicWeightManager) getMetrics(key string) (*DynamicWeightMetrics, error) {
	data, err := m.store.Get(key)
	if err != nil {
		if err == store.ErrNotFound {
			return &DynamicWeightMetrics{}, nil
		}
		return nil, err
	}

	var metrics DynamicWeightMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to unmarshal metrics")
		return nil, err
	}
	return &metrics, nil
}

// SetMetrics stores metrics in the store (exported for persistence layer).
func (m *DynamicWeightManager) SetMetrics(key string, metrics *DynamicWeightMetrics) error {
	metrics.UpdatedAt = time.Now()
	data, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	return m.store.Set(key, data, m.config.MetricsTTL)
}

// RecordSubGroupSuccess records a successful request for a sub-group.
func (m *DynamicWeightManager) RecordSubGroupSuccess(aggregateGroupID, subGroupID uint) {
	key := SubGroupMetricsKey(aggregateGroupID, subGroupID)
	m.recordSuccess(key)
}

// RecordSubGroupFailure records a failed request for a sub-group.
func (m *DynamicWeightManager) RecordSubGroupFailure(aggregateGroupID, subGroupID uint) {
	key := SubGroupMetricsKey(aggregateGroupID, subGroupID)
	m.recordFailure(key)
}

// RecordModelRedirectSuccess records a successful request for a model redirect target.
func (m *DynamicWeightManager) RecordModelRedirectSuccess(groupID uint, sourceModel string, targetModel string) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetModel)
	m.recordSuccess(key)
}

// RecordModelRedirectFailure records a failed request for a model redirect target.
func (m *DynamicWeightManager) RecordModelRedirectFailure(groupID uint, sourceModel string, targetModel string) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetModel)
	m.recordFailure(key)
}

// recordSuccess records a successful request.
// NOTE: Uses mutex for single-instance protection. In distributed deployments
// sharing the same store, concurrent updates may cause lost writes due to
// non-atomic read-modify-write pattern. This is acceptable for approximate
// health tracking where perfect accuracy isn't critical.
func (m *DynamicWeightManager) recordSuccess(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metrics, err := m.getMetrics(key)
	if err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to get metrics for success recording")
		metrics = &DynamicWeightMetrics{}
	}

	now := time.Now()
	metrics.ConsecutiveFailures = 0
	metrics.LastSuccessAt = now

	// Increment all time window counters (new request falls into all windows)
	metrics.Requests7d++
	metrics.Successes7d++
	metrics.Requests14d++
	metrics.Successes14d++
	metrics.Requests30d++
	metrics.Successes30d++
	metrics.Requests90d++
	metrics.Successes90d++
	metrics.Requests180d++
	metrics.Successes180d++

	if err := m.SetMetrics(key, metrics); err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to set metrics after success")
	}

	// Notify persistence layer about dirty key
	if m.dirtyCallback != nil {
		m.dirtyCallback(key)
	}
}

// recordFailure records a failed request.
func (m *DynamicWeightManager) recordFailure(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metrics, err := m.getMetrics(key)
	if err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to get metrics for failure recording")
		metrics = &DynamicWeightMetrics{}
	}

	now := time.Now()
	metrics.ConsecutiveFailures++
	metrics.LastFailureAt = now

	// Increment all time window request counters (failure doesn't increment success)
	metrics.Requests7d++
	metrics.Requests14d++
	metrics.Requests30d++
	metrics.Requests90d++
	metrics.Requests180d++

	if err := m.SetMetrics(key, metrics); err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to set metrics after failure")
	}

	// Notify persistence layer about dirty key
	if m.dirtyCallback != nil {
		m.dirtyCallback(key)
	}
}

// CalculateWeightedSuccessRate calculates the weighted success rate across time windows.
// Recent data (7 days) has the highest weight, older data has progressively lower weights.
// Returns a value between 0 and 100.
func (m *DynamicWeightManager) CalculateWeightedSuccessRate(metrics *DynamicWeightMetrics) float64 {
	if metrics == nil {
		return 100.0
	}

	// Get incremental requests and successes for each time window
	// Window data is cumulative, so we need to calculate incremental values
	type windowData struct {
		requests  int64
		successes int64
		weight    float64
	}

	windows := []windowData{
		{metrics.Requests7d, metrics.Successes7d, 1.0},
		{metrics.Requests14d - metrics.Requests7d, metrics.Successes14d - metrics.Successes7d, 0.8},
		{metrics.Requests30d - metrics.Requests14d, metrics.Successes30d - metrics.Successes14d, 0.6},
		{metrics.Requests90d - metrics.Requests30d, metrics.Successes90d - metrics.Successes30d, 0.3},
		{metrics.Requests180d - metrics.Requests90d, metrics.Successes180d - metrics.Successes90d, 0.1},
	}

	var totalWeightedSuccesses, totalWeightedRequests float64
	for _, w := range windows {
		if w.requests > 0 {
			totalWeightedSuccesses += float64(w.successes) * w.weight
			totalWeightedRequests += float64(w.requests) * w.weight
		}
	}

	if totalWeightedRequests == 0 {
		return 100.0
	}

	return (totalWeightedSuccesses / totalWeightedRequests) * 100
}

// CalculateHealthScore calculates the health score based on metrics.
// Returns a value between MinHealthScore and 1.0.
func (m *DynamicWeightManager) CalculateHealthScore(metrics *DynamicWeightMetrics) float64 {
	if metrics == nil {
		return 1.0
	}

	score := 1.0

	// Penalty for consecutive failures
	if metrics.ConsecutiveFailures > 0 {
		penalty := float64(metrics.ConsecutiveFailures) * m.config.ConsecutiveFailurePenalty
		if penalty > m.config.MaxConsecutiveFailurePenalty {
			penalty = m.config.MaxConsecutiveFailurePenalty
		}
		score -= penalty
	}

	// Penalty for recent failure (time-decaying)
	if !metrics.LastFailureAt.IsZero() {
		timeSinceFailure := time.Since(metrics.LastFailureAt)
		if timeSinceFailure < m.config.RecentFailureCooldown {
			decayRatio := 1.0 - (float64(timeSinceFailure) / float64(m.config.RecentFailureCooldown))
			penalty := m.config.RecentFailurePenalty * decayRatio
			score -= penalty
		}
	}

	// Penalty for low weighted success rate (using time-windowed calculation)
	totalRequests := metrics.Requests180d
	if totalRequests >= m.config.MinRequestsForSuccessRate {
		weightedSuccessRate := m.CalculateWeightedSuccessRate(metrics)
		if weightedSuccessRate < m.config.LowSuccessRateThreshold {
			score -= m.config.LowSuccessRatePenalty
		}
	}

	if score < m.config.MinHealthScore {
		score = m.config.MinHealthScore
	}

	return score
}

// GetEffectiveWeight calculates the effective weight based on base weight and health score.
// Implements non-linear penalty for low health scores to reduce traffic to unhealthy targets.
// Three health score ranges (optimized for unstable channels with intermittent failures):
// 1. Critical (<= 0.50): effective weight reduced to 10% of base weight, capped at 1 (min 0.1)
//    This prevents unhealthy high-weight targets from dominating healthy low-weight targets
//    Example (intermediate values before rounding): baseWeight=100 -> 10% = 10, capped to 1;
//    baseWeight=5 -> 10% = 0.5; baseWeight=1 -> 10% = 0.1
//    (all rounded to minimum 1 in final result)
// 2. Medium (0.50 to 0.75): aggressive non-linear penalty using quadratic function
//    Example: health=0.6 -> weight multiplier = 0.6^2 = 0.36
// 3. Good (> 0.75): linear scaling
// Note: All effective weights are rounded to integers with minimum of 1 for weighted random selection.
func (m *DynamicWeightManager) GetEffectiveWeight(baseWeight int, metrics *DynamicWeightMetrics) int {
	if baseWeight <= 0 {
		return 0
	}

	healthScore := m.CalculateHealthScore(metrics)

	var effectiveWeight float64

	// Critical health: use minimum 10% of base weight to allow recovery
	// Cap at 1 to prevent unhealthy high-weight targets from dominating healthy low-weight targets
	// Example: baseWeight=100 -> min(10, 1) = 1, baseWeight=5 -> 0.5 (rounds up to 1)
	if healthScore <= m.config.CriticalHealthThreshold {
		minWeight := float64(baseWeight) * 0.1
		if minWeight > 1.0 {
			minWeight = 1.0 // Cap at 1 to ensure fair competition with healthy low-weight targets
		}
		if minWeight < 0.1 {
			minWeight = 0.1 // Floor at 0.1 to allow recovery
		}
		effectiveWeight = minWeight
	} else if healthScore < m.config.MediumHealthThreshold {
		// Medium health: apply aggressive non-linear penalty
		// Use power function to create aggressive penalty curve
		// Example with exponent=2.0: health=0.4 -> 0.16, health=0.5 -> 0.25, health=0.6 -> 0.36
		penalizedScore := math.Pow(healthScore, m.config.MediumHealthPenaltyExponent)
		effectiveWeight = float64(baseWeight) * penalizedScore
	} else {
		// Good health: linear scaling
		effectiveWeight = float64(baseWeight) * healthScore
	}

	// Round to nearest integer and ensure minimum of 1
	result := int(math.Round(effectiveWeight))
	if result < 1 {
		result = 1
	}

	return result
}

// SubGroupWeightInput is the input for GetSubGroupDynamicWeights.
type SubGroupWeightInput struct {
	SubGroupID uint
	Weight     int
}

// GetSubGroupDynamicWeights returns dynamic weight info for all sub-groups of an aggregate group.
func (m *DynamicWeightManager) GetSubGroupDynamicWeights(aggregateGroupID uint, subGroups []SubGroupWeightInput) []models.DynamicWeightInfo {
	result := make([]models.DynamicWeightInfo, len(subGroups))

	for i, sg := range subGroups {
		metrics, err := m.GetSubGroupMetrics(aggregateGroupID, sg.SubGroupID)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"aggregate_group_id": aggregateGroupID,
				"sub_group_id":       sg.SubGroupID,
			}).Debug("Failed to get sub-group metrics")
			metrics = &DynamicWeightMetrics{}
		}

		healthScore := m.CalculateHealthScore(metrics)
		effectiveWeight := m.GetEffectiveWeight(sg.Weight, metrics)
		weightedSuccessRate := m.CalculateWeightedSuccessRate(metrics)

		info := models.DynamicWeightInfo{
			BaseWeight:      sg.Weight,
			HealthScore:     healthScore,
			EffectiveWeight: effectiveWeight,
			RequestCount:    metrics.Requests180d,
			SuccessRate:     weightedSuccessRate,
		}

		if !metrics.LastFailureAt.IsZero() {
			ts := metrics.LastFailureAt.Format(time.RFC3339)
			info.LastFailureAt = &ts
		}
		if !metrics.LastSuccessAt.IsZero() {
			ts := metrics.LastSuccessAt.Format(time.RFC3339)
			info.LastSuccessAt = &ts
		}

		result[i] = info
	}

	return result
}

// GetEffectiveWeightsForSelection returns effective weights for weighted random selection.
func (m *DynamicWeightManager) GetEffectiveWeightsForSelection(aggregateGroupID uint, subGroups []SubGroupWeightInput) []int {
	weights := make([]int, len(subGroups))
	for i, sg := range subGroups {
		metrics, err := m.GetSubGroupMetrics(aggregateGroupID, sg.SubGroupID)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"aggregate_group_id": aggregateGroupID,
				"sub_group_id":       sg.SubGroupID,
			}).Debug("Failed to get sub-group metrics for selection")
		}
		weights[i] = m.GetEffectiveWeight(sg.Weight, metrics)
	}
	return weights
}

// GetModelRedirectEffectiveWeights returns effective weights for model redirect targets.
// Takes a slice of target models and their base weights, returns effective weights
// adjusted by health scores.
func (m *DynamicWeightManager) GetModelRedirectEffectiveWeights(groupID uint, sourceModel string, targets []string, targetWeights []int) []int {
	if len(targets) != len(targetWeights) {
		logrus.WithFields(logrus.Fields{
			"group_id":      groupID,
			"source_model":  sourceModel,
			"targets_len":   len(targets),
			"weights_len":   len(targetWeights),
		}).Warn("Mismatched targets and weights length")
		return targetWeights
	}

	weights := make([]int, len(targetWeights))
	for i, baseWeight := range targetWeights {
		targetModel := targets[i]
		metrics, err := m.GetModelRedirectMetrics(groupID, sourceModel, targetModel)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"group_id":     groupID,
				"source_model": sourceModel,
				"target_model": targetModel,
			}).Debug("Failed to get model redirect metrics for selection")
		}
		weights[i] = m.GetEffectiveWeight(baseWeight, metrics)
	}
	return weights
}

// ResetSubGroupMetrics resets metrics for a sub-group.
func (m *DynamicWeightManager) ResetSubGroupMetrics(aggregateGroupID, subGroupID uint) error {
	key := SubGroupMetricsKey(aggregateGroupID, subGroupID)
	return m.store.Delete(key)
}

// ResetModelRedirectMetrics resets metrics for a model redirect target.
func (m *DynamicWeightManager) ResetModelRedirectMetrics(groupID uint, sourceModel string, targetModel string) error {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetModel)
	return m.store.Delete(key)
}

// DynamicWeightedRandomSelect performs weighted random selection using dynamic weights.
func (m *DynamicWeightManager) DynamicWeightedRandomSelect(aggregateGroupID uint, subGroups []SubGroupWeightInput) int {
	weights := m.GetEffectiveWeightsForSelection(aggregateGroupID, subGroups)
	return utils.WeightedRandomSelect(weights)
}

// GetStore returns the underlying store (for persistence layer).
func (m *DynamicWeightManager) GetStore() store.Store {
	return m.store
}
