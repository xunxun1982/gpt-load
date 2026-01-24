package services

// Batch operation thresholds for key operations.
// These thresholds determine whether operations should be synchronous or asynchronous
// based on the number of keys being processed.
//
// Threshold design rationale:
// - Tier 1 (Fast Sync): ≤1000 keys - Simple operations, immediate feedback (<5s)
// - Tier 2 (Bulk Sync): 1001-5000 keys - Optimized batch operations (5-15s)
// - Tier 3 (Large Sync): 5001-10000 keys - Large batches, moderate wait time (15-30s)
// - Tier 4 (Optimized Sync): 10001-20000 keys - Very large batches, stays within HTTP timeout (30-60s)
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

// Database-specific batch size limits for bulk operations
// These limits are based on database constraints and performance testing
const (
	// MaxMySQLBatchSize is the maximum batch size for MySQL bulk inserts
	// Limited by max_allowed_packet (default 4MB) and performance considerations
	MaxMySQLBatchSize = 5000

	// MaxPostgresBatchSize is the maximum batch size for PostgreSQL bulk inserts
	// Limited by 65535 parameter limit and performance considerations
	MaxPostgresBatchSize = 3000

	// MaxSQLiteBatchSize is the maximum batch size for SQLite bulk inserts
	// Limited by 1MB SQL statement size and performance considerations
	MaxSQLiteBatchSize = 50
)

// Progress reporting thresholds for batch operations
// These thresholds determine when to enable detailed progress logging
const (
	// LargeImportThreshold defines when to enable progress logging for imports
	// Imports larger than this will log progress at 25%, 50%, 75% intervals
	LargeImportThreshold = BulkSyncThreshold // 5000 keys

	// LargeExportThreshold defines when to enable progress logging for exports
	// Exports larger than this will log progress at 25%, 50%, 75% intervals
	LargeExportThreshold = LargeSyncThreshold // 10000 keys

	// LargeCleanupThreshold defines when to enable progress logging for cleanup operations
	// Cleanup operations larger than this will log progress every 10000 records
	LargeCleanupThreshold = LargeSyncThreshold // 10000 records
)

// Batch processing chunk sizes for different operations
// These sizes balance performance and resource usage
const (
	// DefaultDeleteChunkSize is the default chunk size for key deletion operations
	// Aligns with FastSyncThreshold for consistency
	DefaultDeleteChunkSize = FastSyncThreshold // 1000 keys

	// LogCleanupBatchSize is the batch size for log cleanup operations
	// Optimized to minimize lock contention and timeout risk
	LogCleanupBatchSize = 1500 // records per batch

	// ImportDecryptBatchSize is the batch size for decrypt-and-insert during import
	// Keys are decrypted and inserted in batches to provide progress feedback
	// Balances memory usage, progress granularity, and transaction overhead
	ImportDecryptBatchSize = 1000 // keys per decrypt-insert batch

	// ImportProgressReportInterval is the interval for reporting progress during import
	// Progress is reported every N keys to avoid excessive updates
	ImportProgressReportInterval = 500 // keys

	// ExportBatchSize is the batch size for exporting keys from a single group
	// Uses offset pagination to avoid FindInBatches limitations
	ExportBatchSize = 2000 // keys per export batch

	// ExportMultiGroupBatchSize is the batch size for exporting keys from multiple groups
	// Larger batch size for efficiency when exporting system-wide
	ExportMultiGroupBatchSize = 5000 // keys per export batch

	// HourlyStatsBatchSize is the batch size for upserting hourly statistics
	// Used for PostgreSQL and MySQL batch upsert operations
	HourlyStatsBatchSize = 500 // stats per batch

	// HourlyStatsBatchSizeSQLite is the batch size for SQLite hourly statistics
	// Smaller batch size for SQLite due to single-writer model
	HourlyStatsBatchSizeSQLite = 50 // stats per batch

	// DynamicWeightBatchSizeSQLite is the batch size for SQLite dynamic weight persistence
	// Smaller batch size for SQLite due to single-writer model
	DynamicWeightBatchSizeSQLite = 50 // metrics per batch

	// SubGroupBatchSize is the batch size for creating sub-group relationships
	// Fixed batch size ensures consistent behavior even with large sub-group counts
	SubGroupBatchSize = 100 // sub-groups per batch
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
