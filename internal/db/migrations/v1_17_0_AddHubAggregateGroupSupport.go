package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// V1_17_0_AddHubAggregateGroupSupport adds support for Hub aggregate group filtering
// and custom model names for aggregate groups.
//
// Changes:
// 1. Add only_aggregate_groups column to hub_settings table
// 2. Add custom_model_names column to groups table
func V1_17_0_AddHubAggregateGroupSupport(db *gorm.DB) error {
	migrator := db.Migrator()

	// Add only_aggregate_groups column to hub_settings table
	if migrator.HasTable("hub_settings") {
		if !migrator.HasColumn(&hubSettingsV1_17_0{}, "only_aggregate_groups") {
			if err := migrator.AddColumn(&hubSettingsV1_17_0{}, "only_aggregate_groups"); err != nil {
				logrus.WithError(err).Error("Failed to add only_aggregate_groups column to hub_settings")
				return err
			}
			// Set default value to true for new column
			if err := db.Exec("UPDATE hub_settings SET only_aggregate_groups = ? WHERE only_aggregate_groups IS NULL", true).Error; err != nil {
				logrus.WithError(err).Warn("Failed to set default only_aggregate_groups value, continuing anyway")
			}
			logrus.Info("Added only_aggregate_groups column to hub_settings table")
		} else {
			logrus.Info("Column only_aggregate_groups already exists in hub_settings, skipping")
		}
	} else {
		logrus.Info("Table hub_settings does not exist, skipping only_aggregate_groups migration")
	}

	// Add custom_model_names column to groups table
	if migrator.HasTable("groups") {
		if !migrator.HasColumn(&groupV1_17_0{}, "custom_model_names") {
			if err := migrator.AddColumn(&groupV1_17_0{}, "custom_model_names"); err != nil {
				logrus.WithError(err).Error("Failed to add custom_model_names column to groups")
				return err
			}
			logrus.Info("Added custom_model_names column to groups table")

			// Initialize custom_model_names to empty JSON array for existing groups
			if err := db.Exec("UPDATE groups SET custom_model_names = ? WHERE custom_model_names IS NULL", datatypes.JSON("[]")).Error; err != nil {
				logrus.WithError(err).Warn("Failed to initialize custom_model_names, continuing anyway")
			} else {
				logrus.Info("Initialized custom_model_names to empty array for existing groups")
			}
		} else {
			logrus.Info("Column custom_model_names already exists in groups, skipping")
		}
	} else {
		logrus.Info("Table groups does not exist, skipping custom_model_names migration")
	}

	return nil
}

// hubSettingsV1_17_0 is a minimal struct for migration purposes
type hubSettingsV1_17_0 struct {
	ID                  uint `gorm:"primaryKey"`
	OnlyAggregateGroups bool `gorm:"column:only_aggregate_groups;not null;default:true"`
}

func (hubSettingsV1_17_0) TableName() string {
	return "hub_settings"
}

// groupV1_17_0 is a minimal struct for migration purposes
type groupV1_17_0 struct {
	ID               uint           `gorm:"primaryKey"`
	CustomModelNames datatypes.JSON `gorm:"column:custom_model_names;type:json"`
}

func (groupV1_17_0) TableName() string {
	return "groups"
}
