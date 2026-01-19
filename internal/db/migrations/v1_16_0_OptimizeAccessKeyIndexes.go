package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_16_0_OptimizeAccessKeyIndexes adds composite indexes to hub_access_keys table
// for improved query performance on common access patterns.
//
// Indexes added:
// - idx_hub_access_keys_enabled_name: Composite index on (enabled, name) for filtered listings
// - idx_hub_access_keys_enabled_last_used: Composite index on (enabled, last_used_at) for usage analytics
//
// These indexes optimize:
// 1. Listing enabled keys sorted by name
// 2. Finding recently used enabled keys
// 3. Usage statistics queries filtered by enabled status
func V1_16_0_OptimizeAccessKeyIndexes(db *gorm.DB) error {
	migrator := db.Migrator()

	// Check if hub_access_keys table exists
	if !migrator.HasTable("hub_access_keys") {
		logrus.Info("Table hub_access_keys does not exist, skipping index optimization")
		return nil
	}

	// Add composite index on (enabled, name) for efficient filtered listings
	indexEnabledName := "idx_hub_access_keys_enabled_name"
	if !migrator.HasIndex(&hubAccessKeyIndexOptimization{}, indexEnabledName) {
		if err := db.Exec("CREATE INDEX " + indexEnabledName + " ON hub_access_keys(enabled, name)").Error; err != nil {
			logrus.WithError(err).Warn("Failed to create composite index on (enabled, name), continuing anyway")
		} else {
			logrus.Info("Created composite index on hub_access_keys(enabled, name)")
		}
	} else {
		logrus.Info("Composite index on (enabled, name) already exists, skipping")
	}

	// Add composite index on (enabled, last_used_at) for usage analytics
	indexEnabledLastUsed := "idx_hub_access_keys_enabled_last_used"
	if !migrator.HasIndex(&hubAccessKeyIndexOptimization{}, indexEnabledLastUsed) {
		if err := db.Exec("CREATE INDEX " + indexEnabledLastUsed + " ON hub_access_keys(enabled, last_used_at DESC)").Error; err != nil {
			logrus.WithError(err).Warn("Failed to create composite index on (enabled, last_used_at), continuing anyway")
		} else {
			logrus.Info("Created composite index on hub_access_keys(enabled, last_used_at)")
		}
	} else {
		logrus.Info("Composite index on (enabled, last_used_at) already exists, skipping")
	}

	logrus.Info("Hub access key index optimization completed")
	return nil
}

// hubAccessKeyIndexOptimization is a minimal struct for migration purposes
type hubAccessKeyIndexOptimization struct {
	ID      uint   `gorm:"primaryKey"`
	Name    string `gorm:"column:name;index:idx_hub_access_keys_enabled_name"`
	Enabled bool   `gorm:"column:enabled;index:idx_hub_access_keys_enabled_name,idx_hub_access_keys_enabled_last_used"`
}

func (hubAccessKeyIndexOptimization) TableName() string {
	return "hub_access_keys"
}
