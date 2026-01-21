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

	// Verify it's at 05:00 Beijing time
	beijingTime := nextRefresh.In(beijingLocation)
	assert.Equal(t, 5, beijingTime.Hour())
	assert.Equal(t, 0, beijingTime.Minute())
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
