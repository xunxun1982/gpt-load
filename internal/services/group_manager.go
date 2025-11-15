package services

import (
	"context"
	"encoding/json"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/syncer"
	"gpt-load/internal/utils"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const GroupUpdateChannel = "groups:updated"

// GroupManager manages the caching of group data.
type GroupManager struct {
	syncer          *syncer.CacheSyncer[map[string]*models.Group]
	db              *gorm.DB
	store           store.Store
	settingsManager *config.SystemSettingsManager
	subGroupManager *SubGroupManager
}

// NewGroupManager creates a new, uninitialized GroupManager.
func NewGroupManager(
	db *gorm.DB,
	store store.Store,
	settingsManager *config.SystemSettingsManager,
	subGroupManager *SubGroupManager,
) *GroupManager {
	return &GroupManager{
		db:              db,
		store:           store,
		settingsManager: settingsManager,
		subGroupManager: subGroupManager,
	}
}

// Initialize sets up the CacheSyncer. This is called separately to handle potential
func (gm *GroupManager) Initialize() error {
loader := func() (map[string]*models.Group, error) {
		groups := make([]*models.Group, 0, 100)
ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		// Use Select to only fetch necessary fields, reducing data transfer and improving performance
		if err := gm.db.WithContext(ctx).Select(
			"id, name, display_name, description, group_type, enabled, upstreams, "+
				"validation_endpoint, channel_type, sort, test_model, param_overrides, "+
				"config, header_rules, model_mapping, model_redirect_rules, "+
				"model_redirect_strict, path_redirects, proxy_keys, last_validated_at, "+
				"created_at, updated_at",
		).Find(&groups).Error; err != nil {
			// If DB is locked or timed out, serve stale cache if available
			if gm.syncer != nil && (strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "busy") || strings.Contains(err.Error(), "interrupted") || err == context.DeadlineExceeded) {
				logrus.WithError(err).Warn("Group loader timed out/locked - returning stale cache")
				return gm.syncer.Get(), nil
			}
			return nil, fmt.Errorf("failed to load groups from db: %w", err)
		}

		// Load all sub-group relationships for aggregate groups (only valid ones with weight > 0)
allSubGroups := make([]models.GroupSubGroup, 0, 200)
		if err := gm.db.WithContext(ctx).Where("weight > 0").Find(&allSubGroups).Error; err != nil {
			if gm.syncer != nil && (strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "busy") || strings.Contains(err.Error(), "interrupted") || err == context.DeadlineExceeded) {
				logrus.WithError(err).Warn("Sub-groups loader timed out/locked - returning stale cache")
				return gm.syncer.Get(), nil
			}
			return nil, fmt.Errorf("failed to load valid sub groups: %w", err)
		}

		// Group sub-groups by aggregate group ID
		subGroupsByAggregateID := make(map[uint][]models.GroupSubGroup)
		for _, sg := range allSubGroups {
			subGroupsByAggregateID[sg.GroupID] = append(subGroupsByAggregateID[sg.GroupID], sg)
		}

		// Create group ID to group object mapping for sub-group lookups
		groupByID := make(map[uint]*models.Group)
		for _, group := range groups {
			groupByID[group.ID] = group
		}

		groupMap := make(map[string]*models.Group, len(groups))
		for _, group := range groups {
			g := *group
			g.EffectiveConfig = gm.settingsManager.GetEffectiveConfig(g.Config)
			g.ProxyKeysMap = utils.StringToSet(g.ProxyKeys, ",")

			// Parse header rules with error handling
			if len(group.HeaderRules) > 0 {
				if err := json.Unmarshal(group.HeaderRules, &g.HeaderRuleList); err != nil {
					logrus.WithError(err).WithField("group_name", g.Name).Warn("Failed to parse header rules for group")
					g.HeaderRuleList = []models.HeaderRule{}
				}
			} else {
				g.HeaderRuleList = []models.HeaderRule{}
			}


		// Parse model mapping cache for performance (deprecated, for backward compatibility)
		if g.ModelMapping != "" {
			modelMap, err := utils.ParseModelMapping(g.ModelMapping)
			if err != nil {
				logrus.WithError(err).WithField("group_name", g.Name).Warn("Failed to parse model mapping for group")
				g.ModelMappingCache = nil
			} else {
				g.ModelMappingCache = modelMap
			}
		}

		// Parse model redirect rules with error handling
		g.ModelRedirectMap = make(map[string]string)
		if len(group.ModelRedirectRules) > 0 {
			hasInvalidRules := false
			for key, value := range group.ModelRedirectRules {
				if valueStr, ok := value.(string); ok {
					g.ModelRedirectMap[key] = valueStr
				} else {
					logrus.WithFields(logrus.Fields{
						"group_name": g.Name,
						"rule_key":   key,
						"value_type": fmt.Sprintf("%T", value),
						"value":      value,
					}).Error("Invalid model redirect rule value type, skipping this rule")
					hasInvalidRules = true
				}
			}
			if hasInvalidRules {
				logrus.WithField("group_name", g.Name).Warn("Group has invalid model redirect rules, some rules were skipped. Please check the configuration.")
			}
		}

		// Parse path redirect rules (OpenAI only)
		if len(group.PathRedirects) > 0 {
			if err := json.Unmarshal(group.PathRedirects, &g.PathRedirectRuleList); err != nil {
				logrus.WithError(err).WithField("group_name", g.Name).Warn("Failed to parse path redirects for group")
				g.PathRedirectRuleList = []models.PathRedirectRule{}
			}
		} else {
			g.PathRedirectRuleList = []models.PathRedirectRule{}
		}

			// Load sub-groups for aggregate groups
			if g.GroupType == "aggregate" {
				if subGroups, ok := subGroupsByAggregateID[g.ID]; ok {
					g.SubGroups = make([]models.GroupSubGroup, len(subGroups))
					for i, sg := range subGroups {
						g.SubGroups[i] = sg
						if subGroup, exists := groupByID[sg.SubGroupID]; exists {
							g.SubGroups[i].SubGroupName = subGroup.Name
							g.SubGroups[i].SubGroupEnabled = subGroup.Enabled
						}
					}
				}
			}

			groupMap[g.Name] = &g
			logrus.WithFields(logrus.Fields{
			"group_name":                   g.Name,
			"effective_config":             g.EffectiveConfig,
			"header_rules_count":           len(g.HeaderRuleList),
			"model_mapping":                g.ModelMapping != "",           // deprecated
			"model_redirect_rules_count":   len(g.ModelRedirectMap),
			"model_redirect_strict":        g.ModelRedirectStrict,
			"path_redirects":               len(g.PathRedirectRuleList),
			"sub_group_count":              len(g.SubGroups),
			}).Debug("Loaded group with effective config")
		}

		return groupMap, nil
	}

	afterReload := func(newCache map[string]*models.Group) {
		gm.subGroupManager.RebuildSelectors(newCache)
	}

	syncer, err := syncer.NewCacheSyncer(
		loader,
		gm.store,
		GroupUpdateChannel,
		logrus.WithField("syncer", "groups"),
		afterReload,
	)
	if err != nil {
		return fmt.Errorf("failed to create group syncer: %w", err)
	}
	gm.syncer = syncer
	return nil
}

// GetGroupByName retrieves a single group by its name from the cache.
func (gm *GroupManager) GetGroupByName(name string) (*models.Group, error) {
	if gm.syncer == nil {
		return nil, fmt.Errorf("GroupManager is not initialized")
	}

	groups := gm.syncer.Get()
	group, ok := groups[name]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return group, nil
}

// GetGroupByID retrieves a single group by its ID from the cache (linear scan over cached map)
func (gm *GroupManager) GetGroupByID(id uint) (*models.Group, error) {
	if gm.syncer == nil {
		return nil, fmt.Errorf("GroupManager is not initialized")
	}
	groups := gm.syncer.Get()
	for _, g := range groups {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

// Invalidate triggers a cache reload across all instances.
func (gm *GroupManager) Invalidate() error {
	if gm.syncer == nil {
		return fmt.Errorf("GroupManager is not initialized")
	}
	return gm.syncer.Invalidate()
}

// Stop gracefully stops the GroupManager's background syncer.
func (gm *GroupManager) Stop(ctx context.Context) {
	if gm.syncer != nil {
		gm.syncer.Stop()
	}
}
