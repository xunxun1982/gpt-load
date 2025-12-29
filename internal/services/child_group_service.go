package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// childGroupsCacheEntry represents cached child groups data
type childGroupsCacheEntry struct {
	Data      map[uint][]models.ChildGroupInfo
	ExpiresAt time.Time
}

// ChildGroupService handles child group operations.
type ChildGroupService struct {
	db           *gorm.DB
	readDB       *gorm.DB // Separate read connection for SQLite WAL mode
	groupManager *GroupManager
	keyService   *KeyService
	taskService  *TaskService
	// Cache for GetAllChildGroups
	cache   *childGroupsCacheEntry
	cacheMu sync.RWMutex
	cacheTTL time.Duration
}

// NewChildGroupService creates a new ChildGroupService instance.
func NewChildGroupService(db *gorm.DB, readDB ReadOnlyDB, groupManager *GroupManager, keyService *KeyService, taskService *TaskService) *ChildGroupService {
	rdb := readDB.DB
	if rdb == nil {
		rdb = db
	}
	return &ChildGroupService{
		db:           db,
		readDB:       rdb,
		groupManager: groupManager,
		keyService:   keyService,
		taskService:  taskService,
		cacheTTL:     30 * time.Second,
	}
}

// InvalidateCache clears the child groups cache.
// This should be called after creating, updating, or deleting child groups.
func (s *ChildGroupService) InvalidateCache() {
	s.cacheMu.Lock()
	s.cache = nil
	s.cacheMu.Unlock()
	logrus.Debug("Child groups cache invalidated")
}

// CreateChildGroupParams defines parameters for creating a child group.
type CreateChildGroupParams struct {
	ParentGroupID uint
	Name          string // Optional, auto-generated if empty
	DisplayName   string // Optional, auto-generated if empty
	Description   string
}

// buildChildGroupUpstream builds the upstream JSON for a child group.
// Uses localhost (127.0.0.1) to route requests through parent group's endpoint.
func buildChildGroupUpstream(parentGroupName string) (datatypes.JSON, error) {
	port := utils.ParseInteger(os.Getenv("PORT"), 3001)
	upstreamURL := fmt.Sprintf("http://127.0.0.1:%d/proxy/%s", port, parentGroupName)
	upstreams := []map[string]interface{}{
		{
			"url":    upstreamURL,
			"weight": 1,
		},
	}
	upstreamsJSON, err := json.Marshal(upstreams)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(upstreamsJSON), nil
}

// getFirstProxyKey extracts the first proxy key from a comma-separated list.
// NOTE: This is intentionally duplicated from group_service.go's getFirstProxyKeyFromString
// to avoid cross-service dependencies. Both implementations are simple and unlikely to diverge.
func getFirstProxyKey(proxyKeys string) string {
	if proxyKeys == "" {
		return ""
	}
	keys := strings.Split(proxyKeys, ",")
	if len(keys) > 0 {
		return strings.TrimSpace(keys[0])
	}
	return ""
}

// CreateChildGroup creates a new child group derived from a parent standard group.
// Child group uses parent's external endpoint as upstream.
// Child group's API key is set to parent's first proxy_key.
// Child group's proxy_keys is randomly generated.
func (s *ChildGroupService) CreateChildGroup(ctx context.Context, params CreateChildGroupParams) (*models.Group, error) {
	// Fetch parent group
	var parentGroup models.Group
	if err := s.db.WithContext(ctx).First(&parentGroup, params.ParentGroupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	// Validate parent group is a standard group (not aggregate, not a child group itself)
	if parentGroup.GroupType == "aggregate" {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.child_group_parent_must_be_standard", nil)
	}
	if parentGroup.ParentGroupID != nil {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.child_group_cannot_nest", nil)
	}

	// Get first proxy key from parent - this will be used as the child group's API key
	parentFirstProxyKey := getFirstProxyKey(parentGroup.ProxyKeys)
	if parentFirstProxyKey == "" {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.parent_group_no_proxy_keys", nil)
	}

	// Generate child group name if not provided
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = s.generateChildGroupName(ctx, parentGroup.Name)
	}

	// Validate name format
	if !isValidGroupName(name) {
		return nil, NewI18nError(app_errors.ErrValidation, "validation.invalid_group_name", nil)
	}

	// Generate display name if not provided
	displayName := strings.TrimSpace(params.DisplayName)
	if displayName == "" {
		// Extract child number from generated name (e.g., "parent_child1" -> 1)
		childNum := s.getNextChildNumber(ctx, params.ParentGroupID)
		if parentGroup.DisplayName != "" {
			displayName = fmt.Sprintf("%s (Child%d)", parentGroup.DisplayName, childNum)
		} else {
			displayName = fmt.Sprintf("%s (Child%d)", parentGroup.Name, childNum)
		}
	}

	// Build upstream using localhost with parent's endpoint path
	upstreamsJSON, err := buildChildGroupUpstream(parentGroup.Name)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Error("Failed to build child group upstream")
		return nil, app_errors.ErrInternalServer
	}

	// Generate random proxy_keys for child group (sk-child-xxxxxxxxxxxx format)
	childProxyKeys := generateChildGroupProxyKey()

	// Create child group with inherited properties
	// Child group inherits parent's sort value so all children have same sort and are ordered by name
	childGroup := models.Group{
		Name:               name,
		DisplayName:        displayName,
		Description:        strings.TrimSpace(params.Description),
		GroupType:          "standard",
		Enabled:            true,
		Upstreams:          upstreamsJSON,
		ChannelType:        parentGroup.ChannelType,
		TestModel:          parentGroup.TestModel,
		ValidationEndpoint: parentGroup.ValidationEndpoint,
		ParentGroupID:      &params.ParentGroupID,
		ProxyKeys:          childProxyKeys, // Randomly generated proxy key
		Config:             parentGroup.Config,
		Sort:               parentGroup.Sort, // Inherit parent's sort value
	}

	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, app_errors.ErrDatabase
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	if err := tx.Create(&childGroup).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	tx = nil // Prevent deferred rollback after successful commit

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"childGroupID":   childGroup.ID,
		"childGroupName": childGroup.Name,
		"parentGroupID":  parentGroup.ID,
	}).Info("Child group created successfully")

	// Add parent's first proxy_key as the child group's API key
	// NOTE: This is intentionally done AFTER the transaction commits. While this means the child
	// group could exist without its API key if AddMultipleKeys fails, this is acceptable because:
	// 1. AddMultipleKeys has its own transaction and memory store operations that can't easily
	//    accept an external transaction
	// 2. A child group without API key won't crash the system - it just can't proxy requests
	// 3. Admin can manually add keys or re-sync if needed
	// 4. Rolling back the child group creation on key failure would confuse users
	if s.keyService != nil {
		result, err := s.keyService.AddMultipleKeys(childGroup.ID, parentFirstProxyKey)
		if err != nil {
			logrus.WithContext(ctx).WithError(err).WithFields(logrus.Fields{
				"childGroupID":   childGroup.ID,
				"childGroupName": childGroup.Name,
			}).Error("Failed to add API key to child group")
			// Don't fail the creation, just log the error
		} else {
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				"childGroupID": childGroup.ID,
				"addedKeys":    result.AddedCount,
			}).Info("Added API key to child group")
		}
	}

	// Invalidate group cache
	if err := s.groupManager.Invalidate(); err != nil {
		logrus.WithContext(ctx).WithError(err).Error("failed to invalidate group cache after creating child group")
	}

	// Invalidate child groups cache so GetAllChildGroups returns fresh data
	s.InvalidateCache()

	return &childGroup, nil
}

// generateChildGroupProxyKey generates a random proxy key for child group.
// Format: sk-child-xxxxxxxxxxxxxxxxxxxx (32 random chars)
func generateChildGroupProxyKey() string {
	// Generate 32 random characters
	randomPart := ""
	for i := 0; i < 8; i++ {
		randomPart += utils.GenerateRandomSuffix()
	}
	return "sk-child-" + randomPart
}

// generateChildGroupName generates a unique child group name with suffix _child1, _child2, etc.
func (s *ChildGroupService) generateChildGroupName(ctx context.Context, parentName string) string {
	baseName := parentName + "_child"

	for i := 1; i <= 100; i++ {
		candidateName := fmt.Sprintf("%s%d", baseName, i)
		var count int64
		s.db.WithContext(ctx).Model(&models.Group{}).Where("name = ?", candidateName).Count(&count)
		if count == 0 {
			return candidateName
		}
	}

	// Fallback: use timestamp suffix (ctx.Value may return nil, so use UnixNano for safety)
	return fmt.Sprintf("%s_%d", baseName, time.Now().UnixNano())
}

// getNextChildNumber returns the next available child number for display name.
func (s *ChildGroupService) getNextChildNumber(ctx context.Context, parentGroupID uint) int {
	var count int64
	s.db.WithContext(ctx).Model(&models.Group{}).Where("parent_group_id = ?", parentGroupID).Count(&count)
	return int(count) + 1
}

// GetChildGroups returns all child groups for a parent group.
func (s *ChildGroupService) GetChildGroups(ctx context.Context, parentGroupID uint) ([]models.ChildGroupInfo, error) {
	var childGroups []models.Group
	if err := s.db.WithContext(ctx).
		Where("parent_group_id = ?", parentGroupID).
		Order("sort ASC, name ASC").
		Find(&childGroups).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	result := make([]models.ChildGroupInfo, 0, len(childGroups))
	for _, cg := range childGroups {
		result = append(result, models.ChildGroupInfo{
			ID:          cg.ID,
			Name:        cg.Name,
			DisplayName: cg.DisplayName,
			Enabled:     cg.Enabled,
			CreatedAt:   cg.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	return result, nil
}

// GetAllChildGroups returns all child groups grouped by parent group ID.
// This is more efficient than calling GetChildGroups for each parent group.
//
// Returns a deep copy of cached data to prevent data races. Although ChildGroupInfo
// contains only primitive fields, the map values are slices which are reference types.
// Returning direct references could allow callers to accidentally modify the shared
// underlying array through append operations.
func (s *ChildGroupService) GetAllChildGroups(ctx context.Context) (map[uint][]models.ChildGroupInfo, error) {
	// Check cache first
	s.cacheMu.RLock()
	if s.cache != nil && time.Now().Before(s.cache.ExpiresAt) {
		result := s.copyChildGroupsMap(s.cache.Data)
		s.cacheMu.RUnlock()
		return result, nil
	}
	// Check if task is running and we have stale cache
	hasStaleCache := s.cache != nil && len(s.cache.Data) > 0
	s.cacheMu.RUnlock()

	if hasStaleCache && s.isTaskRunning() {
		s.cacheMu.RLock()
		result := s.copyChildGroupsMap(s.cache.Data)
		s.cacheMu.RUnlock()
		logrus.Debug("GetAllChildGroups returning stale cache during task execution")
		return result, nil
	}

	var childGroups []models.Group
	// Use readDB with timeout for read operations
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := s.readDB.WithContext(queryCtx).
		Where("parent_group_id IS NOT NULL").
		Order("parent_group_id ASC, sort ASC, name ASC").
		Find(&childGroups).Error; err != nil {
		// Return stale cache on transient errors
		if isTransientDBError(err) {
			s.cacheMu.RLock()
			if s.cache != nil && len(s.cache.Data) > 0 {
				result := s.copyChildGroupsMap(s.cache.Data)
				s.cacheMu.RUnlock()
				logrus.WithError(err).Warn("GetAllChildGroups transient error - returning stale cache")
				return result, nil
			}
			s.cacheMu.RUnlock()
		}
		return nil, app_errors.ParseDBError(err)
	}

	result := make(map[uint][]models.ChildGroupInfo)
	for _, cg := range childGroups {
		if cg.ParentGroupID == nil {
			continue
		}
		parentID := *cg.ParentGroupID
		if _, exists := result[parentID]; !exists {
			result[parentID] = make([]models.ChildGroupInfo, 0)
		}
		result[parentID] = append(result[parentID], models.ChildGroupInfo{
			ID:          cg.ID,
			Name:        cg.Name,
			DisplayName: cg.DisplayName,
			Enabled:     cg.Enabled,
			CreatedAt:   cg.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	// Update cache
	s.cacheMu.Lock()
	s.cache = &childGroupsCacheEntry{
		Data:      result,
		ExpiresAt: time.Now().Add(s.cacheTTL),
	}
	s.cacheMu.Unlock()

	// Return a copy to prevent data races - the first caller should also receive
	// an independent copy, consistent with cache hit behavior (lines 311, 321, 340)
	return s.copyChildGroupsMap(result), nil
}

// isTaskRunning checks if an import or delete task is currently running.
func (s *ChildGroupService) isTaskRunning() bool {
	if s.taskService == nil {
		return false
	}
	status, err := s.taskService.GetTaskStatus()
	if err != nil {
		return false
	}
	return status.IsRunning
}

// copyChildGroupsMap creates a deep copy of the child groups map to prevent data races.
// Although ChildGroupInfo contains only primitive fields, the map values are slices
// which are reference types. This ensures callers cannot modify the cached data.
func (s *ChildGroupService) copyChildGroupsMap(src map[uint][]models.ChildGroupInfo) map[uint][]models.ChildGroupInfo {
	if src == nil {
		return nil
	}
	result := make(map[uint][]models.ChildGroupInfo, len(src))
	for parentID, children := range src {
		copied := make([]models.ChildGroupInfo, len(children))
		copy(copied, children)
		result[parentID] = copied
	}
	return result
}

// CountChildGroups returns the count of child groups for a parent group.
// NOTE: This method is intentionally duplicated in GroupService to avoid circular dependencies.
// Both services need this functionality independently.
func (s *ChildGroupService) CountChildGroups(ctx context.Context, parentGroupID uint) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&models.Group{}).
		Where("parent_group_id = ?", parentGroupID).
		Count(&count).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}
	return count, nil
}

// SyncChildGroupsOnParentUpdate updates all child groups when parent group's name or proxy_keys change.
// When parent name changes: update child groups' upstream URL.
// When parent proxy_keys changes: update child groups' API keys (not proxy_keys).
//
// NOTE: Similar logic exists in GroupService.syncChildGroupsOnParentUpdate. The duplication is
// intentional to avoid circular dependencies between services. GroupService uses its own method
// for inline updates during group save, while this method is exposed for external callers.
func (s *ChildGroupService) SyncChildGroupsOnParentUpdate(ctx context.Context, tx *gorm.DB, parentGroup *models.Group, oldName string, oldProxyKeys string) error {
	// Check if there are any child groups
	var childGroups []models.Group
	if err := tx.WithContext(ctx).
		Where("parent_group_id = ?", parentGroup.ID).
		Find(&childGroups).Error; err != nil {
		return app_errors.ParseDBError(err)
	}

	if len(childGroups) == 0 {
		return nil
	}

	nameChanged := oldName != parentGroup.Name
	proxyKeysChanged := oldProxyKeys != parentGroup.ProxyKeys

	// Update upstream URL if parent name changed
	if nameChanged {
		upstreamsJSON, err := buildChildGroupUpstream(parentGroup.Name)
		if err != nil {
			return app_errors.ErrInternalServer
		}

		if err := tx.WithContext(ctx).
			Model(&models.Group{}).
			Where("parent_group_id = ?", parentGroup.ID).
			Update("upstreams", upstreamsJSON).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
	}

	// Update API keys if parent proxy_keys changed
	// NOTE: AddMultipleKeys uses its own DB connection, not the passed transaction (tx).
	// This is a known limitation - if the caller's transaction is rolled back after this
	// method succeeds, the newly added keys will persist. This is acceptable because:
	// 1. Modifying AddMultipleKeys to accept transactions would require significant refactoring
	// 2. Orphan keys don't cause system issues and can be cleaned up manually
	// 3. The key addition order (add new first, then delete old) ensures child groups
	//    always have at least one working key during the transition
	if proxyKeysChanged && s.keyService != nil {
		newParentFirstKey := getFirstProxyKey(parentGroup.ProxyKeys)
		oldParentFirstKey := getFirstProxyKey(oldProxyKeys)

		if newParentFirstKey != "" && newParentFirstKey != oldParentFirstKey {
			// For each child group, update the API key
			// Add new key FIRST, then delete old key to avoid leaving child without any key
			for _, childGroup := range childGroups {
				// Add new key first to ensure child group always has a working key
				if _, err := s.keyService.AddMultipleKeys(childGroup.ID, newParentFirstKey); err != nil {
					logrus.WithContext(ctx).WithError(err).WithFields(logrus.Fields{
						"childGroupID":   childGroup.ID,
						"childGroupName": childGroup.Name,
					}).Error("Failed to add new API key for child group, keeping old key")
					continue // Keep old key if new key addition fails
				}

				// Now safe to delete old key
				if oldParentFirstKey != "" {
					oldKeyHash := s.keyService.EncryptionSvc.Hash(oldParentFirstKey)
					if err := tx.WithContext(ctx).
						Where("group_id = ? AND key_hash = ?", childGroup.ID, oldKeyHash).
						Delete(&models.APIKey{}).Error; err != nil {
						logrus.WithContext(ctx).WithError(err).WithFields(logrus.Fields{
							"childGroupID": childGroup.ID,
							"operation":    "delete_old_key",
						}).Warn("Failed to delete old API key for child group")
					}
				}
			}
		}
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"parentGroupID":    parentGroup.ID,
		"parentGroupName":  parentGroup.Name,
		"childGroupCount":  len(childGroups),
		"nameChanged":      nameChanged,
		"proxyKeysChanged": proxyKeysChanged,
	}).Info("Synced child groups after parent update")

	// Invalidate cache after updating child groups
	s.InvalidateCache()

	return nil
}

// DeleteChildGroupsForParent deletes all child groups when parent is deleted.
// Returns the count of deleted child groups.
func (s *ChildGroupService) DeleteChildGroupsForParent(ctx context.Context, tx *gorm.DB, parentGroupID uint) (int64, error) {
	// Get child group IDs first for logging
	var childGroupIDs []uint
	if err := tx.WithContext(ctx).
		Model(&models.Group{}).
		Where("parent_group_id = ?", parentGroupID).
		Pluck("id", &childGroupIDs).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}

	if len(childGroupIDs) == 0 {
		return 0, nil
	}

	// Delete API keys for all child groups
	if err := tx.WithContext(ctx).
		Where("group_id IN ?", childGroupIDs).
		Delete(&models.APIKey{}).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}

	// Delete child groups
	result := tx.WithContext(ctx).
		Where("parent_group_id = ?", parentGroupID).
		Delete(&models.Group{})
	if result.Error != nil {
		return 0, app_errors.ParseDBError(result.Error)
	}

	logrus.WithContext(ctx).WithFields(logrus.Fields{
		"parentGroupID":     parentGroupID,
		"deletedChildCount": result.RowsAffected,
	}).Info("Deleted child groups for parent")

	// Invalidate cache after deleting child groups
	s.InvalidateCache()

	return result.RowsAffected, nil
}

// ValidateParentGroupDeletion checks if parent group can be deleted and returns warning info.
func (s *ChildGroupService) ValidateParentGroupDeletion(ctx context.Context, parentGroupID uint) (int64, error) {
	return s.CountChildGroups(ctx, parentGroupID)
}

// GetParentGroup returns the parent group info for a child group.
func (s *ChildGroupService) GetParentGroup(ctx context.Context, childGroupID uint) (*models.Group, error) {
	var childGroup models.Group
	if err := s.db.WithContext(ctx).First(&childGroup, childGroupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	if childGroup.ParentGroupID == nil {
		return nil, nil // Not a child group
	}

	var parentGroup models.Group
	if err := s.db.WithContext(ctx).First(&parentGroup, *childGroup.ParentGroupID).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	return &parentGroup, nil
}

// SyncChildGroupUpstreams updates all child groups' upstream URLs to use the current PORT.
// This should be called at application startup to ensure all child groups use the correct port.
func (s *ChildGroupService) SyncChildGroupUpstreams(ctx context.Context) error {
	currentPort := utils.ParseInteger(os.Getenv("PORT"), 3001)

	// Get all child groups with their parent group names
	var childGroups []struct {
		ID              uint
		Name            string
		ParentGroupID   uint
		ParentGroupName string
		Upstreams       []byte
	}

	// Join with parent group to get parent name
	err := s.db.WithContext(ctx).
		Table("groups AS child").
		Select("child.id, child.name, child.parent_group_id, parent.name AS parent_group_name, child.upstreams").
		Joins("JOIN groups AS parent ON child.parent_group_id = parent.id").
		Where("child.parent_group_id IS NOT NULL").
		Find(&childGroups).Error

	if err != nil {
		return fmt.Errorf("failed to query child groups: %w", err)
	}

	if len(childGroups) == 0 {
		logrus.Debug("No child groups found, skipping upstream sync")
		return nil
	}

	updatedCount := 0
	for _, cg := range childGroups {
		// Build expected upstream URL with current port
		expectedURL := fmt.Sprintf("http://127.0.0.1:%d/proxy/%s", currentPort, cg.ParentGroupName)

		// Check if current upstream matches expected
		var currentUpstreams []map[string]interface{}
		if err := json.Unmarshal(cg.Upstreams, &currentUpstreams); err != nil {
			logrus.WithError(err).Warnf("Failed to parse upstreams for child group %s", cg.Name)
			continue
		}

		needsUpdate := false
		if len(currentUpstreams) > 0 {
			if currentURL, ok := currentUpstreams[0]["url"].(string); ok {
				if currentURL != expectedURL {
					needsUpdate = true
				}
			}
		}

		if needsUpdate {
			// Build new upstream JSON
			newUpstreamsJSON, err := buildChildGroupUpstream(cg.ParentGroupName)
			if err != nil {
				logrus.WithError(err).Warnf("Failed to build upstream for child group %s", cg.Name)
				continue
			}

			// Update the child group's upstream
			if err := s.db.WithContext(ctx).
				Model(&models.Group{}).
				Where("id = ?", cg.ID).
				Update("upstreams", newUpstreamsJSON).Error; err != nil {
				logrus.WithError(err).Warnf("Failed to update upstream for child group %s", cg.Name)
				continue
			}

			updatedCount++
			logrus.WithFields(logrus.Fields{
				"childGroupID":   cg.ID,
				"childGroupName": cg.Name,
				"newPort":        currentPort,
			}).Debug("Updated child group upstream URL")
		}
	}

	if updatedCount > 0 {
		logrus.Infof("Synced %d child group upstream URLs to port %d", updatedCount, currentPort)
		// Invalidate group cache after updates
		if err := s.groupManager.Invalidate(); err != nil {
			logrus.WithError(err).Warn("Failed to invalidate group cache after upstream sync")
		}
	} else if len(childGroups) > 0 {
		logrus.Infof("All %d child group upstream URLs are already up to date (port %d)", len(childGroups), currentPort)
	}

	return nil
}
