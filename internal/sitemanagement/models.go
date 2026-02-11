package sitemanagement

import "time"

const (
	SiteTypeNewAPI     = "new-api"
	SiteTypeVeloera    = "Veloera"
	SiteTypeWongGongyi = "wong-gongyi"
	SiteTypeOneHub     = "one-hub"
	SiteTypeDoneHub    = "done-hub"
	SiteTypeAnyrouter  = "anyrouter"
	SiteTypeBrand      = "brand" // Label-only type, no special checkin logic
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

	CheckInAvailable   bool   `gorm:"column:checkin_available;not null;default:false" json:"checkin_available"`
	CheckInEnabled     bool   `gorm:"column:checkin_enabled;not null;default:false;index:idx_managed_sites_auto,priority:3" json:"checkin_enabled"`
	AutoCheckInEnabled bool   `gorm:"column:auto_checkin_enabled;not null;default:false;index:idx_managed_sites_auto,priority:1" json:"auto_checkin_enabled"`
	CustomCheckInURL   string `gorm:"column:custom_checkin_url;type:varchar(512);not null;default:''" json:"custom_checkin_url"`
	UseProxy           bool   `gorm:"column:use_proxy;not null;default:false" json:"use_proxy"`
	ProxyURL           string `gorm:"column:proxy_url;type:varchar(512);not null;default:''" json:"proxy_url"`

	// BypassMethod specifies the method to bypass WAF/Cloudflare protection.
	// Supported values: "none" (default), "stealth" (TLS fingerprint spoofing)
	// Note: Stealth bypass requires Cookie auth type. CF cookies (cf_clearance, acw_tc, etc.)
	// should be included in AuthValue along with user session cookies.
	BypassMethod string `gorm:"column:bypass_method;type:varchar(32);not null;default:''" json:"bypass_method"`

	// AuthType specifies the authentication method(s) to use for check-in.
	// Supports both single-auth (legacy) and multi-auth (new) formats:
	// - Single-auth: "access_token", "cookie", or "none"
	// - Multi-auth: comma-separated list, e.g., "access_token,cookie"
	// When multiple auth types are specified, check-in will try them in order (access_token first, then cookie).
	// Only one successful authentication is needed for check-in to succeed.
	AuthType string `gorm:"type:varchar(32);not null;default:'none'" json:"auth_type"`

	// AuthValue stores encrypted authentication credentials.
	// Supports both single-auth (legacy) and multi-auth (new) formats:
	// - Single-auth: encrypted single value (e.g., access token or cookie string)
	// - Multi-auth: encrypted JSON string, e.g., {"access_token":"xxx","cookie":"yyy"}
	// The system automatically detects the format and handles backward compatibility.
	AuthValue string `gorm:"type:text;not null;default:''" json:"-"`

	LastCheckInAt      *time.Time `gorm:"column:last_checkin_at" json:"last_checkin_at,omitempty"`
	LastCheckInDate    string     `gorm:"column:last_checkin_date;type:char(10);not null;default:'';index" json:"last_checkin_date"`
	LastCheckInStatus  string     `gorm:"column:last_checkin_status;type:varchar(32);not null;default:''" json:"last_checkin_status"`
	LastCheckInMessage string     `gorm:"column:last_checkin_message;type:text;not null;default:''" json:"last_checkin_message"`

	// Track when user clicked "Open Site" or "Open Check-in Page" buttons.
	// Date format: YYYY-MM-DD in Beijing time (UTC+8), resets at 05:00 Beijing time.
	LastSiteOpenedDate        string `gorm:"column:last_site_opened_date;type:char(10);not null;default:''" json:"last_site_opened_date"`
	LastCheckinPageOpenedDate string `gorm:"column:last_checkin_page_opened_date;type:char(10);not null;default:''" json:"last_checkin_page_opened_date"`

	// Cached balance information, refreshed daily at 05:00 Beijing time.
	// Balance is stored as display string (e.g., "$10.50") or empty if not available.
	LastBalance     string `gorm:"column:last_balance;type:varchar(32);not null;default:''" json:"last_balance"`
	LastBalanceDate string `gorm:"column:last_balance_date;type:char(10);not null;default:''" json:"last_balance_date"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	BoundGroupID *uint `gorm:"index" json:"bound_group_id"` // Bound group ID for site-group binding
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
