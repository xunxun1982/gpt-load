package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/i18n"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	// Initialize i18n for tests
	if err := i18n.Init(); err != nil {
		panic("failed to initialize i18n for tests: " + err.Error())
	}
}

func TestHandleServiceError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		err            error
		expectHandled  bool
		expectedStatus int
		checkResponse  func(*testing.T, map[string]any)
	}{
		{
			name:          "nil_error",
			err:           nil,
			expectHandled: false,
		},
		{
			name: "i18n_error_without_template",
			err: &services.I18nError{
				APIError:  app_errors.ErrBadRequest,
				MessageID: "test.error",
			},
			expectHandled:  true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "i18n_error_with_template",
			err: &services.I18nError{
				APIError:  app_errors.ErrBadRequest,
				MessageID: "test.error",
				Template:  map[string]any{"field": "name"},
			},
			expectHandled:  true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "api_error",
			err:            app_errors.ErrResourceNotFound,
			expectHandled:  true,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "generic_error",
			err:            errors.New("unexpected error"),
			expectHandled:  true,
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/test", nil)

			handled := HandleServiceError(c, tt.err)

			assert.Equal(t, tt.expectHandled, handled)

			if tt.expectHandled {
				assert.Equal(t, tt.expectedStatus, w.Code)

				var resp map[string]any
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				assert.NoError(t, err)

				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}
		})
	}
}
