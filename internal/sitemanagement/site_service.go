package sitemanagement

import (
	"context"
	"encoding/json"
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
	"gorm.io/gorm/clause"
)

const siteScheduleConfigUpdatedChannel = "managed_site:auto_checkin_config_updated"

type SiteService struct {
	db            *gorm.DB
	store         store.Store
	encryptionSvc encryption.Service

	// Site list cache for non-paginated requests
	siteListCache    *siteListCacheEntry
	siteListCacheMu  sync.RWMutex
	siteListCacheTTL time.Duration
	cacheInvalidator func()

	// Callback for syncing enabled status to bound group (set by handler layer)
	SyncSiteEnabledToGroupCallback func(ctx context.Context, siteID uint, enabled bool) error
	// Invalidate group list cache after site sort changes sync to bound groups.
	InvalidateGroupListCacheCallback func()
}

// SetCacheInvalidationCallback registers a callback for other cached site snapshots.
func (s *SiteService) SetCacheInvalidationCallback(callback func()) {
	s.cacheInvalidator = callback
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
		siteListCacheTTL: 15 * time.Second, // Aggressive TTL for faster memory release (site management is non-critical)
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

	CheckInAvailable bool
	CheckInEnabled   bool
	CustomCheckInURL string
	UseProxy         bool
	ProxyURL         string
	BypassMethod     string

	AuthType  string
	AuthValue string

	BalanceMultiplier *int64
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

	CheckInAvailable *bool
	CheckInEnabled   *bool
	CustomCheckInURL *string
	UseProxy         *bool
	ProxyURL         *string
	BypassMethod     *string

	AuthType  *string
	AuthValue *string

	BalanceMultiplier *int64
}

// managedSiteValidationSnapshot contains every persisted field used by
// validateManagedSiteConfiguration. Configuration edits compare this snapshot
// in the UPDATE predicate so a concurrent credential or capability change
// cannot leave an enabled site in an invalid state.
type managedSiteValidationSnapshot struct {
	siteType       string
	userID         string
	authType       string
	authValue      string
	customCheckURL string
	bypassMethod   string
	enabled        bool
	checkinEnabled bool
	autoCheckin    bool
}

func snapshotManagedSiteValidation(site ManagedSite) managedSiteValidationSnapshot {
	return managedSiteValidationSnapshot{
		siteType:       site.SiteType,
		userID:         site.UserID,
		authType:       site.AuthType,
		authValue:      site.AuthValue,
		customCheckURL: site.CustomCheckInURL,
		bypassMethod:   site.BypassMethod,
		enabled:        site.Enabled,
		checkinEnabled: site.CheckInEnabled,
		autoCheckin:    site.AutoCheckInEnabled,
	}
}

func (snapshot managedSiteValidationSnapshot) applyCAS(query *gorm.DB) *gorm.DB {
	return query.Where(
		"site_type = ? AND user_id = ? AND auth_type = ? AND auth_value = ? AND custom_checkin_url = ? AND bypass_method = ? AND enabled = ? AND checkin_enabled = ? AND auto_checkin_enabled = ?",
		snapshot.siteType,
		snapshot.userID,
		snapshot.authType,
		snapshot.authValue,
		snapshot.customCheckURL,
		snapshot.bypassMethod,
		snapshot.enabled,
		snapshot.checkinEnabled,
		snapshot.autoCheckin,
	)
}

func (snapshot managedSiteValidationSnapshot) matches(ctx context.Context, db *gorm.DB, siteID uint) (bool, error) {
	var count int64
	query := snapshot.applyCAS(db.WithContext(ctx).Model(&ManagedSite{}).Where("id = ?", siteID))
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func managedSiteValidationUpdateRequested(params UpdateSiteParams) bool {
	return params.Enabled != nil ||
		params.SiteType != nil ||
		params.UserID != nil ||
		params.CheckInEnabled != nil ||
		params.CustomCheckInURL != nil ||
		params.BypassMethod != nil ||
		params.AuthType != nil ||
		params.AuthValue != nil
}

type SiteReorderItem struct {
	ID   uint
	Sort int
}

func (s *SiteService) ListSites(ctx context.Context) ([]ManagedSiteDTO, error) {
	// Check cache first (fast path with read lock)
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

	// Update cache with double-checked locking to prevent cache stampede.
	// Between releasing RLock and acquiring Lock, another goroutine may have
	// already populated the cache, so we check again before overwriting.
	s.siteListCacheMu.Lock()
	if s.siteListCache != nil && time.Now().Before(s.siteListCache.ExpiresAt) {
		// Another goroutine populated cache while we were querying
		cachedSites := s.siteListCache.Sites
		s.siteListCacheMu.Unlock()
		return cachedSites, nil
	}
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
	// Note on LIKE search performance:
	// 1. LIKE '%pattern%' (leading wildcard) cannot use B-tree indexes regardless of
	//    case sensitivity - this is a fundamental limitation of B-tree index structure
	// 2. SQLite handles LIKE case-insensitively for ASCII by default
	// 3. For high-performance full-text search, consider SQLite FTS5 or similar
	// 4. Current implementation is acceptable for typical site list sizes (<1000 rows)
	// AI suggested adding indexes on name/notes/description, but indexes would not
	// improve LIKE '%pattern%' queries - only LIKE 'pattern%' can use indexes.
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
	if params.FocusSiteID > 0 {
		focusPage, err := s.focusSitePage(ctx, query, params.FocusSiteID, params.PageSize)
		if err != nil {
			return nil, err
		}
		if focusPage > 0 {
			params.Page = focusPage
		}
	}
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

func (s *SiteService) focusSitePage(ctx context.Context, baseQuery *gorm.DB, siteID uint, pageSize int) (int, error) {
	var target ManagedSite
	if err := baseQuery.Session(&gorm.Session{}).
		Select("id", "sort").
		Where("id = ?", siteID).
		First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, app_errors.ParseDBError(err)
	}

	var position int64
	err := baseQuery.Session(&gorm.Session{}).
		Where("(sort < ? OR (sort = ? AND id <= ?))", target.Sort, target.Sort, target.ID).
		Count(&position).Error
	if err != nil {
		return 0, app_errors.ParseDBError(err)
	}
	if position == 0 {
		return 0, nil
	}
	return int((position-1)/int64(pageSize)) + 1, nil
}

// convertSitesToDTOs converts sites to DTOs with batch operations for bound groups
func (s *SiteService) convertSitesToDTOs(ctx context.Context, sites []ManagedSite) ([]ManagedSiteDTO, error) {
	if len(sites) == 0 {
		return []ManagedSiteDTO{}, nil
	}

	// Collect site IDs for batch query
	siteIDs := make([]uint, len(sites))
	for i, site := range sites {
		siteIDs[i] = site.ID
	}

	// Batch fetch all groups bound to these sites
	var boundGroups []models.Group
	if err := s.db.WithContext(ctx).
		Select("id", "name", "display_name", "enabled", "bound_site_id").
		Where("bound_site_id IN ?", siteIDs).
		Order("sort ASC, id ASC").
		Find(&boundGroups).Error; err != nil {
		logrus.WithContext(ctx).WithError(err).Warn("Failed to fetch bound groups for site list")
	}

	// Build map of site ID -> bound groups
	boundGroupsMap := make(map[uint][]BoundGroupInfo)
	for _, g := range boundGroups {
		if g.BoundSiteID != nil {
			siteID := *g.BoundSiteID
			boundGroupsMap[siteID] = append(boundGroupsMap[siteID], BoundGroupInfo{
				ID:          g.ID,
				Name:        g.Name,
				DisplayName: g.DisplayName,
				Enabled:     g.Enabled,
			})
		}
	}

	// Convert to DTOs
	dtos := make([]ManagedSiteDTO, 0, len(sites))
	for i := range sites {
		dtos = append(dtos, s.siteToDTO(&sites[i], boundGroupsMap))
	}

	return dtos, nil
}

// siteToDTO converts a single site to DTO using pre-fetched group data.
// Note: This method is optimized for batch operations where group data is pre-fetched.
// For single-site operations (create/update), use toDTO() which performs individual lookup.
func (s *SiteService) siteToDTO(site *ManagedSite, boundGroupsMap map[uint][]BoundGroupInfo) ManagedSiteDTO {
	// Decrypt user_id for display
	userID := site.UserID
	if userID != "" {
		if decrypted, err := s.encryptionSvc.Decrypt(userID); err == nil {
			userID = decrypted
		}
	}

	return buildManagedSiteDTO(site, userID, boundGroupsMap[site.ID])
}

func buildManagedSiteDTO(site *ManagedSite, userID string, boundGroups []BoundGroupInfo) ManagedSiteDTO {
	multiplier := normalizeManagedSiteBalanceMultiplier(site.BalanceMultiplier)
	return ManagedSiteDTO{
		ID:                        site.ID,
		Name:                      site.Name,
		Notes:                     site.Notes,
		Description:               site.Description,
		Sort:                      site.Sort,
		Enabled:                   site.Enabled,
		BaseURL:                   site.BaseURL,
		SiteType:                  site.SiteType,
		UserID:                    userID,
		CheckInPageURL:            site.CheckInPageURL,
		CheckInAvailable:          site.CheckInAvailable,
		CheckInEnabled:            site.CheckInEnabled || site.AutoCheckInEnabled, // Merge legacy field for UI consistency
		CustomCheckInURL:          site.CustomCheckInURL,
		UseProxy:                  site.UseProxy,
		ProxyURL:                  site.ProxyURL,
		BypassMethod:              site.BypassMethod,
		AuthType:                  site.AuthType,
		HasAuth:                   strings.TrimSpace(site.AuthValue) != "",
		LastCheckInAt:             site.LastCheckInAt,
		LastCheckInDate:           site.LastCheckInDate,
		LastCheckInStatus:         site.LastCheckInStatus,
		LastCheckInMessage:        site.LastCheckInMessage,
		LastSiteOpenedDate:        site.LastSiteOpenedDate,
		LastCheckinPageOpenedDate: site.LastCheckinPageOpenedDate,
		BalanceMultiplier:         multiplier,
		LastBalance:               scaledManagedSiteBalance(site.LastBalance, multiplier),
		LastBalanceDate:           site.LastBalanceDate,
		BoundGroups:               boundGroups,
		BoundGroupCount:           int64(len(boundGroups)),
		CreatedAt:                 site.CreatedAt,
		UpdatedAt:                 site.UpdatedAt,
	}
}

// InvalidateSiteListCache clears the site list cache
func (s *SiteService) InvalidateSiteListCache() {
	s.siteListCacheMu.Lock()
	s.siteListCache = nil
	s.siteListCacheMu.Unlock()
	if s.cacheInvalidator != nil {
		s.cacheInvalidator()
	}
}

func (s *SiteService) invalidateGroupListCache() {
	if s.InvalidateGroupListCacheCallback != nil {
		s.InvalidateGroupListCacheCallback()
	}
}

func validateSiteReorderItems(items []SiteReorderItem) error {
	if len(items) == 0 {
		return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_items_required", nil)
	}

	seen := make(map[uint]struct{}, len(items))
	for _, item := range items {
		if item.ID == 0 {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_site_id", nil)
		}
		if item.Sort < 0 {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_sort_negative", nil)
		}
		if _, ok := seen[item.ID]; ok {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_duplicate_site", map[string]any{"id": item.ID})
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func validateSiteRenumberParams(start, step int) error {
	if start < 0 {
		return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_start_negative", nil)
	}
	if step < 1 {
		return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_step_invalid", nil)
	}
	return nil
}

func buildSiteReorderCase(items []SiteReorderItem) (string, []any, []uint) {
	args := make([]any, 0, len(items)*2)
	ids := make([]uint, 0, len(items))
	caseSQL := strings.Builder{}
	caseSQL.WriteString("CASE id")
	for _, item := range items {
		caseSQL.WriteString(" WHEN ? THEN ?")
		args = append(args, item.ID, item.Sort)
		ids = append(ids, item.ID)
	}
	caseSQL.WriteString(" ELSE sort END")
	return caseSQL.String(), args, ids
}

func buildBoundGroupSortSyncCase(items []SiteReorderItem) (string, []any, []uint) {
	args := make([]any, 0, len(items)*2)
	ids := make([]uint, 0, len(items))
	caseSQL := strings.Builder{}
	caseSQL.WriteString("CASE bound_site_id")
	for _, item := range items {
		caseSQL.WriteString(" WHEN ? THEN ?")
		args = append(args, item.ID, item.Sort)
		ids = append(ids, item.ID)
	}
	caseSQL.WriteString(" ELSE sort END")
	return caseSQL.String(), args, ids
}

func lockExistingSiteIDsForReorder(tx *gorm.DB, ids []uint) (int64, error) {
	query := tx.Model(&ManagedSite{}).Where("id IN ?", ids)
	switch tx.Dialector.Name() {
	case "mysql", "postgres":
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}

	var existingIDs []uint
	if err := query.Pluck("id", &existingIDs).Error; err != nil {
		return 0, err
	}
	return int64(len(existingIDs)), nil
}

func reorderSitesInTx(tx *gorm.DB, items []SiteReorderItem) error {
	// The public callers validate items before opening the transaction.
	caseSQL, args, ids := buildSiteReorderCase(items)
	count, err := lockExistingSiteIDsForReorder(tx, ids)
	if err != nil {
		return app_errors.ParseDBError(err)
	}
	if count != int64(len(ids)) {
		return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_site_not_found", nil)
	}

	result := tx.Model(&ManagedSite{}).
		Where("id IN ?", ids).
		Update("sort", gorm.Expr(caseSQL, args...))
	if result.Error != nil {
		return app_errors.ParseDBError(result.Error)
	}
	if err := tx.Model(&ManagedSite{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	if count != int64(len(ids)) {
		return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.reorder_site_not_found", nil)
	}
	if err := syncBoundGroupSortsForSitesInTx(tx, items); err != nil {
		return err
	}
	return nil
}

func syncBoundGroupSortsForSitesInTx(tx *gorm.DB, items []SiteReorderItem) error {
	caseSQL, args, ids := buildBoundGroupSortSyncCase(items)
	result := tx.Model(&models.Group{}).
		Where("bound_site_id IN ?", ids).
		Update("sort", gorm.Expr(caseSQL, args...))
	if result.Error != nil {
		return app_errors.ParseDBError(result.Error)
	}
	return nil
}

func (s *SiteService) ReorderSites(ctx context.Context, items []SiteReorderItem) error {
	if err := validateSiteReorderItems(items); err != nil {
		return err
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	if err := reorderSitesInTx(tx, items); err != nil {
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	tx = nil
	s.InvalidateSiteListCache()
	s.invalidateGroupListCache()
	return nil
}

func (s *SiteService) RenumberSites(ctx context.Context, start, step int) error {
	if err := validateSiteRenumberParams(start, step); err != nil {
		return err
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	var ids []uint
	// Read IDs inside the reorder transaction so the snapshot matches the update work.
	query := tx.Model(&ManagedSite{}).Order("sort ASC, id ASC")
	switch tx.Dialector.Name() {
	case "mysql", "postgres":
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Pluck("id", &ids).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	if len(ids) == 0 {
		return nil
	}

	items := make([]SiteReorderItem, len(ids))
	for i, id := range ids {
		items[i] = SiteReorderItem{
			ID:   id,
			Sort: start + i*step,
		}
	}

	if err := reorderSitesInTx(tx, items); err != nil {
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	tx = nil
	s.InvalidateSiteListCache()
	s.invalidateGroupListCache()
	return nil
}

func (s *SiteService) CreateSite(ctx context.Context, params CreateSiteParams) (*ManagedSiteDTO, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.name_required", nil)
	}
	balanceMultiplier := defaultManagedSiteBalanceMultiplier
	if params.BalanceMultiplier != nil {
		if *params.BalanceMultiplier < defaultManagedSiteBalanceMultiplier {
			return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.balance_multiplier_min", nil)
		}
		balanceMultiplier = *params.BalanceMultiplier
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
	if !isValidBypassMethod(params.BypassMethod) {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_bypass_method", nil)
	}
	bypassMethod := normalizeBypassMethod(params.BypassMethod)
	if err := validateManagedSiteConfiguration(
		siteType,
		authType,
		params.AuthValue,
		params.UserID,
		strings.TrimSpace(params.CustomCheckInURL),
		bypassMethod,
		params.Enabled,
		params.CheckInEnabled,
	); err != nil {
		return nil, err
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
		Name:              name,
		Notes:             strings.TrimSpace(params.Notes),
		Description:       strings.TrimSpace(params.Description),
		Sort:              params.Sort,
		Enabled:           params.Enabled,
		BaseURL:           baseURL,
		SiteType:          siteType,
		UserID:            encryptedUserID,
		CheckInPageURL:    strings.TrimSpace(params.CheckInPageURL),
		CheckInAvailable:  params.CheckInAvailable,
		CheckInEnabled:    params.CheckInEnabled,
		CustomCheckInURL:  strings.TrimSpace(params.CustomCheckInURL),
		UseProxy:          params.UseProxy,
		ProxyURL:          strings.TrimSpace(params.ProxyURL),
		BypassMethod:      bypassMethod,
		AuthType:          authType,
		AuthValue:         encryptedAuth,
		BalanceMultiplier: balanceMultiplier,
	}

	if err := s.db.WithContext(ctx).Create(site).Error; err != nil {
		// Check if it's a duplicate name error and return i18n error
		parsedErr := app_errors.ParseDBError(err)
		if parsedErr == app_errors.ErrDuplicateResource {
			return nil, services.NewI18nError(app_errors.ErrDuplicateResource, "site_management.validation.name_duplicate", map[string]any{"name": site.Name})
		}
		return nil, parsedErr
	}

	// Invalidate cache after creation
	s.InvalidateSiteListCache()

	return s.toDTO(ctx, site), nil
}

func (s *SiteService) UpdateSite(ctx context.Context, siteID uint, params UpdateSiteParams) (*ManagedSiteDTO, error) {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	originalAuthType := site.AuthType
	originalValidation := snapshotManagedSiteValidation(site)
	authTypeChanged := false
	if params.BalanceMultiplier != nil && *params.BalanceMultiplier < defaultManagedSiteBalanceMultiplier {
		return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.balance_multiplier_min", nil)
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
	if params.BalanceMultiplier != nil {
		site.BalanceMultiplier = *params.BalanceMultiplier
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
		authTypeChanged = authType != site.AuthType
		if authTypeChanged && authType != AuthTypeNone {
			reconciled, err := s.reconcileAuthValueForTypeChange(originalAuthType, authType, site.AuthValue)
			if err != nil {
				return nil, fmt.Errorf("failed to reconcile auth values: %w", err)
			}
			site.AuthValue = reconciled
		}
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

			// Merge with existing auth values for multi-auth support
			// This prevents partial updates from clearing unconfigured auth types
			mergedValue, err := s.mergeAuthValues(site.AuthType, site.AuthValue, value)
			if err != nil {
				return nil, fmt.Errorf("failed to merge auth values: %w", err)
			}

			enc, err := s.encryptionSvc.Encrypt(mergedValue)
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
		// Clear the legacy flag so the current toggle remains authoritative.
		site.AutoCheckInEnabled = false
	}
	if params.UseProxy != nil {
		site.UseProxy = *params.UseProxy
	}
	if params.ProxyURL != nil {
		site.ProxyURL = strings.TrimSpace(*params.ProxyURL)
	}
	if params.BypassMethod != nil {
		if !isValidBypassMethod(*params.BypassMethod) {
			return nil, services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_bypass_method", nil)
		}
		site.BypassMethod = normalizeBypassMethod(*params.BypassMethod)
	}
	if err := s.validateStoredManagedSiteConfiguration(site); err != nil {
		return nil, err
	}

	// Update only requested columns so a concurrent token refresh cannot be
	// overwritten by the stale row snapshot loaded at the start of this method.
	updates := make(map[string]any, 18)
	if params.Name != nil {
		updates["name"] = site.Name
	}
	if params.Notes != nil {
		updates["notes"] = site.Notes
	}
	if params.Description != nil {
		updates["description"] = site.Description
	}
	if params.Sort != nil {
		updates["sort"] = site.Sort
	}
	if params.BalanceMultiplier != nil {
		updates["balance_multiplier"] = site.BalanceMultiplier
	}
	if params.Enabled != nil {
		updates["enabled"] = site.Enabled
	}
	if params.BaseURL != nil {
		updates["base_url"] = site.BaseURL
	}
	if params.SiteType != nil {
		updates["site_type"] = site.SiteType
	}
	if params.UserID != nil {
		updates["user_id"] = site.UserID
	}
	if params.CheckInPageURL != nil {
		updates["checkin_page_url"] = site.CheckInPageURL
	}
	if params.CustomCheckInURL != nil {
		updates["custom_checkin_url"] = site.CustomCheckInURL
	}
	if params.AuthType != nil {
		updates["auth_type"] = site.AuthType
	}
	if params.AuthValue != nil || authTypeChanged {
		updates["auth_value"] = site.AuthValue
	}
	if params.CheckInAvailable != nil {
		updates["checkin_available"] = site.CheckInAvailable
	}
	if params.CheckInEnabled != nil {
		updates["checkin_enabled"] = site.CheckInEnabled
		updates["auto_checkin_enabled"] = site.AutoCheckInEnabled
	}
	if params.UseProxy != nil {
		updates["use_proxy"] = site.UseProxy
	}
	if params.ProxyURL != nil {
		updates["proxy_url"] = site.ProxyURL
	}
	if params.BypassMethod != nil {
		updates["bypass_method"] = site.BypassMethod
	}

	if len(updates) > 0 {
		updateQuery := s.db.WithContext(ctx).Model(&ManagedSite{}).Where("id = ?", siteID)
		// Authentication rotations use compare-and-swap so a user edit cannot
		// silently restore a credential snapshot read before a token refresh.
		authUpdateRequested := params.AuthValue != nil || authTypeChanged
		configurationUpdateRequested := managedSiteValidationUpdateRequested(params)
		if configurationUpdateRequested {
			updateQuery = originalValidation.applyCAS(updateQuery)
		}
		updateResult := updateQuery.Updates(updates)
		if updateResult.Error != nil {
			return nil, app_errors.ParseDBError(updateResult.Error)
		}
		if configurationUpdateRequested && updateResult.RowsAffected == 0 {
			matches, matchErr := originalValidation.matches(ctx, s.db, siteID)
			if matchErr != nil {
				return nil, app_errors.ParseDBError(matchErr)
			}
			if matches {
				// MySQL may report zero affected rows when the CAS matched but
				// every requested value was already equal to the stored value.
				updateResult.RowsAffected = 1
			} else {
				messageKey := "site_management.validation.configuration_changed_retry"
				if authUpdateRequested {
					messageKey = "site_management.validation.auth_changed_retry"
				}
				return nil, services.NewI18nError(app_errors.ErrValidation, messageKey, nil)
			}
		}
		if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
			return nil, app_errors.ParseDBError(err)
		}
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

	return s.toDTO(ctx, &site), nil
}

// RecordSiteOpened records when user clicked "Open Site" button.
// Updates last_site_opened_date to the current site-management local day.
//
// Design Decision: This is a fire-and-forget tracking feature. We intentionally do NOT:
// 1. Check if site exists before update (would add extra DB query)
// 2. Return 404 for non-existent sites (RowsAffected=0 is acceptable)
// The frontend only calls this for sites in the list, so invalid IDs are unlikely.
// AI review suggested checking existence, but the overhead is not justified for this use case.
func (s *SiteService) RecordSiteOpened(ctx context.Context, siteID uint) error {
	date := GetBeijingCheckinDay()
	if err := s.db.WithContext(ctx).
		Model(&ManagedSite{}).
		Where("id = ?", siteID).
		Update("last_site_opened_date", date).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	// Invalidate cache to reflect the change
	s.InvalidateSiteListCache()
	return nil
}

// RecordCheckinPageOpened records when user clicked "Open Check-in Page" button.
// Updates last_checkin_page_opened_date to the current site-management local day.
//
// Design Decision: Same as RecordSiteOpened - fire-and-forget without existence check.
func (s *SiteService) RecordCheckinPageOpened(ctx context.Context, siteID uint) error {
	date := GetBeijingCheckinDay()
	if err := s.db.WithContext(ctx).
		Model(&ManagedSite{}).
		Where("id = ?", siteID).
		Update("last_checkin_page_opened_date", date).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	// Invalidate cache to reflect the change
	s.InvalidateSiteListCache()
	return nil
}

func (s *SiteService) DeleteSite(ctx context.Context, siteID uint) error {
	// Check if any groups are bound to this site
	// Note: This check duplicates BindingService.CheckSiteCanDelete() logic intentionally.
	// SiteService and BindingService are decoupled by design to avoid circular dependencies.
	var boundCount int64
	if err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("bound_site_id = ?", siteID).
		Count(&boundCount).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if boundCount > 0 {
		return services.NewI18nError(app_errors.ErrValidation, "binding.must_unbind_before_delete_site", map[string]any{"count": boundCount})
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

// DeleteAllUnboundSites deletes all sites that have no groups bound to them.
// Returns the count of deleted sites.
// Uses transaction to prevent race condition between fetching IDs and deletion.
func (s *SiteService) DeleteAllUnboundSites(ctx context.Context) (int64, error) {
	var deletedCount int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get all site IDs that have groups bound to them (for exclusion)
		var boundSiteIDs []uint
		if err := tx.Model(&models.Group{}).
			Where("bound_site_id IS NOT NULL").
			Distinct("bound_site_id").
			Pluck("bound_site_id", &boundSiteIDs).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		// Build delete query for unbound sites
		deleteQuery := tx.Model(&ManagedSite{})
		if len(boundSiteIDs) > 0 {
			deleteQuery = deleteQuery.Where("id NOT IN ?", boundSiteIDs)
		}

		// Get IDs of sites to be deleted (for log cleanup)
		var unboundSiteIDs []uint
		if err := deleteQuery.Pluck("id", &unboundSiteIDs).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		if len(unboundSiteIDs) == 0 {
			return nil
		}

		// Delete unbound sites
		result := tx.Where("id IN ?", unboundSiteIDs).Delete(&ManagedSite{})
		if result.Error != nil {
			return app_errors.ParseDBError(result.Error)
		}
		deletedCount = result.RowsAffected

		// Delete orphaned logs for deleted sites
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

// CountUnboundSites returns the count of sites that have no groups bound to them.
func (s *SiteService) CountUnboundSites(ctx context.Context) (int64, error) {
	// Get all site IDs that have groups bound to them
	var boundSiteIDs []uint
	if err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("bound_site_id IS NOT NULL").
		Distinct("bound_site_id").
		Pluck("bound_site_id", &boundSiteIDs).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}

	// Count sites not in the bound list
	query := s.db.WithContext(ctx).Model(&ManagedSite{})
	if len(boundSiteIDs) > 0 {
		query = query.Where("id NOT IN ?", boundSiteIDs)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
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
	// Merge auto_checkin_enabled into checkin_enabled for backward compatibility with legacy data
	newSite := &ManagedSite{
		Name:              uniqueName,
		Notes:             source.Notes,
		Description:       source.Description,
		Sort:              source.Sort,
		Enabled:           source.Enabled,
		BaseURL:           source.BaseURL,
		SiteType:          source.SiteType,
		UserID:            source.UserID,
		CheckInPageURL:    source.CheckInPageURL,
		CheckInAvailable:  source.CheckInAvailable,
		CheckInEnabled:    source.CheckInEnabled || source.AutoCheckInEnabled,
		CustomCheckInURL:  source.CustomCheckInURL,
		UseProxy:          source.UseProxy,
		ProxyURL:          source.ProxyURL,
		BypassMethod:      source.BypassMethod,
		AuthType:          source.AuthType,
		AuthValue:         source.AuthValue,
		BalanceMultiplier: normalizeManagedSiteBalanceMultiplier(source.BalanceMultiplier),
		// BoundGroupID is intentionally not copied
		// LastCheckIn* fields are intentionally not copied
	}

	if err := s.db.WithContext(ctx).Create(newSite).Error; err != nil {
		// Check if it's a duplicate name error and return i18n error
		parsedErr := app_errors.ParseDBError(err)
		if parsedErr == app_errors.ErrDuplicateResource {
			return nil, services.NewI18nError(app_errors.ErrDuplicateResource, "site_management.validation.name_duplicate", map[string]any{"name": newSite.Name})
		}
		return nil, parsedErr
	}

	// Invalidate cache after copy
	s.InvalidateSiteListCache()

	return s.toDTO(ctx, newSite), nil
}

func (s *SiteService) GetAutoCheckinConfig(ctx context.Context) (*AutoCheckinConfig, error) {
	st, err := s.ensureSettingsRow(ctx)
	if err != nil {
		return nil, err
	}

	// Parse schedule times from comma-separated string
	var scheduleTimes []string
	if st.ScheduleTimes != "" {
		for _, t := range strings.Split(st.ScheduleTimes, ",") {
			t = normalizeAutoCheckinTime(t)
			if t != "" {
				scheduleTimes = append(scheduleTimes, t)
			}
		}
	}
	// Default to single time if empty
	if len(scheduleTimes) == 0 {
		scheduleTimes = []string{"09:00"}
	}

	return &AutoCheckinConfig{
		GlobalEnabled:     st.AutoCheckinEnabled,
		ScheduleTimes:     scheduleTimes,
		WindowStart:       normalizeAutoCheckinTime(st.WindowStart),
		WindowEnd:         normalizeAutoCheckinTime(st.WindowEnd),
		ScheduleMode:      st.ScheduleMode,
		DeterministicTime: normalizeAutoCheckinTime(st.DeterministicTime),
		RetryStrategy: AutoCheckinRetryStrategy{
			Enabled:           st.RetryEnabled,
			IntervalMinutes:   st.RetryIntervalMinutes,
			MaxAttemptsPerDay: st.RetryMaxAttemptsPerDay,
		},
	}, nil
}

func (s *SiteService) UpdateAutoCheckinConfig(ctx context.Context, cfg AutoCheckinConfig) (*AutoCheckinConfig, error) {
	mode, err := validateAutoCheckinConfig(cfg)
	if err != nil {
		return nil, err
	}

	st, err := s.ensureSettingsRow(ctx)
	if err != nil {
		return nil, err
	}
	normalized := normalizeAutoCheckinConfig(cfg, mode)
	updates := autoCheckinConfigUpdates(normalized)
	updates["updated_at"] = time.Now()
	if err := s.db.WithContext(ctx).Model(&ManagedSiteSetting{}).
		Where("id = ?", st.ID).
		Updates(updates).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.NotifyScheduleConfigUpdated()
	// Every response field comes from the same explicit update map; no config field is database-generated.
	// Avoid a post-commit read that could prevent the handler's guaranteed local reschedule.
	return &normalized, nil
}

func validateAutoCheckinConfig(cfg AutoCheckinConfig) (string, error) {
	mode := strings.TrimSpace(cfg.ScheduleMode)
	if mode == "" {
		mode = AutoCheckinScheduleModeMultiple
	}

	// Validate based on schedule mode
	switch mode {
	case AutoCheckinScheduleModeMultiple:
		if len(cfg.ScheduleTimes) == 0 {
			return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.schedule_times_required", nil)
		}
		// Validate format and check for duplicates.
		// Backend validation is essential since frontend validation can be bypassed via direct API calls.
		seen := make(map[string]bool)
		for i, t := range cfg.ScheduleTimes {
			t = strings.TrimSpace(t)
			if !utils.IsCanonicalHourMinute(t) {
				return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_time", map[string]any{"field": "schedule_times", "index": i})
			}
			if seen[t] {
				return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.duplicate_time", map[string]any{"time": t})
			}
			seen[t] = true
		}
	case AutoCheckinScheduleModeRandom:
		if cfg.WindowStart == "" || cfg.WindowEnd == "" {
			return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.time_window_required", nil)
		}
		if !utils.IsCanonicalHourMinute(cfg.WindowStart) {
			return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_time", map[string]any{"field": "window_start"})
		}
		if !utils.IsCanonicalHourMinute(cfg.WindowEnd) {
			return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_time", map[string]any{"field": "window_end"})
		}
	case AutoCheckinScheduleModeDeterministic:
		if cfg.DeterministicTime == "" {
			return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.deterministic_time_required", nil)
		}
		if !utils.IsCanonicalHourMinute(cfg.DeterministicTime) {
			return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_time", map[string]any{"field": "deterministic_time"})
		}
	default:
		return "", services.NewI18nError(app_errors.ErrValidation, "site_management.validation.invalid_schedule_mode", nil)
	}
	// Reject oversized serialized values instead of truncating them and changing schedule semantics.
	if len(joinAutoCheckinScheduleTimes(cfg.ScheduleTimes)) > maxAutoCheckinScheduleTimesStorageLength {
		return "", services.NewI18nError(
			app_errors.ErrValidation,
			"site_management.validation.schedule_times_too_long",
			map[string]any{"max": maxAutoCheckinScheduleTimesStorageLength},
		)
	}
	return mode, nil
}

const maxAutoCheckinScheduleTimesStorageLength = 255

func joinAutoCheckinScheduleTimes(scheduleTimes []string) string {
	return strings.Join(normalizeAutoCheckinScheduleTimes(scheduleTimes), ",")
}

func normalizeAutoCheckinScheduleTimes(scheduleTimes []string) []string {
	normalized := make([]string, len(scheduleTimes))
	for i, value := range scheduleTimes {
		normalized[i] = normalizeAutoCheckinTime(value)
	}
	return normalized
}

func normalizeAutoCheckinTime(value string) string {
	if normalized, ok := utils.NormalizeHourMinute(value); ok {
		return normalized
	}
	return strings.TrimSpace(value)
}

func normalizeAutoCheckinConfig(cfg AutoCheckinConfig, mode string) AutoCheckinConfig {
	cfg.ScheduleTimes = normalizeAutoCheckinScheduleTimes(cfg.ScheduleTimes)
	cfg.WindowStart = normalizeAutoCheckinTime(cfg.WindowStart)
	cfg.WindowEnd = normalizeAutoCheckinTime(cfg.WindowEnd)
	cfg.ScheduleMode = mode
	cfg.DeterministicTime = normalizeAutoCheckinTime(cfg.DeterministicTime)
	cfg.RetryStrategy.IntervalMinutes = clampInt(cfg.RetryStrategy.IntervalMinutes, 1, 24*60)
	cfg.RetryStrategy.MaxAttemptsPerDay = clampInt(cfg.RetryStrategy.MaxAttemptsPerDay, 1, 10)
	return cfg
}

func autoCheckinConfigUpdates(cfg AutoCheckinConfig) map[string]any {
	return map[string]any{
		"auto_checkin_enabled":       cfg.GlobalEnabled,
		"schedule_times":             strings.Join(cfg.ScheduleTimes, ","),
		"window_start":               cfg.WindowStart,
		"window_end":                 cfg.WindowEnd,
		"schedule_mode":              cfg.ScheduleMode,
		"deterministic_time":         cfg.DeterministicTime,
		"retry_enabled":              cfg.RetryStrategy.Enabled,
		"retry_interval_minutes":     cfg.RetryStrategy.IntervalMinutes,
		"retry_max_attempts_per_day": cfg.RetryStrategy.MaxAttemptsPerDay,
	}
}

func (s *SiteService) GetAutoBalanceConfig(ctx context.Context) (*AutoBalanceConfig, error) {
	st, err := s.ensureSettingsRow(ctx)
	if err != nil {
		return nil, err
	}

	return &AutoBalanceConfig{
		GlobalEnabled: st.AutoBalanceEnabled,
		IntervalHours: normalizeAutoBalanceIntervalHours(st.BalanceRefreshIntervalHours),
	}, nil
}

func (s *SiteService) UpdateAutoBalanceConfig(ctx context.Context, cfg AutoBalanceConfig) (*AutoBalanceConfig, error) {
	if err := validateAutoBalanceConfig(cfg); err != nil {
		return nil, err
	}

	st, err := s.ensureSettingsRow(ctx)
	if err != nil {
		return nil, err
	}
	updates := autoBalanceConfigUpdates(cfg)
	updates["updated_at"] = time.Now()
	if err := s.db.WithContext(ctx).Model(&ManagedSiteSetting{}).
		Where("id = ?", st.ID).
		Updates(updates).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	s.NotifyScheduleConfigUpdated()
	// Every response field comes from the same explicit update map; no config field is database-generated.
	// Avoid a post-commit read that could prevent the handler's guaranteed local reschedule.
	return &cfg, nil
}

func autoBalanceConfigUpdates(cfg AutoBalanceConfig) map[string]any {
	return map[string]any{
		"auto_balance_enabled":           cfg.GlobalEnabled,
		"balance_refresh_interval_hours": cfg.IntervalHours,
	}
}

func validateAutoBalanceConfig(cfg AutoBalanceConfig) error {
	if cfg.IntervalHours < minAutoBalanceIntervalHours || cfg.IntervalHours > maxAutoBalanceIntervalHours {
		return services.NewI18nError(
			app_errors.ErrValidation,
			"site_management.validation.invalid_balance_interval",
			map[string]any{"min": minAutoBalanceIntervalHours, "max": maxAutoBalanceIntervalHours},
		)
	}
	return nil
}

// NotifyScheduleConfigUpdated publishes a post-commit scheduler reload notification.
func (s *SiteService) NotifyScheduleConfigUpdated() {
	if s.store == nil {
		return
	}
	if err := s.store.Publish(siteScheduleConfigUpdatedChannel, []byte("1")); err != nil {
		logrus.WithError(err).Debug("managed site schedule config publish failed")
	}
}

func (s *SiteService) ensureSettingsRow(ctx context.Context) (*ManagedSiteSetting, error) {
	return ensureSettingsRowInDB(s.db.WithContext(ctx))
}

func ensureSettingsRowInDB(db *gorm.DB) (*ManagedSiteSetting, error) {
	var st ManagedSiteSetting
	err := db.First(&st, 1).Error
	if err == nil {
		return &st, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, app_errors.ParseDBError(err)
	}

	// Default to "multiple" mode for consistency with UpdateAutoCheckinConfig and loadConfig.
	// This ensures new installations use the same default as empty mode updates.
	st = ManagedSiteSetting{
		ID:                          1,
		AutoCheckinEnabled:          false,
		AutoBalanceEnabled:          true,
		BalanceRefreshIntervalHours: defaultAutoBalanceIntervalHours,
		ScheduleTimes:               "09:00",
		WindowStart:                 "09:00",
		WindowEnd:                   "18:00",
		ScheduleMode:                AutoCheckinScheduleModeMultiple,
		DeterministicTime:           "",
		RetryEnabled:                false,
		RetryIntervalMinutes:        60,
		RetryMaxAttemptsPerDay:      2,
	}
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&st)
	if result.Error != nil {
		return nil, app_errors.ParseDBError(result.Error)
	}
	if result.RowsAffected == 0 {
		if err := db.First(&st, 1).Error; err != nil {
			return nil, app_errors.ParseDBError(err)
		}
	}
	return &st, nil
}

func (s *SiteService) toDTO(ctx context.Context, site *ManagedSite) *ManagedSiteDTO {
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

	// Get all groups bound to this site (many-to-one relationship)
	var boundGroups []BoundGroupInfo
	var groups []models.Group
	if err := s.db.WithContext(ctx).Select("id", "name", "display_name", "enabled").
		Where("bound_site_id = ?", site.ID).
		Order("sort ASC, id ASC").
		Find(&groups).Error; err != nil {
		// Log warning but continue with empty bound groups (graceful degradation)
		logrus.WithContext(ctx).WithError(err).WithField("siteID", site.ID).
			Warn("Failed to fetch bound groups for site")
	} else {
		for _, g := range groups {
			boundGroups = append(boundGroups, BoundGroupInfo{
				ID:          g.ID,
				Name:        g.Name,
				DisplayName: g.DisplayName,
				Enabled:     g.Enabled,
			})
		}
	}

	dto := buildManagedSiteDTO(site, userID, boundGroups)
	return &dto
}

// normalizeAuthType normalizes auth type string, supporting both single and comma-separated multi-auth values.
// Examples: "access_token" -> "access_token", "access_token,cookie" -> "access_token,cookie"
// Returns empty string if any component is invalid.
func normalizeAuthType(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")

	// Handle comma-separated multi-auth types (e.g., "access_token,cookie")
	if strings.Contains(s, ",") {
		parts := strings.Split(s, ",")
		var normalized []string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || part == strings.ToLower(AuthTypeNone) {
				continue
			}
			n := normalizeSingleAuthType(part)
			if n == "" {
				return "" // invalid component
			}
			normalized = append(normalized, n)
		}
		if len(normalized) == 0 {
			return AuthTypeNone
		}
		return strings.Join(normalized, ",")
	}

	return normalizeSingleAuthType(s)
}

// normalizeSingleAuthType normalizes a single auth type value.
// Returns empty string for invalid values.
func normalizeSingleAuthType(s string) string {
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

// normalizeBypassMethod normalizes the bypass method string.
// Returns empty string for "none" or invalid values.
func normalizeBypassMethod(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	switch s {
	case BypassMethodStealth:
		return BypassMethodStealth
	case BypassMethodNone, "":
		return ""
	default:
		return ""
	}
}

func isValidBypassMethod(raw string) bool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", BypassMethodNone, BypassMethodStealth:
		return true
	default:
		return false
	}
}

func validateManagedSiteConfiguration(
	siteType string,
	authType string,
	plainAuthValue string,
	plainUserID string,
	customCheckInURL string,
	bypassMethod string,
	enabled bool,
	checkInEnabled bool,
) error {
	authConfig := parseAuthConfig(authType, plainAuthValue)
	active := enabled || checkInEnabled

	if siteType == SiteTypeAnyrouter {
		authTypes := configuredAuthTypeSet(authType)
		_, hasCookie := authTypes[AuthTypeCookie]
		if !hasCookie || len(authTypes) != 1 {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.anyrouter_requires_cookie", nil)
		}
		if active && strings.TrimSpace(plainUserID) == "" {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.anyrouter_user_id_required", nil)
		}
		if active && strings.TrimSpace(authConfig.GetAuthValue(AuthTypeCookie)) == "" {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.anyrouter_cookie_required", nil)
		}
	}
	if siteType == SiteTypeSub2API && active {
		authTypes := configuredAuthTypeSet(authType)
		if _, hasAccessToken := authTypes[AuthTypeAccessToken]; !hasAccessToken {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.sub2api_requires_access_token", nil)
		}
		if strings.TrimSpace(authConfig.GetAuthValue(AuthTypeAccessToken)) == "" &&
			strings.TrimSpace(authConfig.GetSupplementalValue(authFieldRefreshToken)) == "" {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.sub2api_credential_required", nil)
		}
	}

	if isStealthBypassMethod(bypassMethod) {
		if !authConfig.HasAuthType(AuthTypeCookie) {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.stealth_requires_cookie", nil)
		}
		cookie := strings.TrimSpace(authConfig.GetAuthValue(AuthTypeCookie))
		if cookie == "" {
			return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.stealth_cookie_required", nil)
		}
		if missing := validateCFCookies(cookie); len(missing) > 0 {
			return services.NewI18nError(
				app_errors.ErrValidation,
				"site_management.validation.stealth_waf_cookie_required",
				map[string]any{"cookies": strings.Join(missing, ", ")},
			)
		}
	}

	if siteType == SiteTypeSub2API && checkInEnabled && strings.TrimSpace(customCheckInURL) == "" {
		return services.NewI18nError(app_errors.ErrValidation, "site_management.validation.sub2api_custom_checkin_required", nil)
	}
	return nil
}

func (s *SiteService) validateStoredManagedSiteConfiguration(site ManagedSite) error {
	plainAuthValue := ""
	if strings.TrimSpace(site.AuthValue) != "" {
		decrypted, err := s.encryptionSvc.Decrypt(site.AuthValue)
		if err != nil {
			return fmt.Errorf("decrypt auth value for validation: %w", err)
		}
		plainAuthValue = decrypted
	}
	plainUserID := ""
	if strings.TrimSpace(site.UserID) != "" {
		decrypted, err := s.encryptionSvc.Decrypt(site.UserID)
		if err != nil {
			return fmt.Errorf("decrypt user_id for validation: %w", err)
		}
		plainUserID = decrypted
	}
	return validateManagedSiteConfiguration(
		site.SiteType,
		site.AuthType,
		plainAuthValue,
		plainUserID,
		site.CustomCheckInURL,
		site.BypassMethod,
		site.Enabled,
		site.CheckInEnabled || site.AutoCheckInEnabled,
	)
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

// SiteExportInfo represents exported site information.
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
	UseProxy           bool   `json:"use_proxy"`
	ProxyURL           string `json:"proxy_url"`
	BypassMethod       string `json:"bypass_method"`
	AuthType           string `json:"auth_type"`
	AuthValue          string `json:"auth_value,omitempty"` // Encrypted or plain based on export mode
	BalanceMultiplier  int64  `json:"balance_multiplier,omitempty"`
}

// SiteExportData represents the complete export data structure
type SiteExportData struct {
	Version     string             `json:"version"`
	ExportedAt  string             `json:"exported_at"`
	AutoCheckin *AutoCheckinConfig `json:"auto_checkin,omitempty"`
	AutoBalance *AutoBalanceConfig `json:"auto_balance,omitempty"`
	Sites       []SiteExportInfo   `json:"sites"`
}

// ExportSites exports all managed sites with optional schedule configuration.
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
			return nil, err
		}
		exportData.AutoCheckin = cfg
		balanceCfg, err := s.GetAutoBalanceConfig(ctx)
		if err != nil {
			return nil, err
		}
		exportData.AutoBalance = balanceCfg
	}

	// Export sites
	for _, site := range sites {
		// Backup exports preserve unknown future auth types; only normalized no-auth values drop credentials.
		authType := services.NormalizeManagedSiteAuthType(site.AuthType)
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
			UseProxy:           site.UseProxy,
			ProxyURL:           utils.ProxyURLForExport(site.ProxyURL, plainMode),
			BypassMethod:       site.BypassMethod,
			AuthType:           authType,
			BalanceMultiplier:  normalizeManagedSiteBalanceMultiplier(site.BalanceMultiplier),
		}

		// user_id is an independent sensitive field and remains exportable even when auth_type is none.
		if site.UserID != "" {
			if plainMode {
				// Decrypt for plain export
				decrypted, err := s.encryptionSvc.Decrypt(site.UserID)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt user_id for managed site %d", site.ID)
				}
				siteInfo.UserID = decrypted
			} else {
				// Keep encrypted for encrypted export
				siteInfo.UserID = site.UserID
			}
		}

		// Handle auth value based on export mode
		if services.ManagedSiteAuthTypeRequiresCredential(authType) && site.AuthValue != "" {
			if plainMode {
				// Decrypt for plain export
				decrypted, err := s.encryptionSvc.Decrypt(site.AuthValue)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt auth value for managed site %d", site.ID)
				}
				siteInfo.AuthValue = decrypted
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
// Schedule settings use a short transaction; site rows remain independent so valid sites
// can still be imported when another row fails validation.
func (s *SiteService) ImportSites(ctx context.Context, data *SiteExportData, plainMode bool) (int, int, error) {
	if data == nil {
		return 0, 0, nil
	}
	var normalizedAutoCheckin AutoCheckinConfig
	if data.AutoCheckin != nil {
		mode, err := validateAutoCheckinConfig(*data.AutoCheckin)
		if err != nil {
			return 0, 0, err
		}
		normalizedAutoCheckin = normalizeAutoCheckinConfig(*data.AutoCheckin, mode)
	}
	if data.AutoBalance != nil {
		if err := validateAutoBalanceConfig(*data.AutoBalance); err != nil {
			return 0, 0, err
		}
	}
	if data.AutoCheckin != nil || data.AutoBalance != nil {
		// Keep schedule writes atomic and short; site rows intentionally retain partial-success semantics.
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			st, err := ensureSettingsRowInDB(tx)
			if err != nil {
				return err
			}
			updates := make(map[string]any, 12)
			if data.AutoCheckin != nil {
				for key, value := range autoCheckinConfigUpdates(normalizedAutoCheckin) {
					updates[key] = value
				}
			}
			if data.AutoBalance != nil {
				for key, value := range autoBalanceConfigUpdates(*data.AutoBalance) {
					updates[key] = value
				}
			}
			updates["updated_at"] = time.Now()
			if err := tx.Model(&ManagedSiteSetting{}).
				Where("id = ?", st.ID).
				Updates(updates).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
			return nil
		})
		if err != nil {
			return 0, 0, err
		}
		s.NotifyScheduleConfigUpdated()
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
		// Zero is the JSON zero value for legacy exports that predate this field.
		if siteInfo.BalanceMultiplier < 0 {
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
		if !isValidBypassMethod(siteInfo.BypassMethod) {
			logrus.Warnf("Skipping site %s: invalid bypass_method", name)
			skipped++
			continue
		}

		// Site imports activate only auth methods supported by this binary; unknown future types stay disabled.
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

		// Ensure checkin flags are consistent (merge auto_checkin_enabled into checkin_enabled for backward compatibility)
		checkInEnabled := siteInfo.CheckInEnabled || siteInfo.AutoCheckInEnabled
		plainAuthValue := ""
		if encryptedAuth != "" {
			plainAuthValue, err = s.encryptionSvc.Decrypt(encryptedAuth)
			if err != nil {
				logrus.WithError(err).Warnf("Failed to decrypt auth value for site %s", name)
				skipped++
				continue
			}
		}
		plainUserID := ""
		if encryptedUserID != "" {
			plainUserID, err = s.encryptionSvc.Decrypt(encryptedUserID)
			if err != nil {
				logrus.WithError(err).Warnf("Failed to decrypt user_id for site %s", name)
				skipped++
				continue
			}
		}
		bypassMethod := normalizeBypassMethod(siteInfo.BypassMethod)
		if err := validateManagedSiteConfiguration(
			siteType,
			authType,
			plainAuthValue,
			plainUserID,
			strings.TrimSpace(siteInfo.CustomCheckInURL),
			bypassMethod,
			siteInfo.Enabled,
			checkInEnabled,
		); err != nil {
			logrus.WithError(err).Warnf("Skipping site %s: invalid provider configuration", name)
			skipped++
			continue
		}

		// Generate unique name if conflict exists
		uniqueName, err := s.generateUniqueSiteName(ctx, name)
		if err != nil {
			logrus.WithError(err).Warnf("Failed to generate unique name for site %s", name)
			skipped++
			continue
		}

		site := &ManagedSite{
			Name:              uniqueName,
			Notes:             strings.TrimSpace(siteInfo.Notes),
			Description:       strings.TrimSpace(siteInfo.Description),
			Sort:              siteInfo.Sort,
			Enabled:           siteInfo.Enabled,
			BaseURL:           baseURL,
			SiteType:          siteType,
			UserID:            encryptedUserID,
			CheckInPageURL:    strings.TrimSpace(siteInfo.CheckInPageURL),
			CheckInAvailable:  siteInfo.CheckInAvailable,
			CheckInEnabled:    checkInEnabled,
			CustomCheckInURL:  strings.TrimSpace(siteInfo.CustomCheckInURL),
			UseProxy:          siteInfo.UseProxy,
			ProxyURL:          strings.TrimSpace(siteInfo.ProxyURL),
			BypassMethod:      bypassMethod,
			AuthType:          authType,
			AuthValue:         encryptedAuth,
			BalanceMultiplier: normalizeManagedSiteBalanceMultiplier(siteInfo.BalanceMultiplier),
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

	// Invalidate cache after import
	if imported > 0 {
		s.InvalidateSiteListCache()
	}

	return imported, skipped, nil
}

// mergeAuthValues merges new auth values with existing ones for multi-auth support.
// This prevents partial updates from clearing unconfigured auth types.
//
// Parameters:
//   - authType: comma-separated auth types (e.g., "access_token,cookie")
//   - existingEncrypted: existing encrypted auth value from database
//   - newValue: new auth value from update request (plain text or JSON)
//
// Returns merged value in appropriate format (plain text for single auth, JSON for multi-auth).
func (s *SiteService) mergeAuthValues(authType, existingEncrypted, newValue string) (string, error) {
	// Parse auth types
	authTypes := strings.Split(authType, ",")
	var cleanAuthTypes []string
	for _, at := range authTypes {
		at = strings.TrimSpace(at)
		if at != "" && at != AuthTypeNone {
			cleanAuthTypes = append(cleanAuthTypes, at)
		}
	}
	configuredAuthTypes := make(map[string]struct{}, len(cleanAuthTypes))
	for _, at := range cleanAuthTypes {
		configuredAuthTypes[at] = struct{}{}
	}
	filterAuthValues := func(values map[string]string) map[string]string {
		filtered := make(map[string]string, len(values))
		for key, value := range values {
			if !isConfiguredAuthValueKey(key, configuredAuthTypes) {
				continue
			}
			filtered[key] = value
		}
		return filtered
	}
	accessTokenUpdate := func(values map[string]string) (string, bool) {
		if _, configured := configuredAuthTypes[AuthTypeAccessToken]; !configured {
			return "", false
		}
		if value, ok := values[AuthTypeAccessToken]; ok && strings.TrimSpace(value) != "" {
			return value, true
		}
		if value, ok := values[authFieldAuthToken]; ok && strings.TrimSpace(value) != "" {
			return value, true
		}
		return "", false
	}

	// Plain single-auth updates are replacements. Sub2API keeps refresh_token by
	// sending JSON explicitly, so non-JSON values must not keep supplemental fields.
	if len(cleanAuthTypes) <= 1 {
		var newJSON map[string]string
		if err := json.Unmarshal([]byte(newValue), &newJSON); err != nil {
			return newValue, nil
		}

		existingValues := make(map[string]string)
		if existingEncrypted != "" {
			if decrypted, err := s.encryptionSvc.Decrypt(existingEncrypted); err == nil {
				var existingJSON map[string]string
				if err := json.Unmarshal([]byte(decrypted), &existingJSON); err == nil {
					existingValues = filterAuthValues(existingJSON)
				} else if len(cleanAuthTypes) > 0 && strings.TrimSpace(decrypted) != "" {
					existingValues[cleanAuthTypes[0]] = decrypted
				}
			}
		}
		if newToken, ok := accessTokenUpdate(newJSON); ok &&
			strings.TrimSpace(existingValues[AuthTypeAccessToken]) != strings.TrimSpace(newToken) {
			delete(existingValues, authFieldTokenExpiresAt)
		}

		for k, v := range newJSON {
			if strings.TrimSpace(v) != "" && isConfiguredAuthValueKey(k, configuredAuthTypes) {
				existingValues[k] = v
			}
		}
		if len(existingValues) == 0 {
			return newValue, nil
		}
		mergedJSON, err := json.Marshal(existingValues)
		if err != nil {
			return "", fmt.Errorf("failed to marshal merged auth values: %w", err)
		}
		return string(mergedJSON), nil
	}

	// Multi-auth case: merge with existing values
	existingValues := make(map[string]string)

	// Decrypt and parse existing auth value if present
	if existingEncrypted != "" {
		decrypted, err := s.encryptionSvc.Decrypt(existingEncrypted)
		if err != nil {
			// If decryption fails, proceed without existing values (best effort)
			logrus.WithError(err).Warn("Failed to decrypt existing auth value during merge, proceeding without merge")
		} else {
			// Try to parse as JSON (multi-auth format)
			var jsonValues map[string]string
			if err := json.Unmarshal([]byte(decrypted), &jsonValues); err == nil {
				existingValues = filterAuthValues(jsonValues)
			} else {
				// Legacy single-auth format - assign to first auth type
				if len(cleanAuthTypes) > 0 {
					existingValues[cleanAuthTypes[0]] = decrypted
				}
			}
		}
	}

	// Parse new value (could be JSON or plain text)
	newValues := make(map[string]string)
	var jsonValues map[string]string
	if err := json.Unmarshal([]byte(newValue), &jsonValues); err == nil {
		// New value is JSON
		newValues = jsonValues
	} else {
		// New value is plain text - assign to first auth type
		if len(cleanAuthTypes) > 0 {
			newValues[cleanAuthTypes[0]] = newValue
		}
	}

	// New values override existing ones while preserving provider supplemental fields.
	mergedValues := filterAuthValues(existingValues)
	if newToken, ok := accessTokenUpdate(newValues); ok &&
		strings.TrimSpace(existingValues[AuthTypeAccessToken]) != strings.TrimSpace(newToken) {
		delete(mergedValues, authFieldTokenExpiresAt)
	}
	for key, value := range newValues {
		if strings.TrimSpace(value) == "" || !isConfiguredAuthValueKey(key, configuredAuthTypes) {
			continue
		}
		mergedValues[key] = value
	}

	// Return merged values as JSON
	mergedJSON, err := json.Marshal(mergedValues)
	if err != nil {
		return "", fmt.Errorf("failed to marshal merged auth values: %w", err)
	}

	return string(mergedJSON), nil
}

// reconcileAuthValueForTypeChange translates an existing credential snapshot
// when auth_type changes.  The old type is used for parsing legacy plaintext
// values so a token cannot accidentally become a cookie (or vice versa).
// Only credentials present in both type sets survive; Sub2API metadata is kept
// only when access_token itself remains configured.
func (s *SiteService) reconcileAuthValueForTypeChange(oldAuthType, newAuthType, existingEncrypted string) (string, error) {
	oldTypes := configuredAuthTypeSet(oldAuthType)
	newTypes := configuredAuthTypeSet(newAuthType)
	if len(newTypes) == 0 || strings.TrimSpace(existingEncrypted) == "" {
		return "", nil
	}

	decrypted, err := s.encryptionSvc.Decrypt(existingEncrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt existing auth value: %w", err)
	}
	oldConfig := parseAuthConfig(oldAuthType, decrypted)
	values := make(map[string]string, len(newTypes)+2)
	for authType := range newTypes {
		if _, shared := oldTypes[authType]; !shared {
			continue
		}
		value := strings.TrimSpace(oldConfig.GetAuthValue(authType))
		if value != "" {
			values[authType] = value
		}
	}

	// refresh_token and expiry metadata only have meaning alongside the shared
	// access token.  Discarding them on a cookie-only transition avoids stale
	// credentials being interpreted by a later provider.
	if _, accessShared := oldTypes[AuthTypeAccessToken]; accessShared {
		if _, accessConfigured := newTypes[AuthTypeAccessToken]; accessConfigured {
			for key, value := range oldConfig.SupplementalValues {
				if key == authFieldAuthToken || strings.TrimSpace(value) == "" {
					continue
				}
				values[key] = value
			}
		}
	}

	if len(values) == 0 {
		return "", nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal reconciled auth values: %w", err)
	}
	encrypted, err := s.encryptionSvc.Encrypt(string(data))
	if err != nil {
		return "", fmt.Errorf("encrypt reconciled auth values: %w", err)
	}
	return encrypted, nil
}

func configuredAuthTypeSet(authType string) map[string]struct{} {
	configured := make(map[string]struct{})
	for _, value := range strings.Split(authType, ",") {
		value = strings.TrimSpace(value)
		if value != "" && value != AuthTypeNone {
			configured[value] = struct{}{}
		}
	}
	return configured
}

// isConfiguredAuthValueKey keeps active credentials and provider metadata while
// dropping stale credentials for auth types that are no longer selected.
func isConfiguredAuthValueKey(key string, configured map[string]struct{}) bool {
	switch key {
	case AuthTypeAccessToken, AuthTypeCookie:
		_, ok := configured[key]
		return ok
	case authFieldAuthToken:
		_, ok := configured[AuthTypeAccessToken]
		return ok
	case authFieldRefreshToken, authFieldTokenExpiresAt:
		_, ok := configured[AuthTypeAccessToken]
		return ok
	default:
		return true
	}
}
