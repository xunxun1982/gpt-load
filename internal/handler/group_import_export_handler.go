package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/services"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// GroupExportData represents the structure for group export data.
type GroupExportData struct {
	// Group basic information
	Group GroupExportInfo `json:"group"`
	// Key list
	Keys []KeyExportInfo `json:"keys"`
	// Sub-group information (aggregate groups only)
	SubGroups []SubGroupExportInfo `json:"sub_groups,omitempty"`
	// Child group information (standard groups only)
	ChildGroups []ChildGroupExportInfo `json:"child_groups,omitempty"`
	// Export metadata
	ExportedAt string `json:"exported_at"`
	Version    string `json:"version"`
}

// GroupExportInfo represents group export information.
type GroupExportInfo struct {
	Name                 string                    `json:"name"`
	DisplayName          string                    `json:"display_name"`
	Description          string                    `json:"description"`
	GroupType            string                    `json:"group_type"`
	ChannelType          string                    `json:"channel_type"`
	Enabled              bool                      `json:"enabled"`
	TestModel            string                    `json:"test_model"`
	ValidationEndpoint   string                    `json:"validation_endpoint"`
	Upstreams            json.RawMessage           `json:"upstreams"`
	ParamOverrides       map[string]any            `json:"param_overrides"`
	Config               map[string]any            `json:"config"`
	HeaderRules          []models.HeaderRule       `json:"header_rules"`
	ModelMapping         string                    `json:"model_mapping,omitempty"`          // Deprecated: for backward compatibility
	ModelRedirectRules   map[string]string         `json:"model_redirect_rules,omitempty"`   // V1 rules (one-to-one)
	ModelRedirectRulesV2 json.RawMessage           `json:"model_redirect_rules_v2,omitempty"` // V2 rules (one-to-many)
	ModelRedirectStrict  bool                      `json:"model_redirect_strict,omitempty"`  // Strict mode
	PathRedirects        []models.PathRedirectRule `json:"path_redirects,omitempty"`         // Path redirect rules
	ProxyKeys            string                    `json:"proxy_keys"`
	Sort                 int                       `json:"sort"`
}

// KeyExportInfo represents key export information.
type KeyExportInfo struct {
	KeyValue string `json:"key_value"`
	Status   string `json:"status"`
}

// SubGroupExportInfo represents sub-group export information.
type SubGroupExportInfo struct {
	GroupName string `json:"group_name"`
	Weight    int    `json:"weight"`
}

// ChildGroupExportInfo represents child group export information.
type ChildGroupExportInfo struct {
	Name                 string                    `json:"name"`
	DisplayName          string                    `json:"display_name"`
	Description          string                    `json:"description"`
	Enabled              bool                      `json:"enabled"`
	ProxyKeys            string                    `json:"proxy_keys"`
	Sort                 int                       `json:"sort"`
	TestModel            string                    `json:"test_model"`
	ParamOverrides       map[string]any            `json:"param_overrides,omitempty"`
	Config               map[string]any            `json:"config,omitempty"`
	HeaderRules          []models.HeaderRule       `json:"header_rules,omitempty"`
	ModelMapping         string                    `json:"model_mapping,omitempty"`
	ModelRedirectRules   map[string]string         `json:"model_redirect_rules,omitempty"`
	ModelRedirectRulesV2 json.RawMessage           `json:"model_redirect_rules_v2,omitempty"`
	ModelRedirectStrict  bool                      `json:"model_redirect_strict"`
	CustomModelNames     json.RawMessage           `json:"custom_model_names,omitempty"`
	Preconditions        map[string]any            `json:"preconditions,omitempty"`
	PathRedirects        []models.PathRedirectRule `json:"path_redirects,omitempty"`
	Keys                 []KeyExportInfo           `json:"keys"`
}

// ExportGroup exports complete group data.
func (s *Server) ExportGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	// Use the new ImportExportService to export the group
	// This fixes the FindInBatches limitation that only exports 2000 records
	groupData, err := s.ImportExportService.ExportGroup(uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.group_not_found")
		} else if errors.Is(err, services.ErrChildGroupCannotExportIndividually) {
			// Child groups must be exported with their parent group
			response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.child_group_cannot_export_individually")
		} else {
			response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_group")
		}
		return
	}
	// Determine export mode: plain or encrypted (default encrypted)
	exportMode := GetExportMode(c)
	// Parse HeaderRules for export format using common utility function
	// ParseHeaderRulesForExport handles errors internally and logs warnings
	headerRules := ParseHeaderRulesForExport(groupData.Group.HeaderRules, groupData.Group.ID)

	// Parse PathRedirects for export format using common utility function
	pathRedirects := ParsePathRedirectsForExport(groupData.Group.PathRedirects)

	// Convert ModelRedirectRules from datatypes.JSONMap to map[string]string for export
	modelRedirectRules := ConvertModelRedirectRulesToExport(groupData.Group.ModelRedirectRules)

	// Export ModelRedirectRulesV2 as raw JSON
	var modelRedirectRulesV2 json.RawMessage
	if len(groupData.Group.ModelRedirectRulesV2) > 0 {
		modelRedirectRulesV2 = json.RawMessage(groupData.Group.ModelRedirectRulesV2)
	}

	// Build export data structure compatible with existing format
	exportData := GroupExportData{
		Group: GroupExportInfo{
			Name:                 groupData.Group.Name,
			DisplayName:          groupData.Group.DisplayName,
			Description:          groupData.Group.Description,
			GroupType:            groupData.Group.GroupType,
			ChannelType:          groupData.Group.ChannelType,
			Enabled:              groupData.Group.Enabled,
			TestModel:            groupData.Group.TestModel,
			ValidationEndpoint:   groupData.Group.ValidationEndpoint,
			Upstreams:            json.RawMessage(groupData.Group.Upstreams),
			ParamOverrides:       groupData.Group.ParamOverrides,
			Config:               groupData.Group.Config,
			HeaderRules:          headerRules,
			ModelMapping:         groupData.Group.ModelMapping,
			ModelRedirectRules:   modelRedirectRules,
			ModelRedirectRulesV2: modelRedirectRulesV2,
			ModelRedirectStrict:  groupData.Group.ModelRedirectStrict,
			PathRedirects:        pathRedirects,
			ProxyKeys:            groupData.Group.ProxyKeys,
			Sort:                 groupData.Group.Sort,
		},
		Keys:       make([]KeyExportInfo, 0, len(groupData.Keys)),
		SubGroups:  make([]SubGroupExportInfo, 0, len(groupData.SubGroups)),
		ExportedAt: time.Now().Format(time.RFC3339),
		Version:    "2.0",
	}

	// Convert keys to export format. Decrypt to plaintext only when explicitly requested.
	for _, key := range groupData.Keys {
		kv := key.KeyValue
		if exportMode == "plain" {
			if decrypted, derr := s.EncryptionSvc.Decrypt(kv); derr == nil {
				kv = decrypted
			} else {
				logrus.WithError(derr).Debug("Failed to decrypt key during plain export, keeping original value")
			}
		}
		exportData.Keys = append(exportData.Keys, KeyExportInfo{
			KeyValue: kv,
			Status:   key.Status,
		})
	}

	// Convert sub-groups to export format
	for _, sg := range groupData.SubGroups {
		exportData.SubGroups = append(exportData.SubGroups, SubGroupExportInfo{
			GroupName: sg.GroupName,
			Weight:    sg.Weight,
		})
	}

	// Convert child groups to export format (for standard groups)
	if len(groupData.ChildGroups) > 0 {
		exportData.ChildGroups = make([]ChildGroupExportInfo, 0, len(groupData.ChildGroups))
		for _, cg := range groupData.ChildGroups {
			// Parse child group configuration fields
			childHeaderRules := ParseHeaderRulesForExport(cg.HeaderRules, 0)
			childPathRedirects := ParsePathRedirectsForExport(cg.PathRedirects)

			// Convert ModelRedirectRules from []byte to map[string]string
			var childModelRedirectRules map[string]string
			if len(cg.ModelRedirectRules) > 0 {
				var tempMap map[string]any
				if err := json.Unmarshal(cg.ModelRedirectRules, &tempMap); err == nil {
					childModelRedirectRules = make(map[string]string)
					for k, v := range tempMap {
						if strVal, ok := v.(string); ok {
							childModelRedirectRules[k] = strVal
						}
					}
				}
			}

			var childModelRedirectRulesV2 json.RawMessage
			if len(cg.ModelRedirectRulesV2) > 0 {
				childModelRedirectRulesV2 = json.RawMessage(cg.ModelRedirectRulesV2)
			}

			var childCustomModelNames json.RawMessage
			if len(cg.CustomModelNames) > 0 {
				childCustomModelNames = json.RawMessage(cg.CustomModelNames)
			}

			// Convert ParamOverrides, Config, Preconditions from []byte to map[string]any
			var childParamOverrides, childConfig, childPreconditions map[string]any
			if len(cg.ParamOverrides) > 0 {
				json.Unmarshal(cg.ParamOverrides, &childParamOverrides)
			}
			if len(cg.Config) > 0 {
				json.Unmarshal(cg.Config, &childConfig)
			}
			if len(cg.Preconditions) > 0 {
				json.Unmarshal(cg.Preconditions, &childPreconditions)
			}

			childExport := ChildGroupExportInfo{
				Name:                 cg.Name,
				DisplayName:          cg.DisplayName,
				Description:          cg.Description,
				Enabled:              cg.Enabled,
				ProxyKeys:            cg.ProxyKeys,
				Sort:                 cg.Sort,
				TestModel:            cg.TestModel,
				ParamOverrides:       childParamOverrides,
				Config:               childConfig,
				HeaderRules:          childHeaderRules,
				ModelMapping:         cg.ModelMapping,
				ModelRedirectRules:   childModelRedirectRules,
				ModelRedirectRulesV2: childModelRedirectRulesV2,
				ModelRedirectStrict:  cg.ModelRedirectStrict,
				CustomModelNames:     childCustomModelNames,
				Preconditions:        childPreconditions,
				PathRedirects:        childPathRedirects,
				Keys:                 make([]KeyExportInfo, 0, len(cg.Keys)),
			}
			// Convert child group keys, decrypt if plain mode
			for _, key := range cg.Keys {
				kv := key.KeyValue
				if exportMode == "plain" {
					if decrypted, derr := s.EncryptionSvc.Decrypt(kv); derr == nil {
						kv = decrypted
					} else {
						logrus.WithError(derr).Debug("Failed to decrypt child group key during plain export, keeping original value")
					}
				}
				childExport.Keys = append(childExport.Keys, KeyExportInfo{
					KeyValue: kv,
					Status:   key.Status,
				})
			}
			exportData.ChildGroups = append(exportData.ChildGroups, childExport)
		}
		logrus.Debugf("Export via new service: Total %d child groups exported for group %s",
			len(exportData.ChildGroups), groupData.Group.Name)
	}

	logrus.Debugf("Export via new service: Total %d keys exported for group %s",
		len(exportData.Keys), groupData.Group.Name)

	// Set response headers, use different filename prefix based on group type
	var filenamePrefix string
	if groupData.Group.GroupType == "aggregate" {
		filenamePrefix = "aggregate-group"
	} else {
		filenamePrefix = "standard-group"
	}
	safeName := sanitizeFilename(groupData.Group.Name)
	suffix := "enc"
	if exportMode == "plain" {
		suffix = "plain"
	}
	filename := fmt.Sprintf("%s_%s_%s-%s.json", filenamePrefix, safeName, time.Now().Format("20060102_150405"), suffix)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "application/json; charset=utf-8")

	// Return JSON data
	c.JSON(http.StatusOK, exportData)
}

// GroupImportData represents the structure for group import data.
type GroupImportData struct {
	Group       GroupExportInfo        `json:"group"`
	Keys        []KeyExportInfo        `json:"keys"`
	SubGroups   []SubGroupExportInfo   `json:"sub_groups,omitempty"`
	ChildGroups []ChildGroupExportInfo `json:"child_groups,omitempty"` // Child groups for standard groups
}

// ImportGroup imports group data.
func (s *Server) ImportGroup(c *gin.Context) {
	var importData GroupImportData
	if err := c.ShouldBindJSON(&importData); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	// Determine import mode from query, filename or content
	sample := make([]string, 0, 5)
	for i := 0; i < len(importData.Keys) && i < 5; i++ {
		sample = append(sample, importData.Keys[i].KeyValue)
	}
	importMode := GetImportMode(c, sample)
	inputIsPlain := importMode == "plain"

	// Log import summary
	logrus.Infof("Importing group %s with %d keys (mode=%s)",
		importData.Group.Name, len(importData.Keys), importMode)
	if len(importData.SubGroups) > 0 {
		logrus.Debugf("SubGroups: %d", len(importData.SubGroups))
	}

	// Basic validation for group type
	if importData.Group.GroupType != "standard" && importData.Group.GroupType != "aggregate" {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_type")
		return
	}

	// Avoid concurrent long-running tasks to reduce DB contention.
	// Best-effort: If task status cannot be read, proceed without blocking.
	if s.TaskService != nil {
		if status, err := s.TaskService.GetTaskStatus(); err == nil && status.IsRunning {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, "a task is already running, please wait"))
			return
		}
	}

	// Convert handler format to service format
	// Convert HeaderRules back to JSON for storage using common utility function
	headerRulesJSON := ConvertHeaderRulesToJSON(importData.Group.HeaderRules)

	// Convert PathRedirects back to JSON for storage using common utility function
	pathRedirectsJSON := ConvertPathRedirectsToJSON(importData.Group.PathRedirects)

	// Convert ModelRedirectRules to datatypes.JSONMap using common utility function
	modelRedirectRules := ConvertModelRedirectRulesToImport(importData.Group.ModelRedirectRules)

	// Import ModelRedirectRulesV2 as raw JSON bytes
	// Automatically merge duplicate rules (same "from" model) during import
	var modelRedirectRulesV2 []byte
	if len(importData.Group.ModelRedirectRulesV2) > 0 {
		rawJSON := []byte(importData.Group.ModelRedirectRulesV2)
		// Merge duplicate rules to consolidate targets
		merged, err := utils.MergeModelRedirectRulesV2(rawJSON)
		if err != nil {
			logrus.WithError(err).Warn("Failed to merge model redirect rules V2, using original")
			modelRedirectRulesV2 = rawJSON
		} else {
			modelRedirectRulesV2 = merged
		}
	}

	serviceGroupData := services.GroupExportData{
		Group: models.Group{
			Name:                 importData.Group.Name,
			DisplayName:          importData.Group.DisplayName,
			Description:          importData.Group.Description,
			GroupType:            importData.Group.GroupType,
			ChannelType:          importData.Group.ChannelType,
			Enabled:              importData.Group.Enabled,
			TestModel:            importData.Group.TestModel,
			ValidationEndpoint:   importData.Group.ValidationEndpoint,
			Upstreams:            []byte(importData.Group.Upstreams),
			ParamOverrides:       importData.Group.ParamOverrides,
			Config:               importData.Group.Config,
			HeaderRules:          headerRulesJSON,
			ModelMapping:         importData.Group.ModelMapping,
			ModelRedirectRules:   modelRedirectRules,
			ModelRedirectRulesV2: modelRedirectRulesV2,
			ModelRedirectStrict:  importData.Group.ModelRedirectStrict,
			PathRedirects:        pathRedirectsJSON,
			ProxyKeys:            importData.Group.ProxyKeys,
			Sort:                 importData.Group.Sort,
		},
		Keys:      make([]services.KeyExportInfo, 0, len(importData.Keys)),
		SubGroups: make([]services.SubGroupInfo, 0, len(importData.SubGroups)),
	}

	// Convert keys; if input is plaintext, encrypt before passing to service
	for _, k := range importData.Keys {
		kv := k.KeyValue
		if inputIsPlain {
			if enc, e := s.EncryptionSvc.Encrypt(kv); e == nil {
				kv = enc
			} else {
				logrus.WithError(e).WithField("group", importData.Group.Name).Warn("Failed to encrypt plaintext key during import, skipping")
				continue
			}
		}
		serviceGroupData.Keys = append(serviceGroupData.Keys, services.KeyExportInfo{
			KeyValue: kv,
			Status:   k.Status,
		})
	}

	// Convert sub-groups
	for _, sg := range importData.SubGroups {
		serviceGroupData.SubGroups = append(serviceGroupData.SubGroups, services.SubGroupInfo{
			GroupName: sg.GroupName,
			Weight:    sg.Weight,
		})
	}

	// Convert child groups (for standard groups)
	if len(importData.ChildGroups) > 0 {
		serviceGroupData.ChildGroups = make([]services.ChildGroupExport, 0, len(importData.ChildGroups))
		for _, cg := range importData.ChildGroups {
			childExport := services.ChildGroupExport{
				Name:                cg.Name,
				DisplayName:         cg.DisplayName,
				Description:         cg.Description,
				Enabled:             cg.Enabled,
				ProxyKeys:           cg.ProxyKeys,
				Sort:                cg.Sort,
				TestModel:           cg.TestModel,
				ModelMapping:        cg.ModelMapping,
				ModelRedirectStrict: cg.ModelRedirectStrict,
				Keys:                make([]services.KeyExportInfo, 0, len(cg.Keys)),
			}

			// Convert JSON fields to []byte for service layer
			if cg.ParamOverrides != nil {
				if data, err := json.Marshal(cg.ParamOverrides); err == nil {
					childExport.ParamOverrides = data
				}
			}
			if cg.Config != nil {
				if data, err := json.Marshal(cg.Config); err == nil {
					childExport.Config = data
				}
			}
			if len(cg.HeaderRules) > 0 {
				if data, err := json.Marshal(cg.HeaderRules); err == nil {
					childExport.HeaderRules = data
				}
			}
			if cg.ModelRedirectRules != nil {
				if data, err := json.Marshal(cg.ModelRedirectRules); err == nil {
					childExport.ModelRedirectRules = data
				}
			}
			if len(cg.ModelRedirectRulesV2) > 0 {
				childExport.ModelRedirectRulesV2 = []byte(cg.ModelRedirectRulesV2)
			}
			if len(cg.CustomModelNames) > 0 {
				childExport.CustomModelNames = []byte(cg.CustomModelNames)
			}
			if cg.Preconditions != nil {
				if data, err := json.Marshal(cg.Preconditions); err == nil {
					childExport.Preconditions = data
				}
			}
			if len(cg.PathRedirects) > 0 {
				if data, err := json.Marshal(cg.PathRedirects); err == nil {
					childExport.PathRedirects = data
				}
			}

			// Convert child group keys; if input is plaintext, encrypt before passing to service
			for _, k := range cg.Keys {
				kv := k.KeyValue
				if inputIsPlain {
					if enc, e := s.EncryptionSvc.Encrypt(kv); e == nil {
						kv = enc
					} else {
						logrus.WithError(e).WithField("childGroup", cg.Name).Warn("Failed to encrypt plaintext key during child group import, skipping")
						continue
					}
				}
				childExport.Keys = append(childExport.Keys, services.KeyExportInfo{
					KeyValue: kv,
					Status:   k.Status,
				})
			}
			serviceGroupData.ChildGroups = append(serviceGroupData.ChildGroups, childExport)
		}
		logrus.Debugf("ChildGroups: %d", len(importData.ChildGroups))
	}

	// Best-effort: mark a global import task as running so read requests can degrade quickly
	// under SQLite lock contention during large imports.
	var taskErr error
	if s.TaskService != nil {
		totalKeys := len(serviceGroupData.Keys)
		for _, cg := range serviceGroupData.ChildGroups {
			totalKeys += len(cg.Keys)
		}
		if _, err := s.TaskService.StartTask(services.TaskTypeKeyImport, importData.Group.Name, totalKeys); err != nil {
			// If another task started between the status check above and now, surface as TASK_IN_PROGRESS.
			if err.Error() == "a task is already running, please wait" {
				response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, err.Error()))
				return
			}
			logrus.WithError(err).Debug("Failed to start global task for group import, continuing without task signaling")
		} else {
			defer func() {
				if endErr := s.TaskService.EndTask(nil, taskErr); endErr != nil {
					logrus.WithError(endErr).Debug("Failed to end global task for group import")
				}
			}()
		}
	}

	// Use transaction to ensure data consistency, rollback on failure
	var createdGroup models.Group
	var createdGroupID uint
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		// Create progress callback that updates task progress
		progressCallback := func(processed int) {
			if s.TaskService != nil {
				if updateErr := s.TaskService.UpdateProgress(processed); updateErr != nil {
					logrus.WithError(updateErr).Debug("Failed to update task progress during import")
				}
			}
		}
		// Use the centralized ImportGroup service method with progress callback
		createdGroupID, err = s.ImportExportService.ImportGroup(tx, &serviceGroupData, progressCallback)
		if err != nil {
			return err
		}
		// Query the created group within the transaction to avoid an extra query after commit
		if err := tx.First(&createdGroup, createdGroupID).Error; err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		taskErr = err
		if HandleServiceError(c, err) {
			return
		}
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.import_failed")
		return
	}

	// Reload group manager cache to ensure new group is immediately visible
	// Use a short timeout to avoid blocking the response for too long
	// If reload fails or times out, the cache will be updated asynchronously by the background syncer
	reloadSuccess := false
	if s.GroupManager != nil {
		// Create a context with short timeout for cache reload
		// This prevents blocking the HTTP response if database is slow
		reloadCtx, reloadCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer reloadCancel()

		// Attempt synchronous reload with timeout
		reloadDone := make(chan error, 1)
		go func() {
			reloadDone <- s.GroupManager.Reload()
		}()

		select {
		case err := <-reloadDone:
			if err != nil {
				logrus.WithError(err).Warn("Failed to reload group manager cache after import, will retry asynchronously")
			} else {
				logrus.Debug("Group manager cache reloaded successfully after import")
				reloadSuccess = true
			}
		case <-reloadCtx.Done():
			logrus.Warn("Group manager cache reload timed out after import, will retry asynchronously")
			// Trigger async reload in background
			go func() {
				if err := s.GroupManager.Reload(); err != nil {
					logrus.WithError(err).Error("Async group manager cache reload failed after import")
				}
			}()
		}
	}

	// Get the group with complete information
	// If reload succeeded, get from cache; otherwise query from database
	var groupWithRelations *models.Group
	if reloadSuccess && s.GroupManager != nil {
		var err error
		groupWithRelations, err = s.GroupManager.GetGroupByID(createdGroupID)
		if err != nil {
			logrus.WithError(err).Warn("Failed to get group from cache after import, using database query")
			reloadSuccess = false // Fallback to database
		}
	}

	if !reloadSuccess {
		// Fallback: query from database with relationships
		var dbGroup models.Group
		if err := s.DB.Preload("SubGroups").First(&dbGroup, createdGroupID).Error; err != nil {
			logrus.WithError(err).Warn("Failed to preload group relationships after import, using basic info")
			groupWithRelations = &createdGroup
		} else {
			groupWithRelations = &dbGroup
		}
	}

	// Update group list cache with the new group before returning response
	// This ensures ListGroups can return cached data immediately without DB query
	// Similar to CopyGroup optimization - add to cache instead of invalidating
	if s.GroupService != nil {
		logrus.Debugf("Adding imported group %d to list cache", createdGroupID)
		// Use a private method through a helper to add group to list cache
		// This avoids cache miss when frontend immediately requests /api/groups
		// Note: AddGroupToListCache does not return an error; it logs internally if needed
		s.GroupService.AddGroupToListCache(groupWithRelations)

		// For standard groups with child groups, also add each child group to the cache
		// This ensures the frontend sidebar shows child groups immediately after import
		if groupWithRelations.GroupType == "standard" && len(serviceGroupData.ChildGroups) > 0 {
			logrus.Infof("Querying %d child groups from database for cache update", len(serviceGroupData.ChildGroups))
			// Query all child groups from database to get their complete information
			var childGroups []models.Group
			if err := s.DB.Where("parent_group_id = ?", createdGroupID).Find(&childGroups).Error; err != nil {
				logrus.WithError(err).Errorf("Failed to query child groups for cache update (parent_id=%d)", createdGroupID)
			} else {
				logrus.Infof("Found %d child groups in database for parent %d", len(childGroups), createdGroupID)
				// Add each child group to the list cache
				for i := range childGroups {
					logrus.Debugf("Adding child group to cache: id=%d, name=%s, parent_id=%d",
						childGroups[i].ID, childGroups[i].Name, *childGroups[i].ParentGroupID)
					s.GroupService.AddGroupToListCache(&childGroups[i])
				}
				logrus.Infof("Added %d child groups to list cache for imported group %d", len(childGroups), createdGroupID)

				// Invalidate ChildGroupService cache to ensure /api/groups/all-child-groups returns fresh data
				if s.ChildGroupService != nil {
					s.ChildGroupService.InvalidateCache()
					logrus.Debug("Invalidated ChildGroupService cache after importing child groups")
				}
			}
		} else if groupWithRelations.GroupType == "standard" {
			logrus.Debugf("Standard group has no child groups to cache")
		}
	} else {
		logrus.Warn("GroupService is nil, cannot update list cache")
	}

	// Load keys to Redis store and reset failure_count asynchronously
	// These operations run asynchronously after the success response is sent to avoid blocking the HTTP response
	// Design decision: These are best-effort optimizations and non-critical post-processing operations
	// - The group and keys are already committed to the database, ensuring data integrity
	// - Cache loading failures don't affect data persistence; keys will be loaded on next use
	// - Failure counts reset is an optimization; existing counts don't prevent functionality
	// - Failures are logged with request_id for traceability; operators can monitor logs
	// Note: Users are not notified of async operation failures to keep the API simple and avoid complexity
	// If these operations are critical, consider implementing a job queue with status tracking
	// Capture only safe values before launching the goroutine; never retain gin.Context
	parentCtx := context.Background()
	reqID := c.GetHeader("X-Request-ID")
	go func(groupID uint, parent context.Context, reqID string) {
		ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
		defer cancel()
		entry := logrus.WithContext(ctx)
		if reqID != "" {
			entry = entry.WithField("request_id", reqID)
		}

		// First, load all keys to Redis store
		if err := s.KeyService.KeyProvider.LoadGroupKeysToStore(groupID); err != nil {
			entry.WithError(err).Errorf("Failed to load keys to store for imported group %d", groupID)
		} else {
			entry.Infof("Successfully loaded keys to store for imported group %d", groupID)
		}

		// Then reset failure_count for all active keys
		resetCount, resetErr := s.KeyService.ResetGroupActiveKeysFailureCount(groupID)
		if resetErr != nil {
			entry.WithError(resetErr).Warnf("Failed to reset failure_count for imported group %d", groupID)
		} else if resetCount > 0 {
			entry.Infof("Reset failure_count for %d active keys in imported group %d", resetCount, groupID)
		}

		// Pre-warm the key stats cache after import to avoid slow first query
		// This helps the UI load faster when users navigate to the imported group
		if s.GroupService != nil {
			if _, err := s.GroupService.GetGroupStats(ctx, groupID); err != nil {
				entry.WithError(err).Debug("Failed to pre-warm stats cache for imported group")
			} else {
				entry.Debugf("Successfully pre-warmed stats cache for imported group %d", groupID)
			}
		}

		// Optimize database after large import to improve query performance
		// This is especially important for SQLite after bulk inserts
		if err := optimizeDatabaseAfterImport(ctx, s.DB); err != nil {
			entry.WithError(err).Debug("Failed to optimize database after import")
		} else {
			entry.Debug("Successfully optimized database after import")
		}
	}(createdGroupID, parentCtx, reqID)

	response.SuccessI18n(c, "success.group_imported", s.newGroupResponse(groupWithRelations))
}

// sanitizeFilename keeps alphanumerics, dash, underscore, and dot; replaces others with '_'
// Truncates to 100 characters to prevent overly long filenames in Content-Disposition headers
func sanitizeFilename(name string) string {
	b := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b = append(b, r)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "group"
	}
	// Truncate to reasonable length for filename (100 chars)
	// This prevents issues with extremely long filenames in HTTP headers
	// The full filename will still include prefix and timestamp
	if len(b) > 100 {
		b = b[:100]
	}
	return string(b)
}

// optimizeDatabaseAfterImport runs database optimization commands after bulk import
// This is especially important for SQLite to rebuild statistics and checkpoint WAL
func optimizeDatabaseAfterImport(ctx context.Context, db *gorm.DB) error {
	// Get driver name to determine database type
	driverName := db.Dialector.Name()

	if driverName == "sqlite" {
		// For SQLite, run PRAGMA optimize to update query planner statistics
		// This is crucial after bulk inserts to ensure efficient query plans
		if err := db.WithContext(ctx).Exec("PRAGMA optimize").Error; err != nil {
			logrus.WithError(err).Warn("Failed to run PRAGMA optimize after import")
		}

		// Checkpoint WAL to merge changes into main database file
		// This reduces WAL file size and improves subsequent query performance
		// Use PASSIVE mode to avoid blocking other operations
		if err := db.WithContext(ctx).Exec("PRAGMA wal_checkpoint(PASSIVE)").Error; err != nil {
			logrus.WithError(err).Warn("Failed to checkpoint WAL after import")
		}
	} else if driverName == "mysql" {
		// For MySQL, analyze the api_keys table to update statistics
		if err := db.WithContext(ctx).Exec("ANALYZE TABLE api_keys").Error; err != nil {
			logrus.WithError(err).Warn("Failed to analyze api_keys table after import")
		}
		// Also analyze groups table as it's affected by group imports
		if err := db.WithContext(ctx).Exec("ANALYZE TABLE groups").Error; err != nil {
			logrus.WithError(err).Warn("Failed to analyze groups table after import")
		}
		// Analyze group_sub_groups for aggregate group imports
		if err := db.WithContext(ctx).Exec("ANALYZE TABLE group_sub_groups").Error; err != nil {
			logrus.WithError(err).Warn("Failed to analyze group_sub_groups table after import")
		}
	} else if driverName == "postgres" {
		// For PostgreSQL, analyze the api_keys table
		if err := db.WithContext(ctx).Exec("ANALYZE api_keys").Error; err != nil {
			logrus.WithError(err).Warn("Failed to analyze api_keys table after import")
		}
		// Also analyze groups table as it's affected by group imports
		if err := db.WithContext(ctx).Exec("ANALYZE groups").Error; err != nil {
			logrus.WithError(err).Warn("Failed to analyze groups table after import")
		}
		// Analyze group_sub_groups for aggregate group imports
		if err := db.WithContext(ctx).Exec("ANALYZE group_sub_groups").Error; err != nil {
			logrus.WithError(err).Warn("Failed to analyze group_sub_groups table after import")
		}
	}

	// Verify connection is still alive after optimization
	sqlDB, err := db.DB()
	if err != nil {
		logrus.WithError(err).Warn("Failed to get underlying DB connection for ping")
		return err
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		logrus.WithError(err).Warn("Failed to ping database after optimization")
		return err
	}
	return nil
}
