package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"gorm.io/gorm"
)

func TestNewDynamicWeightPersistence(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	kvStore := store.NewMemoryStore()
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
	lastRollover := now.Add(-24 * time.Hour)

	dbm := &models.DynamicWeightMetric{
		ConsecutiveFailures: 3,
		LastFailureAt:       &lastFailure,
		LastSuccessAt:       &lastSuccess,
		Requests7d:          100,
		Successes7d:         95,
		Requests14d:         200,
		Successes14d:        190,
		Requests30d:         400,
		Successes30d:        380,
		Requests90d:         1000,
		Successes90d:        950,
		Requests180d:        2000,
		Successes180d:       1900,
		LastRolloverAt:      &lastRollover,
		UpdatedAt:           now,
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
	if metrics.Requests7d != 100 {
		t.Errorf("Expected Requests7d 100, got %d", metrics.Requests7d)
	}
	if metrics.Successes7d != 95 {
		t.Errorf("Expected Successes7d 95, got %d", metrics.Successes7d)
	}
	if !metrics.LastRolloverAt.Equal(lastRollover) {
		t.Error("LastRolloverAt mismatch")
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
		name        string
		count       int64
		windowDays  int
		daysPassed  int
		expected    int64
	}{
		{
			name:        "no decay",
			count:       100,
			windowDays:  7,
			daysPassed:  0,
			expected:    100,
		},
		{
			name:        "half window passed",
			count:       100,
			windowDays:  10,
			daysPassed:  5,
			expected:    50,
		},
		{
			name:        "full window passed",
			count:       100,
			windowDays:  7,
			daysPassed:  7,
			expected:    0,
		},
		{
			name:        "more than window passed",
			count:       100,
			windowDays:  7,
			daysPassed:  10,
			expected:    0,
		},
		{
			name:        "zero count",
			count:       0,
			windowDays:  7,
			daysPassed:  3,
			expected:    0,
		},
		{
			name:        "one day passed in 7-day window",
			count:       70,
			windowDays:  7,
			daysPassed:  1,
			expected:    60,
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

func TestDynamicWeightPersistence_StartStop(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)

	// Add dynamic_weight_metrics table migration
	err := db.AutoMigrate(&models.DynamicWeightMetric{})
	if err != nil {
		t.Fatalf("Failed to migrate dynamic_weight_metrics table: %v", err)
	}

	kvStore := store.NewMemoryStore()
	manager := NewDynamicWeightManager(kvStore)
	persistence := NewDynamicWeightPersistence(db, manager)

	// Set short interval for testing
	persistence.interval = 100 * time.Millisecond

	// Start service
	persistence.Start()

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

	// Wait for sync
	time.Sleep(200 * time.Millisecond)

	// Stop service
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	persistence.Stop(ctx)

	// Verify metric was persisted
	var dbMetric models.DynamicWeightMetric
	if err = db.Where("metric_type = ? AND group_id = ?", models.MetricTypeGroup, 1).First(&dbMetric).Error; err != nil {
		t.Fatalf("Failed to find persisted metric: %v", err)
	}
	if dbMetric.ConsecutiveFailures != 1 {
		t.Errorf("Expected ConsecutiveFailures 1, got %d", dbMetric.ConsecutiveFailures)
	}
}
