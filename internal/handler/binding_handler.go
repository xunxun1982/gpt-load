package handler

import (
	"strconv"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/response"

	"github.com/gin-gonic/gin"
)

// BindGroupToSiteRequest defines the payload for binding a group to a site
type BindGroupToSiteRequest struct {
	SiteID uint `json:"site_id"`
}

// BindGroupToSite handles binding a group to a managed site
func (s *Server) BindGroupToSite(c *gin.Context) {
	groupID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	var req BindGroupToSiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if req.SiteID == 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, "site_id is required"))
		return
	}

	if err := s.BindingService.BindGroupToSite(c.Request.Context(), uint(groupID), req.SiteID); HandleServiceError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.group_bound_to_site", nil)
}

// UnbindGroupFromSite handles unbinding a group from its bound site
func (s *Server) UnbindGroupFromSite(c *gin.Context) {
	groupID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	if err := s.BindingService.UnbindGroupFromSite(c.Request.Context(), uint(groupID)); HandleServiceError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.group_unbound_from_site", nil)
}

// GetBoundSiteInfo returns the bound site info for a group
func (s *Server) GetBoundSiteInfo(c *gin.Context) {
	groupID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	siteInfo, err := s.BindingService.GetBoundSiteInfo(c.Request.Context(), uint(groupID))
	if HandleServiceError(c, err) {
		return
	}

	response.Success(c, siteInfo)
}

// ListSitesForBinding returns sites available for binding
func (s *Server) ListSitesForBinding(c *gin.Context) {
	sites, err := s.BindingService.ListSitesForBinding(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}

	response.Success(c, sites)
}

// UnbindSiteFromGroup handles unbinding a site from its bound group
func (s *Server) UnbindSiteFromGroup(c *gin.Context) {
	siteID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	if err := s.BindingService.UnbindSiteFromGroup(c.Request.Context(), uint(siteID)); HandleServiceError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.site_unbound_from_group", nil)
}

// GetBoundGroupInfo returns the bound group info for a site
func (s *Server) GetBoundGroupInfo(c *gin.Context) {
	siteID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	groupInfo, err := s.BindingService.GetBoundGroupInfo(c.Request.Context(), uint(siteID))
	if HandleServiceError(c, err) {
		return
	}

	if groupInfo == nil {
		response.Success(c, nil)
		return
	}

	// Return minimal group info
	response.Success(c, map[string]interface{}{
		"id":           groupInfo.ID,
		"name":         groupInfo.Name,
		"display_name": groupInfo.DisplayName,
	})
}
