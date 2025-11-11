package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
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

// SystemExportData represents the data structure for system-wide export.
type SystemExportData struct {
	Version        string                    `json:"version"`
	ExportedAt     string                    `json:"exported_at"`
	SystemSettings map[string]string         `json:"system_settings"`
	Groups         []GroupExportData         `json:"groups"`
}

// ExportAll exports all system data (system settings and all groups).
func (s *Server) ExportAll(c *gin.Context) {
	// Use the new ExportImportService to export the entire system
	// This fixes the FindInBatches limitation that only exports 2000 records
	systemData, err := s.ExportImportService.ExportSystem()
	if err != nil {
		logrus.WithError(err).Error("Failed to export system")
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.export_failed")
		return
	}

	// Convert from services.SystemExportData to handler.SystemExportData
	// Parse HeaderRules for each group to match the expected format
	groupExports := make([]GroupExportData, 0, len(systemData.Groups))
	totalKeys := 0

	for _, groupData := range systemData.Groups {
		// Parse HeaderRules using common utility function
		headerRules := ParseHeaderRulesForExport(groupData.Group.HeaderRules, groupData.Group.ID)

		// Parse PathRedirects for export format using common utility function
		pathRedirects := ParsePathRedirectsForExport(groupData.Group.PathRedirects)

		// Convert ModelRedirectRules from datatypes.JSONMap to map[string]string for export
		modelRedirectRules := ConvertModelRedirectRulesToExport(groupData.Group.ModelRedirectRules)

		groupExport := GroupExportData{
			Group: GroupExportInfo{
				Name:                groupData.Group.Name,
				DisplayName:         groupData.Group.DisplayName,
				Description:         groupData.Group.Description,
				GroupType:           groupData.Group.GroupType,
				ChannelType:         groupData.Group.ChannelType,
				Enabled:             groupData.Group.Enabled,
				TestModel:           groupData.Group.TestModel,
				ValidationEndpoint:  groupData.Group.ValidationEndpoint,
				Upstreams:           json.RawMessage(groupData.Group.Upstreams),
				ParamOverrides:      groupData.Group.ParamOverrides,
				Config:              groupData.Group.Config,
				HeaderRules:         headerRules,
				ModelMapping:        groupData.Group.ModelMapping,
				ModelRedirectRules:  modelRedirectRules,
				ModelRedirectStrict: groupData.Group.ModelRedirectStrict,
				PathRedirects:       pathRedirects,
				ProxyKeys:           groupData.Group.ProxyKeys,
				Sort:                groupData.Group.Sort,
			},
			Keys:      []KeyExportInfo{},
			SubGroups: []SubGroupExportInfo{},
		}

		// Convert keys
		for _, key := range groupData.Keys {
			groupExport.Keys = append(groupExport.Keys, KeyExportInfo{
				KeyValue: key.KeyValue,
				Status:   key.Status,
			})
		}
		totalKeys += len(groupData.Keys)

		// Convert sub-groups
		for _, sg := range groupData.SubGroups {
			groupExport.SubGroups = append(groupExport.SubGroups, SubGroupExportInfo{
				GroupName: sg.GroupName,
				Weight:    sg.Weight,
			})
		}

		groupExports = append(groupExports, groupExport)
	}

	logrus.Debugf("System export via new service: Total %d keys exported across %d groups",
		totalKeys, len(systemData.Groups))

	exportData := SystemExportData{
		Version:        systemData.Version,
		ExportedAt:     systemData.ExportedAt,
		SystemSettings: systemData.SystemSettings,
		Groups:         groupExports,
	}

	// Return JSON data with standard response format
	response.Success(c, exportData)
}

// SystemImportData represents the data structure for system-wide import.
type SystemImportData struct {
	Version        string                    `json:"version"`
	SystemSettings map[string]string         `json:"system_settings"`
	Groups         []GroupExportData         `json:"groups"`
}

// ImportAll imports all system data (system settings and all groups).
func (s *Server) ImportAll(c *gin.Context) {
	var importData SystemImportData
	if err := c.ShouldBindJSON(&importData); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	// Log import summary
	totalKeys := 0
	for _, groupExport := range importData.Groups {
		totalKeys += len(groupExport.Keys)
	}
	logrus.Infof("System import: %d groups with %d total keys",
		len(importData.Groups), totalKeys)
	if len(importData.SystemSettings) > 0 {
		logrus.Debugf("System settings: %d", len(importData.SystemSettings))
	}

	// Validate version compatibility
	// Log warning if version doesn't match expected value to help with future format evolution
	if importData.Version != "" && importData.Version != "2.0" {
		logrus.WithField("version", importData.Version).WithField("expected_version", "2.0").
			Warn("Importing data with different version, compatibility not guaranteed")
	}

	// Validate system settings before transaction to ensure full rollback on failure
	// Convert map[string]string to map[string]any and perform type conversion based on field types
	var convertedSettingsMap map[string]any
	if len(importData.SystemSettings) > 0 {
		var err error
		convertedSettingsMap, err = convertSettingsMap(importData.SystemSettings)
		if err != nil {
			logrus.WithError(err).Error("Failed to convert system settings during import")
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, fmt.Sprintf("Invalid system settings: %v", err)))
			return
		}
		// Validate settings, return error immediately if validation fails without any database operations
		if err := s.SettingsManager.ValidateSettings(convertedSettingsMap); err != nil {
			logrus.WithError(err).Error("System settings validation failed during import")
			response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, fmt.Sprintf("Invalid system settings: %v", err)))
			return
		}
	}

	// Convert handler format to service format for unified import
	serviceImportData := &services.SystemExportData{
		Version:        importData.Version,
		ExportedAt:     "", // Not needed for import
		SystemSettings: importData.SystemSettings,
		Groups:         make([]services.GroupExportData, 0, len(importData.Groups)),
	}

	// Convert groups to service format
	for _, groupExport := range importData.Groups {
		// Convert HeaderRules back to JSON for storage using common utility function
		headerRulesJSON := ConvertHeaderRulesToJSON(groupExport.Group.HeaderRules)

		// Convert PathRedirects back to JSON for storage using common utility function
		pathRedirectsJSON := ConvertPathRedirectsToJSON(groupExport.Group.PathRedirects)

		// Convert ModelRedirectRules to datatypes.JSONMap using common utility function
		modelRedirectRules := ConvertModelRedirectRulesToImport(groupExport.Group.ModelRedirectRules)

		groupData := services.GroupExportData{
			Group: models.Group{
				Name:                groupExport.Group.Name,
				DisplayName:         groupExport.Group.DisplayName,
				Description:         groupExport.Group.Description,
				GroupType:           groupExport.Group.GroupType,
				ChannelType:         groupExport.Group.ChannelType,
				Enabled:             groupExport.Group.Enabled,
				TestModel:           groupExport.Group.TestModel,
				ValidationEndpoint:  groupExport.Group.ValidationEndpoint,
				Upstreams:           []byte(groupExport.Group.Upstreams),
				ParamOverrides:      groupExport.Group.ParamOverrides,
				Config:              groupExport.Group.Config,
				HeaderRules:         headerRulesJSON,
				ModelMapping:        groupExport.Group.ModelMapping,
				ModelRedirectRules:  modelRedirectRules,
				ModelRedirectStrict: groupExport.Group.ModelRedirectStrict,
				PathRedirects:       pathRedirectsJSON,
				ProxyKeys:           groupExport.Group.ProxyKeys,
				Sort:                groupExport.Group.Sort,
			},
			Keys:      make([]services.KeyExportInfo, 0, len(groupExport.Keys)),
			SubGroups: make([]services.SubGroupInfo, 0, len(groupExport.SubGroups)),
		}

		// Convert keys
		for _, key := range groupExport.Keys {
			groupData.Keys = append(groupData.Keys, services.KeyExportInfo{
				KeyValue: key.KeyValue,
				Status:   key.Status,
			})
		}

		// Convert sub-groups
		for _, sg := range groupExport.SubGroups {
			groupData.SubGroups = append(groupData.SubGroups, services.SubGroupInfo{
				GroupName: sg.GroupName,
				Weight:    sg.Weight,
			})
		}

		serviceImportData.Groups = append(serviceImportData.Groups, groupData)
	}

	// Use transaction to ensure data consistency
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// Use the unified ExportImportService for system import
		// This ensures consistent handling of all imports
		if err := s.ExportImportService.ImportSystem(tx, serviceImportData); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.import_failed")
		return
	}

	// Force reload system settings from database after import
	// This ensures all imported settings take effect immediately
	logrus.Info("Forcing system settings reload after import...")

	// Use the new ReloadSettings method to force synchronous cache reload
	if len(convertedSettingsMap) > 0 {
		if err := s.SettingsManager.ReloadSettings(); err != nil {
			logrus.WithError(err).Warn("Failed to reload system settings cache, settings may not take effect immediately")
		} else {
			logrus.Info("System settings cache reloaded successfully")
		}
	}

	// Invalidate group manager cache to ensure new groups are visible
	if s.GroupManager != nil {
		if err := s.GroupManager.Invalidate(); err != nil {
			logrus.WithError(err).Warn("Failed to invalidate group manager cache")
		} else {
			logrus.Info("Group manager cache invalidated successfully")
		}
	}
	// Also invalidate the group list cache so /api/groups returns fresh list
	if s.GroupService != nil {
		s.GroupService.InvalidateGroupListCache()
	}

	logrus.Info("System import completed successfully")
	response.SuccessI18n(c, "success.system_imported", nil)
}

// SystemSettingsImportData represents the data structure for system settings import only.
type SystemSettingsImportData struct {
	SystemSettings map[string]string `json:"system_settings"`
}

// ImportSystemSettings imports system settings only and forces cache reload.
// This is a separate endpoint that focuses solely on system settings import and refresh.
func (s *Server) ImportSystemSettings(c *gin.Context) {
	var importData SystemSettingsImportData
	if err := c.ShouldBindJSON(&importData); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if len(importData.SystemSettings) == 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, "No system settings provided"))
		return
	}

	logrus.Infof("Importing system settings: %d settings", len(importData.SystemSettings))

	// Convert map[string]string to map[string]any and perform type conversion
	convertedSettingsMap, err := convertSettingsMap(importData.SystemSettings)
	if err != nil {
		logrus.WithError(err).Error("Failed to convert system settings during import")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, fmt.Sprintf("Invalid system settings: %v", err)))
		return
	}

	// Validate settings before transaction
	if err := s.SettingsManager.ValidateSettings(convertedSettingsMap); err != nil {
		logrus.WithError(err).Error("System settings validation failed during import")
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, fmt.Sprintf("Invalid system settings: %v", err)))
		return
	}

	// Use transaction to ensure data consistency
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		// Import system settings using the same logic as ExportImportService
		updatedSettings := 0
		createdSettings := 0

		for key, value := range importData.SystemSettings {
			var setting models.SystemSetting
			if err := tx.Where("setting_key = ?", key).First(&setting).Error; err == nil {
				// Update existing setting
				if err := tx.Model(&setting).Updates(map[string]interface{}{
					"setting_value": value,
					"updated_at":    time.Now(),
				}).Error; err != nil {
					return fmt.Errorf("failed to update setting %s: %w", key, err)
				}
				updatedSettings++
				logrus.Debugf("Updated setting: %s", key)
			} else {
				// Create new setting
				setting = models.SystemSetting{
					SettingKey:   key,
					SettingValue: value,
				}
				if err := tx.Create(&setting).Error; err != nil {
					return fmt.Errorf("failed to create setting %s: %w", key, err)
				}
				createdSettings++
				logrus.Debugf("Created new setting: %s", key)
			}
		}

		logrus.Infof("System settings imported: %d updated, %d created", updatedSettings, createdSettings)
		return nil
	})

	if err != nil {
		logrus.WithError(err).Error("Failed to import system settings")
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.import_failed")
		return
	}

	// Force synchronous reload of system settings cache after transaction commits
	// This ensures the imported settings take effect immediately
	logrus.Info("Forcing system settings cache reload after import...")
	if err := s.SettingsManager.ReloadSettings(); err != nil {
		logrus.WithError(err).Error("Failed to reload system settings cache")
		// Still return success as database was updated, but log the error
		response.SuccessI18n(c, "success.system_settings_imported", map[string]interface{}{
			"warning": "Settings imported but cache reload failed. Please restart the service.",
		})
		return
	}

	// Also invalidate group manager cache if needed
	if s.GroupManager != nil {
		if err := s.GroupManager.Invalidate(); err != nil {
			logrus.WithError(err).Warn("Failed to invalidate group manager cache")
		}
	}

	logrus.Info("System settings import completed successfully")
	response.SuccessI18n(c, "success.system_settings_imported", nil)
}

// GroupsBatchImportData represents the data structure for batch group import.
type GroupsBatchImportData struct {
	Groups []GroupExportData `json:"groups"`
}

// ImportGroupsBatch imports multiple groups in batch.
// This reuses the existing ImportGroup logic for consistency.
func (s *Server) ImportGroupsBatch(c *gin.Context) {
	var importData GroupsBatchImportData
	if err := c.ShouldBindJSON(&importData); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if len(importData.Groups) == 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, "No groups provided"))
		return
	}

	// Log import summary
	totalKeys := 0
	for _, groupExport := range importData.Groups {
		totalKeys += len(groupExport.Keys)
	}
	logrus.Infof("Batch importing %d groups with %d total keys", len(importData.Groups), totalKeys)

	// Convert handler format to service format for unified import
	serviceGroups := make([]services.GroupExportData, 0, len(importData.Groups))
	for _, groupExport := range importData.Groups {
		// Convert HeaderRules back to JSON for storage using common utility function
		headerRulesJSON := ConvertHeaderRulesToJSON(groupExport.Group.HeaderRules)

		// Convert PathRedirects back to JSON for storage using common utility function
		pathRedirectsJSON := ConvertPathRedirectsToJSON(groupExport.Group.PathRedirects)

		// Convert ModelRedirectRules to datatypes.JSONMap using common utility function
		modelRedirectRules := ConvertModelRedirectRulesToImport(groupExport.Group.ModelRedirectRules)

		groupData := services.GroupExportData{
			Group: models.Group{
				Name:                groupExport.Group.Name,
				DisplayName:         groupExport.Group.DisplayName,
				Description:         groupExport.Group.Description,
				GroupType:           groupExport.Group.GroupType,
				ChannelType:         groupExport.Group.ChannelType,
				Enabled:             groupExport.Group.Enabled,
				TestModel:           groupExport.Group.TestModel,
				ValidationEndpoint:  groupExport.Group.ValidationEndpoint,
				Upstreams:           []byte(groupExport.Group.Upstreams),
				ParamOverrides:      groupExport.Group.ParamOverrides,
				Config:              groupExport.Group.Config,
				HeaderRules:         headerRulesJSON,
				ModelMapping:        groupExport.Group.ModelMapping,
				ModelRedirectRules:  modelRedirectRules,
				ModelRedirectStrict: groupExport.Group.ModelRedirectStrict,
				PathRedirects:       pathRedirectsJSON,
				ProxyKeys:           groupExport.Group.ProxyKeys,
				Sort:                groupExport.Group.Sort,
			},
			Keys:      make([]services.KeyExportInfo, 0, len(groupExport.Keys)),
			SubGroups: make([]services.SubGroupInfo, 0, len(groupExport.SubGroups)),
		}

		// Convert keys
		for _, key := range groupExport.Keys {
			groupData.Keys = append(groupData.Keys, services.KeyExportInfo{
				KeyValue: key.KeyValue,
				Status:   key.Status,
			})
		}

		// Convert sub-groups
		for _, sg := range groupExport.SubGroups {
			groupData.SubGroups = append(groupData.SubGroups, services.SubGroupInfo{
				GroupName: sg.GroupName,
				Weight:    sg.Weight,
			})
		}

		serviceGroups = append(serviceGroups, groupData)
	}

	// Use transaction to ensure data consistency
	var importedGroups []models.Group
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// Import all groups using the unified ExportImportService
		importedCount := 0
		for _, groupData := range serviceGroups {
			groupID, err := s.ExportImportService.ImportGroup(tx, &groupData)
			if err != nil {
				// Log error but continue with other groups
				logrus.WithError(err).Warnf("Failed to import group %s, skipping", groupData.Group.Name)
				continue
			}

			// Query the created group within the transaction
			var createdGroup models.Group
			if err := tx.First(&createdGroup, groupID).Error; err != nil {
				logrus.WithError(err).Warnf("Failed to query imported group %d", groupID)
				continue
			}

			importedGroups = append(importedGroups, createdGroup)
			importedCount++
			logrus.Debugf("Imported group %s with ID %d", groupData.Group.Name, groupID)
		}

		logrus.Infof("Groups imported: %d/%d successful", importedCount, len(serviceGroups))
		return nil
	})

	if err != nil {
		logrus.WithError(err).Error("Failed to import groups batch")
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.import_failed")
		return
	}

	// Invalidate group manager cache to ensure new groups are visible
	if s.GroupManager != nil {
		if err := s.GroupManager.Invalidate(); err != nil {
			logrus.WithError(err).Warn("Failed to invalidate group manager cache")
		} else {
			logrus.Info("Group manager cache invalidated successfully")
		}
	}
	// Also invalidate the group list cache so /api/groups returns fresh list
	if s.GroupService != nil {
		s.GroupService.InvalidateGroupListCache()
	}

	// Reset failure_count for all active keys in each successfully imported group asynchronously
	// This treats each import as a fresh start, clearing any historical failure counts
	// Run asynchronously to avoid blocking the HTTP response (can take minutes for large groups)
	go func(ctx context.Context, groups []models.Group) {
		// Use background context with timeout to avoid goroutine leaks
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()

		for _, group := range groups {
			resetCount, resetErr := s.KeyService.ResetGroupActiveKeysFailureCount(group.ID)
			if resetErr != nil {
				logrus.WithContext(ctx).WithError(resetErr).Warnf("Failed to reset failure_count for imported group %d (%s)",
					group.ID, group.Name)
			} else if resetCount > 0 {
				logrus.WithContext(ctx).Infof("Reset failure_count for %d active keys in imported group %d (%s)",
					resetCount, group.ID, group.Name)
			}
		}
	}(context.Background(), importedGroups)

	// Convert imported groups to response format
	groupResponses := make([]interface{}, 0, len(importedGroups))
	for _, group := range importedGroups {
		groupResponses = append(groupResponses, s.newGroupResponse(&group))
	}

	logrus.Infof("Batch import completed: %d groups imported", len(importedGroups))
	response.SuccessI18n(c, "success.groups_batch_imported", map[string]interface{}{
		"groups":      groupResponses,
		"imported":    len(importedGroups),
		"total":       len(importData.Groups),
		"failed":      len(importData.Groups) - len(importedGroups),
	})
}

// convertSettingsMap converts map[string]string to map[string]any and performs type conversion based on field types.
func convertSettingsMap(stringMap map[string]string) (map[string]any, error) {
	// Get SystemSettings struct information to determine field types
	tempSettings := utils.DefaultSystemSettings()
	v := reflect.ValueOf(&tempSettings).Elem()
	t := v.Type()
	jsonToField := make(map[string]reflect.StructField)
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		jsonTag := strings.Split(field.Tag.Get("json"), ",")[0]
		if jsonTag != "" && jsonTag != "-" {
			jsonToField[jsonTag] = field
		}
	}

	result := make(map[string]any)
	for key, strValue := range stringMap {
		field, ok := jsonToField[key]
		if !ok {
			// If field doesn't exist, keep as string (may be unknown setting item)
			result[key] = strValue
			continue
		}

		// Convert value based on field type
		switch field.Type.Kind() {
		case reflect.Int:
			intVal, err := strconv.Atoi(strValue)
			if err != nil {
				return nil, fmt.Errorf("invalid integer value for %s: %s", key, strValue)
			}
			// ValidateSettings expects float64 (number after JSON parsing)
			result[key] = float64(intVal)
		case reflect.Bool:
			boolVal, err := strconv.ParseBool(strValue)
			if err != nil {
				return nil, fmt.Errorf("invalid boolean value for %s: %s", key, strValue)
			}
			result[key] = boolVal
		case reflect.String:
			result[key] = strValue
		default:
			// Other types keep as string
			result[key] = strValue
		}
	}

	return result, nil
}
