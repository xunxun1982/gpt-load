package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestExtractHubAccessKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(*gin.Context)
		expected string
	}{
		{
			name: "Bearer token in Authorization header",
			setup: func(c *gin.Context) {
				c.Request.Header.Set("Authorization", "Bearer test-key-123")
			},
			expected: "test-key-123",
		},
		{
			name: "X-Api-Key header",
			setup: func(c *gin.Context) {
				c.Request.Header.Set("X-Api-Key", "test-key-456")
			},
			expected: "test-key-456",
		},
		{
			name: "Query parameter",
			setup: func(c *gin.Context) {
				c.Request.URL.RawQuery = "key=test-key-789"
			},
			expected: "test-key-789",
		},
		{
			name: "No key provided",
			setup: func(c *gin.Context) {
				// No setup
			},
			expected: "",
		},
		{
			name: "Authorization header without Bearer prefix",
			setup: func(c *gin.Context) {
				c.Request.Header.Set("Authorization", "test-key-invalid")
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/test", nil)

			tt.setup(c)

			result := extractHubAccessKey(c)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractAdminAuthKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(*gin.Context)
		expected string
	}{
		{
			name: "Query parameter",
			setup: func(c *gin.Context) {
				c.Request.URL.RawQuery = "key=admin-key-123"
			},
			expected: "admin-key-123",
		},
		{
			name: "Bearer token in Authorization header",
			setup: func(c *gin.Context) {
				c.Request.Header.Set("Authorization", "Bearer admin-key-456")
			},
			expected: "admin-key-456",
		},
		{
			name: "X-Api-Key header",
			setup: func(c *gin.Context) {
				c.Request.Header.Set("X-Api-Key", "admin-key-789")
			},
			expected: "admin-key-789",
		},
		{
			name: "No key provided",
			setup: func(c *gin.Context) {
				// No setup
			},
			expected: "",
		},
		{
			name: "Query parameter takes precedence",
			setup: func(c *gin.Context) {
				c.Request.URL.RawQuery = "key=query-key"
				c.Request.Header.Set("Authorization", "Bearer header-key")
			},
			expected: "query-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/test", nil)

			tt.setup(c)

			result := extractAdminAuthKey(c)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestReturnHubAuthError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		status         int
		code           string
		message        string
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "Unauthorized error",
			status:         http.StatusUnauthorized,
			code:           "hub_key_invalid",
			message:        "Invalid access key",
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   "hub_key_invalid",
		},
		{
			name:           "Key disabled error",
			status:         http.StatusUnauthorized,
			code:           "hub_key_disabled",
			message:        "Access key is disabled",
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   "hub_key_disabled",
		},
		{
			name:           "Internal error",
			status:         http.StatusInternalServerError,
			code:           "hub_internal_error",
			message:        "Internal error",
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "hub_internal_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			returnHubAuthError(c, tt.status, tt.code, tt.message)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedCode)
			assert.Contains(t, w.Body.String(), tt.message)
			assert.Contains(t, w.Body.String(), "authentication_error")
		})
	}
}

func TestExtractHubAccessKey_Priority(t *testing.T) {
	t.Parallel()

	// Test that Bearer token takes precedence over X-Api-Key
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test", nil)
	c.Request.Header.Set("Authorization", "Bearer bearer-key")
	c.Request.Header.Set("X-Api-Key", "api-key")

	result := extractHubAccessKey(c)
	assert.Equal(t, "bearer-key", result)
}

func TestExtractHubAccessKey_XApiKeyFallback(t *testing.T) {
	t.Parallel()

	// Test that X-Api-Key is used when Bearer token is not present
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test", nil)
	c.Request.Header.Set("X-Api-Key", "api-key")

	result := extractHubAccessKey(c)
	assert.Equal(t, "api-key", result)
}

func TestExtractHubAccessKey_QueryFallback(t *testing.T) {
	t.Parallel()

	// Test that query parameter is used as last resort
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test?key=query-key", nil)

	result := extractHubAccessKey(c)
	assert.Equal(t, "query-key", result)
}

// Benchmark tests
func BenchmarkExtractHubAccessKey(b *testing.B) {
	b.ReportAllocs()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test", nil)
	c.Request.Header.Set("Authorization", "Bearer test-key-123")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = extractHubAccessKey(c)
	}
}

func BenchmarkExtractAdminAuthKey(b *testing.B) {
	b.ReportAllocs()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test?key=admin-key", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = extractAdminAuthKey(c)
	}
}

func BenchmarkReturnHubAuthError(b *testing.B) {
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		returnHubAuthError(c, http.StatusUnauthorized, "hub_key_invalid", "Invalid access key")
	}
}
