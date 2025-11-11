package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// importGroupFromExportData imports a group from export data with optimized bulk import.
// This function uses the centralized name generation from ExportImportService
func importGroupFromExportData(tx *gorm.DB, exportImportSvc *services.ExportImportService, encryptionSvc encryption.Service, bulkImportSvc *services.BulkImportService, groupInfo GroupExportInfo, keys []KeyExportInfo, subGroups []SubGroupExportInfo, inputIsPlain bool) (uint, error) {
	// Use the centralized unique name generation from ExportImportService
	groupName, err := exportImportSvc.GenerateUniqueGroupName(tx, groupInfo.Name)
	if err != nil {
		return 0, err
	}

	// Extract the suffix that was added to the name and apply it to display name too
	// This ensures both name and display name have the same random suffix
	displayName := groupInfo.DisplayName
	if displayName != "" && groupName != groupInfo.Name && strings.HasPrefix(groupName, groupInfo.Name) {
		// Find the suffix that was added to the name
		suffix := groupName[len(groupInfo.Name):]
		displayName = groupInfo.DisplayName + suffix
	}

	// Convert HeaderRules using common utility function
	headerRulesJSON := ConvertHeaderRulesToJSON(groupInfo.HeaderRules)

	// Convert PathRedirects using common utility function
	pathRedirectsJSON := ConvertPathRedirectsToJSON(groupInfo.PathRedirects)

	// Convert ModelRedirectRules using common utility function
	modelRedirectRules := ConvertModelRedirectRulesToImport(groupInfo.ModelRedirectRules)

	group := models.Group{
		Name:                groupName,
		DisplayName:         displayName,
		Description:         groupInfo.Description,
		GroupType:           groupInfo.GroupType,
		ChannelType:         groupInfo.ChannelType,
		Enabled:             groupInfo.Enabled,
		TestModel:           groupInfo.TestModel,
		ValidationEndpoint:  groupInfo.ValidationEndpoint,
		Upstreams:           []byte(groupInfo.Upstreams),
		ParamOverrides:      groupInfo.ParamOverrides,
		ModelRedirectStrict: groupInfo.ModelRedirectStrict,
		ModelRedirectRules:  modelRedirectRules,
		PathRedirects:       pathRedirectsJSON,
		Config:              groupInfo.Config,
		HeaderRules:         headerRulesJSON,
		ModelMapping:        groupInfo.ModelMapping,
		ProxyKeys:           groupInfo.ProxyKeys,
		Sort:                groupInfo.Sort,
	}

	if err := tx.Create(&group).Error; err != nil {
		return 0, err
	}

	if len(keys) > 0 {
		startPrep := time.Now()
		keyModels := make([]models.APIKey, 0, len(keys))
		skippedKeys := 0
		for i, keyInfo := range keys {
			var plain string
			var stored string
			if inputIsPlain {
				plain = keyInfo.KeyValue
				enc, e := encryptionSvc.Encrypt(plain)
				if e != nil {
					logrus.WithError(e).WithField("index", i).Warn("Failed to encrypt plaintext key during import, skipping")
					skippedKeys++
					continue
				}
				stored = enc
			} else {
				// Decrypt key_value to calculate key_hash for proper indexing and deduplication
				// The exported key_value is encrypted, so we need to decrypt it first
				dec, derr := encryptionSvc.Decrypt(keyInfo.KeyValue)
				if derr != nil {
					// If decryption fails, log and skip this key (no key material)
					logrus.WithError(derr).
						WithField("index", i).
						Warn("Failed to decrypt key during import, skipping")
					skippedKeys++
					continue
				}
				plain = dec
				stored = keyInfo.KeyValue // already encrypted
			}

			// Calculate key_hash from decrypted/plain key for proper indexing
			keyHash := encryptionSvc.Hash(plain)

			keyModels = append(keyModels, models.APIKey{
				GroupID:  group.ID,
				KeyValue: stored, // encrypted in DB
				KeyHash:  keyHash, // Calculated hash
				Status:   keyInfo.Status,
			})
		}
		if skippedKeys > 0 {
			logrus.Warnf("Skipped %d keys due to preparation failures during import", skippedKeys)
		}
		prepDuration := time.Since(startPrep)
		logrus.Infof("Key preparation took %v for %d keys", prepDuration, len(keyModels))

		if len(keyModels) > 0 {
			// Use the new BulkImportService for optimized bulk insert
			// This service automatically detects the database type and uses optimal batch sizes
			// It also applies database-specific optimizations for maximum performance
			logrus.Debugf("Using BulkImportService to import %d keys for group %s",
				len(keyModels), group.Name)

			// The BulkImportService will:
			// - Detect database type (SQLite/MySQL/PostgreSQL)
			// - Calculate optimal batch size based on key size
			// - Apply database-specific optimizations
			// - Use the existing transaction to avoid nesting issues
			// IMPORTANT: Use BulkInsertAPIKeysWithTx to use the existing transaction
			if err := bulkImportSvc.BulkInsertAPIKeysWithTx(tx, keyModels); err != nil {
				return 0, fmt.Errorf("bulk import failed: %w", err)
			}
		}
	}

	if group.GroupType == "aggregate" && len(subGroups) > 0 {
		// Batch query all sub-groups to avoid N+1 query problem
		// Collect all group names first
		groupNames := make([]string, 0, len(subGroups))
		for _, sgInfo := range subGroups {
			groupNames = append(groupNames, sgInfo.GroupName)
		}

		// Query all sub-groups in a single query
		var foundSubGroups []models.Group
		if err := tx.Where("name IN ?", groupNames).Find(&foundSubGroups).Error; err != nil {
			// If query fails, continue without sub-groups (non-critical)
			logrus.WithError(err).Warnf("Failed to query sub-groups during import, continuing without sub-groups")
			return group.ID, nil
		}

		// Create a map for quick lookup
		subGroupMap := make(map[string]uint, len(foundSubGroups))
		for _, sg := range foundSubGroups {
			subGroupMap[sg.Name] = sg.ID
		}

		// Log any missing sub-groups for visibility (non-fatal)
		missing := make([]string, 0, len(groupNames))
		for _, name := range groupNames {
			if _, ok := subGroupMap[name]; !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			logrus.WithField("missing_sub_groups", strings.Join(missing, ",")).
				Warnf("Some referenced sub-groups were not found; relationships will be partially created for group %s", group.Name)
		}

		// Batch create group-sub-group relationships
		groupSubGroups := make([]models.GroupSubGroup, 0, len(subGroups))
		for _, sgInfo := range subGroups {
			if subGroupID, exists := subGroupMap[sgInfo.GroupName]; exists {
				groupSubGroups = append(groupSubGroups, models.GroupSubGroup{
					GroupID:    group.ID,
					SubGroupID: subGroupID,
					Weight:     sgInfo.Weight,
				})
			}
		}

		if len(groupSubGroups) > 0 {
			if err := tx.CreateInBatches(groupSubGroups, 1000).Error; err != nil {
				// If creation fails, continue without sub-groups (non-critical)
				logrus.WithError(err).Warnf("Failed to create sub-group relationships during import")
				return group.ID, nil
			}
		}
	}

	return group.ID, nil
}

// GroupExportData represents the structure for group export data.
type GroupExportData struct {
	// Group basic information
	Group GroupExportInfo `json:"group"`
	// Key list
	Keys []KeyExportInfo `json:"keys"`
	// Sub-group information (aggregate groups only)
	SubGroups []SubGroupExportInfo `json:"sub_groups,omitempty"`
	// Export metadata
	ExportedAt string `json:"exported_at"`
	Version    string `json:"version"`
}

// GroupExportInfo represents group export information.
type GroupExportInfo struct {
	Name               string              `json:"name"`
	DisplayName        string              `json:"display_name"`
	Description         string              `json:"description"`
	GroupType           string              `json:"group_type"`
	ChannelType         string              `json:"channel_type"`
	Enabled             bool                `json:"enabled"`
	TestModel           string              `json:"test_model"`
	ValidationEndpoint  string              `json:"validation_endpoint"`
	Upstreams           json.RawMessage     `json:"upstreams"`
	ParamOverrides      map[string]any      `json:"param_overrides"`
	Config              map[string]any      `json:"config"`
	HeaderRules         []models.HeaderRule `json:"header_rules"`
	ModelMapping        string              `json:"model_mapping,omitempty"`         // Deprecated: for backward compatibility
	ModelRedirectRules  map[string]string   `json:"model_redirect_rules,omitempty"`  // New field
	ModelRedirectStrict bool                `json:"model_redirect_strict,omitempty"` // New field
	PathRedirects       []models.PathRedirectRule `json:"path_redirects,omitempty"`  // Path redirect rules
	ProxyKeys           string              `json:"proxy_keys"`
	Sort                int                 `json:"sort"`
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

// ExportGroup exports complete group data.
func (s *Server) ExportGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	// Use the new ExportImportService to export the group
	// This fixes the FindInBatches limitation that only exports 2000 records
	groupData, err := s.ExportImportService.ExportGroup(uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.group_not_found")
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

	// Build export data structure compatible with existing format
	exportData := GroupExportData{
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
	Group     GroupExportInfo      `json:"group"`
	Keys      []KeyExportInfo      `json:"keys"`
	SubGroups []SubGroupExportInfo `json:"sub_groups,omitempty"`
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

	// Use transaction to ensure data consistency, rollback on failure
	var createdGroup models.Group
	var createdGroupID uint
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		createdGroupID, err = importGroupFromExportData(tx, s.ExportImportService, s.EncryptionSvc, s.BulkImportService, importData.Group, importData.Keys, importData.SubGroups, inputIsPlain)
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
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.import_failed")
		return
	}

	// Invalidate group manager cache synchronously to ensure new group is immediately visible in the list
	// This must happen before the success response to provide a good user experience
	if s.GroupManager != nil {
		if err := s.GroupManager.Invalidate(); err != nil {
			logrus.WithError(err).Warn("Failed to invalidate group manager cache after import")
		} else {
			logrus.Debug("Group manager cache invalidated successfully after import")
		}
	}
	// Also invalidate the group list cache so /api/groups immediately reflects new data
	if s.GroupService != nil {
		s.GroupService.InvalidateGroupListCache()
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
	}(createdGroupID, parentCtx, reqID)

	response.SuccessI18n(c, "success.group_imported", s.newGroupResponse(&createdGroup))
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
