package db

import (
	"database/sql"
	"encoding/json"

	"gpt-load/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_11_1_MigrateModelRedirectV1ToV2 migrates V1 model redirect rules to V2 format.
// For each group with V1 rules but no V2 rules, converts V1 to V2 and clears V1.
// This is a one-time data migration to unify all redirect rules to V2 format.
func V1_11_1_MigrateModelRedirectV1ToV2(db *gorm.DB) error {
	// First, check if there are any groups that need migration
	var needsMigrationCount int64
	if err := db.Raw(`
		SELECT COUNT(*) FROM groups
		WHERE model_redirect_rules IS NOT NULL
		AND model_redirect_rules != '{}'
		AND model_redirect_rules != ''
		AND (model_redirect_rules_v2 IS NULL OR model_redirect_rules_v2 = '{}' OR model_redirect_rules_v2 = '')
	`).Scan(&needsMigrationCount).Error; err != nil {
		return err
	}

	if needsMigrationCount == 0 {
		return nil
	}

	logrus.WithField("count", needsMigrationCount).Info("Starting V1->V2 model redirect rules migration")

	// Query groups that need migration
	rows, err := db.Raw(`
		SELECT id, model_redirect_rules
		FROM groups
		WHERE model_redirect_rules IS NOT NULL
		AND model_redirect_rules != '{}'
		AND model_redirect_rules != ''
		AND (model_redirect_rules_v2 IS NULL OR model_redirect_rules_v2 = '{}' OR model_redirect_rules_v2 = '')
	`).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()

	// Collect updates for batch processing
	type updateItem struct {
		ID     uint
		V2JSON string
	}
	updates := make([]updateItem, 0, needsMigrationCount)

	for rows.Next() {
		var id uint
		var v1RulesRaw sql.NullString

		if err := rows.Scan(&id, &v1RulesRaw); err != nil {
			logrus.WithError(err).Warn("Failed to scan group row during V1->V2 migration")
			continue
		}

		if !v1RulesRaw.Valid || v1RulesRaw.String == "" || v1RulesRaw.String == "{}" {
			continue
		}

		var v1Rules map[string]string
		if err := json.Unmarshal([]byte(v1RulesRaw.String), &v1Rules); err != nil {
			logrus.WithField("group_id", id).WithError(err).Warn("Failed to parse V1 rules during migration")
			continue
		}

		if len(v1Rules) == 0 {
			continue
		}

		newV2Rules := models.MigrateV1ToV2Rules(v1Rules)
		v2JSON, err := json.Marshal(newV2Rules)
		if err != nil {
			logrus.WithField("group_id", id).WithError(err).Warn("Failed to marshal V2 rules during migration")
			continue
		}

		updates = append(updates, updateItem{ID: id, V2JSON: string(v2JSON)})
	}
	rows.Close()

	if len(updates) == 0 {
		return nil
	}

	// Batch update in a single transaction
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, item := range updates {
			if err := tx.Exec(`
				UPDATE groups
				SET model_redirect_rules_v2 = ?, model_redirect_rules = '{}'
				WHERE id = ?
			`, item.V2JSON, item.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		logrus.WithError(err).Error("Failed to batch update groups during V1->V2 migration")
		return err
	}

	logrus.WithField("count", len(updates)).Info("Migrated model redirect rules from V1 to V2")
	return nil
}
