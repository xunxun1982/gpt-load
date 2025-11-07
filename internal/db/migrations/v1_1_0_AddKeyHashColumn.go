package db

import (
	"fmt"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"strings"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_1_0_AddKeyHashColumn adds key_hash column to api_keys and request_logs tables
func V1_1_0_AddKeyHashColumn(db *gorm.DB) error {
	// First check if there are any records need migration
	var needMigrateCount int64
	db.Model(&models.APIKey{}).
		Where("key_hash IS NULL OR key_hash = ''").
		Count(&needMigrateCount)

	if needMigrateCount == 0 {
		logrus.Info("No api_keys need migration, skipping v1.1.0...")
		return nil
	}

	logrus.Infof("Found %d api_keys need to populate key_hash, starting migration...", needMigrateCount)

	encSvc, err := encryption.NewService("")
	if err != nil {
		return fmt.Errorf("failed to initialize encryption service: %w", err)
	}

	// Process in batches using CASE WHEN for efficient batch update
	const batchSize = 500
	processed := 0
	lastLogPercent := 0

	for {
		var apiKeys []models.APIKey
		// Only query ID and KeyValue to reduce memory usage
		result := db.Select("id", "key_value").
			Where("key_hash IS NULL OR key_hash = ''").
			Limit(batchSize).
			Find(&apiKeys)

		if result.Error != nil {
			return fmt.Errorf("failed to fetch api_keys: %w", result.Error)
		}

		if len(apiKeys) == 0 {
			break
		}

		// Build CASE WHEN statement for batch update with parameterized queries
		// This prevents SQL injection and handles special characters in hashes
		var caseWhen strings.Builder
		args := make([]interface{}, 0, len(apiKeys)*3)
		ids := make([]interface{}, 0, len(apiKeys))

		caseWhen.WriteString("UPDATE api_keys SET key_hash = CASE id ")
		for _, key := range apiKeys {
			keyHash := encSvc.Hash(key.KeyValue)
			caseWhen.WriteString("WHEN ? THEN ? ")
			args = append(args, key.ID, keyHash)
			ids = append(ids, key.ID)
		}
		caseWhen.WriteString("END, updated_at = CURRENT_TIMESTAMP WHERE id IN (")
		for i := range ids {
			if i > 0 {
				caseWhen.WriteString(",")
			}
			caseWhen.WriteString("?")
		}
		caseWhen.WriteString(")")
		args = append(args, ids...)

		// Execute batch update with parameterized query
		err := db.Exec(caseWhen.String(), args...).Error
		if err != nil {
			logrus.WithError(err).Error("Failed to update batch of key_hash")
			return err
		}

		processed += len(apiKeys)

		// Only log every 10% progress to reduce log spam
		currentPercent := (processed * 100) / int(needMigrateCount)
		if currentPercent >= lastLogPercent+10 || processed == int(needMigrateCount) {
			logrus.Infof("Migration progress: %d/%d (%d%%)", processed, needMigrateCount, currentPercent)
			lastLogPercent = currentPercent
		}
	}

	logrus.Info("Migration v1.1.0 completed successfully")
	return nil
}
