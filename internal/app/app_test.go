package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// skipIfNoSQLite skips the test if SQLite driver is not available
// Note: glebarez/sqlite is a pure Go implementation and doesn't require CGO
// Note: This helper is duplicated in internal/keypool/testing_helpers.go to avoid
// cross-package test dependencies (app package should not depend on keypool package)
func skipIfNoSQLite(tb testing.TB) {
	tb.Helper()
	// Try to create a SQLite database to check if driver is available
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		tb.Skipf("Skipping: SQLite driver unavailable: %v", err)
		return
	}
	// Close the test connection
	if db != nil {
		sqlDB, err := db.DB()
		if err != nil {
			return // Driver check passed, cleanup failure is acceptable
		}
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
	skipIfNoSQLite(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

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
	skipIfNoSQLite(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

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
	skipIfNoSQLite(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

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
	skipIfNoSQLite(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Get sql.DB to verify pool stats
	sqlDB, err := db.DB()
	require.NoError(t, err)

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
	skipIfNoSQLite(t)

	// Create in-memory SQLite database with prepared statement cache
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger:      logger.Default.LogMode(logger.Silent),
		PrepareStmt: true,
	})
	require.NoError(t, err)

	// Execute some queries to populate prepared statement cache
	type TestModel struct {
		ID   uint
		Name string
	}
	err = db.AutoMigrate(&TestModel{})
	require.NoError(t, err)
	err = db.Create(&TestModel{Name: "test"}).Error
	require.NoError(t, err)
	err = db.Find(&TestModel{}).Error
	require.NoError(t, err)

	// Close connection (should close prepared statements)
	closeDBConnection(db, "test")
}

func TestCloseDBConnection_WALCheckpoint(t *testing.T) {
	skipIfNoSQLite(t)

	// Create temporary file-based SQLite database with WAL mode
	// Note: WAL mode requires a real file, not in-memory database
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Enable WAL mode
	sqlDB, err := db.DB()
	require.NoError(t, err)
	_, err = sqlDB.Exec("PRAGMA journal_mode=WAL")
	require.NoError(t, err)

	// Create some data to generate WAL entries
	type TestModel struct {
		ID   uint
		Name string
	}
	err = db.AutoMigrate(&TestModel{})
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		err = db.Create(&TestModel{Name: "test"}).Error
		require.NoError(t, err)
	}

	// Close connection (should execute WAL checkpoint)
	closeDBConnection(db, "test")
}

func TestCloseDBConnection_Timeout(t *testing.T) {
	skipIfNoSQLite(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

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
	skipIfNoSQLite(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// First close
	closeDBConnection(db, "test")

	// Second close should not panic
	closeDBConnection(db, "test")
}

// Benchmark tests for PGO optimization
func BenchmarkCloseDBConnection(b *testing.B) {
	for i := 0; i < b.N; i++ {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			b.Fatal(err)
		}
		closeDBConnection(db, "bench")
	}
}

func BenchmarkCloseDBConnectionWithCheckpoint(b *testing.B) {
	for i := 0; i < b.N; i++ {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			b.Fatal(err)
		}

		// Enable WAL mode
		sqlDB, err := db.DB()
		if err != nil {
			b.Fatal(err)
		}
		if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
			b.Fatal(err)
		}

		closeDBConnectionWithOptions(db, "bench", true)
	}
}

// benchTestModel is used in benchmark tests to avoid redefining the struct in loops
type benchTestModel struct {
	ID   uint
	Name string
}

func BenchmarkCloseDBConnectionWithPreparedStmts(b *testing.B) {
	for i := 0; i < b.N; i++ {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger:      logger.Default.LogMode(logger.Silent),
			PrepareStmt: true,
		})
		if err != nil {
			b.Fatal(err)
		}

		// Execute some queries
		err = db.AutoMigrate(&benchTestModel{})
		if err != nil {
			b.Fatal(err)
		}
		err = db.Create(&benchTestModel{Name: "test"}).Error
		if err != nil {
			b.Fatal(err)
		}

		closeDBConnection(db, "bench")
	}
}
