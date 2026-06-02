package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestNewDynamicWeightPersistence(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)

	persistence := NewDynamicWeightPersistence(db, manager)

	if persistence == nil {
		t.Fatal("Expected non-nil persistence")
	}
	if persistence.db != db {
		t.Error("Expected db to be set")
	}
	if persistence.manager != manager {
		t.Error("Expected manager to be set")
	}
	if persistence.interval != DefaultPersistenceInterval {
		t.Errorf("Expected interval %v, got %v", DefaultPersistenceInterval, persistence.interval)
	}
	if persistence.dirtyKeys == nil {
		t.Error("Expected dirtyKeys map to be initialized")
	}
}

func TestDynamicWeightPersistence_MarkDirtyByKey(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	key := "dw:g:1"
	persistence.MarkDirtyByKey(key)

	persistence.dirtyMu.Lock()
	_, exists := persistence.dirtyKeys[key]
	persistence.dirtyMu.Unlock()

	if !exists {
		t.Error("Expected key to be marked dirty")
	}
}

func TestDbMetricToMemory(t *testing.T) {
	t.Parallel()
	now := time.Now()
	lastFailure := now.Add(-time.Hour)
	lastSuccess := now.Add(-30 * time.Minute)
	lastRateLimit := now.Add(-15 * time.Minute)
	lastRollover := now.Add(-24 * time.Hour)

	t.Run("all fields populated", func(t *testing.T) {
		dbm := &models.DynamicWeightMetric{
			ConsecutiveFailures:   3,
			LastFailureAt:         &lastFailure,
			LastSuccessAt:         &lastSuccess,
			ConsecutiveRateLimits: 2,
			LastRateLimitAt:       &lastRateLimit,
			Requests7d:            100,
			Successes7d:           95,
			RateLimits7d:          4,
			Requests14d:           200,
			Successes14d:          190,
			RateLimits14d:         8,
			Requests30d:           400,
			Successes30d:          380,
			RateLimits30d:         12,
			Requests90d:           1000,
			Successes90d:          950,
			RateLimits90d:         20,
			Requests180d:          2000,
			Successes180d:         1900,
			RateLimits180d:        40,
			LastRolloverAt:        &lastRollover,
			UpdatedAt:             now,
		}

		metrics := dbMetricToMemory(dbm)

		if metrics.ConsecutiveFailures != 3 {
			t.Errorf("Expected ConsecutiveFailures 3, got %d", metrics.ConsecutiveFailures)
		}
		if !metrics.LastFailureAt.Equal(lastFailure) {
			t.Error("LastFailureAt mismatch")
		}
		if !metrics.LastSuccessAt.Equal(lastSuccess) {
			t.Error("LastSuccessAt mismatch")
		}
		if metrics.ConsecutiveRateLimits != 2 {
			t.Errorf("Expected ConsecutiveRateLimits 2, got %d", metrics.ConsecutiveRateLimits)
		}
		if !metrics.LastRateLimitAt.Equal(lastRateLimit) {
			t.Error("LastRateLimitAt mismatch")
		}
		if metrics.Requests7d != 100 {
			t.Errorf("Expected Requests7d 100, got %d", metrics.Requests7d)
		}
		if metrics.Successes7d != 95 {
			t.Errorf("Expected Successes7d 95, got %d", metrics.Successes7d)
		}
		if metrics.RateLimits7d != 4 {
			t.Errorf("Expected RateLimits7d 4, got %d", metrics.RateLimits7d)
		}
		if metrics.RateLimits180d != 40 {
			t.Errorf("Expected RateLimits180d 40, got %d", metrics.RateLimits180d)
		}
		if !metrics.LastRolloverAt.Equal(lastRollover) {
			t.Error("LastRolloverAt mismatch")
		}
	})

	t.Run("nil time pointers", func(t *testing.T) {
		dbm := &models.DynamicWeightMetric{
			ConsecutiveFailures: 0,
			LastFailureAt:       nil,
			LastSuccessAt:       nil,
			Requests7d:          50,
			Successes7d:         50,
			LastRolloverAt:      nil,
			UpdatedAt:           now,
		}

		metrics := dbMetricToMemory(dbm)

		if metrics.ConsecutiveFailures != 0 {
			t.Errorf("Expected ConsecutiveFailures 0, got %d", metrics.ConsecutiveFailures)
		}
		if !metrics.LastFailureAt.IsZero() {
			t.Error("Expected zero LastFailureAt for nil pointer")
		}
		if !metrics.LastSuccessAt.IsZero() {
			t.Error("Expected zero LastSuccessAt for nil pointer")
		}
		if !metrics.LastRateLimitAt.IsZero() {
			t.Error("Expected zero LastRateLimitAt for nil pointer")
		}
		if !metrics.LastRolloverAt.IsZero() {
			t.Error("Expected zero LastRolloverAt for nil pointer")
		}
		if metrics.Requests7d != 50 {
			t.Errorf("Expected Requests7d 50, got %d", metrics.Requests7d)
		}
	})
}

func TestDynamicWeightPersistence_KeyToDBMetricPreservesRateLimitFields(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	lastRateLimit := time.Now().Add(-time.Minute).Truncate(time.Second)
	metrics := &DynamicWeightMetrics{
		ConsecutiveRateLimits: 3,
		LastRateLimitAt:       lastRateLimit,
		Requests7d:            10,
		Successes7d:           6,
		RateLimits7d:          4,
		Requests14d:           20,
		Successes14d:          15,
		RateLimits14d:         5,
		Requests30d:           30,
		Successes30d:          24,
		RateLimits30d:         6,
		Requests90d:           90,
		Successes90d:          70,
		RateLimits90d:         7,
		Requests180d:          180,
		Successes180d:         140,
		RateLimits180d:        8,
	}

	dbm := persistence.keyToDBMetric(GroupMetricsKey(7), metrics)
	if dbm == nil {
		t.Fatal("Expected DB metric")
	}
	if dbm.ConsecutiveRateLimits != 3 {
		t.Errorf("Expected ConsecutiveRateLimits 3, got %d", dbm.ConsecutiveRateLimits)
	}
	if dbm.LastRateLimitAt == nil || !dbm.LastRateLimitAt.Equal(lastRateLimit) {
		t.Error("LastRateLimitAt mismatch")
	}
	if dbm.RateLimits7d != 4 {
		t.Errorf("Expected RateLimits7d 4, got %d", dbm.RateLimits7d)
	}
	if dbm.RateLimits180d != 8 {
		t.Errorf("Expected RateLimits180d 8, got %d", dbm.RateLimits180d)
	}
}

func TestParseSubGroupKeyParts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantAgg uint
		wantSub uint
		wantOK  bool
	}{
		{
			name:    "valid key",
			input:   "123:456",
			wantAgg: 123,
			wantSub: 456,
			wantOK:  true,
		},
		{
			name:    "single digit IDs",
			input:   "1:2",
			wantAgg: 1,
			wantSub: 2,
			wantOK:  true,
		},
		{
			name:    "no colon",
			input:   "123456",
			wantAgg: 0,
			wantSub: 0,
			wantOK:  false,
		},
		{
			name:    "empty after colon",
			input:   "123:",
			wantAgg: 0,
			wantSub: 0,
			wantOK:  false,
		},
		{
			name:    "empty before colon",
			input:   ":456",
			wantAgg: 0,
			wantSub: 0,
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aggID, subID, ok := parseSubGroupKeyParts(tt.input)
			if ok != tt.wantOK {
				t.Errorf("Expected ok=%v, got %v", tt.wantOK, ok)
			}
			if ok {
				if aggID != tt.wantAgg {
					t.Errorf("Expected aggID=%d, got %d", tt.wantAgg, aggID)
				}
				if subID != tt.wantSub {
					t.Errorf("Expected subID=%d, got %d", tt.wantSub, subID)
				}
			}
		})
	}
}

func TestParseModelRedirectKeyParts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		wantGroupID uint
		wantSource  string
		wantTarget  string
		wantOK      bool
	}{
		{
			name:        "valid key",
			input:       "123:gpt-4:gpt-3.5-turbo",
			wantGroupID: 123,
			wantSource:  "gpt-4",
			wantTarget:  "gpt-3.5-turbo",
			wantOK:      true,
		},
		{
			name:        "URL encoded models",
			input:       "456:gpt-4%2F0613:claude-3",
			wantGroupID: 456,
			wantSource:  "gpt-4/0613",
			wantTarget:  "claude-3",
			wantOK:      true,
		},
		{
			name:        "missing second colon",
			input:       "123:gpt-4",
			wantGroupID: 0,
			wantSource:  "",
			wantTarget:  "",
			wantOK:      false,
		},
		{
			name:        "no colons",
			input:       "123gpt4",
			wantGroupID: 0,
			wantSource:  "",
			wantTarget:  "",
			wantOK:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupID, source, target, ok := parseModelRedirectKeyParts(tt.input)
			if ok != tt.wantOK {
				t.Errorf("Expected ok=%v, got %v", tt.wantOK, ok)
			}
			if ok {
				if groupID != tt.wantGroupID {
					t.Errorf("Expected groupID=%d, got %d", tt.wantGroupID, groupID)
				}
				if source != tt.wantSource {
					t.Errorf("Expected source=%s, got %s", tt.wantSource, source)
				}
				if target != tt.wantTarget {
					t.Errorf("Expected target=%s, got %s", tt.wantTarget, target)
				}
			}
		})
	}
}

func TestParseUintSimple(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected uint
	}{
		{
			name:     "simple number",
			input:    "123",
			expected: 123,
		},
		{
			name:     "zero",
			input:    "0",
			expected: 0,
		},
		{
			name:     "large number",
			input:    "999999",
			expected: 999999,
		},
		{
			name:     "with non-digits",
			input:    "12a34",
			expected: 1234,
		},
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseUintSimple(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestDynamicWeightPersistence_LoadFromDatabase(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)

	// Add dynamic_weight_metrics table migration
	if err := db.AutoMigrate(&models.DynamicWeightMetric{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	// Create test metrics in database
	now := time.Now()
	metrics := []models.DynamicWeightMetric{
		{
			MetricType:          models.MetricTypeGroup,
			GroupID:             1,
			ConsecutiveFailures: 2,
			Requests7d:          100,
			Successes7d:         95,
			UpdatedAt:           now,
		},
		{
			MetricType:          models.MetricTypeSubGroup,
			GroupID:             2,
			SubGroupID:          3,
			ConsecutiveFailures: 0,
			Requests7d:          50,
			Successes7d:         50,
			UpdatedAt:           now,
		},
		{
			MetricType:          models.MetricTypeModelRedirect,
			GroupID:             4,
			SourceModel:         "gpt-4",
			TargetModel:         "gpt-3.5-turbo",
			ConsecutiveFailures: 1,
			Requests7d:          200,
			Successes7d:         190,
			UpdatedAt:           now,
		},
	}

	for _, m := range metrics {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("Failed to create metric: %v", err)
		}
	}

	// Load from database
	if err := persistence.LoadFromDatabase(); err != nil {
		t.Fatalf("Failed to load from database: %v", err)
	}

	// Verify group metric
	groupKey := GroupMetricsKey(1)
	data, err := kvStore.Get(groupKey)
	if err != nil {
		t.Fatalf("Failed to get group metric: %v", err)
	}
	var groupMetrics DynamicWeightMetrics
	if err := json.Unmarshal(data, &groupMetrics); err != nil {
		t.Fatalf("Failed to unmarshal group metrics: %v", err)
	}
	if groupMetrics.ConsecutiveFailures != 2 {
		t.Errorf("Expected ConsecutiveFailures 2, got %d", groupMetrics.ConsecutiveFailures)
	}
	if groupMetrics.Requests7d != 100 {
		t.Errorf("Expected Requests7d 100, got %d", groupMetrics.Requests7d)
	}

	// Verify sub-group metric
	subGroupKey := SubGroupMetricsKey(2, 3)
	data, err = kvStore.Get(subGroupKey)
	if err != nil {
		t.Fatalf("Failed to get sub-group metric: %v", err)
	}
	var subGroupMetrics DynamicWeightMetrics
	if err := json.Unmarshal(data, &subGroupMetrics); err != nil {
		t.Fatalf("Failed to unmarshal sub-group metrics: %v", err)
	}
	if subGroupMetrics.Requests7d != 50 {
		t.Errorf("Expected Requests7d 50, got %d", subGroupMetrics.Requests7d)
	}

	// Verify model redirect metric
	mrKey := ModelRedirectMetricsKey(4, "gpt-4", "gpt-3.5-turbo")
	data, err = kvStore.Get(mrKey)
	if err != nil {
		t.Fatalf("Failed to get model redirect metric: %v", err)
	}
	var mrMetrics DynamicWeightMetrics
	if err := json.Unmarshal(data, &mrMetrics); err != nil {
		t.Fatalf("Failed to unmarshal model redirect metrics: %v", err)
	}
	if mrMetrics.ConsecutiveFailures != 1 {
		t.Errorf("Expected ConsecutiveFailures 1, got %d", mrMetrics.ConsecutiveFailures)
	}
}

func TestDynamicWeightPersistence_DeleteAndRestoreSubGroupMetrics(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)

	// Add dynamic_weight_metrics table migration
	if err := db.AutoMigrate(&models.DynamicWeightMetric{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	// Create metric
	metric := models.DynamicWeightMetric{
		MetricType:          models.MetricTypeSubGroup,
		GroupID:             1,
		SubGroupID:          2,
		ConsecutiveFailures: 3,
		Requests7d:          100,
		Successes7d:         95,
		UpdatedAt:           time.Now(),
	}
	if err := db.Create(&metric).Error; err != nil {
		t.Fatalf("Failed to create metric: %v", err)
	}

	// Delete (soft delete)
	if err := persistence.DeleteSubGroupMetrics(1, 2); err != nil {
		t.Fatalf("Failed to delete metric: %v", err)
	}

	// Verify soft delete
	var deleted models.DynamicWeightMetric
	err := db.Unscoped().Where("metric_type = ? AND group_id = ? AND sub_group_id = ?",
		models.MetricTypeSubGroup, 1, 2).First(&deleted).Error
	if err != nil {
		t.Fatalf("Failed to query deleted metric: %v", err)
	}
	if deleted.DeletedAt == nil {
		t.Error("Expected metric to be soft deleted")
	}

	// Restore
	restored, err := persistence.RestoreSubGroupMetrics(1, 2)
	if err != nil {
		t.Fatalf("Failed to restore metric: %v", err)
	}
	if !restored {
		t.Error("Expected metric to be restored")
	}

	// Verify restoration
	var restoredMetric models.DynamicWeightMetric
	err = db.Where("metric_type = ? AND group_id = ? AND sub_group_id = ?",
		models.MetricTypeSubGroup, 1, 2).First(&restoredMetric).Error
	if err != nil {
		t.Fatalf("Failed to query restored metric: %v", err)
	}
	if restoredMetric.DeletedAt != nil {
		t.Error("Expected metric to be restored (deleted_at should be nil)")
	}
	if restoredMetric.ConsecutiveFailures != 3 {
		t.Errorf("Expected ConsecutiveFailures 3, got %d", restoredMetric.ConsecutiveFailures)
	}
}

func TestDynamicWeightPersistence_DeleteAndRestoreModelRedirectMetrics(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)

	// Add dynamic_weight_metrics table migration
	if err := db.AutoMigrate(&models.DynamicWeightMetric{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	// Create metric
	metric := models.DynamicWeightMetric{
		MetricType:          models.MetricTypeModelRedirect,
		GroupID:             1,
		SourceModel:         "gpt-4",
		TargetModel:         "gpt-3.5-turbo",
		ConsecutiveFailures: 2,
		Requests7d:          50,
		Successes7d:         48,
		UpdatedAt:           time.Now(),
	}
	if err := db.Create(&metric).Error; err != nil {
		t.Fatalf("Failed to create metric: %v", err)
	}

	// Delete
	if err := persistence.DeleteModelRedirectMetrics(1, "gpt-4", "gpt-3.5-turbo"); err != nil {
		t.Fatalf("Failed to delete metric: %v", err)
	}

	// Verify soft delete
	var deleted models.DynamicWeightMetric
	err := db.Unscoped().Where("metric_type = ? AND group_id = ? AND source_model = ? AND target_model = ?",
		models.MetricTypeModelRedirect, 1, "gpt-4", "gpt-3.5-turbo").First(&deleted).Error
	if err != nil {
		t.Fatalf("Failed to query deleted metric: %v", err)
	}
	if deleted.DeletedAt == nil {
		t.Error("Expected metric to be soft deleted")
	}

	// Restore
	restored, err := persistence.RestoreModelRedirectMetrics(1, "gpt-4", "gpt-3.5-turbo")
	if err != nil {
		t.Fatalf("Failed to restore metric: %v", err)
	}
	if !restored {
		t.Error("Expected metric to be restored")
	}

	// Verify restoration
	var restoredMetric models.DynamicWeightMetric
	err = db.Where("metric_type = ? AND group_id = ? AND source_model = ? AND target_model = ?",
		models.MetricTypeModelRedirect, 1, "gpt-4", "gpt-3.5-turbo").First(&restoredMetric).Error
	if err != nil {
		t.Fatalf("Failed to query restored metric: %v", err)
	}
	if restoredMetric.DeletedAt != nil {
		t.Error("Expected metric to be restored")
	}
}

func TestDynamicWeightPersistence_CleanupExpiredMetrics(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)

	// Add dynamic_weight_metrics table migration
	if err := db.AutoMigrate(&models.DynamicWeightMetric{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	now := time.Now()
	oldDeletedAt := now.AddDate(0, 0, -SoftDeleteRetentionDays-1)
	recentDeletedAt := now.AddDate(0, 0, -1)

	// Create old deleted metric (should be cleaned up)
	oldMetric := models.DynamicWeightMetric{
		MetricType: models.MetricTypeGroup,
		GroupID:    1,
		Requests7d: 100,
		UpdatedAt:  now,
		DeletedAt:  &oldDeletedAt,
	}
	if err := db.Create(&oldMetric).Error; err != nil {
		t.Fatalf("Failed to create old metric: %v", err)
	}

	// Create recent deleted metric (should not be cleaned up)
	recentMetric := models.DynamicWeightMetric{
		MetricType: models.MetricTypeGroup,
		GroupID:    2,
		Requests7d: 50,
		UpdatedAt:  now,
		DeletedAt:  &recentDeletedAt,
	}
	if err := db.Create(&recentMetric).Error; err != nil {
		t.Fatalf("Failed to create recent metric: %v", err)
	}

	// Cleanup
	count, err := persistence.CleanupExpiredMetrics()
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected to cleanup 1 metric, got %d", count)
	}

	// Verify old metric is gone
	var oldCheck models.DynamicWeightMetric
	err = db.Unscoped().Where("group_id = ?", 1).First(&oldCheck).Error
	if err != gorm.ErrRecordNotFound {
		t.Error("Expected old metric to be permanently deleted")
	}

	// Verify recent metric still exists
	var recentCheck models.DynamicWeightMetric
	err = db.Unscoped().Where("group_id = ?", 2).First(&recentCheck).Error
	if err != nil {
		t.Error("Expected recent metric to still exist")
	}
}

func TestApplyDecay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		count      int64
		windowDays int
		daysPassed int
		expected   int64
	}{
		{
			name:       "no decay",
			count:      100,
			windowDays: 7,
			daysPassed: 0,
			expected:   100,
		},
		{
			name:       "half window passed",
			count:      100,
			windowDays: 10,
			daysPassed: 5,
			expected:   50,
		},
		{
			name:       "full window passed",
			count:      100,
			windowDays: 7,
			daysPassed: 7,
			expected:   0,
		},
		{
			name:       "more than window passed",
			count:      100,
			windowDays: 7,
			daysPassed: 10,
			expected:   0,
		},
		{
			name:       "zero count",
			count:      0,
			windowDays: 7,
			daysPassed: 3,
			expected:   0,
		},
		{
			name:       "one day passed in 7-day window",
			count:      70,
			windowDays: 7,
			daysPassed: 1,
			expected:   60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyDecay(tt.count, tt.windowDays, tt.daysPassed)
			if result != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestDynamicWeightPersistence_RolloverTimeWindowsPreservesRateLimits(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.DynamicWeightMetric{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	groupID := uint(42)
	for i := 0; i < 70; i++ {
		manager.RecordGroupFailure(groupID, true)
	}
	persistence.syncDirtyKeys()

	previousRollover := time.Now().Add(-24 * time.Hour)
	if err := db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ?", models.MetricTypeGroup, groupID).
		Update("last_rollover_at", previousRollover).Error; err != nil {
		t.Fatalf("Failed to set last rollover: %v", err)
	}

	persistence.RolloverTimeWindows()

	var dbMetric models.DynamicWeightMetric
	if err := db.Where("metric_type = ? AND group_id = ?", models.MetricTypeGroup, groupID).
		First(&dbMetric).Error; err != nil {
		t.Fatalf("Failed to load rolled over metric: %v", err)
	}
	if dbMetric.RateLimits7d != 60 {
		t.Errorf("Expected RateLimits7d 60, got %d", dbMetric.RateLimits7d)
	}
	if dbMetric.RateLimits14d != 65 {
		t.Errorf("Expected RateLimits14d 65, got %d", dbMetric.RateLimits14d)
	}
	if dbMetric.RateLimits30d != 67 {
		t.Errorf("Expected RateLimits30d 67, got %d", dbMetric.RateLimits30d)
	}
	if dbMetric.RateLimits90d != 69 {
		t.Errorf("Expected RateLimits90d 69, got %d", dbMetric.RateLimits90d)
	}
	if dbMetric.RateLimits180d != 69 {
		t.Errorf("Expected RateLimits180d 69, got %d", dbMetric.RateLimits180d)
	}
	if dbMetric.LastRolloverAt == nil || !dbMetric.LastRolloverAt.After(previousRollover) {
		t.Error("Expected LastRolloverAt to be updated")
	}

	memoryMetrics, err := manager.GetGroupMetrics(groupID)
	if err != nil {
		t.Fatalf("Failed to get memory metrics: %v", err)
	}
	if memoryMetrics.RateLimits7d != dbMetric.RateLimits7d {
		t.Errorf("Expected memory RateLimits7d %d, got %d", dbMetric.RateLimits7d, memoryMetrics.RateLimits7d)
	}
	if memoryMetrics.RateLimits180d != dbMetric.RateLimits180d {
		t.Errorf("Expected memory RateLimits180d %d, got %d", dbMetric.RateLimits180d, memoryMetrics.RateLimits180d)
	}
	if memoryMetrics.LastRolloverAt.IsZero() || !memoryMetrics.LastRolloverAt.After(previousRollover) {
		t.Error("Expected memory LastRolloverAt to be updated")
	}
}

func TestDynamicWeightPersistence_ResetDueSubGroupHealth(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.Group{}, &models.GroupSubGroup{}, &models.DynamicWeightMetric{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	now := time.Date(2026, 6, 2, 1, 5, 0, 0, time.Local)
	baseline := now.Add(-3 * time.Hour)
	aggregateGroup := models.Group{
		Name:        "agg-reset",
		GroupType:   "aggregate",
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON("[]"),
		Config:      datatypes.JSONMap{"health_reset_interval_seconds": float64(3600)},
		CreatedAt:   baseline,
		UpdatedAt:   baseline,
	}
	if err := db.Create(&aggregateGroup).Error; err != nil {
		t.Fatalf("Failed to create aggregate group: %v", err)
	}

	relation := models.GroupSubGroup{
		GroupID:    aggregateGroup.ID,
		SubGroupID: 200,
		Weight:     100,
		CreatedAt:  baseline,
		UpdatedAt:  baseline,
	}
	if err := db.Create(&relation).Error; err != nil {
		t.Fatalf("Failed to create sub-group relation: %v", err)
	}

	metric := models.DynamicWeightMetric{
		MetricType:          models.MetricTypeSubGroup,
		GroupID:             aggregateGroup.ID,
		SubGroupID:          200,
		ConsecutiveFailures: 4,
		Requests7d:          10,
		UpdatedAt:           baseline,
	}
	if err := db.Create(&metric).Error; err != nil {
		t.Fatalf("Failed to create dynamic weight metric: %v", err)
	}
	if err := manager.SetMetrics(SubGroupMetricsKey(aggregateGroup.ID, 200), &DynamicWeightMetrics{
		ConsecutiveFailures: 4,
		Requests7d:          10,
		UpdatedAt:           baseline,
	}); err != nil {
		t.Fatalf("Failed to seed memory metrics: %v", err)
	}

	resetCount, err := persistence.ResetDueSubGroupHealth(now)
	if err != nil {
		t.Fatalf("ResetDueSubGroupHealth failed: %v", err)
	}
	if resetCount != 1 {
		t.Fatalf("Expected 1 reset, got %d", resetCount)
	}

	var metricCount int64
	if err := db.Model(&models.DynamicWeightMetric{}).
		Where("metric_type = ? AND group_id = ? AND sub_group_id = ?", models.MetricTypeSubGroup, aggregateGroup.ID, 200).
		Count(&metricCount).Error; err != nil {
		t.Fatalf("Failed to count metrics: %v", err)
	}
	if metricCount != 0 {
		t.Fatalf("Expected persisted metric to be deleted, got %d rows", metricCount)
	}

	memoryMetrics, err := manager.GetSubGroupMetrics(aggregateGroup.ID, 200)
	if err != nil {
		t.Fatalf("Failed to get memory metrics: %v", err)
	}
	if memoryMetrics.ConsecutiveFailures != 0 || memoryMetrics.Requests7d != 0 {
		t.Fatalf("Expected memory metrics to be reset, got %+v", memoryMetrics)
	}

	var updatedRelation models.GroupSubGroup
	if err := db.First(&updatedRelation, relation.ID).Error; err != nil {
		t.Fatalf("Failed to reload relation: %v", err)
	}
	expectedSlot := time.Date(2026, 6, 2, 1, 0, 0, 0, time.Local)
	if updatedRelation.LastHealthResetAt == nil || !updatedRelation.LastHealthResetAt.Equal(expectedSlot) {
		t.Fatalf("Expected last reset at %v, got %v", expectedSlot, updatedRelation.LastHealthResetAt)
	}
}

func TestDynamicWeightPersistence_ResetDueSubGroupHealthSkipsCurrentSlotAfterConfigUpdate(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.Group{}, &models.GroupSubGroup{}, &models.DynamicWeightMetric{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	now := time.Date(2026, 6, 2, 10, 5, 0, 0, time.Local)
	aggregateGroup := models.Group{
		Name:        "agg-reset-new",
		GroupType:   "aggregate",
		ChannelType: "openai",
		TestModel:   "gpt-4",
		Upstreams:   datatypes.JSON("[]"),
		Config:      datatypes.JSONMap{"health_reset_interval_seconds": float64(86400)},
		UpdatedAt:   now.Add(-time.Hour),
	}
	if err := db.Create(&aggregateGroup).Error; err != nil {
		t.Fatalf("Failed to create aggregate group: %v", err)
	}
	relation := models.GroupSubGroup{
		GroupID:    aggregateGroup.ID,
		SubGroupID: 201,
		Weight:     100,
		UpdatedAt:  now.Add(-time.Hour),
	}
	if err := db.Create(&relation).Error; err != nil {
		t.Fatalf("Failed to create sub-group relation: %v", err)
	}
	if err := db.Create(&models.DynamicWeightMetric{
		MetricType: models.MetricTypeSubGroup,
		GroupID:    aggregateGroup.ID,
		SubGroupID: 201,
		Requests7d: 1,
		UpdatedAt:  now,
	}).Error; err != nil {
		t.Fatalf("Failed to create dynamic weight metric: %v", err)
	}

	resetCount, err := persistence.ResetDueSubGroupHealth(now)
	if err != nil {
		t.Fatalf("ResetDueSubGroupHealth failed: %v", err)
	}
	if resetCount != 0 {
		t.Fatalf("Expected no reset in the already-started daily slot, got %d", resetCount)
	}
}

func TestSubGroupHealthResetSlotUsesRelationOverride(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 2, 6, 1, 0, 0, time.Local)
	baseline := now.Add(-8 * time.Hour)
	lastReset := time.Date(2026, 6, 2, 4, 0, 0, 0, time.Local)

	slot, due := subGroupHealthResetSlot(now, 2*3600, &lastReset, baseline)
	if !due {
		t.Fatal("Expected relation override to be due at the 06:00 slot")
	}
	expectedSlot := time.Date(2026, 6, 2, 6, 0, 0, 0, time.Local)
	if !slot.Equal(expectedSlot) {
		t.Fatalf("Expected slot %v, got %v", expectedSlot, slot)
	}
}

func TestDynamicWeightPersistence_StartStop(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)

	// Add dynamic_weight_metrics table migration
	err := db.AutoMigrate(&models.DynamicWeightMetric{})
	if err != nil {
		t.Fatalf("Failed to migrate dynamic_weight_metrics table: %v", err)
	}

	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	// Set short interval for testing
	persistence.interval = 100 * time.Millisecond

	// Start service
	persistence.Start()
	// Ensure Stop is called on all exit paths to prevent goroutine leak
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		persistence.Stop(ctx)
	})

	// Mark a key dirty
	key := GroupMetricsKey(1)
	metrics := &DynamicWeightMetrics{
		ConsecutiveFailures: 1,
		Requests7d:          10,
		Successes7d:         9,
		UpdatedAt:           time.Now(),
	}
	if err := manager.SetMetrics(key, metrics); err != nil {
		t.Fatalf("Failed to set metrics: %v", err)
	}
	persistence.MarkDirtyByKey(key)

	// Poll until the metric is persisted or timeout
	// Using polling instead of sleep to avoid flaky tests in CI
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	persisted := false
	for !persisted {
		select {
		case <-deadline:
			t.Fatal("Timed out waiting for metric to be persisted")
		case <-ticker.C:
			var count int64
			db.Model(&models.DynamicWeightMetric{}).
				Where("metric_type = ? AND group_id = ?", models.MetricTypeGroup, 1).
				Count(&count)
			persisted = count > 0
		}
	}

	// Verify metric was persisted
	var dbMetric models.DynamicWeightMetric
	if err = db.Where("metric_type = ? AND group_id = ?", models.MetricTypeGroup, 1).First(&dbMetric).Error; err != nil {
		t.Fatalf("Failed to find persisted metric: %v", err)
	}
	if dbMetric.ConsecutiveFailures != 1 {
		t.Errorf("Expected ConsecutiveFailures 1, got %d", dbMetric.ConsecutiveFailures)
	}
}
