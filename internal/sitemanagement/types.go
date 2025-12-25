package sitemanagement

import "time"

type ManagedSiteDTO struct {
	ID uint `json:"id"`

	Name        string `json:"name"`
	Notes       string `json:"notes"`
	Description string `json:"description"`
	Sort        int    `json:"sort"`
	Enabled     bool   `json:"enabled"`

	BaseURL        string `json:"base_url"`
	SiteType       string `json:"site_type"`
	UserID         string `json:"user_id"`
	CheckInPageURL string `json:"checkin_page_url"`

	CheckInEnabled     bool   `json:"checkin_enabled"`
	AutoCheckInEnabled bool   `json:"auto_checkin_enabled"`
	CustomCheckInURL   string `json:"custom_checkin_url"`

	AuthType string `json:"auth_type"`
	HasAuth  bool   `json:"has_auth"`

	LastCheckInAt      *time.Time `json:"last_checkin_at,omitempty"`
	LastCheckInDate    string     `json:"last_checkin_date"`
	LastCheckInStatus  string     `json:"last_checkin_status"`
	LastCheckInMessage string     `json:"last_checkin_message"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AutoCheckinRetryStrategy struct {
	Enabled           bool `json:"enabled"`
	IntervalMinutes   int  `json:"interval_minutes"`
	MaxAttemptsPerDay int  `json:"max_attempts_per_day"`
}

const (
	AutoCheckinScheduleModeRandom        = "random"
	AutoCheckinScheduleModeDeterministic = "deterministic"
)

type AutoCheckinConfig struct {
	GlobalEnabled     bool                     `json:"global_enabled"`
	WindowStart       string                   `json:"window_start"`
	WindowEnd         string                   `json:"window_end"`
	ScheduleMode      string                   `json:"schedule_mode"`
	DeterministicTime string                   `json:"deterministic_time,omitempty"`
	RetryStrategy     AutoCheckinRetryStrategy `json:"retry_strategy"`
}

type AutoCheckinAttemptsTracker struct {
	Date     string `json:"date"`
	Attempts int    `json:"attempts"`
}

type AutoCheckinRunSummary struct {
	TotalEligible int  `json:"total_eligible"`
	Executed      int  `json:"executed"`
	SuccessCount  int  `json:"success_count"`
	FailedCount   int  `json:"failed_count"`
	SkippedCount  int  `json:"skipped_count"`
	NeedsRetry    bool `json:"needs_retry"`
}

const (
	AutoCheckinRunResultSuccess = "success"
	AutoCheckinRunResultPartial = "partial"
	AutoCheckinRunResultFailed  = "failed"
)

type AutoCheckinStatus struct {
	IsRunning       bool                        `json:"is_running"`
	LastRunAt       string                      `json:"last_run_at,omitempty"`
	LastRunResult   string                      `json:"last_run_result,omitempty"`
	NextScheduledAt string                      `json:"next_scheduled_at,omitempty"`
	Summary         *AutoCheckinRunSummary      `json:"summary,omitempty"`
	Attempts        *AutoCheckinAttemptsTracker `json:"attempts,omitempty"`
	PendingRetry    bool                        `json:"pending_retry"`
}
