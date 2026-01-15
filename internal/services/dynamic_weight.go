// Package services provides business logic services for the application.
package services

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
)

// DynamicWeightMetrics holds the metrics used for dynamic weight calculation.
// These metrics are stored in Redis and used to calculate health scores.
type DynamicWeightMetrics struct {
	ConsecutiveFailures int64     `json:"consecutive_failures"` // Number of consecutive failures
	LastFailureAt       time.Time `json:"last_failure_at"`      // Timestamp of last failure
	LastSuccessAt       time.Time `json:"last_success_at"`      // Timestamp of last success
	RequestCount        int64     `json:"request_count"`        // Total request count
	SuccessCount        int64     `json:"success_count"`        // Total success count
	UpdatedAt           time.Time `json:"updated_at"`           // Last update timestamp
}

// DynamicWeightConfig holds configuration for dynamic weight calculation.
type DynamicWeightConfig struct {
	// ConsecutiveFailurePenalty is the penalty per consecutive failure (default: 0.1)
	ConsecutiveFailurePenalty float64
	// MaxConsecutiveFailurePenalty is the maximum penalty from consecutive failures (default: 0.5)
	MaxConsecutiveFailurePenalty float64
	// RecentFailurePenalty is the maximum penalty for recent failures (default: 0.2)
	RecentFailurePenalty float64
	// RecentFailureCooldown is the cooldown period for recent failure penalty (default: 5 minutes)
	RecentFailureCooldown time.Duration
	// LowSuccessRatePenalty is the penalty for low success rate (default: 0.2)
	LowSuccessRatePenalty float64
	// LowSuccessRateThreshold is the threshold below which low success rate penalty applies (default: 50%)
	LowSuccessRateThreshold float64
	// MinRequestsForSuccessRate is the minimum requests needed to calculate success rate (default: 5)
	MinRequestsForSuccessRate int64
	// MinHealthScore is the minimum health score to prevent complete disabling (default: 0.1)
	MinHealthScore float64
	// MetricsTTL is the TTL for metrics in Redis (default: 1 hour)
	// Note: 1-hour TTL means metrics reset after inactivity, which may lose historical data
	// for low-traffic endpoints. Consider longer TTL (e.g., 24 hours) for production use
	// or make this configurable via environment variable for different deployment scenarios.
	MetricsTTL time.Duration
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
		MetricsTTL:                   1 * time.Hour,
	}
}

// DynamicWeightManager manages dynamic weight calculation for sub-groups and model redirects.
// It uses Redis to store metrics and calculates health scores based on request history.
type DynamicWeightManager struct {
	store  store.Store
	config *DynamicWeightConfig
	mu     sync.RWMutex
}

// NewDynamicWeightManager creates a new dynamic weight manager.
// Uses default configuration. For custom configuration, use NewDynamicWeightManagerWithConfig.
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

// subGroupMetricsKey returns the Redis key for sub-group metrics.
func subGroupMetricsKey(aggregateGroupID, subGroupID uint) string {
	return fmt.Sprintf("dw:sg:%d:%d", aggregateGroupID, subGroupID)
}

// modelRedirectMetricsKey returns the Redis key for model redirect metrics.
// Uses URL encoding for sourceModel to prevent key collisions when model names contain colons.
// Example: "anthropic:claude-3" becomes "anthropic%3Aclaude-3" in the key.
func modelRedirectMetricsKey(groupID uint, sourceModel string, targetIndex int) string {
	// URL-encode the model name to handle special characters like colons
	// This prevents ambiguity in Redis key parsing (e.g., "dw:mr:1:model:with:colons:0")
	encodedModel := url.PathEscape(sourceModel)
	return fmt.Sprintf("dw:mr:%d:%s:%d", groupID, encodedModel, targetIndex)
}

// GetSubGroupMetrics retrieves metrics for a sub-group.
func (m *DynamicWeightManager) GetSubGroupMetrics(aggregateGroupID, subGroupID uint) (*DynamicWeightMetrics, error) {
	key := subGroupMetricsKey(aggregateGroupID, subGroupID)
	return m.getMetrics(key)
}

// GetModelRedirectMetrics retrieves metrics for a model redirect target.
func (m *DynamicWeightManager) GetModelRedirectMetrics(groupID uint, sourceModel string, targetIndex int) (*DynamicWeightMetrics, error) {
	key := modelRedirectMetricsKey(groupID, sourceModel, targetIndex)
	return m.getMetrics(key)
}

// getMetrics retrieves metrics from Redis.
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
		return nil, err
	}
	return &metrics, nil
}

// setMetrics stores metrics in Redis.
func (m *DynamicWeightManager) setMetrics(key string, metrics *DynamicWeightMetrics) error {
	metrics.UpdatedAt = time.Now()
	data, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	return m.store.Set(key, data, m.config.MetricsTTL)
}

// RecordSubGroupSuccess records a successful request for a sub-group.
func (m *DynamicWeightManager) RecordSubGroupSuccess(aggregateGroupID, subGroupID uint) {
	key := subGroupMetricsKey(aggregateGroupID, subGroupID)
	m.recordSuccess(key)
}

// RecordSubGroupFailure records a failed request for a sub-group.
func (m *DynamicWeightManager) RecordSubGroupFailure(aggregateGroupID, subGroupID uint) {
	key := subGroupMetricsKey(aggregateGroupID, subGroupID)
	m.recordFailure(key)
}

// RecordModelRedirectSuccess records a successful request for a model redirect target.
func (m *DynamicWeightManager) RecordModelRedirectSuccess(groupID uint, sourceModel string, targetIndex int) {
	key := modelRedirectMetricsKey(groupID, sourceModel, targetIndex)
	m.recordSuccess(key)
}

// RecordModelRedirectFailure records a failed request for a model redirect target.
func (m *DynamicWeightManager) RecordModelRedirectFailure(groupID uint, sourceModel string, targetIndex int) {
	key := modelRedirectMetricsKey(groupID, sourceModel, targetIndex)
	m.recordFailure(key)
}

// recordSuccess records a successful request.
// Note: Uses global mutex for simplicity and correctness within a single instance.
// For high-load scenarios with many sub-groups/redirects, consider per-key locking
// (sync.Map or sharded locks) or Redis WATCH/MULTI/EXEC transactions for better scalability.
//
// Distributed deployment consideration: In multi-instance deployments sharing Redis,
// concurrent updates can cause lost writes due to non-atomic read-modify-write pattern.
// This is acceptable for approximate health tracking where perfect accuracy isn't critical.
// For precise counting, consider using Redis INCR/HINCRBY operations or Lua scripts.
func (m *DynamicWeightManager) recordSuccess(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metrics, err := m.getMetrics(key)
	if err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to get metrics for success recording")
		metrics = &DynamicWeightMetrics{}
	}

	metrics.ConsecutiveFailures = 0
	metrics.LastSuccessAt = time.Now()
	metrics.RequestCount++
	metrics.SuccessCount++

	if err := m.setMetrics(key, metrics); err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to set metrics after success")
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

	metrics.ConsecutiveFailures++
	metrics.LastFailureAt = time.Now()
	metrics.RequestCount++

	if err := m.setMetrics(key, metrics); err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to set metrics after failure")
	}
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
			// Linear decay: full penalty at t=0, zero penalty at t=cooldown
			decayRatio := 1.0 - (float64(timeSinceFailure) / float64(m.config.RecentFailureCooldown))
			penalty := m.config.RecentFailurePenalty * decayRatio
			score -= penalty
		}
	}

	// Penalty for low success rate
	if metrics.RequestCount >= m.config.MinRequestsForSuccessRate {
		successRate := float64(metrics.SuccessCount) / float64(metrics.RequestCount) * 100
		if successRate < m.config.LowSuccessRateThreshold {
			score -= m.config.LowSuccessRatePenalty
		}
	}

	// Ensure minimum health score
	if score < m.config.MinHealthScore {
		score = m.config.MinHealthScore
	}

	return score
}

// GetEffectiveWeight calculates the effective weight based on base weight and health score.
func (m *DynamicWeightManager) GetEffectiveWeight(baseWeight int, metrics *DynamicWeightMetrics) int {
	if baseWeight <= 0 {
		return 0
	}

	healthScore := m.CalculateHealthScore(metrics)
	effectiveWeight := int(float64(baseWeight) * healthScore)

	// Ensure at least 1 if base weight is positive and health score is above minimum
	if effectiveWeight < 1 && healthScore >= m.config.MinHealthScore {
		effectiveWeight = 1
	}

	return effectiveWeight
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

		info := models.DynamicWeightInfo{
			BaseWeight:      sg.Weight,
			HealthScore:     healthScore,
			EffectiveWeight: effectiveWeight,
			RequestCount:    metrics.RequestCount,
		}

		// Calculate success rate
		if metrics.RequestCount > 0 {
			info.SuccessRate = float64(metrics.SuccessCount) / float64(metrics.RequestCount) * 100
		}

		// Format timestamps
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

// SubGroupWeightInput is the input for GetSubGroupDynamicWeights.
type SubGroupWeightInput struct {
	SubGroupID uint
	Weight     int
}

// GetEffectiveWeightsForSelection returns effective weights for weighted random selection.
// This is used by SubGroupManager to get dynamic weights for selection.
func (m *DynamicWeightManager) GetEffectiveWeightsForSelection(aggregateGroupID uint, subGroups []SubGroupWeightInput) []int {
	weights := make([]int, len(subGroups))
	for i, sg := range subGroups {
		metrics, _ := m.GetSubGroupMetrics(aggregateGroupID, sg.SubGroupID)
		weights[i] = m.GetEffectiveWeight(sg.Weight, metrics)
	}
	return weights
}

// GetModelRedirectEffectiveWeights returns effective weights for model redirect targets.
// targetWeights is a slice of base weights for each target.
func (m *DynamicWeightManager) GetModelRedirectEffectiveWeights(groupID uint, sourceModel string, targetWeights []int) []int {
	weights := make([]int, len(targetWeights))
	for i, baseWeight := range targetWeights {
		metrics, _ := m.GetModelRedirectMetrics(groupID, sourceModel, i)
		weights[i] = m.GetEffectiveWeight(baseWeight, metrics)
	}
	return weights
}

// ResetSubGroupMetrics resets metrics for a sub-group.
func (m *DynamicWeightManager) ResetSubGroupMetrics(aggregateGroupID, subGroupID uint) error {
	key := subGroupMetricsKey(aggregateGroupID, subGroupID)
	return m.store.Delete(key)
}

// ResetModelRedirectMetrics resets metrics for a model redirect target.
func (m *DynamicWeightManager) ResetModelRedirectMetrics(groupID uint, sourceModel string, targetIndex int) error {
	key := modelRedirectMetricsKey(groupID, sourceModel, targetIndex)
	return m.store.Delete(key)
}

// DynamicWeightedRandomSelect performs weighted random selection using dynamic weights.
// It calculates effective weights based on metrics and uses the standard weighted random selection.
func (m *DynamicWeightManager) DynamicWeightedRandomSelect(aggregateGroupID uint, subGroups []SubGroupWeightInput) int {
	weights := m.GetEffectiveWeightsForSelection(aggregateGroupID, subGroups)
	return utils.WeightedRandomSelect(weights)
}
