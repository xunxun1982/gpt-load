package keypool

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// skipIfNoSQLite skips the test if SQLite driver is not available
// Note: glebarez/sqlite is a pure Go implementation and doesn't require CGO
func skipIfNoSQLite(tb testing.TB) {
	tb.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		tb.Skipf("Skipping: SQLite driver unavailable: %v", err)
		return
	}
	// Close the test connection
	if db != nil {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}
}
