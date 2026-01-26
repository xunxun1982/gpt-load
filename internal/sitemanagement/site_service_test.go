package sitemanagement

import (
	"context"
	"fmt"
	"testing"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestSiteService_CreateSite tests site creation
func TestSiteService_CreateSite(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	params := CreateSiteParams{
		Name:             "Test Site",
		Notes:            "Test notes",
		Description:      "Test description",
		Sort:             1,
		Enabled:          true,
		BaseURL:          "https://example.com",
		SiteType:         SiteTypeNewAPI,
		UserID:           "user123",
		CheckInPageURL:   "https://example.com/checkin",
		CheckInAvailable: true,
		CheckInEnabled:   true,
		AuthType:         AuthTypeAccessToken,
		AuthValue:        "test-token",
	}

	dto, err := service.CreateSite(context.Background(), params)
	require.NoError(t, err)
	assert.NotNil(t, dto)
	assert.Equal(t, "Test Site", dto.Name)
	assert.Equal(t, "https://example.com", dto.BaseURL)
	assert.Equal(t, "user123", dto.UserID) // Should be decrypted in DTO
	assert.True(t, dto.HasAuth)
}

// TestSiteService_CreateSite_DuplicateName tests duplicate name validation
func TestSiteService_CreateSite_DuplicateName(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	params := CreateSiteParams{
		Name:     "Duplicate Site",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	}

	// Create first site
	_, err = service.CreateSite(context.Background(), params)
	require.NoError(t, err)

	// Try to create duplicate
	_, err = service.CreateSite(context.Background(), params)
	assert.Error(t, err)
}

// TestSiteService_UpdateSite tests site update
func TestSiteService_UpdateSite(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create site
	createParams := CreateSiteParams{
		Name:     "Original Name",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
		Enabled:  true,
	}
	dto, err := service.CreateSite(context.Background(), createParams)
	require.NoError(t, err)

	// Update site
	newName := "Updated Name"
	newEnabled := false
	updateParams := UpdateSiteParams{
		Name:    &newName,
		Enabled: &newEnabled,
	}

	updated, err := service.UpdateSite(context.Background(), dto.ID, updateParams)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", updated.Name)
	assert.False(t, updated.Enabled)
}

// TestSiteService_DeleteSite tests site deletion
func TestSiteService_DeleteSite(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create site
	params := CreateSiteParams{
		Name:     "To Delete",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	}
	dto, err := service.CreateSite(context.Background(), params)
	require.NoError(t, err)

	// Delete site
	err = service.DeleteSite(context.Background(), dto.ID)
	require.NoError(t, err)

	// Verify deletion
	var count int64
	err = db.Model(&ManagedSite{}).Where("id = ?", dto.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestSiteService_ListSites tests site listing with cache
func TestSiteService_ListSites(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create multiple sites
	for i := 0; i < 5; i++ {
		params := CreateSiteParams{
			Name:     "Site " + string(rune('A'+i)),
			BaseURL:  "https://example.com",
			Sort:     i,
			AuthType: AuthTypeNone,
		}
		_, err := service.CreateSite(context.Background(), params)
		require.NoError(t, err)
	}

	// List sites (first call - cache miss)
	sites, err := service.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 5)

	// List sites again (should hit cache)
	sites2, err := service.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites2, 5)
}

// TestSiteService_CopySite tests site copying
func TestSiteService_CopySite(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create original site
	params := CreateSiteParams{
		Name:      "Original",
		BaseURL:   "https://example.com",
		AuthType:  AuthTypeAccessToken,
		AuthValue: "test-token",
	}
	original, err := service.CreateSite(context.Background(), params)
	require.NoError(t, err)

	// Copy site
	copied, err := service.CopySite(context.Background(), original.ID)
	require.NoError(t, err)
	assert.NotEqual(t, original.ID, copied.ID)
	assert.NotEqual(t, original.Name, copied.Name)
	assert.Equal(t, original.BaseURL, copied.BaseURL)
	assert.Equal(t, original.HasAuth, copied.HasAuth)
}

// TestSiteService_RecordSiteOpened tests recording site opened
func TestSiteService_RecordSiteOpened(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create site
	params := CreateSiteParams{
		Name:     "Test Site",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	}
	dto, err := service.CreateSite(context.Background(), params)
	require.NoError(t, err)

	// Record site opened
	err = service.RecordSiteOpened(context.Background(), dto.ID)
	require.NoError(t, err)

	// Verify record
	var site ManagedSite
	err = db.First(&site, dto.ID).Error
	require.NoError(t, err)
	assert.NotEmpty(t, site.LastSiteOpenedDate)
}

// TestSiteService_AutoCheckinConfig tests auto-checkin config management
func TestSiteService_AutoCheckinConfig(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSiteSetting{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Get default config
	cfg, err := service.GetAutoCheckinConfig(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.False(t, cfg.GlobalEnabled)

	// Update config
	newCfg := AutoCheckinConfig{
		GlobalEnabled: true,
		ScheduleTimes: []string{"09:00", "15:00"},
		ScheduleMode:  AutoCheckinScheduleModeMultiple,
	}
	updated, err := service.UpdateAutoCheckinConfig(context.Background(), newCfg)
	require.NoError(t, err)
	assert.True(t, updated.GlobalEnabled)
	assert.Len(t, updated.ScheduleTimes, 2)
}

// TestSiteService_ExportImport tests export and import functionality
func TestSiteService_ExportImport(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &ManagedSiteSetting{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create sites
	for i := 0; i < 3; i++ {
		params := CreateSiteParams{
			Name:     "Site " + string(rune('A'+i)),
			BaseURL:  "https://example.com",
			AuthType: AuthTypeNone,
		}
		_, err := service.CreateSite(context.Background(), params)
		require.NoError(t, err)
	}

	// Export sites
	exportData, err := service.ExportSites(context.Background(), false, true)
	require.NoError(t, err)
	assert.Len(t, exportData.Sites, 3)

	// Clear database
	err = db.Exec("DELETE FROM managed_sites").Error
	require.NoError(t, err)

	// Import sites
	imported, skipped, err := service.ImportSites(context.Background(), exportData, true)
	require.NoError(t, err)
	assert.Equal(t, 3, imported)
	assert.Equal(t, 0, skipped)
}

// TestSiteService_CacheInvalidation tests cache invalidation
func TestSiteService_CacheInvalidation(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create site
	params := CreateSiteParams{
		Name:     "Test Site",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	}
	_, err = service.CreateSite(context.Background(), params)
	require.NoError(t, err)

	// List sites to populate cache
	sites1, err := service.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites1, 1)

	// Invalidate cache
	service.InvalidateSiteListCache()

	// Create another site
	params.Name = "Another Site"
	_, err = service.CreateSite(context.Background(), params)
	require.NoError(t, err)

	// List sites again (should fetch from DB)
	sites2, err := service.ListSites(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites2, 2)
}

// TestSiteService_DeleteAllUnboundSites tests bulk deletion of unbound sites
func TestSiteService_DeleteAllUnboundSites(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create sites
	site1, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Bound Site",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	site2, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Unbound Site",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	// Create group bound to site1
	group := models.Group{
		Name:        "Test Group",
		BoundSiteID: &site1.ID,
		Upstreams:   []byte("[]"),
	}
	err = db.Create(&group).Error
	require.NoError(t, err)

	// Delete unbound sites
	deleted, err := service.DeleteAllUnboundSites(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// Verify site1 still exists
	var count int64
	err = db.Model(&ManagedSite{}).Where("id = ?", site1.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Verify site2 is deleted
	err = db.Model(&ManagedSite{}).Where("id = ?", site2.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestSiteService_ListSitesPaginated tests paginated site listing
func TestSiteService_ListSitesPaginated(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	// Create 15 sites with explicit enabled values
	// GORM uses database default values for zero values, so we create then update
	for i := 0; i < 15; i++ {
		enabled := i%2 == 0
		site := &ManagedSite{
			Name:     "Site " + string(rune('A'+i)),
			BaseURL:  "https://example.com",
			Sort:     i,
			AuthType: AuthTypeNone,
		}
		err := db.Create(site).Error
		require.NoError(t, err)

		// Update enabled field explicitly (GORM respects updates even for false)
		err = db.Model(site).Update("enabled", enabled).Error
		require.NoError(t, err)
	}

	// Test pagination
	result, err := service.ListSitesPaginated(context.Background(), SiteListParams{
		Page:     1,
		PageSize: 10,
	})
	require.NoError(t, err)
	assert.Len(t, result.Sites, 10)
	assert.Equal(t, int64(15), result.Total)
	assert.Equal(t, 2, result.TotalPages)

	// Test filtering by enabled
	enabled := true

	// Debug: check database state
	var dbCount int64
	err = db.Model(&ManagedSite{}).Where("enabled = ?", true).Count(&dbCount).Error
	require.NoError(t, err)
	t.Logf("DB count of enabled sites: %d", dbCount)

	result, err = service.ListSitesPaginated(context.Background(), SiteListParams{
		Page:     1,
		PageSize: 10,
		Enabled:  &enabled,
	})
	require.NoError(t, err)
	t.Logf("Result.Total: %d", result.Total)
	// i%2==0 for i in [0,14]: 0,2,4,6,8,10,12,14 = 8 enabled sites
	assert.Equal(t, int64(8), result.Total, "Should have 8 enabled sites")
	for _, site := range result.Sites {
		assert.True(t, site.Enabled, "All returned sites should be enabled")
	}
}

// BenchmarkSiteService_ListSites benchmarks site listing
func BenchmarkSiteService_ListSites(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	memStore := store.NewMemoryStore()

	db.AutoMigrate(&ManagedSite{}, &models.Group{})

	service := NewSiteService(db, memStore, encSvc)

	// Create 100 sites
	for i := 0; i < 100; i++ {
		params := CreateSiteParams{
			Name:     fmt.Sprintf("Site %d", i),
			BaseURL:  "https://example.com",
			AuthType: AuthTypeNone,
		}
		service.CreateSite(context.Background(), params)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		service.ListSites(context.Background())
	}
}

// BenchmarkSiteService_CreateSite benchmarks site creation
func BenchmarkSiteService_CreateSite(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	memStore := store.NewMemoryStore()

	db.AutoMigrate(&ManagedSite{})

	service := NewSiteService(db, memStore, encSvc)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		params := CreateSiteParams{
			Name:     fmt.Sprintf("Site %d", i),
			BaseURL:  "https://example.com",
			AuthType: AuthTypeNone,
		}
		service.CreateSite(context.Background(), params)
	}
}

// BenchmarkSiteService_CacheHit benchmarks cache hit performance
func BenchmarkSiteService_CacheHit(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	memStore := store.NewMemoryStore()

	db.AutoMigrate(&ManagedSite{}, &models.Group{})

	service := NewSiteService(db, memStore, encSvc)

	// Create sites
	for i := 0; i < 50; i++ {
		params := CreateSiteParams{
			Name:     fmt.Sprintf("Site %d", i),
			BaseURL:  "https://example.com",
			AuthType: AuthTypeNone,
		}
		service.CreateSite(context.Background(), params)
	}

	// Warm up cache
	service.ListSites(context.Background())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		service.ListSites(context.Background())
	}
}
