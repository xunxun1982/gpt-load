package db

import (
	"gorm.io/gorm"
)

// V1_21_0_ChangeTargetIndexToTargetModel changes the dynamic_weight_metrics table
// to use target_model instead of target_index for model redirect metrics.
// This fixes the issue where deleting a model redirect target causes health scores
// to shift to the wrong targets due to index changes.
//
// Changes:
// 1. Add target_model column (varchar(255))
// 2. Drop old unique index idx_dwm_unique
// 3. Create new unique index with target_model instead of target_index
// 4. Existing data will lose health scores (acceptable as they will rebuild automatically)
//
// Note: We don't migrate existing data because:
// - Old records only have target_index, not the actual model name
// - Health scores are dynamic metrics that rebuild quickly
// - Soft-deleted records will be cleaned up by the cleanup task
func V1_21_0_ChangeTargetIndexToTargetModel(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// Detect database type
		dialect := tx.Dialector.Name()

		// Step 1: Add target_model column
		if err := tx.Exec("ALTER TABLE dynamic_weight_metrics ADD COLUMN target_model VARCHAR(255) DEFAULT ''").Error; err != nil {
			return err
		}

		// Step 2: Drop old unique index
		// Different databases have different syntax for dropping indexes
		switch dialect {
		case "sqlite":
			if err := tx.Exec("DROP INDEX IF EXISTS idx_dwm_unique").Error; err != nil {
				return err
			}
		case "mysql":
			// MySQL requires checking if index exists before dropping
			if err := tx.Exec("ALTER TABLE dynamic_weight_metrics DROP INDEX idx_dwm_unique").Error; err != nil {
				// Ignore error if index doesn't exist
				// MySQL returns error 1091 if index doesn't exist
			}
		case "postgres":
			if err := tx.Exec("DROP INDEX IF EXISTS idx_dwm_unique").Error; err != nil {
				return err
			}
		default:
			// Try generic syntax
			if err := tx.Exec("DROP INDEX IF EXISTS idx_dwm_unique").Error; err != nil {
				// Ignore error if not supported
			}
		}

		// Step 3: Create new unique index with target_model
		// The unique constraint is: (metric_type, group_id, sub_group_id, source_model, target_model)
		// This ensures each combination is unique
		switch dialect {
		case "sqlite":
			if err := tx.Exec(`
				CREATE UNIQUE INDEX idx_dwm_unique
				ON dynamic_weight_metrics(metric_type, group_id, sub_group_id, source_model, target_model)
			`).Error; err != nil {
				return err
			}
		case "mysql":
			if err := tx.Exec(`
				CREATE UNIQUE INDEX idx_dwm_unique
				ON dynamic_weight_metrics(metric_type, group_id, sub_group_id, source_model, target_model)
			`).Error; err != nil {
				return err
			}
		case "postgres":
			if err := tx.Exec(`
				CREATE UNIQUE INDEX idx_dwm_unique
				ON dynamic_weight_metrics(metric_type, group_id, sub_group_id, source_model, target_model)
			`).Error; err != nil {
				return err
			}
		default:
			if err := tx.Exec(`
				CREATE UNIQUE INDEX idx_dwm_unique
				ON dynamic_weight_metrics(metric_type, group_id, sub_group_id, source_model, target_model)
			`).Error; err != nil {
				return err
			}
		}

		// Step 4: Soft-delete all existing model redirect metrics
		// They will be cleaned up by the cleanup task after 180 days
		// New metrics will be created with target_model populated
		if err := tx.Exec(`
			UPDATE dynamic_weight_metrics
			SET deleted_at = CURRENT_TIMESTAMP
			WHERE metric_type = 'model_redirect' AND deleted_at IS NULL
		`).Error; err != nil {
			return err
		}

		return nil
	})
}
