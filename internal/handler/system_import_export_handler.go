package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
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
	// Get all system settings
	var systemSettings []models.SystemSetting
	if err := s.DB.Find(&systemSettings).Error; err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_settings")
		return
	}

	settingsMap := make(map[string]string)
	for _, setting := range systemSettings {
		settingsMap[setting.SettingKey] = setting.SettingValue
	}

	// Get all groups, preload keys to avoid N+1 queries
	var groups []models.Group
	if err := s.DB.Preload("APIKeys").Order("sort ASC, id DESC").Find(&groups).Error; err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_groups")
		return
	}

	// Build group export data
	groupExports := make([]GroupExportData, 0, len(groups))
	for _, group := range groups {
		// Parse HeaderRules
		var headerRules []models.HeaderRule
		if len(group.HeaderRules) > 0 {
			if err := json.Unmarshal(group.HeaderRules, &headerRules); err != nil {
				logrus.WithError(err).WithField("group_id", group.ID).Warn("Failed to parse HeaderRules during export")
				headerRules = []models.HeaderRule{}
			}
		}

		groupExport := GroupExportData{
			Group: GroupExportInfo{
				Name:               group.Name,
				DisplayName:        group.DisplayName,
				Description:        group.Description,
				GroupType:          group.GroupType,
				ChannelType:        group.ChannelType,
				Enabled:            group.Enabled,
				TestModel:          group.TestModel,
				ValidationEndpoint: group.ValidationEndpoint,
				Upstreams:          json.RawMessage(group.Upstreams),
				ParamOverrides:     group.ParamOverrides,
				Config:             group.Config,
				HeaderRules:        headerRules,
				ModelMapping:       group.ModelMapping,
				ProxyKeys:          group.ProxyKeys,
				Sort:               group.Sort,
			},
			Keys:      []KeyExportInfo{},
			SubGroups: []SubGroupExportInfo{},
		}

		// Export keys
		for _, key := range group.APIKeys {
			groupExport.Keys = append(groupExport.Keys, KeyExportInfo{
				KeyValue: key.KeyValue,
				Status:   key.Status,
			})
		}

		// If it's an aggregate group, get sub-group information
		if group.GroupType == "aggregate" {
			subGroups, err := s.AggregateGroupService.GetSubGroups(c.Request.Context(), group.ID)
			if err != nil {
				logrus.WithError(err).WithField("group_id", group.ID).Warn("Failed to get sub-groups during export")
			} else {
				for _, sg := range subGroups {
					groupExport.SubGroups = append(groupExport.SubGroups, SubGroupExportInfo{
						GroupName: sg.Group.Name,
						Weight:    sg.Weight,
					})
				}
			}
		}

		groupExports = append(groupExports, groupExport)
	}

	exportData := SystemExportData{
		Version:        "2.0",
		ExportedAt:     time.Now().Format(time.RFC3339),
		SystemSettings: settingsMap,
		Groups:         groupExports,
	}

	// Set response headers
	filename := fmt.Sprintf("system_export_%s.json", time.Now().Format("20060102_150405"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Content-Transfer-Encoding", "binary")

	// Return JSON data
	c.JSON(http.StatusOK, exportData)
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

	// Use transaction to ensure data consistency
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// Import system settings (overwrite mode)
		if len(importData.SystemSettings) > 0 {
			for key, value := range importData.SystemSettings {
				var setting models.SystemSetting
				if err := tx.Where("setting_key = ?", key).First(&setting).Error; err == nil {
					// Update if exists
					if err := tx.Model(&setting).Update("setting_value", value).Error; err != nil {
						return err
					}
				} else {
					// Create if not exists
					setting = models.SystemSetting{
						SettingKey:   key,
						SettingValue: value,
					}
					if err := tx.Create(&setting).Error; err != nil {
						return err
					}
				}
			}
		}

		// Import all groups
		for _, groupExport := range importData.Groups {
			if _, err := importGroupFromExportData(tx, groupExport.Group, groupExport.Keys, groupExport.SubGroups); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.import_failed")
		return
	}

	// Trigger system settings reload (via UpdateSettings which triggers syncer.Invalidate)
	// Use the previously converted settingsMap to trigger cache refresh
	if len(convertedSettingsMap) > 0 {
		// Call UpdateSettings to trigger cache refresh
		// Although database is already updated, UpdateSettings uses OnConflict handling and won't duplicate updates
		// If UpdateSettings fails (e.g., syncer.Invalidate fails), only log warning, don't return error
		// because data is already correctly saved, just cache refresh failed, can wait for auto refresh
		if err := s.SettingsManager.UpdateSettings(convertedSettingsMap); err != nil {
			logrus.WithError(err).Warn("Failed to refresh system settings cache after import, but data has been saved correctly")
			// Don't return error because data is already correctly saved, just cache refresh failed
		}
	}

	response.SuccessI18n(c, "success.system_imported", nil)
}

// convertSettingsMap converts map[string]string to map[string]any and performs type conversion based on field types.
func convertSettingsMap(stringMap map[string]string) (map[string]any, error) {
	// Get SystemSettings struct information to determine field types
	tempSettings := utils.DefaultSystemSettings()
	v := reflect.ValueOf(&tempSettings).Elem()
	t := v.Type()
	jsonToField := make(map[string]reflect.StructField)
	for i := range t.NumField() {
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
