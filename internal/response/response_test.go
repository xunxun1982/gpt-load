package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/i18n"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
	// Initialize i18n for testing
	if err := i18n.Init(); err != nil {
		panic("failed to initialize i18n for tests: " + err.Error())
	}
}

func TestSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		data         any
		expectedCode int
	}{
		{
			name:         "with data",
			data:         map[string]string{"key": "value"},
			expectedCode: http.StatusOK,
		},
		{
			name:         "with nil data",
			data:         nil,
			expectedCode: http.StatusOK,
		},
		{
			name:         "with array data",
			data:         []string{"item1", "item2"},
			expectedCode: http.StatusOK,
		},
		{
			name:         "with string data",
			data:         "success message",
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			Success(c, tt.data)

			assert.Equal(t, tt.expectedCode, w.Code)

			var response SuccessResponse
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, 0, response.Code)
			assert.NotEmpty(t, response.Message)
			if tt.data != nil {
				assert.NotNil(t, response.Data)
			}
		})
	}
}

func TestError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		apiErr         *app_errors.APIError
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "bad request error",
			apiErr:         app_errors.ErrBadRequest,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "BAD_REQUEST",
		},
		{
			name:           "unauthorized error",
			apiErr:         app_errors.ErrUnauthorized,
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   "UNAUTHORIZED",
		},
		{
			name:           "not found error",
			apiErr:         app_errors.ErrResourceNotFound,
			expectedStatus: http.StatusNotFound,
			expectedCode:   "NOT_FOUND",
		},
		{
			name:           "internal server error",
			apiErr:         app_errors.ErrInternalServer,
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "INTERNAL_SERVER_ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			Error(c, tt.apiErr)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response ErrorResponse
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedCode, response.Code)
			assert.NotEmpty(t, response.Message)
		})
	}
}

func TestSuccessI18n(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		msgID        string
		data         any
		templateData []map[string]any
		expectedCode int
	}{
		{
			name:         "simple message",
			msgID:        "common.success",
			data:         map[string]string{"result": "ok"},
			expectedCode: http.StatusOK,
		},
		{
			name:         "with template data",
			msgID:        "common.success",
			data:         nil,
			templateData: []map[string]any{{"count": 5}},
			expectedCode: http.StatusOK,
		},
		{
			name:         "with nil data",
			msgID:        "common.success",
			data:         nil,
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			SuccessI18n(c, tt.msgID, tt.data, tt.templateData...)

			assert.Equal(t, tt.expectedCode, w.Code)

			var response SuccessResponse
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, 0, response.Code)
			assert.NotEmpty(t, response.Message)
		})
	}
}

func TestErrorI18n(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		httpStatus     int
		code           string
		msgID          string
		templateData   []map[string]any
		expectedStatus int
	}{
		{
			name:           "bad request",
			httpStatus:     http.StatusBadRequest,
			code:           "BAD_REQUEST",
			msgID:          "validation.invalid_input",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "with template data",
			httpStatus:     http.StatusNotFound,
			code:           "NOT_FOUND",
			msgID:          "common.not_found",
			templateData:   []map[string]any{{"resource": "user"}},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "internal error",
			httpStatus:     http.StatusInternalServerError,
			code:           "INTERNAL_ERROR",
			msgID:          "common.internal_error",
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			ErrorI18n(c, tt.httpStatus, tt.code, tt.msgID, tt.templateData...)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response ErrorResponse
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, tt.code, response.Code)
			assert.NotEmpty(t, response.Message)
		})
	}
}

func TestErrorI18nFromAPIError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		apiErr         *app_errors.APIError
		msgID          string
		templateData   []map[string]any
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "bad request with custom message",
			apiErr:         app_errors.ErrBadRequest,
			msgID:          "validation.invalid_input",
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "BAD_REQUEST",
		},
		{
			name:           "not found with template data",
			apiErr:         app_errors.ErrResourceNotFound,
			msgID:          "common.not_found",
			templateData:   []map[string]any{{"resource": "group"}},
			expectedStatus: http.StatusNotFound,
			expectedCode:   "NOT_FOUND",
		},
		{
			name:           "unauthorized",
			apiErr:         app_errors.ErrUnauthorized,
			msgID:          "auth.unauthorized",
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   "UNAUTHORIZED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			ErrorI18nFromAPIError(c, tt.apiErr, tt.msgID, tt.templateData...)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response ErrorResponse
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedCode, response.Code)
			assert.NotEmpty(t, response.Message)
		})
	}
}

func TestSuccessResponse_Structure(t *testing.T) {
	t.Parallel()

	response := SuccessResponse{
		Code:    0,
		Message: "Success",
		Data:    map[string]string{"key": "value"},
	}

	assert.Equal(t, 0, response.Code)
	assert.Equal(t, "Success", response.Message)
	assert.NotNil(t, response.Data)
}

func TestErrorResponse_Structure(t *testing.T) {
	t.Parallel()

	response := ErrorResponse{
		Code:    "ERROR_CODE",
		Message: "Error message",
	}

	assert.Equal(t, "ERROR_CODE", response.Code)
	assert.Equal(t, "Error message", response.Message)
}

// Benchmark tests
func BenchmarkSuccess(b *testing.B) {
	b.ReportAllocs()

	data := map[string]string{"key": "value"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		Success(c, data)
	}
}

func BenchmarkError(b *testing.B) {
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		Error(c, app_errors.ErrBadRequest)
	}
}

func BenchmarkSuccessI18n(b *testing.B) {
	b.ReportAllocs()

	data := map[string]string{"key": "value"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		SuccessI18n(c, "common.success", data)
	}
}

func BenchmarkErrorI18n(b *testing.B) {
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		ErrorI18n(c, http.StatusBadRequest, "BAD_REQUEST", "validation.invalid_input")
	}
}
