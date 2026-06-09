package handler

import (
	"errors"
	"strconv"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
)

func respondProxyPoolServiceError(c *gin.Context, err error) {
	var apiErr *app_errors.APIError
	if errors.As(err, &apiErr) {
		response.Error(c, apiErr)
		return
	}
	response.Error(c, app_errors.NewAPIError(app_errors.ErrInternalServer, "unexpected proxy pool error"))
}

// ProxyPoolRequest defines the payload for creating or updating a proxy pool item.
type ProxyPoolRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ListProxyPool handles GET /api/proxy-pool.
func (s *Server) ListProxyPool(c *gin.Context) {
	items := make([]models.ProxyPoolItem, 0)
	pagination, err := response.PaginateFast(c, s.ProxyPoolService.ListQuery(c.Request.Context()), &items)
	if err != nil {
		response.Error(c, app_errors.ParseDBError(err))
		return
	}
	response.Success(c, pagination)
}

// CreateProxyPoolItem handles POST /api/proxy-pool.
func (s *Server) CreateProxyPoolItem(c *gin.Context) {
	var req ProxyPoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}
	item, err := s.ProxyPoolService.Create(c.Request.Context(), services.ProxyPoolInput{
		Name: req.Name,
		URL:  req.URL,
	})
	if err != nil {
		respondProxyPoolServiceError(c, err)
		return
	}
	response.Success(c, item)
}

// UpdateProxyPoolItem handles PUT /api/proxy-pool/:id.
func (s *Server) UpdateProxyPoolItem(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid proxy ID"))
		return
	}
	var req ProxyPoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}
	item, err := s.ProxyPoolService.Update(c.Request.Context(), uint(id), services.ProxyPoolInput{
		Name: req.Name,
		URL:  req.URL,
	})
	if err != nil {
		respondProxyPoolServiceError(c, err)
		return
	}
	response.Success(c, item)
}

// DeleteProxyPoolItem handles DELETE /api/proxy-pool/:id.
func (s *Server) DeleteProxyPoolItem(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid proxy ID"))
		return
	}
	if err := s.ProxyPoolService.Delete(c.Request.Context(), uint(id)); err != nil {
		respondProxyPoolServiceError(c, err)
		return
	}
	response.Success(c, nil)
}

// TestProxyPoolItem handles POST /api/proxy-pool/:id/test.
func (s *Server) TestProxyPoolItem(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrBadRequest, "invalid proxy ID"))
		return
	}
	result, err := s.ProxyPoolService.Test(c.Request.Context(), uint(id))
	if err != nil {
		respondProxyPoolServiceError(c, err)
		return
	}
	response.Success(c, result)
}
