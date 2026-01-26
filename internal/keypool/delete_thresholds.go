package keypool

// Batch operation thresholds for key deletion operations.
// These thresholds align with services/thresholds.go for consistency.
//
// Threshold design rationale:
// - Tier 1-2 (≤5K): Small batches for fast sync operations
// - Tier 3-4 (≤20K): Medium batches for large sync operations
// - Tier 5 (>20K): Large batches for async operations
//
// AI Review Note: Suggested consolidating duplicated constants across packages.
// Decision: Keep separate definitions to avoid circular dependencies between keypool and services.
// The alignment is documented in comments and verified by tests. Extracting to a shared package
// would add complexity without significant benefit for these stable, rarely-changed values.
const (
	// BulkSyncThreshold is the maximum number of keys for bulk synchronous operations.
	// Aligns with services.BulkSyncThreshold for consistency.
	BulkSyncThreshold = 5000

	// OptimizedSyncThreshold is the maximum number of keys for optimized synchronous operations.
	// Aligns with services.OptimizedSyncThreshold for consistency.
	OptimizedSyncThreshold = 20000
)
