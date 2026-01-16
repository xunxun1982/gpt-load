package handler

import (
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/channel"
	"gpt-load/internal/response"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
)

// CommonHandler handles common, non-grouped requests.
type CommonHandler struct{}

// NewCommonHandler creates a new CommonHandler.
func NewCommonHandler() *CommonHandler {
	return &CommonHandler{}
}

// GetChannelTypes returns a list of available channel types.
func (h *CommonHandler) GetChannelTypes(c *gin.Context) {
	channelTypes := channel.GetChannels()
	response.Success(c, channelTypes)
}

// ApplyBrandPrefixRequest defines the request payload for applying brand prefixes.
type ApplyBrandPrefixRequest struct {
	Models       []string `json:"models" binding:"required"`
	UseLowercase bool     `json:"use_lowercase"`
}

// ApplyBrandPrefixResponse defines the response for brand prefix application.
type ApplyBrandPrefixResponse struct {
	// Results maps original model name to prefixed model name
	Results map[string]string `json:"results"`
}

// ApplyBrandPrefix applies brand/vendor prefixes to model names.
// POST /api/models/apply-brand-prefix
func (h *CommonHandler) ApplyBrandPrefix(c *gin.Context) {
	var req ApplyBrandPrefixRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, app_errors.NewAPIError(app_errors.ErrInvalidJSON, err.Error()))
		return
	}

	results := utils.ApplyBrandPrefixBatch(req.Models, req.UseLowercase)
	response.Success(c, ApplyBrandPrefixResponse{Results: results})
}
