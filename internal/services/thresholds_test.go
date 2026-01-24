package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetOperationTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		keyCount int64
		expected OperationTier
	}{
		{
			name:     "Zero keys",
			keyCount: 0,
			expected: TierFastSync,
		},
		{
			name:     "Small batch - 500 keys",
			keyCount: 500,
			expected: TierFastSync,
		},
		{
			name:     "Boundary - exactly 1000 keys",
			keyCount: 1000,
			expected: TierFastSync,
		},
		{
			name:     "Medium batch - 2000 keys",
			keyCount: 2000,
			expected: TierBulkSync,
		},
		{
			name:     "Boundary - exactly 5000 keys",
			keyCount: 5000,
			expected: TierBulkSync,
		},
		{
			name:     "Large batch - 7500 keys",
			keyCount: 7500,
			expected: TierLargeSync,
		},
		{
			name:     "Boundary - exactly 10000 keys",
			keyCount: 10000,
			expected: TierLargeSync,
		},
		{
			name:     "Very large batch - 15000 keys",
			keyCount: 15000,
			expected: TierOptimizedSync,
		},
		{
			name:     "Boundary - exactly 20000 keys",
			keyCount: 20000,
			expected: TierOptimizedSync,
		},
		{
			name:     "Huge batch - 50000 keys",
			keyCount: 50000,
			expected: TierAsync,
		},
		{
			name:     "Massive batch - 1000000 keys",
			keyCount: 1000000,
			expected: TierAsync,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetOperationTier(tt.keyCount)
			assert.Equal(t, tt.expected, result, "GetOperationTier(%d) should return %s", tt.keyCount, tt.expected.String())
		})
	}
}

func TestOperationTierString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier     OperationTier
		expected string
	}{
		{TierFastSync, "fast_sync"},
		{TierBulkSync, "bulk_sync"},
		{TierLargeSync, "large_sync"},
		{TierOptimizedSync, "optimized_sync"},
		{TierAsync, "async"},
		{OperationTier(999), "unknown"}, // Invalid tier
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.tier.String()
			assert.Equal(t, tt.expected, result, "Tier %d should return string '%s'", tt.tier, tt.expected)
		})
	}
}

func TestThresholdConstants(t *testing.T) {
	t.Parallel()

	// Verify threshold ordering
	assert.Less(t, FastSyncThreshold, BulkSyncThreshold, "FastSyncThreshold should be less than BulkSyncThreshold")
	assert.Less(t, BulkSyncThreshold, LargeSyncThreshold, "BulkSyncThreshold should be less than LargeSyncThreshold")
	assert.Less(t, LargeSyncThreshold, OptimizedSyncThreshold, "LargeSyncThreshold should be less than OptimizedSyncThreshold")
	assert.Equal(t, OptimizedSyncThreshold, AsyncThreshold, "AsyncThreshold should equal OptimizedSyncThreshold")

	// Verify specific values match design
	assert.Equal(t, int64(1000), int64(FastSyncThreshold), "FastSyncThreshold should be 1000")
	assert.Equal(t, int64(5000), int64(BulkSyncThreshold), "BulkSyncThreshold should be 5000")
	assert.Equal(t, int64(10000), int64(LargeSyncThreshold), "LargeSyncThreshold should be 10000")
	assert.Equal(t, int64(20000), int64(OptimizedSyncThreshold), "OptimizedSyncThreshold should be 20000")
}

func TestGetOperationTierBoundaries(t *testing.T) {
	t.Parallel()

	// Test boundary transitions
	assert.Equal(t, TierFastSync, GetOperationTier(FastSyncThreshold), "At FastSyncThreshold boundary")
	assert.Equal(t, TierBulkSync, GetOperationTier(FastSyncThreshold+1), "Just above FastSyncThreshold")

	assert.Equal(t, TierBulkSync, GetOperationTier(BulkSyncThreshold), "At BulkSyncThreshold boundary")
	assert.Equal(t, TierLargeSync, GetOperationTier(BulkSyncThreshold+1), "Just above BulkSyncThreshold")

	assert.Equal(t, TierLargeSync, GetOperationTier(LargeSyncThreshold), "At LargeSyncThreshold boundary")
	assert.Equal(t, TierOptimizedSync, GetOperationTier(LargeSyncThreshold+1), "Just above LargeSyncThreshold")

	assert.Equal(t, TierOptimizedSync, GetOperationTier(OptimizedSyncThreshold), "At OptimizedSyncThreshold boundary")
	assert.Equal(t, TierAsync, GetOperationTier(OptimizedSyncThreshold+1), "Just above OptimizedSyncThreshold")
}
