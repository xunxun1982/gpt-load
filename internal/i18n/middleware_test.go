package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMiddleware tests the i18n middleware
func TestMiddleware(t *testing.T) {
	// Initialize i18n
	err := Init()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		acceptLang   string
		expectedLang string
	}{
		{
			name:         "Chinese",
			acceptLang:   "zh-CN",
			expectedLang: "zh-CN",
		},
		{
			name:         "English",
			acceptLang:   "en-US",
			expectedLang: "en-US",
		},
		{
			name:         "Japanese",
			acceptLang:   "ja-JP",
			expectedLang: "ja-JP",
		},
		{
			name:         "empty defaults to Chinese",
			acceptLang:   "",
			expectedLang: "zh-CN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/", nil)
			c.Request.Header.Set("Accept-Language", tt.acceptLang)

			// Apply middleware
			middleware := Middleware()
			middleware(c)

			// Check localizer is set
			localizer := GetLocalizerFromContext(c)
			assert.NotNil(t, localizer)

			// Check language is set
			lang := GetLangFromContext(c)
			assert.Equal(t, tt.expectedLang, lang)
		})
	}
}

// TestGetLocalizerFromContext tests getting localizer from context
func TestGetLocalizerFromContext(t *testing.T) {
	// Initialize i18n
	err := Init()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	t.Run("with localizer in context", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		localizer := GetLocalizer("en-US")
		c.Set(LocalizerKey, localizer)

		result := GetLocalizerFromContext(c)
		assert.NotNil(t, result)
	})

	t.Run("without localizer in context", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		result := GetLocalizerFromContext(c)
		assert.NotNil(t, result)
	})

	t.Run("with invalid localizer type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(LocalizerKey, "invalid")

		result := GetLocalizerFromContext(c)
		assert.NotNil(t, result)
	})
}

// TestGetLangFromContext tests getting language from context
func TestGetLangFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("with language in context", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(LangKey, "en-US")

		result := GetLangFromContext(c)
		assert.Equal(t, "en-US", result)
	})

	t.Run("without language in context", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		result := GetLangFromContext(c)
		assert.Equal(t, "zh-CN", result)
	})

	t.Run("with invalid language type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(LangKey, 123)

		result := GetLangFromContext(c)
		assert.Equal(t, "zh-CN", result)
	})
}

// TestSuccess tests success response
func TestSuccess(t *testing.T) {
	// Initialize i18n
	err := Init()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	localizer := GetLocalizer("en-US")
	c.Set(LocalizerKey, localizer)
	c.Set(LangKey, "en-US")

	Success(c, "common.success", map[string]any{"test": "data"})

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "success")
	assert.Contains(t, w.Body.String(), "true")
}

// TestSuccessWithData tests success response with template data
func TestSuccessWithData(t *testing.T) {
	// Initialize i18n
	err := Init()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	localizer := GetLocalizer("en-US")
	c.Set(LocalizerKey, localizer)
	c.Set(LangKey, "en-US")

	SuccessWithData(c, "common.success", map[string]any{"name": "test"}, map[string]any{"test": "data"})

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "success")
	assert.Contains(t, w.Body.String(), "true")
}

// TestError tests error response
func TestError(t *testing.T) {
	// Initialize i18n
	err := Init()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	localizer := GetLocalizer("en-US")
	c.Set(LocalizerKey, localizer)
	c.Set(LangKey, "en-US")

	Error(c, http.StatusBadRequest, "common.error")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "success")
	assert.Contains(t, w.Body.String(), "false")
}

// TestErrorWithData tests error response with template data
func TestErrorWithData(t *testing.T) {
	// Initialize i18n
	err := Init()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	localizer := GetLocalizer("en-US")
	c.Set(LocalizerKey, localizer)
	c.Set(LangKey, "en-US")

	ErrorWithData(c, http.StatusBadRequest, "common.error", map[string]any{"name": "test"})

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "success")
	assert.Contains(t, w.Body.String(), "false")
}

// TestMessage tests getting a message
func TestMessage(t *testing.T) {
	// Initialize i18n
	err := Init()
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	localizer := GetLocalizer("en-US")
	c.Set(LocalizerKey, localizer)

	result := Message(c, "common.success")
	assert.NotEmpty(t, result)
}

// BenchmarkMiddleware benchmarks the middleware
func BenchmarkMiddleware(b *testing.B) {
	Init()
	gin.SetMode(gin.TestMode)
	middleware := Middleware()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Request.Header.Set("Accept-Language", "zh-CN")

		middleware(c)
	}
}

// BenchmarkGetLocalizerFromContext benchmarks getting localizer from context
func BenchmarkGetLocalizerFromContext(b *testing.B) {
	Init()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	localizer := GetLocalizer("en-US")
	c.Set(LocalizerKey, localizer)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetLocalizerFromContext(c)
	}
}
