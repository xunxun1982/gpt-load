package sitemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

type countingSubscription struct {
	channel    chan *store.Message
	closeCount atomic.Int32
}

func (s *countingSubscription) Channel() <-chan *store.Message {
	return s.channel
}

func (s *countingSubscription) Close() error {
	// Record every call so the test verifies that the owning service provides close idempotence.
	s.closeCount.Add(1)
	return nil
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
		Name:              "Test Site",
		BaseURL:           server.URL,
		SiteType:          SiteTypeNewAPI,
		AuthType:          AuthTypeAccessToken,
		AuthValue:         authValue,
		BalanceMultiplier: 4,
	}
	err = db.Create(site).Error
	require.NoError(t, err)

	// Fetch balance
	result := service.FetchSiteBalance(context.Background(), site)
	require.NotNil(t, result)
	require.NotNil(t, result.Balance)
	assert.Equal(t, "$0.25", *result.Balance)

	var updated ManagedSite
	require.NoError(t, db.First(&updated, site.ID).Error)
	assert.Equal(t, "$1.00", updated.LastBalance)
	assert.Equal(t, 1, invalidations)
}

func TestBalanceServiceRejectsResponseAfterAuthConfigurationChanges(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer old-token", r.Header.Get("Authorization"))
		close(requestStarted)
		<-releaseResponse
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	oldAuth, err := encSvc.Encrypt("old-token")
	require.NoError(t, err)
	newAuth, err := encSvc.Encrypt("new-token")
	require.NoError(t, err)
	site := &ManagedSite{
		Name:      "auth changes during balance request",
		BaseURL:   server.URL,
		SiteType:  SiteTypeNewAPI,
		AuthType:  AuthTypeAccessToken,
		AuthValue: oldAuth,
	}
	require.NoError(t, db.Create(site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	resultCh := make(chan *BalanceInfo, 1)
	go func() {
		resultCh <- service.FetchSiteBalance(t.Context(), site)
	}()

	<-requestStarted
	require.NoError(t, db.Model(&ManagedSite{}).Where("id = ?", site.ID).Update("auth_value", newAuth).Error)
	close(releaseResponse)
	result := <-resultCh

	assert.Nil(t, result.Balance)
	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, newAuth, stored.AuthValue)
	assert.Empty(t, stored.LastBalance)
}

func TestBalanceServiceFetchesNewAPICompatibleSiteTypes(t *testing.T) {
	t.Parallel()

	for _, siteType := range []string{
		SiteTypeNewAPI,
		SiteTypeOneHub,
		SiteTypeDoneHub,
		SiteTypeWongGongyi,
	} {
		t.Run(siteType, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/user/self", r.URL.Path)
				assert.Equal(t, "Bearer login-token", r.Header.Get("Authorization"))
				assert.Equal(t, "42", r.Header.Get("New-API-User"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
			}))
			t.Cleanup(server.Close)

			db := setupTestDB(t)
			encSvc := setupTestEncryption(t)
			require.NoError(t, db.AutoMigrate(&ManagedSite{}))
			authValue, err := encSvc.Encrypt("login-token")
			require.NoError(t, err)
			userID, err := encSvc.Encrypt("42")
			require.NoError(t, err)
			site := &ManagedSite{
				Name:      siteType,
				BaseURL:   server.URL,
				SiteType:  siteType,
				AuthType:  AuthTypeAccessToken,
				AuthValue: authValue,
				UserID:    userID,
			}
			require.NoError(t, db.Create(site).Error)

			result := NewBalanceService(db, encSvc).FetchSiteBalance(t.Context(), site)

			require.NotNil(t, result.Balance)
			assert.Equal(t, "$1.00", *result.Balance)
		})
	}
}

func TestBalanceServiceRefreshAllBalancesReturnsScaledValuesAfterRawPersistence(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(userSelfResponse{
			Success: true,
			Data: struct {
				Quota int64 `json:"quota"`
			}{Quota: 5_000_000}, // $10.00 upstream balance
		})
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	authValue, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:              "Batch scaled balance",
		BaseURL:           server.URL,
		SiteType:          SiteTypeNewAPI,
		Enabled:           true,
		AuthType:          AuthTypeAccessToken,
		AuthValue:         authValue,
		BalanceMultiplier: 4,
	}
	require.NoError(t, db.Create(&site).Error)

	results, err := NewBalanceService(db, encSvc).RefreshAllBalances(context.Background())
	require.NoError(t, err)
	require.NotNil(t, results[site.ID].Balance)
	assert.Equal(t, "$2.50", *results[site.ID].Balance)

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, "$10.00", stored.LastBalance)
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
			name:               "string balance",
			authToken:          "test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"code":0,"message":"success","data":{"balance":"12.5"}}`,
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
			name:               "missing success envelope",
			authToken:          "test-token",
			expectedAuthHeader: "Bearer test-token",
			response:           `{"data":{"balance":12.5}}`,
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
				assert.Equal(t, "/api/v1/auth/me", r.URL.Path)
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

func TestBalanceServiceSkipsStaleSub2APIAdapterAfterSiteTypeChange(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"balance":99}}`))
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	encryptedAuth, err := encSvc.Encrypt("new-api-token")
	require.NoError(t, err)
	stored := ManagedSite{
		Name:      "site type changed",
		BaseURL:   server.URL,
		SiteType:  SiteTypeNewAPI,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(&stored).Error)

	staleSnapshot := stored
	staleSnapshot.SiteType = SiteTypeSub2API
	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	result := service.FetchSiteBalance(t.Context(), &staleSnapshot)

	assert.Nil(t, result.Balance)
	assert.Zero(t, requests.Load())
}

func TestBalanceServiceRefreshAllBalancesRenewsExpiredSub2APIToken(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	var refreshRequests atomic.Int32
	var balanceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer "+expiredToken, r.Header.Get("Authorization"))
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "old-refresh-token", body[authFieldRefreshToken])
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			balanceRequests.Add(1)
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":12.5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	authJSON := fmt.Sprintf(`{"access_token":%q,"refresh_token":"old-refresh-token"}`, expiredToken)
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "automatic Sub2API balance",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		Enabled:   true,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	results, err := service.RefreshAllBalances(t.Context())

	require.NoError(t, err)
	require.NotNil(t, results[site.ID].Balance)
	assert.Equal(t, "$12.50", *results[site.ID].Balance)
	assert.Equal(t, int32(1), refreshRequests.Load())
	assert.Equal(t, int32(1), balanceRequests.Load())

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	decrypted, err := encSvc.Decrypt(stored.AuthValue)
	require.NoError(t, err)
	storedAuth := parseAuthConfig(stored.AuthType, decrypted)
	assert.Equal(t, "fresh-token", storedAuth.GetAuthValue(AuthTypeAccessToken))
	assert.Equal(t, "fresh-refresh-token", storedAuth.GetSupplementalValue(authFieldRefreshToken))
	expiresAt, err := time.Parse(time.RFC3339, storedAuth.GetSupplementalValue("token_expires_at"))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Hour), expiresAt, 5*time.Second)
}

func TestBalanceServiceRefreshesSub2APIWithRefreshTokenOnly(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	var refreshRequests atomic.Int32
	var balanceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			assert.Empty(t, r.Header.Get("Authorization"))
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "refresh-only-token", body[authFieldRefreshToken])
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			balanceRequests.Add(1)
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":6.25}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	encryptedAuth, err := encSvc.Encrypt(`{"refresh_token":"refresh-only-token"}`)
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Sub2API refresh-only balance",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	result := service.FetchSiteBalance(t.Context(), &site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$6.25", *result.Balance)
	assert.Equal(t, int32(1), refreshRequests.Load())
	assert.Equal(t, int32(1), balanceRequests.Load())
}

func TestBalanceServiceRefreshesSub2APITokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	var refreshRequests atomic.Int32
	var balanceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			balanceRequests.Add(1)
			if r.Header.Get("Authorization") == "Bearer stale-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired"}`))
				return
			}
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":8.75}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	encryptedAuth, err := encSvc.Encrypt(`{"access_token":"stale-token","refresh_token":"refresh-token"}`)
	require.NoError(t, err)
	site := &ManagedSite{
		Name:      "reactive Sub2API balance refresh",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	result := service.FetchSiteBalance(t.Context(), site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$8.75", *result.Balance)
	assert.Equal(t, int32(1), refreshRequests.Load())
	assert.Equal(t, int32(2), balanceRequests.Load())
}

func TestBalanceServiceRetriesAfterProactiveSub2APIRefreshFailure(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	var refreshRequests atomic.Int32
	var balanceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			requestNumber := refreshRequests.Add(1)
			if requestNumber == 1 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"code":502,"message":"temporary refresh failure"}`))
				return
			}
			assert.Equal(t, "Bearer "+expiredToken, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			requestNumber := balanceRequests.Add(1)
			if requestNumber == 1 {
				assert.Equal(t, "Bearer "+expiredToken, r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired"}`))
				return
			}
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":9.5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	authJSON := fmt.Sprintf(`{"access_token":%q,"refresh_token":"old-refresh-token"}`, expiredToken)
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	site := &ManagedSite{
		Name:      "Sub2API proactive refresh retry",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	result := service.FetchSiteBalance(t.Context(), site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$9.50", *result.Balance)
	assert.Equal(t, int32(2), refreshRequests.Load())
	assert.Equal(t, int32(2), balanceRequests.Load())
	assertPersistedAuthTokens(t, db, encSvc, site.ID, "fresh-token", "fresh-refresh-token")
}

func TestBalanceServiceRefreshesSub2APITokenAfterCookieFallback(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	var refreshRequests atomic.Int32
	var balanceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			assert.Equal(t, "session=browser", r.Header.Get("Cookie"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			balanceRequests.Add(1)
			authorization := r.Header.Get("Authorization")
			cookie := r.Header.Get("Cookie")
			switch {
			case authorization == "Bearer fresh-token":
				assert.Equal(t, "session=browser", cookie)
				_, _ = w.Write([]byte(`{"code":0,"data":{"balance":8.75}}`))
			case authorization == "" && cookie == "session=browser":
				_, _ = w.Write([]byte(`{"code":0,"data":{"balance":3.5}}`))
			default:
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	encryptedAuth, err := encSvc.Encrypt(`{"access_token":"stale-token","refresh_token":"refresh-token","cookie":"session=browser"}`)
	require.NoError(t, err)
	site := &ManagedSite{
		Name:      "Sub2API cookie fallback refresh",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken + "," + AuthTypeCookie,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	result := service.FetchSiteBalance(t.Context(), site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$8.75", *result.Balance)
	assert.Equal(t, int32(1), refreshRequests.Load())
	assert.Equal(t, int32(4), balanceRequests.Load())
	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	decrypted, err := encSvc.Decrypt(stored.AuthValue)
	require.NoError(t, err)
	storedAuth := parseAuthConfig(stored.AuthType, decrypted)
	assert.Equal(t, "fresh-token", storedAuth.GetAuthValue(AuthTypeAccessToken))
	assert.Equal(t, "fresh-refresh-token", storedAuth.GetSupplementalValue(authFieldRefreshToken))
}

func TestBalanceServiceRetriesFreshSub2APIAccessTokenWithoutStaleCookie(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	var refreshRequests atomic.Int32
	var freshBareRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			authorization := r.Header.Get("Authorization")
			cookie := r.Header.Get("Cookie")
			if authorization == "Bearer fresh-token" && cookie == "" {
				freshBareRequests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"data":{"balance":12.5}}`))
				return
			}
			if authorization == "Bearer fresh-token" && cookie != "" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<!doctype html><html><title>Just a moment...</title><p>Cloudflare challenge</p></html>`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	encryptedAuth, err := encSvc.Encrypt(`{"access_token":"stale-token","refresh_token":"refresh-token","cookie":"stale-session"}`)
	require.NoError(t, err)
	site := &ManagedSite{
		Name:      "Sub2API stale cookie",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken + "," + AuthTypeCookie,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	result := service.FetchSiteBalance(t.Context(), site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$12.50", *result.Balance)
	assert.Equal(t, int32(1), refreshRequests.Load())
	assert.Equal(t, int32(1), freshBareRequests.Load())
}

func TestBalanceServiceSerializesSub2APITokenRefreshPerSite(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// :memory: SQLite databases are connection-local; keep one connection so this test exercises auth concurrency, not isolated schemas.
	sqlDB.SetMaxOpenConns(1)

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	var refreshRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"rotated-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":3.5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	authJSON := fmt.Sprintf(`{"access_token":%q,"refresh_token":"single-use-refresh-token"}`, expiredToken)
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	site := &ManagedSite{
		Name:      "concurrent Sub2API balance",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	results := make(chan *BalanceInfo, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- service.FetchSiteBalance(t.Context(), site)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for result := range results {
		require.NotNil(t, result.Balance)
		assert.Equal(t, "$3.50", *result.Balance)
	}
	assert.Equal(t, int32(1), refreshRequests.Load())
}

func TestBalanceServiceUsesLatestNetworkSettingsAfterAdoptingRotatedSub2APICredentials(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	rotatedAuth, err := encSvc.Encrypt(`{"access_token":"fresh-token","refresh_token":"fresh-refresh-token"}`)
	require.NoError(t, err)
	latestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/me", r.URL.Path)
		assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Sec-Ch-Ua") == "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":403,"message":"browser headers required"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"balance":4.25}}`))
	}))
	t.Cleanup(latestServer.Close)

	var site ManagedSite
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		updateErr := db.Model(&ManagedSite{}).Where("id = ?", site.ID).Updates(map[string]any{
			"auth_value":    rotatedAuth,
			"base_url":      latestServer.URL,
			"bypass_method": BypassMethodStealth,
		}).Error
		if updateErr != nil {
			http.Error(w, "failed to rotate site settings", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"refresh token already rotated"}`))
	}))
	t.Cleanup(refreshServer.Close)

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	oldAuth, err := encSvc.Encrypt(fmt.Sprintf(`{"access_token":%q,"refresh_token":"old-refresh-token"}`, expiredToken))
	require.NoError(t, err)
	site = ManagedSite{
		Name:      "Sub2API latest network settings",
		BaseURL:   refreshServer.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: oldAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	result := service.FetchSiteBalance(t.Context(), &site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$4.25", *result.Balance)
}

func TestBalanceServiceFallsBackToLegacySub2APIProfileEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/me":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"route not found"}`))
		case "/api/v1/user/profile":
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":6.25}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	encryptedAuth, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	site := &ManagedSite{Name: "legacy Sub2API", BaseURL: server.URL, SiteType: SiteTypeSub2API, AuthType: AuthTypeAccessToken, AuthValue: encryptedAuth}
	require.NoError(t, db.Create(site).Error)

	result := NewBalanceService(db, encSvc).FetchSiteBalance(t.Context(), site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$6.25", *result.Balance)
}

func TestBalanceServiceDoesNotFallbackFromSub2APIBrowserChallenge(t *testing.T) {
	t.Parallel()

	var profileRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<!doctype html><html><script>var arg1='challenge';</script></html>`))
		case "/api/v1/user/profile":
			profileRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":99}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	encryptedAuth, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	site := &ManagedSite{
		Name:            "challenged Sub2API",
		BaseURL:         server.URL,
		SiteType:        SiteTypeSub2API,
		AuthType:        AuthTypeAccessToken,
		AuthValue:       encryptedAuth,
		LastBalance:     "$7.00",
		LastBalanceDate: "2026-07-01",
	}
	require.NoError(t, db.Create(site).Error)

	result := NewBalanceService(db, encSvc).FetchSiteBalance(t.Context(), site)

	assert.Nil(t, result.Balance)
	assert.Equal(t, int32(0), profileRequests.Load())
	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, "$7.00", stored.LastBalance)
	assert.Equal(t, "2026-07-01", stored.LastBalanceDate)
}

func TestBalanceServiceFetchesAnyRouterBalanceWithCookieAndUserID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/user/self", r.URL.Path)
		assert.Equal(t, "session=browser-ok", r.Header.Get("Cookie"))
		assert.Equal(t, "123", r.Header.Get("New-API-User"))
		assert.Equal(t, "123", r.Header.Get("User-id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1500000}}`))
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	encryptedAuth, err := encSvc.Encrypt("session=browser-ok")
	require.NoError(t, err)
	encryptedUserID, err := encSvc.Encrypt("123")
	require.NoError(t, err)
	site := &ManagedSite{
		Name:      "AnyRouter",
		BaseURL:   server.URL,
		SiteType:  SiteTypeAnyrouter,
		AuthType:  AuthTypeCookie,
		AuthValue: encryptedAuth,
		UserID:    encryptedUserID,
	}
	require.NoError(t, db.Create(site).Error)

	result := NewBalanceService(db, encSvc).FetchSiteBalance(t.Context(), site)

	require.NotNil(t, result.Balance)
	assert.Equal(t, "$3.00", *result.Balance)
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

func TestBalanceServiceRefreshAllBalancesReturnsFetchCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	authValue, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "cancel during balance fetch",
		BaseURL:   server.URL,
		SiteType:  SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}
	require.NoError(t, db.Create(&site).Error)
	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)

	results, err := service.RefreshAllBalances(ctx)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, results, site.ID)
}

func TestBalanceServicePersistsCompletedBalancesWhenFetchContextExpires(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	authValue, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "persist completed balance after timeout",
		BaseURL:   "https://balance-timeout.example.com",
		SiteType:  SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}
	require.NoError(t, db.Create(&site).Error)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	service.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":{"quota":500000}}`)),
			Request:    req,
		}, nil
	})}

	results, err := service.RefreshAllBalances(ctx)

	assert.ErrorIs(t, err, context.Canceled)
	assert.NotNil(t, results[site.ID].Balance)
	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, "$1.00", stored.LastBalance)
}

func TestBalanceServiceRefreshAllBalancesReturnsUpdateCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	authValue, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "cancel during balance update",
		BaseURL:   server.URL,
		SiteType:  SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}
	require.NoError(t, db.Create(&site).Error)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	const hookName = "test:cancel_after_balance_update"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table == "managed_sites" {
			cancel()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	results, err := service.RefreshAllBalances(ctx)

	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, results[site.ID])
	require.NotNil(t, results[site.ID].Balance)
}

func TestBalanceServiceRefreshAllBalancesReturnsPersistenceErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	authValue, err := encSvc.Encrypt("test-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "balance persistence failure",
		BaseURL:   server.URL,
		SiteType:  SiteTypeNewAPI,
		Enabled:   true,
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}
	require.NoError(t, db.Create(&site).Error)

	forcedErr := errors.New("forced balance persistence failure")
	const hookName = "test:balance_persistence_failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table == "managed_sites" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

	service := NewBalanceService(db, encSvc)
	t.Cleanup(service.closeIdleConnections)
	results, err := service.RefreshAllBalances(context.Background())

	assert.ErrorIs(t, err, forcedErr)
	require.NotNil(t, results[site.ID])
	require.NotNil(t, results[site.ID].Balance)
}

func TestBalanceServiceUpdateBalancesInDBStopsAfterContextCancellation(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	sites := make([]ManagedSite, 3)
	for i := range sites {
		sites[i] = ManagedSite{
			Name:     fmt.Sprintf("cancel update site %d", i),
			BaseURL:  fmt.Sprintf("https://cancel-%d.example.com", i),
			SiteType: SiteTypeNewAPI,
		}
		require.NoError(t, db.Create(&sites[i]).Error)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	updateCalls := 0
	const hookName = "test:cancel_during_balance_updates"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table == "managed_sites" {
			updateCalls++
			if updateCalls == 1 {
				cancel()
			}
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

	balance := "$5.00"
	results := make(map[uint]*BalanceInfo, len(sites))
	for i := range sites {
		results[sites[i].ID] = &BalanceInfo{SiteID: sites[i].ID, Balance: &balance}
	}
	service := NewBalanceService(db, setupTestEncryption(t))
	invalidations := 0
	service.SetCacheInvalidationCallback(func() { invalidations++ })

	err := service.updateBalancesInDB(ctx, results)

	require.NoError(t, err)
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Equal(t, 1, updateCalls)
	assert.Equal(t, 1, invalidations)
}

func TestBalanceServiceUpdateBalancesInDBPreservesPartialWrites(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	sites := []ManagedSite{
		{Name: "partial update site A", BaseURL: "https://partial-a.example.com", SiteType: SiteTypeNewAPI},
		{Name: "partial update site B", BaseURL: "https://partial-b.example.com", SiteType: SiteTypeNewAPI},
	}
	for i := range sites {
		require.NoError(t, db.Create(&sites[i]).Error)
	}

	forcedErr := errors.New("forced single balance update failure")
	updateCalls := 0
	const hookName = "test:single_balance_update_failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table == "managed_sites" {
			updateCalls++
			if updateCalls == 1 {
				tx.AddError(forcedErr)
			}
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

	balance := "$5.00"
	results := map[uint]*BalanceInfo{
		sites[0].ID: {SiteID: sites[0].ID, Balance: &balance},
		sites[1].ID: {SiteID: sites[1].ID, Balance: &balance},
	}
	service := NewBalanceService(db, setupTestEncryption(t))
	invalidations := 0
	service.SetCacheInvalidationCallback(func() { invalidations++ })

	err := service.updateBalancesInDB(context.Background(), results)

	assert.ErrorIs(t, err, forcedErr)
	assert.Equal(t, 2, updateCalls)
	var updated int64
	require.NoError(t, db.Model(&ManagedSite{}).Where("last_balance = ?", balance).Count(&updated).Error)
	assert.Equal(t, int64(1), updated)
	assert.Equal(t, 1, invalidations)
}

func TestBalanceServiceUpdateBalancesInDBSummarizesMultipleFailures(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))
	sites := make([]ManagedSite, 3)
	for i := range sites {
		sites[i] = ManagedSite{
			Name:     fmt.Sprintf("failed update site %d", i),
			BaseURL:  fmt.Sprintf("https://failed-update-%d.example.com", i),
			SiteType: SiteTypeNewAPI,
		}
		require.NoError(t, db.Create(&sites[i]).Error)
	}

	forcedErr := errors.New("forced repeated balance update failure")
	const hookName = "test:repeated_balance_update_failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hookName, func(tx *gorm.DB) {
		if tx.Statement.Table == "managed_sites" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hookName) })

	balance := "$5.00"
	results := make(map[uint]*BalanceInfo, len(sites))
	for i := range sites {
		results[sites[i].ID] = &BalanceInfo{SiteID: sites[i].ID, Balance: &balance}
	}
	service := NewBalanceService(db, setupTestEncryption(t))
	invalidations := 0
	service.SetCacheInvalidationCallback(func() { invalidations++ })

	err := service.updateBalancesInDB(context.Background(), results)

	assert.ErrorIs(t, err, forcedErr)
	assert.Contains(t, err.Error(), "2 additional balance cache updates failed")
	assert.Zero(t, invalidations)
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

func TestBalanceServiceStopClosesSubscriptionOnce(t *testing.T) {
	t.Parallel()

	subscription := &countingSubscription{channel: make(chan *store.Message)}
	service := NewBalanceService(setupTestDB(t), setupTestEncryption(t))
	service.subConfig = subscription

	service.Stop(context.Background())
	service.Stop(context.Background())

	assert.Equal(t, int32(1), subscription.closeCount.Load())
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
		{"Veloera", false},
		{SiteTypeOneHub, true},
		{SiteTypeDoneHub, true},
		{SiteTypeWongGongyi, true},
		{SiteTypeAnyrouter, true},
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
