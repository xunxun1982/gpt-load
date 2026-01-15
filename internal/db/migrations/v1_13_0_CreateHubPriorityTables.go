package db

import (
	"gpt-load/internal/centralizedmgmt"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_13_0_CreateHubPriorityTables creates tables for Hub priority-based routing.
// - hub_model_group_priorities: Stores priority configuration for each model-group pair
// - hub_settings: Stores global Hub configuration (retry count, health threshold, etc.)
func V1_13_0_CreateHubPriorityTables(db *gorm.DB) error {
	migrator := db.Migrator()

	// Create hub_model_group_priorities table
	if !migrator.HasTable(&centralizedmgmt.HubModelGroupPriority{}) {
		if err := db.AutoMigrate(&centralizedmgmt.HubModelGroupPriority{}); err != nil {
			logrus.WithError(err).Error("Failed to create hub_model_group_priorities table")
			return err
		}
		logrus.Info("Created hub_model_group_priorities table successfully")
	} else {
		logrus.Info("Table hub_model_group_priorities already exists, skipping creation")
	}

	// Create hub_settings table
	if !migrator.HasTable(&centralizedmgmt.HubSettings{}) {
		if err := db.AutoMigrate(&centralizedmgmt.HubSettings{}); err != nil {
			logrus.WithError(err).Error("Failed to create hub_settings table")
			return err
		}
		logrus.Info("Created hub_settings table successfully")

		// Insert default settings
		defaultSettings := centralizedmgmt.HubSettings{
			MaxRetries:      3,
			RetryDelay:      100,
			HealthThreshold: 0.5,
			EnablePriority:  true,
		}
		if err := db.Create(&defaultSettings).Error; err != nil {
			logrus.WithError(err).Warn("Failed to insert default hub settings")
			// Not a fatal error, settings can be created later
		} else {
			logrus.Info("Inserted default hub settings")
		}
	} else {
		logrus.Info("Table hub_settings already exists, skipping creation")

		// Ensure default settings exist even if previous insertion failed
		var count int64
		if err := db.Model(&centralizedmgmt.HubSettings{}).Count(&count).Error; err == nil && count == 0 {
			defaultSettings := centralizedmgmt.HubSettings{
				MaxRetries:      3,
				RetryDelay:      100,
				HealthThreshold: 0.5,
				EnablePriority:  true,
			}
			if err := db.Create(&defaultSettings).Error; err != nil {
				logrus.WithError(err).Warn("Failed to insert default hub settings on retry")
			} else {
				logrus.Info("Inserted default hub settings on retry")
			}
		}
	}

	return nil
}
