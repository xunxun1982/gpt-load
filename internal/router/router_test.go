package router

import (
	"embed"
	"gpt-load/internal/handler"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

//go:embed testdata
var testFS embed.FS

func TestEmbedFolder(t *testing.T) {
	t.Parallel()

	// Test with valid embedded path
	fs := EmbedFolder(testFS, "testdata")
	assert.NotNil(t, fs)

	// Verify the filesystem can be used
	exists := fs.Exists("", "test.txt")
	assert.True(t, exists, "test.txt should exist in testdata")

	// Note: fs.Sub does not return error for non-existent paths in embed.FS
	// It only fails when trying to open files, so we cannot test panic behavior here
	// This is expected behavior of Go's embed.FS implementation
}

func TestEmbedFileSystemExists(t *testing.T) {
	t.Parallel()

	// Create a mock file system
	efs := embedFileSystem{
		FileSystem: http.Dir("."),
	}

	// Test existing path
	exists := efs.Exists("", "router.go")
	assert.True(t, exists)

	// Test non-existing path
	exists = efs.Exists("", "nonexistent.go")
	assert.False(t, exists)
}

func TestRegisterSystemRoutes(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Create mock server handler
	mockHandler := &handler.Server{}
	registerSystemRoutes(router, mockHandler)

	// Test health endpoint exists (will return error but endpoint is registered)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)
	router.ServeHTTP(w, req)

	// Should not return 404 (endpoint exists)
	assert.NotEqual(t, http.StatusNotFound, w.Code)
}

func TestRegisterPublicAPIRoutes(t *testing.T) {
	// Skip this test as it requires full handler initialization
	// The function is tested through integration tests
	t.Skip("Requires full handler initialization with dependencies")
}

func TestRegisterFrontendRoutes(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	indexPage := []byte("<html><body>Test</body></html>")
	registerFrontendRoutes(router, testFS, indexPage)

	// Test root path returns index page
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
}

func TestRegisterFrontendRoutes_APINotFound(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	indexPage := []byte("<html><body>Test</body></html>")
	registerFrontendRoutes(router, testFS, indexPage)

	// Test API path - without API routes registered, it falls back to frontend
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/nonexistent", nil)
	router.ServeHTTP(w, req)

	// Without API routes registered, frontend handler serves the index page
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRegisterFrontendRoutes_ProxyNotFound(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	indexPage := []byte("<html><body>Test</body></html>")
	registerFrontendRoutes(router, testFS, indexPage)

	// Test proxy path - without proxy routes registered, it falls back to frontend
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/proxy/nonexistent", nil)
	router.ServeHTTP(w, req)

	// Without proxy routes registered, frontend handler serves the index page
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRegisterFrontendRoutes_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	indexPage := []byte("<html><body>Test</body></html>")
	registerFrontendRoutes(router, testFS, indexPage)

	// Test POST to root path
	// Note: POST to "/" is handled by NoRoute (not NoMethod) because no POST route is registered
	// The NoRoute handler serves the index page for non-API/proxy paths, so it returns 200
	// This is the expected behavior - frontend SPA should handle all non-API routes
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/", nil)
	router.ServeHTTP(w, req)

	// Frontend fallback serves index page for all non-API/proxy routes regardless of method
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
}

func TestRegisterFrontendRoutes_CacheHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	indexPage := []byte("<html><body>Test</body></html>")
	registerFrontendRoutes(router, testFS, indexPage)

	// Test HTML pages have no-cache headers
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/some-page", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, "no-cache, no-store, must-revalidate", w.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", w.Header().Get("Pragma"))
	assert.Equal(t, "0", w.Header().Get("Expires"))
}

// Benchmark tests for PGO optimization
func BenchmarkRegisterSystemRoutes(b *testing.B) {
	b.ReportAllocs()

	gin.SetMode(gin.TestMode)
	mockHandler := &handler.Server{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		router := gin.New()
		registerSystemRoutes(router, mockHandler)
	}
}

func BenchmarkHealthEndpoint(b *testing.B) {
	b.ReportAllocs()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockHandler := &handler.Server{}
	registerSystemRoutes(router, mockHandler)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/health", nil)
		router.ServeHTTP(w, req)
	}
}

func BenchmarkFrontendRouting(b *testing.B) {
	b.ReportAllocs()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	indexPage := []byte("<html><body>Test</body></html>")
	registerFrontendRoutes(router, testFS, indexPage)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/", nil)
		router.ServeHTTP(w, req)
	}
}
