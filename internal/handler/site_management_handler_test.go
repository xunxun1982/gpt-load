package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/i18n"
	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/sitemanagement"
	"gpt-load/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type handlerHubAccessKeyTestModel struct {
	ID            uint   `gorm:"primaryKey"`
	Name          string `gorm:"column:name"`
	KeyHash       string `gorm:"column:key_hash"`
	KeyValue      string `gorm:"column:key_value"`
	AllowedModels []byte `gorm:"column:allowed_models"`
	Enabled       bool   `gorm:"column:enabled"`
}

func (handlerHubAccessKeyTestModel) TableName() string {
	return "hub_access_keys"
}

func init() {
	if err := i18n.Init(); err != nil {
		panic("failed to initialize i18n for tests: " + err.Error())
	}
}

func TestCreateManagedSiteRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request CreateManagedSiteRequest
		wantErr bool
	}{
		{
			name: "valid request",
			request: CreateManagedSiteRequest{
				Name:     "Test Site",
				BaseURL:  "https://example.com",
				SiteType: "api",
				Enabled:  true,
			},
			wantErr: false,
		},
		{
			name: "with check-in enabled",
			request: CreateManagedSiteRequest{
				Name:             "Test Site",
				BaseURL:          "https://example.com",
				SiteType:         "api",
				CheckInAvailable: true,
				CheckInEnabled:   true,
				CheckInPageURL:   "https://example.com/checkin",
			},
			wantErr: false,
		},
		{
			name: "with proxy",
			request: CreateManagedSiteRequest{
				Name:     "Test Site",
				BaseURL:  "https://example.com",
				SiteType: "api",
				UseProxy: true,
				ProxyURL: "http://proxy.example.com:8080",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.request)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/sites", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			var req CreateManagedSiteRequest
			err = c.ShouldBindJSON(&req)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.request.Name, req.Name)
				assert.Equal(t, tt.request.BaseURL, req.BaseURL)
			}
		})
	}
}

func TestUpdateManagedSiteRequest_Validation(t *testing.T) {
	name := "Updated Site"
	enabled := false
	sort := 10

	tests := []struct {
		name    string
		request UpdateManagedSiteRequest
		wantErr bool
	}{
		{
			name: "update name only",
			request: UpdateManagedSiteRequest{
				Name: &name,
			},
			wantErr: false,
		},
		{
			name: "update enabled status",
			request: UpdateManagedSiteRequest{
				Enabled: &enabled,
			},
			wantErr: false,
		},
		{
			name: "update sort order",
			request: UpdateManagedSiteRequest{
				Sort: &sort,
			},
			wantErr: false,
		},
		{
			name:    "empty update",
			request: UpdateManagedSiteRequest{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.request)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/sites/1", bytes.NewBuffer(body))
			c.Request.Header.Set("Content-Type", "application/json")

			var req UpdateManagedSiteRequest
			err = c.ShouldBindJSON(&req)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.request.Name != nil {
					assert.Equal(t, *tt.request.Name, *req.Name)
				}
			}
		})
	}
}

func TestListManagedSites_FocusSiteIDUsesPaginatedPath(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	err := db.AutoMigrate(&sitemanagement.ManagedSite{}, &models.Group{})
	require.NoError(t, err)

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })

	service := sitemanagement.NewSiteService(db, kvStore, encSvc)
	for i := 0; i < 6; i++ {
		site := &sitemanagement.ManagedSite{
			Name:     "Site " + string(rune('A'+i)),
			BaseURL:  "https://example.com",
			Sort:     i,
			AuthType: sitemanagement.AuthTypeNone,
		}
		require.NoError(t, db.Create(site).Error)
	}

	var target sitemanagement.ManagedSite
	require.NoError(t, db.Where("name = ?", "Site F").First(&target).Error)

	server := &Server{SiteService: service}
	router := gin.New()
	router.GET("/site-management/sites", server.ListManagedSites)

	req := httptest.NewRequest(http.MethodGet, "/site-management/sites?focus_site_id="+strconv.FormatUint(uint64(target.ID), 10)+"&page_size=2", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok, "focus_site_id should use paginated response shape")
	assert.Equal(t, float64(3), data["page"])

	sites, ok := data["sites"].([]any)
	require.True(t, ok)
	foundTarget := false
	for _, item := range sites {
		site, ok := item.(map[string]any)
		require.True(t, ok)
		if uint(site["id"].(float64)) == target.ID {
			foundTarget = true
			break
		}
	}
	assert.True(t, foundTarget, "focused site should be included in the returned page")
}

func TestImportManagedSitesSuccessMessageInterpolatesCounts(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&sitemanagement.ManagedSite{}, &sitemanagement.ManagedSiteSetting{}))

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })

	server := &Server{SiteService: sitemanagement.NewSiteService(db, kvStore, encSvc)}
	router := gin.New()
	router.Use(i18n.Middleware())
	router.POST("/site-management/import", server.ImportManagedSites)

	body := []byte(`{
		"version":"1.0",
		"auto_balance":{"global_enabled":false,"interval_hours":6},
		"sites":[
			{
				"name":"Imported Site",
				"base_url":"https://example.com",
				"site_type":"new-api",
				"auth_type":"none"
			}
		]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/site-management/import?mode=plain", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "zh-CN")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	message, ok := resp["message"].(string)
	require.True(t, ok)
	assert.Contains(t, message, "已导入 1 个站点")
	assert.NotContains(t, message, "{{")
	assert.False(t, strings.Contains(message, "<") || strings.Contains(message, ">"))

	data, ok := resp["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), data["imported"])
	assert.Equal(t, float64(0), data["skipped"])
	assert.Equal(t, float64(1), data["total"])
	autoBalance, err := server.SiteService.GetAutoBalanceConfig(req.Context())
	require.NoError(t, err)
	assert.False(t, autoBalance.GlobalEnabled)
	assert.Equal(t, 6, autoBalance.IntervalHours)
}

func TestImportManagedSitesRejectsEmptyPayloadWithSupportedConfigMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/site-management/import", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	(&Server{}).ImportManagedSites(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "No sites or supported configuration provided")
}

func TestUpdateAutoBalanceConfigRejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/site-management/auto-balance/config", strings.NewReader("{"))
	c.Request.Header.Set("Content-Type", "application/json")

	(&Server{}).UpdateAutoBalanceConfig(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_JSON")
}

func TestScheduleConfigUpdatesRequestLocalRescheduleWithoutStore(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("auto check-in", func(t *testing.T) {
		db := setupTestDB(t)
		require.NoError(t, db.AutoMigrate(&sitemanagement.ManagedSiteSetting{}))
		encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
		require.NoError(t, err)
		autoCheckinService := sitemanagement.NewAutoCheckinService(db, nil, encSvc)
		server := &Server{
			SiteService:        sitemanagement.NewSiteService(db, nil, encSvc),
			AutoCheckinService: autoCheckinService,
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/site-management/auto-checkin/config", strings.NewReader(`{
			"global_enabled":true,
			"schedule_times":["09:00"],
			"schedule_mode":"multiple"
		}`))
		c.Request.Header.Set("Content-Type", "application/json")

		server.UpdateAutoCheckinConfig(c)

		require.Equal(t, http.StatusOK, w.Code)
		rescheduleCh := reflect.ValueOf(autoCheckinService).Elem().FieldByName("rescheduleCh")
		assert.Equal(t, 1, rescheduleCh.Len())
	})

	t.Run("auto balance", func(t *testing.T) {
		db := setupTestDB(t)
		require.NoError(t, db.AutoMigrate(&sitemanagement.ManagedSiteSetting{}))
		encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
		require.NoError(t, err)
		balanceService := sitemanagement.NewBalanceService(db, encSvc)
		server := &Server{
			SiteService:    sitemanagement.NewSiteService(db, nil, encSvc),
			BalanceService: balanceService,
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/site-management/auto-balance/config", strings.NewReader(`{
			"global_enabled":false,
			"interval_hours":6
		}`))
		c.Request.Header.Set("Content-Type", "application/json")

		server.UpdateAutoBalanceConfig(c)

		require.Equal(t, http.StatusOK, w.Code)
		rescheduleCh := reflect.ValueOf(balanceService).Elem().FieldByName("rescheduleCh")
		assert.Equal(t, 1, rescheduleCh.Len())
	})
}

func TestImportAllPublishesManagedSiteScheduleUpdateAfterCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// SQLite :memory: is connection-local, while ImportAll starts a DB-reading goroutine after commit.
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	kvStore := store.NewMemoryStore()
	t.Cleanup(func() { kvStore.Close() })
	sub, err := kvStore.Subscribe("managed_site:auto_checkin_config_updated")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })

	server := &Server{
		DB:                  db,
		EncryptionSvc:       encSvc,
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		SiteService:         sitemanagement.NewSiteService(db, kvStore, encSvc),
		BalanceService:      sitemanagement.NewBalanceService(db, encSvc),
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/system/import", strings.NewReader(`{
		"version":"2.0",
		"managed_sites":{
			"auto_checkin":{
				"global_enabled":true,
				"schedule_times":["08:00","12:30"],
				"window_start":"09:00",
				"window_end":"18:00",
				"schedule_mode":"multiple",
				"retry_strategy":{"enabled":false,"interval_minutes":60,"max_attempts_per_day":2}
			},
			"auto_balance":{"global_enabled":false,"interval_hours":6},
			"sites":[]
		}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	select {
	case <-sub.Channel():
	case <-time.After(time.Second):
		t.Fatal("system import did not publish the managed-site schedule update")
	}
	var setting sitemanagement.ManagedSiteSetting
	require.NoError(t, db.First(&setting, 1).Error)
	assert.Equal(t, "08:00,12:30", setting.ScheduleTimes)
}

func TestImportAllInvalidatesManagedSiteCachesAfterCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// ImportAll starts a database-reading goroutine after the transaction commits.
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	require.NoError(t, db.Create(&sitemanagement.ManagedSite{
		Name:     "Existing site",
		BaseURL:  "https://existing.example.com",
		SiteType: sitemanagement.SiteTypeNewAPI,
		AuthType: sitemanagement.AuthTypeNone,
	}).Error)

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	bindingService := sitemanagement.NewBindingService(db, services.ReadOnlyDB{DB: db}, nil)
	siteService := sitemanagement.NewSiteService(db, nil, encSvc)
	siteService.SetCacheInvalidationCallback(bindingService.InvalidateSitesForBindingCache)

	beforeSites, err := siteService.ListSites(context.Background())
	require.NoError(t, err)
	require.Len(t, beforeSites, 1)
	beforeBindings, err := bindingService.ListSitesForBinding(context.Background())
	require.NoError(t, err)
	require.Len(t, beforeBindings, 1)

	server := &Server{
		DB:                  db,
		EncryptionSvc:       encSvc,
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		SiteService:         siteService,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/system/import", strings.NewReader(`{
		"version":"2.0",
		"managed_sites":{"sites":[{
			"name":"Imported site",
			"base_url":"https://imported.example.com",
			"site_type":"new-api",
			"auth_type":"none",
			"balance_multiplier":3
		}]}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	afterSites, err := siteService.ListSites(context.Background())
	require.NoError(t, err)
	require.Len(t, afterSites, 2)
	assert.Equal(t, int64(3), afterSites[1].BalanceMultiplier)
	afterBindings, err := bindingService.ListSitesForBinding(context.Background())
	require.NoError(t, err)
	require.Len(t, afterBindings, 2)
}

func TestImportAllRejectsExplicitlyEmptyAutoCheckinScheduleTimes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// Keep the post-import goroutine on the same SQLite :memory: database if validation regresses.
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	server := &Server{
		DB:                  db,
		EncryptionSvc:       encSvc,
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/system/import", strings.NewReader(`{
		"version":"2.0",
		"managed_sites":{
			"auto_checkin":{"schedule_mode":"multiple","schedule_times":[]},
			"sites":[]
		}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportAll(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var count int64
	require.NoError(t, db.Model(&sitemanagement.ManagedSiteSetting{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestExportAllIncludesScheduleConfigWithoutManagedSites(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.DynamicWeightMetric{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	require.NoError(t, db.Create(&sitemanagement.ManagedSiteSetting{
		ID:                          1,
		AutoBalanceEnabled:          true,
		BalanceRefreshIntervalHours: 6,
	}).Error)
	require.NoError(t, db.Model(&sitemanagement.ManagedSiteSetting{}).
		Where("id = ?", 1).
		Update("auto_balance_enabled", false).Error)

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	server := &Server{
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/system/export", nil)

	server.ExportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var payload struct {
		Data SystemExportData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.NotNil(t, payload.Data.ManagedSites)
	require.NotNil(t, payload.Data.ManagedSites.AutoBalance)
	assert.False(t, payload.Data.ManagedSites.AutoBalance.GlobalEnabled)
	assert.Equal(t, 6, payload.Data.ManagedSites.AutoBalance.IntervalHours)
	assert.Empty(t, payload.Data.ManagedSites.Sites)
}

func TestExportAllReturnsPlainManagedSiteCredentialDecryptErrors(t *testing.T) {
	tests := []struct {
		name      string
		userID    string
		authType  string
		authValue string
	}{
		{
			name:     "user ID",
			userID:   "sensitive-invalid-ciphertext",
			authType: sitemanagement.AuthTypeNone,
		},
		{
			name:      "auth value",
			authType:  sitemanagement.AuthTypeAccessToken,
			authValue: "sensitive-invalid-ciphertext",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&models.SystemSetting{},
				&models.Group{},
				&models.DynamicWeightMetric{},
				&sitemanagement.ManagedSite{},
				&sitemanagement.ManagedSiteSetting{},
			))
			require.NoError(t, db.Create(&sitemanagement.ManagedSite{
				Name:      "site with invalid encrypted data",
				BaseURL:   "https://example.com",
				SiteType:  sitemanagement.SiteTypeNewAPI,
				Enabled:   true,
				UserID:    tt.userID,
				AuthType:  tt.authType,
				AuthValue: tt.authValue,
			}).Error)
			encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
			require.NoError(t, err)
			server := &Server{
				ImportExportService: services.NewImportExportService(db, nil, encSvc),
				EncryptionSvc:       encSvc,
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/system/export?mode=plain", nil)

			server.ExportAll(c)

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.NotContains(t, w.Body.String(), "sensitive-invalid-ciphertext")
			assert.Empty(t, w.Header().Get("Content-Disposition"))
		})
	}
}

func TestExportAllRejectsPlainKeyDecryptErrors(t *testing.T) {
	tests := []struct {
		name string
		kind string
	}{
		{name: "group key", kind: "group"},
		{name: "Hub access key", kind: "hub"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			require.NoError(t, db.AutoMigrate(
				&models.SystemSetting{},
				&models.Group{},
				&models.APIKey{},
				&models.DynamicWeightMetric{},
				&handlerHubAccessKeyTestModel{},
			))
			const invalidCiphertext = "sensitive-invalid-ciphertext"
			switch tt.kind {
			case "group":
				group := models.Group{
					Name:        "plain export failure group",
					GroupType:   "standard",
					ChannelType: "openai",
					Enabled:     true,
					TestModel:   "gpt-test",
					Upstreams:   datatypes.JSON(`[]`),
				}
				require.NoError(t, db.Create(&group).Error)
				require.NoError(t, db.Create(&models.APIKey{
					GroupID:  group.ID,
					KeyValue: invalidCiphertext,
					KeyHash:  "invalid-ciphertext-hash",
					Status:   models.KeyStatusActive,
				}).Error)
			case "hub":
				require.NoError(t, db.Create(&handlerHubAccessKeyTestModel{
					Name:     "plain export failure Hub key",
					KeyHash:  "invalid-hub-key-hash",
					KeyValue: invalidCiphertext,
					Enabled:  true,
				}).Error)
			}
			encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
			require.NoError(t, err)
			server := &Server{
				ImportExportService: services.NewImportExportService(db, nil, encSvc),
				EncryptionSvc:       encSvc,
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/system/export?mode=plain", nil)

			server.ExportAll(c)

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.NotContains(t, w.Body.String(), invalidCiphertext)
			assert.Empty(t, w.Header().Get("Content-Disposition"))
		})
	}
}

func TestExportAllPlainDecryptsGroupAndHubKeys(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.APIKey{},
		&models.DynamicWeightMetric{},
		&handlerHubAccessKeyTestModel{},
	))
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	encryptedGroupKey, err := encSvc.Encrypt("plain-group-key")
	require.NoError(t, err)
	encryptedHubKey, err := encSvc.Encrypt("plain-hub-key")
	require.NoError(t, err)
	group := models.Group{
		Name:        "plain export success group",
		GroupType:   "standard",
		ChannelType: "openai",
		Enabled:     true,
		TestModel:   "gpt-test",
		Upstreams:   datatypes.JSON(`[]`),
	}
	require.NoError(t, db.Create(&group).Error)
	require.NoError(t, db.Create(&models.APIKey{
		GroupID:  group.ID,
		KeyValue: encryptedGroupKey,
		KeyHash:  "valid-group-key-hash",
		Status:   models.KeyStatusActive,
	}).Error)
	require.NoError(t, db.Create(&handlerHubAccessKeyTestModel{
		Name:     "plain export success Hub key",
		KeyHash:  "valid-hub-key-hash",
		KeyValue: encryptedHubKey,
		Enabled:  true,
	}).Error)
	server := &Server{
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/system/export?mode=plain", nil)

	server.ExportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var payload struct {
		Data SystemExportData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Groups, 1)
	require.Len(t, payload.Data.Groups[0].Keys, 1)
	assert.Equal(t, "plain-group-key", payload.Data.Groups[0].Keys[0].KeyValue)
	require.Len(t, payload.Data.HubAccessKeys, 1)
	assert.Equal(t, "plain-hub-key", payload.Data.HubAccessKeys[0].KeyValue)
}

func TestExportGroupRejectsPlainKeyDecryptErrors(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Group{}, &models.APIKey{}))
	group := models.Group{
		Name:        "plain export failure group",
		GroupType:   "standard",
		ChannelType: "openai",
		Enabled:     true,
		TestModel:   "gpt-test",
		Upstreams:   datatypes.JSON(`[]`),
	}
	require.NoError(t, db.Create(&group).Error)
	const invalidCiphertext = "sensitive-invalid-ciphertext"
	require.NoError(t, db.Create(&models.APIKey{
		GroupID:  group.ID,
		KeyValue: invalidCiphertext,
		KeyHash:  "invalid-ciphertext-hash",
		Status:   models.KeyStatusActive,
	}).Error)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	server := &Server{
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(group.ID), 10)}}
	c.Request = httptest.NewRequest(http.MethodGet, "/groups/1/export?mode=plain", nil)

	server.ExportGroup(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), invalidCiphertext)
	assert.Empty(t, w.Header().Get("Content-Disposition"))
}

func TestExportAllNormalizesNoAuthManagedSiteAndOmitsCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.DynamicWeightMetric{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	const staleCredential = "sensitive-stale-ciphertext"
	require.NoError(t, db.Create(&sitemanagement.ManagedSite{
		Name:      "site without authentication",
		BaseURL:   "https://example.com",
		SiteType:  sitemanagement.SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  " \t ",
		AuthValue: staleCredential,
	}).Error)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	server := &Server{
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/system/export?mode=plain", nil)

	server.ExportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var payload struct {
		Data SystemExportData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.NotNil(t, payload.Data.ManagedSites)
	require.Len(t, payload.Data.ManagedSites.Sites, 1)
	assert.Equal(t, sitemanagement.AuthTypeNone, payload.Data.ManagedSites.Sites[0].AuthType)
	assert.Empty(t, payload.Data.ManagedSites.Sites[0].AuthValue)
	assert.NotContains(t, w.Body.String(), staleCredential)
}

func TestImportAllNormalizesNoAuthManagedSiteAndClearsCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.DynamicWeightMetric{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// ImportAll reads the SQLite :memory: database from a post-commit goroutine.
	sqlDB.SetMaxOpenConns(1)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	server := &Server{
		DB:                  db,
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	const staleCredential = "sensitive-plain-credential"
	body := `{
		"version":"2.0",
		"managed_sites":{"sites":[{
			"name":"site without authentication",
			"enabled":true,
			"base_url":"https://example.com",
			"site_type":"new-api",
			"auth_type":"  ",
			"auth_value":"` + staleCredential + `"
		}]}
	}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/system/import?mode=plain", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var imported sitemanagement.ManagedSite
	require.NoError(t, db.Where("name = ?", "site without authentication").First(&imported).Error)
	assert.Equal(t, sitemanagement.AuthTypeNone, imported.AuthType)
	assert.Empty(t, imported.AuthValue)
	assert.NotContains(t, w.Body.String(), staleCredential)
}

func TestSystemPlainExportImportRoundTripsManagedSiteCredentials(t *testing.T) {
	openDB := func(t *testing.T) *gorm.DB {
		t.Helper()
		db := setupTestDB(t)
		require.NoError(t, db.AutoMigrate(
			&models.SystemSetting{},
			&models.Group{},
			&models.DynamicWeightMetric{},
			&sitemanagement.ManagedSite{},
			&sitemanagement.ManagedSiteSetting{},
		))
		return db
	}

	sourceDB := openDB(t)
	sourceEncryption, err := encryption.NewService("source-test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	encryptedUserID, err := sourceEncryption.Encrypt("plain-user-id")
	require.NoError(t, err)
	encryptedAuth, err := sourceEncryption.Encrypt("plain-auth-value")
	require.NoError(t, err)
	require.NoError(t, sourceDB.Create(&sitemanagement.ManagedSite{
		Name:              "round-trip site",
		BaseURL:           "https://example.com",
		SiteType:          sitemanagement.SiteTypeNewAPI,
		Enabled:           true,
		UserID:            encryptedUserID,
		AuthType:          sitemanagement.AuthTypeAccessToken,
		AuthValue:         encryptedAuth,
		UseProxy:          true,
		ProxyURL:          "proxy-pool:7",
		BypassMethod:      sitemanagement.BypassMethodStealth,
		BalanceMultiplier: 7,
	}).Error)
	sourceServer := &Server{
		ImportExportService: services.NewImportExportService(sourceDB, nil, sourceEncryption),
		EncryptionSvc:       sourceEncryption,
	}
	exportRecorder := httptest.NewRecorder()
	exportContext, _ := gin.CreateTestContext(exportRecorder)
	exportContext.Request = httptest.NewRequest(http.MethodGet, "/system/export?mode=plain", nil)

	sourceServer.ExportAll(exportContext)

	require.Equal(t, http.StatusOK, exportRecorder.Code)
	var exported struct {
		Data SystemExportData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(exportRecorder.Body.Bytes(), &exported))
	require.NotNil(t, exported.Data.ManagedSites)
	require.Len(t, exported.Data.ManagedSites.Sites, 1)
	assert.Equal(t, "plain-user-id", exported.Data.ManagedSites.Sites[0].UserID)
	assert.Equal(t, "plain-auth-value", exported.Data.ManagedSites.Sites[0].AuthValue)
	assert.True(t, exported.Data.ManagedSites.Sites[0].UseProxy)
	assert.Equal(t, "proxy-pool:7", exported.Data.ManagedSites.Sites[0].ProxyURL)
	assert.Equal(t, sitemanagement.BypassMethodStealth, exported.Data.ManagedSites.Sites[0].BypassMethod)
	assert.Equal(t, int64(7), exported.Data.ManagedSites.Sites[0].BalanceMultiplier)

	importBody, err := json.Marshal(exported.Data)
	require.NoError(t, err)
	targetDB := openDB(t)
	targetSQLDB, err := targetDB.DB()
	require.NoError(t, err)
	// ImportAll reads the SQLite :memory: database from a post-commit goroutine.
	targetSQLDB.SetMaxOpenConns(1)
	targetEncryption, err := encryption.NewService("target-test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	targetServer := &Server{
		DB:                  targetDB,
		EncryptionSvc:       targetEncryption,
		ImportExportService: services.NewImportExportService(targetDB, nil, targetEncryption),
	}
	importRecorder := httptest.NewRecorder()
	importContext, _ := gin.CreateTestContext(importRecorder)
	importContext.Request = httptest.NewRequest(http.MethodPost, "/system/import?mode=plain", bytes.NewReader(importBody))
	importContext.Request.Header.Set("Content-Type", "application/json")

	targetServer.ImportAll(importContext)

	require.Equal(t, http.StatusOK, importRecorder.Code)
	var imported sitemanagement.ManagedSite
	require.NoError(t, targetDB.Where("name = ?", "round-trip site").First(&imported).Error)
	userID, err := targetEncryption.Decrypt(imported.UserID)
	require.NoError(t, err)
	authValue, err := targetEncryption.Decrypt(imported.AuthValue)
	require.NoError(t, err)
	assert.Equal(t, "plain-user-id", userID)
	assert.Equal(t, "plain-auth-value", authValue)
	assert.True(t, imported.UseProxy)
	assert.Equal(t, "proxy-pool:7", imported.ProxyURL)
	assert.Equal(t, sitemanagement.BypassMethodStealth, imported.BypassMethod)
	assert.Equal(t, int64(7), imported.BalanceMultiplier)
}

func TestSystemImportAutoDetectsEncryptedManagedSiteCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.DynamicWeightMetric{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// ImportAll reads the SQLite :memory: database from a post-commit goroutine.
	sqlDB.SetMaxOpenConns(1)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	encryptedUserID, err := encSvc.Encrypt("plain-user-id")
	require.NoError(t, err)
	encryptedAuth, err := encSvc.Encrypt("plain-auth-value")
	require.NoError(t, err)
	payload := SystemImportData{
		Version: "2.0",
		ManagedSites: &ManagedSitesExportData{Sites: []ManagedSiteExportInfo{{
			Name:      "auto-detected encrypted site",
			Enabled:   true,
			BaseURL:   "https://example.com",
			SiteType:  sitemanagement.SiteTypeNewAPI,
			UserID:    encryptedUserID,
			AuthType:  sitemanagement.AuthTypeAccessToken,
			AuthValue: encryptedAuth,
		}}},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	server := &Server{
		DB:                  db,
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/system/import", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var imported sitemanagement.ManagedSite
	require.NoError(t, db.Where("name = ?", "auto-detected encrypted site").First(&imported).Error)
	userID, err := encSvc.Decrypt(imported.UserID)
	require.NoError(t, err)
	authValue, err := encSvc.Decrypt(imported.AuthValue)
	require.NoError(t, err)
	assert.Equal(t, "plain-user-id", userID)
	assert.Equal(t, "plain-auth-value", authValue)
}

func TestSystemImportAutoDetectsPlainHexLikeUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.DynamicWeightMetric{},
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// ImportAll reads the SQLite :memory: database from a post-commit goroutine.
	sqlDB.SetMaxOpenConns(1)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	const plainUserID = "1234567890123456"
	payload := SystemImportData{
		Version: "2.0",
		ManagedSites: &ManagedSitesExportData{Sites: []ManagedSiteExportInfo{{
			Name:     "plain hex-like user ID",
			Enabled:  true,
			BaseURL:  "https://example.com",
			SiteType: sitemanagement.SiteTypeNewAPI,
			UserID:   plainUserID,
			AuthType: sitemanagement.AuthTypeNone,
		}}},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	server := &Server{
		DB:                  db,
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/system/import", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var imported sitemanagement.ManagedSite
	require.NoError(t, db.Where("name = ?", "plain hex-like user ID").First(&imported).Error)
	userID, err := encSvc.Decrypt(imported.UserID)
	require.NoError(t, err)
	assert.Equal(t, plainUserID, userID)
}

func TestSystemImportAutoDetectsEncryptedHubAccessKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.SystemSetting{},
		&models.Group{},
		&models.DynamicWeightMetric{},
		&handlerHubAccessKeyTestModel{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// ImportAll reads the SQLite :memory: database from a post-commit goroutine.
	sqlDB.SetMaxOpenConns(1)
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	encryptedKey, err := encSvc.Encrypt("plain-hub-access-key")
	require.NoError(t, err)
	payload := SystemImportData{
		Version: "2.0",
		HubAccessKeys: []services.HubAccessKeyExportInfo{{
			Name:     "auto-detected encrypted hub key",
			KeyValue: encryptedKey,
			Enabled:  true,
		}},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	server := &Server{
		DB:                  db,
		ImportExportService: services.NewImportExportService(db, nil, encSvc),
		EncryptionSvc:       encSvc,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/system/import", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportAll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var imported handlerHubAccessKeyTestModel
	require.NoError(t, db.Where("name = ?", "auto-detected encrypted hub key").First(&imported).Error)
	plainKey, err := encSvc.Decrypt(imported.KeyValue)
	require.NoError(t, err)
	assert.Equal(t, "plain-hub-access-key", plainKey)
}

func TestManagedSiteImportAutoDetectsEncryptedUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	encryptedUserID, err := encSvc.Encrypt("plain-user-id")
	require.NoError(t, err)
	sites := make([]sitemanagement.SiteExportInfo, 0, 6)
	for i := 0; i < 5; i++ {
		sites = append(sites, sitemanagement.SiteExportInfo{
			Name:     fmt.Sprintf("site without import sample %d", i),
			Enabled:  true,
			BaseURL:  fmt.Sprintf("https://empty-%d.example.com", i),
			SiteType: sitemanagement.SiteTypeNewAPI,
			AuthType: sitemanagement.AuthTypeNone,
		})
	}
	sites = append(sites, sitemanagement.SiteExportInfo{
		Name:     "auto-detected encrypted user ID",
		Enabled:  true,
		BaseURL:  "https://example.com",
		SiteType: sitemanagement.SiteTypeNewAPI,
		UserID:   encryptedUserID,
		AuthType: sitemanagement.AuthTypeNone,
	})
	payload := SiteImportRequest{
		Version: "1.0",
		Sites:   sites,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	server := &Server{
		SiteService: sitemanagement.NewSiteService(db, nil, encSvc),
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/site-management/import", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportManagedSites(c)

	require.Equal(t, http.StatusOK, w.Code)
	var imported sitemanagement.ManagedSite
	require.NoError(t, db.Where("name = ?", "auto-detected encrypted user ID").First(&imported).Error)
	userID, err := encSvc.Decrypt(imported.UserID)
	require.NoError(t, err)
	assert.Equal(t, "plain-user-id", userID)
}

func TestImportManagedSitesRequestsLocalBalanceReschedule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&sitemanagement.ManagedSite{},
		&sitemanagement.ManagedSiteSetting{},
	))
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	balanceService := sitemanagement.NewBalanceService(db, encSvc)
	server := &Server{
		SiteService:    sitemanagement.NewSiteService(db, nil, encSvc),
		BalanceService: balanceService,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/site-management/import", strings.NewReader(`{
		"version":"1.0",
		"auto_balance":{"global_enabled":false,"interval_hours":6},
		"sites":[]
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	server.ImportManagedSites(c)

	require.Equal(t, http.StatusOK, w.Code)
	rescheduleCh := reflect.ValueOf(balanceService).Elem().FieldByName("rescheduleCh")
	assert.Equal(t, 1, rescheduleCh.Len())
}
