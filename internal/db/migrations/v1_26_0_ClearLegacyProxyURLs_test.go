package db

import (
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func setupClearLegacyProxyURLsTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&models.SystemSetting{}, &models.Group{}))
	return db
}

func TestV1_26_0_ClearLegacyProxyURLsClearsSystemAndGroupProxyValues(t *testing.T) {
	t.Parallel()

	db := setupClearLegacyProxyURLsTestDB(t)

	require.NoError(t, db.Create(&models.SystemSetting{
		SettingKey:   "proxy_url",
		SettingValue: "http://user:pass@127.0.0.1:8080",
	}).Error)
	require.NoError(t, db.Create(&models.Group{
		Name:          "standard",
		GroupType:     "standard",
		Upstreams:     datatypes.JSON(`[{"url":"https://a.example","weight":1,"proxy_url":"socks5://127.0.0.1:1080"}]`),
		ChannelType:   "openai",
		TestModel:     "gpt-test",
		Config:        datatypes.JSONMap{"proxy_url": "http://127.0.0.1:8080", "max_retries": json.Number("2")},
		HeaderRules:   datatypes.JSON("[]"),
		PathRedirects: datatypes.JSON("[]"),
	}).Error)

	require.NoError(t, V1_26_0_ClearLegacyProxyURLs(db))

	var setting models.SystemSetting
	require.NoError(t, db.Where("setting_key = ?", "proxy_url").First(&setting).Error)
	require.Empty(t, setting.SettingValue)

	var group models.Group
	require.NoError(t, db.Where("name = ?", "standard").First(&group).Error)
	require.NotContains(t, string(group.Upstreams), "proxy_url")
	require.NotContains(t, group.Config, "proxy_url")
	maxRetries, ok := group.Config["max_retries"].(json.Number)
	require.True(t, ok)
	require.Equal(t, "2", maxRetries.String())

	require.NoError(t, db.Model(&dataMigrationMarker{}).
		Where("version = ?", clearLegacyProxyURLsMigrationVersion).
		First(&dataMigrationMarker{}).Error)

	require.NoError(t, db.Model(&setting).Update("setting_value", "http://127.0.0.1:7890").Error)
	group.Upstreams = datatypes.JSON(`[{"url":"https://a.example","weight":1,"proxy_url":"http://127.0.0.1:7890"}]`)
	group.Config = datatypes.JSONMap{"proxy_url": "http://127.0.0.1:7890"}
	require.NoError(t, db.Save(&group).Error)

	require.NoError(t, V1_26_0_ClearLegacyProxyURLs(db))
	require.NoError(t, db.Where("setting_key = ?", "proxy_url").First(&setting).Error)
	require.Equal(t, "http://127.0.0.1:7890", setting.SettingValue)
	require.NoError(t, db.Where("name = ?", "standard").First(&group).Error)
	require.Contains(t, string(group.Upstreams), "proxy_url")
	require.Contains(t, group.Config, "proxy_url")
}

func TestV1_26_0_ClearLegacyProxyURLsMarkerInsertIsIdempotent(t *testing.T) {
	t.Parallel()

	db := setupClearLegacyProxyURLsTestDB(t)
	require.NoError(t, ensureDataMigrationsTable(db))

	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, tx.Create(&dataMigrationMarker{
		Version:   clearLegacyProxyURLsMigrationVersion,
		CreatedAt: time.Now().UTC(),
	}).Error)

	acquired, err := acquireClearLegacyProxyURLsMigrationMarker(tx)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, tx.Commit().Error)

	var count int64
	require.NoError(t, db.Model(&dataMigrationMarker{}).
		Where("version = ?", clearLegacyProxyURLsMigrationVersion).
		Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestV1_26_0_ClearLegacyProxyURLsConcurrentSkipDoesNotLogCompleted(t *testing.T) {
	db := setupClearLegacyProxyURLsTestDB(t)
	require.NoError(t, ensureDataMigrationsTable(db))

	insertedByConcurrentInstance := false
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(
		"test:insert_clear_legacy_proxy_urls_marker",
		func(tx *gorm.DB) {
			if insertedByConcurrentInstance || tx.Statement == nil || tx.Statement.Table != "data_migrations" {
				return
			}
			marker, ok := tx.Statement.Dest.(*dataMigrationMarker)
			if !ok || marker.Version != clearLegacyProxyURLsMigrationVersion {
				return
			}
			insertedByConcurrentInstance = true
			if err := tx.Exec(
				"INSERT INTO data_migrations (version, created_at) VALUES (?, ?)",
				clearLegacyProxyURLsMigrationVersion,
				time.Now().UTC(),
			).Error; err != nil {
				tx.AddError(err)
			}
		},
	))

	hook := logrustest.NewGlobal()
	defer hook.Reset()

	require.NoError(t, V1_26_0_ClearLegacyProxyURLs(db))

	var messages []string
	for _, entry := range hook.AllEntries() {
		messages = append(messages, entry.Message)
	}
	require.Contains(t, messages, "Migration v1.26.0 completed concurrently, skipping")
	require.NotContains(t, messages, "Migration v1.26.0 completed")
}
