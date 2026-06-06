package db

import (
	"testing"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV1_25_0_OptimizeAPIKeyIndexesCreatesIndexes(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.APIKey{}))

	require.NoError(t, V1_25_0_OptimizeAPIKeyIndexes(db))

	require.True(t, db.Migrator().HasIndex("api_keys", apiKeyGroupOrderIndexV2))
	require.True(t, db.Migrator().HasIndex("api_keys", apiKeyGroupHashIndex))
	require.True(t, db.Migrator().HasIndex("api_keys", apiKeyGroupIDIDIndex))
}

func TestV1_25_0_OptimizeAPIKeyIndexesUsesGroupIDIDIndexForExportCursor(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.APIKey{}))
	require.NoError(t, V1_25_0_OptimizeAPIKeyIndexes(db))

	for i := 0; i < 20; i++ {
		require.NoError(t, db.Create(&models.APIKey{
			GroupID:  uint(i%2 + 1),
			KeyValue: "encrypted-key",
			KeyHash:  "hash-export-cursor",
		}).Error)
	}

	var plans []struct {
		Detail string
	}
	require.NoError(t, db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT id, group_id, key_value, status
		FROM api_keys
		WHERE group_id IN (?, ?)
			AND (group_id > ? OR (group_id = ? AND id > ?))
		ORDER BY group_id ASC, id ASC
		LIMIT ?
	`, 1, 2, 0, 0, 0, 500).Scan(&plans).Error)

	joinedPlan := ""
	for _, plan := range plans {
		joinedPlan += plan.Detail + "\n"
	}
	require.Contains(t, joinedPlan, apiKeyGroupIDIDIndex)
	require.NotContains(t, joinedPlan, "USE TEMP B-TREE")
}
