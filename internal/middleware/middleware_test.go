package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// TestLogger tests logging middleware
func TestLogger(t *testing.T) {
	config := types.LogConfig{Level: "info"}
	middleware := Logger(config)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)

	middleware(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestCORS tests CORS middleware
func TestCORS(t *testing.T) {
	tests := []struct {
		name             string
		config           types.CORSConfig
		origin           string
		method           string
		expectedStatus   int
		expectHeaders    bool
	}{
		{
			name: "CORS disabled",
			config: types.CORSConfig{
				Enabled: false,
			},
			origin:         "http://localhost:3000",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
			expectHeaders:  false,
		},
		{
			name: "CORS enabled with wildcard",
			config: types.CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
				AllowedMethods: []string{"GET", "POST"},
				AllowedHeaders: []string{"*"},
			},
			origin:         "http://localhost:3000",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
			expectHeaders:  true,
		},
		{
			name: "CORS preflight request",
			config: types.CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"http://localhost:3000"},
				AllowedMethods: []string{"GET", "POST"},
				AllowedHeaders: []string{"Content-Type"},
			},
			origin:         "http://localhost:3000",
			method:         http.MethodOptions,
			expectedStatus: http.StatusNoContent,
			expectHeaders:  true,
		},
		{
			name: "CORS with specific origin",
			config: types.CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"http://localhost:3000"},
				AllowedMethods: []string{"GET"},
				AllowedHeaders: []string{"*"},
			},
			origin:         "http://localhost:3000",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
			expectHeaders:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware := CORS(tt.config)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tt.method, "/test", nil)
			c.Request.Header.Set("Origin", tt.origin)

			middleware(c)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectHeaders && tt.config.Enabled {
				assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

// TestAuth tests authentication middleware
func TestAuth(t *testing.T) {
	authConfig := types.AuthConfig{
		Key: "test-auth-key",
	}

	tests := []struct {
		name           string
		authKey        string
		expectedStatus int
		shouldAbort    bool
	}{
		{
			name:           "valid auth key in query",
			authKey:        "test-auth-key",
			expectedStatus: http.StatusOK,
			shouldAbort:    false,
		},
		{
			name:           "invalid auth key",
			authKey:        "wrong-key",
			expectedStatus: http.StatusUnauthorized,
			shouldAbort:    true,
		},
		{
			name:           "missing auth key",
			authKey:        "",
			expectedStatus: http.StatusUnauthorized,
			shouldAbort:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware := Auth(authConfig)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			if tt.authKey != "" {
				c.Request = httptest.NewRequest(http.MethodGet, "/test?key="+tt.authKey, nil)
			} else {
				c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
			}

			middleware(c)

			if tt.shouldAbort {
				assert.True(t, c.IsAborted())
			} else {
				assert.False(t, c.IsAborted())
			}
		})
	}
}

// TestRecovery tests recovery middleware
func TestRecovery(t *testing.T) {
	middleware := Recovery()

	w := httptest.NewRecorder()
	c, router := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)

	// Add middleware and panic handler
	router.Use(middleware)
	router.GET("/test", func(c *gin.Context) {
		panic("test panic")
	})

	// Should not panic
	assert.NotPanics(t, func() {
		router.ServeHTTP(w, c.Request)
	})
}

// TestRateLimiter tests rate limiting middleware
func TestRateLimiter(t *testing.T) {
	config := types.PerformanceConfig{
		MaxConcurrentRequests: 2,
	}

	middleware := RateLimiter(config)

	// First two requests should succeed
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)

		middleware(c)
		assert.False(t, c.IsAborted())
	}
}

// TestIsMonitoringEndpoint tests monitoring endpoint detection
func TestIsMonitoringEndpoint(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/health", true},
		{"/api/test", false},
		{"/", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isMonitoringEndpoint(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestExtractAuthKey tests auth key extraction
func TestExtractAuthKey(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*gin.Context)
		expectedKey string
	}{
		{
			name: "query parameter",
			setupFunc: func(c *gin.Context) {
				c.Request = httptest.NewRequest(http.MethodGet, "/test?key=test-key", nil)
			},
			expectedKey: "test-key",
		},
		{
			name: "bearer token",
			setupFunc: func(c *gin.Context) {
				c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
				c.Request.Header.Set("Authorization", "Bearer test-key")
			},
			expectedKey: "test-key",
		},
		{
			name: "X-Api-Key header",
			setupFunc: func(c *gin.Context) {
				c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
				c.Request.Header.Set("X-Api-Key", "test-key")
			},
			expectedKey: "test-key",
		},
		{
			name: "X-Goog-Api-Key header",
			setupFunc: func(c *gin.Context) {
				c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
				c.Request.Header.Set("X-Goog-Api-Key", "test-key")
			},
			expectedKey: "test-key",
		},
		{
			name: "no key",
			setupFunc: func(c *gin.Context) {
				c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
			},
			expectedKey: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			tt.setupFunc(c)

			key := extractAuthKey(c)
			assert.Equal(t, tt.expectedKey, key)
		})
	}
}

// TestIsStaticResource tests static resource detection
func TestIsStaticResource(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/assets/style.css", true},
		{"/assets/script.js", true},
		{"/favicon.ico", true},
		{"/image.png", true},
		{"/api/test", false},
		{"/", false},
		{"/test.html", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isStaticResource(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestStaticCache tests static cache middleware
func TestStaticCache(t *testing.T) {
	middleware := StaticCache()

	tests := []struct {
		path          string
		expectHeaders bool
	}{
		{"/assets/style.css", true},
		{"/api/test", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tt.path, nil)

			middleware(c)

			if tt.expectHeaders {
				assert.NotEmpty(t, w.Header().Get("Cache-Control"))
			} else {
				assert.Empty(t, w.Header().Get("Cache-Control"))
			}
		})
	}
}

// TestSecurityHeaders tests security headers middleware
func TestSecurityHeaders(t *testing.T) {
	middleware := SecurityHeaders()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)

	middleware(c)

	assert.NotEmpty(t, w.Header().Get("X-Content-Type-Options"))
	assert.NotEmpty(t, w.Header().Get("Referrer-Policy"))
	assert.NotEmpty(t, w.Header().Get("Permissions-Policy"))
	assert.NotEmpty(t, w.Header().Get("X-Frame-Options"))
}

// TestRequestBodySizeLimit tests request body size limit middleware
func TestRequestBodySizeLimit(t *testing.T) {
	middleware := RequestBodySizeLimit(100) // 100 bytes limit

	tests := []struct {
		name           string
		bodySize       int
		expectedStatus int
	}{
		{
			name:           "small body",
			bodySize:       50,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "large body",
			bodySize:       200,
			expectedStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			body := strings.Repeat("a", tt.bodySize)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
			c.Request.Header.Set("Content-Length", strconv.Itoa(tt.bodySize))

			middleware(c)

			if tt.expectedStatus == http.StatusRequestEntityTooLarge {
				assert.True(t, c.IsAborted())
			}
		})
	}
}

// BenchmarkLogger benchmarks logging middleware
func BenchmarkLogger(b *testing.B) {
	config := types.LogConfig{Level: "info"}
	middleware := Logger(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
		middleware(c)
	}
}

// BenchmarkCORS benchmarks CORS middleware
func BenchmarkCORS(b *testing.B) {
	config := types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"*"},
	}
	middleware := CORS(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
		c.Request.Header.Set("Origin", "http://localhost:3000")
		middleware(c)
	}
}

// BenchmarkExtractAuthKey benchmarks auth key extraction
func BenchmarkExtractAuthKey(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/test?key=test-key", nil)
		_ = extractAuthKey(c)
	}
}



// TestLoggerWithKeyInfo tests logger with key information
func TestLoggerWithKeyInfo(t *testing.T) {
	router := gin.New()
	router.Use(Logger(types.LogConfig{Level: "info", Format: "text"}))
	router.GET("/test", func(c *gin.Context) {
		c.Set("keyIndex", 1)
		c.Set("keyPreview", "sk-test***")
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestLoggerWithRetryInfo tests logger with retry information
func TestLoggerWithRetryInfo(t *testing.T) {
	router := gin.New()
	router.Use(Logger(types.LogConfig{Level: "info", Format: "text"}))
	router.GET("/test", func(c *gin.Context) {
		c.Set("retryCount", 2)
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestLoggerWithDifferentStatusCodes tests logger with different status codes
func TestLoggerWithDifferentStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"success 200", 200},
		{"client error 400", 400},
		{"not found 404", 404},
		{"server error 500", 500},
		{"bad gateway 502", 502},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(Logger(types.LogConfig{Level: "info", Format: "text"}))
			router.GET("/test", func(c *gin.Context) {
				c.String(tt.statusCode, "Response")
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/test", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.statusCode, w.Code)
		})
	}
}

// TestLoggerMonitoringEndpoints tests logger filtering monitoring endpoints
func TestLoggerMonitoringEndpoints(t *testing.T) {
	router := gin.New()
	router.Use(Logger(types.LogConfig{Level: "info", Format: "text"}))
	router.GET("/health", func(c *gin.Context) {
		c.String(200, "OK")
	})
	router.GET("/health/error", func(c *gin.Context) {
		c.String(500, "Error")
	})

	// Success should not log
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)

	// Error should log
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/health/error", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, 500, w.Code)
}

// TestCORSWithCredentials tests CORS with credentials
func TestCORSWithCredentials(t *testing.T) {
	router := gin.New()
	router.Use(CORS(types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"http://localhost:3000"},
		AllowCredentials: true,
	}))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "http://localhost:3000", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, w.Header().Get("Vary"), "Origin")
}

// TestCORSPreflightRequest tests CORS preflight OPTIONS request
func TestCORSPreflightRequest(t *testing.T) {
	router := gin.New()
	router.Use(CORS(types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"http://localhost:3000"},
		AllowedMethods: []string{"GET", "POST", "PUT"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
	}))
	router.OPTIONS("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	router.ServeHTTP(w, req)

	assert.Equal(t, 204, w.Code)
	assert.Equal(t, "http://localhost:3000", w.Header().Get("Access-Control-Allow-Origin"))
	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Methods"))
	assert.NotEmpty(t, w.Header().Get("Access-Control-Max-Age"))
}

// TestCORSDisallowedOrigin tests CORS with disallowed origin
func TestCORSDisallowedOrigin(t *testing.T) {
	router := gin.New()
	router.Use(CORS(types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"http://localhost:3000"},
	}))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://evil.com")
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

// TestCORSWildcardWithCredentials tests CORS wildcard with credentials warning
func TestCORSWildcardWithCredentials(t *testing.T) {
	router := gin.New()
	router.Use(CORS(types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"*"},
		AllowCredentials: true,
	}))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestAuthMonitoringEndpoint tests auth bypass for monitoring endpoints
func TestAuthMonitoringEndpoint(t *testing.T) {
	router := gin.New()
	router.Use(Auth(types.AuthConfig{Key: "test-key"}))
	router.GET("/health", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestAuthEmptyKey tests auth with empty key
func TestAuthEmptyKey(t *testing.T) {
	router := gin.New()
	router.Use(Auth(types.AuthConfig{Key: "test-key"}))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 401, w.Code)
}

// TestExtractAuthKeyFromDifferentSources tests extracting auth key from various sources
func TestExtractAuthKeyFromDifferentSources(t *testing.T) {
	tests := []struct {
		name     string
		setupReq func(*http.Request)
		expected string
	}{
		{
			name: "from query parameter",
			setupReq: func(req *http.Request) {
				q := req.URL.Query()
				q.Add("key", "query-key")
				req.URL.RawQuery = q.Encode()
			},
			expected: "query-key",
		},
		{
			name: "from Authorization Bearer",
			setupReq: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer bearer-key")
			},
			expected: "bearer-key",
		},
		{
			name: "from X-Api-Key",
			setupReq: func(req *http.Request) {
				req.Header.Set("X-Api-Key", "api-key")
			},
			expected: "api-key",
		},
		{
			name: "from X-Goog-Api-Key",
			setupReq: func(req *http.Request) {
				req.Header.Set("X-Goog-Api-Key", "goog-key")
			},
			expected: "goog-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			var extractedKey string
			router.Use(func(c *gin.Context) {
				extractedKey = extractAuthKey(c)
				c.Next()
			})
			router.GET("/test", func(c *gin.Context) {
				c.String(200, "OK")
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/test", nil)
			tt.setupReq(req)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expected, extractedKey)
		})
	}
}

// TestIsStaticResourceVariants tests static resource detection
func TestIsStaticResourceVariants(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/assets/main.js", true},
		{"/assets/style.css", true},
		{"/favicon.ico", true},
		{"/logo.png", true},
		{"/api/test", false},
		{"/", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isStaticResource(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestRequestBodySizeLimitExactly tests body size limit at exact boundary
func TestRequestBodySizeLimitExactly(t *testing.T) {
	router := gin.New()
	router.Use(RequestBodySizeLimit(10))
	router.POST("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	// Exactly 10 bytes
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/test", strings.NewReader("1234567890"))
	router.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)

	// 11 bytes - should fail
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/test", strings.NewReader("12345678901"))
	router.ServeHTTP(w, req)
	assert.Equal(t, 413, w.Code)
}

// BenchmarkLoggerWithKeyInfo benchmarks logger with key info
func BenchmarkLoggerWithKeyInfo(b *testing.B) {
	router := gin.New()
	router.Use(Logger(types.LogConfig{Level: "info", Format: "text"}))
	router.GET("/test", func(c *gin.Context) {
		c.Set("keyIndex", 1)
		c.Set("keyPreview", "sk-test***")
		c.Set("retryCount", 2)
		c.String(200, "OK")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w, req)
	}
}

// BenchmarkCORSPreflight benchmarks CORS preflight handling
func BenchmarkCORSPreflight(b *testing.B) {
	router := gin.New()
	router.Use(CORS(types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"http://localhost:3000"},
	}))
	router.OPTIONS("/test", func(c *gin.Context) {})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("OPTIONS", "/test", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		router.ServeHTTP(w, req)
	}
}



// TestErrorHandler tests error handling middleware
func TestErrorHandler(t *testing.T) {
	router := gin.New()
	router.Use(ErrorHandler())
	router.GET("/test", func(c *gin.Context) {
		c.Error(errors.New("test error"))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 500, w.Code)
}

// TestErrorHandlerWithAPIError tests error handler with API error
func TestErrorHandlerWithAPIError(t *testing.T) {
	router := gin.New()
	router.Use(ErrorHandler())
	router.GET("/test", func(c *gin.Context) {
		c.Error(app_errors.ErrUnauthorized)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 401, w.Code)
}

// TestErrorHandlerNoErrors tests error handler with no errors
func TestErrorHandlerNoErrors(t *testing.T) {
	router := gin.New()
	router.Use(ErrorHandler())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestStaticCacheExpires tests static cache expires header
func TestStaticCacheExpires(t *testing.T) {
	router := gin.New()
	router.Use(StaticCache())
	router.GET("/assets/logo.png", func(c *gin.Context) {
		c.String(200, "image")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/assets/logo.png", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.NotEmpty(t, w.Header().Get("Cache-Control"))
	assert.NotEmpty(t, w.Header().Get("Expires"))
}

// TestIsStaticResourceAllExtensions tests all static resource extensions
func TestIsStaticResourceAllExtensions(t *testing.T) {
	extensions := []string{
		".js", ".css", ".ico", ".png", ".jpg", ".jpeg",
		".gif", ".svg", ".woff", ".woff2", ".ttf", ".eot",
		".webp", ".avif", ".map",
	}

	for _, ext := range extensions {
		t.Run(ext, func(t *testing.T) {
			path := "/file" + ext
			result := isStaticResource(path)
			assert.True(t, result)
		})
	}
}

// TestSecurityHeadersAllHeaders tests all security headers
func TestSecurityHeadersAllHeaders(t *testing.T) {
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "strict-origin-when-cross-origin", w.Header().Get("Referrer-Policy"))
	assert.NotEmpty(t, w.Header().Get("Permissions-Policy"))
	assert.Equal(t, "SAMEORIGIN", w.Header().Get("X-Frame-Options"))
}

// TestRequestBodySizeLimitDefault tests default body size limit
func TestRequestBodySizeLimitDefault(t *testing.T) {
	router := gin.New()
	router.Use(RequestBodySizeLimit(0)) // 0 means use default
	router.POST("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/test", strings.NewReader("test"))
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestRequestBodySizeLimitContentLength tests Content-Length check
func TestRequestBodySizeLimitContentLength(t *testing.T) {
	router := gin.New()
	router.Use(RequestBodySizeLimit(10))
	router.POST("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/test", strings.NewReader("12345678901"))
	req.Header.Set("Content-Length", "11")
	router.ServeHTTP(w, req)

	assert.Equal(t, 413, w.Code)
}

// TestRateLimiterConcurrent tests rate limiter with concurrent requests
func TestRateLimiterConcurrent(t *testing.T) {
	router := gin.New()
	router.Use(RateLimiter(types.PerformanceConfig{MaxConcurrentRequests: 2}))
	router.GET("/test", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond)
		c.String(200, "OK")
	})

	// Start 3 concurrent requests (limit is 2)
	var wg sync.WaitGroup
	results := make(chan int, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/test", nil)
			router.ServeHTTP(w, req)
			results <- w.Code
		}()
	}

	wg.Wait()
	close(results)

	// Collect results in main goroutine
	rejectedCount := 0
	for code := range results {
		if code != 200 {
			rejectedCount++
		}
	}
	// At least one should be rejected (429 or 500)
	assert.Greater(t, rejectedCount, 0)
}

// TestLoggerWithNonIntKeyIndex tests logger with non-int key index
func TestLoggerWithNonIntKeyIndex(t *testing.T) {
	router := gin.New()
	router.Use(Logger(types.LogConfig{Level: "info", Format: "text"}))
	router.GET("/test", func(c *gin.Context) {
		c.Set("keyIndex", "string-index")
		c.Set("keyPreview", "sk-test***")
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestLoggerWithNonIntRetryCount tests logger with non-int retry count
func TestLoggerWithNonIntRetryCount(t *testing.T) {
	router := gin.New()
	router.Use(Logger(types.LogConfig{Level: "info", Format: "text"}))
	router.GET("/test", func(c *gin.Context) {
		c.Set("retryCount", "string-count")
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
}

// TestCORSVaryHeaderExisting tests CORS Vary header when already exists
func TestCORSVaryHeaderExisting(t *testing.T) {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Header("Vary", "Accept-Encoding")
		c.Next()
	})
	router.Use(CORS(types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"http://localhost:3000"},
	}))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Header().Get("Vary"), "Origin")
}

// TestCORSVaryHeaderAlreadyHasOrigin tests CORS when Vary already has Origin
func TestCORSVaryHeaderAlreadyHasOrigin(t *testing.T) {
	router := gin.New()
	router.Use(CORS(types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"http://localhost:3000"},
	}))
	router.GET("/test", func(c *gin.Context) {
		c.Header("Vary", "Origin, Accept-Encoding")
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	// Should not duplicate Origin
	varyHeader := w.Header().Get("Vary")
	assert.Contains(t, varyHeader, "Origin")
}

// TestExtractAuthKeyQueryRemoval tests that query key is removed
func TestExtractAuthKeyQueryRemoval(t *testing.T) {
	router := gin.New()
	var finalURL string
	router.Use(func(c *gin.Context) {
		_ = extractAuthKey(c)
		finalURL = c.Request.URL.String()
		c.Next()
	})
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test?key=secret&other=value", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.NotContains(t, finalURL, "key=secret")
	assert.Contains(t, finalURL, "other=value")
}

// BenchmarkErrorHandler benchmarks error handler
func BenchmarkErrorHandler(b *testing.B) {
	router := gin.New()
	router.Use(ErrorHandler())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w, req)
	}
}

// BenchmarkStaticCache benchmarks static cache middleware
func BenchmarkStaticCache(b *testing.B) {
	router := gin.New()
	router.Use(StaticCache())
	router.GET("/assets/style.css", func(c *gin.Context) {
		c.String(200, "css")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/assets/style.css", nil)
		router.ServeHTTP(w, req)
	}
}

// BenchmarkSecurityHeaders benchmarks security headers middleware
func BenchmarkSecurityHeaders(b *testing.B) {
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w, req)
	}
}

// BenchmarkRequestBodySizeLimit benchmarks body size limit middleware
func BenchmarkRequestBodySizeLimit(b *testing.B) {
	router := gin.New()
	router.Use(RequestBodySizeLimit(1024))
	router.POST("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/test", strings.NewReader("test"))
		router.ServeHTTP(w, req)
	}
}
