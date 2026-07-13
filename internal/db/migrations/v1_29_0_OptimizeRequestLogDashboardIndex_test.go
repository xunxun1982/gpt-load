package db

import (
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV1_29_0_OptimizeRequestLogDashboardIndexCreatesCompositeIndex(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.RequestLog{}))

	require.NoError(t, V1_29_0_OptimizeRequestLogDashboardIndex(db))

	require.True(t, db.Migrator().HasIndex("request_logs", requestLogTypeTimestampIndex))
}

func TestV1_29_0_OptimizeRequestLogDashboardIndexIsUsedByRPMQuery(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.RequestLog{}))
	require.NoError(t, V1_29_0_OptimizeRequestLogDashboardIndex(db))

	var plans []struct {
		Detail string
	}
	now := time.Now()
	require.NoError(t, db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT
			count(case when timestamp >= ? then 1 end) as current_requests,
			count(case when timestamp >= ? and timestamp < ? then 1 end) as previous_requests
		FROM request_logs
		WHERE timestamp >= ? AND request_type = ?
	`, now.Add(-10*time.Minute), now.Add(-20*time.Minute), now.Add(-10*time.Minute),
		now.Add(-20*time.Minute), models.RequestTypeFinal).Scan(&plans).Error)

	joinedPlan := ""
	for _, plan := range plans {
		joinedPlan += plan.Detail + "\n"
	}
	require.Contains(t, joinedPlan, requestLogTypeTimestampIndex)
}
