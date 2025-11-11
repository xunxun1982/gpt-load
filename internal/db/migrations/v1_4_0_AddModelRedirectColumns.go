package db

import (
	"gorm.io/gorm"
)

// V1_4_0_AddModelRedirectColumns adds model_redirect_rules and model_redirect_strict columns to groups table
func V1_4_0_AddModelRedirectColumns(db *gorm.DB) error {
	var columnExists bool
	switch db.Dialector.Name() {
	case "mysql":
		if err := db.Raw(`
			SELECT COUNT(*) > 0
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = 'groups'
			AND COLUMN_NAME = 'model_redirect_rules'
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	case "postgres":
		if err := db.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM information_schema.columns
				WHERE table_schema = current_schema()
				AND table_name = 'groups'
				AND column_name = 'model_redirect_rules'
			)
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	default:
		if err := db.Raw(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('groups')
			WHERE name = 'model_redirect_rules'
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	}

	if !columnExists {
		if err := db.Exec("ALTER TABLE groups ADD COLUMN model_redirect_rules JSON").Error; err != nil {
			return err
		}
	}

	columnExists = false
	switch db.Dialector.Name() {
	case "mysql":
		if err := db.Raw(`
			SELECT COUNT(*) > 0
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = 'groups'
			AND COLUMN_NAME = 'model_redirect_strict'
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	case "postgres":
		if err := db.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM information_schema.columns
				WHERE table_schema = current_schema()
				AND table_name = 'groups'
				AND column_name = 'model_redirect_strict'
			)
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	default:
		if err := db.Raw(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('groups')
			WHERE name = 'model_redirect_strict'
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	}

	if !columnExists {
		if err := db.Exec("ALTER TABLE groups ADD COLUMN model_redirect_strict BOOLEAN DEFAULT FALSE").Error; err != nil {
			return err
		}
	}

	return nil
}
