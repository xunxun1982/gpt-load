package sitemanagement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gpt-load/internal/encryption"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestBalanceService_FetchSiteBalance tests balance fetching for a single site
func TestBalanceService_FetchSiteBalance(t *testing.T) {
	t.Parallel()

	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/user/self", r.URL.Path)
		resp := userSelfResponse{
			Success: true,
			Data: struct {
				Quota int64 `json:"quota"`
			}{
				Quota: 500000, // $1.00
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewBalanceService(db, encSvc)
	invalidations := 0
	service.SetCacheInvalidationCallback(func() {
		invalidations++
	})

	// Create test site
	authValue, _ := encSvc.Encrypt("test-token")
	site := &ManagedSite{
		Name:      "Test Site",
		BaseURL:   server.URL,
		SiteType:  SiteTypeNewAPI,
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}
	err = db.Create(site).Error
	require.NoError(t, err)

	// Fetch balance
	result := service.FetchSiteBalance(context.Background(), site)
	require.NotNil(t, result)
	require.NotNil(t, result.Balance)
	assert.Equal(t, "$1.00", *result.Balance)

	var updated ManagedSite
	require.NoError(t, db.First(&updated, site.ID).Error)
	assert.Equal(t, "$1.00", updated.LastBalance)
	assert.Equal(t, 1, invalidations)
}

func TestBalanceService_FetchSiteBalanceClearsStaleBalanceAndInvalidatesCache(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	service := NewBalanceService(db, encSvc)
	invalidations := 0
	service.SetCacheInvalidationCallback(func() {
		invalidations++
	})

	site := &ManagedSite{
		Name:        "No Auth Site",
		BaseURL:     "https://example.com",
		SiteType:    SiteTypeNewAPI,
		LastBalance: "$9.99",
	}
	require.NoError(t, db.Create(site).Error)

	result := service.FetchSiteBalance(context.Background(), site)
	require.NotNil(t, result)
	assert.Nil(t, result.Balance)

	var updated ManagedSite
	require.NoError(t, db.First(&updated, site.ID).Error)
	assert.Empty(t, updated.LastBalance)
	assert.Equal(t, 1, invalidations)
}

func TestBalanceService_FetchSub2APIBalance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		authToken          string
		cookieSession      string
		expectedAuthHeader string
		response           string
		expectedBalance    *string
	}{
		{
			name:               "happy path",
			authToken:          "test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"code":0,"message":"success","data":{"balance":12.5}}`,
			expectedBalance:    stringPtr("$12.50"),
		},
		{
			name:               "access token with browser session cookie",
			authToken:          "test-token",
			cookieSession:      "session=browser-ok",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"code":0,"message":"success","data":{"balance":12.5}}`,
			expectedBalance:    stringPtr("$12.50"),
		},
		{
			name:               "already bearer prefixed token",
			authToken:          "Bearer test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"code":0,"message":"success","data":{"balance":12.5}}`,
			expectedBalance:    stringPtr("$12.50"),
		},
		{
			name:               "success false",
			authToken:          "test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"success":false,"data":{"balance":12.5}}`,
			expectedBalance:    nil,
		},
		{
			name:               "nonzero code",
			authToken:          "test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"code":1,"message":"failed","data":{"balance":12.5}}`,
			expectedBalance:    nil,
		},
		{
			name:               "zero data balance",
			authToken:          "test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"code":0,"message":"success","data":{"balance":0}}`,
			expectedBalance:    stringPtr("$0.00"),
		},
		{
			name:               "root balance fallback",
			authToken:          "test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"code":0,"message":"success","balance":8.75}`,
			expectedBalance:    stringPtr("$8.75"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v1/user/profile", r.URL.Path)
				assert.Equal(t, tt.expectedAuthHeader, r.Header.Get("Authorization"))
				assert.Equal(t, tt.cookieSession, r.Header.Get("Cookie"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			db := setupTestDB(t)
			encSvc := setupTestEncryption(t)

			require.NoError(t, db.AutoMigrate(&ManagedSite{}))

			service := NewBalanceService(db, encSvc)

			authType := AuthTypeAccessToken
			authPlainValue := tt.authToken
			if tt.cookieSession != "" {
				authType = AuthTypeAccessToken + "," + AuthTypeCookie
				authPlainValue = fmt.Sprintf(`{"%s":%q,"%s":%q}`, AuthTypeAccessToken, tt.authToken, AuthTypeCookie, tt.cookieSession)
			}
			authValue, err := encSvc.Encrypt(authPlainValue)
			require.NoError(t, err)
			site := &ManagedSite{
				Name:      "Sub2API Site",
				BaseURL:   server.URL + "/check-in",
				SiteType:  SiteTypeSub2API,
				AuthType:  authType,
				AuthValue: authValue,
			}
			require.NoError(t, db.Create(site).Error)

			result := service.FetchSiteBalance(context.Background(), site)
			require.NotNil(t, result)
			if tt.expectedBalance == nil {
				assert.Nil(t, result.Balance)
				return
			}
			require.NotNil(t, result.Balance)
			assert.Equal(t, *tt.expectedBalance, *result.Balance)
		})
	}
}

// TestBalanceService_FetchSiteBalance_NoAuth tests balance fetch without auth
func TestBalanceService_FetchSiteBalance_NoAuth(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewBalanceService(db, encSvc)

	site := &ManagedSite{
		Name:      "Test Site",
		BaseURL:   "https://example.com",
		SiteType:  SiteTypeNewAPI,
		AuthType:  AuthTypeNone,
		AuthValue: "",
	}

	result := service.FetchSiteBalance(context.Background(), site)
	require.NotNil(t, result)
	assert.Nil(t, result.Balance) // No balance without auth
}

// TestBalanceService_FetchSiteBalance_UnsupportedType tests unsupported site type
func TestBalanceService_FetchSiteBalance_UnsupportedType(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewBalanceService(db, encSvc)

	authValue, _ := encSvc.Encrypt("test-token")
	site := &ManagedSite{
		Name:      "Test Site",
		BaseURL:   "https://example.com",
		SiteType:  SiteTypeUnknown, // Unsupported type
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}

	result := service.FetchSiteBalance(context.Background(), site)
	require.NotNil(t, result)
	assert.Nil(t, result.Balance) // No balance for unsupported type
}

// TestBalanceService_ParseBalanceResponse tests balance response parsing
func TestBalanceService_ParseBalanceResponse(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewBalanceService(db, encSvc)

	tests := []struct {
		name     string
		response string
		expected *string
	}{
		{
			name:     "Success with data wrapper",
			response: `{"success":true,"data":{"quota":500000}}`,
			expected: stringPtr("$1.00"),
		},
		{
			name:     "Success with root quota",
			response: `{"success":true,"quota":1000000}`,
			expected: stringPtr("$2.00"),
		},
		{
			name:     "Zero balance",
			response: `{"success":true,"data":{"quota":0}}`,
			expected: stringPtr("$0.00"),
		},
		{
			name:     "Negative balance",
			response: `{"success":true,"data":{"quota":-500000}}`,
			expected: stringPtr("$-1.00"),
		},
		{
			name:     "Failed response",
			response: `{"success":false,"data":{"quota":500000}}`,
			expected: nil,
		},
		{
			name:     "Invalid JSON",
			response: `invalid json`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := service.parseBalanceResponse([]byte(tt.response))
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestBuildBalanceHeaders(t *testing.T) {
	t.Parallel()

	t.Run("access token", func(t *testing.T) {
		t.Parallel()

		headers := buildBalanceHeaders(AuthTypeAccessToken, "test-access-token", "123", "")

		assert.Equal(t, "Bearer test-access-token", headers["Authorization"])
		assert.Equal(t, "123", headers["New-API-User"])
		assert.Empty(t, headers["Cookie"])
	})

	t.Run("access token with browser session cookie", func(t *testing.T) {
		t.Parallel()

		headers := buildBalanceHeaders(AuthTypeAccessToken, "test-access-token", "123", "session=browser")

		assert.Equal(t, "Bearer test-access-token", headers["Authorization"])
		assert.Equal(t, "123", headers["New-API-User"])
		assert.Equal(t, "session=browser", headers["Cookie"])
	})

	t.Run("cookie", func(t *testing.T) {
		t.Parallel()

		headers := buildBalanceHeaders(AuthTypeCookie, "session=test", "123", "")

		assert.Equal(t, "session=test", headers["Cookie"])
		assert.Equal(t, "123", headers["New-API-User"])
		assert.Empty(t, headers["Authorization"])
	})

	t.Run("unsupported", func(t *testing.T) {
		t.Parallel()

		assert.Nil(t, buildBalanceHeaders(AuthTypeNone, "unused", "123", ""))
	})
}

// TestBalanceService_FetchAllBalances tests concurrent balance fetching
func TestBalanceService_FetchAllBalances(t *testing.T) {
	t.Parallel()

	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := userSelfResponse{
			Success: true,
			Data: struct {
				Quota int64 `json:"quota"`
			}{
				Quota: 500000,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewBalanceService(db, encSvc)

	// Create multiple sites
	var sites []ManagedSite
	for i := 0; i < 10; i++ {
		authValue, _ := encSvc.Encrypt("test-token")
		site := ManagedSite{
			Name:      "Site " + string(rune('A'+i)),
			BaseURL:   server.URL,
			SiteType:  SiteTypeNewAPI,
			AuthType:  AuthTypeAccessToken,
			AuthValue: authValue,
		}
		err = db.Create(&site).Error
		require.NoError(t, err)
		sites = append(sites, site)
	}

	// Fetch all balances
	results := service.FetchAllBalances(context.Background(), sites)
	assert.Len(t, results, 10)

	for _, result := range results {
		assert.NotNil(t, result.Balance)
	}
}

// TestBalanceService_GetHTTPClient tests HTTP client selection
func TestBalanceService_GetHTTPClient(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewBalanceService(db, encSvc)

	t.Run("Direct connection", func(t *testing.T) {
		site := &ManagedSite{
			UseProxy: false,
		}
		client := service.getHTTPClient(site)
		assert.Same(t, service.client, client)
	})

	t.Run("Proxy connection", func(t *testing.T) {
		site := &ManagedSite{
			UseProxy: true,
			ProxyURL: "http://proxy:8080",
		}
		client := service.getHTTPClient(site)
		assert.NotSame(t, service.client, client)

		// Second call should return cached client
		client2 := service.getHTTPClient(site)
		assert.Same(t, client, client2)
	})

	t.Run("Stealth bypass", func(t *testing.T) {
		site := &ManagedSite{
			BypassMethod: BypassMethodStealth,
		}
		client := service.getHTTPClient(site)
		assert.NotSame(t, service.client, client)
	})
}

// TestBalanceService_UpdateBalancesInDB tests database update
func TestBalanceService_UpdateBalancesInDB(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewBalanceService(db, encSvc)

	// Create site
	site := ManagedSite{
		Name:     "Test Site",
		BaseURL:  "https://example.com",
		SiteType: SiteTypeNewAPI,
	}
	err = db.Create(&site).Error
	require.NoError(t, err)

	// Update balance
	balance := "$5.00"
	results := map[uint]*BalanceInfo{
		site.ID: {
			SiteID:  site.ID,
			Balance: &balance,
		},
	}

	service.updateBalancesInDB(context.Background(), results)

	// Verify update
	var updated ManagedSite
	err = db.First(&updated, site.ID).Error
	require.NoError(t, err)
	assert.Equal(t, "$5.00", updated.LastBalance)
	assert.NotEmpty(t, updated.LastBalanceDate)
}

// TestBalanceService_RefreshScheduler tests the background refresh scheduler
func TestBalanceService_RefreshScheduler(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewBalanceService(db, encSvc)

	// Test nextRefreshTime calculation
	nextRefresh := service.nextRefreshTime()
	assert.True(t, nextRefresh.After(time.Now()))

	// Verify it's at local midnight.
	localTime := nextRefresh.In(checkinLocation())
	assert.Equal(t, 0, localTime.Hour())
	assert.Equal(t, 0, localTime.Minute())
}

func TestBalanceService_NextRefreshTimeKeepsMidnightAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	now := time.Date(2026, 3, 8, 23, 0, 0, 0, loc)
	nextRefresh := nextRefreshTimeAtLocation(now, loc)

	localTime := nextRefresh.In(loc)
	assert.Equal(t, 2026, localTime.Year())
	assert.Equal(t, time.March, localTime.Month())
	assert.Equal(t, 9, localTime.Day())
	assert.Equal(t, 0, localTime.Hour())
	assert.Equal(t, 0, localTime.Minute())
}

// TestBalanceService_SupportsBalance tests site type support check
func TestBalanceService_SupportsBalance(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewBalanceService(db, encSvc)

	tests := []struct {
		siteType string
		expected bool
	}{
		{SiteTypeNewAPI, true},
		{SiteTypeSub2API, true},
		{SiteTypeVeloera, true},
		{SiteTypeOneHub, true},
		{SiteTypeDoneHub, true},
		{SiteTypeWongGongyi, true},
		{SiteTypeAnyrouter, false},
		{SiteTypeUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.siteType, func(t *testing.T) {
			result := service.supportsBalance(tt.siteType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// BenchmarkBalanceService_FetchAllBalances benchmarks concurrent balance fetching
func BenchmarkBalanceService_FetchAllBalances(b *testing.B) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := userSelfResponse{
			Success: true,
			Data: struct {
				Quota int64 `json:"quota"`
			}{
				Quota: 500000,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")

	db.AutoMigrate(&ManagedSite{})

	service := NewBalanceService(db, encSvc)

	// Create 50 sites
	var sites []ManagedSite
	for i := 0; i < 50; i++ {
		authValue, _ := encSvc.Encrypt("test-token")
		site := ManagedSite{
			Name:      fmt.Sprintf("Site %d", i),
			BaseURL:   server.URL,
			SiteType:  SiteTypeNewAPI,
			AuthType:  AuthTypeAccessToken,
			AuthValue: authValue,
		}
		db.Create(&site)
		sites = append(sites, site)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		service.FetchAllBalances(context.Background(), sites)
	}
}

// BenchmarkBalanceService_ParseBalanceResponse benchmarks response parsing
func BenchmarkBalanceService_ParseBalanceResponse(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	service := NewBalanceService(db, encSvc)

	data := []byte(`{"success":true,"data":{"quota":500000}}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		service.parseBalanceResponse(data)
	}
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}
