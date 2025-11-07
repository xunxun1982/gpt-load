package handler

import (
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/response"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// HandleServiceError handles service errors uniformly across all handlers.
// Returns true if an error was handled (response already sent to client).
func HandleServiceError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}

	// Handle I18nError from services
	if svcErr, ok := err.(*services.I18nError); ok {
		if svcErr.Template != nil {
			response.ErrorI18nFromAPIError(c, svcErr.APIError, svcErr.MessageID, svcErr.Template)
		} else {
			response.ErrorI18nFromAPIError(c, svcErr.APIError, svcErr.MessageID)
		}
		return true
	}

	// Handle APIError
	if apiErr, ok := err.(*app_errors.APIError); ok {
		response.Error(c, apiErr)
		return true
	}

	// Handle database errors
	if dbErr := app_errors.ParseDBError(err); dbErr != nil {
		response.Error(c, dbErr)
		return true
	}

	// Unexpected error - log and return internal server error
	logrus.WithContext(c.Request.Context()).WithError(err).Error("unexpected service error")
	response.Error(c, app_errors.ErrInternalServer)
	return true
}

