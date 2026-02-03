package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_22_0_UpdatePrioritySemantics updates priority semantics from 0=disabled to 1000=disabled.
// This migration changes the priority value system to be more consistent:
// - Old: 0=disabled, 1-999=priority (lower=higher)
// - New: 1-999=priority (lower=higher), 1000=disabled
//
// Migration steps:
// 1. Update all priority=0 records to priority=1000
// 2. This ensures disabled groups are represented by the maximum value (1000)
//    which is consistent with "lower value = higher priority" semantics
func V1_22_0_UpdatePrioritySemantics(db *gorm.DB) error {
	logrus.Info("Starting migration: Update priority semantics (0→1000 for disabled)")

	// Check if table exists to avoid migration failures on partial installs
	if !db.Migrator().HasTable("hub_model_group_priorities") {
		logrus.Info("Table hub_model_group_priorities does not exist, skipping priority semantics update")
		return nil
	}

	// Update hub_model_group_priorities table
	// Change all priority=0 to priority=1000
	result := db.Exec(`
		UPDATE hub_model_group_priorities
		SET priority = 1000
		WHERE priority = 0
	`)

	if result.Error != nil {
		logrus.WithError(result.Error).Error("Failed to update priority semantics in hub_model_group_priorities")
		return result.Error
	}

	logrus.WithField("rows_affected", result.RowsAffected).Info("Updated priority semantics: changed priority=0 to priority=1000")

	return nil
}
