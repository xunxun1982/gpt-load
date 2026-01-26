package keypool

// Batch operation thresholds for key deletion operations.
// These thresholds align with services/thresholds.go for consistency.
//
// Threshold design rationale:
// - Tier 1-2 (≤5K): Small batches for fast sync operations
// - Tier 3-4 (≤20K): Medium batches for large sync operations
// - Tier 5 (>20K): Large batches for async operations
const (
	// BulkSyncThreshold is the maximum number of keys for bulk synchronous operations.
	// Aligns with services.BulkSyncThreshold for consistency.
	BulkSyncThreshold = 5000

	// OptimizedSyncThreshold is the maximum number of keys for optimized synchronous operations.
	// Aligns with services.OptimizedSyncThreshold for consistency.
	OptimizedSyncThreshold = 20000
)
