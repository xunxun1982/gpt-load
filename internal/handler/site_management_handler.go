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

	CheckInEnabled     bool   `json:"checkin_enabled"`
	AutoCheckInEnabled bool   `json:"auto_checkin_enabled"`
	CustomCheckInURL   string `json:"custom_checkin_url"`

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

	CheckInEnabled     *bool   `json:"checkin_enabled"`
	AutoCheckInEnabled *bool   `json:"auto_checkin_enabled"`
	CustomCheckInURL   *string `json:"custom_checkin_url"`

	AuthType  *string `json:"auth_type"`
	AuthValue *string `json:"auth_value"`
}

func (s *Server) ListManagedSites(c *gin.Context) {
	sites, err := s.SiteService.ListSites(c.Request.Context())
	if HandleServiceError(c, err) {
		return
	}
	response.Success(c, sites)
}

func (s *Server) CreateManagedSite(c *gin.Context) {
	var req CreateManagedSiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	site, err := s.SiteService.CreateSite(c.Request.Context(), sitemanagement.CreateSiteParams{
		Name:               req.Name,
		Notes:              req.Notes,
		Description:        req.Description,
		Sort:               req.Sort,
		Enabled:            req.Enabled,
		BaseURL:            req.BaseURL,
		SiteType:           req.SiteType,
		UserID:             req.UserID,
		CheckInPageURL:     req.CheckInPageURL,
		CheckInEnabled:     req.CheckInEnabled,
		AutoCheckInEnabled: req.AutoCheckInEnabled,
		CustomCheckInURL:   req.CustomCheckInURL,
		AuthType:           req.AuthType,
		AuthValue:          req.AuthValue,
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
		Name:               req.Name,
		Notes:              req.Notes,
		Description:        req.Description,
		Sort:               req.Sort,
		Enabled:            req.Enabled,
		BaseURL:            req.BaseURL,
		SiteType:           req.SiteType,
		UserID:             req.UserID,
		CheckInPageURL:     req.CheckInPageURL,
		CheckInEnabled:     req.CheckInEnabled,
		AutoCheckInEnabled: req.AutoCheckInEnabled,
		CustomCheckInURL:   req.CustomCheckInURL,
		AuthType:           req.AuthType,
		AuthValue:          req.AuthValue,
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

func (s *Server) ListManagedSiteCheckinLogs(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, app_errors.ErrBadRequest)
		return
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

	var logs []sitemanagement.ManagedSiteCheckinLog
	if err := s.DB.WithContext(c.Request.Context()).
		Where("site_id = ?", uint(id)).
		Order("created_at DESC, id DESC").
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

	response.Success(c, resp)
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
