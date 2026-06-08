package db

import (
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const clearLegacyProxyURLsMigrationVersion = "v1.26.0_clear_legacy_proxy_urls"

type dataMigrationMarker struct {
	Version   string    `gorm:"primaryKey;type:varchar(128)"`
	CreatedAt time.Time `gorm:"not null"`
}

func (dataMigrationMarker) TableName() string {
	return "data_migrations"
}

// V1_26_0_ClearLegacyProxyURLs clears pre-pool proxy URL settings.
// Operators rebuild proxy choices in the proxy pool after upgrading.
func V1_26_0_ClearLegacyProxyURLs(db *gorm.DB) error {
	logrus.Info("Running migration v1.26.0: Clearing legacy proxy URLs")

	if err := ensureDataMigrationsTable(db); err != nil {
		return err
	}
	ran, err := hasDataMigrationRun(db, clearLegacyProxyURLsMigrationVersion)
	if err != nil {
		return err
	}
	if ran {
		logrus.Info("Migration v1.26.0 already completed, skipping")
		return nil
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		acquired, err := acquireClearLegacyProxyURLsMigrationMarker(tx)
		if err != nil {
			return err
		}
		if !acquired {
			logrus.Info("Migration v1.26.0 completed concurrently, skipping")
			return nil
		}

		if tx.Migrator().HasTable("system_settings") {
			result := tx.Table("system_settings").
				Where("setting_key = ? AND setting_value <> ?", "proxy_url", "").
				Update("setting_value", "")
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				logrus.WithField("count", result.RowsAffected).Info("Cleared legacy system proxy URLs")
			}
		}

		if tx.Migrator().HasTable("groups") {
			if err := clearLegacyGroupProxyURLs(tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	logrus.Info("Migration v1.26.0 completed")
	return nil
}

func ensureDataMigrationsTable(db *gorm.DB) error {
	if db.Migrator().HasTable(&dataMigrationMarker{}) {
		return nil
	}
	return db.Migrator().CreateTable(&dataMigrationMarker{})
}

func hasDataMigrationRun(db *gorm.DB, version string) (bool, error) {
	var count int64
	err := db.Model(&dataMigrationMarker{}).Where("version = ?", version).Count(&count).Error
	return count > 0, err
}

func acquireClearLegacyProxyURLsMigrationMarker(db *gorm.DB) (bool, error) {
	result := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "version"}},
		DoNothing: true,
	}).Create(&dataMigrationMarker{
		Version:   clearLegacyProxyURLsMigrationVersion,
		CreatedAt: time.Now().UTC(),
	})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func clearLegacyGroupProxyURLs(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "postgres", "pgx":
		return db.Exec(`
			UPDATE groups
			SET
				config = (COALESCE(config::jsonb, '{}'::jsonb) - 'proxy_url')::json,
				upstreams = (
					SELECT COALESCE(jsonb_agg(item - 'proxy_url'), '[]'::jsonb)
					FROM jsonb_array_elements(COALESCE(upstreams::jsonb, '[]'::jsonb)) AS item
				)::json
			WHERE
				(COALESCE(config::jsonb, '{}'::jsonb) ? 'proxy_url')
				OR upstreams::text LIKE '%"proxy_url"%'
		`).Error
	case "mysql":
		return db.Exec(`
			UPDATE groups
			SET
				config = JSON_REMOVE(COALESCE(config, JSON_OBJECT()), '$.proxy_url'),
				upstreams = COALESCE((
					SELECT JSON_ARRAYAGG(JSON_REMOVE(item.item, '$.proxy_url'))
					FROM JSON_TABLE(COALESCE(upstreams, JSON_ARRAY()), '$[*]' COLUMNS (item JSON PATH '$')) AS item
				), JSON_ARRAY())
			WHERE
				JSON_CONTAINS_PATH(COALESCE(config, JSON_OBJECT()), 'one', '$.proxy_url')
				OR CAST(upstreams AS CHAR) LIKE '%"proxy_url"%'
		`).Error
	default:
		return db.Exec(`
			UPDATE groups
			SET
				config = json_remove(COALESCE(config, '{}'), '$.proxy_url'),
				upstreams = COALESCE((
					SELECT json_group_array(json_remove(value, '$.proxy_url'))
					FROM json_each(COALESCE(upstreams, '[]'))
				), '[]')
			WHERE
				json_extract(COALESCE(config, '{}'), '$.proxy_url') IS NOT NULL
				OR upstreams LIKE '%"proxy_url"%'
		`).Error
	}
}
