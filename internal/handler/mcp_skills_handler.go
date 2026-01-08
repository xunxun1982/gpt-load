package handler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/i18n"
	"gpt-load/internal/mcpskills"
	"gpt-load/internal/response"

	"github.com/gin-gonic/gin"
)

// getServerAddress returns the server address from system settings or derives it from the request
func (s *Server) getServerAddress(c *gin.Context) string {
	// Try to get from system settings (AppUrl)
	settings := s.SettingsManager.GetSettings()
	if settings.AppUrl != "" {
		return strings.TrimSuffix(settings.AppUrl, "/")
	}

	// Fallback: derive from request
	scheme := "https"
	if c.Request.TLS == nil {
		// Check X-Forwarded-Proto header for reverse proxy scenarios
		if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + c.Request.Host
}

// MCP Service Handlers

// ListMCPServices handles GET /api/mcp-skills/services
func (s *Server) ListMCPServices(c *gin.Context) {
	pageStr := c.Query("page")
	if pageStr != "" {
		s.ListMCPServicesPaginated(c)
		return
	}

	services, err := s.MCPSkillsService.ListServices(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, services)
}

// ListMCPServicesPaginated handles paginated service listing
func (s *Server) ListMCPServicesPaginated(c *gin.Context) {
	var params mcpskills.ServiceListParams

	// AI Review Note: Logging parse errors for pagination params was suggested but not adopted.
	// Reason: Silent fallback to defaults is standard practice for user-friendly APIs.
	// Logging such errors would create noise without practical debugging value.
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		page = 1
	}
	params.Page = page

	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if err != nil {
		pageSize = 50
	}
	params.PageSize = pageSize
	params.Search = c.Query("search")
	params.Category = c.Query("category")
	params.Type = c.Query("type")

	if enabledStr := c.Query("enabled"); enabledStr != "" {
		enabled := enabledStr == "true"
		params.Enabled = &enabled
	}

	result, err := s.MCPSkillsService.ListServicesPaginated(c.Request.Context(), params)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, result)
}

// GetMCPService handles GET /api/mcp-skills/services/:id
func (s *Server) GetMCPService(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	service, err := s.MCPSkillsService.GetServiceByID(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, service)
}

// CreateMCPServiceRequest represents the create service request
type CreateMCPServiceRequest struct {
	Name            string                       `json:"name"`
	DisplayName     string                       `json:"display_name"`
	Description     string                       `json:"description"`
	Category        string                       `json:"category"`
	Icon            string                       `json:"icon"`
	Sort            int                          `json:"sort"`
	Enabled         bool                         `json:"enabled"`
	Type            string                       `json:"type"`
	Command         string                       `json:"command,omitempty"`
	Args            []string                     `json:"args,omitempty"`
	Cwd             string                       `json:"cwd,omitempty"`
	APIEndpoint     string                       `json:"api_endpoint,omitempty"`
	APIKeyName      string                       `json:"api_key_name,omitempty"`
	APIKeyValue     string                       `json:"api_key_value,omitempty"`
	APIKeyHeader    string                       `json:"api_key_header,omitempty"`
	APIKeyPrefix    string                       `json:"api_key_prefix,omitempty"`
	RequiredEnvVars []mcpskills.EnvVarDefinition `json:"required_env_vars,omitempty"`
	DefaultEnvs     map[string]string            `json:"default_envs,omitempty"`
	Headers         map[string]string            `json:"headers,omitempty"`
	Tools           []mcpskills.ToolDefinition   `json:"tools,omitempty"`
	RPDLimit        int                          `json:"rpd_limit"`
	MCPEnabled      bool                         `json:"mcp_enabled"`
	AccessToken     string                       `json:"access_token,omitempty"`
}

// CreateMCPService handles POST /api/mcp-skills/services
func (s *Server) CreateMCPService(c *gin.Context) {
	var req CreateMCPServiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	service, err := s.MCPSkillsService.CreateService(c.Request.Context(), mcpskills.CreateServiceParams{
		Name:            req.Name,
		DisplayName:     req.DisplayName,
		Description:     req.Description,
		Category:        req.Category,
		Icon:            req.Icon,
		Sort:            req.Sort,
		Enabled:         req.Enabled,
		Type:            req.Type,
		Command:         req.Command,
		Args:            req.Args,
		Cwd:             req.Cwd,
		APIEndpoint:     req.APIEndpoint,
		APIKeyName:      req.APIKeyName,
		APIKeyValue:     req.APIKeyValue,
		APIKeyHeader:    req.APIKeyHeader,
		APIKeyPrefix:    req.APIKeyPrefix,
		RequiredEnvVars: req.RequiredEnvVars,
		DefaultEnvs:     req.DefaultEnvs,
		Headers:         req.Headers,
		Tools:           req.Tools,
		RPDLimit:        req.RPDLimit,
		MCPEnabled:      req.MCPEnabled,
		AccessToken:     req.AccessToken,
	})
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.service_created", service)
}

// UpdateMCPServiceRequest represents the update service request
type UpdateMCPServiceRequest struct {
	Name            *string                        `json:"name,omitempty"`
	DisplayName     *string                        `json:"display_name,omitempty"`
	Description     *string                        `json:"description,omitempty"`
	Category        *string                        `json:"category,omitempty"`
	Icon            *string                        `json:"icon,omitempty"`
	Sort            *int                           `json:"sort,omitempty"`
	Enabled         *bool                          `json:"enabled,omitempty"`
	Type            *string                        `json:"type,omitempty"`
	Command         *string                        `json:"command,omitempty"`
	Args            *[]string                      `json:"args,omitempty"`
	Cwd             *string                        `json:"cwd,omitempty"`
	APIEndpoint     *string                        `json:"api_endpoint,omitempty"`
	APIKeyName      *string                        `json:"api_key_name,omitempty"`
	APIKeyValue     *string                        `json:"api_key_value,omitempty"`
	APIKeyHeader    *string                        `json:"api_key_header,omitempty"`
	APIKeyPrefix    *string                        `json:"api_key_prefix,omitempty"`
	RequiredEnvVars *[]mcpskills.EnvVarDefinition  `json:"required_env_vars,omitempty"`
	DefaultEnvs     *map[string]string             `json:"default_envs,omitempty"`
	Headers         *map[string]string             `json:"headers,omitempty"`
	Tools           *[]mcpskills.ToolDefinition    `json:"tools,omitempty"`
	RPDLimit        *int                           `json:"rpd_limit,omitempty"`
	MCPEnabled      *bool                          `json:"mcp_enabled,omitempty"`
	AccessToken     *string                        `json:"access_token,omitempty"`
}

// UpdateMCPService handles PUT /api/mcp-skills/services/:id
func (s *Server) UpdateMCPService(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	var req UpdateMCPServiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	service, err := s.MCPSkillsService.UpdateService(c.Request.Context(), uint(id), mcpskills.UpdateServiceParams{
		Name:            req.Name,
		DisplayName:     req.DisplayName,
		Description:     req.Description,
		Category:        req.Category,
		Icon:            req.Icon,
		Sort:            req.Sort,
		Enabled:         req.Enabled,
		Type:            req.Type,
		Command:         req.Command,
		Args:            req.Args,
		Cwd:             req.Cwd,
		APIEndpoint:     req.APIEndpoint,
		APIKeyName:      req.APIKeyName,
		APIKeyValue:     req.APIKeyValue,
		APIKeyHeader:    req.APIKeyHeader,
		APIKeyPrefix:    req.APIKeyPrefix,
		RequiredEnvVars: req.RequiredEnvVars,
		DefaultEnvs:     req.DefaultEnvs,
		Headers:         req.Headers,
		Tools:           req.Tools,
		RPDLimit:        req.RPDLimit,
		MCPEnabled:      req.MCPEnabled,
		AccessToken:     req.AccessToken,
	})
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.service_updated", service)
}

// DeleteMCPService handles DELETE /api/mcp-skills/services/:id
func (s *Server) DeleteMCPService(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	if err := s.MCPSkillsService.DeleteService(c.Request.Context(), uint(id)); HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.service_deleted", nil)
}

// DeleteAllMCPServices handles DELETE /api/mcp-skills/services/all
// Deletes ALL MCP services and clears service references from groups
// Requires ?confirm=true query parameter to prevent accidental deletion
func (s *Server) DeleteAllMCPServices(c *gin.Context) {
	// Require explicit confirmation to prevent accidental deletion
	if c.Query("confirm") != "true" {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation,
			"Add ?confirm=true to confirm deletion of all services"))
		return
	}

	deleted, err := s.MCPSkillsService.DeleteAllServices(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.services_deleted_all", map[string]int64{"deleted": deleted}, map[string]any{"deleted": deleted})
}

// CountAllMCPServices handles GET /api/mcp-skills/services/count
func (s *Server) CountAllMCPServices(c *gin.Context) {
	count, err := s.MCPSkillsService.CountAllServices(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, map[string]int64{"count": count})
}

// ToggleMCPServiceEnabled handles POST /api/mcp-skills/services/:id/toggle
func (s *Server) ToggleMCPServiceEnabled(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	service, err := s.MCPSkillsService.ToggleServiceEnabled(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}

	status := "mcp_skills.status.disabled"
	if service.Enabled {
		status = "mcp_skills.status.enabled"
	}
	// Translate status before passing to template
	translatedStatus := i18n.Message(c, status)
	response.SuccessI18n(c, "mcp_skills.service_toggled", service, map[string]any{"status": translatedStatus})
}

// GetAPIBridgeTemplates handles GET /api/mcp-skills/templates
func (s *Server) GetAPIBridgeTemplates(c *gin.Context) {
	templates := s.MCPSkillsService.GetAPIBridgeTemplates()
	response.Success(c, templates)
}

// CreateServiceFromTemplateRequest represents the request to create service from template
type CreateServiceFromTemplateRequest struct {
	TemplateID    string `json:"template_id"`
	APIKeyValue   string `json:"api_key_value"`
	CustomEndpoint string `json:"custom_endpoint,omitempty"` // Optional custom API endpoint
}

// CreateMCPServiceFromTemplate handles POST /api/mcp-skills/services/from-template
func (s *Server) CreateMCPServiceFromTemplate(c *gin.Context) {
	var req CreateServiceFromTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	service, err := s.MCPSkillsService.CreateServiceFromTemplate(c.Request.Context(), req.TemplateID, req.APIKeyValue, req.CustomEndpoint)
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.service_created", service)
}

// TestMCPServiceRequest represents the request to test a service
type TestMCPServiceRequest struct {
	ToolName   string                 `json:"tool_name,omitempty"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
}

// TestMCPService handles POST /api/mcp-skills/services/:id/test
// Tests if an MCP service is working correctly by executing a simple API call
func (s *Server) TestMCPService(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	var req TestMCPServiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body for simple test
		req = TestMCPServiceRequest{}
	}

	result, err := s.MCPSkillsService.TestService(c.Request.Context(), uint(id), req.ToolName, req.Arguments)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, result)
}

// MCP Service Group Handlers

// ListMCPServiceGroups handles GET /api/mcp-skills/groups
func (s *Server) ListMCPServiceGroups(c *gin.Context) {
	pageStr := c.Query("page")
	if pageStr != "" {
		s.ListMCPServiceGroupsPaginated(c)
		return
	}

	groups, err := s.MCPSkillsGroupService.ListGroups(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, groups)
}

// ListMCPServiceGroupsPaginated handles paginated group listing
func (s *Server) ListMCPServiceGroupsPaginated(c *gin.Context) {
	var params mcpskills.GroupListParams

	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		page = 1
	}
	params.Page = page

	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if err != nil {
		pageSize = 50
	}
	params.PageSize = pageSize
	params.Search = c.Query("search")

	if enabledStr := c.Query("enabled"); enabledStr != "" {
		enabled := enabledStr == "true"
		params.Enabled = &enabled
	}

	result, err := s.MCPSkillsGroupService.ListGroupsPaginated(c.Request.Context(), params)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, result)
}

// GetMCPServiceGroup handles GET /api/mcp-skills/groups/:id
func (s *Server) GetMCPServiceGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	group, err := s.MCPSkillsGroupService.GetGroupByID(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, group)
}

// GetMCPGroupServicesWithTools handles GET /api/mcp-skills/groups/:id/services-with-tools
func (s *Server) GetMCPGroupServicesWithTools(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	result, err := s.MCPSkillsGroupService.GetGroupServicesWithTools(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, result)
}

// CreateMCPServiceGroupRequest represents the create group request
type CreateMCPServiceGroupRequest struct {
	Name               string                          `json:"name"`
	DisplayName        string                          `json:"display_name"`
	Description        string                          `json:"description"`
	ServiceIDs         []uint                          `json:"service_ids"`
	ServiceWeights     map[uint]int                    `json:"service_weights,omitempty"`
	ToolAliases        map[string][]string             `json:"tool_aliases,omitempty"`
	ToolAliasConfigs   map[string]mcpskills.ToolAliasConfig `json:"tool_alias_configs,omitempty"` // New format with descriptions
	Enabled            bool                            `json:"enabled"`
	AggregationEnabled bool                            `json:"aggregation_enabled"`
	AccessToken        string                          `json:"access_token,omitempty"`
}

// CreateMCPServiceGroup handles POST /api/mcp-skills/groups
func (s *Server) CreateMCPServiceGroup(c *gin.Context) {
	var req CreateMCPServiceGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	group, err := s.MCPSkillsGroupService.CreateGroup(c.Request.Context(), mcpskills.CreateGroupParams{
		Name:               req.Name,
		DisplayName:        req.DisplayName,
		Description:        req.Description,
		ServiceIDs:         req.ServiceIDs,
		ServiceWeights:     req.ServiceWeights,
		ToolAliases:        req.ToolAliases,
		ToolAliasConfigs:   req.ToolAliasConfigs,
		Enabled:            req.Enabled,
		AggregationEnabled: req.AggregationEnabled,
		AccessToken:        req.AccessToken,
	})
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.group_created", group)
}

// UpdateMCPServiceGroupRequest represents the update group request
type UpdateMCPServiceGroupRequest struct {
	Name               *string                           `json:"name,omitempty"`
	DisplayName        *string                           `json:"display_name,omitempty"`
	Description        *string                           `json:"description,omitempty"`
	ServiceIDs         *[]uint                           `json:"service_ids,omitempty"`
	ServiceWeights     *map[uint]int                     `json:"service_weights,omitempty"`
	ToolAliases        *map[string][]string              `json:"tool_aliases,omitempty"`
	ToolAliasConfigs   *map[string]mcpskills.ToolAliasConfig `json:"tool_alias_configs,omitempty"` // New format with descriptions
	Enabled            *bool                             `json:"enabled,omitempty"`
	AggregationEnabled *bool                             `json:"aggregation_enabled,omitempty"`
	AccessToken        *string                           `json:"access_token,omitempty"`
}

// UpdateMCPServiceGroup handles PUT /api/mcp-skills/groups/:id
func (s *Server) UpdateMCPServiceGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	var req UpdateMCPServiceGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	group, err := s.MCPSkillsGroupService.UpdateGroup(c.Request.Context(), uint(id), mcpskills.UpdateGroupParams{
		Name:               req.Name,
		DisplayName:        req.DisplayName,
		Description:        req.Description,
		ServiceIDs:         req.ServiceIDs,
		ServiceWeights:     req.ServiceWeights,
		ToolAliases:        req.ToolAliases,
		ToolAliasConfigs:   req.ToolAliasConfigs,
		Enabled:            req.Enabled,
		AggregationEnabled: req.AggregationEnabled,
		AccessToken:        req.AccessToken,
	})
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.group_updated", group)
}

// DeleteMCPServiceGroup handles DELETE /api/mcp-skills/groups/:id
func (s *Server) DeleteMCPServiceGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	if err := s.MCPSkillsGroupService.DeleteGroup(c.Request.Context(), uint(id)); HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.group_deleted", nil)
}

// ToggleMCPServiceGroupEnabled handles POST /api/mcp-skills/groups/:id/toggle
func (s *Server) ToggleMCPServiceGroupEnabled(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	group, err := s.MCPSkillsGroupService.ToggleGroupEnabled(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}

	status := "mcp_skills.status.disabled"
	if group.Enabled {
		status = "mcp_skills.status.enabled"
	}
	// AI Review: Use group-specific i18n key for semantic clarity
	// Translate status before passing to template
	translatedStatus := i18n.Message(c, status)
	response.SuccessI18n(c, "mcp_skills.group_toggled", group, map[string]any{"status": translatedStatus})
}

// AddServicesToGroupRequest represents the request to add services to a group
type AddServicesToGroupRequest struct {
	ServiceIDs []uint `json:"service_ids"`
}

// AddServicesToMCPGroup handles POST /api/mcp-skills/groups/:id/services
func (s *Server) AddServicesToMCPGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	var req AddServicesToGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	group, err := s.MCPSkillsGroupService.AddServicesToGroup(c.Request.Context(), uint(id), req.ServiceIDs)
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.group_updated", group)
}

// RemoveServicesFromMCPGroup handles DELETE /api/mcp-skills/groups/:id/services
func (s *Server) RemoveServicesFromMCPGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	var req AddServicesToGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	group, err := s.MCPSkillsGroupService.RemoveServicesFromGroup(c.Request.Context(), uint(id), req.ServiceIDs)
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.group_updated", group)
}

// ExportMCPGroupAsSkill handles GET /api/mcp-skills/groups/:id/export
// Supports both Authorization header and ?token= query parameter for authentication
func (s *Server) ExportMCPGroupAsSkill(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	// Get server address from system settings or request
	serverAddress := s.getServerAddress(c)

	// SECURITY: Use group's own access token instead of request auth token
	// This prevents leaking system API keys (AUTH_KEY) in exported files
	// The group's access token is specifically generated for MCP endpoint access
	groupToken, err := s.MCPSkillsGroupService.GetGroupAccessToken(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}

	zipBuffer, filename, err := s.MCPSkillsExportService.ExportGroupAsSkill(c.Request.Context(), uint(id), serverAddress, groupToken)
	if HandleServiceError(c, err) {
		return
	}

	zipData := zipBuffer.Bytes()
	fileSize := len(zipData)

	// Bypass gzip middleware for binary file download
	c.Request.Header.Del("Accept-Encoding")

	// Set headers for file download
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, filename))
	c.Header("Content-Length", strconv.Itoa(fileSize))
	c.Header("Content-Type", "application/zip")
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")

	c.Status(200)
	_, _ = c.Writer.Write(zipData)
}


// GetMCPGroupEndpointInfo handles GET /api/mcp-skills/groups/:id/endpoint-info
func (s *Server) GetMCPGroupEndpointInfo(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	// Get server address from system settings or request
	serverAddress := s.getServerAddress(c)

	info, err := s.MCPSkillsGroupService.GetGroupEndpointInfo(c.Request.Context(), uint(id), serverAddress)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, info)
}

// RegenerateMCPGroupAccessToken handles POST /api/mcp-skills/groups/:id/regenerate-token
func (s *Server) RegenerateMCPGroupAccessToken(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	token, err := s.MCPSkillsGroupService.RegenerateAccessToken(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.token_regenerated", map[string]string{"access_token": token})
}

// GetMCPGroupAccessToken handles GET /api/mcp-skills/groups/:id/access-token
func (s *Server) GetMCPGroupAccessToken(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	token, err := s.MCPSkillsGroupService.GetGroupAccessToken(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, map[string]string{"access_token": token})
}

// extractAccessToken extracts access token from request using multiple sources
// Priority: query "key" -> X-Access-Token header -> Bearer token from Authorization header
func extractAccessToken(c *gin.Context) string {
	if token := c.Query("key"); token != "" {
		return token
	}
	if token := c.GetHeader("X-Access-Token"); token != "" {
		return token
	}
	if auth := c.GetHeader("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

// handleJSONRPCAuthError handles authentication errors for JSON-RPC endpoints.
// Returns JSON-RPC formatted error response instead of standard REST JSON response.
// This ensures consistent response format for MCP protocol endpoints.
// AI Review: Adopted suggestion to use JSON-RPC error format for auth failures in MCP endpoints.
func handleJSONRPCAuthError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	// Return JSON-RPC 2.0 formatted error for authentication/authorization failures
	c.JSON(200, mcpskills.MCPResponse{
		JSONRPC: "2.0",
		Error:   &mcpskills.MCPError{Code: -32000, Message: "Authentication failed: " + err.Error()},
	})
	return true
}

// HandleAggregationMCP handles POST /mcp/aggregation/:name - MCP Aggregation JSON-RPC endpoint
// This endpoint exposes only search_tools and execute_tool for reduced context usage
func (s *Server) HandleAggregationMCP(c *gin.Context) {
	groupName := c.Param("name")
	accessToken := extractAccessToken(c)

	// Get group with token validation
	// AI Review: Use JSON-RPC error format for auth failures to maintain protocol consistency
	group, err := s.MCPSkillsGroupService.GetGroupByNameWithToken(c.Request.Context(), groupName, accessToken)
	if handleJSONRPCAuthError(c, err) {
		return
	}

	// AI Review Note: Suggestion to use HTTP 200 for all JSON-RPC errors was NOT adopted.
	// Reason: JSON-RPC 2.0 over HTTP specification (jsonrpc.org/historical/json-rpc-over-http.html)
	// explicitly defines HTTP status codes for different error types:
	// - Server errors (-32000 to -32099) should return HTTP 500
	// Using 503 for disabled group is intentional to indicate service unavailability.
	if !group.Enabled {
		c.JSON(503, mcpskills.MCPResponse{
			JSONRPC: "2.0",
			Error:   &mcpskills.MCPError{Code: -32000, Message: "Group is disabled"},
		})
		return
	}

	// Using 400 for configuration error (aggregation not enabled) follows REST semantics
	// while still providing JSON-RPC formatted error response for client compatibility.
	if !group.AggregationEnabled {
		c.JSON(400, mcpskills.MCPResponse{
			JSONRPC: "2.0",
			Error:   &mcpskills.MCPError{Code: -32000, Message: "MCP Aggregation is not enabled for this group"},
		})
		return
	}

	// Parse MCP request
	// AI Review Note: Suggestion to use HTTP 200 for parse errors was NOT adopted.
	// Reason: JSON-RPC 2.0 over HTTP spec defines -32700 Parse error should return HTTP 500,
	// but HTTP 400 is more semantically correct for malformed client requests.
	// This is a deliberate deviation that aligns with REST best practices.
	var req mcpskills.MCPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, mcpskills.MCPResponse{
			JSONRPC: "2.0",
			Error:   &mcpskills.MCPError{Code: -32700, Message: "Parse error: " + err.Error()},
		})
		return
	}

	// Handle the MCP request
	resp := s.MCPSkillsAggregationHandler.HandleMCPRequest(c.Request.Context(), group, &req)
	c.JSON(200, resp)
}


// ExportMCPSkills handles GET /api/mcp-skills/export
// Exports all MCP services and groups
func (s *Server) ExportMCPSkills(c *gin.Context) {
	// Determine export mode: plain or encrypted (default encrypted)
	plainMode := c.Query("mode") == "plain"

	services, err := s.MCPSkillsService.ExportServices(c.Request.Context(), plainMode)
	if HandleServiceError(c, err) {
		return
	}

	groups, err := s.MCPSkillsGroupService.ExportGroups(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}

	exportData := mcpskills.MCPSkillsExportData{
		Version:    "1.0",
		ExportedAt: time.Now().Format(time.RFC3339),
		Services:   services,
		Groups:     groups,
	}

	response.Success(c, exportData)
}

// ImportMCPSkillsRequest represents the import request
type ImportMCPSkillsRequest struct {
	Version    string                            `json:"version"`
	ExportedAt string                            `json:"exported_at"`
	Services   []mcpskills.MCPServiceExportInfo  `json:"services"`
	Groups     []mcpskills.MCPServiceGroupExportInfo `json:"groups"`
}

// ImportMCPSkills handles POST /api/mcp-skills/import
// Imports MCP services and groups from export data
func (s *Server) ImportMCPSkills(c *gin.Context) {
	// Determine import mode: plain or encrypted (default encrypted)
	plainMode := c.Query("mode") == "plain"

	var req ImportMCPSkillsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	// Import services first (groups depend on services)
	servicesImported, servicesSkipped, err := s.MCPSkillsService.ImportServices(c.Request.Context(), req.Services, plainMode)
	if HandleServiceError(c, err) {
		return
	}

	// Import groups
	groupsImported, groupsSkipped, err := s.MCPSkillsGroupService.ImportGroups(c.Request.Context(), req.Groups)
	if HandleServiceError(c, err) {
		return
	}

	result := mcpskills.MCPSkillsImportResult{
		ServicesImported: servicesImported,
		ServicesSkipped:  servicesSkipped,
		GroupsImported:   groupsImported,
		GroupsSkipped:    groupsSkipped,
	}

	response.SuccessI18n(c, "mcp_skills.import_completed", result)
}

// ImportMCPServersRequest represents the request to import MCP servers from standard JSON format
type ImportMCPServersRequest struct {
	MCPServers map[string]mcpskills.MCPServerConfig `json:"mcpServers"`
}

// ImportMCPServers handles POST /api/mcp-skills/import-mcp-json
// Imports MCP services from standard MCP JSON configuration format (Claude Desktop, Kiro, etc.)
func (s *Server) ImportMCPServers(c *gin.Context) {
	var req ImportMCPServersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if len(req.MCPServers) == 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, "No MCP servers found in configuration"))
		return
	}

	config := mcpskills.MCPServersConfig{
		MCPServers: req.MCPServers,
	}

	result, err := s.MCPSkillsService.ImportMCPServersFromJSON(c.Request.Context(), config)
	if HandleServiceError(c, err) {
		return
	}

	response.SuccessI18n(c, "mcp_skills.mcp_json_import_completed", result)
}

// GetMCPServiceEndpointInfo handles GET /api/mcp-skills/services/:id/endpoint-info
func (s *Server) GetMCPServiceEndpointInfo(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	serverAddress := s.getServerAddress(c)
	info, err := s.MCPSkillsService.GetServiceEndpointInfo(c.Request.Context(), uint(id), serverAddress)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, info)
}

// ToggleMCPServiceMCPEnabled handles POST /api/mcp-skills/services/:id/toggle-mcp
func (s *Server) ToggleMCPServiceMCPEnabled(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	service, err := s.MCPSkillsService.ToggleMCPEnabled(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}

	// Use i18n response for consistency with ToggleMCPServiceEnabled
	status := "mcp_skills.status.disabled"
	if service.MCPEnabled {
		status = "mcp_skills.status.enabled"
	}
	// Translate status before passing to template
	translatedStatus := i18n.Message(c, status)
	response.SuccessI18n(c, "mcp_skills.mcp_toggled", service, map[string]any{"status": translatedStatus})
}

// RegenerateMCPServiceAccessToken handles POST /api/mcp-skills/services/:id/regenerate-token
func (s *Server) RegenerateMCPServiceAccessToken(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	token, err := s.MCPSkillsService.RegenerateServiceAccessToken(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.token_regenerated", map[string]string{"access_token": token})
}

// GetMCPServiceTools handles GET /api/mcp-skills/services/:id/tools
// Returns the tools for a service with caching support
func (s *Server) GetMCPServiceTools(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	// Check if force refresh is requested
	forceRefresh := c.Query("refresh") == "true"

	result, err := s.MCPSkillsService.GetServiceTools(c.Request.Context(), uint(id), forceRefresh)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, result)
}

// RefreshMCPServiceTools handles POST /api/mcp-skills/services/:id/tools/refresh
// Forces a refresh of the tool cache for a service
func (s *Server) RefreshMCPServiceTools(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	result, err := s.MCPSkillsService.RefreshServiceToolCache(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "mcp_skills.tools_refreshed", result)
}

// HandleServiceMCP handles POST /mcp/service/:id - Single service MCP JSON-RPC endpoint
// This endpoint exposes the service's tools via standard MCP protocol
// Using ID instead of name to support duplicate service names
func (s *Server) HandleServiceMCP(c *gin.Context) {
	// AI Review Note: Suggestion to use HTTP 200 for invalid params was NOT adopted.
	// Reason: JSON-RPC 2.0 over HTTP spec defines -32602 Invalid params should return HTTP 500,
	// but HTTP 400 is more semantically correct for invalid client input.
	// This is a deliberate deviation that aligns with REST best practices.
	serviceID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, mcpskills.MCPResponse{
			JSONRPC: "2.0",
			Error:   &mcpskills.MCPError{Code: -32602, Message: "Invalid service ID"},
		})
		return
	}

	accessToken := extractAccessToken(c)

	// Get service with token validation
	// AI Review: Use JSON-RPC error format for auth failures to maintain protocol consistency
	service, err := s.MCPSkillsService.GetServiceByIDWithToken(c.Request.Context(), uint(serviceID), accessToken)
	if handleJSONRPCAuthError(c, err) {
		return
	}

	// Parse MCP request
	// AI Review Note: HTTP 400 for parse errors is intentional (see HandleAggregationMCP comment)
	var req mcpskills.MCPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, mcpskills.MCPResponse{
			JSONRPC: "2.0",
			Error:   &mcpskills.MCPError{Code: -32700, Message: "Parse error: " + err.Error()},
		})
		return
	}

	// Handle the MCP request
	resp := s.MCPSkillsServiceHandler.HandleMCPRequest(c.Request.Context(), service, &req)
	c.JSON(200, resp)
}
