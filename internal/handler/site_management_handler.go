package handler

import (
	"fmt"
	"strconv"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/response"
	"gpt-load/internal/sitemanagement"

	"github.com/gin-gonic/gin"
)

type CreateManagedSiteRequest struct {
	Name        string `json:"name"`
	Notes       string `json:"notes"`
	Description string `json:"description"`
	Sort        int    `json:"sort"`
	Enabled     bool   `json:"enabled"`

	BaseURL        string `json:"base_url"`
	SiteType       string `json:"site_type"`
	UserID         string `json:"user_id"`
	CheckInPageURL string `json:"checkin_page_url"`

	CheckInAvailable bool   `json:"checkin_available"`
	CheckInEnabled   bool   `json:"checkin_enabled"`
	CustomCheckInURL string `json:"custom_checkin_url"`

	AuthType  string `json:"auth_type"`
	AuthValue string `json:"auth_value"`
}

type UpdateManagedSiteRequest struct {
	Name        *string `json:"name"`
	Notes       *string `json:"notes"`
	Description *string `json:"description"`
	Sort        *int    `json:"sort"`
	Enabled     *bool   `json:"enabled"`

	BaseURL        *string `json:"base_url"`
	SiteType       *string `json:"site_type"`
	UserID         *string `json:"user_id"`
	CheckInPageURL *string `json:"checkin_page_url"`

	CheckInAvailable *bool   `json:"checkin_available"`
	CheckInEnabled   *bool   `json:"checkin_enabled"`
	CustomCheckInURL *string `json:"custom_checkin_url"`

	AuthType  *string `json:"auth_type"`
	AuthValue *string `json:"auth_value"`
}

func (s *Server) ListManagedSites(c *gin.Context) {
	// Route to paginated or non-paginated endpoint based on query params.
	// Design decision: Keep both endpoints for backward compatibility and different use cases:
	// - Non-paginated: Uses cache (30s TTL), fast for small datasets and internal use
	// - Paginated: For frontend table with filters, search, and large datasets
	// AI suggested always using pagination, but this dual approach provides flexibility
	// and better performance for cached full-list scenarios.
	pageStr := c.Query("page")
	if pageStr != "" {
		// Use paginated endpoint
		s.ListManagedSitesPaginated(c)
		return
	}

	// Non-paginated (legacy) endpoint
	sites, err := s.SiteService.ListSites(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, sites)
}

// ListManagedSitesPaginated handles paginated site listing with filters
func (s *Server) ListManagedSitesPaginated(c *gin.Context) {
	var params sitemanagement.SiteListParams

	// Parse pagination params with fallback to defaults on parse error.
	// Service layer performs additional bounds validation (page >= 1, pageSize <= 200).
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

	// Parse boolean filter params using direct string comparison.
	// AI suggested using strconv.ParseBool for flexibility (accepts "1", "t", "TRUE", etc.),
	// but we intentionally use strict "true" comparison because:
	// 1. API behavior should be predictable - only "true" means true
	// 2. Consistent with other handlers in this project (e.g., base_channel.go)
	// 3. Avoids ambiguity in API documentation and client implementations
	if enabledStr := c.Query("enabled"); enabledStr != "" {
		enabled := enabledStr == "true"
		params.Enabled = &enabled
	}
	if checkinStr := c.Query("checkin_available"); checkinStr != "" {
		checkin := checkinStr == "true"
		params.CheckinAvailable = &checkin
	}

	result, err := s.SiteService.ListSitesPaginated(c.Request.Context(), params)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, result)
}

func (s *Server) CreateManagedSite(c *gin.Context) {
	var req CreateManagedSiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	site, err := s.SiteService.CreateSite(c.Request.Context(), sitemanagement.CreateSiteParams{
		Name:             req.Name,
		Notes:            req.Notes,
		Description:      req.Description,
		Sort:             req.Sort,
		Enabled:          req.Enabled,
		BaseURL:          req.BaseURL,
		SiteType:         req.SiteType,
		UserID:           req.UserID,
		CheckInPageURL:   req.CheckInPageURL,
		CheckInAvailable: req.CheckInAvailable,
		CheckInEnabled:   req.CheckInEnabled,
		CustomCheckInURL: req.CustomCheckInURL,
		AuthType:         req.AuthType,
		AuthValue:        req.AuthValue,
	})
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, site)
}

func (s *Server) UpdateManagedSite(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	var req UpdateManagedSiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	site, err := s.SiteService.UpdateSite(c.Request.Context(), uint(id), sitemanagement.UpdateSiteParams{
		Name:             req.Name,
		Notes:            req.Notes,
		Description:      req.Description,
		Sort:             req.Sort,
		Enabled:          req.Enabled,
		BaseURL:          req.BaseURL,
		SiteType:         req.SiteType,
		UserID:           req.UserID,
		CheckInPageURL:   req.CheckInPageURL,
		CheckInAvailable: req.CheckInAvailable,
		CheckInEnabled:   req.CheckInEnabled,
		CustomCheckInURL: req.CustomCheckInURL,
		AuthType:         req.AuthType,
		AuthValue:        req.AuthValue,
	})
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, site)
}

func (s *Server) DeleteManagedSite(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	if err := s.SiteService.DeleteSite(c.Request.Context(), uint(id)); HandleServiceError(c, err) {
		return
	}
	response.Success(c, nil)
}

// DeleteAllUnboundSites deletes all sites that are not bound to any group
func (s *Server) DeleteAllUnboundSites(c *gin.Context) {
	deleted, err := s.SiteService.DeleteAllUnboundSites(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.SuccessI18n(c, "success.unbound_sites_deleted", map[string]interface{}{
		"count": deleted,
	}, map[string]any{"count": deleted})
}

// CountUnboundSites returns the count of sites not bound to any group
func (s *Server) CountUnboundSites(c *gin.Context) {
	count, err := s.SiteService.CountUnboundSites(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, map[string]int64{"count": count})
}

// CopyManagedSite creates a copy of an existing site
func (s *Server) CopyManagedSite(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	site, err := s.SiteService.CopySite(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, site)
}

func (s *Server) GetAutoCheckinConfig(c *gin.Context) {
	cfg, err := s.SiteService.GetAutoCheckinConfig(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, cfg)
}

func (s *Server) UpdateAutoCheckinConfig(c *gin.Context) {
	var cfg sitemanagement.AutoCheckinConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	updated, err := s.SiteService.UpdateAutoCheckinConfig(c.Request.Context(), cfg)
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, updated)
}

func (s *Server) GetAutoCheckinStatus(c *gin.Context) {
	response.Success(c, s.AutoCheckinService.GetStatus())
}

func (s *Server) RunAutoCheckinNow(c *gin.Context) {
	s.AutoCheckinService.TriggerRunNow()
	response.Success(c, nil)
}

func (s *Server) CheckInManagedSite(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	res, err := s.AutoCheckinService.CheckInSite(c.Request.Context(), uint(id))
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, res)
}

// RecordSiteOpened records when user clicked "Open Site" button.
// This helps track which sites have been visited today (resets at 05:00 Beijing time).
func (s *Server) RecordSiteOpened(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	if err := s.SiteService.RecordSiteOpened(c.Request.Context(), uint(id)); HandleServiceError(c, err) {
		return
	}
	response.Success(c, nil)
}

// RecordCheckinPageOpened records when user clicked "Open Check-in Page" button.
// This helps track which check-in pages have been visited today (resets at 05:00 Beijing time).
func (s *Server) RecordCheckinPageOpened(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	if err := s.SiteService.RecordCheckinPageOpened(c.Request.Context(), uint(id)); HandleServiceError(c, err) {
		return
	}
	response.Success(c, nil)
}

func (s *Server) ListManagedSiteCheckinLogs(c *gin.Context) {
	// Note: This handler directly accesses s.DB for checkin log queries.
	// Future refactor consideration: Move log queries to SiteService for consistency
	// with site list pagination (which delegates to SiteService.ListSitesPaginated).
	// Current implementation is correct and performant with idx_checkin_logs_site_created index.
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
	}

	// Parse pagination params with fallback to defaults on parse error.
	// Consistent with ListManagedSitesPaginated error handling pattern.
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		page = 1
	}

	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil {
			if n < 1 {
				n = 1
			}
			if n > 200 {
				n = 200
			}
			limit = n
		}
	}

	// Calculate offset for pagination
	offset := (page - 1) * limit

	// Get total count for pagination metadata
	var total int64
	if err := s.DB.WithContext(c.Request.Context()).
		Model(&sitemanagement.ManagedSiteCheckinLog{}).
		Where("site_id = ?", uint(id)).
		Count(&total).Error; HandleServiceError(c, err) {
		return
	}

	// Get paginated logs
	var logs []sitemanagement.ManagedSiteCheckinLog
	if err := s.DB.WithContext(c.Request.Context()).
		Where("site_id = ?", uint(id)).
		Order("created_at DESC, id DESC").
		Offset(offset).
		Limit(limit).
		Find(&logs).Error; HandleServiceError(c, err) {
		return
	}

	type logDTO struct {
		ID        uint      `json:"id"`
		SiteID    uint      `json:"site_id"`
		Status    string    `json:"status"`
		Message   string    `json:"message"`
		CreatedAt time.Time `json:"created_at"`
	}

	resp := make([]logDTO, 0, len(logs))
	for i := range logs {
		resp = append(resp, logDTO{
			ID:        logs[i].ID,
			SiteID:    logs[i].SiteID,
			Status:    logs[i].Status,
			Message:   logs[i].Message,
			CreatedAt: logs[i].CreatedAt,
		})
	}

	// Return paginated response
	totalPages := int((total + int64(limit) - 1) / int64(limit))
	response.Success(c, map[string]interface{}{
		"logs":        resp,
		"total":       total,
		"page":        page,
		"page_size":   limit,
		"total_pages": totalPages,
	})
}

// ExportManagedSites exports all managed sites
func (s *Server) ExportManagedSites(c *gin.Context) {
	// Determine export mode: plain or encrypted (default encrypted)
	exportMode := GetExportMode(c)
	plainMode := exportMode == "plain"

	// Check if auto-checkin config should be included
	includeConfig := c.DefaultQuery("include_config", "true") == "true"

	exportData, err := s.SiteService.ExportSites(c.Request.Context(), includeConfig, plainMode)
	if HandleServiceError(c, err) {
		return
	}

	// Set response headers
	suffix := "enc"
	if plainMode {
		suffix = "plain"
	}
	filename := fmt.Sprintf("sites-export_%s-%s.json", time.Now().Format("20060102_150405"), suffix)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "application/json; charset=utf-8")

	c.JSON(200, exportData)
}

// SiteImportRequest represents the import request structure
type SiteImportRequest struct {
	Version     string                          `json:"version"`
	AutoCheckin *sitemanagement.AutoCheckinConfig `json:"auto_checkin,omitempty"`
	Sites       []sitemanagement.SiteExportInfo `json:"sites"`
}

// ImportManagedSites imports managed sites from JSON data
func (s *Server) ImportManagedSites(c *gin.Context) {
	var importData SiteImportRequest
	if err := c.ShouldBindJSON(&importData); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	if len(importData.Sites) == 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrValidation, "No sites provided"))
		return
	}

	// Determine import mode from query, filename or content heuristic
	sample := make([]string, 0, 5)
	for i := 0; i < len(importData.Sites) && i < 5; i++ {
		if importData.Sites[i].AuthValue != "" {
			sample = append(sample, importData.Sites[i].AuthValue)
		}
	}
	importMode := GetImportMode(c, sample)
	plainMode := importMode == "plain"

	// Convert to service format
	serviceData := &sitemanagement.SiteExportData{
		Version:     importData.Version,
		AutoCheckin: importData.AutoCheckin,
		Sites:       importData.Sites,
	}

	imported, skipped, err := s.SiteService.ImportSites(c.Request.Context(), serviceData, plainMode)
	if HandleServiceError(c, err) {
		return
	}

	response.SuccessI18n(c, "success.sites_imported", map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
		"total":    len(importData.Sites),
	})
}
