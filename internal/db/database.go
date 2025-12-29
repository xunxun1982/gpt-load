package db

import (
	"context"
	"fmt"
	"gpt-load/internal/types"
	"gpt-load/internal/utils"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// ReadDB is a separate read-only connection pool for SQLite to avoid read/write lock contention.
// For MySQL and PostgreSQL, this is the same as DB since they handle concurrency natively.
var ReadDB *gorm.DB

func NewDB(configManager types.ConfigManager) (*gorm.DB, error) {
	dbConfig := configManager.GetDatabaseConfig()
	dsn := dbConfig.DSN
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_DSN is not configured")
	}

	var newLogger logger.Interface
	if configManager.GetLogConfig().Level == "debug" {
		// Use logrus output to ensure GORM logs go to both console and file
		newLogger = logger.New(
			log.New(logrus.StandardLogger().Out, "\r\n", log.LstdFlags), // io writer
			logger.Config{
				SlowThreshold:             time.Second, // Slow SQL threshold
				LogLevel:                  logger.Info, // Log level
				IgnoreRecordNotFoundError: true,        // Ignore ErrRecordNotFound error for logger
				Colorful:                  true,        // Disable color
			},
		)
	}

	// Detect driver types once and reuse
	isPostgres := strings.HasPrefix(dsn, "postgres://") ||
		strings.HasPrefix(dsn, "postgresql://") ||
		(strings.Contains(dsn, "host=") && strings.Contains(dsn, "dbname="))
	isMySQL := strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(")

	var dialector gorm.Dialector
	if isPostgres {
		dialector = postgres.New(postgres.Config{
			DSN:                  dsn,
			PreferSimpleProtocol: true,
		})
	} else if isMySQL {
		if !strings.Contains(dsn, "parseTime") {
			if strings.Contains(dsn, "?") {
				dsn += "&parseTime=true"
			} else {
				dsn += "?parseTime=true"
			}
		}
		dialector = mysql.Open(dsn)
	} else {
		// Create directory only for plain filesystem paths (not SQLite file: URIs)
		// SQLite supports various URI formats: file:, file://, file:///, file://localhost/
		// All of these start with "file:", so we skip directory creation for any URI format
		if strings.HasPrefix(dsn, "file:") {
			// Skip mkdir for file: URIs to avoid creating wrong directories
			// SQLite driver will handle URI parsing and path extraction
		} else {
			if err := os.MkdirAll(filepath.Dir(dsn), 0755); err != nil {
				return nil, fmt.Errorf("failed to create database directory: %w", err)
			}
		}
		// Enhanced SQLite optimizations for bulk operations
		// WAL mode: Better concurrency and write performance
		// synchronous=NORMAL: Safe with WAL, faster writes
		// cache_size: Larger cache for better performance (configurable via env var)
		// temp_store=MEMORY: Use memory for temp tables (configurable via env var)
		// mmap_size: Memory mapping for faster reads (set via direct SQL to avoid slow query log)
		cacheSize := utils.GetEnvOrDefault("SQLITE_CACHE_SIZE", "10000")      // ~40MB cache with 4KB pages
		tempStore := utils.GetEnvOrDefault("SQLITE_TEMP_STORE", "MEMORY")     // Use memory for temporary tables
		params := fmt.Sprintf("_pragma=foreign_keys(1)&_busy_timeout=10000&_journal_mode=WAL&_synchronous=NORMAL&cache=shared&_cache_size=%s&_temp_store=%s", cacheSize, tempStore)
		delimiter := "?"
		if strings.Contains(dsn, "?") {
			delimiter = "&"
		}
		dialector = sqlite.Open(dsn + delimiter + params)
	}

	var err error
	DB, err = gorm.Open(dialector, &gorm.Config{
		Logger:      newLogger,
		PrepareStmt: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// Set connection pool parameters based on database type
	if isPostgres || isMySQL {
		// PostgreSQL and MySQL can handle more connections
		sqlDB.SetMaxIdleConns(50)
		sqlDB.SetMaxOpenConns(500)
		sqlDB.SetConnMaxLifetime(time.Hour)

		// Apply additional optimizations for MySQL
		if isMySQL {
			// Set larger timeouts for bulk operations
			sqlDB.SetConnMaxIdleTime(time.Minute * 10)
			// Apply MySQL session optimizations
			if err := DB.Exec("SET SESSION sql_mode='TRADITIONAL'").Error; err != nil {
				return nil, fmt.Errorf("failed to set sql_mode: %w", err)
			}
			if err := DB.Exec("SET SESSION innodb_lock_wait_timeout=50").Error; err != nil {
				return nil, fmt.Errorf("failed to set innodb_lock_wait_timeout: %w", err)
			}
		}
		// For MySQL and PostgreSQL, ReadDB is the same as DB
		ReadDB = DB
	} else {
		// SQLite needs limited connections to avoid locking issues
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetConnMaxLifetime(time.Hour)

		// Apply additional SQLite PRAGMA optimizations via DSN or direct SQL
		// Most PRAGMA settings are already in DSN, but some need to be set via direct SQL
		// Use raw SQL connection to avoid slow SQL logging for initialization commands
		rawDB, err := sqlDB.Conn(context.Background())
		if err != nil {
			log.Printf("failed to acquire connection for SQLite PRAGMAs: %v", err)
		} else {
			// Get environment variables for PRAGMA settings
			mmapSize := utils.GetEnvOrDefault("SQLITE_MMAP_SIZE", "30000000000") // 30GB memory mapping (virtual, not physical RAM)
			pageSize := utils.GetEnvOrDefault("SQLITE_PAGE_SIZE", "4096")        // Optimal page size
			journalSizeLimit := utils.GetEnvOrDefault("SQLITE_JOURNAL_SIZE_LIMIT", "67108864") // 64MB WAL file limit
			walAutocheckpoint := utils.GetEnvOrDefault("SQLITE_WAL_AUTOCHECKPOINT", "1000") // Checkpoint every 1000 pages

			// Set PRAGMA via raw SQL connection to avoid GORM slow SQL logging
			// These are initialization commands, not actual slow queries
			// Use a bounded context to prevent rare hangs during startup when the DB is busy
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if _, err := rawDB.ExecContext(ctx, fmt.Sprintf("PRAGMA mmap_size = %s", mmapSize)); err != nil {
				log.Printf("failed to apply PRAGMA mmap_size: %v", err)
			}
			if _, err := rawDB.ExecContext(ctx, fmt.Sprintf("PRAGMA page_size = %s", pageSize)); err != nil {
				log.Printf("failed to apply PRAGMA page_size: %v", err)
			}
			if _, err := rawDB.ExecContext(ctx, fmt.Sprintf("PRAGMA journal_size_limit = %s", journalSizeLimit)); err != nil {
				log.Printf("failed to apply PRAGMA journal_size_limit: %v", err)
			}
			if _, err := rawDB.ExecContext(ctx, fmt.Sprintf("PRAGMA wal_autocheckpoint = %s", walAutocheckpoint)); err != nil {
				log.Printf("failed to apply PRAGMA wal_autocheckpoint: %v", err)
			}
			rawDB.Close()
		}
		// cache_size and temp_store are already set in the DSN above; avoid reapplying via PRAGMA to prevent duplicate work and noisy logs

		// Create a separate read-only connection pool for SQLite
		// This allows concurrent reads while writes are happening (WAL mode benefit)
		ReadDB, err = createSQLiteReadDB(dsn, newLogger)
		if err != nil {
			logrus.WithError(err).Warn("Failed to create SQLite read connection pool, using main DB for reads")
			ReadDB = DB
		}
	}

	return DB, nil
}

// createSQLiteReadDB creates a separate read-only connection pool for SQLite.
// In WAL mode, readers don't block writers and vice versa, but only if they use separate connections.
func createSQLiteReadDB(dsn string, newLogger logger.Interface) (*gorm.DB, error) {
	cacheSize := utils.GetEnvOrDefault("SQLITE_CACHE_SIZE", "10000")
	tempStore := utils.GetEnvOrDefault("SQLITE_TEMP_STORE", "MEMORY")
	// Separate connection pool for reads, without cache=shared to avoid lock contention
	// Use shorter busy_timeout for read connections to fail fast on contention
	params := fmt.Sprintf("_pragma=foreign_keys(1)&_busy_timeout=1000&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=%s&_temp_store=%s", cacheSize, tempStore)
	delimiter := "?"
	if strings.Contains(dsn, "?") {
		delimiter = "&"
	}
	dialector := sqlite.Open(dsn + delimiter + params)

	// Disable PrepareStmt for read DB to speed up shutdown
	// PrepareStmt caches prepared statements which can delay Close()
	readDB, err := gorm.Open(dialector, &gorm.Config{
		Logger:      newLogger,
		PrepareStmt: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite read connection: %w", err)
	}

	sqlDB, err := readDB.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB for read connection: %w", err)
	}

	// Allow multiple read connections for concurrent reads
	// Use shorter lifetime to allow faster connection turnover during shutdown
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	sqlDB.SetConnMaxIdleTime(1 * time.Minute)

	logrus.Info("SQLite read-only connection pool created for concurrent reads")
	return readDB, nil
}
