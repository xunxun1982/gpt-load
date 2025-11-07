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
func importGroupFromExportData(tx *gorm.DB, exportImportSvc *services.ExportImportService, encryptionSvc encryption.Service, bulkImportSvc *services.BulkImportService, groupInfo GroupExportInfo, keys []KeyExportInfo, subGroups []SubGroupExportInfo) (uint, error) {
	// Use the centralized unique name generation from ExportImportService
	groupName, err := exportImportSvc.GenerateUniqueGroupName(tx, groupInfo.Name)
	if err != nil {
		return 0, err
	}

	headerRulesJSON, err := json.Marshal(groupInfo.HeaderRules)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal header rules: %w", err)
	}
	group := models.Group{
		Name:               groupName,
		DisplayName:        groupInfo.DisplayName,
		Description:        groupInfo.Description,
		GroupType:          groupInfo.GroupType,
		ChannelType:        groupInfo.ChannelType,
		Enabled:            groupInfo.Enabled,
		TestModel:          groupInfo.TestModel,
		ValidationEndpoint: groupInfo.ValidationEndpoint,
		Upstreams:          []byte(groupInfo.Upstreams),
		ParamOverrides:     groupInfo.ParamOverrides,
		Config:             groupInfo.Config,
		HeaderRules:        headerRulesJSON,
		ModelMapping:       groupInfo.ModelMapping,
		ProxyKeys:          groupInfo.ProxyKeys,
		Sort:               groupInfo.Sort,
	}

	if err := tx.Create(&group).Error; err != nil {
		return 0, err
	}

	if len(keys) > 0 {
		startPrep := time.Now()
		keyModels := make([]models.APIKey, 0, len(keys))
		skippedKeys := 0
		for i, keyInfo := range keys {
			// Decrypt key_value to calculate key_hash for proper indexing and deduplication
			// The exported key_value is encrypted, so we need to decrypt it first
			decryptedKey, err := encryptionSvc.Decrypt(keyInfo.KeyValue)
			if err != nil {
				// If decryption fails, log and skip this key (no key material)
				logrus.WithError(err).
					WithField("index", i).
					Warn("Failed to decrypt key during import, skipping")
				skippedKeys++
				continue
			}

			// Calculate key_hash from decrypted key for proper indexing
			keyHash := encryptionSvc.Hash(decryptedKey)

			keyModels = append(keyModels, models.APIKey{
				GroupID:  group.ID,
				KeyValue: keyInfo.KeyValue, // Keep encrypted value
				KeyHash:  keyHash,         // Add calculated hash
				Status:   keyInfo.Status,
			})
		}
		if skippedKeys > 0 {
			logrus.Warnf("Skipped %d keys due to decryption failures during import", skippedKeys)
		}
		prepDuration := time.Since(startPrep)
		logrus.Infof("Key preparation (decrypt+hash) took %v for %d keys", prepDuration, len(keyModels))

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
	Description        string              `json:"description"`
	GroupType          string              `json:"group_type"`
	ChannelType        string              `json:"channel_type"`
	Enabled            bool                `json:"enabled"`
	TestModel          string              `json:"test_model"`
	ValidationEndpoint string              `json:"validation_endpoint"`
	Upstreams          json.RawMessage     `json:"upstreams"`
	ParamOverrides     map[string]any     `json:"param_overrides"`
	Config             map[string]any     `json:"config"`
	HeaderRules        []models.HeaderRule `json:"header_rules"`
	ModelMapping       string              `json:"model_mapping"`
	ProxyKeys          string              `json:"proxy_keys"`
	Sort               int                 `json:"sort"`
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

	// Parse HeaderRules for export format
	// Fail export on parse error to prevent silent data loss
	// If HeaderRules are corrupted, user should know before exporting incomplete data
	var headerRules []models.HeaderRule
	if len(groupData.Group.HeaderRules) > 0 {
		if err := json.Unmarshal(groupData.Group.HeaderRules, &headerRules); err != nil {
			logrus.WithError(err).
				WithField("group", groupData.Group.Name).
				Error("Failed to parse HeaderRules for export")
			response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.export_failed")
			return
		}
	}

	// Build export data structure compatible with existing format
	exportData := GroupExportData{
		Group: GroupExportInfo{
			Name:               groupData.Group.Name,
			DisplayName:        groupData.Group.DisplayName,
			Description:        groupData.Group.Description,
			GroupType:          groupData.Group.GroupType,
			ChannelType:        groupData.Group.ChannelType,
			Enabled:            groupData.Group.Enabled,
			TestModel:          groupData.Group.TestModel,
			ValidationEndpoint: groupData.Group.ValidationEndpoint,
			Upstreams:          json.RawMessage(groupData.Group.Upstreams),
			ParamOverrides:     groupData.Group.ParamOverrides,
			Config:             groupData.Group.Config,
			HeaderRules:        headerRules,
			ModelMapping:       groupData.Group.ModelMapping,
			ProxyKeys:          groupData.Group.ProxyKeys,
			Sort:               groupData.Group.Sort,
		},
		Keys:       make([]KeyExportInfo, 0, len(groupData.Keys)),
		SubGroups:  make([]SubGroupExportInfo, 0, len(groupData.SubGroups)),
		ExportedAt: time.Now().Format(time.RFC3339),
		Version:    "2.0",
	}

	// Convert keys to export format
	for _, key := range groupData.Keys {
		exportData.Keys = append(exportData.Keys, KeyExportInfo{
			KeyValue: key.KeyValue,
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
	filename := fmt.Sprintf("%s_%s_%s.json", filenamePrefix, safeName, time.Now().Format("20060102_150405"))
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

	// Log import summary
	logrus.Infof("Importing group %s with %d keys",
		importData.Group.Name, len(importData.Keys))
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
		createdGroupID, err = importGroupFromExportData(tx, s.ExportImportService, s.EncryptionSvc, s.BulkImportService, importData.Group, importData.Keys, importData.SubGroups)
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
