package db

import (
	"testing"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestV1_30_0_AddStatsTimeIndexesCreatesTimeIndexes verifies the migration creates
// dedicated single-column time indexes on both stats tables. AutoMigrate also creates
// these indexes from the model gorm tags, so we drop them first to prove the migration
// itself builds them rather than silently relying on AutoMigrate.
func TestV1_30_0_AddStatsTimeIndexesCreatesTimeIndexes(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()

	require.NoError(t, db.AutoMigrate(&models.GroupHourlyStat{}, &models.ModelTokenHourlyStat{}))

	require.NoError(t, db.Migrator().DropIndex("group_hourly_stats", groupHourlyStatsTimeIndex))
	require.NoError(t, db.Migrator().DropIndex("model_token_hourly_stats", modelTokenHourlyStatsTimeIndex))
	require.False(t, db.Migrator().HasIndex("group_hourly_stats", groupHourlyStatsTimeIndex))
	require.False(t, db.Migrator().HasIndex("model_token_hourly_stats", modelTokenHourlyStatsTimeIndex))

	require.NoError(t, V1_30_0_AddStatsTimeIndexes(db))
	require.True(t, db.Migrator().HasIndex("group_hourly_stats", groupHourlyStatsTimeIndex))
	require.True(t, db.Migrator().HasIndex("model_token_hourly_stats", modelTokenHourlyStatsTimeIndex))
}

// TestV1_30_0_AddStatsTimeIndexesIsIdempotent verifies the migration is safe to run
// multiple times (e.g. on restart or on a DB where AutoMigrate already built the indexes).
func TestV1_30_0_AddStatsTimeIndexesIsIdempotent(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()

	require.NoError(t, db.AutoMigrate(&models.GroupHourlyStat{}, &models.ModelTokenHourlyStat{}))

	require.NoError(t, V1_30_0_AddStatsTimeIndexes(db))
	require.NoError(t, V1_30_0_AddStatsTimeIndexes(db))
	require.NoError(t, V1_30_0_AddStatsTimeIndexes(db))
	require.True(t, db.Migrator().HasIndex("group_hourly_stats", groupHourlyStatsTimeIndex))
	require.True(t, db.Migrator().HasIndex("model_token_hourly_stats", modelTokenHourlyStatsTimeIndex))
}
