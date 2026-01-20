package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

// TestCommonHandler_GetChannelTypes tests getting channel types
func TestCommonHandler_GetChannelTypes(t *testing.T) {
	handler := NewCommonHandler()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/channel-types", nil)

	handler.GetChannelTypes(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	// Check response structure (code, message, data)
	assert.Contains(t, response, "code")
	assert.Contains(t, response, "message")
	assert.Contains(t, response, "data")
}

// TestCommonHandler_ApplyBrandPrefix tests applying brand prefix to models
func TestCommonHandler_ApplyBrandPrefix(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    ApplyBrandPrefixRequest
		expectedStatus int
		expectError    bool
	}{
		{
			name: "valid request with models",
			requestBody: ApplyBrandPrefixRequest{
				Models: []string{"gpt-4", "gpt-3.5-turbo", "claude-3"},
			},
			expectedStatus: http.StatusOK,
			expectError:    false,
		},
		{
			name: "empty models list",
			requestBody: ApplyBrandPrefixRequest{
				Models: []string{},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name: "single model",
			requestBody: ApplyBrandPrefixRequest{
				Models: []string{"gpt-4"},
			},
			expectedStatus: http.StatusOK,
			expectError:    false,
		},
		{
			name: "with lowercase option",
			requestBody: ApplyBrandPrefixRequest{
				Models:       []string{"gpt-4", "claude-3"},
				UseLowercase: true,
			},
			expectedStatus: http.StatusOK,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewCommonHandler()

			body, _ := json.Marshal(tt.requestBody)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/models/apply-brand-prefix", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			handler.ApplyBrandPrefix(c)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)

			if !tt.expectError {
				assert.Contains(t, response, "code")
				assert.Contains(t, response, "data")
			} else {
				// Error responses have code and message but no data
				assert.Contains(t, response, "code")
				assert.Contains(t, response, "message")
			}
		})
	}
}

// TestCommonHandler_ApplyBrandPrefix_InvalidJSON tests invalid JSON handling
func TestCommonHandler_ApplyBrandPrefix_InvalidJSON(t *testing.T) {
	handler := NewCommonHandler()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/models/apply-brand-prefix", bytes.NewBufferString("invalid json"))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.ApplyBrandPrefix(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// BenchmarkCommonHandler_GetChannelTypes benchmarks getting channel types
func BenchmarkCommonHandler_GetChannelTypes(b *testing.B) {
	handler := NewCommonHandler()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/channel-types", nil)

		handler.GetChannelTypes(c)
	}
}

// BenchmarkCommonHandler_ApplyBrandPrefix benchmarks applying brand prefix
func BenchmarkCommonHandler_ApplyBrandPrefix(b *testing.B) {
	handler := NewCommonHandler()
	requestBody := ApplyBrandPrefixRequest{
		Models: []string{"gpt-4", "gpt-3.5-turbo", "claude-3", "gemini-pro"},
	}
	body, _ := json.Marshal(requestBody)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/models/apply-brand-prefix", bytes.NewBuffer(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.ApplyBrandPrefix(c)
	}
}
