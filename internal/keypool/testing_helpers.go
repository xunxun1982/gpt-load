package keypool

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// skipIfNoCGO skips the test if SQLite driver is not available
// Note: glebarez/sqlite is a pure Go implementation and doesn't require CGO
func skipIfNoCGO(tb testing.TB) {
	tb.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		// Check if error indicates SQLite is not available
		if strings.Contains(err.Error(), "CGO_ENABLED=0") ||
		   strings.Contains(err.Error(), "requires cgo") ||
		   strings.Contains(err.Error(), "stub") ||
		   strings.Contains(err.Error(), "not available") {
			tb.Skip("Skipping test: SQLite driver is not available")
		}
		// Fail for unexpected errors
		tb.Fatalf("Failed to open in-memory SQLite database: %v", err)
	}
	// Try a simple query to verify SQLite actually works
	if db != nil {
		err = db.Exec("SELECT 1").Error
		if err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "CGO_ENABLED=0") ||
			   strings.Contains(errMsg, "requires cgo") ||
			   strings.Contains(errMsg, "stub") ||
			   strings.Contains(errMsg, "not available") {
				tb.Skip("Skipping test: SQLite driver is not available")
			}
		}
		// Close the test connection
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}
}
