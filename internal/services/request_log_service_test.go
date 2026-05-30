package services

import (
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRequestLogServiceWriteLogsToDBUpdatesKeyStatsByGroupAndHash(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.APIKey{}, &models.RequestLog{}, &models.GroupHourlyStat{}))

	const sharedHash = "shared-hash"
	keys := []models.APIKey{
		{GroupID: 1, KeyValue: "key-1", KeyHash: sharedHash, Status: models.KeyStatusActive},
		{GroupID: 2, KeyValue: "key-2", KeyHash: sharedHash, Status: models.KeyStatusActive},
	}
	require.NoError(t, db.Create(&keys).Error)

	baseTime := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	logs := []*models.RequestLog{
		{ID: "1", Timestamp: baseTime, GroupID: 1, KeyHash: sharedHash, IsSuccess: true, StatusCode: 200, RequestType: models.RequestTypeFinal},
		{ID: "2", Timestamp: baseTime.Add(time.Minute), GroupID: 1, KeyHash: sharedHash, IsSuccess: true, StatusCode: 200, RequestType: models.RequestTypeFinal},
		{ID: "3", Timestamp: baseTime.Add(2 * time.Minute), GroupID: 2, KeyHash: sharedHash, IsSuccess: true, StatusCode: 200, RequestType: models.RequestTypeFinal},
		{ID: "4", Timestamp: baseTime.Add(3 * time.Minute), GroupID: 2, KeyHash: sharedHash, IsSuccess: false, StatusCode: 500, RequestType: models.RequestTypeFinal},
	}

	service := &RequestLogService{db: db}
	require.NoError(t, service.writeLogsToDB(logs))

	var group1Key models.APIKey
	require.NoError(t, db.First(&group1Key, keys[0].ID).Error)
	assert.EqualValues(t, 2, group1Key.RequestCount)
	require.NotNil(t, group1Key.LastUsedAt)
	assert.True(t, group1Key.LastUsedAt.Equal(baseTime.Add(time.Minute)))

	var group2Key models.APIKey
	require.NoError(t, db.First(&group2Key, keys[1].ID).Error)
	assert.EqualValues(t, 1, group2Key.RequestCount)
	require.NotNil(t, group2Key.LastUsedAt)
	assert.True(t, group2Key.LastUsedAt.Equal(baseTime.Add(2*time.Minute)))
}
