package db

import (
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	apiKeyGroupOrderIndexV2 = "idx_api_keys_group_order_v2"
	apiKeyGroupHashIndex    = "idx_api_keys_group_key_hash"
)

// V1_25_0_OptimizeAPIKeyIndexes adds composite indexes for high-throughput API key queries.
// Compatible with SQLite, MySQL 8.0+, and PostgreSQL 12+.
func V1_25_0_OptimizeAPIKeyIndexes(db *gorm.DB) error {
	logrus.Info("Running migration v1.25.0: Optimizing api_keys composite indexes")

	if err := createAPIKeyGroupOrderIndex(db); err != nil {
		return err
	}

	// Reuse the existing model index name to avoid creating a duplicate equivalent
	// index with another review-suggested name.
	if err := createIndexIfNotExists(db, "api_keys", apiKeyGroupHashIndex, "group_id, key_hash"); err != nil {
		return err
	}

	logrus.Info("Migration v1.25.0 completed")
	return nil
}

func createAPIKeyGroupOrderIndex(db *gorm.DB) error {
	columns := "group_id, last_used_at, updated_at, id"
	if dialect := db.Dialector.Name(); dialect == "postgres" || dialect == "pgx" {
		columns = "group_id, last_used_at DESC NULLS LAST, updated_at DESC, id DESC"
	}
	return createIndexIfNotExists(db, "api_keys", apiKeyGroupOrderIndexV2, columns)
}
