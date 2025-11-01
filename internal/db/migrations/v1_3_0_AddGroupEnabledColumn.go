package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_3_0_AddGroupEnabledColumn adds enabled column to groups table
func V1_3_0_AddGroupEnabledColumn(db *gorm.DB) error {
	logrus.Info("Running migration v1.3.0: Add enabled column to groups")

	// Check if column already exists
	if db.Migrator().HasColumn(&GroupV130{}, "enabled") {
		logrus.Info("Column enabled already exists, skipping migration v1.3.0")
		return nil
	}

	// Add the column with default value true
	if err := db.Migrator().AddColumn(&GroupV130{}, "enabled"); err != nil {
		logrus.WithError(err).Error("Failed to add enabled column")
		return err
	}

	// Set default value for existing rows
	if err := db.Exec("UPDATE groups SET enabled = ? WHERE enabled IS NULL", true).Error; err != nil {
		logrus.WithError(err).Error("Failed to set default value for enabled column")
		return err
	}

	logrus.Info("Successfully added enabled column to groups table")
	return nil
}

// GroupV130 is a minimal struct for migration purposes
type GroupV130 struct {
	Enabled bool `gorm:"default:true;not null"`
}

// TableName specifies the table name for GORM
func (GroupV130) TableName() string {
	return "groups"
}
