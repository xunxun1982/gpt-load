package db

import (
	"gorm.io/gorm"
)

// V1_11_0_AddModelRedirectRulesV2 adds model_redirect_rules_v2 column to groups table
// for enhanced one-to-many model redirect support with weighted random selection.
func V1_11_0_AddModelRedirectRulesV2(db *gorm.DB) error {
	var columnExists bool

	switch db.Dialector.Name() {
	case "mysql":
		if err := db.Raw(`
			SELECT COUNT(*) > 0
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = 'groups'
			AND COLUMN_NAME = 'model_redirect_rules_v2'
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
				AND column_name = 'model_redirect_rules_v2'
			)
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	default:
		// SQLite
		if err := db.Raw(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('groups')
			WHERE name = 'model_redirect_rules_v2'
		`).Scan(&columnExists).Error; err != nil {
			return err
		}
	}

	if !columnExists {
		// Use JSON type which is supported by MySQL 5.7+, PostgreSQL 9.2+, and SQLite 3.38+
		// For older SQLite versions, JSON is stored as TEXT which is compatible
		if err := db.Exec("ALTER TABLE groups ADD COLUMN model_redirect_rules_v2 JSON").Error; err != nil {
			return err
		}
	}

	return nil
}
