package db

import (
	"fmt"
	"gpt-load/internal/types"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func NewDB(configManager types.ConfigManager) (*gorm.DB, error) {
	dbConfig := configManager.GetDatabaseConfig()
	dsn := dbConfig.DSN
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_DSN is not configured")
	}

	var newLogger logger.Interface
	if configManager.GetLogConfig().Level == "debug" {
		newLogger = logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
			logger.Config{
				SlowThreshold:             time.Second, // Slow SQL threshold
				LogLevel:                  logger.Info, // Log level
				IgnoreRecordNotFoundError: true,        // Ignore ErrRecordNotFound error for logger
				Colorful:                  true,        // Disable color
			},
		)
	}

	var dialector gorm.Dialector
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		dialector = postgres.New(postgres.Config{
			DSN:                  dsn,
			PreferSimpleProtocol: true,
		})
	} else if strings.Contains(dsn, "@tcp") {
		if !strings.Contains(dsn, "parseTime") {
			if strings.Contains(dsn, "?") {
				dsn += "&parseTime=true"
			} else {
				dsn += "?parseTime=true"
			}
		}
		dialector = mysql.Open(dsn)
	} else {
		if err := os.MkdirAll(filepath.Dir(dsn), 0755); err != nil {
			return nil, fmt.Errorf("failed to create database directory: %w", err)
		}
		// Enhanced SQLite optimizations for bulk operations
		// WAL mode: Better concurrency and write performance
		// synchronous=NORMAL: Safe with WAL, faster writes
		// cache_size: Larger cache for better performance
		// temp_store=MEMORY: Use memory for temp tables
		// mmap_size: Memory mapping for faster reads
		params := "_busy_timeout=10000&_journal_mode=WAL&_synchronous=NORMAL&cache=shared&_cache_size=10000&_temp_store=MEMORY"
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
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") || strings.Contains(dsn, "@tcp") {
		// PostgreSQL and MySQL can handle more connections
		sqlDB.SetMaxIdleConns(50)
		sqlDB.SetMaxOpenConns(500)
		sqlDB.SetConnMaxLifetime(time.Hour)

		// Apply additional optimizations for MySQL
		if strings.Contains(dsn, "@tcp") {
			// Set larger timeouts for bulk operations
			sqlDB.SetConnMaxIdleTime(time.Minute * 10)
			// Apply MySQL session optimizations
			DB.Exec("SET SESSION sql_mode='TRADITIONAL'")
			DB.Exec("SET SESSION innodb_lock_wait_timeout=50")
		}
	} else {
		// SQLite needs limited connections to avoid locking issues
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetConnMaxLifetime(time.Hour)

		// Apply additional SQLite PRAGMA optimizations
		DB.Exec("PRAGMA cache_size = 10000")      // ~40MB cache with 4KB pages
		DB.Exec("PRAGMA temp_store = MEMORY")     // Use memory for temporary tables
		DB.Exec("PRAGMA mmap_size = 30000000000") // 30GB memory mapping
		DB.Exec("PRAGMA page_size = 4096")        // Optimal page size
		DB.Exec("PRAGMA journal_size_limit = 67108864") // 64MB WAL file limit
		DB.Exec("PRAGMA wal_autocheckpoint = 1000") // Checkpoint every 1000 pages
	}

	return DB, nil
}
