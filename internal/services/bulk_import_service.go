package services

import (
	"fmt"
	"gpt-load/internal/models"
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
func (s *BulkImportService) initializeSQLiteOptimizations() {
	// Apply SQLite-specific PRAGMA optimizations for bulk operations
	pragmas := []string{
		"PRAGMA cache_size = 20000",        // Increase cache to 20000 pages (~80MB with 4KB pages)
		"PRAGMA temp_store = MEMORY",       // Use memory for temporary tables
		"PRAGMA journal_mode = WAL",        // Ensure WAL mode is enabled
		"PRAGMA synchronous = OFF",         // Disable sync for maximum speed during import
		"PRAGMA page_size = 4096",         // Optimal page size
		"PRAGMA mmap_size = 30000000000",  // 30GB memory mapping
		"PRAGMA busy_timeout = 30000",     // 30 second busy timeout
		"PRAGMA foreign_keys = OFF",       // Temporarily disable foreign key checks for import
		"PRAGMA locking_mode = EXCLUSIVE",  // Exclusive locking for better performance
		"PRAGMA cache_spill = OFF",        // Don't spill cache to disk during import
		"PRAGMA wal_autocheckpoint = 10000", // Less frequent WAL checkpoints
	}

	for _, pragma := range pragmas {
		if err := s.db.Exec(pragma).Error; err != nil {
			logrus.Warnf("Failed to apply optimization: %s, error: %v", pragma, err)
		}
	}
}

// initializeMySQLOptimizations applies MySQL-specific optimizations
func (s *BulkImportService) initializeMySQLOptimizations() {
	// Apply MySQL-specific optimizations for bulk operations
	optimizations := []string{
		"SET autocommit = 0",                           // Disable autocommit for bulk operations
		"SET unique_checks = 0",                        // Temporarily disable unique checks
		"SET foreign_key_checks = 0",                   // Temporarily disable foreign key checks
		"SET sql_log_bin = 0",                         // Disable binary logging if allowed
		"SET SESSION bulk_insert_buffer_size = 256*1024*1024", // 256MB buffer for bulk inserts
	}

	for _, opt := range optimizations {
		if err := s.db.Exec(opt).Error; err != nil {
			// Some settings might fail due to permissions, log but continue
			logrus.Debugf("MySQL optimization setting failed (may be permission issue): %s, error: %v", opt, err)
		}
	}

	// Check and potentially increase max_allowed_packet
	var maxAllowedPacket int64
	s.db.Raw("SELECT @@max_allowed_packet").Scan(&maxAllowedPacket)
	logrus.Infof("MySQL max_allowed_packet: %d bytes", maxAllowedPacket)
}

// initializePostgresOptimizations applies PostgreSQL-specific optimizations
func (s *BulkImportService) initializePostgresOptimizations() {
	// PostgreSQL optimizations are typically set at session level
	// Most optimizations are handled by GORM's transaction management

	// Disable synchronous_commit for this session (if permissions allow)
	if err := s.db.Exec("SET synchronous_commit = OFF").Error; err != nil {
		logrus.Debugf("PostgreSQL optimization setting failed: synchronous_commit, error: %v", err)
	}

	// Increase work_mem for better sorting/hashing performance
	if err := s.db.Exec("SET work_mem = '256MB'").Error; err != nil {
		logrus.Debugf("PostgreSQL optimization setting failed: work_mem, error: %v", err)
	}
}

// setDefaultBatchSizes sets optimal batch sizes based on database type
func (s *BulkImportService) setDefaultBatchSizes() {
	switch s.dbType {
	case "sqlite":
		// SQLite: Conservative batch sizes due to 1MB SQL statement limit
		// Reduced sizes for encrypted keys which are ~200+ chars each
		s.batchSizes["small"] = 25   // For records with large text fields
		s.batchSizes["medium"] = 50  // For normal records
		s.batchSizes["large"] = 100  // For records with minimal data

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

// CalculateOptimalBatchSize calculates the optimal batch size based on record characteristics
func (s *BulkImportService) CalculateOptimalBatchSize(avgFieldSize int, numFields int) int {
	// Estimate record size in bytes
	recordSize := avgFieldSize * numFields

	var maxBatchSize int

	switch s.dbType {
	case "sqlite":
		// SQLite: 1MB SQL statement limit
		const maxSQLSize = 900000 // 900KB safety margin
		maxBatchSize = maxSQLSize / recordSize
		// Reduced max batch size for SQLite due to performance issues with large batches
		if maxBatchSize > 50 {
			maxBatchSize = 50 // Cap at 50 for SQLite (reduced from 200)
		}

	case "mysql":
		// MySQL: Limited by max_allowed_packet (default 4MB, often 16MB+)
		var maxPacket int64 = 4194304 // Default 4MB
		s.db.Raw("SELECT @@max_allowed_packet").Scan(&maxPacket)

		// Use 80% of max_allowed_packet for safety
		safePacketSize := int(maxPacket) * 8 / 10
		maxBatchSize = safePacketSize / recordSize
		if maxBatchSize > 5000 {
			maxBatchSize = 5000 // Cap at 5000 for MySQL
		}

	case "postgres":
		// PostgreSQL: 65535 parameter limit
		const maxParams = 65535
		// Each record has numFields parameters + some overhead
		paramsPerRecord := numFields + 2 // +2 for safety
		maxBatchSize = maxParams / paramsPerRecord
		if maxBatchSize > 3000 {
			maxBatchSize = 3000 // Cap at 3000 for PostgreSQL
		}
	}

	// Ensure minimum batch size
	if maxBatchSize < 10 {
		maxBatchSize = 10
	}

	return maxBatchSize
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
	if err := s.BulkInsertAPIKeysWithTx(tx, keys); err != nil {
		tx.Rollback()
		return err
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit bulk insert transaction: %w", err)
	}

	// Re-enable constraints
	s.restoreConstraints()

	return nil
}

// BulkInsertAPIKeysWithTx performs optimized bulk insert using an existing transaction
// This method should be used when you're already in a transaction context
func (s *BulkImportService) BulkInsertAPIKeysWithTx(tx *gorm.DB, keys []models.APIKey) error {
	if len(keys) == 0 {
		return nil
	}

	// Calculate optimal batch size based on key characteristics
	avgKeySize := 0
	for _, key := range keys {
		avgKeySize += len(key.KeyValue) + len(key.KeyHash) + len(key.Notes)
	}
	if len(keys) > 0 {
		avgKeySize = avgKeySize / len(keys)
	}

	// APIKey has approximately 8 fields
	batchSize := s.CalculateOptimalBatchSize(avgKeySize/8, 8)
	totalKeys := len(keys)

	// Initial summary log
	logrus.Infof("Bulk importing %d keys (batch size: %d)", totalKeys, batchSize)
	logrus.Debugf("Database type: %s, Average key size: %d bytes", s.dbType, avgKeySize)

	// Create a session with optimized settings using the provided transaction
	session := tx.Session(&gorm.Session{
		PrepareStmt:            true,  // Use prepared statements
		SkipDefaultTransaction: true,  // We're using the provided transaction
		CreateBatchSize:        batchSize,
	})

	// Process in optimized batches
	totalProcessed := 0
	startTime := time.Now()
	lastLoggedPercent := 0

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

		// Log progress at 25%, 50%, 75% intervals for large imports (>5000 keys)
		if totalKeys > 5000 {
			currentPercent := (totalProcessed * 100) / totalKeys
			if currentPercent >= lastLoggedPercent+25 {
				elapsed := time.Since(startTime)
				rate := float64(totalProcessed) / elapsed.Seconds()
				logrus.Infof("Import progress: %d%% (%d/%d keys, %.0f keys/sec)",
					currentPercent, totalProcessed, totalKeys, rate)
				lastLoggedPercent = currentPercent
			}
		}

		// Debug logging for detailed progress
		if logrus.GetLevel() >= logrus.DebugLevel {
			if totalProcessed%500 == 0 || totalProcessed == totalKeys {
				elapsed := time.Since(startTime)
				rate := float64(totalProcessed) / elapsed.Seconds()
				logrus.Debugf("Processed %d/%d keys (%.0f keys/sec)", totalProcessed, totalKeys, rate)
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

// restoreConstraints re-enables database constraints after bulk import
func (s *BulkImportService) restoreConstraints() {
	switch s.dbType {
	case "sqlite":
		s.db.Exec("PRAGMA foreign_keys = ON")
		s.db.Exec("PRAGMA synchronous = NORMAL")  // Restore safe synchronous mode
		s.db.Exec("PRAGMA locking_mode = NORMAL") // Restore normal locking
		s.db.Exec("PRAGMA cache_spill = ON")      // Re-enable cache spill
		s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)") // Checkpoint WAL file

	case "mysql":
		s.db.Exec("SET foreign_key_checks = 1")
		s.db.Exec("SET unique_checks = 1")
		s.db.Exec("SET autocommit = 1")

	case "postgres":
		// PostgreSQL constraints are transaction-scoped, automatically restored
		s.db.Exec("SET synchronous_commit = ON")
	}
}

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

	batchSize := s.CalculateOptimalBatchSize(avgRecordSize/estimatedFields, estimatedFields)

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

	s.restoreConstraints()

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