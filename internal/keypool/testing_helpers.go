package keypool

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// skipIfNoCGO skips the test if CGO is not enabled
func skipIfNoCGO(t *testing.T) {
	_, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil && err.Error() == "Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work. This is a stub" {
		t.Skip("Skipping test: CGO is not enabled (required for SQLite)")
	}
}
