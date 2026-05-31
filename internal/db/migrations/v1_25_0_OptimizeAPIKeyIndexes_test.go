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
}
