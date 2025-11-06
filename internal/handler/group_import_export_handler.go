package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// generateUniqueGroupName generates a unique group name, adding a random suffix if it already exists.
func generateUniqueGroupName(tx *gorm.DB, baseName string) (string, error) {
	groupName := baseName
	var existingGroup models.Group
	maxAttempts := 10
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := tx.Where("name = ?", groupName).First(&existingGroup).Error; err != nil {
			return groupName, nil
		}
		if attempt < maxAttempts-1 {
			if len(baseName)+4 > 100 {
				baseName = baseName[:96]
			}
			groupName = baseName + utils.GenerateRandomSuffix()
		} else {
			return "", fmt.Errorf("failed to generate unique group name for %s after %d attempts", baseName, maxAttempts)
		}
	}
	return groupName, nil
}

// importGroupFromExportData imports a group from export data (helper function to reduce code duplication).
func importGroupFromExportData(tx *gorm.DB, groupInfo GroupExportInfo, keys []KeyExportInfo, subGroups []SubGroupExportInfo) (uint, error) {
	groupName, err := generateUniqueGroupName(tx, groupInfo.Name)
	if err != nil {
		return 0, err
	}

	headerRulesJSON, _ := json.Marshal(groupInfo.HeaderRules)
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
		keyModels := make([]models.APIKey, 0, len(keys))
		for _, keyInfo := range keys {
			keyModels = append(keyModels, models.APIKey{
				GroupID:  group.ID,
				KeyValue: keyInfo.KeyValue,
				Status:   keyInfo.Status,
			})
		}
		if err := tx.CreateInBatches(keyModels, 1000).Error; err != nil {
			return 0, err
		}
	}

	if group.GroupType == "aggregate" && len(subGroups) > 0 {
		for _, sgInfo := range subGroups {
			var subGroup models.Group
			if err := tx.Where("name = ?", sgInfo.GroupName).First(&subGroup).Error; err == nil {
				groupSubGroup := models.GroupSubGroup{
					GroupID:    group.ID,
					SubGroupID: subGroup.ID,
					Weight:     sgInfo.Weight,
				}
				if err := tx.Create(&groupSubGroup).Error; err != nil {
					continue
				}
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

	// Get group information, preload keys to avoid N+1 queries
	var group models.Group
	if err := s.DB.Preload("APIKeys").First(&group, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.group_not_found")
		} else {
			response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_group")
		}
		return
	}

	// Parse HeaderRules
	var headerRules []models.HeaderRule
	if len(group.HeaderRules) > 0 {
		if err := json.Unmarshal(group.HeaderRules, &headerRules); err != nil {
			headerRules = []models.HeaderRule{}
		}
	}

	// Build export data
	exportData := GroupExportData{
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
		Keys:       []KeyExportInfo{},
		SubGroups:  []SubGroupExportInfo{},
		ExportedAt: time.Now().Format(time.RFC3339),
		Version:    "2.0",
	}

	// Export keys
	for _, key := range group.APIKeys {
		exportData.Keys = append(exportData.Keys, KeyExportInfo{
			KeyValue: key.KeyValue,
			Status:   key.Status,
		})
	}

	// If it's an aggregate group, get sub-group information
	if group.GroupType == "aggregate" {
		subGroups, err := s.AggregateGroupService.GetSubGroups(c.Request.Context(), uint(id))
		if err == nil {
			for _, sg := range subGroups {
				exportData.SubGroups = append(exportData.SubGroups, SubGroupExportInfo{
					GroupName: sg.Group.Name,
					Weight:    sg.Weight,
				})
			}
		}
	}

	// Set response headers, use different filename prefix based on group type
	var filenamePrefix string
	if group.GroupType == "aggregate" {
		filenamePrefix = "aggregate-group"
	} else {
		filenamePrefix = "standard-group"
	}
	filename := fmt.Sprintf("%s_%s_%s.json", filenamePrefix, group.Name, time.Now().Format("20060102_150405"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Content-Transfer-Encoding", "binary")

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

	// Use transaction to ensure data consistency, rollback on failure
	var createdGroupID uint
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		createdGroupID, err = importGroupFromExportData(tx, importData.Group, importData.Keys, importData.SubGroups)
		return err
	})

	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.import_failed")
		return
	}

	// Query the created group
	var group models.Group
	if err := s.DB.First(&group, createdGroupID).Error; err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_group")
		return
	}

	response.SuccessI18n(c, "success.group_imported", s.newGroupResponse(&group))
}
