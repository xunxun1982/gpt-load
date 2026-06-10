// Package services provides business logic services for the application.
package services

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
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

	// Rate limit tracking (429 errors)
	// Rate limit failures are tracked separately because they indicate temporary throttling
	// rather than service unavailability, and should receive lighter penalties
	ConsecutiveRateLimits int64     `json:"consecutive_rate_limits"`
	LastRateLimitAt       time.Time `json:"last_rate_limit_at"`

	// Time-windowed statistics (cumulative)
	Requests7d     int64 `json:"requests_7d"`
	Successes7d    int64 `json:"successes_7d"`
	RateLimits7d   int64 `json:"rate_limits_7d"`
	Requests14d    int64 `json:"requests_14d"`
	Successes14d   int64 `json:"successes_14d"`
	RateLimits14d  int64 `json:"rate_limits_14d"`
	Requests30d    int64 `json:"requests_30d"`
	Successes30d   int64 `json:"successes_30d"`
	RateLimits30d  int64 `json:"rate_limits_30d"`
	Requests90d    int64 `json:"requests_90d"`
	Successes90d   int64 `json:"successes_90d"`
	RateLimits90d  int64 `json:"rate_limits_90d"`
	Requests180d   int64 `json:"requests_180d"`
	Successes180d  int64 `json:"successes_180d"`
	RateLimits180d int64 `json:"rate_limits_180d"`

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

	// Rate limit (429) specific penalties
	// 429 errors indicate temporary throttling, not service failure
	// They should receive lighter penalties than hard failures (500, 401, etc.)
	ConsecutiveRateLimitPenalty    float64
	MaxConsecutiveRateLimitPenalty float64
	RecentRateLimitPenalty         float64
	RecentRateLimitCooldown        time.Duration
	RateLimitSuccessCredit         float64

	// CriticalHealthThreshold is the health score threshold at or below which
	// enabled targets use the fixed 1.0 recovery weight to prevent unhealthy
	// high-weight targets from dominating healthy low-weight targets.
	CriticalHealthThreshold float64
	// Health score threshold for applying aggressive penalty
	// Targets with health score between CriticalHealthThreshold and MediumHealthThreshold
	// will have their weight reduced more aggressively using quadratic penalty
	MediumHealthThreshold float64
	// Penalty multiplier for medium health scores (0.3-0.7 range)
	// Applied as: effectiveWeight = baseWeight * (healthScore ^ MediumHealthPenaltyExponent)
	MediumHealthPenaltyExponent float64
}

// Recent failure memory is kept after a later success, but softened to reward recovery.
const recentPenaltyAfterSuccessMultiplier = 0.75

// DefaultDynamicWeightConfig returns the default configuration.
// Optimized for aggregate routing: isolated failures are tolerated, while repeated hard
// failures quickly lose traffic and retain only the minimum recovery weight.
func DefaultDynamicWeightConfig() *DynamicWeightConfig {
	return &DynamicWeightConfig{
		// Consecutive failure penalty: 0.08 per failure
		// The hard-failure path uses a progressive curve so repeated failures are penalized
		// faster than isolated failures, similar to passive outlier-detection thresholds.
		ConsecutiveFailurePenalty: 0.08,
		// Max penalty at 0.99 (reached around 10 consecutive hard failures)
		// Keeps a recovery path through MinHealthScore while quickly removing bad targets from normal selection.
		MaxConsecutiveFailurePenalty: 0.99,
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
		// Minimum health score of 0.01 for display; effective weight keeps a separate recovery floor.
		MinHealthScore: 0.01,
		// Metrics TTL of 180 days
		MetricsTTL: 180 * 24 * time.Hour,
		// Time window configs for weighted success rate calculation
		TimeWindowConfigs: models.DefaultTimeWindowConfigs(),

		// Rate limit (429) penalties - approximately 30% of regular failure penalties
		// 429 indicates temporary throttling, not service unavailability
		// Lighter penalties allow the service to recover traffic faster once rate limit clears
		ConsecutiveRateLimitPenalty: 0.025, // ~30% of 0.08
		// Max penalty at 0.125 (reached after 5 consecutive rate limits)
		// Much lighter than hard-failure max of 0.99.
		MaxConsecutiveRateLimitPenalty: 0.125,
		// Recent rate limit penalty: 0.04 with time decay (~30% of 0.12)
		RecentRateLimitPenalty: 0.04,
		// 3-minute cooldown for rate limit penalty (shorter than regular 8-minute)
		// Rate limits typically clear faster than service failures
		RecentRateLimitCooldown: 3 * time.Minute,
		// Count 429 as a partial success in long-window success rate.
		// Throttling is still a failed request, but it should not collapse health like auth/billing/server failures.
		RateLimitSuccessCredit: 0.35,

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

// GroupMetricsKey returns the store key for standard group metrics.
// Used for tracking request success/failure at the group level for Hub health score calculation.
func GroupMetricsKey(groupID uint) string {
	return fmt.Sprintf("dw:g:%d", groupID)
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

// GetGroupMetrics retrieves metrics for a standard group.
// Used for Hub health score calculation based on request success rate.
func (m *DynamicWeightManager) GetGroupMetrics(groupID uint) (*DynamicWeightMetrics, error) {
	key := GroupMetricsKey(groupID)
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
func (m *DynamicWeightManager) RecordSubGroupFailure(aggregateGroupID, subGroupID uint, isRateLimit bool) {
	key := SubGroupMetricsKey(aggregateGroupID, subGroupID)
	m.recordFailure(key, isRateLimit)
}

// RecordGroupSuccess records a successful request for a standard group.
// Used for Hub health score calculation based on request success rate.
func (m *DynamicWeightManager) RecordGroupSuccess(groupID uint) {
	key := GroupMetricsKey(groupID)
	m.recordSuccess(key)
}

// RecordGroupFailure records a failed request for a standard group.
// Used for Hub health score calculation based on request success rate.
func (m *DynamicWeightManager) RecordGroupFailure(groupID uint, isRateLimit bool) {
	key := GroupMetricsKey(groupID)
	m.recordFailure(key, isRateLimit)
}

// RecordModelRedirectSuccess records a successful request for a model redirect target.
func (m *DynamicWeightManager) RecordModelRedirectSuccess(groupID uint, sourceModel string, targetModel string) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetModel)
	m.recordSuccess(key)
}

// RecordModelRedirectFailure records a failed request for a model redirect target.
func (m *DynamicWeightManager) RecordModelRedirectFailure(groupID uint, sourceModel string, targetModel string, isRateLimit bool) {
	key := ModelRedirectMetricsKey(groupID, sourceModel, targetModel)
	m.recordFailure(key, isRateLimit)
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
	metrics.ConsecutiveRateLimits = 0 // Clear rate limit counter on success
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
// isRateLimit indicates if this is a 429 rate limit error, which receives lighter penalties.
func (m *DynamicWeightManager) recordFailure(key string, isRateLimit bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metrics, err := m.getMetrics(key)
	if err != nil {
		logrus.WithError(err).WithField("key", key).Debug("Failed to get metrics for failure recording")
		metrics = &DynamicWeightMetrics{}
	}

	now := time.Now()

	if isRateLimit {
		// Rate limit (429) errors: track separately with lighter penalties
		// Don't increment ConsecutiveFailures as service is still available
		metrics.ConsecutiveRateLimits++
		metrics.LastRateLimitAt = now
	} else {
		// Regular failures (500, 401, etc.): full penalty
		metrics.ConsecutiveFailures++
		metrics.ConsecutiveRateLimits = 0 // Reset rate limit counter on hard failure
		metrics.LastFailureAt = now
	}

	// Increment all time window request counters (failure doesn't increment success)
	metrics.Requests7d++
	metrics.Requests14d++
	metrics.Requests30d++
	metrics.Requests90d++
	metrics.Requests180d++
	if isRateLimit {
		metrics.RateLimits7d++
		metrics.RateLimits14d++
		metrics.RateLimits30d++
		metrics.RateLimits90d++
		metrics.RateLimits180d++
	}

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
	return m.calculateWeightedSuccessRate(metrics, 0)
}

func (m *DynamicWeightManager) calculateWeightedSuccessRate(metrics *DynamicWeightMetrics, rateLimitCredit float64) float64 {
	if metrics == nil {
		return 100.0
	}

	var totalWeightedSuccesses, totalWeightedRequests float64
	addWeightedWindow := func(requests, successes, rateLimits int64, weight float64) {
		if requests <= 0 {
			return
		}
		creditedSuccesses := float64(successes)
		if rateLimitCredit > 0 && rateLimits > 0 {
			failedRequests := requests - successes
			if failedRequests < 0 {
				failedRequests = 0
			}
			if rateLimits > failedRequests {
				rateLimits = failedRequests
			}
			creditedSuccesses += float64(rateLimits) * rateLimitCredit
		}
		totalWeightedSuccesses += creditedSuccesses * weight
		totalWeightedRequests += float64(requests) * weight
	}

	windowConfigs := m.config.TimeWindowConfigs
	if len(windowConfigs) == 0 {
		windowConfigs = models.DefaultTimeWindowConfigs()
	}
	windowConfigs = supportedTimeWindowConfigs(windowConfigs)
	sort.Slice(windowConfigs, func(i, j int) bool {
		return windowConfigs[i].Days < windowConfigs[j].Days
	})

	// Window data is cumulative, so each older bucket is converted to its incremental value.
	var previousRequests, previousSuccesses, previousRateLimits int64
	for _, config := range windowConfigs {
		requests, successes, rateLimits, ok := metrics.windowValues(config.Days)
		if !ok {
			continue
		}
		addWeightedWindow(
			requests-previousRequests,
			successes-previousSuccesses,
			rateLimits-previousRateLimits,
			config.Weight,
		)
		previousRequests = requests
		previousSuccesses = successes
		previousRateLimits = rateLimits
	}

	if totalWeightedRequests == 0 {
		return 100.0
	}

	return (totalWeightedSuccesses / totalWeightedRequests) * 100
}

func supportedTimeWindowConfigs(configs []models.TimeWindowConfig) []models.TimeWindowConfig {
	valid := make([]models.TimeWindowConfig, 0, len(configs))
	for _, config := range configs {
		if isSupportedTimeWindow(config.Days) {
			valid = append(valid, config)
		}
	}
	if len(valid) == 0 {
		return models.DefaultTimeWindowConfigs()
	}
	return valid
}

func isSupportedTimeWindow(days int) bool {
	switch days {
	case 7, 14, 30, 90, 180:
		return true
	default:
		return false
	}
}

func (m *DynamicWeightMetrics) windowValues(days int) (requests, successes, rateLimits int64, ok bool) {
	switch days {
	case 7:
		return m.Requests7d, m.Successes7d, m.RateLimits7d, true
	case 14:
		return m.Requests14d, m.Successes14d, m.RateLimits14d, true
	case 30:
		return m.Requests30d, m.Successes30d, m.RateLimits30d, true
	case 90:
		return m.Requests90d, m.Successes90d, m.RateLimits90d, true
	case 180:
		return m.Requests180d, m.Successes180d, m.RateLimits180d, true
	default:
		return 0, 0, 0, false
	}
}

// calculateHealthSuccessRate returns a weighted success rate for health scoring.
// It gives 429 throttling failures partial credit while preserving raw success rate reporting.
func (m *DynamicWeightManager) calculateHealthSuccessRate(metrics *DynamicWeightMetrics) float64 {
	credit := min(max(m.config.RateLimitSuccessCredit, 0.0), 1.0)
	return m.calculateWeightedSuccessRate(metrics, credit)
}

func calculateRecentEventPenalty(now, eventAt, lastSuccessAt time.Time, cooldown time.Duration, basePenalty float64) float64 {
	if eventAt.IsZero() || cooldown <= 0 || basePenalty <= 0 {
		return 0
	}

	timeSinceEvent := now.Sub(eventAt)
	if timeSinceEvent < 0 {
		timeSinceEvent = 0
	}
	if timeSinceEvent >= cooldown {
		return 0
	}

	decayRatio := 1.0 - (float64(timeSinceEvent) / float64(cooldown))
	penalty := basePenalty * decayRatio
	if !lastSuccessAt.IsZero() && lastSuccessAt.After(eventAt) {
		penalty *= recentPenaltyAfterSuccessMultiplier
	}
	return penalty
}

func calculateConsecutiveHardFailurePenalty(count int64, basePenalty, maxPenalty float64) float64 {
	if count <= 0 || basePenalty <= 0 || maxPenalty <= 0 {
		return 0
	}

	// Explicit multipliers keep 2-10 consecutive hard failures distinct.
	// This mirrors passive outlier detection: early failures are tolerated, repeated failures accelerate quickly.
	multipliers := [...]float64{0, 1, 2.25, 4, 5.75, 7.25, 8.75, 10.25, 11.5, 12, 12.375}
	var penalty float64
	if count < int64(len(multipliers)) {
		penalty = basePenalty * multipliers[count]
	} else {
		penalty = maxPenalty
	}
	if penalty > maxPenalty {
		return maxPenalty
	}
	return penalty
}

func zeroSuccessHealthCap(totalRequests int64, minHealth float64) float64 {
	switch {
	case totalRequests >= 10:
		return minHealth
	case totalRequests >= 9:
		return 0.04
	case totalRequests >= 8:
		return 0.08
	case totalRequests >= 7:
		return 0.12
	case totalRequests >= 6:
		return 0.18
	default:
		return 0.24
	}
}

// CalculateHealthScore calculates the health score based on metrics.
// Returns a value between MinHealthScore and 1.0.
func (m *DynamicWeightManager) CalculateHealthScore(metrics *DynamicWeightMetrics) float64 {
	if metrics == nil {
		return 1.0
	}

	score := 1.0
	now := time.Now()

	// Penalty for consecutive failures (hard failures like 500, 401, etc.)
	if metrics.ConsecutiveFailures > 0 {
		score -= calculateConsecutiveHardFailurePenalty(
			metrics.ConsecutiveFailures,
			m.config.ConsecutiveFailurePenalty,
			m.config.MaxConsecutiveFailurePenalty,
		)
	}

	// Penalty for consecutive rate limits (429 errors) - lighter than hard failures
	if metrics.ConsecutiveRateLimits > 0 {
		penalty := float64(metrics.ConsecutiveRateLimits) * m.config.ConsecutiveRateLimitPenalty
		if penalty > m.config.MaxConsecutiveRateLimitPenalty {
			penalty = m.config.MaxConsecutiveRateLimitPenalty
		}
		score -= penalty
	}

	// Penalty for recent failure (time-decaying)
	score -= calculateRecentEventPenalty(
		now,
		metrics.LastFailureAt,
		metrics.LastSuccessAt,
		m.config.RecentFailureCooldown,
		m.config.RecentFailurePenalty,
	)

	// Penalty for recent rate limit (time-decaying) - lighter than hard failure
	score -= calculateRecentEventPenalty(
		now,
		metrics.LastRateLimitAt,
		metrics.LastSuccessAt,
		m.config.RecentRateLimitCooldown,
		m.config.RecentRateLimitPenalty,
	)

	// Penalty for low weighted success rate (using time-windowed calculation)
	totalRequests := metrics.Requests180d
	if totalRequests >= m.config.MinRequestsForSuccessRate {
		weightedSuccessRate := m.calculateHealthSuccessRate(metrics)
		if weightedSuccessRate <= 0 {
			// Zero health success rate after enough samples caps display health by sample count,
			// while effective weight still preserves a separate recovery path.
			score = min(score, zeroSuccessHealthCap(totalRequests, m.config.MinHealthScore))
		} else if weightedSuccessRate < m.config.LowSuccessRateThreshold {
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
//  1. Critical (<= 0.50): fixed 1.0 recovery weight
//     This prevents unhealthy high-weight targets from dominating healthy low-weight targets
//     Example: baseWeight=100 -> 1.0; baseWeight=5 -> 1.0; baseWeight=1 -> 1.0
//  2. Medium (0.50 to 0.75): aggressive non-linear penalty using quadratic function
//     Example: health=0.6 -> weight multiplier = 0.6^2 = 0.36
//  3. Good (> 0.75): linear scaling
//
// Returns a float64 value with 1 decimal place precision, minimum 1.0 for enabled weights.
func (m *DynamicWeightManager) GetEffectiveWeight(baseWeight int, metrics *DynamicWeightMetrics) float64 {
	if baseWeight <= 0 {
		return 0.0
	}

	healthScore := m.CalculateHealthScore(metrics)

	var effectiveWeight float64

	// Critical health: fixed 1.0 recovery weight for all enabled targets.
	if healthScore <= m.config.CriticalHealthThreshold {
		effectiveWeight = 1.0
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

	// Round to 1 decimal place and ensure enabled targets keep a 1.0 recovery floor.
	result := math.Round(effectiveWeight*10) / 10
	if result < 1.0 {
		result = 1.0
	}

	return result
}

// GetEffectiveWeightForSelection converts float effective weight to integer for weighted random selection.
// Multiplies by 10 to preserve 1 decimal place precision (e.g., 1.5 -> 15, 0.1 -> 1).
// This ensures accurate weight ratios in weighted random selection:
//   - Without scaling: 1.5 rounds to 2, 0.5 rounds to 1, ratio becomes 2:1 (incorrect)
//   - With 10x scaling: 1.5 -> 15, 0.5 -> 5, ratio stays 15:5 = 3:1 (correct)
//
// Returns 0 if the effective weight is 0 (disabled).
func GetEffectiveWeightForSelection(effectiveWeight float64) int {
	if effectiveWeight == 0.0 {
		return 0
	}
	weight := int(math.Round(effectiveWeight * 10))
	if weight < 1 {
		weight = 1
	}
	return weight
}

// SubGroupWeightInput holds the sub-group ID and its base weight for dynamic weight calculation.
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
// Converts float effective weights to integers by multiplying by 10 to preserve 1 decimal place precision.
// Returns 0 for disabled sub-groups (base weight = 0).
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
		effectiveWeight := m.GetEffectiveWeight(sg.Weight, metrics)
		weights[i] = GetEffectiveWeightForSelection(effectiveWeight)
	}
	return weights
}

// GetModelRedirectEffectiveWeights returns effective weights for model redirect targets.
// Takes a slice of target models and their base weights, returns effective weights
// adjusted by health scores. Converts float weights to integers for weighted random selection.
// Returns 0 for disabled targets (base weight = 0).
func (m *DynamicWeightManager) GetModelRedirectEffectiveWeights(groupID uint, sourceModel string, targets []string, targetWeights []int) []int {
	if len(targets) != len(targetWeights) {
		logrus.WithFields(logrus.Fields{
			"group_id":     groupID,
			"source_model": sourceModel,
			"targets_len":  len(targets),
			"weights_len":  len(targetWeights),
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
		effectiveWeight := m.GetEffectiveWeight(baseWeight, metrics)
		weights[i] = GetEffectiveWeightForSelection(effectiveWeight)
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
