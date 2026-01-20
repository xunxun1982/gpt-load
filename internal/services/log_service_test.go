package services

import (
	"bytes"
	"encoding/csv"
	"net/http/httptest"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupLogServiceTest creates a test database and log service
func setupLogServiceTest(t *testing.T) (*LogService, *gorm.DB) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	err = db.AutoMigrate(&models.RequestLog{})
	require.NoError(t, err)

	encryptionSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)

	service := NewLogService(db, encryptionSvc)
	return service, db
}

// TestNewLogService tests creating a new log service
func TestNewLogService(t *testing.T) {
	service, _ := setupLogServiceTest(t)
	assert.NotNil(t, service)
	assert.NotNil(t, service.DB)
	assert.NotNil(t, service.EncryptionSvc)
}

// TestEscapeLike tests LIKE pattern escaping
func TestEscapeLike(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no special characters",
			input:    "test",
			expected: "test",
		},
		{
			name:     "with percent",
			input:    "test%value",
			expected: "test!%value",
		},
		{
			name:     "with underscore",
			input:    "test_value",
			expected: "test!_value",
		},
		{
			name:     "with escape char",
			input:    "test!value",
			expected: "test!!value",
		},
		{
			name:     "with multiple special chars",
			input:    "test%_!value",
			expected: "test!%!_!!value",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := escapeLike(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestLogFiltersScope tests log filtering
func TestLogFiltersScope(t *testing.T) {
	service, db := setupLogServiceTest(t)
	gin.SetMode(gin.TestMode)

	// Create test data
	keyValue := "test-key-value"
	keyHash := service.EncryptionSvc.Hash(keyValue)
	encryptedKey, err := service.EncryptionSvc.Encrypt(keyValue)
	require.NoError(t, err)

	testLogs := []models.RequestLog{
		{
			ID:              "test-log-1",
			ParentGroupName: "parent1",
			GroupName:       "group1",
			KeyHash:         keyHash,
			KeyValue:        encryptedKey,
			Model:           "gpt-4",
			IsSuccess:       true,
			RequestType:     "chat",
			StatusCode:      200,
			SourceIP:        "192.168.1.1",
			ErrorMessage:    "",
			Timestamp:       time.Now(),
		},
		{
			ID:              "test-log-2",
			ParentGroupName: "parent2",
			GroupName:       "group2",
			KeyHash:         "other-hash",
			KeyValue:        "other-key",
			Model:           "gpt-3.5-turbo",
			IsSuccess:       false,
			RequestType:     "completion",
			StatusCode:      500,
			SourceIP:        "192.168.1.2",
			ErrorMessage:    "test error",
			Timestamp:       time.Now().Add(-1 * time.Hour),
		},
	}

	for _, log := range testLogs {
		err := db.Create(&log).Error
		require.NoError(t, err)
	}

	tests := []struct {
		name          string
		queryParams   map[string]string
		expectedCount int
	}{
		{
			name:          "no filters",
			queryParams:   map[string]string{},
			expectedCount: 2,
		},
		{
			name:          "filter by parent_group_name",
			queryParams:   map[string]string{"parent_group_name": "parent1"},
			expectedCount: 1,
		},
		{
			name:          "filter by group_name",
			queryParams:   map[string]string{"group_name": "group2"},
			expectedCount: 1,
		},
		{
			name:          "filter by key_value",
			queryParams:   map[string]string{"key_value": keyValue},
			expectedCount: 1,
		},
		{
			name:          "filter by model",
			queryParams:   map[string]string{"model": "gpt-4"},
			expectedCount: 1,
		},
		{
			name:          "filter by is_success true",
			queryParams:   map[string]string{"is_success": "true"},
			expectedCount: 1,
		},
		{
			name:          "filter by is_success false",
			queryParams:   map[string]string{"is_success": "false"},
			expectedCount: 1,
		},
		{
			name:          "filter by request_type",
			queryParams:   map[string]string{"request_type": "chat"},
			expectedCount: 1,
		},
		{
			name:          "filter by status_code",
			queryParams:   map[string]string{"status_code": "200"},
			expectedCount: 1,
		},
		{
			name:          "filter by source_ip",
			queryParams:   map[string]string{"source_ip": "192.168.1.1"},
			expectedCount: 1,
		},
		{
			name:          "filter by error_contains",
			queryParams:   map[string]string{"error_contains": "test"},
			expectedCount: 1,
		},
		{
			name:          "multiple filters",
			queryParams:   map[string]string{"group_name": "group1", "is_success": "true"},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/", nil)

			// Set query parameters
			q := c.Request.URL.Query()
			for key, value := range tt.queryParams {
				q.Add(key, value)
			}
			c.Request.URL.RawQuery = q.Encode()

			var count int64
			err := service.GetLogsQuery(c).Count(&count).Error
			require.NoError(t, err)
			assert.Equal(t, int64(tt.expectedCount), count)
		})
	}
}

// TestGetLogsQuery tests getting logs query
func TestGetLogsQuery(t *testing.T) {
	service, db := setupLogServiceTest(t)
	gin.SetMode(gin.TestMode)

	// Create test log
	testLog := models.RequestLog{
		GroupName:  "test-group",
		Model:      "gpt-4",
		IsSuccess:  true,
		StatusCode: 200,
		Timestamp:  time.Now(),
	}
	err := db.Create(&testLog).Error
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	query := service.GetLogsQuery(c)
	assert.NotNil(t, query)

	var logs []models.RequestLog
	err = query.Find(&logs).Error
	require.NoError(t, err)
	assert.Len(t, logs, 1)
}

// TestStreamLogKeysToCSV tests streaming log keys to CSV
func TestStreamLogKeysToCSV(t *testing.T) {
	service, db := setupLogServiceTest(t)
	gin.SetMode(gin.TestMode)

	// Create test data
	keyValue := "test-key-value"
	keyHash := service.EncryptionSvc.Hash(keyValue)
	encryptedKey, err := service.EncryptionSvc.Encrypt(keyValue)
	require.NoError(t, err)

	testLogs := []models.RequestLog{
		{
			ID:         "test-csv-1",
			GroupName:  "group1",
			KeyHash:    keyHash,
			KeyValue:   encryptedKey,
			StatusCode: 200,
			Timestamp:  time.Now(),
		},
		{
			ID:         "test-csv-2",
			GroupName:  "group1",
			KeyHash:    keyHash,
			KeyValue:   encryptedKey,
			StatusCode: 201,
			Timestamp:  time.Now().Add(-1 * time.Hour),
		},
	}

	for _, log := range testLogs {
		err := db.Create(&log).Error
		require.NoError(t, err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	var buf bytes.Buffer
	err = service.StreamLogKeysToCSV(c, &buf)
	require.NoError(t, err)

	// Parse CSV
	reader := csv.NewReader(&buf)
	records, err := reader.ReadAll()
	require.NoError(t, err)

	// Should have header + 1 unique key
	assert.Len(t, records, 2)
	assert.Equal(t, []string{"key_value", "group_name", "status_code"}, records[0])
	assert.Equal(t, keyValue, records[1][0])
	assert.Equal(t, "group1", records[1][1])
	assert.Equal(t, "200", records[1][2]) // Should use latest timestamp
}

// TestStreamLogKeysToCSV_EmptyResult tests CSV export with no results
func TestStreamLogKeysToCSV_EmptyResult(t *testing.T) {
	service, _ := setupLogServiceTest(t)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	var buf bytes.Buffer
	err := service.StreamLogKeysToCSV(c, &buf)
	require.NoError(t, err)

	// Parse CSV
	reader := csv.NewReader(&buf)
	records, err := reader.ReadAll()
	require.NoError(t, err)

	// Should only have header
	assert.Len(t, records, 1)
	assert.Equal(t, []string{"key_value", "group_name", "status_code"}, records[0])
}

// TestStreamLogKeysToCSV_WithFilters tests CSV export with filters
func TestStreamLogKeysToCSV_WithFilters(t *testing.T) {
	service, db := setupLogServiceTest(t)
	gin.SetMode(gin.TestMode)

	// Create test data with different groups
	key1 := "key1"
	key2 := "key2"
	hash1 := service.EncryptionSvc.Hash(key1)
	hash2 := service.EncryptionSvc.Hash(key2)
	encrypted1, err := service.EncryptionSvc.Encrypt(key1)
	require.NoError(t, err)
	encrypted2, err := service.EncryptionSvc.Encrypt(key2)
	require.NoError(t, err)

	testLogs := []models.RequestLog{
		{
			ID:         "test-filter-1",
			GroupName:  "group1",
			KeyHash:    hash1,
			KeyValue:   encrypted1,
			StatusCode: 200,
			Timestamp:  time.Now(),
		},
		{
			ID:         "test-filter-2",
			GroupName:  "group2",
			KeyHash:    hash2,
			KeyValue:   encrypted2,
			StatusCode: 200,
			Timestamp:  time.Now(),
		},
	}

	for _, log := range testLogs {
		err := db.Create(&log).Error
		require.NoError(t, err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?group_name=group1", nil)

	var buf bytes.Buffer
	err = service.StreamLogKeysToCSV(c, &buf)
	require.NoError(t, err)

	// Parse CSV
	reader := csv.NewReader(&buf)
	records, err := reader.ReadAll()
	require.NoError(t, err)

	// Should have header + 1 filtered key
	assert.Len(t, records, 2)
	assert.Equal(t, key1, records[1][0])
	assert.Equal(t, "group1", records[1][1])
}

// TestLogFiltersScope_TimeRange tests time range filtering
func TestLogFiltersScope_TimeRange(t *testing.T) {
	service, db := setupLogServiceTest(t)
	gin.SetMode(gin.TestMode)

	now := time.Now()
	testLogs := []models.RequestLog{
		{
			ID:         "test-time-1",
			GroupName:  "group1",
			StatusCode: 200,
			Timestamp:  now.Add(-2 * time.Hour),
		},
		{
			ID:         "test-time-2",
			GroupName:  "group2",
			StatusCode: 200,
			Timestamp:  now.Add(-1 * time.Hour),
		},
		{
			ID:         "test-time-3",
			GroupName:  "group3",
			StatusCode: 200,
			Timestamp:  now,
		},
	}

	for _, log := range testLogs {
		err := db.Create(&log).Error
		require.NoError(t, err)
	}

	tests := []struct {
		name          string
		startTime     string
		endTime       string
		expectedCount int
	}{
		{
			name:          "all logs",
			startTime:     "",
			endTime:       "",
			expectedCount: 3,
		},
		{
			name:          "logs after start time",
			startTime:     now.Add(-90 * time.Minute).Format(time.RFC3339),
			endTime:       "",
			expectedCount: 2,
		},
		{
			name:          "logs before end time",
			startTime:     "",
			endTime:       now.Add(-90 * time.Minute).Format(time.RFC3339),
			expectedCount: 1,
		},
		{
			name:          "logs in time range",
			startTime:     now.Add(-90 * time.Minute).Format(time.RFC3339),
			endTime:       now.Add(-30 * time.Minute).Format(time.RFC3339),
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/", nil)

			q := c.Request.URL.Query()
			if tt.startTime != "" {
				q.Add("start_time", tt.startTime)
			}
			if tt.endTime != "" {
				q.Add("end_time", tt.endTime)
			}
			c.Request.URL.RawQuery = q.Encode()

			var count int64
			err := service.GetLogsQuery(c).Count(&count).Error
			require.NoError(t, err)
			assert.Equal(t, int64(tt.expectedCount), count)
		})
	}
}

// BenchmarkEscapeLike benchmarks LIKE pattern escaping
func BenchmarkEscapeLike(b *testing.B) {
	input := "test%_!value"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = escapeLike(input)
	}
}

// BenchmarkLogFiltersScope benchmarks log filtering
func BenchmarkLogFiltersScope(b *testing.B) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		b.Fatal(err)
	}
	err = db.AutoMigrate(&models.RequestLog{})
	if err != nil {
		b.Fatal(err)
	}

	encryptionSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	if err != nil {
		b.Fatal(err)
	}
	service := NewLogService(db, encryptionSvc)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?group_name=test&is_success=true", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = service.GetLogsQuery(c)
	}
}

// BenchmarkStreamLogKeysToCSV benchmarks CSV export
func BenchmarkStreamLogKeysToCSV(b *testing.B) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		b.Fatal(err)
	}
	err = db.AutoMigrate(&models.RequestLog{})
	if err != nil {
		b.Fatal(err)
	}

	encryptionSvc, err := encryption.NewService("test-encryption-key-32-bytes!!")
	if err != nil {
		b.Fatal(err)
	}
	service := NewLogService(db, encryptionSvc)

	// Create test data
	keyValue := "test-key"
	keyHash := encryptionSvc.Hash(keyValue)
	encryptedKey, err := encryptionSvc.Encrypt(keyValue)
	if err != nil {
		b.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		if err := db.Create(&models.RequestLog{
			GroupName:  "group1",
			KeyHash:    keyHash,
			KeyValue:   encryptedKey,
			StatusCode: 200,
			Timestamp:  time.Now(),
		}).Error; err != nil {
			b.Fatal(err)
		}
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		_ = service.StreamLogKeysToCSV(c, &buf)
	}
}
