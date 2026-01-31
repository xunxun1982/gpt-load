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
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const GroupUpdateChannel = "groups:updated"

const (
	defaultDBLookupTimeoutMs = 1200
	minDBLookupTimeoutMs     = 50
)

// getDBLookupTimeout returns the timeout for group-related DB lookups.
// It falls back to a sane default if the environment variable is not set or invalid.
func getDBLookupTimeout() time.Duration {
	if v := os.Getenv("DB_LOOKUP_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			if ms >= minDBLookupTimeoutMs {
				return time.Duration(ms) * time.Millisecond
			}
			logrus.Warnf("DB_LOOKUP_TIMEOUT_MS value %d is below minimum %d, using default %dms", ms, minDBLookupTimeoutMs, defaultDBLookupTimeoutMs)
		} else {
			logrus.Warnf("Invalid DB_LOOKUP_TIMEOUT_MS value '%s', using default %dms", v, defaultDBLookupTimeoutMs)
		}
	}
	return time.Duration(defaultDBLookupTimeoutMs) * time.Millisecond
}

type groupCache struct {
	ByName map[string]*models.Group
	ByID   map[uint]*models.Group
}

// GroupManager manages the caching of group data.
type GroupManager struct {
	syncer                    *syncer.CacheSyncer[groupCache]
	db                        *gorm.DB
	store                     store.Store
	settingsManager           *config.SystemSettingsManager
	subGroupManager           *SubGroupManager
	CacheInvalidationCallback func() // Callback to invalidate other caches (e.g., GroupService list cache)
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
	loader := func() (groupCache, error) {
		groups := make([]*models.Group, 0, 100)

		// Use a short timeout per query to keep startup fast while avoiding long blocking.
		// Groups and sub-groups each get their own context budget so one slow query
		// does not consume the entire timeout window. The timeout is configurable
		// via the DB_LOOKUP_TIMEOUT_MS environment variable.
		timeout := getDBLookupTimeout()

		maxAttempts := 1
		if gm.syncer == nil {
			maxAttempts = 3
		}

		var loadErr error
		for attempt := 0; attempt < maxAttempts; attempt++ {
			groups = groups[:0]
			groupsCtx, groupsCancel := context.WithTimeout(context.Background(), timeout)
			err := gm.db.WithContext(groupsCtx).Select(
				"id, name, display_name, description, group_type, enabled, upstreams, " +
					"validation_endpoint, channel_type, sort, test_model, param_overrides, " +
					"config, header_rules, model_mapping, model_redirect_rules, " +
					"model_redirect_rules_v2, model_redirect_strict, custom_model_names, " +
					"preconditions, path_redirects, proxy_keys, parent_group_id, bound_site_id, " +
					"last_validated_at, created_at, updated_at",
			).Find(&groups).Error
			groupsCancel()
			if err == nil {
				loadErr = nil
				break
			}
			loadErr = err

			// If DB is locked or timed out, serve stale cache if available.
			if gm.syncer != nil && utils.IsTransientDBError(err) {
				logrus.WithError(err).Warn("Group loader timed out/locked - returning stale cache")
				return gm.syncer.Get(), nil
			}

			// On initial load, retry transient failures with exponential backoff.
			if gm.syncer == nil && utils.IsTransientDBError(err) && attempt < maxAttempts-1 {
				backoff := 200 * time.Millisecond * time.Duration(1<<attempt)
				time.Sleep(backoff)
				timeout *= 2
				if timeout > 5*time.Second {
					timeout = 5 * time.Second
				}
				continue
			}
			break
		}
		if loadErr != nil {
			return groupCache{}, fmt.Errorf("failed to load groups from db: %w", loadErr)
		}

		// Load all sub-group relationships for aggregate groups (only valid ones with weight > 0).
		allSubGroups := make([]models.GroupSubGroup, 0, 200)
		subCtx, subCancel := context.WithTimeout(context.Background(), timeout)
		defer subCancel()
		if err := gm.db.WithContext(subCtx).
			Select("group_id, sub_group_id, weight").
			Where("weight > ?", 0).
			Find(&allSubGroups).Error; err != nil {
			if utils.IsTransientDBError(err) {
				// Serve stale cache if available
				if gm.syncer != nil {
					logrus.WithError(err).Warn("Sub-groups loader timed out/locked - returning stale cache")
					return gm.syncer.Get(), nil
				}
				// On initial load we have no previous cache; for transient issues it is
				// better to start with groups only than to fail the whole application.
				logrus.WithError(err).Warn("Sub-groups loader timed out/locked on initial load - continuing without sub-groups")
				allSubGroups = nil
			} else {
				return groupCache{}, fmt.Errorf("failed to load valid sub groups: %w", err)
			}
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

		cache := groupCache{
			ByName: make(map[string]*models.Group, len(groups)),
			ByID:   make(map[uint]*models.Group, len(groups)),
		}
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

			// Parse model redirect rules with error handling (V1: one-to-one mapping)
			// NOTE: V1 rules are migrated to V2 during group create/update (v1.11.1)
			// Runtime code checks both V1 and V2 for backward compatibility with existing groups
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

			// Parse V2 model redirect rules (one-to-many mapping with weighted selection)
			g.ModelRedirectMapV2 = parseModelRedirectRulesV2(group.ModelRedirectRulesV2, g.Name)

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

			cache.ByName[g.Name] = &g
			cache.ByID[g.ID] = &g
		}

		return cache, nil
	}

	afterReload := func(newCache groupCache) {
		gm.subGroupManager.RebuildSelectors(newCache.ByName)
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

	cache := gm.syncer.Get()
	group, ok := cache.ByName[name]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return group, nil
}

// GetGroupByID retrieves a single group by its ID from the cache.
func (gm *GroupManager) GetGroupByID(id uint) (*models.Group, error) {
	if gm.syncer == nil {
		return nil, fmt.Errorf("GroupManager is not initialized")
	}
	cache := gm.syncer.Get()
	if g, ok := cache.ByID[id]; ok {
		return g, nil
	}
	return nil, gorm.ErrRecordNotFound
}

// Invalidate triggers a cache reload across all instances.
func (gm *GroupManager) Invalidate() error {
	if gm.syncer == nil {
		return fmt.Errorf("GroupManager is not initialized")
	}
	// Call the callback to invalidate other caches (e.g., GroupService list cache)
	if gm.CacheInvalidationCallback != nil {
		gm.CacheInvalidationCallback()
	}
	return gm.syncer.Invalidate()
}

// Reload forces an immediate synchronous reload of the cache from the database.
// This is useful when you need to ensure the cache is updated immediately after database changes.
// Unlike Invalidate(), this method blocks until the cache is fully reloaded.
func (gm *GroupManager) Reload() error {
	if gm.syncer == nil {
		return fmt.Errorf("GroupManager is not initialized")
	}
	// Call the callback to invalidate other caches (e.g., GroupService list cache)
	if gm.CacheInvalidationCallback != nil {
		gm.CacheInvalidationCallback()
	}
	return gm.syncer.Reload()
}

// Stop gracefully stops the GroupManager's background syncer.
func (gm *GroupManager) Stop(ctx context.Context) {
	if gm.syncer != nil {
		gm.syncer.Stop()
	}
}

// parseModelRedirectRulesV2 parses V2 model redirect rules from JSON.
// Returns nil if the input is empty or parsing fails.
func parseModelRedirectRulesV2(rulesJSON []byte, groupName string) map[string]*models.ModelRedirectRuleV2 {
	if len(rulesJSON) == 0 {
		return nil
	}

	var rules map[string]*models.ModelRedirectRuleV2
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		logrus.WithError(err).WithField("group_name", groupName).Warn("Failed to parse V2 model redirect rules")
		return nil
	}

	// Validate rules and log warnings for invalid configurations
	for sourceModel, rule := range rules {
		if rule == nil || len(rule.Targets) == 0 {
			logrus.WithFields(logrus.Fields{
				"group_name":   groupName,
				"source_model": sourceModel,
			}).Warn("V2 redirect rule has no targets, will be skipped")
			delete(rules, sourceModel)
			continue
		}

		// Validate each target
		validTargetCount := 0
		for i, target := range rule.Targets {
			if target.Model == "" {
				logrus.WithFields(logrus.Fields{
					"group_name":   groupName,
					"source_model": sourceModel,
					"target_index": i,
				}).Warn("V2 redirect target has empty model name")
				continue
			}
			if target.IsEnabled() && target.GetWeight() > 0 {
				validTargetCount++
			}
		}

		// Delete rule if no valid enabled targets to prevent runtime errors in SelectTarget()
		if validTargetCount == 0 {
			logrus.WithFields(logrus.Fields{
				"group_name":   groupName,
				"source_model": sourceModel,
			}).Warn("V2 redirect rule has no valid enabled targets, removing from map")
			delete(rules, sourceModel)
		}
	}

	if len(rules) == 0 {
		return nil
	}

	return rules
}
