package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_2_0_AddModelMappingColumn adds model_mapping column to groups table
func V1_2_0_AddModelMappingColumn(db *gorm.DB) error {
	// Check if column already exists
	if db.Migrator().HasColumn("groups", "model_mapping") {
		logrus.Info("Column model_mapping already exists, skipping migration v1.2.0")
		return nil
	}

	logrus.Info("Running migration v1.2.0: Adding model_mapping column to groups table")

	// Add the column
	if err := db.Exec("ALTER TABLE groups ADD COLUMN model_mapping TEXT").Error; err != nil {
		return err
	}

	logrus.Info("Migration v1.2.0 completed successfully")
	return nil
}
