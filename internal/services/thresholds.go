package services

// Batch operation thresholds for key operations.
// These thresholds determine whether operations should be synchronous or asynchronous
// based on the number of keys being processed.
//
// Threshold design rationale:
// - Tier 1 (Fast Sync): ≤1000 keys - Simple operations, immediate feedback (<5s)
// - Tier 2 (Bulk Sync): 1000-5000 keys - Optimized batch operations (5-15s)
// - Tier 3 (Large Sync): 5000-10000 keys - Large batches, moderate wait time (15-30s)
// - Tier 4 (Optimized Sync): 10000-20000 keys - Very large batches, stays within HTTP timeout (30-60s)
// - Tier 5 (Async): >20000 keys - Background processing to avoid HTTP timeout
//
// These thresholds are based on:
// 1. Salesforce best practices: <2000 sync, ≥2000 async
// 2. Microsoft recommendations: batch size up to 1000 operations
// 3. ETL best practices: batch INSERT 10x faster than single
// 4. Empirical testing with SQLite/MySQL/PostgreSQL in this project
const (
	// FastSyncThreshold is the maximum number of keys for fast synchronous operations.
	// Operations below this threshold use simple, fast methods (e.g., AddMultipleKeys).
	// Target response time: <5 seconds
	FastSyncThreshold = 1000

	// BulkSyncThreshold is the maximum number of keys for bulk synchronous operations.
	// Operations below this threshold use optimized bulk methods (e.g., BulkImportService).
	// Target response time: 5-15 seconds
	BulkSyncThreshold = 5000

	// LargeSyncThreshold is the maximum number of keys for large synchronous operations.
	// Operations below this threshold use large batches with moderate wait time.
	// Target response time: 15-30 seconds
	LargeSyncThreshold = 10000

	// OptimizedSyncThreshold is the maximum number of keys for optimized synchronous operations.
	// Operations below this threshold use very large batches but stay within HTTP timeout.
	// Target response time: 30-60 seconds
	OptimizedSyncThreshold = 20000

	// AsyncThreshold is the threshold above which operations become asynchronous.
	// Operations above this threshold return immediately with a task_id for progress tracking.
	// This prevents HTTP timeouts and provides better user experience for large datasets.
	AsyncThreshold = OptimizedSyncThreshold
)

// OperationTier represents the tier of a batch operation based on size.
type OperationTier int

const (
	// TierFastSync represents fast synchronous operations (≤1000 keys)
	TierFastSync OperationTier = iota
	// TierBulkSync represents bulk synchronous operations (1000-5000 keys)
	TierBulkSync
	// TierLargeSync represents large synchronous operations (5000-10000 keys)
	TierLargeSync
	// TierOptimizedSync represents optimized synchronous operations (10000-20000 keys)
	TierOptimizedSync
	// TierAsync represents asynchronous operations (>20000 keys)
	TierAsync
)

// GetOperationTier determines the appropriate operation tier based on key count.
// This function provides a centralized decision point for all batch operations.
func GetOperationTier(keyCount int64) OperationTier {
	switch {
	case keyCount <= FastSyncThreshold:
		return TierFastSync
	case keyCount <= BulkSyncThreshold:
		return TierBulkSync
	case keyCount <= LargeSyncThreshold:
		return TierLargeSync
	case keyCount <= OptimizedSyncThreshold:
		return TierOptimizedSync
	default:
		return TierAsync
	}
}

// String returns a human-readable name for the operation tier.
func (t OperationTier) String() string {
	switch t {
	case TierFastSync:
		return "fast_sync"
	case TierBulkSync:
		return "bulk_sync"
	case TierLargeSync:
		return "large_sync"
	case TierOptimizedSync:
		return "optimized_sync"
	case TierAsync:
		return "async"
	default:
		return "unknown"
	}
}
