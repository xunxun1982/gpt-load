package sitemanagement

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/services"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const autoCheckinConfigUpdatedChannel = "managed_site:auto_checkin_config_updated"

type SiteService struct {
	db            *gorm.DB
	store         store.Store
	encryptionSvc encryption.Service
}

func NewSiteService(db *gorm.DB, store store.Store, encryptionSvc encryption.Service) *SiteService {
	return &SiteService{db: db, store: store, encryptionSvc: encryptionSvc}
}

type CreateSiteParams struct {
	Name        string
	Notes       string
	Description string
	Sort        int
	Enabled     bool

	BaseURL        string
	SiteType       string
	UserID         string
	CheckInPageURL string

	CheckInEnabled     bool
	AutoCheckInEnabled bool
	CustomCheckInURL   string

	AuthType  string
	AuthValue string
}

type UpdateSiteParams struct {
	Name        *string
	Notes       *string
	Description *string
	Sort        *int
	Enabled     *bool

	BaseURL        *string
	SiteType       *string
	UserID         *string
	CheckInPageURL *string

	CheckInEnabled     *bool
	AutoCheckInEnabled *bool
	CustomCheckInURL   *string

	AuthType  *string
	AuthValue *string
}

func (s *SiteService) ListSites(ctx context.Context) ([]ManagedSiteDTO, error) {
	var sites []ManagedSite
	if err := s.db.WithContext(ctx).
		Order("sort ASC, id ASC").
		Find(&sites).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	resp := make([]ManagedSiteDTO, 0, len(sites))
	for i := range sites {
		site := &sites[i]
		resp = append(resp, ManagedSiteDTO{
			ID:                 site.ID,
			Name:               site.Name,
			Notes:              site.Notes,
			Description:        site.Description,
			Sort:               site.Sort,
			Enabled:            site.Enabled,
			BaseURL:            site.BaseURL,
			SiteType:           site.SiteType,
			UserID:             site.UserID,
			CheckInPageURL:     site.CheckInPageURL,
			CheckInEnabled:     site.CheckInEnabled,
			AutoCheckInEnabled: site.AutoCheckInEnabled,
			CustomCheckInURL:   site.CustomCheckInURL,
			AuthType:           site.AuthType,
			HasAuth:            strings.TrimSpace(site.AuthValue) != "",
			LastCheckInAt:      site.LastCheckInAt,
			LastCheckInDate:    site.LastCheckInDate,
			LastCheckInStatus:  site.LastCheckInStatus,
			LastCheckInMessage: site.LastCheckInMessage,
			CreatedAt:          site.CreatedAt,
			UpdatedAt:          site.UpdatedAt,
		})
	}
	return resp, nil
}

func (s *SiteService) CreateSite(ctx context.Context, params CreateSiteParams) (*ManagedSiteDTO, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.name_required", nil)
	}

	// Check for duplicate name
	var count int64
	if err := s.db.WithContext(ctx).Model(&ManagedSite{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	if count > 0 {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.name_duplicate", map[string]any{"name": name})
	}

	baseURL, err := normalizeBaseURL(params.BaseURL)
	if err != nil {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_base_url", map[string]any{"error": err.Error()})
	}

	siteType := strings.TrimSpace(params.SiteType)
	if siteType == "" {
		siteType = SiteTypeUnknown
	}

	authType := normalizeAuthType(params.AuthType)
	if authType == "" {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_auth_type", nil)
	}

	checkInEnabled := params.CheckInEnabled
	autoCheckInEnabled := params.AutoCheckInEnabled
	if autoCheckInEnabled {
		checkInEnabled = true
	}
	if !checkInEnabled {
		autoCheckInEnabled = false
	}

	encryptedAuth := ""
	if authType != AuthTypeNone {
		value := strings.TrimSpace(params.AuthValue)
		if value != "" {
			encryptedAuth, err = s.encryptionSvc.Encrypt(value)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt auth value: %w", err)
			}
		}
	}

	site := &ManagedSite{
		Name:               name,
		Notes:              strings.TrimSpace(params.Notes),
		Description:        strings.TrimSpace(params.Description),
		Sort:               params.Sort,
		Enabled:            params.Enabled,
		BaseURL:            baseURL,
		SiteType:           siteType,
		UserID:             strings.TrimSpace(params.UserID),
		CheckInPageURL:     strings.TrimSpace(params.CheckInPageURL),
		CheckInEnabled:     checkInEnabled,
		AutoCheckInEnabled: autoCheckInEnabled,
		CustomCheckInURL:   strings.TrimSpace(params.CustomCheckInURL),
		AuthType:           authType,
		AuthValue:          encryptedAuth,
	}

	if err := s.db.WithContext(ctx).Create(site).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	return s.toDTO(site), nil
}

func (s *SiteService) UpdateSite(ctx context.Context, siteID uint, params UpdateSiteParams) (*ManagedSiteDTO, error) {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if params.Name != nil {
		name := strings.TrimSpace(*params.Name)
		if name == "" {
			return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.name_required", nil)
		}
		// Check for duplicate name (exclude current site)
		if name != site.Name {
			var count int64
			if err := s.db.WithContext(ctx).Model(&ManagedSite{}).Where("name = ? AND id != ?", name, siteID).Count(&count).Error; err != nil {
				return nil, app_errors.ParseDBError(err)
			}
			if count > 0 {
				return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.name_duplicate", map[string]any{"name": name})
			}
		}
		site.Name = name
	}
	if params.Notes != nil {
		site.Notes = strings.TrimSpace(*params.Notes)
	}
	if params.Description != nil {
		site.Description = strings.TrimSpace(*params.Description)
	}
	if params.Sort != nil {
		site.Sort = *params.Sort
	}
	if params.Enabled != nil {
		site.Enabled = *params.Enabled
	}
	if params.BaseURL != nil {
		baseURL, err := normalizeBaseURL(*params.BaseURL)
		if err != nil {
			return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_base_url", map[string]any{"error": err.Error()})
		}
		site.BaseURL = baseURL
	}
	if params.SiteType != nil {
		st := strings.TrimSpace(*params.SiteType)
		if st == "" {
			st = SiteTypeUnknown
		}
		site.SiteType = st
	}
	if params.UserID != nil {
		site.UserID = strings.TrimSpace(*params.UserID)
	}
	if params.CheckInPageURL != nil {
		site.CheckInPageURL = strings.TrimSpace(*params.CheckInPageURL)
	}
	if params.CustomCheckInURL != nil {
		site.CustomCheckInURL = strings.TrimSpace(*params.CustomCheckInURL)
	}
	if params.AuthType != nil {
		authType := normalizeAuthType(*params.AuthType)
		if authType == "" {
			return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_auth_type", nil)
		}
		// Update AuthType first - subsequent AuthValue check depends on this value
		site.AuthType = authType
		if authType == AuthTypeNone {
			site.AuthValue = ""
		}
	}
	if params.AuthValue != nil {
		value := strings.TrimSpace(*params.AuthValue)
		if value == "" {
			site.AuthValue = ""
		} else {
			// Check uses already-updated AuthType from above (intentional order dependency)
			if site.AuthType == AuthTypeNone {
				return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.auth_value_requires_auth_type", nil)
			}
			enc, err := s.encryptionSvc.Encrypt(value)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt auth value: %w", err)
			}
			site.AuthValue = enc
		}
	}

	if params.CheckInEnabled != nil {
		site.CheckInEnabled = *params.CheckInEnabled
		if !site.CheckInEnabled {
			site.AutoCheckInEnabled = false
		}
	}
	if params.AutoCheckInEnabled != nil {
		site.AutoCheckInEnabled = *params.AutoCheckInEnabled
		if site.AutoCheckInEnabled {
			site.CheckInEnabled = true
		}
	}

	if err := s.db.WithContext(ctx).Save(&site).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	return s.toDTO(&site), nil
}

func (s *SiteService) DeleteSite(ctx context.Context, siteID uint) error {
	// Best-effort cascade delete logs (fast because of idx_site_time).
	// Avoid hard FK constraints to keep migrations portable across databases.
	if err := s.db.WithContext(ctx).Where("site_id = ?", siteID).Delete(&ManagedSiteCheckinLog{}).Error; err != nil {
		if parsed := app_errors.ParseDBError(err); parsed != nil {
			return parsed
		}
		return err
	}

	if err := s.db.WithContext(ctx).Delete(&ManagedSite{}, siteID).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	return nil
}

func (s *SiteService) GetAutoCheckinConfig(ctx context.Context) (*AutoCheckinConfig, error) {
	st, err := s.ensureSettingsRow(ctx)
	if err != nil {
		return nil, err
	}

	return &AutoCheckinConfig{
		GlobalEnabled:     st.AutoCheckinEnabled,
		WindowStart:       st.WindowStart,
		WindowEnd:         st.WindowEnd,
		ScheduleMode:      st.ScheduleMode,
		DeterministicTime: st.DeterministicTime,
		RetryStrategy: AutoCheckinRetryStrategy{
			Enabled:           st.RetryEnabled,
			IntervalMinutes:   st.RetryIntervalMinutes,
			MaxAttemptsPerDay: st.RetryMaxAttemptsPerDay,
		},
	}, nil
}

func (s *SiteService) UpdateAutoCheckinConfig(ctx context.Context, cfg AutoCheckinConfig) (*AutoCheckinConfig, error) {
	if cfg.WindowStart == "" || cfg.WindowEnd == "" {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.time_window_required", nil)
	}
	if _, err := parseTimeToMinutes(cfg.WindowStart); err != nil {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_time", map[string]any{"field": "window_start"})
	}
	if _, err := parseTimeToMinutes(cfg.WindowEnd); err != nil {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_time", map[string]any{"field": "window_end"})
	}

	mode := strings.TrimSpace(cfg.ScheduleMode)
	if mode == "" {
		mode = AutoCheckinScheduleModeRandom
	}
	if mode != AutoCheckinScheduleModeRandom && mode != AutoCheckinScheduleModeDeterministic {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_schedule_mode", nil)
	}
	if mode == AutoCheckinScheduleModeDeterministic {
		if cfg.DeterministicTime == "" {
			return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.deterministic_time_required", nil)
		}
		if _, err := parseTimeToMinutes(cfg.DeterministicTime); err != nil {
			return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_time", map[string]any{"field": "deterministic_time"})
		}
	}

	st, err := s.ensureSettingsRow(ctx)
	if err != nil {
		return nil, err
	}

	st.AutoCheckinEnabled = cfg.GlobalEnabled
	st.WindowStart = cfg.WindowStart
	st.WindowEnd = cfg.WindowEnd
	st.ScheduleMode = mode
	st.DeterministicTime = strings.TrimSpace(cfg.DeterministicTime)
	st.RetryEnabled = cfg.RetryStrategy.Enabled
	st.RetryIntervalMinutes = clampInt(cfg.RetryStrategy.IntervalMinutes, 1, 24*60)
	st.RetryMaxAttemptsPerDay = clampInt(cfg.RetryStrategy.MaxAttemptsPerDay, 1, 10)
	st.UpdatedAt = time.Now()

	if err := s.db.WithContext(ctx).Save(st).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.publishAutoCheckinConfigUpdated()

	return s.GetAutoCheckinConfig(ctx)
}

func (s *SiteService) publishAutoCheckinConfigUpdated() {
	if s.store == nil {
		return
	}
	if err := s.store.Publish(autoCheckinConfigUpdatedChannel, []byte("1")); err != nil {
		logrus.WithError(err).Debug("managed site auto-checkin config publish failed")
	}
}

func (s *SiteService) ensureSettingsRow(ctx context.Context) (*ManagedSiteSetting, error) {
	var st ManagedSiteSetting
	err := s.db.WithContext(ctx).First(&st, 1).Error
	if err == nil {
		return &st, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, app_errors.ParseDBError(err)
	}

	st = ManagedSiteSetting{
		ID:                     1,
		AutoCheckinEnabled:     false,
		WindowStart:            "09:00",
		WindowEnd:              "18:00",
		ScheduleMode:           AutoCheckinScheduleModeRandom,
		DeterministicTime:      "",
		RetryEnabled:           false,
		RetryIntervalMinutes:   60,
		RetryMaxAttemptsPerDay: 2,
	}
	if createErr := s.db.WithContext(ctx).Create(&st).Error; createErr != nil {
		return nil, app_errors.ParseDBError(createErr)
	}
	return &st, nil
}

func (s *SiteService) toDTO(site *ManagedSite) *ManagedSiteDTO {
	if site == nil {
		return nil
	}
	return &ManagedSiteDTO{
		ID:                 site.ID,
		Name:               site.Name,
		Notes:              site.Notes,
		Description:        site.Description,
		Sort:               site.Sort,
		Enabled:            site.Enabled,
		BaseURL:            site.BaseURL,
		SiteType:           site.SiteType,
		UserID:             site.UserID,
		CheckInPageURL:     site.CheckInPageURL,
		CheckInEnabled:     site.CheckInEnabled,
		AutoCheckInEnabled: site.AutoCheckInEnabled,
		CustomCheckInURL:   site.CustomCheckInURL,
		AuthType:           site.AuthType,
		HasAuth:            strings.TrimSpace(site.AuthValue) != "",
		LastCheckInAt:      site.LastCheckInAt,
		LastCheckInDate:    site.LastCheckInDate,
		LastCheckInStatus:  site.LastCheckInStatus,
		LastCheckInMessage: site.LastCheckInMessage,
		CreatedAt:          site.CreatedAt,
		UpdatedAt:          site.UpdatedAt,
	}
}

func normalizeAuthType(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")

	switch s {
	case strings.ToLower(AuthTypeAccessToken):
		return AuthTypeAccessToken
	case strings.ToLower(AuthTypeCookie):
		return AuthTypeCookie
	case strings.ToLower(AuthTypeNone), "":
		return AuthTypeNone
	default:
		return ""
	}
}

func normalizeBaseURL(raw string) (string, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimRight(clean, "/")
	if clean == "" {
		return "", fmt.Errorf("empty")
	}
	u, err := url.Parse(clean)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme")
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	return clean, nil
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// generateUniqueSiteName generates a unique site name by appending a random suffix if needed.
// Note: This logic is similar to GenerateUniqueGroupName in import_export_service.go,
// but kept separate to avoid coupling between site and group management modules.
func (s *SiteService) generateUniqueSiteName(ctx context.Context, baseName string) (string, error) {
	siteName := baseName
	maxAttempts := 10

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Check if this name already exists
		var count int64
		if err := s.db.WithContext(ctx).Model(&ManagedSite{}).Where("name = ?", siteName).Count(&count).Error; err != nil {
			return "", fmt.Errorf("failed to check site name: %w", err)
		}

		// If name is unique, we're done
		if count == 0 {
			return siteName, nil
		}

		// Generate a new name with random suffix for next attempt
		if attempt < maxAttempts-1 {
			// Ensure the name doesn't exceed database limits
			if len(baseName)+4 > 100 {
				baseName = baseName[:96]
			}
			// Append random suffix using shared utility (4 chars)
			siteName = baseName + utils.GenerateRandomSuffix()
		} else {
			return "", fmt.Errorf("failed to generate unique site name for %s after %d attempts", baseName, maxAttempts)
		}
	}

	return siteName, nil
}

// SiteExportInfo represents exported site information (without sensitive data in plain mode)
type SiteExportInfo struct {
	Name               string `json:"name"`
	Notes              string `json:"notes"`
	Description        string `json:"description"`
	Sort               int    `json:"sort"`
	Enabled            bool   `json:"enabled"`
	BaseURL            string `json:"base_url"`
	SiteType           string `json:"site_type"`
	UserID             string `json:"user_id"`
	CheckInPageURL     string `json:"checkin_page_url"`
	CheckInEnabled     bool   `json:"checkin_enabled"`
	AutoCheckInEnabled bool   `json:"auto_checkin_enabled"`
	CustomCheckInURL   string `json:"custom_checkin_url"`
	AuthType           string `json:"auth_type"`
	AuthValue          string `json:"auth_value,omitempty"` // Encrypted or plain based on export mode
}

// SiteExportData represents the complete export data structure
type SiteExportData struct {
	Version        string                `json:"version"`
	ExportedAt     string                `json:"exported_at"`
	AutoCheckin    *AutoCheckinConfig    `json:"auto_checkin,omitempty"`
	Sites          []SiteExportInfo      `json:"sites"`
}

// ExportSites exports all managed sites with optional auto-checkin config
func (s *SiteService) ExportSites(ctx context.Context, includeConfig bool, plainMode bool) (*SiteExportData, error) {
	var sites []ManagedSite
	if err := s.db.WithContext(ctx).Order("sort ASC, id ASC").Find(&sites).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	exportData := &SiteExportData{
		Version:    "1.0",
		ExportedAt: time.Now().Format(time.RFC3339),
		Sites:      make([]SiteExportInfo, 0, len(sites)),
	}

	// Export auto-checkin config if requested
	if includeConfig {
		cfg, err := s.GetAutoCheckinConfig(ctx)
		if err != nil {
			// Log error but continue export without config
			logrus.WithError(err).Debug("Failed to get auto-checkin config for export")
		} else {
			exportData.AutoCheckin = cfg
		}
	}

	// Export sites
	for _, site := range sites {
		siteInfo := SiteExportInfo{
			Name:               site.Name,
			Notes:              site.Notes,
			Description:        site.Description,
			Sort:               site.Sort,
			Enabled:            site.Enabled,
			BaseURL:            site.BaseURL,
			SiteType:           site.SiteType,
			UserID:             site.UserID,
			CheckInPageURL:     site.CheckInPageURL,
			CheckInEnabled:     site.CheckInEnabled,
			AutoCheckInEnabled: site.AutoCheckInEnabled,
			CustomCheckInURL:   site.CustomCheckInURL,
			AuthType:           site.AuthType,
		}

		// Handle auth value based on export mode
		if site.AuthValue != "" {
			if plainMode {
				// Decrypt for plain export
				if decrypted, err := s.encryptionSvc.Decrypt(site.AuthValue); err == nil {
					siteInfo.AuthValue = decrypted
				}
			} else {
				// Keep encrypted for encrypted export
				siteInfo.AuthValue = site.AuthValue
			}
		}

		exportData.Sites = append(exportData.Sites, siteInfo)
	}

	return exportData, nil
}

// ImportSites imports sites from export data.
// Note: Intentionally not using transaction wrapping - partial success is desired behavior
// to import as many sites as possible even if some fail validation.
func (s *SiteService) ImportSites(ctx context.Context, data *SiteExportData, plainMode bool) (int, int, error) {
	if data == nil || len(data.Sites) == 0 {
		return 0, 0, nil
	}

	imported := 0
	skipped := 0

	for _, siteInfo := range data.Sites {
		// Validate required fields
		name := strings.TrimSpace(siteInfo.Name)
		if name == "" {
			skipped++
			continue
		}

		baseURL, err := normalizeBaseURL(siteInfo.BaseURL)
		if err != nil {
			logrus.WithError(err).Warnf("Skipping site %s: invalid base_url", name)
			skipped++
			continue
		}

		siteType := strings.TrimSpace(siteInfo.SiteType)
		if siteType == "" {
			siteType = SiteTypeUnknown
		}

		authType := normalizeAuthType(siteInfo.AuthType)
		if authType == "" {
			authType = AuthTypeNone
		}

		// Handle auth value encryption
		encryptedAuth := ""
		if authType != AuthTypeNone && siteInfo.AuthValue != "" {
			if plainMode {
				// Input is plain, need to encrypt
				enc, err := s.encryptionSvc.Encrypt(siteInfo.AuthValue)
				if err != nil {
					logrus.WithError(err).Warnf("Failed to encrypt auth value for site %s", name)
					skipped++
					continue
				}
				encryptedAuth = enc
			} else {
				// Input is already encrypted, verify it can be decrypted
				if _, err := s.encryptionSvc.Decrypt(siteInfo.AuthValue); err != nil {
					logrus.WithError(err).Warnf("Failed to decrypt auth value for site %s, skipping", name)
					skipped++
					continue
				}
				encryptedAuth = siteInfo.AuthValue
			}
		}

		// Ensure checkin flags are consistent
		checkInEnabled := siteInfo.CheckInEnabled
		autoCheckInEnabled := siteInfo.AutoCheckInEnabled
		if autoCheckInEnabled {
			checkInEnabled = true
		}
		if !checkInEnabled {
			autoCheckInEnabled = false
		}

		// Generate unique name if conflict exists
		uniqueName, err := s.generateUniqueSiteName(ctx, name)
		if err != nil {
			logrus.WithError(err).Warnf("Failed to generate unique name for site %s", name)
			skipped++
			continue
		}

		site := &ManagedSite{
			Name:               uniqueName,
			Notes:              strings.TrimSpace(siteInfo.Notes),
			Description:        strings.TrimSpace(siteInfo.Description),
			Sort:               siteInfo.Sort,
			Enabled:            siteInfo.Enabled,
			BaseURL:            baseURL,
			SiteType:           siteType,
			UserID:             strings.TrimSpace(siteInfo.UserID),
			CheckInPageURL:     strings.TrimSpace(siteInfo.CheckInPageURL),
			CheckInEnabled:     checkInEnabled,
			AutoCheckInEnabled: autoCheckInEnabled,
			CustomCheckInURL:   strings.TrimSpace(siteInfo.CustomCheckInURL),
			AuthType:           authType,
			AuthValue:          encryptedAuth,
		}

		if err := s.db.WithContext(ctx).Create(site).Error; err != nil {
			logrus.WithError(err).Warnf("Failed to create site %s", uniqueName)
			skipped++
			continue
		}

		if uniqueName != name {
			logrus.Infof("Imported site %s (renamed from %s)", uniqueName, name)
		}
		imported++
	}

	// Import auto-checkin config if present
	if data.AutoCheckin != nil {
		if _, err := s.UpdateAutoCheckinConfig(ctx, *data.AutoCheckin); err != nil {
			logrus.WithError(err).Warn("Failed to import auto-checkin config")
		}
	}

	return imported, skipped, nil
}
