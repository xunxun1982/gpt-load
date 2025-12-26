package sitemanagement

import (
	"context"
	"errors"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/services"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// BindingService handles bidirectional binding between groups and sites
type BindingService struct {
	db *gorm.DB
	// CacheInvalidationCallback is called after binding/unbinding to invalidate group list cache
	CacheInvalidationCallback func()
}

// NewBindingService creates a new binding service
func NewBindingService(db *gorm.DB) *BindingService {
	return &BindingService{db: db}
}

// BindGroupToSite binds a standard group to a managed site (bidirectional)
// Only standard groups (not aggregate, not child groups) can be bound to sites
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

		// Check if site is already bound to another group
		if site.BoundGroupID != nil && *site.BoundGroupID != groupID {
			return services.NewI18nError(app_errors.ErrValidation, "binding.site_already_bound", nil)
		}

		// Update group's bound_site_id
		if err := tx.Model(&models.Group{}).Where("id = ?", groupID).
			Update("bound_site_id", siteID).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		// Update site's bound_group_id
		if err := tx.Model(&ManagedSite{}).Where("id = ?", siteID).
			Update("bound_group_id", groupID).Error; err != nil {
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

		// Clear site's bound_group_id
		if err := tx.Model(&ManagedSite{}).Where("id = ?", siteID).
			Update("bound_group_id", nil).Error; err != nil {
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

// UnbindSiteFromGroup removes the binding from site side
func (s *BindingService) UnbindSiteFromGroup(ctx context.Context, siteID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get site to find bound group
		var site ManagedSite
		if err := tx.First(&site, siteID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return services.NewI18nError(app_errors.ErrResourceNotFound, "binding.site_not_found", nil)
			}
			return app_errors.ParseDBError(err)
		}

		if site.BoundGroupID == nil {
			// Already unbound, nothing to do
			return nil
		}

		groupID := *site.BoundGroupID

		// Clear site's bound_group_id
		if err := tx.Model(&ManagedSite{}).Where("id = ?", siteID).
			Update("bound_group_id", nil).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		// Clear group's bound_site_id
		if err := tx.Model(&models.Group{}).Where("id = ?", groupID).
			Update("bound_site_id", nil).Error; err != nil {
			return app_errors.ParseDBError(err)
		}

		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"groupID": groupID,
			"siteID":  siteID,
		}).Info("Unbound site from group")

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

// CheckSiteCanDelete checks if a site can be deleted (must unbind first)
func (s *BindingService) CheckSiteCanDelete(ctx context.Context, siteID uint) error {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // Site doesn't exist, can be deleted
		}
		return app_errors.ParseDBError(err)
	}

	if site.BoundGroupID != nil {
		return services.NewI18nError(app_errors.ErrValidation, "binding.must_unbind_before_delete_site", nil)
	}

	return nil
}

// SyncGroupEnabledToSite syncs group enabled status to bound site
func (s *BindingService) SyncGroupEnabledToSite(ctx context.Context, groupID uint, enabled bool) error {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if group.BoundSiteID == nil {
		return nil // No bound site, nothing to sync
	}

	if err := s.db.WithContext(ctx).Model(&ManagedSite{}).
		Where("id = ?", *group.BoundSiteID).
		Update("enabled", enabled).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"groupID": groupID,
		"siteID":  *group.BoundSiteID,
		"enabled": enabled,
	}).Info("Synced group enabled status to bound site")

	return nil
}

// SyncSiteEnabledToGroup syncs site enabled status to bound group
func (s *BindingService) SyncSiteEnabledToGroup(ctx context.Context, siteID uint, enabled bool) error {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if site.BoundGroupID == nil {
		return nil // No bound group, nothing to sync
	}

	if err := s.db.WithContext(ctx).Model(&models.Group{}).
		Where("id = ?", *site.BoundGroupID).
		Update("enabled", enabled).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"siteID":  siteID,
		"groupID": *site.BoundGroupID,
		"enabled": enabled,
	}).Info("Synced site enabled status to bound group")

	return nil
}

// GetBoundSiteInfo returns bound site info for a group
func (s *BindingService) GetBoundSiteInfo(ctx context.Context, groupID uint) (*ManagedSiteDTO, error) {
	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, groupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if group.BoundSiteID == nil {
		return nil, nil
	}

	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, *group.BoundSiteID).Error; err != nil {
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

// GetBoundGroupInfo returns bound group info for a site
func (s *BindingService) GetBoundGroupInfo(ctx context.Context, siteID uint) (*models.Group, error) {
	var site ManagedSite
	if err := s.db.WithContext(ctx).First(&site, siteID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if site.BoundGroupID == nil {
		return nil, nil
	}

	var group models.Group
	if err := s.db.WithContext(ctx).First(&group, *site.BoundGroupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, app_errors.ParseDBError(err)
	}

	return &group, nil
}

// ListSitesForBinding returns sites available for binding (sorted by sort order)
func (s *BindingService) ListSitesForBinding(ctx context.Context) ([]ManagedSiteDTO, error) {
	var sites []ManagedSite
	if err := s.db.WithContext(ctx).
		Order("sort ASC, id ASC").
		Find(&sites).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	result := make([]ManagedSiteDTO, 0, len(sites))
	for _, site := range sites {
		result = append(result, ManagedSiteDTO{
			ID:           site.ID,
			Name:         site.Name,
			Sort:         site.Sort,
			Enabled:      site.Enabled,
			BoundGroupID: site.BoundGroupID,
		})
	}

	return result, nil
}
