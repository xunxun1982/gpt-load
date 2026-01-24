package handler

import (
	"context"
	"encoding/json"
	"errors"
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
// Note: This type mirrors services.SystemExportData for JSON binding in handlers.
type SystemExportData struct {
	Version        string                            `json:"version"`
	ExportedAt     string                            `json:"exported_at"`
	SystemSettings map[string]string                 `json:"system_settings"`
	Groups         []GroupExportData                 `json:"groups"`
	ManagedSites   *ManagedSitesExportData           `json:"managed_sites,omitempty"`
	HubAccessKeys  []services.HubAccessKeyExportInfo `json:"hub_access_keys,omitempty"`
}

// ManagedSitesExportData represents exported managed sites data for handler
type ManagedSitesExportData struct {
	AutoCheckin *ManagedSiteAutoCheckinConfig `json:"auto_checkin,omitempty"`
	Sites       []ManagedSiteExportInfo       `json:"sites"`
}

// ManagedSiteAutoCheckinConfig represents auto-checkin configuration
type ManagedSiteAutoCheckinConfig struct {
	GlobalEnabled     bool                              `json:"global_enabled"`
	WindowStart       string                            `json:"window_start"`
	WindowEnd         string                            `json:"window_end"`
	ScheduleMode      string                            `json:"schedule_mode"`
	DeterministicTime string                            `json:"deterministic_time,omitempty"`
	RetryStrategy     ManagedSiteAutoCheckinRetryConfig `json:"retry_strategy"`
}

// ManagedSiteAutoCheckinRetryConfig represents retry strategy
type ManagedSiteAutoCheckinRetryConfig struct {
	Enabled           bool `json:"enabled"`
	IntervalMinutes   int  `json:"interval_minutes"`
	MaxAttemptsPerDay int  `json:"max_attempts_per_day"`
}

// ManagedSiteExportInfo represents exported site information
type ManagedSiteExportInfo struct {
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
	AuthValue          string `json:"auth_value,omitempty"`
}

// ExportAll exports all system data (system settings and all groups).
func (s *Server) ExportAll(c *gin.Context) {
	// Determine export mode: plain or encrypted (default encrypted)
	exportMode := GetExportMode(c)

	// Use the new ImportExportService to export the entire system
	// This fixes the FindInBatches limitation that only exports 2000 records
	systemData, err := s.ImportExportService.ExportSystem()
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

		// Convert keys; when plain mode, decrypt for output
		for _, key := range groupData.Keys {
			kv := key.KeyValue
			if exportMode == "plain" {
				if dec, derr := s.EncryptionSvc.Decrypt(kv); derr == nil {
					kv = dec
				} else {
					logrus.WithError(derr).Debug("Failed to decrypt key during plain system export, keeping original value")
				}
			}
			groupExport.Keys = append(groupExport.Keys, KeyExportInfo{
				KeyValue: kv,
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

	// Convert HubAccessKeys; when plain mode, decrypt key values
	hubAccessKeys := systemData.HubAccessKeys
	if exportMode == "plain" && len(hubAccessKeys) > 0 {
		decryptedKeys := make([]services.HubAccessKeyExportInfo, 0, len(hubAccessKeys))
		for _, key := range hubAccessKeys {
			kv := key.KeyValue
			if dec, derr := s.EncryptionSvc.Decrypt(kv); derr == nil {
				kv = dec
			} else {
				logrus.WithError(derr).Debugf("Failed to decrypt hub access key %s during plain export, keeping original value", key.Name)
			}
			decryptedKeys = append(decryptedKeys, services.HubAccessKeyExportInfo{
				Name:          key.Name,
				KeyValue:      kv,
				AllowedModels: key.AllowedModels,
				Enabled:       key.Enabled,
			})
		}
		hubAccessKeys = decryptedKeys
	}

	exportData := SystemExportData{
		Version:        systemData.Version,
		ExportedAt:     systemData.ExportedAt,
		SystemSettings: systemData.SystemSettings,
		Groups:         groupExports,
		HubAccessKeys:  hubAccessKeys,
	}

	// Convert managed sites if present
	if systemData.ManagedSites != nil && len(systemData.ManagedSites.Sites) > 0 {
		managedSites := &ManagedSitesExportData{
			Sites: make([]ManagedSiteExportInfo, 0, len(systemData.ManagedSites.Sites)),
		}

		// Copy auto-checkin config
		if systemData.ManagedSites.AutoCheckin != nil {
			managedSites.AutoCheckin = &ManagedSiteAutoCheckinConfig{
				GlobalEnabled:     systemData.ManagedSites.AutoCheckin.GlobalEnabled,
				WindowStart:       systemData.ManagedSites.AutoCheckin.WindowStart,
				WindowEnd:         systemData.ManagedSites.AutoCheckin.WindowEnd,
				ScheduleMode:      systemData.ManagedSites.AutoCheckin.ScheduleMode,
				DeterministicTime: systemData.ManagedSites.AutoCheckin.DeterministicTime,
				RetryStrategy: ManagedSiteAutoCheckinRetryConfig{
					Enabled:           systemData.ManagedSites.AutoCheckin.RetryStrategy.Enabled,
					IntervalMinutes:   systemData.ManagedSites.AutoCheckin.RetryStrategy.IntervalMinutes,
					MaxAttemptsPerDay: systemData.ManagedSites.AutoCheckin.RetryStrategy.MaxAttemptsPerDay,
				},
			}
		}

		// Convert sites; when plain mode, decrypt auth values
		for _, site := range systemData.ManagedSites.Sites {
			siteInfo := ManagedSiteExportInfo{
				Name:               site.Name,
				Notes:              site.Notes,
				Description:        site.Description,
				Sort:               site.Sort,
				Enabled:            site.Enabled,
				BaseURL:            site.BaseURL,
				SiteType:           site.SiteType,
				UserID:             site.UserID,
				CheckInPageURL:     site.CheckInPageURL,
				CheckInAvailable:   site.CheckInAvailable,
				CheckInEnabled:     site.CheckInEnabled,
				AutoCheckInEnabled: site.AutoCheckInEnabled,
				CustomCheckInURL:   site.CustomCheckInURL,
				AuthType:           site.AuthType,
			}

			// Handle auth value based on export mode
			if site.AuthValue != "" {
				if exportMode == "plain" {
					if dec, derr := s.EncryptionSvc.Decrypt(site.AuthValue); derr == nil {
						siteInfo.AuthValue = dec
					} else {
						logrus.WithError(derr).Warnf("Failed to decrypt site auth value for %s during plain export, omitting auth", site.Name)
					}
				} else {
					siteInfo.AuthValue = site.AuthValue
				}
			}

			managedSites.Sites = append(managedSites.Sites, siteInfo)
		}

		exportData.ManagedSites = managedSites
	}

	// Set download headers with mode suffix for filename
	suffix := "enc"
	if exportMode == "plain" {
		suffix = "plain"
	}
	filename := fmt.Sprintf("system-export_%s-%s.json", time.Now().Format("20060102_150405"), suffix)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "application/json; charset=utf-8")

	// Return JSON data with standard response format
	response.Success(c, exportData)
}

// SystemImportData represents the data structure for system-wide import.
type SystemImportData struct {
	Version        string                            `json:"version"`
	SystemSettings map[string]string                 `json:"system_settings"`
	Groups         []GroupExportData                 `json:"groups"`
	ManagedSites   *ManagedSitesExportData           `json:"managed_sites,omitempty"`
	HubAccessKeys  []services.HubAccessKeyExportInfo `json:"hub_access_keys,omitempty"`
}

// ImportAll imports all system data (system settings and all groups).
func (s *Server) ImportAll(c *gin.Context) {
	var importData SystemImportData
	if err := c.ShouldBindJSON(&importData); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	// Determine import mode from query, filename or content
	sample := make([]string, 0, 5)
outer:
	for _, g := range importData.Groups {
		for _, k := range g.Keys {
			if len(sample) < 5 {
				sample = append(sample, k.KeyValue)
			} else {
				break outer
			}
		}
	}
	importMode := GetImportMode(c, sample)
	inputIsPlain := importMode == "plain"

	// Log import summary
	totalKeys := 0
	for _, groupExport := range importData.Groups {
		totalKeys += len(groupExport.Keys)
	}
	logrus.Infof("System import: %d groups with %d total keys (mode=%s)",
		len(importData.Groups), totalKeys, importMode)
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

	// Convert HubAccessKeys; if input is plaintext, encrypt key values
	// NOTE: Keys that fail encryption are silently skipped with warning log.
	// This is consistent with regular key import behavior and avoids failing
	// the entire import due to a single problematic key. The warning log
	// provides visibility for debugging. Adding skipped count to response
	// would require API changes and is deferred for future enhancement.
	// AI Review: Keeping silent skip behavior for consistency with existing patterns.
	hubAccessKeys := importData.HubAccessKeys
	if inputIsPlain && len(hubAccessKeys) > 0 {
		encryptedKeys := make([]services.HubAccessKeyExportInfo, 0, len(hubAccessKeys))
		for _, key := range hubAccessKeys {
			kv := key.KeyValue
			if enc, e := s.EncryptionSvc.Encrypt(kv); e == nil {
				kv = enc
			} else {
				logrus.WithError(e).Warnf("Failed to encrypt hub access key %s during import, skipping", key.Name)
				continue
			}
			encryptedKeys = append(encryptedKeys, services.HubAccessKeyExportInfo{
				Name:          key.Name,
				KeyValue:      kv,
				AllowedModels: key.AllowedModels,
				Enabled:       key.Enabled,
			})
		}
		hubAccessKeys = encryptedKeys
	}

	// Convert handler format to service format for unified import
	serviceImportData := &services.SystemExportData{
		Version:        importData.Version,
		ExportedAt:     "", // Not needed for import
		SystemSettings: importData.SystemSettings,
		Groups:         make([]services.GroupExportData, 0, len(importData.Groups)),
		HubAccessKeys:  hubAccessKeys,
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

		// Convert keys; if input is plaintext, encrypt before passing to service
		for idx := range groupExport.Keys {
			kv := groupExport.Keys[idx].KeyValue
			if inputIsPlain {
				if enc, e := s.EncryptionSvc.Encrypt(kv); e == nil {
					kv = enc
				} else {
					logrus.WithError(e).WithField("group", groupExport.Group.Name).Warn("Failed to encrypt plaintext key during system import, skipping")
					continue
				}
			}
			groupData.Keys = append(groupData.Keys, services.KeyExportInfo{
				KeyValue: kv,
				Status:   groupExport.Keys[idx].Status,
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

	// Convert managed sites if present
	if importData.ManagedSites != nil && len(importData.ManagedSites.Sites) > 0 {
		managedSites := &services.ManagedSitesExportData{
			Sites: make([]services.ManagedSiteExportInfo, 0, len(importData.ManagedSites.Sites)),
		}

		// Copy auto-checkin config
		if importData.ManagedSites.AutoCheckin != nil {
			managedSites.AutoCheckin = &services.ManagedSiteAutoCheckinConfig{
				GlobalEnabled:     importData.ManagedSites.AutoCheckin.GlobalEnabled,
				WindowStart:       importData.ManagedSites.AutoCheckin.WindowStart,
				WindowEnd:         importData.ManagedSites.AutoCheckin.WindowEnd,
				ScheduleMode:      importData.ManagedSites.AutoCheckin.ScheduleMode,
				DeterministicTime: importData.ManagedSites.AutoCheckin.DeterministicTime,
				RetryStrategy: services.ManagedSiteAutoCheckinRetryConfig{
					Enabled:           importData.ManagedSites.AutoCheckin.RetryStrategy.Enabled,
					IntervalMinutes:   importData.ManagedSites.AutoCheckin.RetryStrategy.IntervalMinutes,
					MaxAttemptsPerDay: importData.ManagedSites.AutoCheckin.RetryStrategy.MaxAttemptsPerDay,
				},
			}
		}

		// Convert sites; if input is plaintext, encrypt auth values
		for _, site := range importData.ManagedSites.Sites {
			siteInfo := services.ManagedSiteExportInfo{
				Name:               site.Name,
				Notes:              site.Notes,
				Description:        site.Description,
				Sort:               site.Sort,
				Enabled:            site.Enabled,
				BaseURL:            site.BaseURL,
				SiteType:           site.SiteType,
				UserID:             site.UserID,
				CheckInPageURL:     site.CheckInPageURL,
				CheckInAvailable:   site.CheckInAvailable,
				CheckInEnabled:     site.CheckInEnabled,
				AutoCheckInEnabled: site.AutoCheckInEnabled,
				CustomCheckInURL:   site.CustomCheckInURL,
				AuthType:           site.AuthType,
			}

			// Handle auth value encryption
			if site.AuthValue != "" && site.AuthType != "none" {
				if inputIsPlain {
					if enc, e := s.EncryptionSvc.Encrypt(site.AuthValue); e == nil {
						siteInfo.AuthValue = enc
					} else {
						logrus.WithError(e).Warnf("Failed to encrypt site auth value for %s, skipping auth", site.Name)
					}
				} else {
					siteInfo.AuthValue = site.AuthValue
				}
			}

			managedSites.Sites = append(managedSites.Sites, siteInfo)
		}

		serviceImportData.ManagedSites = managedSites
	}

	// Use transaction to ensure data consistency
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// Use the unified ImportExportService for system import
		// This ensures consistent handling of all imports
		if err := s.ImportExportService.ImportSystem(tx, serviceImportData); err != nil {
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

	// Load keys to Redis store asynchronously for all imported groups
	// Query all groups and load their keys to Redis store
	// NOTE: WithContext is kept for consistency with project-wide logging pattern
	// and potential future tracing integration, even though ctx is detached
	go func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		entry := logrus.WithContext(ctx)

		var groups []models.Group
		if err := s.DB.Select("id, name").Find(&groups).Error; err != nil {
			entry.WithError(err).Warn("Failed to query groups for Redis cache loading after system import")
			return
		}

		for _, group := range groups {
			// Load keys to Redis store
			if err := s.KeyService.KeyProvider.LoadGroupKeysToStore(group.ID); err != nil {
				entry.WithError(err).Warnf("Failed to load keys to store for group %d (%s)",
					group.ID, group.Name)
			}

			// Reset failure_count for all active keys
			resetCount, resetErr := s.KeyService.ResetGroupActiveKeysFailureCount(group.ID)
			if resetErr != nil {
				entry.WithError(resetErr).Warnf("Failed to reset failure_count for group %d (%s)",
					group.ID, group.Name)
			} else if resetCount > 0 {
				entry.Debugf("Reset failure_count for %d active keys in group %d (%s)",
					resetCount, group.ID, group.Name)
			}
		}
		entry.Infof("Completed loading keys to Redis store for %d groups after system import", len(groups))
	}(context.Background())

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
		// Import system settings using the same logic as ImportExportService
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

	// Avoid concurrent long-running tasks to reduce DB contention.
	// Best-effort: If task signaling fails, continue without task status updates.
	taskStarted := false
	var taskErr error
	if s.TaskService != nil {
		// NOTE: Pre-check before StartTask is intentionally kept for fast-fail behavior.
		// Although StartTask performs the same check atomically, this pre-check:
		// 1. Avoids unnecessary serialization/storage operations when task is already running
		// 2. Provides clearer code intent and better readability
		// 3. The minor TOCTOU window is harmless since StartTask rechecks atomically
		if status, err := s.TaskService.GetTaskStatus(); err == nil && status.IsRunning {
			response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, "a task is already running, please wait"))
			return
		}
		if _, err := s.TaskService.StartTask(services.TaskTypeKeyImport, "system", totalKeys); err != nil {
			if errors.Is(err, services.ErrTaskAlreadyRunning) {
				response.Error(c, app_errors.NewAPIError(app_errors.ErrTaskInProgress, err.Error()))
				return
			}
			logrus.WithError(err).Debug("Failed to start global task for batch group import, continuing without task signaling")
		} else {
			taskStarted = true
			defer func() {
				if endErr := s.TaskService.EndTask(nil, taskErr); endErr != nil {
					logrus.WithError(endErr).Debug("Failed to end global task for batch group import")
				}
			}()
		}
	}

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
	// NOTE: Transaction returns nil even when individual group imports fail (logged and skipped).
	// This is intentional - partial import failures are expected behavior, not task-level errors.
	// The response payload includes imported/total/failed counts for visibility into partial success.
	// taskErr is only set for GORM-level transaction failures, not business-level partial failures.
	// NOTE: Context cancellation is intentionally NOT added to this transaction because:
	// 1. Import operations should be atomic - once started, they should complete for data integrity
	// 2. Consistent with project-wide pattern where no DB.Transaction uses WithContext
	// 3. Partial cancellation mid-import could leave data in inconsistent state
	var importedGroups []models.Group
	processedKeys := 0
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// Import all groups using the unified ImportExportService
		importedCount := 0
		for _, groupData := range serviceGroups {
			// Create progress callback for this group's import
			progressCallback := func(processed int) {
				if taskStarted {
					// Update progress with cumulative count across all groups
					totalProcessed := processedKeys + processed
					if updateErr := s.TaskService.UpdateProgress(totalProcessed); updateErr != nil {
						logrus.WithError(updateErr).Debug("Failed to update task progress during batch import")
					}
				}
			}

			groupID, err := s.ImportExportService.ImportGroup(tx, &groupData, progressCallback)
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
			processedKeys += len(groupData.Keys)
			// Final progress update after this group is complete
			if taskStarted {
				if updateErr := s.TaskService.UpdateProgress(processedKeys); updateErr != nil {
					logrus.WithError(updateErr).Debug("Failed to update task progress for batch group import")
				}
			}
			logrus.Debugf("Imported group %s with ID %d", groupData.Group.Name, groupID)
		}

		logrus.Infof("Groups imported: %d/%d successful", importedCount, len(serviceGroups))
		return nil
	})

	if err != nil {
		taskErr = err
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

	// Load keys to Redis store and reset failure_count for all active keys asynchronously
	// This treats each import as a fresh start, clearing any historical failure counts
	// Run asynchronously to avoid blocking the HTTP response (can take minutes for large groups)
	// NOTE: WithContext is kept for consistency with project-wide logging pattern
	// and potential future tracing integration, even though ctx is detached
	go func(ctx context.Context, groups []models.Group) {
		// Use background context with timeout to avoid goroutine leaks
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		entry := logrus.WithContext(ctx)

		for _, group := range groups {
			// First, load all keys to Redis store
			if err := s.KeyService.KeyProvider.LoadGroupKeysToStore(group.ID); err != nil {
				entry.WithError(err).Warnf("Failed to load keys to store for imported group %d (%s)",
					group.ID, group.Name)
			}

			// Then reset failure_count for all active keys
			resetCount, resetErr := s.KeyService.ResetGroupActiveKeysFailureCount(group.ID)
			if resetErr != nil {
				entry.WithError(resetErr).Warnf("Failed to reset failure_count for imported group %d (%s)",
					group.ID, group.Name)
			} else if resetCount > 0 {
				entry.Infof("Reset failure_count for %d active keys in imported group %d (%s)",
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
		"groups":   groupResponses,
		"imported": len(importedGroups),
		"total":    len(importData.Groups),
		"failed":   len(importData.Groups) - len(importedGroups),
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
