package services

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupReadDBContentionTest(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()

	dsn := filepath.Join(t.TempDir(), "read-contention.db") +
		"?_busy_timeout=100&_journal_mode=WAL&_synchronous=NORMAL"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger:      logger.Default.LogMode(logger.Silent),
			PrepareStmt: false,
		})
		require.NoError(t, err)
		return db
	}

	writeDB := open()
	readDB := open()

	writeSQLDB, err := writeDB.DB()
	require.NoError(t, err)
	writeSQLDB.SetMaxOpenConns(1)
	writeSQLDB.SetMaxIdleConns(1)

	readSQLDB, err := readDB.DB()
	require.NoError(t, err)
	readSQLDB.SetMaxOpenConns(2)
	readSQLDB.SetMaxIdleConns(1)

	t.Cleanup(func() {
		_ = readSQLDB.Close()
		_ = writeSQLDB.Close()
	})

	return writeDB, readDB
}

func holdOnlyWriteConnection(t *testing.T, writeDB *gorm.DB) func() {
	t.Helper()

	sqlDB, err := writeDB.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)

	return func() {
		require.NoError(t, conn.Close())
	}
}

func TestGroupServiceRequestStatsUsesReadDBWhenWritePoolIsBusy(t *testing.T) {
	writeDB, readDB := setupReadDBContentionTest(t)
	require.NoError(t, writeDB.AutoMigrate(&models.GroupHourlyStat{}))

	const groupID = 42
	require.NoError(t, writeDB.Create(&models.GroupHourlyStat{
		Time:         time.Now().Truncate(time.Hour).Add(-time.Hour),
		GroupID:      groupID,
		SuccessCount: 3,
		FailureCount: 1,
	}).Error)

	service := &GroupService{db: writeDB, readDB: readDB}
	releaseWriteConnection := holdOnlyWriteConnection(t, writeDB)
	defer releaseWriteConnection()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	stats24h, stats7d, stats30d, err := service.queryMultipleTimeRangeStats(ctx, groupID)
	require.NoError(t, err)
	require.Equal(t, int64(4), stats24h.TotalRequests)
	require.Equal(t, int64(4), stats7d.TotalRequests)
	require.Equal(t, int64(4), stats30d.TotalRequests)
}

func TestAggregateGroupServiceGetSubGroupsUsesReadDBWhenWritePoolIsBusy(t *testing.T) {
	writeDB, readDB := setupReadDBContentionTest(t)
	require.NoError(t, writeDB.AutoMigrate(
		&models.Group{},
		&models.GroupSubGroup{},
		&models.APIKey{},
	))

	aggregateGroup := models.Group{
		Name:        "aggregate-read-pool",
		GroupType:   "aggregate",
		Enabled:     true,
		Upstreams:   datatypes.JSON(`[]`),
		ChannelType: "openai",
		TestModel:   "test-model",
	}
	subGroup := models.Group{
		Name:        "standard-read-pool",
		GroupType:   "standard",
		Enabled:     true,
		Upstreams:   datatypes.JSON(`[]`),
		ChannelType: "openai",
		TestModel:   "test-model",
	}
	require.NoError(t, writeDB.Create(&aggregateGroup).Error)
	require.NoError(t, writeDB.Create(&subGroup).Error)
	require.NoError(t, writeDB.Create(&models.GroupSubGroup{
		GroupID:            aggregateGroup.ID,
		SubGroupID:         subGroup.ID,
		Weight:             1,
		MinEffectiveWeight: 1,
	}).Error)
	require.NoError(t, writeDB.Create(&models.APIKey{
		GroupID:  subGroup.ID,
		KeyValue: "test-key",
		Status:   models.KeyStatusActive,
	}).Error)

	service := NewAggregateGroupService(
		writeDB,
		ReadOnlyDB{DB: readDB},
		nil,
		nil,
	)
	releaseWriteConnection := holdOnlyWriteConnection(t, writeDB)
	defer releaseWriteConnection()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	subGroups, err := service.GetSubGroups(ctx, aggregateGroup.ID)
	require.NoError(t, err)
	require.Len(t, subGroups, 1)
	require.Equal(t, subGroup.ID, subGroups[0].Group.ID)
	require.Equal(t, int64(1), subGroups[0].TotalKeys)
	require.Equal(t, int64(1), subGroups[0].ActiveKeys)
}

func TestKeyServiceListKeysUsesReadDBWhenWritePoolIsBusy(t *testing.T) {
	writeDB, readDB := setupReadDBContentionTest(t)
	require.NoError(t, writeDB.AutoMigrate(&models.APIKey{}))

	const groupID = 7
	require.NoError(t, writeDB.Create(&models.APIKey{
		GroupID:  groupID,
		KeyValue: "test-key",
		Status:   models.KeyStatusActive,
	}).Error)

	service := NewKeyService(writeDB, ReadOnlyDB{DB: readDB}, nil, nil, nil, nil)
	releaseWriteConnection := holdOnlyWriteConnection(t, writeDB)
	defer releaseWriteConnection()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var keys []models.APIKey
	err := service.ListKeysInGroupQuery(groupID, "", "").WithContext(ctx).Find(&keys).Error
	require.NoError(t, err)
	require.Len(t, keys, 1)
}
