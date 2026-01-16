package db

import (
	"gpt-load/internal/centralizedmgmt"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_12_0_CreateHubAccessKeys creates the hub_access_keys table for centralized API management.
// This table stores Hub access keys that control which models users can access through the hub endpoint.
//
// Table schema:
// - id: Primary key, auto-increment
// - name: Human-readable name for the access key
// - key_value: Encrypted key value (using encryption.Service)
// - allowed_models: JSON array of allowed model names (empty array means all models)
// - enabled: Whether the key is active
// - created_at, updated_at: Timestamps
//
// Indexes:
// - idx_hub_access_keys_enabled: For filtering by enabled status
// - idx_hub_access_keys_key_hash: Unique index for key hash lookup during validation
func V1_12_0_CreateHubAccessKeys(db *gorm.DB) error {
	migrator := db.Migrator()

	// Check if table already exists
	if migrator.HasTable(&centralizedmgmt.HubAccessKey{}) {
		logrus.Info("Table hub_access_keys already exists, skipping creation")
		return nil
	}

	// Create the table using GORM AutoMigrate
	// This will create the table with all columns and indexes defined in the model
	if err := db.AutoMigrate(&centralizedmgmt.HubAccessKey{}); err != nil {
		logrus.WithError(err).Error("Failed to create hub_access_keys table")
		return err
	}

	logrus.Info("Created hub_access_keys table successfully")
	return nil
}
