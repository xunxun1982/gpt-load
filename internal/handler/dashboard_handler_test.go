package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestServerWithDB creates a test server with a provided database
func setupTestServerWithDB(t *testing.T, db *gorm.DB) *Server {
	t.Helper()

	mockConfig := &config.MockConfig{
		AuthKeyValue:       "test-auth-key-12345678",
		EncryptionKeyValue: "test-encryption-key-12345678",
	}

	// Using real SystemSettingsManager instead of mock because:
	// 1. It's a simple in-memory settings manager without external dependencies
	// 2. Tests need actual settings behavior for realistic scenarios
	// 3. No performance or isolation concerns in this context
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("")
	require.NoError(t, err)

	return &Server{
		DB:              db,
		config:          mockConfig,
		SettingsManager: settingsManager,
		EncryptionSvc:   encSvc,
	}
}

func TestStats(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		setupData      func(*gorm.DB)
		expectedStatus int
		checkResponse  func(*testing.T, map[string]any)
	}{
		{
			name:           "empty_database",
			setupData:      func(db *gorm.DB) {},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, resp map[string]any) {
				data := resp["data"].(map[string]any)
				keyCount := data["key_count"].(map[string]any)
				assert.Equal(t, float64(0), keyCount["value"])
			},
		},
		{
			name: "with_active_keys",
			setupData: func(db *gorm.DB) {
				for i := 0; i < 5; i++ {
					db.Create(&models.APIKey{
						KeyValue: fmt.Sprintf("sk-test-%d", i),
						KeyHash:  fmt.Sprintf("hash-%d", i),
						Status:   models.KeyStatusActive,
					})
				}
				for i := 0; i < 2; i++ {
					db.Create(&models.APIKey{
						KeyValue: fmt.Sprintf("sk-invalid-%d", i),
						KeyHash:  fmt.Sprintf("hash-invalid-%d", i),
						Status:   models.KeyStatusInvalid,
					})
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, resp map[string]any) {
				data := resp["data"].(map[string]any)
				keyCount := data["key_count"].(map[string]any)
				assert.Equal(t, float64(5), keyCount["value"])
				assert.Equal(t, float64(2), keyCount["sub_value"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := setupTestDB(t)
			tt.setupData(db)
			server := setupTestServerWithDB(t, db)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/api/stats", nil)

			server.Stats(c)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectedStatus == http.StatusOK {
				var resp map[string]any
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err)
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestChart(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		queryParams    string
		setupData      func(*gorm.DB)
		expectedStatus int
	}{
		{
			name:        "default_range",
			queryParams: "",
			setupData: func(db *gorm.DB) {
				now := time.Now().Truncate(time.Hour)
				for i := 0; i < 24; i++ {
					db.Create(&models.GroupHourlyStat{
						GroupID:      1,
						Time:         now.Add(-time.Duration(i) * time.Hour),
						SuccessCount: int64(10 + i),
						FailureCount: int64(i),
					})
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid_group_id",
			queryParams:    "?groupId=invalid",
			setupData:      func(db *gorm.DB) {},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := setupTestDB(t)
			tt.setupData(db)
			server := setupTestServerWithDB(t, db)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/api/chart"+tt.queryParams, nil)

			server.Chart(c)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

func TestEncryptionStatus(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	server := setupTestServerWithDB(t, db)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/encryption-status", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)

	server.EncryptionStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

// Benchmark tests
func BenchmarkStats(b *testing.B) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatal(err)
	}

	if err := db.AutoMigrate(&models.APIKey{}, &models.Group{}, &models.RequestLog{}, &models.GroupHourlyStat{}); err != nil {
		b.Fatal(err)
	}

	// Setup test data
	now := time.Now()
	for i := 0; i < 100; i++ {
		db.Create(&models.APIKey{
			KeyValue: fmt.Sprintf("sk-bench-%d", i),
			KeyHash:  fmt.Sprintf("hash-%d", i),
			Status:   models.KeyStatusActive,
		})
	}
	for i := 0; i < 1000; i++ {
		db.Create(&models.RequestLog{
			Timestamp:   now.Add(-time.Duration(i) * time.Minute),
			RequestType: models.RequestTypeFinal,
		})
	}
	for i := 0; i < 48; i++ {
		db.Create(&models.GroupHourlyStat{
			GroupID:      1,
			Time:         now.Add(-time.Duration(i) * time.Hour),
			SuccessCount: int64(100 + i),
			FailureCount: int64(i % 10),
		})
	}

	mockConfig := &config.MockConfig{
		AuthKeyValue:       "bench-auth-key",
		EncryptionKeyValue: "bench-encryption-key",
	}
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("")
	if err != nil {
		b.Fatal(err)
	}

	server := &Server{
		DB:              db,
		config:          mockConfig,
		SettingsManager: settingsManager,
		EncryptionSvc:   encSvc,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/api/stats", nil)
		server.Stats(c)
	}
}

func BenchmarkChart(b *testing.B) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatal(err)
	}

	if err := db.AutoMigrate(&models.GroupHourlyStat{}); err != nil {
		b.Fatal(err)
	}

	now := time.Now().Truncate(time.Hour)
	for groupID := 1; groupID <= 5; groupID++ {
		for i := 0; i < 168; i++ {
			db.Create(&models.GroupHourlyStat{
				GroupID:      uint(groupID),
				Time:         now.Add(-time.Duration(i) * time.Hour),
				SuccessCount: int64(50 + i%100),
				FailureCount: int64(i % 20),
			})
		}
	}

	mockConfig := &config.MockConfig{
		AuthKeyValue:       "bench-auth-key",
		EncryptionKeyValue: "bench-encryption-key",
	}
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("")
	if err != nil {
		b.Fatal(err)
	}

	server := &Server{
		DB:              db,
		config:          mockConfig,
		SettingsManager: settingsManager,
		EncryptionSvc:   encSvc,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/api/chart", nil)
		server.Chart(c)
	}
}
