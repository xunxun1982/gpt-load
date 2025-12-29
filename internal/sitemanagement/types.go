package sitemanagement

import "time"

// SiteListParams defines pagination and filter parameters for site listing
type SiteListParams struct {
	Page             int    // Page number (1-based)
	PageSize         int    // Items per page (default 50, max 200)
	Search           string // Optional search term for name/notes/description/base_url
	Enabled          *bool  // Optional filter by enabled status
	CheckinAvailable *bool  // Optional filter by checkin_available status
}

// SiteListResult contains paginated site list with metadata
type SiteListResult struct {
	Sites      []ManagedSiteDTO `json:"sites"`
	Total      int64            `json:"total"`
	Page       int              `json:"page"`
	PageSize   int              `json:"page_size"`
	TotalPages int              `json:"total_pages"`
}

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

	CheckInAvailable   bool   `json:"checkin_available"`
	CheckInEnabled     bool   `json:"checkin_enabled"`
	AutoCheckInEnabled bool   `json:"auto_checkin_enabled"`
	CustomCheckInURL   string `json:"custom_checkin_url"`

	AuthType string `json:"auth_type"`
	HasAuth  bool   `json:"has_auth"`

	LastCheckInAt      *time.Time `json:"last_checkin_at,omitempty"`
	LastCheckInDate    string     `json:"last_checkin_date"`
	LastCheckInStatus  string     `json:"last_checkin_status"`
	LastCheckInMessage string     `json:"last_checkin_message"`

	// Track when user clicked "Open Site" or "Open Check-in Page" buttons.
	// Date format: YYYY-MM-DD in Beijing time (UTC+8), resets at 05:00 Beijing time.
	LastSiteOpenedDate        string `json:"last_site_opened_date"`
	LastCheckinPageOpenedDate string `json:"last_checkin_page_opened_date"`

	BoundGroupID   *uint  `json:"bound_group_id,omitempty"`
	BoundGroupName string `json:"bound_group_name,omitempty"`

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

// AutoCheckinConfig holds the auto check-in scheduling configuration.
type AutoCheckinConfig struct {
	GlobalEnabled bool `json:"global_enabled"`
	// WindowStart is the start time in "HH:MM" format (24-hour, local time).
	WindowStart string `json:"window_start"`
	// WindowEnd is the end time in "HH:MM" format (24-hour, local time).
	WindowEnd    string `json:"window_end"`
	ScheduleMode string `json:"schedule_mode"`
	// DeterministicTime is the fixed check-in time in "HH:MM" format when ScheduleMode is "deterministic".
	DeterministicTime string                   `json:"deterministic_time,omitempty"`
	RetryStrategy     AutoCheckinRetryStrategy `json:"retry_strategy"`
}

// AutoCheckinAttemptsTracker tracks daily check-in attempts.
type AutoCheckinAttemptsTracker struct {
	// Date is in "YYYY-MM-DD" format (local time).
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
