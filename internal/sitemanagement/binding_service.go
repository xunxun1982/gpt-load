package sitemanagement

import (
	"context"
	"errors"
	"sync"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// sitesForBindingCacheEntry represents cached sites data
type sitesForBindingCacheEntry struct {
	Data      []ManagedSiteDTO
	ExpiresAt time.Time
}

// BindingService handles bidirectional binding between groups and sites
type BindingService struct {
	db          *gorm.DB
	readDB      *gorm.DB // Separate read connection for SQLite WAL mode
	taskService *services.TaskService
	// CacheInvalidationCallback is called after binding/unbinding to invalidate group list cache
	CacheInvalidationCallback func()
	// SyncChildGroupsEnabledCallback syncs enabled status to child groups when parent group is enabled/disabled
	SyncChildGroupsEnabledCallback func(ctx context.Context, parentGroupID uint, enabled bool) error
	// Cache for ListSitesForBinding
	cache    *sitesForBindingCacheEntry
	cacheMu  sync.RWMutex
	cacheTTL time.Duration
}

// NewBindingService creates a new binding service
func NewBindingService(db *gorm.DB, readDB services.ReadOnlyDB, taskService *services.TaskService) *BindingService {
	rdb := readDB.DB
	if rdb == nil {
		rdb = db
	}
	return &BindingService{
		db:          db,
		readDB:      rdb,
		taskService: taskService,
		cacheTTL:    30 * time.Second,
	}
}

// BindGroupToSite binds a standard group to a managed site.
// Multiple groups can bind to the same site (many-to-one relationship).
// Only standard groups (not aggregate, not child groups) can be bound to sites.
func (s *BindingService) BindGroupToSite(ctx context.Context, groupID uint, siteID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Validate group exists and is a standard group (not aggregate, not child)
		var group models.Group
		if err := tx.First(&group, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return services.NewI18nError(app_errors.ErrResourceNotFound, "binding.group_not_found", nil)
			}
			return app_errors.ParseDBError(err)
		}

		if group.GroupType == "aggregate" {
			return services.NewI18nError(app_errors.ErrValidation, "binding.aggregate_cannot_bind", nil)
		}
		if group.ParentGroupID != nil {
			return services.NewI18nError(app_errors.ErrValidation, "binding.child_group_cannot_bind", nil)
		}

		// Validate site exists
		var site ManagedSite
		if err := tx.First(&site, siteID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return services.NewI18nError(app_errors.ErrResourceNotFound, "binding.site_not_found", nil)
			}
			return app_errors.ParseDBError(err)
		}

		// Check if group is already bound to another site
		if group.BoundSiteID != nil && *group.BoundSiteID != siteID {
			return services.NewI18nError(app_errors.ErrValidation, "binding.group_already_bound", nil)
		}

		// Note: Multiple groups can bind to the same site (many-to-one)
		// No need to check if site is already bound to another group

		// Update group's bound_site_id
		if err := tx.Model(&models.Group{}).Where("id = ?", groupID).
			Update("bound_site_id", siteID).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"groupID": groupID,
			"siteID":  siteID,
		}).Info("Bound group to site")

		return nil
	})
	if err == nil && s.CacheInvalidationCallback != nil {
		s.CacheInvalidationCallback()
	}
	return err
}

// UnbindGroupFromSite removes the binding between a group and site
func (s *BindingService) UnbindGroupFromSite(ctx context.Context, groupID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get group to find bound site
		var group models.Group
		if err := tx.First(&group, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return services.NewI18nError(app_errors.ErrResourceNotFound, "binding.group_not_found", nil)
			}
			return app_errors.ParseDBError(err)
		}

		if group.BoundSiteID == nil {
			// Already unbound, nothing to do
			return nil
		}

		siteID := *group.BoundSiteID

		// Clear group's bound_site_id
		if err := tx.Model(&models.Group{}).Where("id = ?", groupID).
			Update("bound_site_id", nil).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"groupID": groupID,
			"siteID":  siteID,
		}).Info("Unbound group from site")

		return nil
	})
	if err == nil && s.CacheInvalidationCallback != nil {
		s.CacheInvalidationCallback()
	}
	return err
}

// UnbindSiteFromGroup removes all group bindings from a site (many-to-one)
func (s *BindingService) UnbindSiteFromGroup(ctx context.Context, siteID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Verify site exists
		var site ManagedSite
		if err := tx.First(&site, siteID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return services.NewI18nError(app_errors.ErrResourceNotFound, "binding.site_not_found", nil)
			}
			return app_errors.ParseDBError(err)
		}

		// Clear bound_site_id for all groups bound to this site (single UPDATE)
		result := tx.Model(&models.Group{}).
			Where("bound_site_id = ?", siteID).
			Update("bound_site_id", nil)
		if result.Error != nil {
			return app_errors.ParseDBError(result.Error)
		}

		if result.RowsAffected > 0 {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"siteID":        siteID,
				"unboundGroups": result.RowsAffected,
			}).Info("Unbound all groups from site")
		}

		return nil
	})
	if err == nil && s.CacheInvalidationCallback != nil {
		s.CacheInvalidationCallback()
	}
	return err
}

// CheckGroupCanDelete checks if a group can be deleted (must unbind first)
func (s *BindingService) CheckGroupCanDelete(ctx context.Context, groupID uint) error {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // Group doesn't exist, can be deleted
		}
		return app_errors.ParseDBError(err)
	}

	if group.BoundSiteID != nil {
		return services.NewI18nError(app_errors.ErrValidation, "binding.must_unbind_before_delete_group", nil)
	}

	return nil
}

// CheckSiteCanDelete checks if a site can be deleted (must unbind all groups first)
func (s *BindingService) CheckSiteCanDelete(ctx context.Context, siteID uint) error {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // Site doesn't exist, can be deleted
		}
		return app_errors.ParseDBError(err)
	}

	// Check if any groups are bound to this site
	var boundCount int64
	if err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("bound_site_id = ?", siteID).
		Count(&boundCount).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if boundCount > 0 {
		return services.NewI18nError(app_errors.ErrValidation, "binding.must_unbind_before_delete_site", map[string]any{"count": boundCount})
	}

	return nil
}

// Note: SyncGroupEnabledToSite is intentionally removed.
// Group disable does NOT cascade to site (one-way sync: site -> groups only).

// SyncSiteEnabledToGroup syncs site enabled status to all bound groups and their child groups.
// When a site is disabled, all groups bound to it will be disabled.
// Note: Group disable does NOT cascade to site (one-way sync only).
//
// Design Decision: We intentionally query bound group IDs AFTER the UPDATE succeeds,
// rather than before. This ensures the main sync completes even if the child sync
// query fails. The UPDATE only modifies the 'enabled' field, not 'bound_site_id',
// so the subsequent query returns the same groups. The slight overhead of a second
// query is acceptable for this defensive approach.
func (s *BindingService) SyncSiteEnabledToGroup(ctx context.Context, siteID uint, enabled bool) error {
	// Update enabled status for all bound groups (single UPDATE)
	result := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("bound_site_id = ?", siteID).
		Update("enabled", enabled)
	if result.Error != nil {
		return app_errors.ParseDBError(result.Error)
	}

	if result.RowsAffected == 0 {
		return nil // No bound groups, nothing to sync
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"siteID":     siteID,
		"enabled":    enabled,
		"groupCount": result.RowsAffected,
	}).Info("Synced site enabled status to all bound groups")

	// Sync enabled status to child groups of each bound group
	if s.SyncChildGroupsEnabledCallback != nil {
		// Only fetch group IDs for child sync (minimal data)
		var boundGroupIDs []uint
		if err := s.db.WithContext(ctx).Model(&models.Group{}).
			Where("bound_site_id = ?", siteID).
			Pluck("id", &boundGroupIDs).Error; err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("Failed to fetch bound group IDs for child sync")
			return nil // Don't fail, main sync already succeeded
		}

		for _, groupID := range boundGroupIDs {
			if err := s.SyncChildGroupsEnabledCallback(ctx, groupID, enabled); err != nil {
				logrus.WithContext(ctx).WithError(err).WithField("groupID", groupID).
					Warn("Failed to sync enabled status to child groups after site enabled change")
				// Don't fail the operation, just log the warning
			}
		}
	}

	return nil
}

// GetBoundSiteInfo returns bound site info for a group
func (s *BindingService) GetBoundSiteInfo(ctx context.Context, groupID uint) (*ManagedSiteDTO, error) {
	var group models.Group
	if err := s.readDB.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if group.BoundSiteID == nil {
		return nil, nil
	}

	var site ManagedSite
	if err := s.readDB.WithContext(ctx).First(&site, *group.BoundSiteID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, app_errors.ParseDBError(err)
	}

	return &ManagedSiteDTO{
		ID:   site.ID,
		Name: site.Name,
	}, nil
}

// GetBoundGroupInfo returns all groups bound to a site (many-to-one relationship)
func (s *BindingService) GetBoundGroupInfo(ctx context.Context, siteID uint) ([]BoundGroupInfo, error) {
	var site ManagedSite
	if err := s.readDB.WithContext(ctx).First(&site, siteID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Find all groups bound to this site
	var groups []models.Group
	if err := s.readDB.WithContext(ctx).
		Select("id", "name", "display_name", "enabled").
		Where("bound_site_id = ?", siteID).
		Order("sort ASC, id ASC").
		Find(&groups).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	result := make([]BoundGroupInfo, 0, len(groups))
	for _, g := range groups {
		result = append(result, BoundGroupInfo{
			ID:          g.ID,
			Name:        g.Name,
			DisplayName: g.DisplayName,
			Enabled:     g.Enabled,
		})
	}

	return result, nil
}

// ListSitesForBinding returns sites available for binding (sorted by sort order)
// Each site includes the count of groups currently bound to it.
func (s *BindingService) ListSitesForBinding(ctx context.Context) ([]ManagedSiteDTO, error) {
	// Check cache first
	s.cacheMu.RLock()
	if s.cache != nil && time.Now().Before(s.cache.ExpiresAt) {
		result := s.cache.Data
		s.cacheMu.RUnlock()
		return result, nil
	}
	// Check if task is running and we have stale cache
	hasStaleCache := s.cache != nil && len(s.cache.Data) > 0
	s.cacheMu.RUnlock()

	if hasStaleCache && s.isTaskRunning() {
		s.cacheMu.RLock()
		result := s.cache.Data
		s.cacheMu.RUnlock()
		logrus.Debug("ListSitesForBinding returning stale cache during task execution")
		return result, nil
	}

	var sites []ManagedSite
	// Use readDB with timeout for read operations
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := s.readDB.WithContext(queryCtx).
		Order("sort ASC, id ASC").
		Find(&sites).Error; err != nil {
		// Return stale cache on transient errors
		if utils.IsTransientDBError(err) {
			s.cacheMu.RLock()
			if s.cache != nil && len(s.cache.Data) > 0 {
				result := s.cache.Data
				s.cacheMu.RUnlock()
				logrus.WithError(err).Warn("ListSitesForBinding transient error - returning stale cache")
				return result, nil
			}
			s.cacheMu.RUnlock()
		}
		return nil, app_errors.ParseDBError(err)
	}

	// Batch query to get bound group counts for all sites
	siteIDs := make([]uint, len(sites))
	for i, site := range sites {
		siteIDs[i] = site.ID
	}

	// Get bound group counts per site using single query
	type siteGroupCount struct {
		SiteID uint
		Count  int64
	}
	var counts []siteGroupCount
	if len(siteIDs) > 0 {
		if err := s.readDB.WithContext(queryCtx).
			Model(&models.Group{}).
			Select("bound_site_id as site_id, COUNT(*) as count").
			Where("bound_site_id IN ?", siteIDs).
			Group("bound_site_id").
			Scan(&counts).Error; err != nil {
			logrus.WithError(err).Warn("Failed to get bound group counts")
			// Continue without counts
		}
	}

	// Build count map for O(1) lookup
	countMap := make(map[uint]int64, len(counts))
	for _, c := range counts {
		countMap[c.SiteID] = c.Count
	}

	result := make([]ManagedSiteDTO, 0, len(sites))
	for _, site := range sites {
		boundCount := countMap[site.ID]
		result = append(result, ManagedSiteDTO{
			ID:              site.ID,
			Name:            site.Name,
			Sort:            site.Sort,
			Enabled:         site.Enabled,
			BoundGroupCount: boundCount,
		})
	}

	// Update cache
	s.cacheMu.Lock()
	s.cache = &sitesForBindingCacheEntry{
		Data:      result,
		ExpiresAt: time.Now().Add(s.cacheTTL),
	}
	s.cacheMu.Unlock()

	return result, nil
}

// isTaskRunning checks if an import or delete task is currently running.
// Note: This helper is intentionally duplicated from ChildGroupService rather than extracted
// to a shared location because the function is simple and extracting would create unnecessary
// cross-package dependencies.
func (s *BindingService) isTaskRunning() bool {
	if s.taskService == nil {
		return false
	}
	status, err := s.taskService.GetTaskStatus()
	if err != nil {
		return false
	}
	return status.IsRunning
}
