package sitemanagement

import "time"

const (
	SiteTypeNewAPI     = "new-api"
	SiteTypeVeloera    = "Veloera"
	SiteTypeAnyrouter  = "anyrouter"
	SiteTypeWongGongyi = "wong-gongyi"
	SiteTypeUnknown    = "unknown"
)

const (
	AuthTypeAccessToken = "access_token"
	AuthTypeCookie      = "cookie"
	AuthTypeNone        = "none"
)

const (
	CheckinResultSuccess        = "success"
	CheckinResultAlreadyChecked = "already_checked"
	CheckinResultFailed         = "failed"
	CheckinResultSkipped        = "skipped"
)

// ManagedSite stores a single site/account entry that can be checked in.
//
// Notes on schema design:
// - Keep columns simple (varchar/text/bool/int) for cross-database compatibility.
// - Avoid JSON columns here to keep queries/indexing predictable across SQLite/MySQL/Postgres.
// - Sensitive credentials are stored encrypted in AuthValue and never returned to the client.
type ManagedSite struct {
	ID uint `gorm:"primaryKey;autoIncrement" json:"id"`

	Name        string `gorm:"type:varchar(255);not null" json:"name"`
	Notes       string `gorm:"type:varchar(255);not null;default:''" json:"notes"`
	Description string `gorm:"type:varchar(1024);not null;default:''" json:"description"`
	Sort        int    `gorm:"not null;default:0" json:"sort"`
	Enabled     bool   `gorm:"not null;default:true;index:idx_managed_sites_auto,priority:2" json:"enabled"`

	BaseURL        string `gorm:"type:varchar(512);not null;index" json:"base_url"`
	SiteType       string `gorm:"type:varchar(64);not null;default:'unknown';index" json:"site_type"`
	UserID         string `gorm:"type:varchar(64);not null;default:''" json:"user_id"`
	CheckInPageURL string `gorm:"column:checkin_page_url;type:varchar(512);not null;default:''" json:"checkin_page_url"`

	CheckInEnabled     bool   `gorm:"column:checkin_enabled;not null;default:false;index:idx_managed_sites_auto,priority:3" json:"checkin_enabled"`
	AutoCheckInEnabled bool   `gorm:"column:auto_checkin_enabled;not null;default:false;index:idx_managed_sites_auto,priority:1" json:"auto_checkin_enabled"`
	CustomCheckInURL   string `gorm:"column:custom_checkin_url;type:varchar(512);not null;default:''" json:"custom_checkin_url"`

	AuthType  string `gorm:"type:varchar(32);not null;default:'none'" json:"auth_type"`
	AuthValue string `gorm:"type:text;not null;default:''" json:"-"`

	LastCheckInAt      *time.Time `gorm:"column:last_checkin_at" json:"last_checkin_at,omitempty"`
	LastCheckInDate    string     `gorm:"column:last_checkin_date;type:char(10);not null;default:'';index" json:"last_checkin_date"`
	LastCheckInStatus  string     `gorm:"column:last_checkin_status;type:varchar(32);not null;default:''" json:"last_checkin_status"`
	LastCheckInMessage string     `gorm:"column:last_checkin_message;type:text;not null;default:''" json:"last_checkin_message"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ManagedSiteCheckinLog is an append-only check-in audit log.
// Keep it lightweight to avoid impacting primary workloads.
type ManagedSiteCheckinLog struct {
	ID     uint `gorm:"primaryKey;autoIncrement" json:"id"`
	SiteID uint `gorm:"not null;index:idx_site_time,priority:1" json:"site_id"`

	Status  string `gorm:"type:varchar(32);not null" json:"status"`
	Message string `gorm:"type:text;not null;default:''" json:"message"`

	CreatedAt time.Time `gorm:"index:idx_site_time,priority:2" json:"created_at"`
}
