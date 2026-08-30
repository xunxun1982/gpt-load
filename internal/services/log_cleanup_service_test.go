package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/require"
)

func TestDeleteSQLiteExpiredLogsBatchIsBounded(t *testing.T) {
	writeDB, _ := setupReadDBContentionTest(t)
	require.NoError(t, writeDB.AutoMigrate(&models.RequestLog{}))

	cutoffTime := time.Now().UTC()
	logs := make([]models.RequestLog, SQLiteLogCleanupBatchSize+25)
	for i := range logs {
		logs[i] = models.RequestLog{
			ID:          fmt.Sprintf("expired-%04d", i),
			Timestamp:   cutoffTime.Add(-time.Duration(i+1) * time.Second),
			RequestType: models.RequestTypeFinal,
		}
	}
	require.NoError(t, writeDB.CreateInBatches(logs, 100).Error)

	service := &LogCleanupService{db: writeDB}
	result := service.deleteSQLiteExpiredLogsBatch(
		context.Background(),
		cutoffTime,
		SQLiteLogCleanupBatchSize,
	)

	require.NoError(t, result.Error)
	require.Equal(t, int64(SQLiteLogCleanupBatchSize), result.RowsAffected)

	var remaining int64
	require.NoError(t, writeDB.Model(&models.RequestLog{}).
		Where("timestamp < ?", cutoffTime).
		Count(&remaining).Error)
	require.Equal(t, int64(25), remaining)
}

func TestCleanupExpiredStatsDeletesOldRetainsFresh(t *testing.T) {
	t.Parallel()

	db := setupRequestLogServiceTestDB(t, &models.GroupHourlyStat{}, &models.ModelTokenHourlyStat{})

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -(StatsRetentionDays + 1))
	fresh := now.AddDate(0, 0, -1)

	require.NoError(t, db.Create(&[]models.GroupHourlyStat{
		{Time: old, GroupID: 1, SuccessCount: 1},
		{Time: fresh, GroupID: 1, SuccessCount: 1},
	}).Error)
	require.NoError(t, db.Create(&[]models.ModelTokenHourlyStat{
		{Time: old, GroupID: 1, Model: "m1", RequestCount: 1},
		{Time: fresh, GroupID: 1, Model: "m1", RequestCount: 1},
	}).Error)

	service := &LogCleanupService{db: db}
	service.cleanupExpiredStats()

	var groupRows int64
	require.NoError(t, db.Model(&models.GroupHourlyStat{}).Count(&groupRows).Error)
	require.Equal(t, int64(1), groupRows, "only the fresh group_hourly_stats row should remain")
	var groupLeft []models.GroupHourlyStat
	require.NoError(t, db.Find(&groupLeft).Error)
	require.Len(t, groupLeft, 1)
	require.True(t, groupLeft[0].Time.Equal(fresh))

	var tokenRows int64
	require.NoError(t, db.Model(&models.ModelTokenHourlyStat{}).Count(&tokenRows).Error)
	require.Equal(t, int64(1), tokenRows, "only the fresh model_token_hourly_stats row should remain")
	var tokenLeft []models.ModelTokenHourlyStat
	require.NoError(t, db.Find(&tokenLeft).Error)
	require.Len(t, tokenLeft, 1)
	require.True(t, tokenLeft[0].Time.Equal(fresh))
}

func TestCleanupExpiredStatsWithNothingExpiredIsSafe(t *testing.T) {
	t.Parallel()

	db := setupRequestLogServiceTestDB(t, &models.GroupHourlyStat{}, &models.ModelTokenHourlyStat{})

	fresh := time.Now().UTC().AddDate(0, 0, -1)
	require.NoError(t, db.Create(&[]models.GroupHourlyStat{
		{Time: fresh, GroupID: 1},
	}).Error)
	require.NoError(t, db.Create(&[]models.ModelTokenHourlyStat{
		{Time: fresh, GroupID: 1, Model: "m1"},
	}).Error)

	service := &LogCleanupService{db: db}
	// Must not panic, must not error, and must leave all rows untouched.
	service.cleanupExpiredStats()

	var groupRows int64
	require.NoError(t, db.Model(&models.GroupHourlyStat{}).Count(&groupRows).Error)
	require.Equal(t, int64(1), groupRows)
	var tokenRows int64
	require.NoError(t, db.Model(&models.ModelTokenHourlyStat{}).Count(&tokenRows).Error)
	require.Equal(t, int64(1), tokenRows)
}

// TestCleanupExpiredStatsMultiBatch verifies deleteExpiredStatsTable keeps issuing
// bounded batches until all expired rows past the retention cutoff are removed. Without
// the loop the table would retain rows beyond the first batchSize.
func TestCleanupExpiredStatsMultiBatch(t *testing.T) {
	t.Parallel()

	db := setupRequestLogServiceTestDB(t, &models.GroupHourlyStat{}, &models.ModelTokenHourlyStat{})

	old := time.Now().UTC().AddDate(0, 0, -(StatsRetentionDays + 1))
	rows := make([]models.GroupHourlyStat, SQLiteLogCleanupBatchSize*2+17)
	for i := range rows {
		// The (time, group_id unique constraint requires a distinct time per group;
		// minute offsets stay well below the retention cutoff, so every row is expired.
		rows[i] = models.GroupHourlyStat{Time: old.Add(time.Duration(i) * time.Minute), GroupID: uint(i%5 + 1), SuccessCount: 1}
	}
	require.NoError(t, db.Create(&rows).Error)

	service := &LogCleanupService{db: db}
	service.cleanupExpiredStats()

	var remaining int64
	require.NoError(t, db.Model(&models.GroupHourlyStat{}).Count(&remaining).Error)
	require.Equal(t, int64(0), remaining, "all expired stats rows past cutoff should be deleted across batches")
}
