package db

import (
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
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
		Config:        datatypes.JSONMap{"proxy_url": "http://127.0.0.1:8080", "max_retries": float64(2)},
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
