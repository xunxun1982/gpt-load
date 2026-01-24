package services

import (
	"fmt"
	"gpt-load/internal/models"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupBulkImportTest creates a test environment for BulkImportService
func setupBulkImportTest(tb testing.TB) (*gorm.DB, *BulkImportService) {
	tb.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		PrepareStmt: false,
	})
	require.NoError(tb, err)

	sqlDB, err := db.DB()
	require.NoError(tb, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	tb.Cleanup(func() {
		_ = sqlDB.Close()
	})

	err = db.AutoMigrate(&models.APIKey{}, &models.Group{})
	require.NoError(tb, err)

	bulkImportSvc := NewBulkImportService(db)

	return db, bulkImportSvc
}

// TestBulkImportService_DatabaseDetection tests database type detection
func TestBulkImportService_DatabaseDetection(t *testing.T) {
	t.Parallel()
	_, svc := setupBulkImportTest(t)

	// SQLite should be detected
	dbType := svc.GetDatabaseType()
	assert.Equal(t, "sqlite", dbType)
}

// TestBulkImportService_SmallBatch tests bulk import with a small batch
func TestBulkImportService_SmallBatch(t *testing.T) {
	t.Parallel()
	db, svc := setupBulkImportTest(t)

	// Create test group
	group := models.Group{
		Name:        "test-group",
		ChannelType: "openai",
		GroupType:   "standard",
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	err := db.Create(&group).Error
	require.NoError(t, err)

	// Create 10 test keys
	keys := make([]models.APIKey, 10)
	for i := 0; i < 10; i++ {
		keys[i] = models.APIKey{
			GroupID:  group.ID,
			KeyValue: fmt.Sprintf("encrypted-key-%d", i),
			KeyHash:  fmt.Sprintf("hash-%d", i),
			Status:   models.KeyStatusActive,
		}
	}

	// Bulk insert
	err = svc.BulkInsertAPIKeys(keys)
	require.NoError(t, err)

	// Verify all keys were inserted
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(10), count)
}

// TestBulkImportService_LargeBatch tests bulk import with a large batch
func TestBulkImportService_LargeBatch(t *testing.T) {
	t.Parallel()
	db, svc := setupBulkImportTest(t)

	// Create test group
	group := models.Group{
		Name:        "test-group-large",
		ChannelType: "openai",
		GroupType:   "standard",
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	err := db.Create(&group).Error
	require.NoError(t, err)

	// Create 1000 test keys
	keys := make([]models.APIKey, 1000)
	for i := 0; i < 1000; i++ {
		keys[i] = models.APIKey{
			GroupID:  group.ID,
			KeyValue: fmt.Sprintf("encrypted-key-%d", i),
			KeyHash:  fmt.Sprintf("hash-%d", i),
			Status:   models.KeyStatusActive,
		}
	}

	// Measure performance
	start := time.Now()
	err = svc.BulkInsertAPIKeys(keys)
	duration := time.Since(start)
	require.NoError(t, err)

	// Verify all keys were inserted
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(1000), count)

	// Performance check: should complete in reasonable time (< 5 seconds for 1000 keys)
	// Skip time assertion in short mode to avoid flaky failures on slow CI nodes
	if !testing.Short() {
		assert.Less(t, duration, 5*time.Second, "Bulk import took too long")
	} else {
		t.Logf("bulk import duration: %s", duration)
	}
}

// TestBulkImportService_EmptyBatch tests handling of empty batch
func TestBulkImportService_EmptyBatch(t *testing.T) {
	t.Parallel()
	_, svc := setupBulkImportTest(t)

	// Empty batch should not error
	err := svc.BulkInsertAPIKeys([]models.APIKey{})
	assert.NoError(t, err)
}

// TestBulkImportService_CalculateOptimalBatchSize tests batch size calculation
func TestBulkImportService_CalculateOptimalBatchSize(t *testing.T) {
	t.Parallel()
	_, svc := setupBulkImportTest(t)

	tests := []struct {
		name          string
		avgFieldSize  int
		numFields     int
		expectedRange [2]int // min and max expected batch size
	}{
		{
			name:          "small records",
			avgFieldSize:  10,
			numFields:     5,
			expectedRange: [2]int{10, 100},
		},
		{
			name:          "medium records",
			avgFieldSize:  50,
			numFields:     8,
			expectedRange: [2]int{10, 100},
		},
		{
			name:          "large records",
			avgFieldSize:  200,
			numFields:     10,
			expectedRange: [2]int{10, 100},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batchSize := svc.CalculateOptimalBatchSize(tt.avgFieldSize, tt.numFields)
			assert.GreaterOrEqual(t, batchSize, tt.expectedRange[0], "Batch size too small")
			assert.LessOrEqual(t, batchSize, tt.expectedRange[1], "Batch size too large")
		})
	}
}

// TestBulkImportService_WithTransaction tests bulk import within a transaction
func TestBulkImportService_WithTransaction(t *testing.T) {
	t.Parallel()
	db, svc := setupBulkImportTest(t)

	// Create test group
	group := models.Group{
		Name:        "test-group-tx",
		ChannelType: "openai",
		GroupType:   "standard",
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	err := db.Create(&group).Error
	require.NoError(t, err)

	// Start transaction
	tx := db.Begin()
	require.NoError(t, tx.Error)

	// Create test keys
	keys := make([]models.APIKey, 50)
	for i := 0; i < 50; i++ {
		keys[i] = models.APIKey{
			GroupID:  group.ID,
			KeyValue: fmt.Sprintf("encrypted-key-tx-%d", i),
			KeyHash:  fmt.Sprintf("hash-tx-%d", i),
			Status:   models.KeyStatusActive,
		}
	}

	// Bulk insert within transaction
	err = svc.BulkInsertAPIKeysWithTx(tx, keys, nil)
	require.NoError(t, err)

	// Commit transaction
	err = tx.Commit().Error
	require.NoError(t, err)

	// Verify all keys were inserted
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(50), count)
}

// TestBulkImportService_TransactionRollback tests transaction rollback
func TestBulkImportService_TransactionRollback(t *testing.T) {
	t.Parallel()
	db, svc := setupBulkImportTest(t)

	// Create test group
	group := models.Group{
		Name:        "test-group-rollback",
		ChannelType: "openai",
		GroupType:   "standard",
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	err := db.Create(&group).Error
	require.NoError(t, err)

	// Start transaction
	tx := db.Begin()
	require.NoError(t, tx.Error)

	// Create test keys
	keys := make([]models.APIKey, 20)
	for i := 0; i < 20; i++ {
		keys[i] = models.APIKey{
			GroupID:  group.ID,
			KeyValue: fmt.Sprintf("encrypted-key-rollback-%d", i),
			KeyHash:  fmt.Sprintf("hash-rollback-%d", i),
			Status:   models.KeyStatusActive,
		}
	}

	// Bulk insert within transaction
	err = svc.BulkInsertAPIKeysWithTx(tx, keys, nil)
	require.NoError(t, err)

	// Rollback transaction
	err = tx.Rollback().Error
	require.NoError(t, err)

	// Verify no keys were inserted (rollback successful)
	var count int64
	err = db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestBulkImportService_EstimateImportTime tests import time estimation
func TestBulkImportService_EstimateImportTime(t *testing.T) {
	t.Parallel()
	_, svc := setupBulkImportTest(t)

	tests := []struct {
		name          string
		recordCount   int
		avgRecordSize int
	}{
		{
			name:          "small import",
			recordCount:   100,
			avgRecordSize: 100,
		},
		{
			name:          "medium import",
			recordCount:   1000,
			avgRecordSize: 200,
		},
		{
			name:          "large import",
			recordCount:   10000,
			avgRecordSize: 150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			estimate := svc.EstimateImportTime(tt.recordCount, tt.avgRecordSize)
			// Estimate should be positive and reasonable
			assert.Greater(t, estimate, time.Duration(0))
			// For SQLite, estimate should be less than 10 seconds per 1000 records
			maxExpected := time.Duration(tt.recordCount/100) * time.Second
			assert.Less(t, estimate, maxExpected)
		})
	}
}
