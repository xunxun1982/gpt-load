package db

import (
	"github.com/sirupsen/logrus"
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
	migrator := db.Migrator()

	// Check if table exists
	if !migrator.HasTable("dynamic_weight_metrics") {
		logrus.Info("Table dynamic_weight_metrics does not exist, skipping v1.21.0 migration")
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Detect database type
		dialect := tx.Dialector.Name()

		// Step 1: Add target_model column (check existence within transaction)
		// Use raw SQL to check column existence for better reliability across databases
		var columnExists bool
		switch dialect {
		case "sqlite":
			// SQLite: Check PRAGMA table_info
			var count int64
			tx.Raw("SELECT COUNT(*) FROM pragma_table_info('dynamic_weight_metrics') WHERE name = 'target_model'").Scan(&count)
			columnExists = count > 0
		case "mysql":
			// MySQL: Check information_schema
			var count int64
			tx.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dynamic_weight_metrics' AND COLUMN_NAME = 'target_model'").Scan(&count)
			columnExists = count > 0
		case "postgres":
			// PostgreSQL: Check information_schema
			var count int64
			tx.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'dynamic_weight_metrics' AND column_name = 'target_model'").Scan(&count)
			columnExists = count > 0
		default:
			// Fallback: Try using migrator (may not be reliable)
			columnExists = migrator.HasColumn(&dynamicWeightMetricV1_21_0{}, "target_model")
		}

		if columnExists {
			logrus.Info("Column target_model already exists in dynamic_weight_metrics, skipping v1.21.0 migration")
			return nil
		}

		// Add target_model column
		if err := tx.Exec("ALTER TABLE dynamic_weight_metrics ADD COLUMN target_model VARCHAR(255) DEFAULT ''").Error; err != nil {
			logrus.WithError(err).Error("Failed to add target_model column")
			return err
		}
		logrus.Info("Added target_model column to dynamic_weight_metrics table")

		// Step 2: Drop old unique index
		// Check if index exists before dropping to ensure idempotency
		logrus.Info("Dropping old unique index idx_dwm_unique")
		var indexExists bool
		switch dialect {
		case "sqlite":
			// SQLite: Check sqlite_master
			var count int64
			tx.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_dwm_unique'").Scan(&count)
			indexExists = count > 0
		case "mysql":
			// MySQL: Check information_schema
			var count int64
			tx.Raw(`
				SELECT COUNT(*) FROM information_schema.STATISTICS
				WHERE TABLE_SCHEMA = DATABASE()
				AND TABLE_NAME = 'dynamic_weight_metrics'
				AND INDEX_NAME = 'idx_dwm_unique'
			`).Scan(&count)
			indexExists = count > 0
		case "postgres":
			// PostgreSQL: Check pg_indexes
			var count int64
			tx.Raw("SELECT COUNT(*) FROM pg_indexes WHERE tablename = 'dynamic_weight_metrics' AND indexname = 'idx_dwm_unique'").Scan(&count)
			indexExists = count > 0
		default:
			// For unknown databases, assume index exists and try to drop
			indexExists = true
		}

		if indexExists {
			switch dialect {
			case "sqlite":
				if err := tx.Exec("DROP INDEX idx_dwm_unique").Error; err != nil {
					logrus.WithError(err).Error("Failed to drop index for SQLite")
					return err
				}
			case "mysql":
				if err := tx.Exec("ALTER TABLE dynamic_weight_metrics DROP INDEX idx_dwm_unique").Error; err != nil {
					logrus.WithError(err).Error("Failed to drop index for MySQL")
					return err
				}
			case "postgres":
				if err := tx.Exec("DROP INDEX idx_dwm_unique").Error; err != nil {
					logrus.WithError(err).Error("Failed to drop index for PostgreSQL")
					return err
				}
			default:
				if err := tx.Exec("DROP INDEX IF EXISTS idx_dwm_unique").Error; err != nil {
					logrus.WithError(err).Debug("Failed to drop index with generic syntax, continuing")
				}
			}
			logrus.Info("Dropped old unique index")
		} else {
			logrus.Info("Index idx_dwm_unique does not exist, skipping drop")
		}

		// Step 3: Create new unique index with target_model
		// The unique constraint is: (metric_type, group_id, sub_group_id, source_model, target_model)
		// This ensures each combination is unique
		// Note: CREATE UNIQUE INDEX syntax is identical across SQLite, MySQL, and PostgreSQL
		logrus.Info("Creating new unique index with target_model")
		if err := tx.Exec(`
			CREATE UNIQUE INDEX idx_dwm_unique
			ON dynamic_weight_metrics(metric_type, group_id, sub_group_id, source_model, target_model)
		`).Error; err != nil {
			logrus.WithError(err).Error("Failed to create unique index")
			return err
		}
		logrus.Info("Created new unique index")

		// Step 4: Soft-delete all existing model redirect metrics
		// They will be cleaned up by the cleanup task after 180 days
		// New metrics will be created with target_model populated
		logrus.Info("Soft-deleting existing model redirect metrics")
		if err := tx.Exec(`
			UPDATE dynamic_weight_metrics
			SET deleted_at = CURRENT_TIMESTAMP
			WHERE metric_type = 'model_redirect' AND deleted_at IS NULL
		`).Error; err != nil {
			logrus.WithError(err).Error("Failed to soft-delete existing metrics")
			return err
		}
		logrus.Info("Soft-deleted existing model redirect metrics")

		logrus.Info("Migration v1.21.0 completed successfully")
		return nil
	})
}


// dynamicWeightMetricV1_21_0 is a minimal struct for migration purposes
type dynamicWeightMetricV1_21_0 struct {
	ID          uint   `gorm:"primaryKey"`
	TargetModel string `gorm:"column:target_model;type:varchar(255)"`
}

// TableName returns the table name for GORM
func (dynamicWeightMetricV1_21_0) TableName() string {
	return "dynamic_weight_metrics"
}
