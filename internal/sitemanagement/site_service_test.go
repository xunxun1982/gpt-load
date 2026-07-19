package sitemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/store"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSiteServiceUpdateInvalidatesBindingSnapshot(t *testing.T) {
	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))

	site := ManagedSite{
		Name:    "Old Site Name",
		BaseURL: "https://example.com",
		Enabled: true,
	}
	require.NoError(t, db.Create(&site).Error)
	bindingService := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)
	siteService := NewSiteService(db, memStore, encSvc)
	siteService.SetCacheInvalidationCallback(bindingService.InvalidateSitesForBindingCache)

	initial, err := bindingService.ListSitesForBinding(context.Background())
	require.NoError(t, err)
	require.Len(t, initial, 1)
	assert.Equal(t, "Old Site Name", initial[0].Name)

	newName := "New Site Name"
	_, err = siteService.UpdateSite(context.Background(), site.ID, UpdateSiteParams{Name: &newName})
	require.NoError(t, err)

	refreshed, err := bindingService.ListSitesForBinding(context.Background())
	require.NoError(t, err)
	require.Len(t, refreshed, 1)
	assert.Equal(t, newName, refreshed[0].Name)
}

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
	assert.Equal(t, int64(1), dto.BalanceMultiplier)
}

func TestSiteServiceRejectsInvalidBalanceMultiplier(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))
	service := NewSiteService(db, nil, encSvc)

	zero := int64(0)
	_, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:              "Invalid multiplier",
		BaseURL:           "https://example.com",
		AuthType:          AuthTypeNone,
		BalanceMultiplier: &zero,
	})
	require.Error(t, err)

	site, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Valid multiplier",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	negative := int64(-1)
	_, err = service.UpdateSite(context.Background(), site.ID, UpdateSiteParams{
		BalanceMultiplier: &negative,
	})
	require.Error(t, err)

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, int64(1), stored.BalanceMultiplier)
}

func TestSiteServiceBalanceMultiplierUsesScaledCachedOutputs(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))

	bindingService := NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)
	service := NewSiteService(db, nil, encSvc)
	service.SetCacheInvalidationCallback(bindingService.InvalidateSitesForBindingCache)

	site, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Scaled balance",
		BaseURL:  "https://example.com",
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&ManagedSite{}).Where("id = ?", site.ID).
		Update("last_balance", "$120.00").Error)

	before, err := bindingService.ListSitesForBinding(context.Background())
	require.NoError(t, err)
	require.Len(t, before, 1)
	assert.Equal(t, "$120.00", before[0].LastBalance)

	multiplier := int64(3)
	updated, err := service.UpdateSite(context.Background(), site.ID, UpdateSiteParams{
		BalanceMultiplier: &multiplier,
	})
	require.NoError(t, err)
	assert.Equal(t, multiplier, updated.BalanceMultiplier)
	assert.Equal(t, "$40.00", updated.LastBalance)

	after, err := bindingService.ListSitesForBinding(context.Background())
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, multiplier, after[0].BalanceMultiplier)
	assert.Equal(t, "$40.00", after[0].LastBalance)

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, "$120.00", stored.LastBalance)
	assert.Equal(t, multiplier, stored.BalanceMultiplier)
}

func TestSiteServiceCopyPreservesBalanceMultiplier(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))
	service := NewSiteService(db, nil, encSvc)
	multiplier := int64(7)

	source, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:              "Multiplier source",
		BaseURL:           "https://example.com",
		AuthType:          AuthTypeNone,
		BalanceMultiplier: &multiplier,
	})
	require.NoError(t, err)

	copied, err := service.CopySite(context.Background(), source.ID)
	require.NoError(t, err)
	assert.Equal(t, multiplier, copied.BalanceMultiplier)
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

func TestSiteServiceRejectsProviderConfigurationsThatCannotRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		siteType  string
		authType  string
		authValue string
		userID    string
		bypass    string
		checkinOn bool
		customURL string
		wantError bool
	}{
		{
			name:      "anyrouter access token",
			siteType:  SiteTypeAnyrouter,
			authType:  AuthTypeAccessToken,
			authValue: "token",
			userID:    "42",
			wantError: true,
		},
		{
			name:      "anyrouter missing user id",
			siteType:  SiteTypeAnyrouter,
			authType:  AuthTypeCookie,
			authValue: "session=browser",
			wantError: true,
		},
		{
			name:      "stealth access token only",
			siteType:  SiteTypeOneHub,
			authType:  AuthTypeAccessToken,
			authValue: "token",
			bypass:    BypassMethodStealth,
			wantError: true,
		},
		{
			name:      "stealth cookie without waf cookie",
			siteType:  SiteTypeOneHub,
			authType:  AuthTypeCookie,
			authValue: "session=browser",
			bypass:    BypassMethodStealth,
			wantError: true,
		},
		{
			name:      "sub2api automatic checkin without endpoint",
			siteType:  SiteTypeSub2API,
			authType:  AuthTypeAccessToken,
			authValue: "token",
			checkinOn: true,
			wantError: true,
		},
		{
			name:      "sub2api cookie only cannot fetch balance",
			siteType:  SiteTypeSub2API,
			authType:  AuthTypeCookie,
			authValue: "session=browser",
			wantError: true,
		},
		{
			name:      "sub2api active site requires a credential",
			siteType:  SiteTypeSub2API,
			authType:  AuthTypeAccessToken,
			wantError: true,
		},
		{
			name:      "sub2api refresh token only is valid",
			siteType:  SiteTypeSub2API,
			authType:  AuthTypeAccessToken,
			authValue: `{"refresh_token":"refresh-token"}`,
		},
		{
			name:      "valid anyrouter cookie configuration",
			siteType:  SiteTypeAnyrouter,
			authType:  AuthTypeCookie,
			authValue: "session=browser",
			userID:    "42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			encSvc := setupTestEncryption(t)
			require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))
			service := NewSiteService(db, store.NewMemoryStore(), encSvc)

			_, err := service.CreateSite(t.Context(), CreateSiteParams{
				Name:             tt.name,
				BaseURL:          "https://example.com",
				Enabled:          true,
				SiteType:         tt.siteType,
				AuthType:         tt.authType,
				AuthValue:        tt.authValue,
				UserID:           tt.userID,
				BypassMethod:     tt.bypass,
				CheckInEnabled:   tt.checkinOn,
				CustomCheckInURL: tt.customURL,
			})
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSiteServiceUpdatePreservesConcurrentAuthRotation(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewSiteService(db, store.NewMemoryStore(), encSvc)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))

	oldAuth, err := encSvc.Encrypt(`{"access_token":"old-token","refresh_token":"old-refresh"}`)
	require.NoError(t, err)
	freshAuth, err := encSvc.Encrypt(`{"access_token":"fresh-token","refresh_token":"fresh-refresh"}`)
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Concurrent auth site",
		BaseURL:   "https://example.com",
		AuthType:  AuthTypeAccessToken,
		AuthValue: oldAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	callbackName := "test:rotate-auth-before-site-update"
	rotated := false
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "managed_sites" || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "ManagedSite" {
			return
		}
		rotated = true
		if err := tx.Exec("UPDATE managed_sites SET auth_value = ? WHERE id = ?", freshAuth, site.ID).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	notes := "edited while token refreshed"
	_, err = service.UpdateSite(context.Background(), site.ID, UpdateSiteParams{Notes: &notes})
	require.NoError(t, err)
	assert.True(t, rotated)

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, freshAuth, stored.AuthValue, "editing a non-auth field must not roll back a concurrent token rotation")
}

func TestSiteServiceEnableRejectsConcurrentCredentialClear(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewSiteService(db, store.NewMemoryStore(), encSvc)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))

	encryptedAuth, err := encSvc.Encrypt("access-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Concurrent configuration validation",
		BaseURL:   "https://example.com",
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
		Enabled:   false,
	}
	require.NoError(t, db.Create(&site).Error)
	require.NoError(t, db.Model(&ManagedSite{}).Where("id = ?", site.ID).Update("enabled", false).Error)

	cleared := false
	callbackName := "test:clear-auth-before-enable"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if cleared || tx.Statement.Table != "managed_sites" || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "ManagedSite" {
			return
		}
		cleared = true
		if err := tx.Exec("UPDATE managed_sites SET auth_value = '' WHERE id = ?", site.ID).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	enabled := true
	_, err = service.UpdateSite(context.Background(), site.ID, UpdateSiteParams{Enabled: &enabled})
	require.Error(t, err)
	assert.True(t, cleared)

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.False(t, stored.Enabled)
	assert.Empty(t, stored.AuthValue)
}

func TestSiteServiceUpdateClearsCredentialsWhenAuthTypeChangesWithoutValue(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewSiteService(db, store.NewMemoryStore(), encSvc)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))

	encryptedAuth, err := encSvc.Encrypt("old-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Auth type switch",
		BaseURL:   "https://example.com",
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	newAuthType := AuthTypeCookie
	_, err = service.UpdateSite(context.Background(), site.ID, UpdateSiteParams{AuthType: &newAuthType})
	require.NoError(t, err)

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, AuthTypeCookie, stored.AuthType)
	assert.Empty(t, stored.AuthValue, "changing auth type without a new value must not retain a credential under the wrong type")
}

func TestSiteServiceAuthTypeUpdateDoesNotOverwriteConcurrentCredentialRotation(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewSiteService(db, store.NewMemoryStore(), encSvc)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))

	oldAuth, err := encSvc.Encrypt("old-token")
	require.NoError(t, err)
	freshAuth, err := encSvc.Encrypt(`{"access_token":"fresh-token","refresh_token":"fresh-refresh"}`)
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Auth type concurrent rotation",
		BaseURL:   "https://example.com",
		AuthType:  AuthTypeAccessToken,
		AuthValue: oldAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	rotated := false
	callbackName := "test:rotate-auth-before-auth-type-update"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if rotated || tx.Statement.Table != "managed_sites" || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "ManagedSite" {
			return
		}
		rotated = true
		if err := tx.Exec("UPDATE managed_sites SET auth_value = ? WHERE id = ?", freshAuth, site.ID).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	newAuthType := AuthTypeCookie
	_, err = service.UpdateSite(context.Background(), site.ID, UpdateSiteParams{AuthType: &newAuthType})

	require.Error(t, err)
	assert.True(t, rotated)
	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, AuthTypeAccessToken, stored.AuthType)
	assert.Equal(t, freshAuth, stored.AuthValue)
}

func TestSiteServiceUpdatePreservesOnlySharedAuthTypesWhenAuthTypeChanges(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewSiteService(db, store.NewMemoryStore(), encSvc)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &models.Group{}))

	encryptedCookie, err := encSvc.Encrypt("session=old-cookie")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Auth type expansion",
		BaseURL:   "https://example.com",
		AuthType:  AuthTypeCookie,
		AuthValue: encryptedCookie,
	}
	require.NoError(t, db.Create(&site).Error)

	newAuthType := AuthTypeAccessToken + "," + AuthTypeCookie
	_, err = service.UpdateSite(context.Background(), site.ID, UpdateSiteParams{AuthType: &newAuthType})
	require.NoError(t, err)

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	decrypted, err := encSvc.Decrypt(stored.AuthValue)
	require.NoError(t, err)
	config := parseAuthConfig(stored.AuthType, decrypted)
	assert.Empty(t, config.GetAuthValue(AuthTypeAccessToken))
	assert.Equal(t, "session=old-cookie", config.GetAuthValue(AuthTypeCookie))
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

func TestSiteService_ReorderSitesUpdatesSortAndInvalidatesCache(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	a, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Reorder A",
		BaseURL:  "https://a.example.com",
		Sort:     1,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)
	b, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Reorder B",
		BaseURL:  "https://b.example.com",
		Sort:     2,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)
	c, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Reorder C",
		BaseURL:  "https://c.example.com",
		Sort:     3,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	_, err = service.ListSites(context.Background())
	require.NoError(t, err)

	err = service.ReorderSites(context.Background(), []SiteReorderItem{
		{ID: a.ID, Sort: 10},
		{ID: b.ID, Sort: 15},
		{ID: c.ID, Sort: 20},
	})
	require.NoError(t, err)

	sites, err := service.ListSites(context.Background())
	require.NoError(t, err)
	require.Len(t, sites, 3)
	assert.Equal(t, []int{10, 15, 20}, []int{sites[0].Sort, sites[1].Sort, sites[2].Sort})
}

func TestSiteService_ReorderSitesSyncsBoundGroupSorts(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	a, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Bound Sort A",
		BaseURL:  "https://bound-a.example.com",
		Sort:     1,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)
	b, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Bound Sort B",
		BaseURL:  "https://bound-b.example.com",
		Sort:     2,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	boundA1 := models.Group{Name: "bound-a-1", Upstreams: []byte("[]"), BoundSiteID: &a.ID, Sort: 100}
	boundA2 := models.Group{Name: "bound-a-2", Upstreams: []byte("[]"), BoundSiteID: &a.ID, Sort: 110}
	boundB := models.Group{Name: "bound-b", Upstreams: []byte("[]"), BoundSiteID: &b.ID, Sort: 120}
	unbound := models.Group{Name: "unbound", Upstreams: []byte("[]"), Sort: 130}
	require.NoError(t, db.Create(&[]models.Group{boundA1, boundA2, boundB, unbound}).Error)

	err = service.ReorderSites(context.Background(), []SiteReorderItem{
		{ID: a.ID, Sort: 30},
		{ID: b.ID, Sort: 40},
	})
	require.NoError(t, err)

	var groups []models.Group
	require.NoError(t, db.Order("name ASC").Find(&groups).Error)
	require.Len(t, groups, 4)
	assert.Equal(t, []int{30, 30, 40, 130}, []int{groups[0].Sort, groups[1].Sort, groups[2].Sort, groups[3].Sort})
}

func TestSiteService_ReorderSitesInvalidatesGroupListCache(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)
	invalidated := false
	service.InvalidateGroupListCacheCallback = func() {
		invalidated = true
	}

	site, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Cache Sync Site",
		BaseURL:  "https://cache-sync.example.com",
		Sort:     1,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	err = service.ReorderSites(context.Background(), []SiteReorderItem{{ID: site.ID, Sort: 20}})
	require.NoError(t, err)
	assert.True(t, invalidated)
}

func TestSiteService_ReorderSitesValidationErrors(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	site, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Validation Site",
		BaseURL:  "https://validation.example.com",
		Sort:     1,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	tests := []struct {
		name  string
		items []SiteReorderItem
	}{
		{name: "empty items", items: nil},
		{name: "zero id", items: []SiteReorderItem{{ID: 0, Sort: 1}}},
		{name: "negative sort", items: []SiteReorderItem{{ID: site.ID, Sort: -1}}},
		{name: "duplicate id", items: []SiteReorderItem{{ID: site.ID, Sort: 1}, {ID: site.ID, Sort: 2}}},
		{name: "site not found", items: []SiteReorderItem{{ID: site.ID + 999, Sort: 1}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.ReorderSites(context.Background(), tt.items)
			require.Error(t, err)
		})
	}
}

func TestSiteService_RenumberSitesUpdatesAllSitesInCurrentOrder(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	for i := 25; i >= 1; i-- {
		_, err = service.CreateSite(context.Background(), CreateSiteParams{
			Name:     fmt.Sprintf("Renumber %02d", i),
			BaseURL:  fmt.Sprintf("https://%02d.example.com", i),
			Sort:     i * 10,
			AuthType: AuthTypeNone,
		})
		require.NoError(t, err)
	}

	_, err = service.ListSites(context.Background())
	require.NoError(t, err)

	err = service.RenumberSites(context.Background(), 100, 10)
	require.NoError(t, err)

	sites, err := service.ListSites(context.Background())
	require.NoError(t, err)
	require.Len(t, sites, 25)
	for i, site := range sites {
		assert.Equal(t, fmt.Sprintf("Renumber %02d", i+1), site.Name)
		assert.Equal(t, 100+i*10, site.Sort)
	}
}

func TestSiteService_RenumberSitesSyncsBoundGroupSorts(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	first, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Renumber Bound First",
		BaseURL:  "https://first.example.com",
		Sort:     10,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)
	second, err := service.CreateSite(context.Background(), CreateSiteParams{
		Name:     "Renumber Bound Second",
		BaseURL:  "https://second.example.com",
		Sort:     20,
		AuthType: AuthTypeNone,
	})
	require.NoError(t, err)

	firstGroup := models.Group{Name: "renumber-bound-first", Upstreams: []byte("[]"), BoundSiteID: &first.ID, Sort: 1}
	secondGroup := models.Group{Name: "renumber-bound-second", Upstreams: []byte("[]"), BoundSiteID: &second.ID, Sort: 2}
	require.NoError(t, db.Create(&[]models.Group{firstGroup, secondGroup}).Error)

	err = service.RenumberSites(context.Background(), 100, 10)
	require.NoError(t, err)

	var groups []models.Group
	require.NoError(t, db.Order("name ASC").Find(&groups).Error)
	require.Len(t, groups, 2)
	assert.Equal(t, []int{100, 110}, []int{groups[0].Sort, groups[1].Sort})
}

func TestSiteService_RenumberSitesValidationErrors(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	err := db.AutoMigrate(&ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	service := NewSiteService(db, memStore, encSvc)

	tests := []struct {
		name  string
		start int
		step  int
	}{
		{name: "negative start", start: -1, step: 10},
		{name: "zero step", start: 1, step: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.RenumberSites(context.Background(), tt.start, tt.step)
			require.Error(t, err)
		})
	}
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
	expectedDayBefore := time.Now().In(checkinLocation()).Format("2006-01-02")
	err = service.RecordSiteOpened(context.Background(), dto.ID)
	require.NoError(t, err)
	expectedDayAfter := time.Now().In(checkinLocation()).Format("2006-01-02")

	// Verify record
	var site ManagedSite
	err = db.First(&site, dto.ID).Error
	require.NoError(t, err)
	assert.Contains(t, []string{expectedDayBefore, expectedDayAfter}, site.LastSiteOpenedDate)
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

func TestGetAutoCheckinConfigCanonicalizesLegacyTimesWithoutWriting(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	require.NoError(t, db.Create(&ManagedSiteSetting{
		ID:                1,
		ScheduleTimes:     "9:0,12:30",
		WindowStart:       "8:5",
		WindowEnd:         "18:0",
		ScheduleMode:      AutoCheckinScheduleModeRandom,
		DeterministicTime: "7:1",
	}).Error)

	service := NewSiteService(db, nil, encSvc)
	cfg, err := service.GetAutoCheckinConfig(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"09:00", "12:30"}, cfg.ScheduleTimes)
	assert.Equal(t, "08:05", cfg.WindowStart)
	assert.Equal(t, "18:00", cfg.WindowEnd)
	assert.Equal(t, "07:01", cfg.DeterministicTime)

	var stored ManagedSiteSetting
	require.NoError(t, db.First(&stored, 1).Error)
	assert.Equal(t, "9:0,12:30", stored.ScheduleTimes)
	assert.Equal(t, "8:5", stored.WindowStart)
	assert.Equal(t, "18:0", stored.WindowEnd)
	assert.Equal(t, "7:1", stored.DeterministicTime)
}

func TestSiteService_AutoBalanceConfig(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	service := NewSiteService(db, memStore, encSvc)

	cfg, err := service.GetAutoBalanceConfig(context.Background())
	require.NoError(t, err)
	assert.True(t, cfg.GlobalEnabled)
	assert.Equal(t, 24, cfg.IntervalHours)

	updated, err := service.UpdateAutoBalanceConfig(context.Background(), AutoBalanceConfig{
		GlobalEnabled: false,
		IntervalHours: 6,
	})
	require.NoError(t, err)
	assert.False(t, updated.GlobalEnabled)
	assert.Equal(t, 6, updated.IntervalHours)

	_, err = service.UpdateAutoBalanceConfig(context.Background(), AutoBalanceConfig{IntervalHours: 0})
	assert.Error(t, err)
	_, err = service.UpdateAutoBalanceConfig(context.Background(), AutoBalanceConfig{IntervalHours: 25})
	assert.Error(t, err)
}

func TestScheduleConfigUpdatesReturnCommittedValuesWithoutReadBack(t *testing.T) {
	setup := func(t *testing.T) (*SiteService, *gorm.DB, *int, func()) {
		t.Helper()
		db := setupTestDB(t)
		encSvc := setupTestEncryption(t)
		require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
		require.NoError(t, db.Create(&ManagedSiteSetting{ID: 1}).Error)

		queryCount := 0
		rejectPostCommitRead := true
		forcedErr := errors.New("forced post-commit config read failure")
		const hookName = "test:reject_post_commit_config_read"
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register(hookName, func(tx *gorm.DB) {
			if tx.Statement.Table != "managed_site_settings" {
				return
			}
			queryCount++
			if rejectPostCommitRead && queryCount == 2 {
				tx.AddError(forcedErr)
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Query().Remove(hookName) })

		// Keep the store nil to verify committed config updates do not depend on pub/sub availability.
		return NewSiteService(db, nil, encSvc), db, &queryCount, func() {
			rejectPostCommitRead = false
		}
	}

	t.Run("auto check-in", func(t *testing.T) {
		service, db, queryCount, allowReads := setup(t)

		updated, err := service.UpdateAutoCheckinConfig(context.Background(), AutoCheckinConfig{
			GlobalEnabled:     true,
			ScheduleTimes:     []string{" 08:00 ", "12:30 "},
			WindowStart:       " 09:00 ",
			WindowEnd:         "18:00 ",
			ScheduleMode:      " random ",
			DeterministicTime: " 07:15 ",
			RetryStrategy: AutoCheckinRetryStrategy{
				Enabled:           true,
				IntervalMinutes:   0,
				MaxAttemptsPerDay: 100,
			},
		})

		require.NoError(t, err)
		assert.Equal(t, 1, *queryCount)
		assert.Equal(t, []string{"08:00", "12:30"}, updated.ScheduleTimes)
		assert.Equal(t, "09:00", updated.WindowStart)
		assert.Equal(t, "18:00", updated.WindowEnd)
		assert.Equal(t, AutoCheckinScheduleModeRandom, updated.ScheduleMode)
		assert.Equal(t, "07:15", updated.DeterministicTime)
		assert.Equal(t, 1, updated.RetryStrategy.IntervalMinutes)
		assert.Equal(t, 10, updated.RetryStrategy.MaxAttemptsPerDay)

		allowReads()
		var stored ManagedSiteSetting
		require.NoError(t, db.First(&stored, 1).Error)
		assert.Equal(t, updated.GlobalEnabled, stored.AutoCheckinEnabled)
		assert.Equal(t, strings.Join(updated.ScheduleTimes, ","), stored.ScheduleTimes)
		assert.Equal(t, updated.WindowStart, stored.WindowStart)
		assert.Equal(t, updated.WindowEnd, stored.WindowEnd)
		assert.Equal(t, updated.ScheduleMode, stored.ScheduleMode)
		assert.Equal(t, updated.DeterministicTime, stored.DeterministicTime)
		assert.Equal(t, updated.RetryStrategy.Enabled, stored.RetryEnabled)
		assert.Equal(t, updated.RetryStrategy.IntervalMinutes, stored.RetryIntervalMinutes)
		assert.Equal(t, updated.RetryStrategy.MaxAttemptsPerDay, stored.RetryMaxAttemptsPerDay)
	})

	t.Run("auto balance", func(t *testing.T) {
		service, db, queryCount, allowReads := setup(t)

		updated, err := service.UpdateAutoBalanceConfig(context.Background(), AutoBalanceConfig{
			GlobalEnabled: false,
			IntervalHours: 6,
		})

		require.NoError(t, err)
		assert.Equal(t, 1, *queryCount)
		assert.Equal(t, &AutoBalanceConfig{GlobalEnabled: false, IntervalHours: 6}, updated)

		allowReads()
		var stored ManagedSiteSetting
		require.NoError(t, db.First(&stored, 1).Error)
		assert.Equal(t, updated.GlobalEnabled, stored.AutoBalanceEnabled)
		assert.Equal(t, updated.IntervalHours, stored.BalanceRefreshIntervalHours)
	})
}

func TestValidateAutoCheckinConfigRejectsOversizedScheduleTimes(t *testing.T) {
	scheduleTimes := make([]string, 44)
	for i := range scheduleTimes {
		scheduleTimes[i] = fmt.Sprintf("%02d:%02d", i/60, i%60)
	}

	_, err := validateAutoCheckinConfig(AutoCheckinConfig{
		ScheduleMode:  AutoCheckinScheduleModeMultiple,
		ScheduleTimes: scheduleTimes,
	})

	require.Error(t, err)
}

func TestValidateAutoCheckinConfigRejectsNonCanonicalTimes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  AutoCheckinConfig
	}{
		{
			name: "multiple schedule time",
			cfg: AutoCheckinConfig{
				ScheduleMode:  AutoCheckinScheduleModeMultiple,
				ScheduleTimes: []string{"9:00"},
			},
		},
		{
			name: "random window start",
			cfg: AutoCheckinConfig{
				ScheduleMode: AutoCheckinScheduleModeRandom,
				WindowStart:  "9:00",
				WindowEnd:    "18:00",
			},
		},
		{
			name: "random window end",
			cfg: AutoCheckinConfig{
				ScheduleMode: AutoCheckinScheduleModeRandom,
				WindowStart:  "09:00",
				WindowEnd:    "18:0",
			},
		},
		{
			name: "deterministic time",
			cfg: AutoCheckinConfig{
				ScheduleMode:      AutoCheckinScheduleModeDeterministic,
				DeterministicTime: "9:00",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateAutoCheckinConfig(tt.cfg)
			require.Error(t, err)
		})
	}
}

func TestEnsureSettingsRowHandlesConcurrentCreate(t *testing.T) {
	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	service := NewSiteService(db, nil, encSvc)

	injected := false
	const hookName = "test:concurrent_settings_create"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hookName, func(tx *gorm.DB) {
		if injected || tx.Statement.Table != "managed_site_settings" {
			return
		}
		injected = true
		// Simulate a competing writer inserting the singleton after our initial read.
		if err := tx.Exec("INSERT INTO managed_site_settings (id) VALUES (?)", 1).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(hookName) })

	cfg, err := service.GetAutoBalanceConfig(context.Background())

	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.GlobalEnabled)
	assert.Equal(t, defaultAutoBalanceIntervalHours, cfg.IntervalHours)
}

func TestExportSitesReturnsRequestedScheduleConfigErrors(t *testing.T) {
	t.Run("auto check-in config", func(t *testing.T) {
		db := setupTestDB(t)
		encSvc := setupTestEncryption(t)
		require.NoError(t, db.AutoMigrate(&ManagedSite{}))
		service := NewSiteService(db, nil, encSvc)

		exported, err := service.ExportSites(context.Background(), true, true)

		require.Error(t, err)
		assert.Nil(t, exported)
	})

	t.Run("auto balance config", func(t *testing.T) {
		db := setupTestDB(t)
		encSvc := setupTestEncryption(t)
		require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteSetting{}))
		require.NoError(t, db.Create(&ManagedSiteSetting{ID: 1}).Error)
		service := NewSiteService(db, nil, encSvc)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		settingsQueries := 0
		// Cancel after auto-check-in loads so the next read tests the auto-balance error path portably.
		const hookName = "test:cancel_before_auto_balance_export"
		require.NoError(t, db.Callback().Query().After("gorm:query").Register(hookName, func(tx *gorm.DB) {
			if tx.Statement.Table != "managed_site_settings" {
				return
			}
			settingsQueries++
			if settingsQueries == 1 {
				cancel()
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Query().Remove(hookName) })

		exported, err := service.ExportSites(ctx, true, true)

		require.Error(t, err)
		assert.Nil(t, exported)
	})
}

func TestManagedSiteBalanceMultiplierDatabaseDefault(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	site := ManagedSite{Name: "Database default", BaseURL: "https://example.com"}
	require.NoError(t, db.Create(&site).Error)
	assert.Equal(t, int64(1), site.BalanceMultiplier)
}

func TestManagedSiteBalanceMultiplierAutoMigrationDefaultsExistingRows(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE managed_sites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		base_url TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO managed_sites (name, base_url) VALUES (?, ?)",
		"Existing site",
		"https://example.com",
	).Error)

	// AutoMigrate must apply the constant default to rows created before the column existed.
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	var site ManagedSite
	require.NoError(t, db.Where("name = ?", "Existing site").First(&site).Error)
	assert.Equal(t, int64(1), site.BalanceMultiplier)
}

func TestExportImportSitesPreservesBalanceMultiplier(t *testing.T) {
	t.Parallel()

	sourceDB := setupTestDB(t)
	require.NoError(t, sourceDB.AutoMigrate(&ManagedSite{}))
	require.NoError(t, sourceDB.Create(&ManagedSite{
		Name:              "Exported multiplier",
		BaseURL:           "https://example.com",
		SiteType:          SiteTypeNewAPI,
		AuthType:          AuthTypeNone,
		BalanceMultiplier: 7,
	}).Error)
	sourceService := NewSiteService(sourceDB, nil, setupTestEncryption(t))

	exported, err := sourceService.ExportSites(context.Background(), false, false)
	require.NoError(t, err)
	require.Len(t, exported.Sites, 1)
	assert.Equal(t, int64(7), exported.Sites[0].BalanceMultiplier)

	targetDB := setupTestDB(t)
	require.NoError(t, targetDB.AutoMigrate(&ManagedSite{}))
	targetService := NewSiteService(targetDB, nil, setupTestEncryption(t))
	imported, skipped, err := targetService.ImportSites(context.Background(), exported, false)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Zero(t, skipped)

	legacy := &SiteExportData{Sites: []SiteExportInfo{{
		Name:     "Legacy multiplier",
		BaseURL:  "https://legacy.example.com",
		SiteType: SiteTypeNewAPI,
		AuthType: AuthTypeNone,
	}}}
	imported, skipped, err = targetService.ImportSites(context.Background(), legacy, false)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Zero(t, skipped)

	invalid := &SiteExportData{Sites: []SiteExportInfo{{
		Name:              "Invalid multiplier",
		BaseURL:           "https://invalid.example.com",
		SiteType:          SiteTypeNewAPI,
		AuthType:          AuthTypeNone,
		BalanceMultiplier: -1,
	}}}
	imported, skipped, err = targetService.ImportSites(context.Background(), invalid, false)
	require.NoError(t, err)
	assert.Zero(t, imported)
	assert.Equal(t, 1, skipped)

	var sites []ManagedSite
	require.NoError(t, targetDB.Order("id ASC").Find(&sites).Error)
	require.Len(t, sites, 2)
	assert.Equal(t, int64(7), sites[0].BalanceMultiplier)
	assert.Equal(t, int64(1), sites[1].BalanceMultiplier)
}

func TestExportSitesOnlyIncludesProxyURLInPlainMode(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	const proxyURL = "http://user:pass@proxy.example.test:8080"
	require.NoError(t, db.Create(&ManagedSite{
		Name:     "site with authenticated proxy",
		BaseURL:  "https://example.com",
		SiteType: SiteTypeNewAPI,
		Enabled:  true,
		UseProxy: true,
		ProxyURL: proxyURL,
		AuthType: AuthTypeNone,
	}).Error)
	service := NewSiteService(db, nil, encSvc)

	encrypted, err := service.ExportSites(context.Background(), false, false)
	require.NoError(t, err)
	require.Len(t, encrypted.Sites, 1)
	assert.Empty(t, encrypted.Sites[0].ProxyURL)
	encryptedJSON, err := json.Marshal(encrypted)
	require.NoError(t, err)
	assert.NotContains(t, string(encryptedJSON), proxyURL)
	targetDB := setupTestDB(t)
	require.NoError(t, targetDB.AutoMigrate(&ManagedSite{}))
	targetService := NewSiteService(targetDB, nil, setupTestEncryption(t))
	imported, skipped, err := targetService.ImportSites(context.Background(), encrypted, false)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Zero(t, skipped)
	var importedSite ManagedSite
	require.NoError(t, targetDB.Where("name = ?", "site with authenticated proxy").First(&importedSite).Error)
	assert.True(t, importedSite.UseProxy)
	assert.Empty(t, importedSite.ProxyURL)

	plain, err := service.ExportSites(context.Background(), false, true)
	require.NoError(t, err)
	require.Len(t, plain.Sites, 1)
	assert.Equal(t, proxyURL, plain.Sites[0].ProxyURL)
}

func TestExportSitesReturnsPlainCredentialDecryptErrors(t *testing.T) {
	tests := []struct {
		name      string
		userID    string
		authType  string
		authValue string
	}{
		{
			name:     "user ID",
			userID:   "sensitive-invalid-ciphertext",
			authType: AuthTypeNone,
		},
		{
			name:      "auth value",
			authType:  AuthTypeAccessToken,
			authValue: "sensitive-invalid-ciphertext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			encSvc := setupTestEncryption(t)
			require.NoError(t, db.AutoMigrate(&ManagedSite{}))
			require.NoError(t, db.Create(&ManagedSite{
				Name:      "site with invalid encrypted data",
				BaseURL:   "https://example.com",
				SiteType:  SiteTypeNewAPI,
				Enabled:   true,
				UserID:    tt.userID,
				AuthType:  tt.authType,
				AuthValue: tt.authValue,
			}).Error)
			service := NewSiteService(db, nil, encSvc)

			exported, err := service.ExportSites(context.Background(), false, true)

			require.Error(t, err)
			assert.Nil(t, exported)
			assert.NotContains(t, err.Error(), "sensitive-invalid-ciphertext")
			assert.NotContains(t, err.Error(), "encoding/hex")
		})
	}
}

func TestExportSitesNormalizesNoAuthAndOmitsCredential(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	require.NoError(t, db.Create(&ManagedSite{
		Name:      "site without authentication",
		BaseURL:   "https://example.com",
		SiteType:  SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  " \t ",
		AuthValue: "sensitive-stale-ciphertext",
	}).Error)
	service := NewSiteService(db, nil, encSvc)

	exported, err := service.ExportSites(context.Background(), false, true)

	require.NoError(t, err)
	require.Len(t, exported.Sites, 1)
	assert.Equal(t, AuthTypeNone, exported.Sites[0].AuthType)
	assert.Empty(t, exported.Sites[0].AuthValue)
}

func TestExportSitesPreservesUnknownAuthTypeForForwardCompatibleBackup(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	encryptedAuth, err := encSvc.Encrypt("future-auth-value")
	require.NoError(t, err)
	require.NoError(t, db.Create(&ManagedSite{
		Name:      "site with future authentication",
		BaseURL:   "https://example.com",
		SiteType:  SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  "future_auth",
		AuthValue: encryptedAuth,
	}).Error)
	service := NewSiteService(db, nil, encSvc)

	exported, err := service.ExportSites(context.Background(), false, false)

	require.NoError(t, err)
	require.Len(t, exported.Sites, 1)
	assert.Equal(t, "future_auth", exported.Sites[0].AuthType)
	assert.Equal(t, encryptedAuth, exported.Sites[0].AuthValue)
}

func TestScheduleConfigUpdatesDoNotOverwriteOtherScheduleFields(t *testing.T) {
	t.Run("auto check-in update preserves a concurrent balance update", func(t *testing.T) {
		db := setupTestDB(t)
		encSvc := setupTestEncryption(t)
		require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
		memStore := store.NewMemoryStore()
		t.Cleanup(func() { memStore.Close() })
		service := NewSiteService(db, memStore, encSvc)
		_, err := service.GetAutoBalanceConfig(context.Background())
		require.NoError(t, err)

		const hookName = "test:concurrent_balance_update"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hookName, func(tx *gorm.DB) {
			if tx.Statement.Table == "managed_site_settings" {
				tx.Exec("UPDATE managed_site_settings SET auto_balance_enabled = ?, balance_refresh_interval_hours = ? WHERE id = ?", false, 6, 1)
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

		_, err = service.UpdateAutoCheckinConfig(context.Background(), AutoCheckinConfig{
			GlobalEnabled: true,
			ScheduleTimes: []string{"10:00"},
			ScheduleMode:  AutoCheckinScheduleModeMultiple,
		})
		require.NoError(t, err)

		var setting ManagedSiteSetting
		require.NoError(t, db.First(&setting, 1).Error)
		assert.False(t, setting.AutoBalanceEnabled)
		assert.Equal(t, 6, setting.BalanceRefreshIntervalHours)
	})

	t.Run("auto balance update preserves a concurrent check-in update", func(t *testing.T) {
		db := setupTestDB(t)
		encSvc := setupTestEncryption(t)
		require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
		memStore := store.NewMemoryStore()
		t.Cleanup(func() { memStore.Close() })
		service := NewSiteService(db, memStore, encSvc)
		_, err := service.GetAutoCheckinConfig(context.Background())
		require.NoError(t, err)

		const hookName = "test:concurrent_checkin_update"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hookName, func(tx *gorm.DB) {
			if tx.Statement.Table == "managed_site_settings" {
				tx.Exec("UPDATE managed_site_settings SET auto_checkin_enabled = ?, schedule_times = ? WHERE id = ?", true, "11:00", 1)
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

		_, err = service.UpdateAutoBalanceConfig(context.Background(), AutoBalanceConfig{
			GlobalEnabled: false,
			IntervalHours: 6,
		})
		require.NoError(t, err)

		var setting ManagedSiteSetting
		require.NoError(t, db.First(&setting, 1).Error)
		assert.True(t, setting.AutoCheckinEnabled)
		assert.Equal(t, "11:00", setting.ScheduleTimes)
	})
}

func TestImportSitesRejectsInvalidScheduleConfigBeforeWritingSites(t *testing.T) {
	tests := []struct {
		name string
		data *SiteExportData
	}{
		{
			name: "auto check-in config",
			data: &SiteExportData{
				AutoCheckin: &AutoCheckinConfig{ScheduleMode: AutoCheckinScheduleModeMultiple},
			},
		},
		{
			name: "auto balance config",
			data: &SiteExportData{
				AutoBalance: &AutoBalanceConfig{GlobalEnabled: true, IntervalHours: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			encSvc := setupTestEncryption(t)
			require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteSetting{}))
			service := NewSiteService(db, nil, encSvc)
			tt.data.Sites = []SiteExportInfo{
				{
					Name:     "must not be imported",
					BaseURL:  "https://example.com",
					SiteType: SiteTypeNewAPI,
					Enabled:  true,
					AuthType: AuthTypeNone,
				},
			}

			imported, skipped, err := service.ImportSites(context.Background(), tt.data, true)

			require.Error(t, err)
			assert.Zero(t, imported)
			assert.Zero(t, skipped)
			var count int64
			require.NoError(t, db.Model(&ManagedSite{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestImportSitesRollsBackScheduleConfigBeforeWritingSites(t *testing.T) {
	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteSetting{}))
	service := NewSiteService(db, nil, encSvc)
	_, err := service.GetAutoCheckinConfig(context.Background())
	require.NoError(t, err)

	var before ManagedSiteSetting
	require.NoError(t, db.First(&before, 1).Error)
	forcedErr := errors.New("forced schedule update failure")
	const hookName = "test:import_schedule_update_error"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table == "managed_site_settings" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

	imported, skipped, err := service.ImportSites(context.Background(), &SiteExportData{
		AutoCheckin: &AutoCheckinConfig{
			GlobalEnabled: true,
			ScheduleTimes: []string{"08:00", "12:30"},
			ScheduleMode:  AutoCheckinScheduleModeMultiple,
		},
		AutoBalance: &AutoBalanceConfig{GlobalEnabled: false, IntervalHours: 6},
		Sites: []SiteExportInfo{
			{
				Name:     "must not be imported",
				BaseURL:  "https://example.com",
				SiteType: SiteTypeNewAPI,
				Enabled:  true,
				AuthType: AuthTypeNone,
			},
		},
	}, true)

	require.Error(t, err)
	assert.Zero(t, imported)
	assert.Zero(t, skipped)
	var siteCount int64
	require.NoError(t, db.Model(&ManagedSite{}).Count(&siteCount).Error)
	assert.Zero(t, siteCount)

	var after ManagedSiteSetting
	require.NoError(t, db.First(&after, 1).Error)
	assert.Equal(t, before.AutoCheckinEnabled, after.AutoCheckinEnabled)
	assert.Equal(t, before.ScheduleTimes, after.ScheduleTimes)
	assert.Equal(t, before.AutoBalanceEnabled, after.AutoBalanceEnabled)
	assert.Equal(t, before.BalanceRefreshIntervalHours, after.BalanceRefreshIntervalHours)
}

func TestImportSitesAppliesAutoBalanceWithoutSites(t *testing.T) {
	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()
	t.Cleanup(func() { memStore.Close() })
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteSetting{}))
	service := NewSiteService(db, memStore, encSvc)

	imported, skipped, err := service.ImportSites(context.Background(), &SiteExportData{
		AutoBalance: &AutoBalanceConfig{GlobalEnabled: false, IntervalHours: 6},
		Sites:       []SiteExportInfo{},
	}, true)

	require.NoError(t, err)
	assert.Zero(t, imported)
	assert.Zero(t, skipped)
	config, err := service.GetAutoBalanceConfig(context.Background())
	require.NoError(t, err)
	assert.False(t, config.GlobalEnabled)
	assert.Equal(t, 6, config.IntervalHours)
}

func TestManagedSiteSettingAutoMigrateBackfillsAutoBalanceDefaults(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE managed_site_settings (
		id integer primary key autoincrement,
		auto_checkin_enabled numeric not null default false,
		schedule_times text not null default '09:00',
		window_start text not null default '09:00',
		window_end text not null default '18:00',
		schedule_mode text not null default 'multiple',
		deterministic_time text not null default '',
		retry_enabled numeric not null default false,
		retry_interval_minutes integer not null default 60,
		retry_max_attempts_per_day integer not null default 2,
		created_at datetime,
		updated_at datetime
	)`).Error)
	require.NoError(t, db.Exec("INSERT INTO managed_site_settings (id) VALUES (1)").Error)

	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	var setting ManagedSiteSetting
	require.NoError(t, db.First(&setting, 1).Error)
	assert.True(t, setting.AutoBalanceEnabled)
	assert.Equal(t, defaultAutoBalanceIntervalHours, setting.BalanceRefreshIntervalHours)
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
	_, err = service.UpdateAutoBalanceConfig(context.Background(), AutoBalanceConfig{
		GlobalEnabled: false,
		IntervalHours: 6,
	})
	require.NoError(t, err)

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
	exportData, err := service.ExportSites(context.Background(), true, true)
	require.NoError(t, err)
	assert.Len(t, exportData.Sites, 3)
	require.NotNil(t, exportData.AutoBalance)
	assert.False(t, exportData.AutoBalance.GlobalEnabled)
	assert.Equal(t, 6, exportData.AutoBalance.IntervalHours)

	// Clear database
	err = db.Exec("DELETE FROM managed_sites").Error
	require.NoError(t, err)
	_, err = service.UpdateAutoBalanceConfig(context.Background(), AutoBalanceConfig{
		GlobalEnabled: true,
		IntervalHours: 24,
	})
	require.NoError(t, err)

	// Import sites
	imported, skipped, err := service.ImportSites(context.Background(), exportData, true)
	require.NoError(t, err)
	assert.Equal(t, 3, imported)
	assert.Equal(t, 0, skipped)
	restored, err := service.GetAutoBalanceConfig(context.Background())
	require.NoError(t, err)
	assert.False(t, restored.GlobalEnabled)
	assert.Equal(t, 6, restored.IntervalHours)
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

	var target ManagedSite
	err = db.Where("name = ?", "Site M").First(&target).Error
	require.NoError(t, err)

	result, err = service.ListSitesPaginated(context.Background(), SiteListParams{
		Page:        1,
		PageSize:    5,
		FocusSiteID: target.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, result.Page)
	require.NotEmpty(t, result.Sites)

	foundTarget := false
	for _, site := range result.Sites {
		if site.ID == target.ID {
			foundTarget = true
			break
		}
	}
	assert.True(t, foundTarget, "focused site should be included in the returned page")
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

// TestNormalizeAuthType tests auth type normalization including multi-auth support
func TestNormalizeAuthType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Single auth types
		{
			name:     "access_token lowercase",
			input:    "access_token",
			expected: AuthTypeAccessToken,
		},
		{
			name:     "access_token uppercase",
			input:    "ACCESS_TOKEN",
			expected: AuthTypeAccessToken,
		},
		{
			name:     "access_token mixed case",
			input:    "Access_Token",
			expected: AuthTypeAccessToken,
		},
		{
			name:     "cookie lowercase",
			input:    "cookie",
			expected: AuthTypeCookie,
		},
		{
			name:     "cookie uppercase",
			input:    "COOKIE",
			expected: AuthTypeCookie,
		},
		{
			name:     "none lowercase",
			input:    "none",
			expected: AuthTypeNone,
		},
		{
			name:     "none uppercase",
			input:    "NONE",
			expected: AuthTypeNone,
		},
		{
			name:     "empty string",
			input:    "",
			expected: AuthTypeNone,
		},
		{
			name:     "whitespace only",
			input:    "   ",
			expected: AuthTypeNone,
		},
		{
			name:     "invalid type",
			input:    "invalid",
			expected: "",
		},
		// Multi-auth types (comma-separated)
		{
			name:     "access_token,cookie",
			input:    "access_token,cookie",
			expected: "access_token,cookie",
		},
		{
			name:     "cookie,access_token (reversed order)",
			input:    "cookie,access_token",
			expected: "cookie,access_token",
		},
		{
			name:     "access_token,cookie with spaces",
			input:    "access_token , cookie",
			expected: "access_token,cookie",
		},
		{
			name:     "ACCESS_TOKEN,COOKIE uppercase",
			input:    "ACCESS_TOKEN,COOKIE",
			expected: "access_token,cookie",
		},
		{
			name:     "access_token,cookie with extra spaces",
			input:    "  access_token  ,  cookie  ",
			expected: "access_token,cookie",
		},
		{
			name:     "access_token,none,cookie (none filtered out)",
			input:    "access_token,none,cookie",
			expected: "access_token,cookie",
		},
		{
			name:     "none,access_token (none filtered out)",
			input:    "none,access_token",
			expected: "access_token",
		},
		{
			name:     "access_token,invalid (invalid component)",
			input:    "access_token,invalid",
			expected: "",
		},
		{
			name:     "invalid,cookie (invalid component)",
			input:    "invalid,cookie",
			expected: "",
		},
		{
			name:     "none,none (all filtered out)",
			input:    "none,none",
			expected: AuthTypeNone,
		},
		{
			name:     "empty components",
			input:    "access_token,,cookie",
			expected: "access_token,cookie",
		},
		{
			name:     "trailing comma",
			input:    "access_token,",
			expected: "access_token",
		},
		{
			name:     "leading comma",
			input:    ",cookie",
			expected: "cookie",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeAuthType(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestNormalizeSingleAuthType tests single auth type normalization
func TestNormalizeSingleAuthType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "access_token",
			input:    "access_token",
			expected: AuthTypeAccessToken,
		},
		{
			name:     "cookie",
			input:    "cookie",
			expected: AuthTypeCookie,
		},
		{
			name:     "none",
			input:    "none",
			expected: AuthTypeNone,
		},
		{
			name:     "empty",
			input:    "",
			expected: AuthTypeNone,
		},
		{
			name:     "invalid",
			input:    "invalid",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeSingleAuthType(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
