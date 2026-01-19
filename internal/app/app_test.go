package app

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// skipIfNoCGO skips the test if SQLite driver is not available
// Note: glebarez/sqlite is a pure Go implementation and doesn't require CGO
func skipIfNoCGO(t *testing.T) {
	t.Helper()
	// Try to create a SQLite database to check if driver is available
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		if strings.Contains(err.Error(), "CGO_ENABLED=0") ||
		   strings.Contains(err.Error(), "requires cgo") ||
		   strings.Contains(err.Error(), "stub") ||
		   strings.Contains(err.Error(), "not available") {
			t.Skip("Skipping test: SQLite driver is not available")
		}
		t.Fatalf("Failed to open in-memory SQLite database: %v", err)
	}
	// Close the test connection
	if db != nil {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}
}

func TestCloseDBConnection_NilDB(t *testing.T) {
	// Should handle nil DB gracefully
	closeDBConnection(nil, "test")
}

func TestCloseDBConnection_ValidDB(t *testing.T) {
	skipIfNoCGO(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(t, err)

	// Close should complete without error
	done := make(chan struct{})
	go func() {
		closeDBConnection(db, "test")
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("closeDBConnection timed out")
	}
}

func TestCloseDBConnectionWithOptions_SkipCheckpoint(t *testing.T) {
	skipIfNoCGO(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(t, err)

	// Close with skip checkpoint
	done := make(chan struct{})
	go func() {
		closeDBConnectionWithOptions(db, "test", false)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("closeDBConnectionWithOptions timed out")
	}
}

func TestCloseDBConnectionWithOptions_WithCheckpoint(t *testing.T) {
	skipIfNoCGO(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(t, err)

	// Close with checkpoint
	done := make(chan struct{})
	go func() {
		closeDBConnectionWithOptions(db, "test", true)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("closeDBConnectionWithOptions with checkpoint timed out")
	}
}

func TestCloseDBConnection_ConnectionPoolStats(t *testing.T) {
	skipIfNoCGO(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(t, err)

	// Get sql.DB to verify pool stats
	sqlDB, err := db.DB()
	assert.NoError(t, err)

	// Set pool configuration
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// Close connection
	closeDBConnection(db, "test")

	// Verify connection pool was cleaned up
	stats := sqlDB.Stats()
	assert.Equal(t, 0, stats.OpenConnections)
}

func TestCloseDBConnection_PreparedStatements(t *testing.T) {
	skipIfNoCGO(t)

	// Create in-memory SQLite database with prepared statement cache
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger:      logger.Default.LogMode(logger.Silent),
		PrepareStmt: true,
	})
	assert.NoError(t, err)

	// Execute some queries to populate prepared statement cache
	type TestModel struct {
		ID   uint
		Name string
	}
	db.AutoMigrate(&TestModel{})
	db.Create(&TestModel{Name: "test"})
	db.Find(&TestModel{})

	// Close connection (should close prepared statements)
	closeDBConnection(db, "test")
}

func TestCloseDBConnection_WALCheckpoint(t *testing.T) {
	skipIfNoCGO(t)

	// Create temporary file-based SQLite database with WAL mode
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(t, err)

	// Enable WAL mode
	sqlDB, err := db.DB()
	assert.NoError(t, err)
	_, err = sqlDB.Exec("PRAGMA journal_mode=WAL")
	assert.NoError(t, err)

	// Create some data to generate WAL entries
	type TestModel struct {
		ID   uint
		Name string
	}
	db.AutoMigrate(&TestModel{})
	for i := 0; i < 100; i++ {
		db.Create(&TestModel{Name: "test"})
	}

	// Close connection (should execute WAL checkpoint)
	closeDBConnection(db, "test")
}

func TestCloseDBConnection_Timeout(t *testing.T) {
	skipIfNoCGO(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(t, err)

	// Close should complete within timeout
	done := make(chan struct{})
	go func() {
		closeDBConnection(db, "test")
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(3 * time.Second):
		t.Fatal("closeDBConnection exceeded expected timeout")
	}
}

func TestCloseDBConnection_MultipleClose(t *testing.T) {
	skipIfNoCGO(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(t, err)

	// First close
	closeDBConnection(db, "test")

	// Second close should not panic
	closeDBConnection(db, "test")
}

// Benchmark tests for PGO optimization
func BenchmarkCloseDBConnection(b *testing.B) {
	// Skip if CGO is not available
	if runtime.Compiler == "gc" {
		_, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			b.Skip("Skipping benchmark: CGO is not enabled")
		}
	}

	for i := 0; i < b.N; i++ {
		db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		closeDBConnection(db, "bench")
	}
}

func BenchmarkCloseDBConnectionWithCheckpoint(b *testing.B) {
	// Skip if CGO is not available
	if runtime.Compiler == "gc" {
		_, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			b.Skip("Skipping benchmark: CGO is not enabled")
		}
	}

	for i := 0; i < b.N; i++ {
		db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})

		// Enable WAL mode
		sqlDB, _ := db.DB()
		sqlDB.Exec("PRAGMA journal_mode=WAL")

		closeDBConnectionWithOptions(db, "bench", true)
	}
}

func BenchmarkCloseDBConnectionWithPreparedStmts(b *testing.B) {
	// Skip if CGO is not available
	if runtime.Compiler == "gc" {
		_, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			b.Skip("Skipping benchmark: CGO is not enabled")
		}
	}

	for i := 0; i < b.N; i++ {
		db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger:      logger.Default.LogMode(logger.Silent),
			PrepareStmt: true,
		})

		// Execute some queries
		type TestModel struct {
			ID   uint
			Name string
		}
		db.AutoMigrate(&TestModel{})
		db.Create(&TestModel{Name: "test"})

		closeDBConnection(db, "bench")
	}
}
