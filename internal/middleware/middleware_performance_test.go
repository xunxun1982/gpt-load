package middleware

import (
	"net/http/httptest"
	"testing"

	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

// BenchmarkCORSMiddleware benchmarks CORS middleware
func BenchmarkCORSMiddleware(b *testing.B) {
	router := gin.New()
	corsConfig := types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}
	router.Use(CORS(corsConfig))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "https://example.com")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// BenchmarkCORSPreflightRequest benchmarks CORS preflight handling
func BenchmarkCORSPreflightRequest(b *testing.B) {
	router := gin.New()
	corsConfig := types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}
	router.Use(CORS(corsConfig))
	router.POST("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// BenchmarkMultipleMiddlewares benchmarks middleware chain
func BenchmarkMultipleMiddlewares(b *testing.B) {
	router := gin.New()
	corsConfig := types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: false,
	}
	router.Use(CORS(corsConfig))
	router.Use(SecurityHeaders())
	router.Use(gin.Recovery())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "https://example.com")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// BenchmarkHeaderOperations benchmarks header manipulation
func BenchmarkHeaderOperations(b *testing.B) {
	b.Run("SetSingleHeader", func(b *testing.B) {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Header("X-Custom-Header", "value")
			c.Next()
		})
		router.GET("/test", func(c *gin.Context) {
			c.String(200, "OK")
		})

		req := httptest.NewRequest("GET", "/test", nil)

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})

	b.Run("SetMultipleHeaders", func(b *testing.B) {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Header("X-Header-1", "value1")
			c.Header("X-Header-2", "value2")
			c.Header("X-Header-3", "value3")
			c.Header("X-Header-4", "value4")
			c.Next()
		})
		router.GET("/test", func(c *gin.Context) {
			c.String(200, "OK")
		})

		req := httptest.NewRequest("GET", "/test", nil)

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})
}

// BenchmarkContextOperations benchmarks context value operations
func BenchmarkContextOperations(b *testing.B) {
	b.Run("SetContextValue", func(b *testing.B) {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("key", "value")
			c.Next()
		})
		router.GET("/test", func(c *gin.Context) {
			c.String(200, "OK")
		})

		req := httptest.NewRequest("GET", "/test", nil)

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})

	b.Run("GetContextValue", func(b *testing.B) {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("key", "value")
			c.Next()
		})
		router.GET("/test", func(c *gin.Context) {
			_ = c.GetString("key")
			c.String(200, "OK")
		})

		req := httptest.NewRequest("GET", "/test", nil)

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})
}

// BenchmarkRealisticMiddlewareChain simulates realistic middleware usage
func BenchmarkRealisticMiddlewareChain(b *testing.B) {
	router := gin.New()

	// Typical middleware chain in production
	corsConfig := types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}
	router.Use(CORS(corsConfig))
	router.Use(SecurityHeaders())
	router.Use(func(c *gin.Context) {
		// Simulate auth check
		c.Set("user_id", 123)
		c.Next()
	})
	router.Use(func(c *gin.Context) {
		// Simulate logging
		_ = c.Request.Method
		_ = c.Request.URL.Path
		c.Next()
	})

	router.POST("/api/v1/chat/completions", func(c *gin.Context) {
		_ = c.GetInt("user_id")
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("POST", "/api/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// BenchmarkConcurrentMiddlewareRequests benchmarks concurrent request handling
func BenchmarkConcurrentMiddlewareRequests(b *testing.B) {
	router := gin.New()
	corsConfig := types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET"},
		AllowedHeaders: []string{"Content-Type"},
	}
	router.Use(CORS(corsConfig))
	router.Use(SecurityHeaders())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://example.com")

		for pb.Next() {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})
}

// BenchmarkMiddlewareWithDifferentMethods benchmarks different HTTP methods
func BenchmarkMiddlewareWithDifferentMethods(b *testing.B) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	for _, method := range methods {
		b.Run(method, func(b *testing.B) {
			router := gin.New()
			corsConfig := types.CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
				AllowedMethods: []string{method},
				AllowedHeaders: []string{"Content-Type"},
			}
			router.Use(CORS(corsConfig))
			router.Use(SecurityHeaders())

			handler := func(c *gin.Context) {
				c.String(200, "OK")
			}

			switch method {
			case "GET":
				router.GET("/test", handler)
			case "POST":
				router.POST("/test", handler)
			case "PUT":
				router.PUT("/test", handler)
			case "DELETE":
				router.DELETE("/test", handler)
			case "PATCH":
				router.PATCH("/test", handler)
			}

			req := httptest.NewRequest(method, "/test", nil)
			req.Header.Set("Origin", "https://example.com")

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
			}
		})
	}
}

// BenchmarkSecurityHeadersPerf benchmarks security headers middleware performance
func BenchmarkSecurityHeadersPerf(b *testing.B) {
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	req := httptest.NewRequest("GET", "/test", nil)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// BenchmarkRealisticMiddlewareChainConcurrent simulates concurrent realistic middleware usage
// This is the most important benchmark for PGO as it reflects real production patterns
func BenchmarkRealisticMiddlewareChainConcurrent(b *testing.B) {
	router := gin.New()

	// Typical middleware chain in production
	corsConfig := types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}
	router.Use(CORS(corsConfig))
	router.Use(SecurityHeaders())
	router.Use(func(c *gin.Context) {
		// Simulate auth check
		c.Set("user_id", 123)
		c.Next()
	})
	router.Use(func(c *gin.Context) {
		// Simulate logging
		_ = c.Request.Method
		_ = c.Request.URL.Path
		c.Next()
	})

	router.POST("/api/v1/chat/completions", func(c *gin.Context) {
		_ = c.GetInt("user_id")
		c.JSON(200, gin.H{"status": "ok"})
	})

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("POST", "/api/v1/chat/completions", nil)
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-token")

		for pb.Next() {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})
}

// BenchmarkCORSMiddlewareConcurrent benchmarks concurrent CORS middleware
func BenchmarkCORSMiddlewareConcurrent(b *testing.B) {
	router := gin.New()
	corsConfig := types.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}
	router.Use(CORS(corsConfig))
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://example.com")

		for pb.Next() {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})
}

// BenchmarkSecurityHeadersConcurrent benchmarks concurrent security headers middleware
func BenchmarkSecurityHeadersConcurrent(b *testing.B) {
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("GET", "/test", nil)

		for pb.Next() {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})
}

// BenchmarkMultipleMiddlewaresConcurrent benchmarks concurrent middleware chain
func BenchmarkMultipleMiddlewaresConcurrent(b *testing.B) {
	router := gin.New()
	corsConfig := types.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type"},
	}
	router.Use(CORS(corsConfig))
	router.Use(SecurityHeaders())
	router.Use(gin.Recovery())
	router.GET("/test", func(c *gin.Context) {
		c.String(200, "OK")
	})

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://example.com")

		for pb.Next() {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})
}
