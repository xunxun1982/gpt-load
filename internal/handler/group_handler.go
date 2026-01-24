// Package handler provides HTTP handlers for the application
package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/i18n"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
)

// handleGroupError is deprecated, use HandleServiceError instead
// Kept for backward compatibility during migration
func (s *Server) handleGroupError(c *gin.Context, err error) bool {
	return HandleServiceError(c, err)
}

// GroupCreateRequest defines the payload for creating a group.
type GroupCreateRequest struct {
	Name                 string                    `json:"name"`
	DisplayName          string                    `json:"display_name"`
	Description          string                    `json:"description"`
	GroupType            string                    `json:"group_type"` // 'standard' or 'aggregate'
	Upstreams            json.RawMessage           `json:"upstreams"`
	ChannelType          string                    `json:"channel_type"`
	Sort                 int                       `json:"sort"`
	TestModel            string                    `json:"test_model"`
	ValidationEndpoint   string                    `json:"validation_endpoint"`
	ParamOverrides       map[string]any            `json:"param_overrides"`
	Config               map[string]any            `json:"config"`
	HeaderRules          []models.HeaderRule       `json:"header_rules"`
	ModelMapping         string                    `json:"model_mapping"` // Deprecated: for backward compatibility
	ModelRedirectRules   map[string]string         `json:"model_redirect_rules"`
	ModelRedirectRulesV2 json.RawMessage           `json:"model_redirect_rules_v2"` // V2: one-to-many mapping
	ModelRedirectStrict  bool                      `json:"model_redirect_strict"`
	PathRedirects        []models.PathRedirectRule `json:"path_redirects"`
	ProxyKeys            string                    `json:"proxy_keys"`
}

// CreateGroup handles the creation of a new group.
func (s *Server) CreateGroup(c *gin.Context) {
	var req GroupCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	params := services.GroupCreateParams{
		Name:                 req.Name,
		DisplayName:          req.DisplayName,
		Description:          req.Description,
		GroupType:            req.GroupType,
		Upstreams:            req.Upstreams,
		ChannelType:          req.ChannelType,
		Sort:                 req.Sort,
		TestModel:            req.TestModel,
		ValidationEndpoint:   req.ValidationEndpoint,
		ParamOverrides:       req.ParamOverrides,
		Config:               req.Config,
		HeaderRules:          req.HeaderRules,
		ModelMapping:         req.ModelMapping, // Deprecated: for backward compatibility
		ModelRedirectRules:   req.ModelRedirectRules,
		ModelRedirectRulesV2: req.ModelRedirectRulesV2,
		ModelRedirectStrict:  req.ModelRedirectStrict,
		PathRedirects:        req.PathRedirects,
		ProxyKeys:            req.ProxyKeys,
	}

	group, err := s.GroupService.CreateGroup(c.Request.Context(), params)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, s.newGroupResponse(group))
}

// ListGroups handles listing all groups.
func (s *Server) ListGroups(c *gin.Context) {
	groups, err := s.GroupService.ListGroups(c.Request.Context())
	if s.handleGroupError(c, err) {
		return
	}

	groupResponses := make([]GroupResponse, 0, len(groups))
	for i := range groups {
		groupResponses = append(groupResponses, *s.newGroupResponse(&groups[i]))
	}

	response.Success(c, groupResponses)
}

// GroupUpdateRequest defines the payload for updating a group.
// Using a dedicated struct avoids issues with zero values being ignored by GORM's Update.
type GroupUpdateRequest struct {
	Name                 *string                   `json:"name,omitempty"`
	DisplayName          *string                   `json:"display_name,omitempty"`
	Description          *string                   `json:"description,omitempty"`
	GroupType            *string                   `json:"group_type,omitempty"`
	Upstreams            json.RawMessage           `json:"upstreams"`
	ChannelType          *string                   `json:"channel_type,omitempty"`
	Sort                 *int                      `json:"sort"`
	TestModel            string                    `json:"test_model"`
	ValidationEndpoint   *string                   `json:"validation_endpoint,omitempty"`
	ParamOverrides       map[string]any            `json:"param_overrides"`
	Config               map[string]any            `json:"config"`
	HeaderRules          []models.HeaderRule       `json:"header_rules"`
	ModelMapping         *string                   `json:"model_mapping,omitempty"` // Deprecated: for backward compatibility
	ModelRedirectRules   map[string]string         `json:"model_redirect_rules"`
	ModelRedirectRulesV2 json.RawMessage           `json:"model_redirect_rules_v2"` // V2: one-to-many mapping
	ModelRedirectStrict  *bool                     `json:"model_redirect_strict"`
	PathRedirects        []models.PathRedirectRule `json:"path_redirects"`
	ProxyKeys            *string                   `json:"proxy_keys,omitempty"`
}

// UpdateGroup handles updating an existing group.
func (s *Server) UpdateGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req GroupUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	params := services.GroupUpdateParams{
		Name:                 req.Name,
		DisplayName:          req.DisplayName,
		Description:          req.Description,
		GroupType:            req.GroupType,
		ChannelType:          req.ChannelType,
		Sort:                 req.Sort,
		ValidationEndpoint:   req.ValidationEndpoint,
		ParamOverrides:       req.ParamOverrides,
		Config:               req.Config,
		ModelMapping:         req.ModelMapping, // Deprecated: for backward compatibility
		ModelRedirectRules:   req.ModelRedirectRules,
		ModelRedirectRulesV2: req.ModelRedirectRulesV2,
		ModelRedirectStrict:  req.ModelRedirectStrict,
		PathRedirects:        req.PathRedirects,
		ProxyKeys:            req.ProxyKeys,
	}

	if req.Upstreams != nil {
		params.Upstreams = req.Upstreams
		params.HasUpstreams = true
	}

	if req.TestModel != "" {
		params.TestModel = req.TestModel
		params.HasTestModel = true
	}

	if req.HeaderRules != nil {
		rules := req.HeaderRules
		params.HeaderRules = &rules
	}

	group, err := s.GroupService.UpdateGroup(c.Request.Context(), uint(id), params)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, s.newGroupResponse(group))
}

// GroupResponse defines the structure for a group response, excluding sensitive or large fields.
type GroupResponse struct {
	ID                   uint                      `json:"id"`
	Name                 string                    `json:"name"`
	Endpoint             string                    `json:"endpoint"`
	DisplayName          string                    `json:"display_name"`
	Description          string                    `json:"description"`
	GroupType            string                    `json:"group_type"`
	Enabled              bool                      `json:"enabled"`
	Upstreams            datatypes.JSON            `json:"upstreams"`
	ChannelType          string                    `json:"channel_type"`
	Sort                 int                       `json:"sort"`
	TestModel            string                    `json:"test_model"`
	ValidationEndpoint   string                    `json:"validation_endpoint"`
	ParamOverrides       datatypes.JSONMap         `json:"param_overrides"`
	Config               datatypes.JSONMap         `json:"config"`
	HeaderRules          []models.HeaderRule       `json:"header_rules"`
	ModelMapping         string                    `json:"model_mapping"` // Deprecated: for backward compatibility
	ModelRedirectRules   datatypes.JSONMap         `json:"model_redirect_rules"`
	ModelRedirectRulesV2 datatypes.JSON            `json:"model_redirect_rules_v2"` // V2: one-to-many mapping
	ModelRedirectStrict  bool                      `json:"model_redirect_strict"`
	PathRedirects        []models.PathRedirectRule `json:"path_redirects"`
	ProxyKeys            string                    `json:"proxy_keys"`
	ParentGroupID        *uint                     `json:"parent_group_id"`
	BoundSiteID          *uint                     `json:"bound_site_id"`
	LastValidatedAt      *time.Time                `json:"last_validated_at"`
	CreatedAt            time.Time                 `json:"created_at"`
	UpdatedAt            time.Time                 `json:"updated_at"`
}

// newGroupResponse creates a new GroupResponse from a models.Group.
func (s *Server) newGroupResponse(group *models.Group) *GroupResponse {
	appURL := s.SettingsManager.GetAppUrl()
	endpoint := ""
	if appURL != "" {
		u, err := url.Parse(appURL)
		if err == nil {
			u.Path = strings.TrimRight(u.Path, "/") + "/proxy/" + group.Name
			endpoint = u.String()
		}
	}

	// Parse header rules from JSON
	var headerRules []models.HeaderRule
	if len(group.HeaderRules) > 0 {
		if err := json.Unmarshal(group.HeaderRules, &headerRules); err != nil {
			logrus.WithError(err).Error("Failed to unmarshal header rules")
			headerRules = make([]models.HeaderRule, 0)
		}
	}
	// Parse path redirects from JSON
	var pathRedirects []models.PathRedirectRule
	if len(group.PathRedirects) > 0 {
		if err := json.Unmarshal(group.PathRedirects, &pathRedirects); err != nil {
			logrus.WithError(err).Error("Failed to unmarshal path redirects")
			pathRedirects = make([]models.PathRedirectRule, 0)
		}
	}

	return &GroupResponse{
		ID:                   group.ID,
		Name:                 group.Name,
		Endpoint:             endpoint,
		DisplayName:          group.DisplayName,
		Description:          group.Description,
		GroupType:            group.GroupType,
		Enabled:              group.Enabled,
		Upstreams:            group.Upstreams,
		ChannelType:          group.ChannelType,
		Sort:                 group.Sort,
		TestModel:            group.TestModel,
		ValidationEndpoint:   group.ValidationEndpoint,
		ParamOverrides:       group.ParamOverrides,
		Config:               group.Config,
		HeaderRules:          headerRules,
		ModelMapping:         group.ModelMapping, // Deprecated: for backward compatibility
		ModelRedirectRules:   group.ModelRedirectRules,
		ModelRedirectRulesV2: group.ModelRedirectRulesV2,
		ModelRedirectStrict:  group.ModelRedirectStrict,
		PathRedirects:        pathRedirects,
		ProxyKeys:            group.ProxyKeys,
		ParentGroupID:        group.ParentGroupID,
		BoundSiteID:          group.BoundSiteID,
		LastValidatedAt:      group.LastValidatedAt,
		CreatedAt:            group.CreatedAt,
		UpdatedAt:            group.UpdatedAt,
	}
}

// DeleteGroup handles deleting a group.
func (s *Server) DeleteGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	err = s.GroupService.DeleteGroup(c.Request.Context(), uint(id))

	// Check if this is an async deletion (returns 202 Accepted with task info)
	if apiErr, ok := err.(*app_errors.APIError); ok && apiErr.HTTPStatus == http.StatusAccepted {
		// Async deletion started - return 202 with task info
		c.JSON(http.StatusAccepted, gin.H{
			"message": apiErr.Message,
			"code":    apiErr.Code,
		})
		return
	}

	// Handle other errors
	if s.handleGroupError(c, err) {
		return
	}

	// Sync deletion completed successfully
	response.SuccessI18n(c, "success.group_deleted", nil)
}

// ConfigOption represents a single configurable option for a group.
type ConfigOption struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	DefaultValue any    `json:"default_value"`
}

// GetGroupConfigOptions returns a list of available configuration options for groups.
func (s *Server) GetGroupConfigOptions(c *gin.Context) {
	options, err := s.GroupService.GetGroupConfigOptions()
	if s.handleGroupError(c, err) {
		return
	}

	translated := make([]ConfigOption, 0, len(options))
	for _, option := range options {
		name := option.Name
		if strings.HasPrefix(name, "config.") {
			name = i18n.Message(c, name)
		}
		description := option.Description
		if strings.HasPrefix(description, "config.") {
			description = i18n.Message(c, description)
		}

		translated = append(translated, ConfigOption{
			Key:          option.Key,
			Name:         name,
			Description:  description,
			DefaultValue: option.DefaultValue,
		})
	}

	response.Success(c, translated)
}

// calculateRequestStats is a helper to compute request statistics.
func (s *Server) GetGroupStats(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	groupID := uint(id)
	groupName := ""
	if s.GroupManager != nil {
		if g, err := s.GroupManager.GetGroupByID(groupID); err == nil && g != nil {
			groupName = g.Name
		}
	}
	if s.shouldDegradeReadDuringTask(groupName) {
		response.Success(c, &services.GroupStats{})
		return
	}

	stats, err := s.GroupService.GetGroupStats(c.Request.Context(), groupID)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, stats)
}

// GroupCopyRequest defines the payload for copying a group.
type GroupCopyRequest struct {
	CopyKeys string `json:"copy_keys"` // "none"|"valid_only"|"all"
}

// GroupCopyResponse defines the response for group copy operation.
type GroupCopyResponse struct {
	Group *GroupResponse `json:"group"`
}

// CopyGroup handles copying a group with optional content.

func (s *Server) CopyGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req GroupCopyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	newGroup, err := s.GroupService.CopyGroup(c.Request.Context(), uint(id), req.CopyKeys)
	if s.handleGroupError(c, err) {
		return
	}

	groupResponse := s.newGroupResponse(newGroup)
	copyResponse := &GroupCopyResponse{
		Group: groupResponse,
	}

	response.Success(c, copyResponse)
}

// List godoc
func (s *Server) List(c *gin.Context) {
	type groupListItem struct {
		ID            uint   `json:"id"`
		Name          string `json:"name"`
		DisplayName   string `json:"display_name"`
		Sort          int    `json:"sort"`
		GroupType     string `json:"group_type"`
		ParentGroupID *uint  `json:"parent_group_id"`
	}

	var groups []groupListItem
	if err := s.DB.Model(&models.Group{}).
		Select("id, name, display_name, sort, group_type, parent_group_id").
		Order("sort ASC, id ASC").
		Find(&groups).Error; err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrDatabase, "database.cannot_get_groups")
		return
	}
	response.Success(c, groups)
}

// AddSubGroupsRequest defines the payload for adding sub groups to an aggregate group
type AddSubGroupsRequest struct {
	SubGroups []services.SubGroupInput `json:"sub_groups"`
}

// UpdateSubGroupWeightRequest defines the payload for updating a sub group weight
type UpdateSubGroupWeightRequest struct {
	Weight int `json:"weight"`
}

// GetSubGroups handles getting sub groups of an aggregate group
func (s *Server) GetSubGroups(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	groupID := uint(id)
	groupName := ""
	if s.GroupManager != nil {
		if g, err := s.GroupManager.GetGroupByID(groupID); err == nil && g != nil {
			groupName = g.Name
		}
	}
	if s.shouldDegradeReadDuringTask(groupName) {
		response.Success(c, []models.SubGroupInfo{})
		return
	}

	subGroups, err := s.AggregateGroupService.GetSubGroups(c.Request.Context(), groupID)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, subGroups)
}

// AddSubGroups handles adding sub groups to an aggregate group
func (s *Server) AddSubGroups(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req AddSubGroupsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := s.AggregateGroupService.AddSubGroups(c.Request.Context(), uint(id), req.SubGroups); s.handleGroupError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.sub_groups_added", nil)
}

// UpdateSubGroupWeight handles updating the weight of a sub group
func (s *Server) UpdateSubGroupWeight(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	subGroupID, err := strconv.Atoi(c.Param("subGroupId"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_sub_group_id")
		return
	}

	var req UpdateSubGroupWeightRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := s.AggregateGroupService.UpdateSubGroupWeight(c.Request.Context(), uint(id), uint(subGroupID), req.Weight); s.handleGroupError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.sub_group_weight_updated", nil)
}

// DeleteSubGroup handles deleting a sub group from an aggregate group
func (s *Server) DeleteSubGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	subGroupID, err := strconv.Atoi(c.Param("subGroupId"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_sub_group_id")
		return
	}

	if err := s.AggregateGroupService.DeleteSubGroup(c.Request.Context(), uint(id), uint(subGroupID)); s.handleGroupError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.sub_group_deleted", nil)
}

// GetParentAggregateGroups handles getting parent aggregate groups that reference a group
func (s *Server) GetParentAggregateGroups(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	groupID := uint(id)
	groupName := ""
	if s.GroupManager != nil {
		if g, err := s.GroupManager.GetGroupByID(groupID); err == nil && g != nil {
			groupName = g.Name
		}
	}
	if s.shouldDegradeReadDuringTask(groupName) {
		response.Success(c, []models.ParentAggregateGroupInfo{})
		return
	}

	parentGroups, err := s.AggregateGroupService.GetParentAggregateGroups(c.Request.Context(), groupID)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, parentGroups)
}

// ToggleGroupEnabledRequest defines the payload for toggling group enabled status
type ToggleGroupEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// ToggleGroupEnabled handles enabling/disabling a group
func (s *Server) ToggleGroupEnabled(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req ToggleGroupEnabledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if err := s.GroupService.ToggleGroupEnabled(c.Request.Context(), uint(id), req.Enabled); s.handleGroupError(c, err) {
		return
	}

	messageKey := "success.group_enabled"
	if !req.Enabled {
		messageKey = "success.group_disabled"
	}
	response.SuccessI18n(c, messageKey, nil)
}

// DeleteAllGroups handles deleting all groups and their associated resources.
// This is a dangerous debugging operation that should only be accessible when DEBUG_MODE is enabled.
//
// Security considerations:
// - This endpoint is only registered when DEBUG_MODE environment variable is set to true
// - It requires authentication like all other admin endpoints
// - It logs a warning before execution
// - It should NEVER be enabled in production environments
//
// The operation will:
// - Delete all sub-group relationships
// - Delete all API keys across all groups
// - Delete all groups
// - Clear the in-memory key cache
// - Invalidate the group cache
//
// Returns a success message if all operations complete successfully,
// or an error if any operation fails (with transaction rollback).
func (s *Server) DeleteAllGroups(c *gin.Context) {
	// Double-check that debug mode is enabled (defense in depth)
	if !s.config.IsDebugMode() {
		response.ErrorI18nFromAPIError(c, app_errors.ErrForbidden, "error.debug_mode_required")
		return
	}

	logrus.WithContext(c.Request.Context()).Warn("DeleteAllGroups endpoint called - proceeding with deletion of all groups")

	if err := s.GroupService.DeleteAllGroups(c.Request.Context()); s.handleGroupError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.all_groups_deleted", nil)
}

// GetGroupModels fetches available models from upstream service
// This handler retrieves the complete model list from the group's upstream API endpoints,
// considering proxy settings and path redirects configured for the group
func (s *Server) GetGroupModels(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	groupModels, err := s.GroupService.FetchGroupModels(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, groupModels)
}

// CreateChildGroupRequest defines the payload for creating a child group
type CreateChildGroupRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// CreateChildGroup handles creating a child group for a standard group
func (s *Server) CreateChildGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	var req CreateChildGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	params := services.CreateChildGroupParams{
		ParentGroupID: uint(id),
		Name:          req.Name,
		DisplayName:   req.DisplayName,
		Description:   req.Description,
	}

	childGroup, err := s.ChildGroupService.CreateChildGroup(c.Request.Context(), params)
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, s.newGroupResponse(childGroup))
}

// GetChildGroups handles getting child groups of a standard group
func (s *Server) GetChildGroups(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	childGroups, err := s.ChildGroupService.GetChildGroups(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, childGroups)
}

// GetParentGroup handles getting the parent group of a child group
func (s *Server) GetParentGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	parentGroup, err := s.ChildGroupService.GetParentGroup(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}

	if parentGroup == nil {
		response.Success(c, nil)
		return
	}

	response.Success(c, s.newGroupResponse(parentGroup))
}

// GetChildGroupCount handles getting the count of child groups for deletion warning
func (s *Server) GetChildGroupCount(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	count, err := s.GroupService.CountChildGroups(c.Request.Context(), uint(id))
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, map[string]int64{"count": count})
}

// GetAllChildGroups handles getting all child groups grouped by parent group ID
func (s *Server) GetAllChildGroups(c *gin.Context) {
	childGroupsMap, err := s.ChildGroupService.GetAllChildGroups(c.Request.Context())
	if s.handleGroupError(c, err) {
		return
	}

	response.Success(c, childGroupsMap)
}

// ModelRedirectDynamicWeightResponse represents the dynamic weight info for a model redirect rule.
type ModelRedirectDynamicWeightResponse struct {
	SourceModel string                      `json:"source_model"`
	Targets     []ModelRedirectTargetWeight `json:"targets"`
}

// ModelRedirectTargetWeight represents the dynamic weight info for a single target.
type ModelRedirectTargetWeight struct {
	Model           string  `json:"model"`
	BaseWeight      int     `json:"base_weight"`
	EffectiveWeight int     `json:"effective_weight"`
	HealthScore     float64 `json:"health_score"`
	SuccessRate     float64 `json:"success_rate"`
	RequestCount    int64   `json:"request_count"`
	LastFailureAt   *string `json:"last_failure_at,omitempty"`
	LastSuccessAt   *string `json:"last_success_at,omitempty"`
	Enabled         bool    `json:"enabled"`
}

// GetModelRedirectDynamicWeights handles getting dynamic weight info for model redirect rules.
// GET /api/groups/:id/model-redirect-weights
func (s *Server) GetModelRedirectDynamicWeights(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		response.ErrorI18nFromAPIError(c, app_errors.ErrBadRequest, "validation.invalid_group_id")
		return
	}

	groupID := uint(id)

	// Get group to access model redirect rules
	group, err := s.GroupManager.GetGroupByID(groupID)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}
	// Defensive nil check to prevent panic if GetGroupByID returns nil group
	if group == nil {
		response.Error(c, app_errors.ErrResourceNotFound)
		return
	}

	// Check if group has V2 model redirect rules
	if len(group.ModelRedirectMapV2) == 0 {
		response.Success(c, []ModelRedirectDynamicWeightResponse{})
		return
	}

	// Build response with dynamic weight info for each rule
	// Sort source models for deterministic API output
	sourceModels := make([]string, 0, len(group.ModelRedirectMapV2))
	for sourceModel := range group.ModelRedirectMapV2 {
		sourceModels = append(sourceModels, sourceModel)
	}
	sort.Strings(sourceModels)

	result := make([]ModelRedirectDynamicWeightResponse, 0, len(group.ModelRedirectMapV2))

	for _, sourceModel := range sourceModels {
		rule := group.ModelRedirectMapV2[sourceModel]
		if rule == nil || len(rule.Targets) == 0 {
			continue
		}

		// Get dynamic weight info for all targets
		dwInfos := services.GetModelRedirectDynamicWeights(s.DynamicWeightManager, groupID, sourceModel, rule)

		targets := make([]ModelRedirectTargetWeight, len(rule.Targets))
		for i, target := range rule.Targets {
			// NOTE: BaseWeight reflects the configured weight regardless of enabled status.
			// This design allows users to see the original configuration value.
			// EffectiveWeight will be 0 for disabled targets, showing actual routing behavior.
			// AI Review: Keeping BaseWeight as configured value for UI clarity.
			targets[i] = ModelRedirectTargetWeight{
				Model:      target.Model,
				BaseWeight: target.GetWeight(),
				Enabled:    target.IsEnabled(),
			}

			// Add dynamic weight info if available
			if i < len(dwInfos) {
				targets[i].EffectiveWeight = dwInfos[i].EffectiveWeight
				targets[i].HealthScore = dwInfos[i].HealthScore
				targets[i].SuccessRate = dwInfos[i].SuccessRate
				targets[i].RequestCount = dwInfos[i].RequestCount
				targets[i].LastFailureAt = dwInfos[i].LastFailureAt
				targets[i].LastSuccessAt = dwInfos[i].LastSuccessAt
			} else {
				// No dynamic weight data, use base weight for effective weight
				// For disabled targets, effective weight should be 0
				if target.IsEnabled() {
					targets[i].EffectiveWeight = target.GetWeight()
				} else {
					targets[i].EffectiveWeight = 0
				}
				targets[i].HealthScore = 1.0
			}
		}

		result = append(result, ModelRedirectDynamicWeightResponse{
			SourceModel: sourceModel,
			Targets:     targets,
		})
	}

	response.Success(c, result)
}
