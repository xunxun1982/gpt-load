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
	require.NoError(t, db.AutoMigrate(&models.APIKey{}, &models.RequestLog{}, &models.GroupHourlyStat{}, &models.ModelTokenHourlyStat{}))

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

func TestRequestLogServiceWriteLogsToDBAggregatesModelTokenStats(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.RequestLog{}, &models.GroupHourlyStat{}, &models.ModelTokenHourlyStat{}))

	baseTime := time.Date(2026, 5, 30, 10, 10, 0, 0, time.UTC)
	logs := []*models.RequestLog{
		{
			ID:           "token-1",
			Timestamp:    baseTime,
			GroupID:      1,
			Model:        "gpt-4o",
			IsSuccess:    true,
			StatusCode:   200,
			RequestType:  models.RequestTypeFinal,
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
		{
			ID:           "token-2",
			Timestamp:    baseTime.Add(time.Minute),
			GroupID:      1,
			Model:        "gpt-4o",
			IsSuccess:    false,
			StatusCode:   429,
			RequestType:  models.RequestTypeRetry,
			InputTokens:  100,
			OutputTokens: 100,
			TotalTokens:  200,
		},
		{
			ID:              "token-3",
			Timestamp:       baseTime.Add(2 * time.Minute),
			GroupID:         2,
			ParentGroupID:   9,
			Model:           "claude-sonnet-4",
			IsSuccess:       true,
			StatusCode:      200,
			RequestType:     models.RequestTypeFinal,
			InputTokens:     20,
			OutputTokens:    10,
			TotalTokens:     30,
			CacheReadTokens: 7,
		},
		{
			ID:               "token-4",
			Timestamp:        baseTime.Add(3 * time.Minute),
			GroupID:          2,
			ParentGroupID:    9,
			Model:            "claude-sonnet-4",
			IsSuccess:        true,
			StatusCode:       200,
			RequestType:      models.RequestTypeFinal,
			InputTokens:      5,
			OutputTokens:     3,
			TotalTokens:      8,
			CacheWriteTokens: 2,
			ThinkingTokens:   1,
			TokenUsageSource: models.TokenUsageSourceEstimated,
		},
		{
			ID:           "token-5",
			Timestamp:    baseTime.Add(4 * time.Minute),
			GroupID:      1,
			Model:        "legacy-type",
			IsSuccess:    true,
			StatusCode:   200,
			RequestType:  "chat",
			InputTokens:  100,
			OutputTokens: 100,
			TotalTokens:  200,
		},
	}

	service := &RequestLogService{db: db}
	require.NoError(t, service.writeLogsToDB(logs))

	var direct models.ModelTokenHourlyStat
	require.NoError(t, db.Where("group_id = ? AND parent_group_id = ? AND model = ?", 1, 0, "gpt-4o").First(&direct).Error)
	assert.EqualValues(t, 1, direct.RequestCount)
	assert.EqualValues(t, 10, direct.InputTokens)
	assert.EqualValues(t, 5, direct.OutputTokens)
	assert.EqualValues(t, 15, direct.TotalTokens)

	var aggregate models.ModelTokenHourlyStat
	require.NoError(t, db.Where("group_id = ? AND parent_group_id = ? AND model = ?", 2, 9, "claude-sonnet-4").First(&aggregate).Error)
	assert.EqualValues(t, 2, aggregate.RequestCount)
	assert.EqualValues(t, 25, aggregate.InputTokens)
	assert.EqualValues(t, 13, aggregate.OutputTokens)
	assert.EqualValues(t, 38, aggregate.TotalTokens)
	assert.EqualValues(t, 7, aggregate.CacheReadTokens)
	assert.EqualValues(t, 2, aggregate.CacheWriteTokens)
	assert.EqualValues(t, 1, aggregate.ThinkingTokens)
	assert.EqualValues(t, 8, aggregate.EstimatedTokens)
	assert.EqualValues(t, 1, aggregate.EstimatedRequestCount)

	var count int64
	require.NoError(t, db.Model(&models.ModelTokenHourlyStat{}).Count(&count).Error)
	assert.EqualValues(t, 2, count)
}
