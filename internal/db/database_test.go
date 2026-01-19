package db

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"gpt-load/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConfigManager implements types.ConfigManager for testing
type mockConfigManager struct {
	dsn           string
	logLevel      string
	encryptionKey string
}

func (m *mockConfigManager) IsMaster() bool {
	return true
}

func (m *mockConfigManager) GetAuthConfig() types.AuthConfig {
	return types.AuthConfig{Key: "test-key"}
}

func (m *mockConfigManager) GetCORSConfig() types.CORSConfig {
	return types.CORSConfig{}
}

func (m *mockConfigManager) GetPerformanceConfig() types.PerformanceConfig {
	return types.PerformanceConfig{MaxConcurrentRequests: 100}
}

func (m *mockConfigManager) GetLogConfig() types.LogConfig {
	return types.LogConfig{Level: m.logLevel}
}

func (m *mockConfigManager) GetRedisDSN() string {
	return ""
}

func (m *mockConfigManager) GetDatabaseConfig() types.DatabaseConfig {
	return types.DatabaseConfig{DSN: m.dsn}
}

func (m *mockConfigManager) GetEncryptionKey() string {
	return m.encryptionKey
}

func (m *mockConfigManager) IsDebugMode() bool {
	return false
}

func (m *mockConfigManager) GetEffectiveServerConfig() types.ServerConfig {
	return types.ServerConfig{}
}

func (m *mockConfigManager) ReloadConfig() error {
	return nil
}

func (m *mockConfigManager) Validate() error {
	return nil
}

func (m *mockConfigManager) DisplayServerConfig() {}

// TestNewDB_SQLite tests SQLite database connection
func TestNewDB_SQLite(t *testing.T) {
	tempFile := t.TempDir() + "/test.db"

	config := &mockConfigManager{
		dsn:      tempFile,
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	// Verify connection
	sqlDB, err := db.DB()
	require.NoError(t, err)
	err = sqlDB.Ping()
	require.NoError(t, err)

	// Verify ReadDB is created
	assert.NotNil(t, ReadDB)

	// Cleanup - close both main and read connections
	if ReadDB != nil {
		readSQLDB, _ := ReadDB.DB()
		if readSQLDB != nil {
			readSQLDB.Close()
		}
	}
	sqlDB.Close()
}

// TestNewDB_SQLiteMemory tests in-memory SQLite database
func TestNewDB_SQLiteMemory(t *testing.T) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	err = sqlDB.Ping()
	require.NoError(t, err)
}

// TestNewDB_SQLiteFileURI tests SQLite file URI format
func TestNewDB_SQLiteFileURI(t *testing.T) {
	tempFile := t.TempDir() + "/test.db"

	config := &mockConfigManager{
		dsn:      fmt.Sprintf("file:%s", tempFile),
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() {
		if ReadDB != nil {
			readSQLDB, _ := ReadDB.DB()
			if readSQLDB != nil {
				readSQLDB.Close()
			}
		}
		sqlDB.Close()
	}()

	err = sqlDB.Ping()
	require.NoError(t, err)
}

// TestNewDB_EmptyDSN tests error handling for empty DSN
func TestNewDB_EmptyDSN(t *testing.T) {
	config := &mockConfigManager{
		dsn:      "",
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.Error(t, err)
	assert.Nil(t, db)
	assert.Contains(t, err.Error(), "DATABASE_DSN is not configured")
}

// TestNewDB_DebugMode tests database creation with debug logging
func TestNewDB_DebugMode(t *testing.T) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "debug",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
}

// TestCalculateAdaptivePoolSize tests adaptive pool size calculation
func TestCalculateAdaptivePoolSize(t *testing.T) {
	maxIdle, maxOpen := calculateAdaptivePoolSize()

	// Verify constraints
	assert.GreaterOrEqual(t, maxOpen, 4, "maxOpen should be at least 4")
	assert.LessOrEqual(t, maxOpen, 32, "maxOpen should be at most 32")
	assert.GreaterOrEqual(t, maxIdle, 2, "maxIdle should be at least 2")
	assert.Equal(t, maxOpen/2, maxIdle, "maxIdle should be half of maxOpen")

	// Verify scaling with CPU cores
	numCPU := runtime.NumCPU()
	expectedMaxOpen := numCPU * 2
	if expectedMaxOpen < 4 {
		expectedMaxOpen = 4
	}
	if expectedMaxOpen > 32 {
		expectedMaxOpen = 32
	}
	assert.Equal(t, expectedMaxOpen, maxOpen)
}

// TestCreateSQLiteReadDB tests read-only connection pool creation
func TestCreateSQLiteReadDB(t *testing.T) {
	tempFile := t.TempDir() + "/test.db"

	// First create main DB
	config := &mockConfigManager{
		dsn:      tempFile,
		logLevel: "info",
	}
	mainDB, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, mainDB)

	// Verify ReadDB was created
	assert.NotNil(t, ReadDB)
	assert.NotEqual(t, mainDB, ReadDB, "ReadDB should be a separate connection")

	// Verify ReadDB works
	sqlDB, err := ReadDB.DB()
	require.NoError(t, err)
	err = sqlDB.Ping()
	require.NoError(t, err)

	// Cleanup
	mainSQLDB, _ := mainDB.DB()
	mainSQLDB.Close()
	sqlDB.Close()
}

// TestNewDB_WithEnvironmentVariables tests database with custom environment variables
func TestNewDB_WithEnvironmentVariables(t *testing.T) {
	// Set custom environment variables
	os.Setenv("SQLITE_CACHE_SIZE", "20000")
	os.Setenv("SQLITE_TEMP_STORE", "MEMORY")
	os.Setenv("SQLITE_MMAP_SIZE", "40000000000")
	os.Setenv("SQLITE_READ_MAX_IDLE_CONNS", "8")
	os.Setenv("SQLITE_READ_MAX_OPEN_CONNS", "16")
	defer func() {
		os.Unsetenv("SQLITE_CACHE_SIZE")
		os.Unsetenv("SQLITE_TEMP_STORE")
		os.Unsetenv("SQLITE_MMAP_SIZE")
		os.Unsetenv("SQLITE_READ_MAX_IDLE_CONNS")
		os.Unsetenv("SQLITE_READ_MAX_OPEN_CONNS")
	}()

	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	err = sqlDB.Ping()
	require.NoError(t, err)
}

// BenchmarkNewDB_SQLite benchmarks SQLite database creation
func BenchmarkNewDB_SQLite(b *testing.B) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, err := NewDB(config)
		if err != nil {
			b.Fatal(err)
		}
		sqlDB, _ := db.DB()
		sqlDB.Close()
	}
}

// BenchmarkCalculateAdaptivePoolSize benchmarks pool size calculation
func BenchmarkCalculateAdaptivePoolSize(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = calculateAdaptivePoolSize()
	}
}

// BenchmarkDBQuery benchmarks database query performance
func BenchmarkDBQuery(b *testing.B) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		sqlDB, _ := db.DB()
		sqlDB.Close()
	}()

	// Create a test table
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	db.Exec("INSERT INTO test (name) VALUES ('test')")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var count int64
		db.Raw("SELECT COUNT(*) FROM test").Scan(&count)
	}
}

// BenchmarkDBInsert benchmarks database insert performance
func BenchmarkDBInsert(b *testing.B) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		sqlDB, _ := db.DB()
		sqlDB.Close()
	}()

	// Create a test table
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Exec("INSERT INTO test (name) VALUES (?)", fmt.Sprintf("test-%d", i))
	}
}

// TestNewDB_WithPragmaSettings tests database with custom PRAGMA settings
func TestNewDB_WithPragmaSettings(t *testing.T) {
	os.Setenv("SQLITE_MMAP_SIZE", "20000000000")
	os.Setenv("SQLITE_PAGE_SIZE", "4096")
	os.Setenv("SQLITE_JOURNAL_SIZE_LIMIT", "50000000")
	os.Setenv("SQLITE_WAL_AUTOCHECKPOINT", "500")
	defer func() {
		os.Unsetenv("SQLITE_MMAP_SIZE")
		os.Unsetenv("SQLITE_PAGE_SIZE")
		os.Unsetenv("SQLITE_JOURNAL_SIZE_LIMIT")
		os.Unsetenv("SQLITE_WAL_AUTOCHECKPOINT")
	}()

	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	err = sqlDB.Ping()
	require.NoError(t, err)
}

// TestNewDB_WithReadPool tests database with read pool creation
func TestNewDB_WithReadPool(t *testing.T) {
	tempFile := t.TempDir() + "/test.db"

	config := &mockConfigManager{
		dsn:      tempFile,
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	// Verify ReadDB is created and different from main DB
	assert.NotNil(t, ReadDB)
	assert.NotEqual(t, db, ReadDB)

	// Verify both connections work
	mainSQLDB, err := db.DB()
	require.NoError(t, err)
	err = mainSQLDB.Ping()
	require.NoError(t, err)

	readSQLDB, err := ReadDB.DB()
	require.NoError(t, err)
	err = readSQLDB.Ping()
	require.NoError(t, err)

	// Cleanup
	mainSQLDB.Close()
	readSQLDB.Close()
}

// TestNewDB_WithCustomReadPoolSize tests database with custom read pool sizes
func TestNewDB_WithCustomReadPoolSize(t *testing.T) {
	os.Setenv("SQLITE_READ_MAX_IDLE_CONNS", "4")
	os.Setenv("SQLITE_READ_MAX_OPEN_CONNS", "8")
	defer func() {
		os.Unsetenv("SQLITE_READ_MAX_IDLE_CONNS")
		os.Unsetenv("SQLITE_READ_MAX_OPEN_CONNS")
	}()

	tempFile := t.TempDir() + "/test.db"

	config := &mockConfigManager{
		dsn:      tempFile,
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	assert.NotNil(t, ReadDB)

	// Cleanup
	mainSQLDB, _ := db.DB()
	readSQLDB, _ := ReadDB.DB()
	mainSQLDB.Close()
	readSQLDB.Close()
}

// TestNewDB_WithQueryParams tests database with query parameters in DSN
func TestNewDB_WithQueryParams(t *testing.T) {
	tempFile := t.TempDir() + "/test.db"
	dsn := tempFile + "?mode=rwc"

	config := &mockConfigManager{
		dsn:      dsn,
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() {
		if ReadDB != nil {
			readSQLDB, _ := ReadDB.DB()
			if readSQLDB != nil {
				readSQLDB.Close()
			}
		}
		sqlDB.Close()
	}()

	err = sqlDB.Ping()
	require.NoError(t, err)
}

// TestNewDB_WithDebugLogging tests database creation with debug logging enabled
func TestNewDB_WithDebugLogging(t *testing.T) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "debug",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	// Verify logger is configured
	assert.NotNil(t, db.Logger)
}

// TestNewDB_WithInfoLogging tests database creation with info logging
func TestNewDB_WithInfoLogging(t *testing.T) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
}

// TestNewDB_WithDirectoryCreation tests database directory creation
func TestNewDB_WithDirectoryCreation(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := tempDir + "/subdir/test.db"

	config := &mockConfigManager{
		dsn:      dbPath,
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	// Verify directory was created
	assert.DirExists(t, tempDir+"/subdir")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() {
		if ReadDB != nil {
			readSQLDB, _ := ReadDB.DB()
			if readSQLDB != nil {
				readSQLDB.Close()
			}
		}
		sqlDB.Close()
	}()
}

// TestNewDB_WithConcurrentReads tests concurrent read operations
func TestNewDB_WithConcurrentReads(t *testing.T) {
	tempFile := t.TempDir() + "/test.db"

	config := &mockConfigManager{
		dsn:      tempFile,
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	// Create test table
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	db.Exec("INSERT INTO test (value) VALUES ('test1')")
	db.Exec("INSERT INTO test (value) VALUES ('test2')")

	// Concurrent reads
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			var count int64
			ReadDB.Raw("SELECT COUNT(*) FROM test").Scan(&count)
			assert.Greater(t, count, int64(0))
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// Cleanup
	mainSQLDB, _ := db.DB()
	readSQLDB, _ := ReadDB.DB()
	mainSQLDB.Close()
	readSQLDB.Close()
}

// TestNewDB_WithPrepareStmt tests database with prepared statements
func TestNewDB_WithPrepareStmt(t *testing.T) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	// Create table and insert data
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	db.Exec("INSERT INTO test (name) VALUES (?)", "test1")
	db.Exec("INSERT INTO test (name) VALUES (?)", "test2")

	// Query with prepared statement
	var count int64
	db.Raw("SELECT COUNT(*) FROM test").Scan(&count)
	assert.Equal(t, int64(2), count)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
}

// TestNewDB_WithForeignKeys tests database with foreign key constraints
func TestNewDB_WithForeignKeys(t *testing.T) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)

	// Create tables with foreign key relationship
	db.Exec("CREATE TABLE parent (id INTEGER PRIMARY KEY)")
	db.Exec("CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES parent(id))")

	// Insert parent
	db.Exec("INSERT INTO parent (id) VALUES (1)")

	// Insert child with valid parent
	result := db.Exec("INSERT INTO child (parent_id) VALUES (1)")
	assert.NoError(t, result.Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
}

// BenchmarkNewDB_WithReadPool benchmarks database creation with read pool
func BenchmarkNewDB_WithReadPool(b *testing.B) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, err := NewDB(config)
		if err != nil {
			b.Fatal(err)
		}
		sqlDB, _ := db.DB()
		sqlDB.Close()
		if ReadDB != nil {
			readSQLDB, _ := ReadDB.DB()
			readSQLDB.Close()
		}
	}
}

// BenchmarkConcurrentReads benchmarks concurrent read operations
func BenchmarkConcurrentReads(b *testing.B) {
	config := &mockConfigManager{
		dsn:      ":memory:",
		logLevel: "info",
	}

	db, err := NewDB(config)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		sqlDB, _ := db.DB()
		sqlDB.Close()
		if ReadDB != nil {
			readSQLDB, _ := ReadDB.DB()
			readSQLDB.Close()
		}
	}()

	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	db.Exec("INSERT INTO test (value) VALUES ('test')")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var count int64
			ReadDB.Raw("SELECT COUNT(*) FROM test").Scan(&count)
		}
	})
}
