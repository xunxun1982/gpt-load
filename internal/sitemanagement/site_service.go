package sitemanagement

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"gpt-load/internal/encryption"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
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

	// Site list cache for non-paginated requests
	siteListCache    *siteListCacheEntry
	siteListCacheMu  sync.RWMutex
	siteListCacheTTL time.Duration

	// Callback for syncing enabled status to bound group (set by handler layer)
	SyncSiteEnabledToGroupCallback func(ctx context.Context, siteID uint, enabled bool) error
}

// siteListCacheEntry holds cached site list data
type siteListCacheEntry struct {
	Sites     []ManagedSiteDTO
	ExpiresAt time.Time
}

func NewSiteService(db *gorm.DB, store store.Store, encryptionSvc encryption.Service) *SiteService {
	return &SiteService{
		db:               db,
		store:            store,
		encryptionSvc:    encryptionSvc,
		siteListCacheTTL: 30 * time.Second, // Short TTL for freshness
	}
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

	CheckInAvailable   bool
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

	CheckInAvailable   *bool
	CheckInEnabled     *bool
	AutoCheckInEnabled *bool
	CustomCheckInURL   *string

	AuthType  *string
	AuthValue *string
}

func (s *SiteService) ListSites(ctx context.Context) ([]ManagedSiteDTO, error) {
	// Check cache first
	s.siteListCacheMu.RLock()
	if s.siteListCache != nil && time.Now().Before(s.siteListCache.ExpiresAt) {
		sites := s.siteListCache.Sites
		s.siteListCacheMu.RUnlock()
		return sites, nil
	}
	s.siteListCacheMu.RUnlock()

	// Cache miss - fetch from database
	var sites []ManagedSite
	if err := s.db.WithContext(ctx).
		Order("sort ASC, id ASC").
		Find(&sites).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	dtos, err := s.convertSitesToDTOs(ctx, sites)
	if err != nil {
		return nil, err
	}

	// Update cache
	s.siteListCacheMu.Lock()
	s.siteListCache = &siteListCacheEntry{
		Sites:     dtos,
		ExpiresAt: time.Now().Add(s.siteListCacheTTL),
	}
	s.siteListCacheMu.Unlock()

	return dtos, nil
}

// ListSitesPaginated returns paginated site list with optional filters
func (s *SiteService) ListSitesPaginated(ctx context.Context, params SiteListParams) (*SiteListResult, error) {
	// Validate and normalize pagination params
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PageSize < 1 {
		params.PageSize = 50
	}
	if params.PageSize > 200 {
		params.PageSize = 200
	}

	// Build base query
	query := s.db.WithContext(ctx).Model(&ManagedSite{})

	// Apply filters
	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		query = query.Where(
			"name LIKE ? OR notes LIKE ? OR description LIKE ? OR base_url LIKE ?",
			searchPattern, searchPattern, searchPattern, searchPattern,
		)
	}
	if params.Enabled != nil {
		query = query.Where("enabled = ?", *params.Enabled)
	}
	if params.CheckinAvailable != nil {
		query = query.Where("checkin_available = ?", *params.CheckinAvailable)
	}

	// Get total count
	var total int64
	countQuery := query.Session(&gorm.Session{})
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Calculate pagination
	offset := (params.Page - 1) * params.PageSize
	totalPages := int((total + int64(params.PageSize) - 1) / int64(params.PageSize))

	// Fetch paginated data
	var sites []ManagedSite
	if err := query.
		Order("sort ASC, id ASC").
		Offset(offset).
		Limit(params.PageSize).
		Find(&sites).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Batch convert to DTOs
	dtos, err := s.convertSitesToDTOs(ctx, sites)
	if err != nil {
		return nil, err
	}

	return &SiteListResult{
		Sites:      dtos,
		Total:      total,
		Page:       params.Page,
		PageSize:   params.PageSize,
		TotalPages: totalPages,
	}, nil
}

// convertSitesToDTOs converts sites to DTOs with batch operations for group names
func (s *SiteService) convertSitesToDTOs(ctx context.Context, sites []ManagedSite) ([]ManagedSiteDTO, error) {
	if len(sites) == 0 {
		return []ManagedSiteDTO{}, nil
	}

	// Collect bound group IDs for batch query
	boundGroupIDs := make([]uint, 0, len(sites))
	for _, site := range sites {
		if site.BoundGroupID != nil {
			boundGroupIDs = append(boundGroupIDs, *site.BoundGroupID)
		}
	}

	// Batch fetch group names
	groupNameMap := make(map[uint]string)
	if len(boundGroupIDs) > 0 {
		var groups []models.Group
		if err := s.db.WithContext(ctx).Select("id", "name").Where("id IN ?", boundGroupIDs).Find(&groups).Error; err == nil {
			for _, g := range groups {
				groupNameMap[g.ID] = g.Name
			}
		}
	}

	// Convert to DTOs
	dtos := make([]ManagedSiteDTO, 0, len(sites))
	for i := range sites {
		dtos = append(dtos, s.siteToDTO(&sites[i], groupNameMap))
	}

	return dtos, nil
}

// siteToDTO converts a single site to DTO using pre-fetched group name map
func (s *SiteService) siteToDTO(site *ManagedSite, groupNameMap map[uint]string) ManagedSiteDTO {
	// Decrypt user_id for display
	userID := site.UserID
	if userID != "" {
		if decrypted, err := s.encryptionSvc.Decrypt(userID); err == nil {
			userID = decrypted
		}
	}

	var boundGroupName string
	if site.BoundGroupID != nil {
		boundGroupName = groupNameMap[*site.BoundGroupID]
	}

	return ManagedSiteDTO{
		ID:                 site.ID,
		Name:               site.Name,
		Notes:              site.Notes,
		Description:        site.Description,
		Sort:               site.Sort,
		Enabled:            site.Enabled,
		BaseURL:            site.BaseURL,
		SiteType:           site.SiteType,
		UserID:             userID,
		CheckInPageURL:     site.CheckInPageURL,
		CheckInAvailable:   site.CheckInAvailable,
		CheckInEnabled:     site.CheckInEnabled,
		AutoCheckInEnabled: site.AutoCheckInEnabled,
		CustomCheckInURL:   site.CustomCheckInURL,
		AuthType:           site.AuthType,
		HasAuth:            strings.TrimSpace(site.AuthValue) != "",
		LastCheckInAt:      site.LastCheckInAt,
		LastCheckInDate:    site.LastCheckInDate,
		LastCheckInStatus:  site.LastCheckInStatus,
		LastCheckInMessage: site.LastCheckInMessage,
		BoundGroupID:       site.BoundGroupID,
		BoundGroupName:     boundGroupName,
		CreatedAt:          site.CreatedAt,
		UpdatedAt:          site.UpdatedAt,
	}
}

// InvalidateSiteListCache clears the site list cache
func (s *SiteService) InvalidateSiteListCache() {
	s.siteListCacheMu.Lock()
	s.siteListCache = nil
	s.siteListCacheMu.Unlock()
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

	// Encrypt user_id
	encryptedUserID := ""
	if uid := strings.TrimSpace(params.UserID); uid != "" {
		encryptedUserID, err = s.encryptionSvc.Encrypt(uid)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt user_id: %w", err)
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
		UserID:             encryptedUserID,
		CheckInPageURL:     strings.TrimSpace(params.CheckInPageURL),
		CheckInAvailable:   params.CheckInAvailable,
		CheckInEnabled:     checkInEnabled,
		AutoCheckInEnabled: autoCheckInEnabled,
		CustomCheckInURL:   strings.TrimSpace(params.CustomCheckInURL),
		AuthType:           authType,
		AuthValue:          encryptedAuth,
	}

	if err := s.db.WithContext(ctx).Create(site).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Invalidate cache after creation
	s.InvalidateSiteListCache()

	return s.toDTO(site), nil
}

func (s *SiteService) UpdateSite(ctx context.Context, siteID uint, params UpdateSiteParams) (*ManagedSiteDTO, error) {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Track original enabled status for sync callback
	originalEnabled := site.Enabled
	enabledChanged := false

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
		if site.Enabled != originalEnabled {
			enabledChanged = true
		}
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
		uid := strings.TrimSpace(*params.UserID)
		if uid == "" {
			site.UserID = ""
		} else {
			enc, err := s.encryptionSvc.Encrypt(uid)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt user_id: %w", err)
			}
			site.UserID = enc
		}
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

	if params.CheckInAvailable != nil {
		site.CheckInAvailable = *params.CheckInAvailable
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

	// Invalidate cache after update
	s.InvalidateSiteListCache()

	// Sync enabled status to bound group if changed
	if enabledChanged && s.SyncSiteEnabledToGroupCallback != nil {
		if err := s.SyncSiteEnabledToGroupCallback(ctx, siteID, site.Enabled); err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("Failed to sync site enabled status to bound group")
			// Don't fail the operation, just log the warning
		}
	}

	return s.toDTO(&site), nil
}

func (s *SiteService) DeleteSite(ctx context.Context, siteID uint) error {
	// Check if site is bound to a group
	var site ManagedSite
	if err := s.db.WithContext(ctx).Select("id", "bound_group_id").First(&site, siteID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // Site doesn't exist, idempotent delete
		}
		return app_errors.ParseDBError(err)
	}

	if site.BoundGroupID != nil {
		return services.NewI18nError(app_errors.ErrValidation, "binding.must_unbind_before_delete_site", nil)
	}

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

	// Invalidate cache after deletion
	s.InvalidateSiteListCache()

	return nil
}

// DeleteAllUnboundSites deletes all sites that are not bound to any group.
// Returns the count of deleted sites.
// Uses transaction to prevent race condition between fetching IDs and deletion.
func (s *SiteService) DeleteAllUnboundSites(ctx context.Context) (int64, error) {
	var deletedCount int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Find all unbound site IDs (sites where bound_group_id IS NULL)
		var unboundSiteIDs []uint
		if err := tx.Model(&ManagedSite{}).
			Where("bound_group_id IS NULL").
			Pluck("id", &unboundSiteIDs).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		if len(unboundSiteIDs) == 0 {
			return nil
		}

		// Delete sites first with re-check condition to prevent deleting concurrently-bound sites.
		// This ensures business rule consistency: bound sites cannot be deleted.
		result := tx.Where("id IN ? AND bound_group_id IS NULL", unboundSiteIDs).Delete(&ManagedSite{})
		if result.Error != nil {
			return app_errors.ParseDBError(result.Error)
		}
		deletedCount = result.RowsAffected

		// Delete orphaned logs for actually deleted sites.
		// Note: Some logs may remain if sites were bound between fetch and delete,
		// but those sites still exist so their logs are still valid.
		if deletedCount > 0 {
			if err := tx.Where("site_id IN ?", unboundSiteIDs).
				Delete(&ManagedSiteCheckinLog{}).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	// Invalidate cache after bulk deletion
	if deletedCount > 0 {
		s.InvalidateSiteListCache()
	}

	return deletedCount, nil
}

// CountUnboundSites returns the count of sites not bound to any group.
func (s *SiteService) CountUnboundSites(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&ManagedSite{}).
		Where("bound_group_id IS NULL").
		Count(&count).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}
	return count, nil
}

// CopySite creates a copy of an existing site with a unique name.
// The copied site will have the same configuration but without binding relationships.
func (s *SiteService) CopySite(ctx context.Context, siteID uint) (*ManagedSiteDTO, error) {
	// Fetch the source site
	var source ManagedSite
	if err := s.db.WithContext(ctx).First(&source, siteID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, services.NewI18nError(app_errors.ErrResourceNotFound, "site_management.site_not_found", nil)
		}
		return nil, app_errors.ParseDBError(err)
	}

	// Generate unique name for the copy
	uniqueName, err := s.generateUniqueSiteName(ctx, source.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to generate unique name: %w", err)
	}

	// Create the copy (without binding, checkin status, and timestamps)
	newSite := &ManagedSite{
		Name:               uniqueName,
		Notes:              source.Notes,
		Description:        source.Description,
		Sort:               source.Sort,
		Enabled:            source.Enabled,
		BaseURL:            source.BaseURL,
		SiteType:           source.SiteType,
		UserID:             source.UserID,
		CheckInPageURL:     source.CheckInPageURL,
		CheckInAvailable:   source.CheckInAvailable,
		CheckInEnabled:     source.CheckInEnabled,
		AutoCheckInEnabled: source.AutoCheckInEnabled,
		CustomCheckInURL:   source.CustomCheckInURL,
		AuthType:           source.AuthType,
		AuthValue:          source.AuthValue,
		// BoundGroupID is intentionally not copied
		// LastCheckIn* fields are intentionally not copied
	}

	if err := s.db.WithContext(ctx).Create(newSite).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Invalidate cache after copy
	s.InvalidateSiteListCache()

	return s.toDTO(newSite), nil
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
	// Decrypt user_id for display
	userID := site.UserID
	if userID != "" {
		if decrypted, err := s.encryptionSvc.Decrypt(userID); err == nil {
			userID = decrypted
		}
	}

	// Get bound group name if bound
	var boundGroupName string
	if site.BoundGroupID != nil {
		var group models.Group
		if err := s.db.Select("name").First(&group, *site.BoundGroupID).Error; err == nil {
			boundGroupName = group.Name
		}
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
		UserID:             userID,
		CheckInPageURL:     site.CheckInPageURL,
		CheckInAvailable:   site.CheckInAvailable,
		CheckInEnabled:     site.CheckInEnabled,
		AutoCheckInEnabled: site.AutoCheckInEnabled,
		CustomCheckInURL:   site.CustomCheckInURL,
		AuthType:           site.AuthType,
		HasAuth:            strings.TrimSpace(site.AuthValue) != "",
		LastCheckInAt:      site.LastCheckInAt,
		LastCheckInDate:    site.LastCheckInDate,
		LastCheckInStatus:  site.LastCheckInStatus,
		LastCheckInMessage: site.LastCheckInMessage,
		BoundGroupID:       site.BoundGroupID,
		BoundGroupName:     boundGroupName,
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
	CheckInAvailable   bool   `json:"checkin_available"`
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
			CheckInPageURL:     site.CheckInPageURL,
			CheckInAvailable:   site.CheckInAvailable,
			CheckInEnabled:     site.CheckInEnabled,
			AutoCheckInEnabled: site.AutoCheckInEnabled,
			CustomCheckInURL:   site.CustomCheckInURL,
			AuthType:           site.AuthType,
		}

		// Handle user_id based on export mode (stored encrypted in DB)
		if site.UserID != "" {
			if plainMode {
				// Decrypt for plain export
				if decrypted, err := s.encryptionSvc.Decrypt(site.UserID); err == nil {
					siteInfo.UserID = decrypted
				} else {
					logrus.WithError(err).Warnf("Failed to decrypt user_id for site %s during export", site.Name)
				}
			} else {
				// Keep encrypted for encrypted export
				siteInfo.UserID = site.UserID
			}
		}

		// Handle auth value based on export mode
		if site.AuthValue != "" {
			if plainMode {
				// Decrypt for plain export
				if decrypted, err := s.encryptionSvc.Decrypt(site.AuthValue); err == nil {
					siteInfo.AuthValue = decrypted
				} else {
					logrus.WithError(err).Warnf("Failed to decrypt auth value for site %s during export", site.Name)
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

		// Handle user_id encryption with auto-detection for mixed-format imports.
		// Design decision: Unlike auth_value which skips on decrypt failure, user_id uses
		// auto-detection (try decrypt, fallback to encrypt) to support:
		// 1. Users manually editing exported files with plain user_ids
		// 2. Mixed imports where some user_ids are encrypted and some are plain
		// 3. Better UX by not failing entire imports due to format inconsistency
		// Risk assessment: Double-encryption is unlikely because encrypted strings have
		// distinct base64/hex patterns that rarely match valid plain user_ids.
		// AI review suggested aligning with auth_value behavior, but we intentionally
		// keep this flexible approach based on real-world usage patterns.
		encryptedUserID := ""
		if siteInfo.UserID != "" {
			if plainMode {
				// Input is plain, need to encrypt
				enc, err := s.encryptionSvc.Encrypt(siteInfo.UserID)
				if err != nil {
					logrus.WithError(err).Warnf("Failed to encrypt user_id for site %s", name)
					skipped++
					continue
				}
				encryptedUserID = enc
			} else {
				// Auto-detect: try decrypt first, fallback to encrypt if it fails
				if _, err := s.encryptionSvc.Decrypt(siteInfo.UserID); err != nil {
					// Likely plain text, encrypt it
					enc, encErr := s.encryptionSvc.Encrypt(siteInfo.UserID)
					if encErr != nil {
						logrus.WithError(encErr).Warnf("Failed to encrypt user_id for site %s", name)
						skipped++
						continue
					}
					encryptedUserID = enc
					logrus.Debugf("user_id for site %s was plain text in encrypted mode, auto-encrypted", name)
				} else {
					encryptedUserID = siteInfo.UserID
				}
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
			UserID:             encryptedUserID,
			CheckInPageURL:     strings.TrimSpace(siteInfo.CheckInPageURL),
			CheckInAvailable:   siteInfo.CheckInAvailable,
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

	// Invalidate cache after import
	if imported > 0 {
		s.InvalidateSiteListCache()
	}

	return imported, skipped, nil
}
