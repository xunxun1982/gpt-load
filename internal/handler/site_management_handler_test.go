package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gpt-load/internal/encryption"
	"gpt-load/internal/i18n"
	"gpt-load/internal/models"
	"gpt-load/internal/sitemanagement"
	"gpt-load/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
}
