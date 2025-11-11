package db

import (
	"gorm.io/gorm"
)

// V1_4_0_AddModelRedirectColumns adds model_redirect_rules and model_redirect_strict columns to groups table
func V1_4_0_AddModelRedirectColumns(db *gorm.DB) error {
	// Check if model_redirect_rules column exists
	var columnExists bool
	if db.Dialector.Name() == "mysql" {
		db.Raw(`
			SELECT COUNT(*) > 0
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = 'groups'
			AND COLUMN_NAME = 'model_redirect_rules'
		`).Scan(&columnExists)
	} else {
		db.Raw(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('groups')
			WHERE name = 'model_redirect_rules'
		`).Scan(&columnExists)
	}

	if !columnExists {
		if err := db.Exec("ALTER TABLE groups ADD COLUMN model_redirect_rules JSON").Error; err != nil {
			return err
		}
	}

	// Check if model_redirect_strict column exists
	if db.Dialector.Name() == "mysql" {
		db.Raw(`
			SELECT COUNT(*) > 0
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = 'groups'
			AND COLUMN_NAME = 'model_redirect_strict'
		`).Scan(&columnExists)
	} else {
		db.Raw(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('groups')
			WHERE name = 'model_redirect_strict'
		`).Scan(&columnExists)
	}

	if !columnExists {
		if err := db.Exec("ALTER TABLE groups ADD COLUMN model_redirect_strict BOOLEAN DEFAULT FALSE").Error; err != nil {
			return err
		}
	}

	return nil
}
