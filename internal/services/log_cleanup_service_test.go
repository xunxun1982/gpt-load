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
