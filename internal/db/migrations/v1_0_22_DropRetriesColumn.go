package db

import "gorm.io/gorm"

// RequestLog is a temporary struct for migration.
type RequestLog struct {
	Retries int `gorm:"column:retries"`
}

// V1_0_22_DropRetriesColumn drops the retries column from the request_logs table.
func V1_0_22_DropRetriesColumn(db *gorm.DB) error {
	// Check if retries column exists
	if db.Migrator().HasColumn(&RequestLog{}, "retries") {
		// Drop retries column
		if err := db.Migrator().DropColumn(&RequestLog{}, "retries"); err != nil {
			return err
		}
	}
	return nil
}
