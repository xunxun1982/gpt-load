package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_3_1_AddGroupIDIndex adds a dedicated index on group_id for faster deletion operations
// This fixes the slow SQL issue where SELECT id FROM api_keys WHERE group_id = ? was taking 1.3+ seconds
func V1_3_1_AddGroupIDIndex(db *gorm.DB) error {
	migrator := db.Migrator()

	logrus.Info("Running migration v1.3.1: Adding dedicated group_id index to api_keys for faster deletion")

	// Check if the dedicated group_id index already exists
	// Note: Some databases already have this from the composite index, but a dedicated one is better
	indexName := "idx_api_keys_group_id"

	// Check if index exists
	hasIndex := migrator.HasIndex("api_keys", indexName)

	if !hasIndex {
		// Create index on group_id for faster lookups when deleting groups
		// This significantly improves performance of queries like:
		// SELECT id FROM api_keys WHERE group_id = ?
		// DELETE FROM api_keys WHERE group_id = ?
		if err := db.Exec("CREATE INDEX idx_api_keys_group_id ON api_keys(group_id)").Error; err != nil {
			// Try GORM's method if raw SQL fails
			type APIKeyIndex struct {
				GroupID uint `gorm:"index:idx_api_keys_group_id"`
			}
			if err := migrator.CreateIndex(&APIKeyIndex{}, "idx_api_keys_group_id"); err != nil {
				logrus.WithError(err).Error("Failed to create group_id index on api_keys")
				return err
			}
		}

		logrus.Info("Successfully added idx_api_keys_group_id index")
	} else {
		logrus.Info("Index idx_api_keys_group_id already exists, skipping migration v1.3.1")
	}

	return nil
}