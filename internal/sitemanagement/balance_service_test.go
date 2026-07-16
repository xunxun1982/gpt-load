package sitemanagement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/store"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type subscribeHookStore struct {
	store.Store
	beforeSubscribe func()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (s *subscribeHookStore) Subscribe(channel string) (store.Subscription, error) {
	if s.beforeSubscribe != nil {
		s.beforeSubscribe()
	}
	return s.Store.Subscribe(channel)
}

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

func TestBalanceService_FetchSiteBalanceKeepsCachedBalanceOnFetchFailure(t *testing.T) {
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
		Name:            "No Auth Site",
		BaseURL:         "https://example.com",
		SiteType:        SiteTypeNewAPI,
		LastBalance:     "$9.99",
		LastBalanceDate: "2026-01-01",
	}
	require.NoError(t, db.Create(site).Error)

	result := service.FetchSiteBalance(context.Background(), site)
	require.NotNil(t, result)
	assert.Nil(t, result.Balance)

	var updated ManagedSite
	require.NoError(t, db.First(&updated, site.ID).Error)
	assert.Equal(t, "$9.99", updated.LastBalance)
	assert.Equal(t, "2026-01-01", updated.LastBalanceDate)
	assert.Equal(t, 0, invalidations)
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

			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount.Add(1)
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
			t.Cleanup(service.closeIdleConnections)

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
			assert.Equal(t, int32(1), requestCount.Load())
			if tt.expectedBalance == nil {
				assert.Nil(t, result.Balance)
				return
			}
			require.NotNil(t, result.Balance)
			assert.Equal(t, *tt.expectedBalance, *result.Balance)
		})
	}
}

func TestBalanceService_FetchSiteBalanceIsolatesConfiguredCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		siteType         string
		acceptedAuthType string
		response         string
		expectedBalance  string
		expectedRequests int32
	}{
		{
			name:             "new api token succeeds when cookie fails",
			siteType:         SiteTypeNewAPI,
			acceptedAuthType: AuthTypeAccessToken,
			response:         `{"success":true,"data":{"quota":500000}}`,
			expectedBalance:  "$1.00",
			expectedRequests: 2,
		},
		{
			name:             "new api cookie succeeds when token fails",
			siteType:         SiteTypeNewAPI,
			acceptedAuthType: AuthTypeCookie,
			response:         `{"success":true,"data":{"quota":500000}}`,
			expectedBalance:  "$1.00",
			expectedRequests: 3,
		},
		{
			name:             "sub2api token succeeds when cookie fails",
			siteType:         SiteTypeSub2API,
			acceptedAuthType: AuthTypeAccessToken,
			response:         `{"code":0,"data":{"balance":12.5}}`,
			expectedBalance:  "$12.50",
			expectedRequests: 2,
		},
		{
			name:             "sub2api cookie succeeds when token fails",
			siteType:         SiteTypeSub2API,
			acceptedAuthType: AuthTypeCookie,
			response:         `{"code":0,"data":{"balance":12.5}}`,
			expectedBalance:  "$12.50",
			expectedRequests: 3,
		},
		{
			name:             "all credentials fail and cached balance remains",
			siteType:         SiteTypeNewAPI,
			expectedRequests: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount.Add(1)
				assert.Equal(t, resolveSiteCapabilities(tt.siteType).BalanceEndpoint, r.URL.Path)

				// Accept only an isolated credential so an invalid companion credential cannot be ignored by the test server.
				tokenOnly := r.Header.Get("Authorization") == "Bearer valid-token" && r.Header.Get("Cookie") == ""
				cookieOnly := r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "session=valid"
				authorized := (tt.acceptedAuthType == AuthTypeAccessToken && tokenOnly) ||
					(tt.acceptedAuthType == AuthTypeCookie && cookieOnly)
				if !authorized {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			t.Cleanup(server.Close)

			db := setupTestDB(t)
			encSvc := setupTestEncryption(t)
			require.NoError(t, db.AutoMigrate(&ManagedSite{}))

			token := "invalid-token"
			cookie := "session=invalid"
			switch tt.acceptedAuthType {
			case AuthTypeAccessToken:
				token = "valid-token"
			case AuthTypeCookie:
				cookie = "session=valid"
			}
			plainAuth, err := json.Marshal(map[string]string{
				AuthTypeAccessToken: token,
				AuthTypeCookie:      cookie,
			})
			require.NoError(t, err)
			encryptedAuth, err := encSvc.Encrypt(string(plainAuth))
			require.NoError(t, err)

			site := &ManagedSite{
				Name:            tt.name,
				BaseURL:         server.URL,
				SiteType:        tt.siteType,
				AuthType:        AuthTypeAccessToken + "," + AuthTypeCookie,
				AuthValue:       encryptedAuth,
				LastBalance:     "$9.99",
				LastBalanceDate: "2026-01-01",
			}
			require.NoError(t, db.Create(site).Error)

			service := NewBalanceService(db, encSvc)
			t.Cleanup(service.closeIdleConnections)
			result := service.FetchSiteBalance(context.Background(), site)

			require.NotNil(t, result)
			assert.Equal(t, tt.expectedRequests, requestCount.Load())
			if tt.expectedBalance == "" {
				assert.Nil(t, result.Balance)
				var stored ManagedSite
				require.NoError(t, db.First(&stored, site.ID).Error)
				assert.Equal(t, "$9.99", stored.LastBalance)
				assert.Equal(t, "2026-01-01", stored.LastBalanceDate)
				return
			}
			require.NotNil(t, result.Balance)
			assert.Equal(t, tt.expectedBalance, *result.Balance)
		})
	}
}

func TestBalanceService_FetchBalanceStopsAuthFallbackAfterContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var requestCount atomic.Int32

	service := NewBalanceService(setupTestDB(t), setupTestEncryption(t))
	t.Cleanup(service.closeIdleConnections)
	service.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount.Add(1)
		cancel()
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}

	authConfig := AuthConfig{
		AuthTypes: []string{AuthTypeAccessToken, AuthTypeCookie},
		AuthValues: map[string]string{
			AuthTypeAccessToken: "test-token",
			AuthTypeCookie:      "session=test",
		},
	}
	site := &ManagedSite{
		BaseURL:  "https://example.test",
		SiteType: SiteTypeNewAPI,
	}

	balance := service.fetchBalanceFromAPI(ctx, site, authConfig, "")

	assert.Nil(t, balance)
	assert.Equal(t, int32(1), requestCount.Load())
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
		client := service.getHTTPClient(context.Background(), site)
		assert.Same(t, service.client, client)
	})

	t.Run("Proxy connection", func(t *testing.T) {
		site := &ManagedSite{
			UseProxy: true,
			ProxyURL: "http://proxy:8080",
		}
		client := service.getHTTPClient(context.Background(), site)
		assert.NotSame(t, service.client, client)

		// Second call should return cached client
		client2 := service.getHTTPClient(context.Background(), site)
		assert.Same(t, client, client2)
	})

	t.Run("Stealth bypass", func(t *testing.T) {
		site := &ManagedSite{
			BypassMethod: BypassMethodStealth,
		}
		client := service.getHTTPClient(context.Background(), site)
		assert.NotSame(t, service.client, client)
	})
}

// TestBalanceService_UpdateBalancesInDB tests database updates for successful fetches only.
func TestBalanceService_UpdateBalancesInDB(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	err := db.AutoMigrate(&ManagedSite{})
	require.NoError(t, err)

	service := NewBalanceService(db, encSvc)
	invalidations := 0
	service.SetCacheInvalidationCallback(func() {
		invalidations++
	})

	updatedSite := ManagedSite{
		Name:     "Updated Site",
		BaseURL:  "https://updated.example.com",
		SiteType: SiteTypeNewAPI,
	}
	failedSite := ManagedSite{
		Name:            "Failed Site",
		BaseURL:         "https://failed.example.com",
		SiteType:        SiteTypeNewAPI,
		LastBalance:     "$9.99",
		LastBalanceDate: "2026-01-01",
	}
	emptySite := ManagedSite{
		Name:            "Empty Site",
		BaseURL:         "https://empty.example.com",
		SiteType:        SiteTypeNewAPI,
		LastBalance:     "$1.23",
		LastBalanceDate: "2026-01-01",
	}
	require.NoError(t, db.Create(&updatedSite).Error)
	require.NoError(t, db.Create(&failedSite).Error)
	require.NoError(t, db.Create(&emptySite).Error)

	balance := "$5.00"
	emptyBalance := ""
	results := map[uint]*BalanceInfo{
		updatedSite.ID: {
			SiteID:  updatedSite.ID,
			Balance: &balance,
		},
		failedSite.ID: {
			SiteID: failedSite.ID,
		},
		emptySite.ID: {
			SiteID:  emptySite.ID,
			Balance: &emptyBalance,
		},
	}

	service.updateBalancesInDB(context.Background(), results)

	var updated ManagedSite
	require.NoError(t, db.First(&updated, updatedSite.ID).Error)
	assert.Equal(t, "$5.00", updated.LastBalance)
	assert.NotEmpty(t, updated.LastBalanceDate)

	var failed ManagedSite
	require.NoError(t, db.First(&failed, failedSite.ID).Error)
	assert.Equal(t, "$9.99", failed.LastBalance)
	assert.Equal(t, "2026-01-01", failed.LastBalanceDate)

	var empty ManagedSite
	require.NoError(t, db.First(&empty, emptySite.ID).Error)
	assert.Empty(t, empty.LastBalance)
	assert.Equal(t, GetBeijingCheckinDay(), empty.LastBalanceDate)
	assert.Equal(t, 1, invalidations)
}

func TestNextBalanceRefreshTimeAtUsesTimezoneAnchoredIntervals(t *testing.T) {
	t.Parallel()

	shanghai := time.FixedZone("UTC+8", 8*60*60)
	western := time.FixedZone("UTC-5", -5*60*60)
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	tests := []struct {
		name          string
		now           time.Time
		intervalHours int
		location      *time.Location
		expected      time.Time
	}{
		{
			name:          "six hour interval uses the next slot",
			now:           time.Date(2026, 7, 15, 1, 30, 0, 0, shanghai),
			intervalHours: 6,
			location:      shanghai,
			expected:      time.Date(2026, 7, 15, 6, 0, 0, 0, shanghai),
		},
		{
			name:          "an exact slot advances instead of rerunning",
			now:           time.Date(2026, 7, 15, 6, 0, 0, 0, shanghai),
			intervalHours: 6,
			location:      shanghai,
			expected:      time.Date(2026, 7, 15, 12, 0, 0, 0, shanghai),
		},
		{
			name:          "non divisor intervals reset at next local midnight",
			now:           time.Date(2026, 7, 15, 20, 30, 0, 0, shanghai),
			intervalHours: 5,
			location:      shanghai,
			expected:      time.Date(2026, 7, 16, 0, 0, 0, 0, shanghai),
		},
		{
			name:          "twenty four hour interval advances to next local midnight",
			now:           time.Date(2026, 7, 15, 23, 30, 0, 0, shanghai),
			intervalHours: 24,
			location:      shanghai,
			expected:      time.Date(2026, 7, 16, 0, 0, 0, 0, shanghai),
		},
		{
			name:          "an explicit western timezone does not use the host timezone",
			now:           time.Date(2026, 1, 15, 1, 30, 0, 0, western),
			intervalHours: 6,
			location:      western,
			expected:      time.Date(2026, 1, 15, 6, 0, 0, 0, western),
		},
		// UTC construction disambiguates transition instants and keeps the test independent of host TZ.
		{
			name:          "spring forward skips the nonexistent local hour",
			now:           time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC).In(newYork),
			intervalHours: 1,
			location:      newYork,
			expected:      time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC).In(newYork),
		},
		{
			name:          "fall back first repeated 01:30 advances to 02:00",
			now:           time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).In(newYork),
			intervalHours: 2,
			location:      newYork,
			expected:      time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC).In(newYork),
		},
		{
			name:          "fall back second repeated 01:30 advances to 02:00",
			now:           time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).In(newYork),
			intervalHours: 2,
			location:      newYork,
			expected:      time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC).In(newYork),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := nextBalanceRefreshTimeAt(tt.now, tt.intervalHours, tt.location)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestBalanceServiceStartReconcilesConfigAfterSubscriptionSetup(t *testing.T) {
	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	require.NoError(t, db.Create(&ManagedSiteSetting{
		ID:                          1,
		AutoBalanceEnabled:          true,
		BalanceRefreshIntervalHours: 24,
	}).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	// A single connection keeps SQLite :memory: state shared while the query/update hand-off is coordinated.
	sqlDB.SetMaxOpenConns(1)

	configQueries := make(chan struct{}, 2)
	const hookName = "test:balance_startup_config_query"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table != "managed_site_settings" {
			return
		}
		select {
		case configQueries <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(hookName) })

	memoryStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = memoryStore.Close() })
	hookedStore := &subscribeHookStore{
		Store: memoryStore,
		beforeSubscribe: func() {
			select {
			case <-configQueries:
			case <-time.After(time.Second):
				t.Fatal("scheduler did not perform its initial configuration read")
			}
			// This update lands after the initial read but before the subscription becomes active.
			require.NoError(t, db.Model(&ManagedSiteSetting{}).
				Where("id = ?", 1).
				Updates(map[string]any{
					"auto_balance_enabled":           false,
					"balance_refresh_interval_hours": 6,
				}).Error)
		},
	}

	service := NewBalanceService(db, encSvc)
	service.SetStore(hookedStore)
	service.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		service.Stop(ctx)
	})

	select {
	case <-configQueries:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not reconcile configuration after subscription setup")
	}
}

func TestNormalizeAutoBalanceIntervalHours(t *testing.T) {
	t.Parallel()

	assert.Equal(t, defaultAutoBalanceIntervalHours, normalizeAutoBalanceIntervalHours(0))
	assert.Equal(t, defaultAutoBalanceIntervalHours, normalizeAutoBalanceIntervalHours(25))
	assert.Equal(t, 6, normalizeAutoBalanceIntervalHours(6))
}

func TestBalanceServiceStopCancelsScheduledRefresh(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	var startedOnce sync.Once
	var canceledOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(requestStarted) })
		<-r.Context().Done()
		canceledOnce.Do(func() { close(requestCanceled) })
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	authValue, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	require.NoError(t, db.Create(&ManagedSite{
		Name:      "blocking balance site",
		BaseURL:   server.URL,
		SiteType:  SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}).Error)

	service := NewBalanceService(db, encSvc)
	service.wg.Add(1)
	go func() {
		defer service.wg.Done()
		service.refreshAllBalancesBackground(service.lifecycleCtx)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled refresh did not start")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	service.Stop(stopCtx)

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("scheduled refresh request was not canceled during stop")
	}
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
