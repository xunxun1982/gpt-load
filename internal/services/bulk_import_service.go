package services

import (
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// BulkImportService provides optimized bulk import functionality for different database types
type BulkImportService struct {
	db         *gorm.DB
	dbType     string // "sqlite", "mysql", "postgres"
	batchSizes map[string]int
}

// DatabaseType represents the type of database
type DatabaseType string

const (
	DBTypeSQLite   DatabaseType = "sqlite"
	DBTypeMySQL    DatabaseType = "mysql"
	DBTypePostgres DatabaseType = "postgres"
)

// NewBulkImportService creates a new bulk import service with database-specific optimizations
func NewBulkImportService(db *gorm.DB) *BulkImportService {
	service := &BulkImportService{
		db:         db,
		batchSizes: make(map[string]int),
	}

	// Detect database type
	service.detectDatabaseType()

	// Initialize database-specific optimizations
	service.initializeOptimizations()

	return service
}

// detectDatabaseType detects the database type from the connection
func (s *BulkImportService) detectDatabaseType() {
	// Get the database name from the connection
	dbName := s.db.Dialector.Name()

	switch strings.ToLower(dbName) {
	case "sqlite", "sqlite3":
		s.dbType = "sqlite"
	case "mysql":
		s.dbType = "mysql"
	case "postgres", "postgresql", "pgx":
		s.dbType = "postgres"
	default:
		// Default to SQLite for safety (smallest batch sizes)
		s.dbType = "sqlite"
		logrus.Warnf("Unknown database type %s, defaulting to SQLite optimizations", dbName)
	}

	logrus.Infof("BulkImportService initialized for %s database", s.dbType)
}

// initializeOptimizations sets up database-specific optimizations
func (s *BulkImportService) initializeOptimizations() {
	switch s.dbType {
	case "sqlite":
		s.initializeSQLiteOptimizations()
	case "mysql":
		s.initializeMySQLOptimizations()
	case "postgres":
		s.initializePostgresOptimizations()
	}

	// Set default batch sizes based on database type
	s.setDefaultBatchSizes()
}

// initializeSQLiteOptimizations applies SQLite-specific optimizations
// Note: Only applies safe, global optimizations. Transaction-specific settings
// are applied within the transaction scope in BulkInsertAPIKeysWithTx
// PRAGMA settings can be configured via environment variables for deployment flexibility
func (s *BulkImportService) initializeSQLiteOptimizations() {
	// Apply only safe, global SQLite PRAGMA optimizations
	// Do NOT disable foreign_keys, synchronous, or other safety features globally
	// Use environment variables with reasonable defaults for bulk import operations
	cacheSize := utils.GetEnvOrDefault("SQLITE_CACHE_SIZE", "20000")                 // Increase cache to 20000 pages (~80MB with 4KB pages)
	tempStore := utils.GetEnvOrDefault("SQLITE_TEMP_STORE", "MEMORY")                // Use memory for temporary tables
	mmapSize := utils.GetEnvOrDefault("SQLITE_MMAP_SIZE", "30000000000")             // 30GB memory mapping (virtual, not physical RAM)
	pageSize := utils.GetEnvOrDefault("SQLITE_PAGE_SIZE", "4096")                    // Optimal page size
	busyTimeout := utils.GetEnvOrDefault("SQLITE_BUSY_TIMEOUT", "30000")             // 30 second busy timeout
	walAutocheckpoint := utils.GetEnvOrDefault("SQLITE_WAL_AUTOCHECKPOINT", "10000") // Less frequent WAL checkpoints

	pragmas := []string{
		fmt.Sprintf("PRAGMA cache_size = %s", cacheSize),
		fmt.Sprintf("PRAGMA temp_store = %s", tempStore),
		"PRAGMA journal_mode = WAL", // Ensure WAL mode is enabled
		fmt.Sprintf("PRAGMA page_size = %s", pageSize),
		fmt.Sprintf("PRAGMA mmap_size = %s", mmapSize),
		fmt.Sprintf("PRAGMA busy_timeout = %s", busyTimeout),
		fmt.Sprintf("PRAGMA wal_autocheckpoint = %s", walAutocheckpoint),
	}

	for _, pragma := range pragmas {
		if err := s.db.Exec(pragma).Error; err != nil {
			logrus.Warnf("Failed to apply optimization: %s, error: %v", pragma, err)
		}
	}
}

// initializeMySQLOptimizations applies MySQL-specific optimizations
// Note: Only applies safe, global optimizations. Transaction-specific settings
// are applied within the transaction scope in BulkInsertAPIKeysWithTx
func (s *BulkImportService) initializeMySQLOptimizations() {
	// Check max_allowed_packet for information only
	// Do NOT disable autocommit, unique_checks, or foreign_key_checks globally
	var maxAllowedPacket int64
	s.db.Raw("SELECT @@max_allowed_packet").Scan(&maxAllowedPacket)
	logrus.Infof("MySQL max_allowed_packet: %d bytes", maxAllowedPacket)

	// Note: Transaction-specific optimizations like disabling checks
	// should be applied within the transaction scope, not globally
}

// initializePostgresOptimizations applies PostgreSQL-specific optimizations
// Note: Only applies safe, global optimizations. Transaction-specific settings
// are applied within the transaction scope in BulkInsertAPIKeysWithTx
func (s *BulkImportService) initializePostgresOptimizations() {
	// PostgreSQL optimizations are typically set at session level
	// Most optimizations are handled by GORM's transaction management
	// Do NOT disable synchronous_commit globally as it affects all connections

	logrus.Debug("PostgreSQL bulk import will use transaction-scoped optimizations")
}

// setDefaultBatchSizes sets optimal batch sizes based on database type
func (s *BulkImportService) setDefaultBatchSizes() {
	switch s.dbType {
	case "sqlite":
		// SQLite: Conservative batch sizes due to 1MB SQL statement limit
		// Reduced sizes for encrypted keys which are ~200+ chars each
		s.batchSizes["small"] = 25  // For records with large text fields
		s.batchSizes["medium"] = 50 // For normal records
		s.batchSizes["large"] = 100 // For records with minimal data

	case "mysql":
		// MySQL: Larger batches, limited by max_allowed_packet
		s.batchSizes["small"] = 500   // For records with large text fields
		s.batchSizes["medium"] = 1000 // For normal records
		s.batchSizes["large"] = 2000  // For records with minimal data

	case "postgres":
		// PostgreSQL: Limited by 65535 parameters
		s.batchSizes["small"] = 500   // For records with large text fields
		s.batchSizes["medium"] = 1000 // For normal records
		s.batchSizes["large"] = 2000  // For records with minimal data
	}
}

// CalculateOptimalBatchSize calculates the optimal batch size based on record characteristics and total key count.
// It uses adaptive batch sizing based on operation tier to optimize performance for different data volumes.
func (s *BulkImportService) CalculateOptimalBatchSize(avgFieldSize int, numFields int, totalKeys int) int {
	// Estimate record size in bytes
	recordSize := avgFieldSize * numFields
	if recordSize <= 0 {
		logrus.Debugf("Invalid recordSize calculated (avgFieldSize=%d, numFields=%d), using safe default batch size", avgFieldSize, numFields)
		// Fallback to a small but safe default to avoid panics
		return 10
	}

	// Get base batch size based on database type and record size
	var baseBatchSize int
	var sqliteMaxBySize int // Track SQLite size-based limit for later capping
	var maxByConstraint int // MySQL/Postgres size/param constraint

	switch s.dbType {
	case "sqlite":
		// SQLite: 1MB SQL statement limit
		const maxSQLSize = 900000 // 900KB safety margin
		sqliteMaxBySize = maxSQLSize / recordSize
		baseBatchSize = sqliteMaxBySize
		// Reduced max batch size for SQLite due to performance issues with large batches
		if baseBatchSize > MaxSQLiteBatchSize {
			baseBatchSize = MaxSQLiteBatchSize
		}

	case "mysql":
		// MySQL: Limited by max_allowed_packet (default 4MB, often 16MB+)
		var maxPacket int64 = 4194304 // Default 4MB
		s.db.Raw("SELECT @@max_allowed_packet").Scan(&maxPacket)

		// Use 80% of max_allowed_packet for safety
		safePacketSize := int(maxPacket) * 8 / 10
		maxByConstraint = safePacketSize / recordSize
		baseBatchSize = maxByConstraint
		if baseBatchSize > MaxMySQLBatchSize {
			baseBatchSize = MaxMySQLBatchSize
		}

	case "postgres":
		// PostgreSQL: 65535 parameter limit
		const maxParams = 65535
		// Each record has numFields parameters + some overhead
		paramsPerRecord := numFields + 2 // +2 for safety
		maxByConstraint = maxParams / paramsPerRecord
		baseBatchSize = maxByConstraint
		if baseBatchSize > MaxPostgresBatchSize {
			baseBatchSize = MaxPostgresBatchSize
		}
	}

	// Ensure minimum batch size (allow 1 for very large records to avoid exceeding DB limits)
	if baseBatchSize < 1 {
		baseBatchSize = 1
	}

	// Adaptive batch sizing based on operation tier
	// Larger operations benefit from larger batches to reduce transaction overhead
	tier := GetOperationTier(int64(totalKeys))
	var multiplier int
	switch tier {
	case TierFastSync: // ≤1000 keys
		multiplier = 1 // Use base batch size
	case TierBulkSync: // 1001-5000 keys
		multiplier = 2 // 2x base batch size for better throughput
	case TierLargeSync: // 5001-10000 keys
		multiplier = 3 // 3x base batch size for large operations
	case TierOptimizedSync: // 10001-20000 keys
		multiplier = 4 // 4x base batch size for very large operations
	case TierAsync: // 20001-100000 keys
		multiplier = 10 // 10x base batch size for async operations
	case TierMassiveAsync: // >100000 keys
		multiplier = 20 // 20x base batch size for massive async operations (500K+ keys)
	default:
		multiplier = 1
	}

	adaptiveBatchSize := baseBatchSize * multiplier

	// Ensure we don't exceed database-specific limits
	switch s.dbType {
	case "sqlite":
		// Determine max batch size based on tier
		var maxSQLiteBatch int
		switch tier {
		case TierAsync:
			// Async operations (20K-100K keys): use larger batches
			// For 30K keys: 30000/5000 = 6 batches
			maxSQLiteBatch = MaxSQLiteBatchSizeAsync // 5000
		case TierMassiveAsync:
			// Massive async operations (>100K keys): use even larger batches
			// For 500K keys: 500000/10000 = 50 batches (vs 100 batches with 5000)
			maxSQLiteBatch = MaxSQLiteBatchSizeMassive // 10000
		default:
			// For sync/small-to-medium operations, cap at 5× the base SQLite batch limit
			maxSQLiteBatch = MaxSQLiteBatchSize * 5
		}
		// Cap by SQL statement size to prevent tier multiplier from exceeding size limit
		if sqliteMaxBySize > 0 && maxSQLiteBatch > sqliteMaxBySize {
			maxSQLiteBatch = sqliteMaxBySize
		}
		if adaptiveBatchSize > maxSQLiteBatch {
			adaptiveBatchSize = maxSQLiteBatch
		}
	case "mysql":
		// Cap by max_allowed_packet constraint to prevent tier multiplier from exceeding packet size
		if maxByConstraint > 0 && adaptiveBatchSize > maxByConstraint {
			adaptiveBatchSize = maxByConstraint
		}
		if adaptiveBatchSize > MaxMySQLBatchSize {
			adaptiveBatchSize = MaxMySQLBatchSize // Cap at 10000 for MySQL
		}
	case "postgres":
		// Cap by parameter limit to prevent tier multiplier from exceeding parameter count
		if maxByConstraint > 0 && adaptiveBatchSize > maxByConstraint {
			adaptiveBatchSize = maxByConstraint
		}
		if adaptiveBatchSize > MaxPostgresBatchSize {
			adaptiveBatchSize = MaxPostgresBatchSize // Cap at 10000 for PostgreSQL
		}
	}

	// Ensure minimum batch size (allow 1 for very large records to avoid exceeding DB limits)
	if adaptiveBatchSize < 1 {
		adaptiveBatchSize = 1
	}

	logrus.Debugf("Adaptive batch size: base=%d, tier=%s, multiplier=%d, final=%d (totalKeys=%d)",
		baseBatchSize, tier.String(), multiplier, adaptiveBatchSize, totalKeys)

	return adaptiveBatchSize
}

// BulkInsertAPIKeys performs optimized bulk insert of API keys
func (s *BulkImportService) BulkInsertAPIKeys(keys []models.APIKey) error {
	if len(keys) == 0 {
		return nil
	}

	// Start transaction for better performance
	tx := s.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	// Use the transactional version
	if err := s.BulkInsertAPIKeysWithTx(tx, keys, nil); err != nil {
		tx.Rollback()
		return err
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit bulk insert transaction: %w", err)
	}

	return nil
}

// BulkInsertAPIKeysWithTx performs optimized bulk insert using an existing transaction
// This method should be used when you're already in a transaction context
// progressCallback is an optional callback to report progress during insertion
func (s *BulkImportService) BulkInsertAPIKeysWithTx(tx *gorm.DB, keys []models.APIKey, progressCallback func(processed int)) error {
	if len(keys) == 0 {
		return nil
	}

	// Calculate optimal batch size based on key characteristics and total count
	avgKeySize := 0
	for _, key := range keys {
		avgKeySize += len(key.KeyValue) + len(key.KeyHash) + len(key.Notes)
	}
	if len(keys) > 0 {
		avgKeySize = avgKeySize / len(keys)
	}

	// APIKey has approximately 8 fields
	totalKeys := len(keys)
	batchSize := s.CalculateOptimalBatchSize(avgKeySize/8, 8, totalKeys)

	// Initial summary log
	logrus.Infof("Bulk importing %d keys (batch size: %d)", totalKeys, batchSize)
	logrus.Debugf("Database type: %s, Average key size: %d bytes", s.dbType, avgKeySize)

	// Create a session with optimized settings using the provided transaction
	session := tx.Session(&gorm.Session{
		PrepareStmt:            true, // Use prepared statements
		SkipDefaultTransaction: true, // We're using the provided transaction
		CreateBatchSize:        batchSize,
	})

	// Process in optimized batches
	totalProcessed := 0
	startTime := time.Now()
	lastLoggedPercent := 0
	lastProgressLog := startTime

	// For SQLite, apply additional optimizations before bulk insert
	if s.dbType == "sqlite" {
		// Temporarily disable autocommit and use a savepoint
		tx.Exec("SAVEPOINT bulk_insert")
	}

	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}

		batch := keys[i:end]

		// For SQLite, use Create instead of CreateInBatches for better performance
		var err error
		if s.dbType == "sqlite" {
			// Direct batch insert without CreateInBatches overhead
			// Use a single Create call which GORM optimizes into a bulk INSERT
			err = session.Create(&batch).Error
		} else {
			// Use CreateInBatches for MySQL and PostgreSQL
			err = session.CreateInBatches(batch, len(batch)).Error
		}

		if err != nil {
			if s.dbType == "sqlite" {
				tx.Exec("ROLLBACK TO SAVEPOINT bulk_insert")
			}
			return fmt.Errorf("failed to insert batch %d-%d: %w", i, end, err)
		}

		totalProcessed += len(batch)

		// Report progress via callback if provided
		if progressCallback != nil {
			progressCallback(totalProcessed)
		}

		// For SQLite, yield to other queries periodically to prevent long lock times.
		// Release and recreate savepoint every few batches to allow other reads.
		// Savepoint errors are logged but not fatal - the import continues with degraded performance.
		if s.dbType == "sqlite" && totalProcessed%ImportProgressReportInterval == 0 && totalProcessed < totalKeys {
			if err := tx.Exec("RELEASE SAVEPOINT bulk_insert").Error; err != nil {
				logrus.WithError(err).Warn("Failed to release savepoint during bulk import, continuing without yield")
			}
			// Brief yield to allow pending reads
			time.Sleep(5 * time.Millisecond)
			if err := tx.Exec("SAVEPOINT bulk_insert").Error; err != nil {
				logrus.WithError(err).Warn("Failed to recreate savepoint during bulk import, continuing without savepoint protection")
			}
		}

		// Log progress at 25%, 50%, 75% intervals for large imports
		// Reduced logging frequency to minimize overhead
		if totalKeys > LargeImportThreshold {
			currentPercent := (totalProcessed * 100) / totalKeys
			if currentPercent >= lastLoggedPercent+25 {
				elapsed := time.Since(startTime)
				rate := float64(totalProcessed) / elapsed.Seconds()
				logrus.Infof("Import progress: %d%% (%d/%d keys, %.0f keys/sec)",
					currentPercent, totalProcessed, totalKeys, rate)
				lastLoggedPercent = currentPercent
			}
		}

		// Debug logging for detailed progress - throttled to once per second
		if logrus.GetLevel() >= logrus.DebugLevel {
			now := time.Now()
			if now.Sub(lastProgressLog) >= time.Second || totalProcessed == totalKeys {
				elapsed := time.Since(startTime)
				rate := float64(totalProcessed) / elapsed.Seconds()
				logrus.Debugf("Processed %d/%d keys (%.0f keys/sec)", totalProcessed, totalKeys, rate)
				lastProgressLog = now
			}
		}
	}

	// For SQLite, release the savepoint
	if s.dbType == "sqlite" {
		tx.Exec("RELEASE SAVEPOINT bulk_insert")
	}

	// Final summary
	elapsed := time.Since(startTime)
	rate := float64(totalKeys) / elapsed.Seconds()
	logrus.Infof("Bulk import completed: %d keys in %v (%.0f keys/sec)",
		totalKeys, elapsed.Round(time.Millisecond), rate)

	return nil
}

// restoreConstraints is deprecated and should not be used
// All constraint modifications should be transaction-scoped
// This method is kept for backward compatibility but does nothing
func (s *BulkImportService) restoreConstraints() {
	// No-op: All optimizations are now transaction-scoped
	// Constraints are automatically restored when transaction commits/rollbacks
	logrus.Debug("restoreConstraints called but no action needed (transaction-scoped optimizations)")
}

var _ = (*BulkImportService).restoreConstraints

// BulkInsertGeneric performs optimized bulk insert for any model type
func (s *BulkImportService) BulkInsertGeneric(records interface{}, recordCount int, avgRecordSize int) error {
	if recordCount == 0 {
		return nil
	}

	// Estimate fields based on average record size (rough estimate)
	estimatedFields := 10 // Default estimate
	if avgRecordSize < 100 {
		estimatedFields = 5
	} else if avgRecordSize > 1000 {
		estimatedFields = 15
	}

	batchSize := s.CalculateOptimalBatchSize(avgRecordSize/estimatedFields, estimatedFields, recordCount)

	logrus.Infof("Bulk inserting %d records with batch size %d for %s database",
		recordCount, batchSize, s.dbType)

	// Start transaction
	tx := s.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	// Create optimized session
	session := tx.Session(&gorm.Session{
		PrepareStmt:            true,
		SkipDefaultTransaction: false,
		CreateBatchSize:        batchSize,
	})

	// Use CreateInBatches for automatic batch processing
	if err := session.CreateInBatches(records, batchSize).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to bulk insert records: %w", err)
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit bulk insert transaction: %w", err)
	}

	return nil
}

// GetDatabaseType returns the detected database type
func (s *BulkImportService) GetDatabaseType() string {
	return s.dbType
}

// GetRecommendedBatchSize returns the recommended batch size for a given record type
func (s *BulkImportService) GetRecommendedBatchSize(recordType string) int {
	if size, exists := s.batchSizes[recordType]; exists {
		return size
	}
	return s.batchSizes["medium"] // Default to medium
}

// EstimateImportTime estimates the time required for bulk import
func (s *BulkImportService) EstimateImportTime(recordCount int, avgRecordSize int) time.Duration {
	// Rough estimates based on database type and typical performance
	var recordsPerSecond float64

	switch s.dbType {
	case "sqlite":
		// SQLite: ~10k-50k records/second depending on size
		if avgRecordSize < 100 {
			recordsPerSecond = 30000
		} else if avgRecordSize < 500 {
			recordsPerSecond = 15000
		} else {
			recordsPerSecond = 5000
		}

	case "mysql":
		// MySQL: ~50k-200k records/second
		if avgRecordSize < 100 {
			recordsPerSecond = 100000
		} else if avgRecordSize < 500 {
			recordsPerSecond = 50000
		} else {
			recordsPerSecond = 20000
		}

	case "postgres":
		// PostgreSQL: ~30k-150k records/second
		if avgRecordSize < 100 {
			recordsPerSecond = 80000
		} else if avgRecordSize < 500 {
			recordsPerSecond = 40000
		} else {
			recordsPerSecond = 15000
		}

	default:
		recordsPerSecond = 10000 // Conservative estimate
	}

	estimatedSeconds := float64(recordCount) / recordsPerSecond
	return time.Duration(estimatedSeconds * float64(time.Second))
}
