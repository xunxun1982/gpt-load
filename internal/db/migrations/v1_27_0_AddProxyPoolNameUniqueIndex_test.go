package db

import (
	"strings"
	"testing"
	"time"

	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type proxyPoolItemLegacyV1_27_0 struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	Name      string `gorm:"type:varchar(255);not null"`
	URL       string `gorm:"type:text;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (proxyPoolItemLegacyV1_27_0) TableName() string {
	return "proxy_pool_items"
}

func TestV1_27_0_AddProxyPoolNameUniqueIndexRenamesExistingDuplicates(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&proxyPoolItemLegacyV1_27_0{}))
	require.NoError(t, db.Create(&proxyPoolItemLegacyV1_27_0{Name: "duplicate", URL: "http://proxy-a.example.com:8080"}).Error)
	require.NoError(t, db.Create(&proxyPoolItemLegacyV1_27_0{Name: "duplicate", URL: "http://proxy-b.example.com:8080"}).Error)
	require.NoError(t, db.Create(&proxyPoolItemLegacyV1_27_0{Name: "unique", URL: "http://proxy-c.example.com:8080"}).Error)

	require.NoError(t, V1_27_0_AddProxyPoolNameUniqueIndex(db))

	require.True(t, db.Migrator().HasIndex("proxy_pool_items", proxyPoolNameUniqueIndex))
	var items []models.ProxyPoolItem
	require.NoError(t, db.Order("id ASC").Find(&items).Error)
	require.Len(t, items, 3)
	assert.Equal(t, "duplicate", items[0].Name)
	assert.Equal(t, "duplicate-2", items[1].Name)
	assert.Equal(t, "unique", items[2].Name)
}

func TestV1_27_0_AddProxyPoolNameUniqueIndexKeepsRenamedNamesWithinLimit(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&proxyPoolItemLegacyV1_27_0{}))

	longName := strings.Repeat("a", proxyPoolNameMaxLength)
	require.NoError(t, db.Create(&proxyPoolItemLegacyV1_27_0{Name: longName, URL: "http://proxy-a.example.com:8080"}).Error)
	require.NoError(t, db.Create(&proxyPoolItemLegacyV1_27_0{Name: longName, URL: "http://proxy-b.example.com:8080"}).Error)

	require.NoError(t, V1_27_0_AddProxyPoolNameUniqueIndex(db))

	var items []models.ProxyPoolItem
	require.NoError(t, db.Order("id ASC").Find(&items).Error)
	require.Len(t, items, 2)
	assert.Len(t, []rune(items[1].Name), proxyPoolNameMaxLength)
	assert.Equal(t, strings.Repeat("a", proxyPoolNameMaxLength-2)+"-2", items[1].Name)
}

func TestV1_27_0_AddProxyPoolNameUniqueIndexSkipsWhenMarkerAlreadyAcquired(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&proxyPoolItemLegacyV1_27_0{}))
	require.NoError(t, db.Create(&proxyPoolItemLegacyV1_27_0{Name: "duplicate", URL: "http://proxy-a.example.com:8080"}).Error)
	require.NoError(t, db.Create(&proxyPoolItemLegacyV1_27_0{Name: "duplicate", URL: "http://proxy-b.example.com:8080"}).Error)
	require.NoError(t, ensureDataMigrationsTable(db))
	require.NoError(t, db.Create(&dataMigrationMarker{
		Version:   proxyPoolNameUniqueIndexMigrationVersion,
		CreatedAt: time.Now().UTC(),
	}).Error)

	require.NoError(t, V1_27_0_AddProxyPoolNameUniqueIndex(db))

	require.False(t, db.Migrator().HasIndex("proxy_pool_items", proxyPoolNameUniqueIndex))
	var items []models.ProxyPoolItem
	require.NoError(t, db.Order("id ASC").Find(&items).Error)
	require.Len(t, items, 2)
	assert.Equal(t, "duplicate", items[0].Name)
	assert.Equal(t, "duplicate", items[1].Name)
}
