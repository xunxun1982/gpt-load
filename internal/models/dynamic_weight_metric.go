package models

import (
	"time"
)

// MetricType defines the type of dynamic weight metric.
type MetricType string

const (
	// MetricTypeSubGroup represents metrics for aggregate group sub-groups.
	MetricTypeSubGroup MetricType = "sub_group"
	// MetricTypeModelRedirect represents metrics for model redirect targets.
	MetricTypeModelRedirect MetricType = "model_redirect"
)

// DynamicWeightMetric stores health metrics for dynamic weight calculation.
// This table persists metrics to survive application restarts.
// Metrics are tracked across multiple time windows (7d, 14d, 30d, 90d, 180d)
// with different weights for health score calculation.
//
// Design notes:
// - Each (aggregate_group, sub_group) pair has independent metrics
// - Same sub-group in different aggregate groups has separate health tracking
// - This isolation prevents one aggregate group's failures from affecting others
// - Soft delete is used to preserve data when sub-groups are temporarily removed
type DynamicWeightMetric struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	MetricType  MetricType `gorm:"type:varchar(20);not null;uniqueIndex:idx_dwm_unique" json:"metric_type"`
	GroupID     uint       `gorm:"not null;uniqueIndex:idx_dwm_unique;index:idx_dwm_group" json:"group_id"`
	SubGroupID  uint       `gorm:"default:0;uniqueIndex:idx_dwm_unique" json:"sub_group_id"`
	SourceModel string     `gorm:"type:varchar(255);default:'';uniqueIndex:idx_dwm_unique" json:"source_model"`
	TargetIndex int        `gorm:"default:0;uniqueIndex:idx_dwm_unique" json:"target_index"`

	// Real-time tracking fields
	ConsecutiveFailures int64      `gorm:"default:0" json:"consecutive_failures"`
	LastFailureAt       *time.Time `json:"last_failure_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`

	// Time-windowed statistics (cumulative, each includes all shorter windows)
	// These are updated in real-time for the current window and periodically rolled over
	Requests7d    int64 `gorm:"default:0" json:"requests_7d"`
	Successes7d   int64 `gorm:"default:0" json:"successes_7d"`
	Requests14d   int64 `gorm:"default:0" json:"requests_14d"`
	Successes14d  int64 `gorm:"default:0" json:"successes_14d"`
	Requests30d   int64 `gorm:"default:0" json:"requests_30d"`
	Successes30d  int64 `gorm:"default:0" json:"successes_30d"`
	Requests90d   int64 `gorm:"default:0" json:"requests_90d"`
	Successes90d  int64 `gorm:"default:0" json:"successes_90d"`
	Requests180d  int64 `gorm:"default:0" json:"requests_180d"`
	Successes180d int64 `gorm:"default:0" json:"successes_180d"`

	// Lifecycle management
	LastRolloverAt *time.Time `json:"last_rollover_at"`
	UpdatedAt      time.Time  `gorm:"autoUpdateTime" json:"updated_at"`

	// Soft delete support: when sub-group is removed from aggregate group,
	// we mark it as deleted but keep the data for potential restoration.
	// Data is permanently deleted after 180 days by cleanup task.
	DeletedAt *time.Time `gorm:"index:idx_dwm_deleted" json:"deleted_at"`
}

// TableName returns the table name for GORM.
func (DynamicWeightMetric) TableName() string {
	return "dynamic_weight_metrics"
}

// IsDeleted returns true if the metric is soft-deleted.
func (m *DynamicWeightMetric) IsDeleted() bool {
	return m.DeletedAt != nil
}

// TimeWindowConfig defines the time windows and their weights for health calculation.
type TimeWindowConfig struct {
	Days   int     // Number of days in this window
	Weight float64 // Weight for this time window (higher = more important)
}

// DefaultTimeWindowConfigs returns the default time window configurations.
// Windows are cumulative: 7d includes 0-7 days, 14d includes 0-14 days, etc.
// Weights decrease for older data to prioritize recent performance.
func DefaultTimeWindowConfigs() []TimeWindowConfig {
	return []TimeWindowConfig{
		{Days: 7, Weight: 1.0},   // 0-7 days: highest weight
		{Days: 14, Weight: 0.8},  // 8-14 days: high weight
		{Days: 30, Weight: 0.6},  // 15-30 days: medium weight
		{Days: 90, Weight: 0.3},  // 31-90 days: low weight
		{Days: 180, Weight: 0.1}, // 91-180 days: lowest weight
	}
}
