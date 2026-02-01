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

	// Health score threshold below which effective weight becomes zero
	// This prevents unhealthy targets from receiving traffic
	CriticalHealthThreshold float64
	// Health score threshold for applying aggressive penalty
	// Targets with health score between CriticalHealthThreshold and MediumHealthThreshold
	// will have their weight reduced more aggressively
	MediumHealthThreshold float64
	// Penalty multiplier for medium health scores (0.3-0.7 range)
	// Applied as: effectiveWeight = baseWeight * (healthScore ^ MediumHealthPenaltyExponent)
	MediumHealthPenaltyExponent float64
}

// DefaultDynamicWeightConfig returns the default configuration.
func DefaultDynamicWeightConfig() *DynamicWeightConfig {
	return &DynamicWeightConfig{
		ConsecutiveFailurePenalty:    0.1,
		MaxConsecutiveFailurePenalty: 0.5,
		RecentFailurePenalty:         0.2,
		RecentFailureCooldown:        5 * time.Minute,
		LowSuccessRatePenalty:        0.2,
		LowSuccessRateThreshold:      50.0,
		MinRequestsForSuccessRate:    5,
		MinHealthScore:               0.1,
		MetricsTTL:                   180 * 24 * time.Hour, // 180 days
		TimeWindowConfigs:            models.DefaultTimeWindowConfigs(),
		// Critical health threshold: below this, effective weight becomes zero
		CriticalHealthThreshold: 0.3,
		// Medium health threshold: between critical and this, apply aggressive penalty
		MediumHealthThreshold: 0.7,
		// Penalty exponent for medium health scores (quadratic penalty by default)
		// This makes low-medium health scores (e.g., 0.4) have much lower effective weight
		// Example: 0.4^2 = 0.16, so a weight of 10 becomes 1.6 (rounded to 2)
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
func ModelRedirectMetricsKey(groupID uint, sourceModel string, targetIndex int) string {
	encodedModel := url.PathEscape(sourceModel)
	return fmt.Sprintf("dw:mr:%d:%s:%d", groupID, encodedModel, targetIndex)
}

// GetSubGroupMetrics retrieves metrics for a sub-group.
func (m *DynamicWeightManager) GetSubGroupMetrics(aggregateGroupID, subGroupID uint) (*DynamicWeightMetrics, error) {
	key := SubGroupMetricsKey(aggregateGroupID, subGroupID)
	return m.getMetrics(key)
}

// GetModelRedirectMetrics retrieves metrics for a model redirect target.
func (m *DynamicWeightManager) GetModelRedirectMetrics(groupID uint, sourceModel string, targetIndex int) (*DynamicWeightMetrics, error) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetIndex)
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
func (m *DynamicWeightManager) RecordModelRedirectSuccess(groupID uint, sourceModel string, targetIndex int) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetIndex)
	m.recordSuccess(key)
}

// RecordModelRedirectFailure records a failed request for a model redirect target.
func (m *DynamicWeightManager) RecordModelRedirectFailure(groupID uint, sourceModel string, targetIndex int) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetIndex)
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
// Implements non-linear penalty for low health scores to prevent unhealthy targets from receiving traffic.
// Three health score ranges:
// 1. Critical (<= CriticalHealthThreshold): effective weight = 0 (completely excluded)
// 2. Medium (CriticalHealthThreshold to MediumHealthThreshold): aggressive non-linear penalty
// 3. Good (> MediumHealthThreshold): linear scaling
func (m *DynamicWeightManager) GetEffectiveWeight(baseWeight int, metrics *DynamicWeightMetrics) int {
	if baseWeight <= 0 {
		return 0
	}

	healthScore := m.CalculateHealthScore(metrics)

	// Critical health: completely exclude from selection
	if healthScore <= m.config.CriticalHealthThreshold {
		return 0
	}

	var effectiveWeight int

	// Medium health: apply aggressive non-linear penalty
	if healthScore < m.config.MediumHealthThreshold {
		// Use power function to create aggressive penalty curve
		// Example with exponent=2.0: health=0.4 -> 0.16, health=0.5 -> 0.25, health=0.6 -> 0.36
		penalizedScore := math.Pow(healthScore, m.config.MediumHealthPenaltyExponent)
		effectiveWeight = int(float64(baseWeight) * penalizedScore)
	} else {
		// Good health: linear scaling
		effectiveWeight = int(float64(baseWeight) * healthScore)
	}

	// Ensure minimum weight of 1 for non-critical health scores
	// This prevents rounding to zero for small base weights with medium health
	if effectiveWeight < 1 && healthScore > m.config.CriticalHealthThreshold {
		effectiveWeight = 1
	}

	return effectiveWeight
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
func (m *DynamicWeightManager) GetModelRedirectEffectiveWeights(groupID uint, sourceModel string, targetWeights []int) []int {
	weights := make([]int, len(targetWeights))
	for i, baseWeight := range targetWeights {
		metrics, err := m.GetModelRedirectMetrics(groupID, sourceModel, i)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"group_id":     groupID,
				"source_model": sourceModel,
				"target_index": i,
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
func (m *DynamicWeightManager) ResetModelRedirectMetrics(groupID uint, sourceModel string, targetIndex int) error {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetIndex)
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
