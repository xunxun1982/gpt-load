package db

import (
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_15_0_AddAccessKeyUsageStats adds usage statistics columns to hub_access_keys table.
// This migration adds usage_count and last_used_at columns for tracking key usage.
func V1_15_0_AddAccessKeyUsageStats(db *gorm.DB) error {
	migrator := db.Migrator()

	// Check if hub_access_keys table exists
	if !migrator.HasTable("hub_access_keys") {
		logrus.Info("Table hub_access_keys does not exist, skipping usage stats migration")
		return nil
	}

	// Add usage_count column if not exists
	if !migrator.HasColumn(&hubAccessKeyUsageStats{}, "usage_count") {
		if err := migrator.AddColumn(&hubAccessKeyUsageStats{}, "usage_count"); err != nil {
			logrus.WithError(err).Error("Failed to add usage_count column to hub_access_keys")
			return err
		}
		logrus.Info("Added usage_count column to hub_access_keys table")
	} else {
		logrus.Info("Column usage_count already exists in hub_access_keys, skipping")
	}

	// Add last_used_at column if not exists
	if !migrator.HasColumn(&hubAccessKeyUsageStats{}, "last_used_at") {
		if err := migrator.AddColumn(&hubAccessKeyUsageStats{}, "last_used_at"); err != nil {
			logrus.WithError(err).Error("Failed to add last_used_at column to hub_access_keys")
			return err
		}
		logrus.Info("Added last_used_at column to hub_access_keys table")
	} else {
		logrus.Info("Column last_used_at already exists in hub_access_keys, skipping")
	}

	// Add index on last_used_at for efficient queries
	indexName := "idx_hub_access_keys_last_used"
	if !migrator.HasIndex(&hubAccessKeyUsageStats{}, indexName) {
		if err := db.Exec("CREATE INDEX " + indexName + " ON hub_access_keys(last_used_at)").Error; err != nil {
			logrus.WithError(err).Warn("Failed to create index on last_used_at, continuing anyway")
		} else {
			logrus.Info("Created index on hub_access_keys.last_used_at")
		}
	}

	return nil
}

// hubAccessKeyUsageStats is a minimal struct for migration purposes
type hubAccessKeyUsageStats struct {
	ID         uint       `gorm:"primaryKey"`
	UsageCount int64      `gorm:"column:usage_count;not null;default:0"`
	LastUsedAt *time.Time `gorm:"column:last_used_at;index:idx_hub_access_keys_last_used"`
}

func (hubAccessKeyUsageStats) TableName() string {
	return "hub_access_keys"
}
