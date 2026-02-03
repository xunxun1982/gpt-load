package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_23_0_UpdateHealthThresholdDefault updates the default health threshold from 0.5 to 0.3.
// This migration aligns the Hub health threshold with the optimized dynamic weight configuration
// for unstable channels that may experience intermittent failures (5-6 consecutive failures).
//
// Migration strategy:
// - Only update records with health_threshold = 0.5 (the old default value)
// - Preserve user-customized values (anything other than 0.5)
// - New installations will use 0.3 as the default from code
//
// Rationale for 0.3:
// - Aligns with dynamic weight's critical threshold (0.25)
// - More tolerant of unstable channels while still filtering severely degraded ones
// - Balances between availability and quality control
func V1_23_0_UpdateHealthThresholdDefault(db *gorm.DB) error {
	logrus.Info("Starting migration: Update health threshold default (0.5→0.3)")

	// Check if hub_settings table exists
	if !db.Migrator().HasTable("hub_settings") {
		logrus.Info("Table hub_settings does not exist, skipping health threshold update")
		return nil
	}

	// Update only records with the old default value (0.5)
	// This preserves any user-customized values
	result := db.Exec(`
		UPDATE hub_settings
		SET health_threshold = 0.3
		WHERE health_threshold = 0.5
	`)

	if result.Error != nil {
		logrus.WithError(result.Error).Error("Failed to update health threshold default in hub_settings")
		return result.Error
	}

	if result.RowsAffected > 0 {
		logrus.WithField("rows_affected", result.RowsAffected).Info("Updated health threshold default: changed 0.5 to 0.3")
	} else {
		logrus.Info("No records with health_threshold=0.5 found, skipping update")
	}

	return nil
}
