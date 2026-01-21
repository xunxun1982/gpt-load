package sitemanagement

import (
	"context"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/services"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// createTestGroup creates a test group with required fields
func createTestGroup(db *gorm.DB, name string, opts ...func(*models.Group)) *models.Group {
	group := &models.Group{
		Name:      name,
		Upstreams: []byte("[]"), // Required JSON field
	}
	for _, opt := range opts {
		opt(group)
	}
	db.Create(group)
	return group
}

// TestBindingService_BindGroupToSite tests binding a group to a site
func TestBindingService_BindGroupToSite(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create group
	group := createTestGroup(db, "Test Group")
	require.NotZero(t, group.ID)

	// Bind group to site
	err = service.BindGroupToSite(context.Background(), group.ID, site.ID)
	require.NoError(t, err)

	// Verify binding
	var updated models.Group
	err = db.First(&updated, group.ID).Error
	require.NoError(t, err)
	require.NotNil(t, updated.BoundSiteID)
	assert.Equal(t, site.ID, *updated.BoundSiteID)
}

// TestBindingService_BindGroupToSite_AggregateGroup tests that aggregate groups cannot be bound
func TestBindingService_BindGroupToSite_AggregateGroup(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create aggregate group
	group := createTestGroup(db, "Aggregate Group", func(g *models.Group) {
		g.GroupType = "aggregate"
	})
	require.NotZero(t, group.ID)

	// Try to bind aggregate group (should fail)
	err = service.BindGroupToSite(context.Background(), group.ID, site.ID)
	assert.Error(t, err)
}

// TestBindingService_BindGroupToSite_ChildGroup tests that child groups cannot be bound
func TestBindingService_BindGroupToSite_ChildGroup(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create parent group
	parent := createTestGroup(db, "Parent Group")
	require.NotZero(t, parent.ID)

	// Create child group
	child := createTestGroup(db, "Child Group", func(g *models.Group) {
		g.ParentGroupID = &parent.ID
	})
	require.NotZero(t, child.ID)

	// Try to bind child group (should fail)
	err = service.BindGroupToSite(context.Background(), child.ID, site.ID)
	assert.Error(t, err)
}

// TestBindingService_UnbindGroupFromSite tests unbinding a group from a site
func TestBindingService_UnbindGroupFromSite(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create bound group
	group := createTestGroup(db, "Test Group", func(g *models.Group) {
		g.BoundSiteID = &site.ID
	})
	require.NotZero(t, group.ID)

	// Unbind group
	err = service.UnbindGroupFromSite(context.Background(), group.ID)
	require.NoError(t, err)

	// Verify unbinding
	var updated models.Group
	err = db.First(&updated, group.ID).Error
	require.NoError(t, err)
	assert.Nil(t, updated.BoundSiteID)
}

// TestBindingService_UnbindSiteFromGroup tests unbinding all groups from a site
func TestBindingService_UnbindSiteFromGroup(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create multiple bound groups
	for i := 0; i < 3; i++ {
		createTestGroup(db, "Group "+string(rune('A'+i)), func(g *models.Group) {
			g.BoundSiteID = &site.ID
		})
	}

	// Unbind all groups from site
	err = service.UnbindSiteFromGroup(context.Background(), site.ID)
	require.NoError(t, err)

	// Verify all groups are unbound
	var count int64
	err = db.Model(&models.Group{}).Where("bound_site_id = ?", site.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestBindingService_SyncSiteEnabledToGroup tests syncing site enabled status to groups
func TestBindingService_SyncSiteEnabledToGroup(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
		Enabled: true,
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create bound groups
	var groupIDs []uint
	for i := 0; i < 3; i++ {
		group := createTestGroup(db, "Group "+string(rune('A'+i)), func(g *models.Group) {
			g.BoundSiteID = &site.ID
			g.Enabled = true
		})
		groupIDs = append(groupIDs, group.ID)
	}

	// Disable site and sync to groups
	err = service.SyncSiteEnabledToGroup(context.Background(), site.ID, false)
	require.NoError(t, err)

	// Verify all bound groups are disabled
	for _, groupID := range groupIDs {
		var group models.Group
		err = db.First(&group, groupID).Error
		require.NoError(t, err)
		assert.False(t, group.Enabled)
	}
}

// TestBindingService_GetBoundGroupInfo tests getting bound group information
func TestBindingService_GetBoundGroupInfo(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create bound groups
	for i := 0; i < 3; i++ {
		createTestGroup(db, "Group "+string(rune('A'+i)), func(g *models.Group) {
			g.DisplayName = "Display " + string(rune('A'+i))
			g.BoundSiteID = &site.ID
			g.Enabled = true
		})
	}

	// Get bound group info
	info, err := service.GetBoundGroupInfo(context.Background(), site.ID)
	require.NoError(t, err)
	assert.Len(t, info, 3)
	assert.Equal(t, "Group A", info[0].Name)
}

// TestBindingService_ListSitesForBinding tests listing sites for binding
func TestBindingService_ListSitesForBinding(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create sites
	site1 := ManagedSite{
		Name:    "Site A",
		BaseURL: "https://example.com",
		Sort:    1,
	}
	err = db.Create(&site1).Error
	require.NoError(t, err)

	site2 := ManagedSite{
		Name:    "Site B",
		BaseURL: "https://example.com",
		Sort:    2,
	}
	err = db.Create(&site2).Error
	require.NoError(t, err)

	// Create group bound to site1
	group := createTestGroup(db, "Test Group", func(g *models.Group) {
		g.BoundSiteID = &site1.ID
	})
	require.NotZero(t, group.ID)

	// List sites for binding
	sites, err := service.ListSitesForBinding(context.Background())
	require.NoError(t, err)
	assert.Len(t, sites, 2)
	assert.Equal(t, int64(1), sites[0].BoundGroupCount)
	assert.Equal(t, int64(0), sites[1].BoundGroupCount)
}

// TestBindingService_CheckGroupCanDelete tests group deletion check
func TestBindingService_CheckGroupCanDelete(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Create bound group
	boundGroup := createTestGroup(db, "Bound Group", func(g *models.Group) {
		g.BoundSiteID = &site.ID
	})
	require.NotZero(t, boundGroup.ID)

	// Create unbound group
	unboundGroup := createTestGroup(db, "Unbound Group")
	require.NotZero(t, unboundGroup.ID)

	// Check bound group (should fail)
	err = service.CheckGroupCanDelete(context.Background(), boundGroup.ID)
	assert.Error(t, err)

	// Check unbound group (should succeed)
	err = service.CheckGroupCanDelete(context.Background(), unboundGroup.ID)
	assert.NoError(t, err)
}

// TestBindingService_CheckSiteCanDelete tests site deletion check
func TestBindingService_CheckSiteCanDelete(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create sites
	boundSite := ManagedSite{
		Name:    "Bound Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&boundSite).Error
	require.NoError(t, err)

	unboundSite := ManagedSite{
		Name:    "Unbound Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&unboundSite).Error
	require.NoError(t, err)

	// Create group bound to site
	group := createTestGroup(db, "Test Group", func(g *models.Group) {
		g.BoundSiteID = &boundSite.ID
	})
	require.NotZero(t, group.ID)

	// Check bound site (should fail)
	err = service.CheckSiteCanDelete(context.Background(), boundSite.ID)
	assert.Error(t, err)

	// Check unbound site (should succeed)
	err = service.CheckSiteCanDelete(context.Background(), unboundSite.ID)
	assert.NoError(t, err)
}

// TestBindingService_CacheInvalidation tests cache invalidation callback
func TestBindingService_CacheInvalidation(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Set up cache invalidation callback
	invalidated := false
	service.CacheInvalidationCallback = func() {
		invalidated = true
	}

	// Create site and group
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	group := createTestGroup(db, "Test Group")
	require.NotZero(t, group.ID)

	// Bind group (should trigger callback)
	err = service.BindGroupToSite(context.Background(), group.ID, site.ID)
	require.NoError(t, err)
	assert.True(t, invalidated)

	// Reset flag
	invalidated = false

	// Unbind group (should trigger callback)
	err = service.UnbindGroupFromSite(context.Background(), group.ID)
	require.NoError(t, err)
	assert.True(t, invalidated)
}

// TestBindingService_ManyToOne tests many-to-one relationship (multiple groups to one site)
func TestBindingService_ManyToOne(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Bind multiple groups to the same site
	for i := 0; i < 5; i++ {
		group := createTestGroup(db, "Group "+string(rune('A'+i)))
		require.NotZero(t, group.ID)

		err = service.BindGroupToSite(context.Background(), group.ID, site.ID)
		require.NoError(t, err)
	}

	// Verify all groups are bound to the same site
	var count int64
	err = db.Model(&models.Group{}).Where("bound_site_id = ?", site.ID).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(5), count)
}

// BenchmarkBindingService_BindGroupToSite benchmarks binding operation
func BenchmarkBindingService_BindGroupToSite(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	db.AutoMigrate(&ManagedSite{}, &models.Group{})

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create site
	site := ManagedSite{
		Name:    "Test Site",
		BaseURL: "https://example.com",
	}
	db.Create(&site)

	// Create groups
	var groupIDs []uint
	for i := 0; i < 100; i++ {
		group := createTestGroup(db, "Group "+string(rune(i)))
		groupIDs = append(groupIDs, group.ID)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % len(groupIDs)
		service.BindGroupToSite(context.Background(), groupIDs[idx], site.ID)
	}
}

// BenchmarkBindingService_ListSitesForBinding benchmarks site listing
func BenchmarkBindingService_ListSitesForBinding(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	db.AutoMigrate(&ManagedSite{}, &models.Group{})

	service := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)

	// Create 100 sites
	for i := 0; i < 100; i++ {
		site := ManagedSite{
			Name:    "Site " + string(rune(i)),
			BaseURL: "https://example.com",
			Sort:    i,
		}
		db.Create(&site)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		service.ListSitesForBinding(context.Background())
	}
}
