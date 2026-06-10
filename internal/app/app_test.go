package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const blockingCloseDriverName = "app_blocking_close"
const failingCloseDriverName = "app_failing_close"
const persistWALDriverName = "app_persist_wal"
const blockingCloseDelay = 150 * time.Millisecond

var registerBlockingCloseDriverOnce sync.Once
var registerFailingCloseDriverOnce sync.Once
var registerPersistWALDriverOnce sync.Once
var failingCloseCalled atomic.Bool
var persistWALDriverMu sync.Mutex
var persistWALDriverConn *fakeSQLitePersistConn

func TestStartDelegatesProxyPoolNameMigrationToMigrateDatabase(t *testing.T) {
	t.Parallel()

	contentBytes, err := os.ReadFile("app.go")
	require.NoError(t, err)
	content := string(contentBytes)

	require.NotContains(t, content, "V1_27_0_AddProxyPoolNameUniqueIndex")
	autoMigrateIndex := strings.Index(content, "a.db.AutoMigrate(")
	migrateDatabaseIndex := strings.Index(content, "dbmigrations.MigrateDatabase(a.db)")
	require.NotEqual(t, -1, autoMigrateIndex)
	require.NotEqual(t, -1, migrateDatabaseIndex)
	require.Less(t, autoMigrateIndex, migrateDatabaseIndex)
}

type blockingCloseDriver struct{}

func (blockingCloseDriver) Open(_ string) (driver.Conn, error) {
	return blockingCloseConn{}, nil
}

type blockingCloseConn struct{}

func (blockingCloseConn) Prepare(_ string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (blockingCloseConn) Close() error {
	time.Sleep(blockingCloseDelay)
	return nil
}

func (blockingCloseConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (blockingCloseConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return &sqliteVersionRows{}, nil
}

type sqliteVersionRows struct {
	sent bool
}

func (sqliteVersionRows) Columns() []string {
	return []string{"sqlite_version()"}
}

func (sqliteVersionRows) Close() error {
	return nil
}

func (r *sqliteVersionRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	dest[0] = "3.45.0"
	r.sent = true
	return nil
}

type failingCloseDriver struct{}

func (failingCloseDriver) Open(_ string) (driver.Conn, error) {
	return failingCloseConn{}, nil
}

type failingCloseConn struct{}

func (failingCloseConn) Prepare(_ string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (failingCloseConn) Close() error {
	failingCloseCalled.Store(true)
	return errors.New("close failed")
}

func (failingCloseConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

type persistWALDriver struct {
}

func (d persistWALDriver) Open(_ string) (driver.Conn, error) {
	persistWALDriverMu.Lock()
	defer persistWALDriverMu.Unlock()
	if persistWALDriverConn == nil {
		return &fakeSQLitePersistConn{}, nil
	}
	return persistWALDriverConn, nil
}

type fakeSQLitePersistConn struct {
	err   error
	calls int
}

func (c *fakeSQLitePersistConn) Prepare(_ string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (c *fakeSQLitePersistConn) Close() error {
	return nil
}

func (c *fakeSQLitePersistConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (c *fakeSQLitePersistConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return &sqliteVersionRows{}, nil
}

func (c *fakeSQLitePersistConn) FileControlPersistWAL(dbName string, mode int) (int, error) {
	if dbName == "main" && mode == 1 {
		c.calls++
	}
	return mode, c.err
}

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

func TestCloseDBConnectionWithOptions_WriteConnection(t *testing.T) {
	skipIfNoSQLite(t)

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		closeDBConnectionWithOptions(db, "test", true)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("closeDBConnectionWithOptions write connection timed out")
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

func TestCloseDBConnection_WALMode(t *testing.T) {
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

	// SQLite shutdown enables persist WAL, then closes the connection pool.
	closeDBConnection(db, "test")

	reopened, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	reopenedSQLDB, err := reopened.DB()
	require.NoError(t, err)
	defer reopenedSQLDB.Close()

	var count int64
	err = reopened.Model(&TestModel{}).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(100), count)
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

func TestCloseSQLDBReturnsDriverError(t *testing.T) {
	registerFailingCloseDriverOnce.Do(func() {
		sql.Register(failingCloseDriverName, failingCloseDriver{})
	})
	failingCloseCalled.Store(false)

	sqlDB, err := sql.Open(failingCloseDriverName, "")
	require.NoError(t, err)

	conn, err := sqlDB.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	failingCloseCalled.Store(false)

	closeSQLDB(sqlDB, "failing test")
	assert.True(t, failingCloseCalled.Load())
}

func TestCloseDBConnectionClosesSQLiteDriverConnection(t *testing.T) {
	registerBlockingCloseDriverOnce.Do(func() {
		sql.Register(blockingCloseDriverName, blockingCloseDriver{})
	})

	sqlDB, err := sql.Open(blockingCloseDriverName, "")
	require.NoError(t, err)

	conn, err := sqlDB.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	gormDB, err := gorm.Open(sqlite.Dialector{
		Conn: sqlDB,
	}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	start := time.Now()
	closeDBConnectionWithOptions(gormDB, "blocking gorm test", false)
	assert.GreaterOrEqual(t, time.Since(start), blockingCloseDelay)
	assert.Equal(t, 0, sqlDB.Stats().OpenConnections)
}

func TestSetSQLitePersistWALRawController(t *testing.T) {
	conn := &fakeSQLitePersistConn{}
	persistWALDriverMu.Lock()
	persistWALDriverConn = conn
	persistWALDriverMu.Unlock()
	t.Cleanup(func() {
		persistWALDriverMu.Lock()
		persistWALDriverConn = nil
		persistWALDriverMu.Unlock()
	})

	registerPersistWALDriverOnce.Do(func() {
		sql.Register(persistWALDriverName, persistWALDriver{})
	})

	sqlDB, err := sql.Open(persistWALDriverName, "")
	require.NoError(t, err)
	defer sqlDB.Close()

	setSQLitePersistWAL(sqlDB, "persist wal test")
	assert.Equal(t, 1, conn.calls)
}

func TestCloseDBConnectionWithOptionsSkipsPersistWALForReadConnection(t *testing.T) {
	conn := &fakeSQLitePersistConn{}
	persistWALDriverMu.Lock()
	persistWALDriverConn = conn
	persistWALDriverMu.Unlock()
	t.Cleanup(func() {
		persistWALDriverMu.Lock()
		persistWALDriverConn = nil
		persistWALDriverMu.Unlock()
	})

	registerPersistWALDriverOnce.Do(func() {
		sql.Register(persistWALDriverName, persistWALDriver{})
	})

	sqlDB, err := sql.Open(persistWALDriverName, "")
	require.NoError(t, err)

	gormDB, err := gorm.Open(sqlite.Dialector{
		Conn: sqlDB,
	}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	closeDBConnectionWithOptions(gormDB, "read persist wal test", false)
	assert.Equal(t, 0, conn.calls)
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
