package proxy

import (
	"errors"
	"net/http"
	"strings"

	"gpt-load/internal/channel"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// shouldInterceptModelList checks if this is a model list request that should be intercepted
func shouldInterceptModelList(path string, method string) bool {
	if method != "GET" {
		return false
	}

	// Check various model list endpoints
	return strings.HasSuffix(path, "/v1/models") ||
		strings.HasSuffix(path, "/v1beta/models") ||
		strings.Contains(path, "/v1beta/openai/v1/models")
}

// handleModelListResponse processes the model list response and applies filtering based on redirect rules
func (ps *ProxyServer) handleModelListResponse(c *gin.Context, resp *http.Response, group *models.Group, channelHandler channel.ChannelProxy) {
	// Read the upstream response body with size limit to prevent memory exhaustion
	const maxModelListBodySize = 10 * 1024 * 1024 // 10MB limit for model list responses
	bodyBytes, err := readAllWithLimit(resp.Body, maxModelListBodySize)
	if err != nil {
		// Use errors.Is() for sentinel error comparison to handle wrapped errors properly
		if errors.Is(err, ErrBodyTooLarge) {
			logrus.WithField("limit_mb", maxModelListBodySize/(1024*1024)).
				Warn("Model list response body too large")
			c.JSON(http.StatusBadGateway, gin.H{"error": "Model list response too large"})
			return
		}
		logrus.WithError(err).Error("Failed to read model list response body")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read response"})
		return
	}

	// Decompress response data based on Content-Encoding with size limit
	contentEncoding := resp.Header.Get("Content-Encoding")
	decompressed, err := utils.DecompressResponseWithLimit(contentEncoding, bodyBytes, maxModelListBodySize)
	if err != nil {
		// Use errors.Is() for sentinel error comparison to handle wrapped errors properly
		if errors.Is(err, utils.ErrDecompressedTooLarge) {
			logrus.WithField("limit_mb", maxModelListBodySize/(1024*1024)).
				Warn("Decompressed model list response too large")
			c.JSON(http.StatusBadGateway, gin.H{"error": "Decompressed response too large"})
			return
		}
		logrus.WithError(err).Warn("Decompression failed, using original data")
		decompressed = bodyBytes
	}

	// Transform model list (returns map[string]any directly, no marshaling)
	response, err := channelHandler.TransformModelList(c.Request, decompressed, group)
	if err != nil {
		logrus.WithError(err).Error("Failed to transform model list")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process response"})
		return
	}

	c.JSON(http.StatusOK, response)
}
