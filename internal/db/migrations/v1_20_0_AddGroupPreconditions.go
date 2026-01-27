package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// V1_20_0_AddGroupPreconditions adds preconditions column to groups table.
// Preconditions define requirements that must be met before a request can enter an aggregate group.
// Initial support: max_request_size_kb (default: no limit unless explicitly set)
//
// Changes:
// 1. Add preconditions column to groups table (JSON type)
// 2. Initialize with empty JSON object for existing groups
func V1_20_0_AddGroupPreconditions(db *gorm.DB) error {
	migrator := db.Migrator()

	// Add preconditions column to groups table
	if migrator.HasTable("groups") {
		if !migrator.HasColumn(&groupV1_20_0{}, "preconditions") {
			if err := migrator.AddColumn(&groupV1_20_0{}, "preconditions"); err != nil {
				logrus.WithError(err).Error("Failed to add preconditions column to groups")
				return err
			}
			logrus.Info("Added preconditions column to groups table")

			// Initialize preconditions to empty JSON object for existing groups
			// This ensures backward compatibility - groups without preconditions will have no restrictions
			if err := db.Exec("UPDATE groups SET preconditions = ? WHERE preconditions IS NULL", datatypes.JSON("{}")).Error; err != nil {
				logrus.WithError(err).Warn("Failed to initialize preconditions, continuing anyway")
			} else {
				logrus.Info("Initialized preconditions to empty object for existing groups")
			}
		} else {
			logrus.Info("Column preconditions already exists in groups, skipping")
		}
	} else {
		logrus.Info("Table groups does not exist, skipping preconditions migration")
	}

	return nil
}

// groupV1_20_0 is a minimal struct for migration purposes
type groupV1_20_0 struct {
	ID            uint           `gorm:"primaryKey"`
	Preconditions datatypes.JSON `gorm:"column:preconditions;type:json"`
}

func (groupV1_20_0) TableName() string {
	return "groups"
}
