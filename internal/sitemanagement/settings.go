package sitemanagement

import "time"

// ManagedSiteSetting stores global auto check-in configuration.
//
// This table is expected to contain exactly one row (ID = 1).
// The explicit columns keep migrations simple and portable across SQLite/MySQL/Postgres.
type ManagedSiteSetting struct {
	ID uint `gorm:"primaryKey;autoIncrement" json:"id"`

	AutoCheckinEnabled bool `gorm:"not null;default:false" json:"auto_checkin_enabled"`

	// ScheduleTimes stores multiple check-in times in "HH:MM" format, comma-separated.
	// Example: "09:00,12:00,18:00" for three daily check-ins.
	// All times are in Beijing time (UTC+8).
	ScheduleTimes string `gorm:"type:varchar(255);not null;default:'09:00'" json:"schedule_times"`

	// Legacy fields kept for backward compatibility during migration
	WindowStart       string `gorm:"type:char(5);not null;default:'09:00'" json:"window_start"`
	WindowEnd         string `gorm:"type:char(5);not null;default:'18:00'" json:"window_end"`
	ScheduleMode      string `gorm:"type:varchar(32);not null;default:'random'" json:"schedule_mode"`
	DeterministicTime string `gorm:"type:char(5);not null;default:''" json:"deterministic_time"`

	RetryEnabled           bool `gorm:"not null;default:false" json:"retry_enabled"`
	RetryIntervalMinutes   int  `gorm:"not null;default:60" json:"retry_interval_minutes"`
	RetryMaxAttemptsPerDay int  `gorm:"not null;default:2" json:"retry_max_attempts_per_day"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
