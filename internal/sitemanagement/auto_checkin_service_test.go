package sitemanagement

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/i18n"
	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func init() {
	if err := i18n.Init(); err != nil {
		panic("failed to initialize i18n for tests: " + err.Error())
	}
}

func assertPersistedAuthTokens(t *testing.T, db *gorm.DB, encSvc encryption.Service, siteID uint, accessToken, refreshToken string) {
	t.Helper()

	var updated ManagedSite
	require.NoError(t, db.First(&updated, siteID).Error)
	decrypted, err := encSvc.Decrypt(updated.AuthValue)
	require.NoError(t, err)
	var persisted map[string]string
	require.NoError(t, json.Unmarshal([]byte(decrypted), &persisted))
	assert.Equal(t, accessToken, persisted["access_token"])
	assert.Equal(t, refreshToken, persisted["refresh_token"])
}

func TestTryMultiAuthPreservesAuthUpdatesAcrossFallback(t *testing.T) {
	t.Parallel()

	config := AuthConfig{
		AuthTypes: []string{AuthTypeAccessToken, AuthTypeCookie},
		AuthValues: map[string]string{
			AuthTypeAccessToken: "old-access-token",
			AuthTypeCookie:      "session=browser",
		},
	}

	result, err := tryMultiAuth(config, []string{AuthTypeAccessToken, AuthTypeCookie}, func(authType, _ string) (providerResult, error) {
		if authType == AuthTypeAccessToken {
			return providerResult{
				Status: CheckinResultFailed,
				AuthUpdates: map[string]string{
					AuthTypeAccessToken:   "fresh-access-token",
					authFieldRefreshToken: "fresh-refresh-token",
				},
			}, nil
		}
		return providerResult{Status: CheckinResultSuccess, Message: "cookie fallback succeeded"}, nil
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, map[string]string{
		AuthTypeAccessToken:   "fresh-access-token",
		authFieldRefreshToken: "fresh-refresh-token",
	}, result.AuthUpdates)
}

// TestTaskTypeConstantsSync verifies that local task type constants match services package
// Uses string literals to avoid import cycle
func TestTaskTypeConstantsSync(t *testing.T) {
	assert.Equal(t, "KEY_IMPORT", taskTypeKeyImport, "taskTypeKeyImport must match services.TaskTypeKeyImport")
	assert.Equal(t, "KEY_DELETE", taskTypeKeyDelete, "taskTypeKeyDelete must match services.TaskTypeKeyDelete")
	assert.Equal(t, "KEY_RESTORE", taskTypeKeyRestore, "taskTypeKeyRestore must match services.TaskTypeKeyRestore")
}

func TestNewAPIProviderRetriesWithPoWChallenge(t *testing.T) {
	t.Parallel()

	const challenge = "abcdef0123456789abcdef0123456789"
	const difficulty = 8

	checkinRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/pow/challenge":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "checkin", r.URL.Query().Get("action"))
			assert.Equal(t, "123", r.Header.Get("New-API-User"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"challenge":"abcdef0123456789abcdef0123456789","difficulty":8}}`))
		case "/api/user/checkin":
			assert.Equal(t, http.MethodPost, r.Method)
			checkinRequests++
			powChallenge := r.URL.Query().Get("pow_challenge")
			powNonce := r.URL.Query().Get("pow_nonce")
			if powChallenge == "" || powNonce == "" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":false,"message":"PoW challenge and nonce are required"}`))
				return
			}
			assert.Equal(t, challenge, powChallenge)
			assert.Len(t, powNonce, 8)
			assert.True(t, testPoWMeetsDifficulty(challenge, powNonce, difficulty))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL + "/console",
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, "签到成功", result.Message)
	assert.Equal(t, 2, checkinRequests)
}

func TestNewAPIProviderUsesPoWChallengeIDAndHashPrefix(t *testing.T) {
	t.Parallel()

	const challengeID = "server-challenge-id"
	const prefix = "hash-prefix"
	const difficulty = 8

	checkinRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/pow/challenge":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"server-challenge-id","challenge":"hash-prefix","difficulty":8}}`))
		case "/api/user/checkin":
			checkinRequests++
			powChallenge := r.URL.Query().Get("pow_challenge")
			powNonce := r.URL.Query().Get("pow_nonce")
			w.Header().Set("Content-Type", "application/json")
			if powChallenge == "" || powNonce == "" {
				_, _ = w.Write([]byte(`{"success":false,"message":"PoW challenge and nonce are required"}`))
				return
			}
			if powChallenge != challengeID {
				_, _ = w.Write([]byte(`{"success":false,"message":"PoW verification failed: challenge not found or expired"}`))
				return
			}
			if !testPoWMeetsDifficulty(prefix, powNonce, difficulty) {
				_, _ = w.Write([]byte(`{"success":false,"message":"PoW verification failed: nonce invalid"}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, "签到成功", result.Message)
	assert.Equal(t, 2, checkinRequests)
}

func TestParseNewAPIPoWChallengeSeparatesChallengeAndPrefix(t *testing.T) {
	t.Parallel()

	challenge, err := parseNewAPIPoWChallenge([]byte(`{"success":true,"data":{"challenge":"server-challenge-id","prefix":"hash-prefix","difficulty":8}}`))

	require.NoError(t, err)
	assert.Equal(t, "server-challenge-id", challenge.ChallengeID)
	assert.Equal(t, "hash-prefix", challenge.HashPrefix)
	assert.Equal(t, 8, challenge.Difficulty)
}

func TestNewAPIProviderKeepsPlainCheckinPathWhenPoWNotRequired(t *testing.T) {
	t.Parallel()

	checkinRequests := 0
	challengeRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/pow/challenge":
			challengeRequests++
			http.NotFound(w, r)
		case "/api/user/checkin":
			checkinRequests++
			assert.Empty(t, r.URL.Query().Get("pow_challenge"))
			assert.Empty(t, r.URL.Query().Get("pow_nonce"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, 1, checkinRequests)
	assert.Zero(t, challengeRequests)
}

func TestNewAPIProviderFallbacksToSignInWhenDefaultCheckinNotFound(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
		assert.Equal(t, "123", r.Header.Get("New-API-User"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/checkin":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"message":"route not found"}`))
		case "/api/user/sign_in":
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, "签到成功", result.Message)
	assert.Equal(t, []string{"/api/user/checkin", "/api/user/sign_in"}, paths)
}

func TestNewAPIProviderDoesNotFallbackToSignInForCustomCheckinURL(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"message":"route not found"}`))
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:          server.URL,
		SiteType:         SiteTypeNewAPI,
		UserID:           "123",
		CustomCheckInURL: "/custom/checkin",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Contains(t, result.Message, "404")
	assert.Equal(t, []string{"/custom/checkin"}, paths)
}

func TestNewAPIProviderDoesNotFallbackToSignInForBusinessFailure(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"message":"Turnstile token 为空"}`))
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Contains(t, result.Message, "Turnstile")
	assert.Equal(t, []string{"/api/user/checkin"}, paths)
}

func TestNewAPIProviderDoesNotForgeTurnstileToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/user/checkin", r.URL.Path)
		assert.Empty(t, r.URL.Query().Get("turnstile"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"message":"Turnstile token 为空"}`))
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Contains(t, result.Message, "浏览器")
	assert.Contains(t, result.Message, "Turnstile")
}

func TestNewAPIProviderAccessTokenCarriesCookieSessionWhenBothConfigured(t *testing.T) {
	t.Parallel()

	checkinRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/user/checkin", r.URL.Path)
		checkinRequests++
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			_, _ = w.Write([]byte(`{"success":false,"message":"missing access token"}`))
			return
		}
		if r.Header.Get("New-API-User") != "123" {
			_, _ = w.Write([]byte(`{"success":false,"message":"missing user id"}`))
			return
		}
		if !strings.Contains(r.Header.Get("Cookie"), "session=turnstile-ok") {
			_, _ = w.Write([]byte(`{"success":false,"message":"Turnstile token 为空"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes: []string{AuthTypeAccessToken, AuthTypeCookie},
		AuthValues: map[string]string{
			AuthTypeAccessToken: "test-access-token",
			AuthTypeCookie:      "session=turnstile-ok; other=value",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, "签到成功", result.Message)
	assert.Equal(t, 1, checkinRequests)
}

func TestNewAPIProviderExplainsPrivateCheckinSignatureHeader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/user/checkin", r.URL.Path)
		assert.Equal(t, "123", r.Header.Get("New-API-User"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"message":"缺少签到签名请求头"}`))
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Contains(t, result.Message, "私有签到签名")
	assert.Contains(t, result.Message, "Cookie")
}

func TestNewAPIProviderDetectsHTMLChallengePage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/user/checkin", r.URL.Path)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><script>var arg1='x';document.cookie='acw_sc__v2=y';</script></body></html>`))
	}))
	defer server.Close()

	provider := newAPIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeNewAPI,
		UserID:   "123",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, msgBrowserChallengeDetected, result.Message)
}

func TestIsBrowserChallengeResponseIgnoresJSONForbiddenChallengeMessage(t *testing.T) {
	t.Parallel()

	data := []byte(`{"success":false,"message":"challenge token is invalid"}`)

	assert.False(t, isBrowserChallengeResponse(http.StatusForbidden, data))
}

func TestResolveProviderMapsNewAPICompatibleSites(t *testing.T) {
	t.Parallel()

	for _, siteType := range []string{SiteTypeNewAPI, SiteTypeOneHub, SiteTypeDoneHub} {
		t.Run(siteType, func(t *testing.T) {
			t.Parallel()
			assert.IsType(t, newAPIProvider{}, resolveProvider(siteType))
		})
	}
}

func TestWongProviderUsesMatchingStealthHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Header.Get("User-Agent"), "Chrome/146.0.0.0")
		assert.Contains(t, r.Header.Get("Sec-Ch-Ua"), `v="146"`)
		assert.Equal(t, "session=browser-ok; cf_clearance=test", r.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
	}))
	t.Cleanup(server.Close)

	result, err := (wongProvider{}).CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:      server.URL,
		SiteType:     SiteTypeWongGongyi,
		BypassMethod: "stealth",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeCookie},
		AuthValues: map[string]string{AuthTypeCookie: "session=browser-ok; cf_clearance=test"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
}

func TestAnyRouterProviderUsesCookieAjaxSignInEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		baseURL     func(string) string
		wantReferer func(string) string
	}{
		{
			name:        "origin only",
			baseURL:     func(serverURL string) string { return serverURL },
			wantReferer: func(serverURL string) string { return serverURL + "/console/personal" },
		},
		{
			name:        "pathful base URL",
			baseURL:     func(serverURL string) string { return serverURL + "/check-in" },
			wantReferer: func(serverURL string) string { return serverURL + "/console/personal" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var paths []string
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "session=browser-ok", r.Header.Get("Cookie"))
				assert.Equal(t, "XMLHttpRequest", r.Header.Get("X-Requested-With"))
				assert.Equal(t, tt.wantReferer(server.URL), r.Header.Get("Referer"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
			}))
			defer server.Close()

			provider := anyrouterProvider{}
			result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
				BaseURL:  tt.baseURL(server.URL),
				SiteType: SiteTypeAnyrouter,
			}, AuthConfig{
				AuthTypes:  []string{AuthTypeCookie},
				AuthValues: map[string]string{AuthTypeCookie: "session=browser-ok"},
			})

			require.NoError(t, err)
			assert.Equal(t, CheckinResultSuccess, result.Status)
			assert.Equal(t, "签到成功", result.Message)
			assert.Equal(t, []string{"/api/user/sign_in"}, paths)
		})
	}
}

func TestAutoCheckinBatchLoadsAnyRouterStealthBypassMethod(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}, &ManagedSiteSetting{}))

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/user/sign_in" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Sec-Ch-Ua") == "" || !strings.Contains(r.Header.Get("User-Agent"), "Chrome/146.0.0.0") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<!doctype html><title>Just a moment...</title><p>Cloudflare challenge</p>"))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
	}))
	t.Cleanup(server.Close)

	encryptedCookie, err := encSvc.Encrypt("session=browser-ok; cf_clearance=test")
	require.NoError(t, err)
	site := ManagedSite{
		Name:           "AnyRouter scheduled stealth",
		BaseURL:        server.URL,
		SiteType:       SiteTypeAnyrouter,
		Enabled:        true,
		CheckInEnabled: true,
		BypassMethod:   BypassMethodStealth,
		AuthType:       AuthTypeCookie,
		AuthValue:      encryptedCookie,
	}
	require.NoError(t, db.Create(&site).Error)
	require.NoError(t, db.Create(&ManagedSiteSetting{
		ID:                     1,
		AutoCheckinEnabled:     true,
		ScheduleTimes:          "09:00",
		WindowStart:            "09:00",
		WindowEnd:              "18:00",
		ScheduleMode:           AutoCheckinScheduleModeMultiple,
		RetryMaxAttemptsPerDay: 2,
	}).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	service.runAllCheckins(t.Context())

	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, CheckinResultSuccess, stored.LastCheckInStatus)
	assert.Equal(t, int32(1), requests.Load())
}

func TestAutoCheckinBatchSkipsSub2APIWithoutCustomEndpoint(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))
	authValue, err := encSvc.Encrypt("access-token")
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "standard Sub2API",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: authValue,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	t.Cleanup(service.closeIdleConnections)
	summary := service.runSitesCheckin(t.Context(), []ManagedSite{site})

	assert.Equal(t, 1, summary.SkippedCount)
	assert.Zero(t, summary.FailedCount)
	assert.False(t, summary.NeedsRetry)
	assert.Zero(t, requests.Load())
}

func TestAnyRouterProviderSendsUserIDHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-user-id", r.Header.Get("New-API-User"))
		assert.Equal(t, "test-user-id", r.Header.Get("User-id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
	}))
	defer server.Close()

	provider := anyrouterProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeAnyrouter,
		UserID:   "test-user-id",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeCookie},
		AuthValues: map[string]string{AuthTypeCookie: "session=browser-ok"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
}

func TestAnyRouterProviderReturnsProtocolFieldsWhenMessageEmpty(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"message":"","code":40003,"ret":-1}`))
	}))
	defer server.Close()

	provider := anyrouterProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeAnyrouter,
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeCookie},
		AuthValues: map[string]string{AuthTypeCookie: "session=browser-ok"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Contains(t, result.Message, "code=40003")
	assert.Contains(t, result.Message, "ret=-1")
}

func TestAnyRouterProviderDetectsHTMLChallengePage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><script>var arg1='x';document.cookie='acw_sc__v2=y';</script></body></html>`))
	}))
	defer server.Close()

	provider := anyrouterProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeAnyrouter,
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeCookie},
		AuthValues: map[string]string{AuthTypeCookie: "session=browser-ok; acw_tc=test"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, msgBrowserChallengeDetected, result.Message)
}

func TestSub2APIProviderFallbacksToNewCheckInEndpointWhenLegacyMissing(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
		assert.Equal(t, "session=browser-ok", r.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/check-in":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"message":"route not found"}`))
		case "/api/v1/check-in":
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL + "/check-in",
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes: []string{AuthTypeAccessToken, AuthTypeCookie},
		AuthValues: map[string]string{
			AuthTypeAccessToken: "test-access-token",
			AuthTypeCookie:      "session=browser-ok",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, "签到成功", result.Message)
	assert.Equal(t, []string{"/api/v1/user/check-in", "/api/v1/check-in"}, paths)
}

func TestSub2APIProviderReportsMissingCheckInEndpointWhenDefaultsMissing(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"message":"route not found"}`))
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, "check-in endpoint not configured", result.Message)
	assert.Equal(t, []string{"/api/v1/user/check-in", "/api/v1/check-in"}, paths)
}

func TestSub2APIProviderAcceptsBearerPrefixedAccessToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/user/check-in", r.URL.Path)
		assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "Bearer test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, "签到成功", result.Message)
}

func TestSub2APIProviderDoesNotFallbackOnBusinessFailure(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"message":"already used today"}`))
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultAlreadyChecked, result.Status)
	assert.Equal(t, "already used today", result.Message)
	assert.Equal(t, []string{"/api/v1/user/check-in"}, paths)
}

func TestSub2APIProviderKeepsCustomCheckInURLBeforeDefaultFallbacks(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/custom/check-in":
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"success":false,"message":"method not allowed"}`))
		case "/api/v1/user/check-in":
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:          server.URL,
		SiteType:         SiteTypeSub2API,
		CustomCheckInURL: "/custom/check-in",
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeCookie},
		AuthValues: map[string]string{AuthTypeCookie: "session=browser-ok"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, []string{"/custom/check-in", "/api/v1/user/check-in"}, paths)
}

func TestSub2APIProviderDetectsHTMLChallengePage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/user/check-in", r.URL.Path)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><script>var arg1='x';document.cookie='acw_tc=y';</script></body></html>`))
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "test-access-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, msgBrowserChallengeDetected, result.Message)
}

func TestSub2APIProviderKeepsExpiredTokenMessageOnUnauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/user/check-in", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"message":"Token has expired"}`))
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:  []string{AuthTypeAccessToken},
		AuthValues: map[string]string{AuthTypeAccessToken: "expired-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, "HTTP 401: Token has expired", result.Message)
}

func TestSub2APIProviderRestoresAccessTokenFromRefreshTokenOnly(t *testing.T) {
	t.Parallel()

	var refreshRequests atomic.Int32
	var checkinRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			assert.Empty(t, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/user/check-in":
			checkinRequests.Add(1)
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	result, err := (sub2APIProvider{}).CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:          []string{AuthTypeAccessToken},
		AuthValues:         map[string]string{AuthTypeAccessToken: ""},
		SupplementalValues: map[string]string{authFieldRefreshToken: "refresh-only-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, int32(1), refreshRequests.Load())
	assert.Equal(t, int32(1), checkinRequests.Load())
	assert.Equal(t, "fresh-token", result.AuthUpdates[AuthTypeAccessToken])
	assert.Equal(t, "fresh-refresh-token", result.AuthUpdates[authFieldRefreshToken])
}

func TestSub2APIProviderReportsRefreshOnlyProactiveFailure(t *testing.T) {
	t.Parallel()

	var refreshRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		refreshRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":502,"message":"temporary refresh failure"}`))
	}))
	t.Cleanup(server.Close)

	result, err := (sub2APIProvider{}).CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:          []string{AuthTypeAccessToken},
		AuthValues:         map[string]string{AuthTypeAccessToken: ""},
		SupplementalValues: map[string]string{authFieldRefreshToken: "refresh-only-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Contains(t, result.Message, "token refresh failed")
	assert.Equal(t, int32(1), refreshRequests.Load())
}

func TestSub2APIProviderReportsRefreshFailureOnUnauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/check-in":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"message":"Token has expired"}`))
		case "/api/v1/auth/refresh":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"message":"refresh token expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := sub2APIProvider{}
	result, err := provider.CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:          []string{AuthTypeAccessToken},
		AuthValues:         map[string]string{AuthTypeAccessToken: "expired-token"},
		SupplementalValues: map[string]string{authFieldRefreshToken: "expired-refresh-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, "HTTP 401: Token has expired; token refresh failed: upstream rejected token refresh", result.Message)
	assert.NotContains(t, result.Message, "refresh token expired")
	assert.NotContains(t, result.Message, "expired-refresh-token")
}

func TestSub2APIProviderRetriesAfterProactiveFailureOnlyOnce(t *testing.T) {
	t.Parallel()

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	var refreshRequests atomic.Int32
	var checkinRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"refresh rejected"}`))
		case "/api/v1/user/check-in":
			checkinRequests.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"message":"Token has expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	result, err := (sub2APIProvider{}).CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:          []string{AuthTypeAccessToken},
		AuthValues:         map[string]string{AuthTypeAccessToken: expiredToken},
		SupplementalValues: map[string]string{authFieldRefreshToken: "single-use-refresh-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	// A failed proactive attempt must not consume the single 401-driven retry;
	// the reactive attempt itself still happens at most once.
	assert.Equal(t, int32(2), refreshRequests.Load())
	assert.Equal(t, int32(1), checkinRequests.Load())
}

func TestSub2APIProviderRetriesAfterProactiveRefreshFailure(t *testing.T) {
	t.Parallel()

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	var refreshRequests atomic.Int32
	var checkinRequests atomic.Int32
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
		case "/api/v1/user/check-in":
			requestNumber := checkinRequests.Add(1)
			if requestNumber == 1 {
				assert.Equal(t, "Bearer "+expiredToken, r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false,"message":"Token has expired"}`))
				return
			}
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	result, err := (sub2APIProvider{}).CheckIn(t.Context(), server.Client(), ManagedSite{
		BaseURL:  server.URL,
		SiteType: SiteTypeSub2API,
	}, AuthConfig{
		AuthTypes:          []string{AuthTypeAccessToken},
		AuthValues:         map[string]string{AuthTypeAccessToken: expiredToken},
		SupplementalValues: map[string]string{authFieldRefreshToken: "refresh-token"},
	})

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, int32(2), refreshRequests.Load())
	assert.Equal(t, int32(2), checkinRequests.Load())
	assert.Equal(t, "fresh-token", result.AuthUpdates[AuthTypeAccessToken])
}

func TestAutoCheckinRefreshesSub2APITokenOnExpiredAccessToken(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	var checkinAuthorizations []string
	var balanceAuthorizations []string
	refreshRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/check-in":
			assert.Equal(t, http.MethodPost, r.Method)
			checkinAuthorizations = append(checkinAuthorizations, r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") == "Bearer expired-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false,"message":"Token has expired"}`))
				return
			}
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		case "/api/v1/auth/refresh":
			refreshRequests++
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer expired-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/auth/me":
			balanceAuthorizations = append(balanceAuthorizations, r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer fresh-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authJSON := `{"access_token":"expired-token","refresh_token":"refresh-token"}`
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	site := ManagedSite{
		Name:           "sub2api",
		BaseURL:        server.URL,
		SiteType:       SiteTypeSub2API,
		Enabled:        true,
		CheckInEnabled: true,
		AuthType:       AuthTypeAccessToken,
		AuthValue:      encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	service.SetBalanceService(NewBalanceService(db, encSvc))

	result, err := service.CheckInSite(t.Context(), site.ID)

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, []string{"Bearer expired-token", "Bearer fresh-token"}, checkinAuthorizations)
	assert.Equal(t, []string{"Bearer fresh-token"}, balanceAuthorizations)
	assert.Equal(t, 1, refreshRequests)
	assertPersistedAuthTokens(t, db, encSvc, site.ID, "fresh-token", "fresh-refresh-token")
	var stored ManagedSite
	require.NoError(t, db.First(&stored, site.ID).Error)
	assert.Equal(t, "$2.00", stored.LastBalance)
}

func TestAutoCheckinRefreshesSub2APITokenBeforeCheckinWhenJWTExpired(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	checkinRequests := 0
	refreshRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests++
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer "+expiredToken, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/user/check-in":
			checkinRequests++
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authJSON := `{"access_token":"` + expiredToken + `","refresh_token":"refresh-token"}`
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	site := ManagedSite{
		Name:           "sub2api",
		BaseURL:        server.URL,
		SiteType:       SiteTypeSub2API,
		Enabled:        true,
		CheckInEnabled: true,
		AuthType:       AuthTypeAccessToken,
		AuthValue:      encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	service.SetBalanceService(NewBalanceService(db, encSvc))

	result, err := service.CheckInSite(t.Context(), site.ID)

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, 1, refreshRequests)
	assert.Equal(t, 1, checkinRequests)
	assertPersistedAuthTokens(t, db, encSvc, site.ID, "fresh-token", "fresh-refresh-token")
}

func TestAutoCheckinPersistsProactiveSub2APITokenWhenEndpointMissing(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	refreshRequests := 0
	checkinRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests++
			assert.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		default:
			checkinRequests++
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authJSON := `{"access_token":"` + expiredToken + `","refresh_token":"refresh-token"}`
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	site := ManagedSite{
		Name:           "sub2api",
		BaseURL:        server.URL,
		SiteType:       SiteTypeSub2API,
		Enabled:        true,
		CheckInEnabled: true,
		AuthType:       AuthTypeAccessToken,
		AuthValue:      encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)

	result, err := service.CheckInSite(t.Context(), site.ID)

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, "check-in endpoint not configured", result.Message)
	assert.Equal(t, 1, refreshRequests)
	assert.Equal(t, 2, checkinRequests)
	assertPersistedAuthTokens(t, db, encSvc, site.ID, "fresh-token", "fresh-refresh-token")
}

func TestAutoCheckinPersistsProactiveSub2APITokenWhenCheckinRequestErrors(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))

	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	refreshRequests := 0
	checkinRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshRequests++
			assert.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/user/check-in":
			checkinRequests++
			hijacker, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hijacker.Hijack()
			require.NoError(t, err)
			_ = conn.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authJSON := `{"access_token":"` + expiredToken + `","refresh_token":"refresh-token"}`
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	site := ManagedSite{
		Name:           "sub2api",
		BaseURL:        server.URL,
		SiteType:       SiteTypeSub2API,
		Enabled:        true,
		CheckInEnabled: true,
		AuthType:       AuthTypeAccessToken,
		AuthValue:      encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)

	result, err := service.CheckInSite(t.Context(), site.ID)

	require.NoError(t, err)
	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, 1, refreshRequests)
	assert.Equal(t, 1, checkinRequests)
	assertPersistedAuthTokens(t, db, encSvc, site.ID, "fresh-token", "fresh-refresh-token")
}

func TestPersistAuthUpdatesSkipsStaleCredentialSnapshot(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))

	originalAuth, err := encSvc.Encrypt(`{"access_token":"old-token","refresh_token":"old-refresh"}`)
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "sub2api",
		BaseURL:   "https://example.test",
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: originalAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	userEditedAuth, err := encSvc.Encrypt(`{"access_token":"user-token","refresh_token":"user-refresh"}`)
	require.NoError(t, err)
	require.NoError(t, db.Model(&ManagedSite{}).Where("id = ?", site.ID).Update("auth_value", userEditedAuth).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	err = service.persistAuthUpdates(t.Context(), site, parseAuthConfig(site.AuthType, `{"access_token":"old-token","refresh_token":"old-refresh"}`), map[string]string{
		AuthTypeAccessToken:   "fresh-token",
		authFieldRefreshToken: "fresh-refresh-token",
	})

	require.Error(t, err)
	assertPersistedAuthTokens(t, db, encSvc, site.ID, "user-token", "user-refresh")
}

func TestAutoCheckinRefreshesBalanceAfterSuccessfulCheckin(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/checkin":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		case "/api/user/self":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":2500000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	encryptedAuth, err := encSvc.Encrypt("test-access-token")
	require.NoError(t, err)
	encryptedUserID, err := encSvc.Encrypt("123")
	require.NoError(t, err)
	site := ManagedSite{
		Name:           "newapi",
		BaseURL:        server.URL,
		SiteType:       SiteTypeNewAPI,
		Enabled:        true,
		CheckInEnabled: true,
		AuthType:       AuthTypeAccessToken,
		AuthValue:      encryptedAuth,
		UserID:         encryptedUserID,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	service.SetBalanceService(NewBalanceService(db, encSvc))

	result, err := service.CheckInSite(t.Context(), site.ID)

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	var updated ManagedSite
	require.NoError(t, db.First(&updated, site.ID).Error)
	assert.Equal(t, "$5.00", updated.LastBalance)
	assert.Equal(t, GetBeijingCheckinDay(), updated.LastBalanceDate)
}

func TestAutoCheckinRefreshesBalanceAfterSuccessfulCheckinWithMultiAuth(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/checkin":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
			assert.Contains(t, r.Header.Get("Cookie"), "session=browser-ok")
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		case "/api/user/self":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1500000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authJSON := `{"access_token":"test-access-token","cookie":"session=browser-ok"}`
	encryptedAuth, err := encSvc.Encrypt(authJSON)
	require.NoError(t, err)
	encryptedUserID, err := encSvc.Encrypt("123")
	require.NoError(t, err)
	site := ManagedSite{
		Name:           "newapi-multi-auth",
		BaseURL:        server.URL,
		SiteType:       SiteTypeNewAPI,
		Enabled:        true,
		CheckInEnabled: true,
		AuthType:       AuthTypeAccessToken + "," + AuthTypeCookie,
		AuthValue:      encryptedAuth,
		UserID:         encryptedUserID,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	service.SetBalanceService(NewBalanceService(db, encSvc))

	result, err := service.CheckInSite(t.Context(), site.ID)

	require.NoError(t, err)
	assert.Equal(t, CheckinResultSuccess, result.Status)
	var updated ManagedSite
	require.NoError(t, db.First(&updated, site.ID).Error)
	assert.Equal(t, "$3.00", updated.LastBalance)
	assert.Equal(t, GetBeijingCheckinDay(), updated.LastBalanceDate)
}

func TestAutoCheckinRandomScheduleSkipsCurrentWindowAfterSuccess(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	now := time.Now().In(beijingLocation)
	windowStart := now.Add(-30 * time.Minute)
	windowEnd := now.Add(30 * time.Minute)
	if now.Hour() == 0 {
		windowStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, beijingLocation)
	}
	if now.Hour() == 23 {
		windowEnd = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, beijingLocation)
	}

	require.NoError(t, db.Create(&ManagedSiteSetting{
		ID:                     1,
		AutoCheckinEnabled:     true,
		WindowStart:            windowStart.Format("15:04"),
		WindowEnd:              windowEnd.Format("15:04"),
		ScheduleMode:           AutoCheckinScheduleModeRandom,
		RetryEnabled:           true,
		RetryIntervalMinutes:   3,
		RetryMaxAttemptsPerDay: 3,
	}).Error)

	successStatus := AutoCheckinStatus{
		LastRunAt:     now.UTC().Format(time.RFC3339),
		LastRunResult: AutoCheckinRunResultSuccess,
		Summary: &AutoCheckinRunSummary{
			TotalEligible: 1,
			Executed:      1,
			SuccessCount:  1,
		},
		Attempts: &AutoCheckinAttemptsTracker{
			Date:     todayString(now),
			Attempts: 1,
		},
		PendingRetry: false,
	}
	statusBytes, err := json.Marshal(successStatus)
	require.NoError(t, err)
	require.NoError(t, memStore.Set(autoCheckinStatusKey, statusBytes, time.Hour))

	service := NewAutoCheckinService(db, memStore, encSvc)

	next, enabled, err := service.computeNextTriggerTime(context.Background())

	require.NoError(t, err)
	require.True(t, enabled)
	assert.True(t, next.In(beijingLocation).After(windowEnd),
		"successful auto check-in should not be scheduled again in the same random window")
}

func TestAutoCheckinRandomScheduleSkipsCrossMidnightWindowAfterSuccess(t *testing.T) {
	t.Setenv("TZ", fallbackTimezoneName)

	now := time.Date(2026, 6, 13, 1, 30, 0, 0, beijingLocation)
	cfg := &AutoCheckinConfig{
		ScheduleMode: AutoCheckinScheduleModeRandom,
		WindowStart:  "23:00",
		WindowEnd:    "02:00",
	}

	next, err := computeNextRegularTrigger(cfg, now, true)

	require.NoError(t, err)
	assert.True(t, !next.In(beijingLocation).Before(time.Date(2026, 6, 14, 23, 0, 0, 0, beijingLocation)) &&
		next.In(beijingLocation).Before(time.Date(2026, 6, 15, 2, 0, 0, 0, beijingLocation)),
		"random skip should advance to the next local day for a cross-midnight window")
}

func TestSub2APIRefreshFailureMessageDoesNotExposeRefreshError(t *testing.T) {
	t.Parallel()

	message := sub2APIRefreshFailureMessage(
		"HTTP 401: access token expired",
		errors.New("refresh http 401: echoed refresh_token secret"),
	)

	assert.Equal(t, "HTTP 401: access token expired; token refresh failed: upstream rejected token refresh", message)
	assert.NotContains(t, message, "secret")
	assert.NotContains(t, message, "refresh_token")
}

func TestSub2APIRefreshTokensDoesNotExposeApplicationMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"message":"echoed refresh_token secret"}`))
	}))
	t.Cleanup(server.Close)

	_, err := sub2APIProvider{}.refreshTokens(
		context.Background(),
		server.Client(),
		ManagedSite{BaseURL: server.URL},
		"old-access",
		"old-refresh",
		false,
	)

	require.Error(t, err)
	assert.Equal(t, "upstream rejected token refresh", err.Error())
	assert.NotContains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "refresh_token")
}

func TestSub2APIRefreshTokensSendsWAFSessionCookie(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		assert.Equal(t, "Bearer old-access", r.Header.Get("Authorization"))
		assert.Equal(t, "session=browser; cf_clearance=valid", r.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
	}))
	t.Cleanup(server.Close)

	refreshed, err := sub2APIProvider{}.refreshTokens(
		t.Context(),
		server.Client(),
		ManagedSite{BaseURL: server.URL},
		"old-access",
		"old-refresh",
		true,
		"session=browser; cf_clearance=valid",
	)

	require.NoError(t, err)
	assert.Equal(t, "fresh-token", refreshed.AccessToken)
}

func TestBearerProvidersUseCookieWithStealthRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider checkinProvider
		siteType string
		path     string
	}{
		{name: "sub2api", provider: sub2APIProvider{}, siteType: SiteTypeSub2API, path: "/api/v1/user/check-in"},
		{name: "onehub", provider: newAPIProvider{}, siteType: SiteTypeOneHub, path: "/api/user/checkin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tt.path, r.URL.Path)
				assert.Equal(t, "Bearer access-token", r.Header.Get("Authorization"))
				assert.Equal(t, "session=browser; cf_clearance=valid", r.Header.Get("Cookie"))
				assert.Contains(t, r.Header.Get("User-Agent"), "Chrome/146.0.0.0")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"message":"ok"}`))
			}))
			t.Cleanup(server.Close)

			result, err := tt.provider.CheckIn(t.Context(), server.Client(), ManagedSite{
				BaseURL:      server.URL,
				SiteType:     tt.siteType,
				BypassMethod: BypassMethodStealth,
			}, AuthConfig{
				AuthTypes: []string{AuthTypeAccessToken, AuthTypeCookie},
				AuthValues: map[string]string{
					AuthTypeAccessToken: "access-token",
					AuthTypeCookie:      "session=browser; cf_clearance=valid",
				},
				SupplementalValues: map[string]string{},
			})

			require.NoError(t, err)
			assert.Equal(t, CheckinResultSuccess, result.Status)
		})
	}
}

func TestSub2APIRefreshTokensRejectsInvalidSuccessEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "missing code",
			response: `{"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`,
		},
		{
			name:     "missing expiry",
			response: `{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			t.Cleanup(server.Close)

			_, err := sub2APIProvider{}.refreshTokens(
				t.Context(),
				server.Client(),
				ManagedSite{BaseURL: server.URL},
				"old-access",
				"old-refresh",
				false,
			)

			require.EqualError(t, err, "invalid refresh response")
		})
	}
}

func TestComputeRandomTriggerTreatsWindowEndAsCutoff(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")

	now := time.Date(2026, 6, 13, 18, 0, 0, 0, beijingLocation)

	next, err := computeRandomTrigger("09:00", "18:00", now)

	require.NoError(t, err)
	localNext := next.In(beijingLocation)
	assert.True(t, !localNext.Before(time.Date(2026, 6, 14, 9, 0, 0, 0, beijingLocation)) &&
		localNext.Before(time.Date(2026, 6, 14, 18, 0, 0, 0, beijingLocation)),
		"window end should advance to the next day's random window")
}

func TestComputeRandomTriggerTreatsCrossMidnightWindowEndAsCutoff(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")

	now := time.Date(2026, 6, 13, 2, 0, 0, 0, beijingLocation)

	next, err := computeRandomTrigger("23:00", "02:00", now)

	require.NoError(t, err)
	localNext := next.In(beijingLocation)
	assert.True(t, !localNext.Before(time.Date(2026, 6, 13, 23, 0, 0, 0, beijingLocation)) &&
		localNext.Before(time.Date(2026, 6, 14, 2, 0, 0, 0, beijingLocation)),
		"cross-midnight window end should advance to the next random window")
}

func TestRandomScheduleDayStartTreatsCrossMidnightWindowEndAsExclusive(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")

	startMin := 23 * 60
	endMin := 2 * 60

	beforeEnd := time.Date(2026, 6, 13, 1, 59, 0, 0, beijingLocation)
	atEnd := time.Date(2026, 6, 13, 2, 0, 0, 0, beijingLocation)

	assert.Equal(t, "2026-06-12", randomScheduleDayStart(beforeEnd, startMin, endMin).Format("2006-01-02"))
	assert.Equal(t, "2026-06-13", randomScheduleDayStart(atEnd, startMin, endMin).Format("2006-01-02"))
}

func TestAutoCheckinMultipleScheduleSkipsRemainingTimesAfterSuccess(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	now := time.Now().In(beijingLocation)
	futureToday := sameDayFutureScheduleMinute(t, now)

	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	require.NoError(t, db.Create(&ManagedSiteSetting{
		ID:                     1,
		AutoCheckinEnabled:     true,
		ScheduleTimes:          "00:00," + futureToday.Format("15:04"),
		ScheduleMode:           AutoCheckinScheduleModeMultiple,
		RetryEnabled:           true,
		RetryIntervalMinutes:   3,
		RetryMaxAttemptsPerDay: 3,
	}).Error)
	require.NoError(t, storeSuccessfulAutoCheckinStatus(memStore, now))

	service := NewAutoCheckinService(db, memStore, encSvc)

	next, enabled, err := service.computeNextTriggerTime(context.Background())

	require.NoError(t, err)
	require.True(t, enabled)
	assert.True(t, next.In(beijingLocation).After(futureToday),
		"successful auto check-in should not be scheduled again at a later fixed time on the same day")
}

func TestComputeMultipleTriggerKeepsWallClockTimeAcrossDST(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	now := time.Date(2026, 3, 8, 1, 0, 0, 0, loc)

	next := computeMultipleTrigger([]string{"00:30"}, now)

	localNext := next.In(loc)
	assert.Equal(t, 2026, localNext.Year())
	assert.Equal(t, time.March, localNext.Month())
	assert.Equal(t, 9, localNext.Day())
	assert.Equal(t, 0, localNext.Hour())
	assert.Equal(t, 30, localNext.Minute())
	assert.Equal(t, "-04:00", localNext.Format("-07:00"))
}

func TestAutoCheckinStatusIncludesServerTimezoneMetadata(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	service := NewAutoCheckinService(db, store.NewMemoryStore(), encSvc)

	before := time.Now()
	status := service.GetStatus()

	assert.Equal(t, "America/New_York", status.Timezone)
	resetAt, err := time.Parse(time.RFC3339, status.NextCheckinResetAt)
	require.NoError(t, err)
	// Derive the day from resetAt to avoid another live clock read near midnight.
	assert.Equal(t, resetAt.In(loc).Add(-time.Second).Format("2006-01-02"), status.CurrentCheckinDay)
	assert.True(t, resetAt.After(before))
}

func TestAutoCheckinStatusMetadataUsesServerLocalTimezoneWhenTZUnset(t *testing.T) {
	originalLocal := time.Local
	local, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	time.Local = local
	t.Cleanup(func() {
		time.Local = originalLocal
	})
	t.Setenv("TZ", "")

	status := withCheckinMetadataAt(AutoCheckinStatus{}, time.Date(2026, 6, 29, 3, 30, 0, 0, time.UTC))

	assert.Equal(t, "2026-06-28", status.CurrentCheckinDay)
	assert.Equal(t, "America/New_York", status.Timezone)
	resetAt, err := time.Parse(time.RFC3339, status.NextCheckinResetAt)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 6, 29, 4, 0, 0, 0, time.UTC), resetAt)
}

func TestAutoCheckinStatusMetadataFallsBackToBeijingTimezone(t *testing.T) {
	now := time.Date(2026, 6, 28, 16, 30, 0, 0, time.UTC)

	t.Setenv("TZ", "Invalid/Timezone")

	status := withCheckinMetadataAt(AutoCheckinStatus{}, now)

	assert.Equal(t, "2026-06-29", status.CurrentCheckinDay)
	assert.Equal(t, fallbackTimezoneName, status.Timezone)
	resetAt, err := time.Parse(time.RFC3339, status.NextCheckinResetAt)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC), resetAt)
}

func TestWithCheckinMetadataUsesSingleBaseTime(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	now := time.Date(2026, 6, 1, 23, 59, 59, 0, loc)

	status := withCheckinMetadataAt(AutoCheckinStatus{}, now)

	assert.Equal(t, "2026-06-01", status.CurrentCheckinDay)
	assert.Equal(t, "America/New_York", status.Timezone)
	resetAt, err := time.Parse(time.RFC3339, status.NextCheckinResetAt)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 6, 2, 0, 0, 0, 0, loc).UTC(), resetAt)
}

func TestAutoCheckinDeterministicScheduleSkipsTodayAfterSuccess(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	now := time.Now().In(beijingLocation)
	futureToday := sameDayFutureScheduleMinute(t, now)

	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	require.NoError(t, db.Create(&ManagedSiteSetting{
		ID:                     1,
		AutoCheckinEnabled:     true,
		WindowStart:            "00:00",
		WindowEnd:              "23:59",
		ScheduleMode:           AutoCheckinScheduleModeDeterministic,
		DeterministicTime:      futureToday.Format("15:04"),
		RetryEnabled:           true,
		RetryIntervalMinutes:   3,
		RetryMaxAttemptsPerDay: 3,
	}).Error)
	require.NoError(t, storeSuccessfulAutoCheckinStatus(memStore, now))

	service := NewAutoCheckinService(db, memStore, encSvc)

	next, enabled, err := service.computeNextTriggerTime(context.Background())

	require.NoError(t, err)
	require.True(t, enabled)
	assert.True(t, next.In(beijingLocation).After(futureToday),
		"successful auto check-in should not be scheduled again at the deterministic time on the same day")
}

func TestAutoCheckinRetryTimeStillTakesPriorityAfterFailure(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	memStore := store.NewMemoryStore()

	now := time.Now().In(beijingLocation)
	lastRun := now.Add(-time.Minute)

	require.NoError(t, db.AutoMigrate(&ManagedSiteSetting{}))
	require.NoError(t, db.Create(&ManagedSiteSetting{
		ID:                     1,
		AutoCheckinEnabled:     true,
		ScheduleTimes:          sameDayFutureScheduleMinute(t, now).Format("15:04"),
		ScheduleMode:           AutoCheckinScheduleModeMultiple,
		RetryEnabled:           true,
		RetryIntervalMinutes:   3,
		RetryMaxAttemptsPerDay: 3,
	}).Error)

	failedStatus := AutoCheckinStatus{
		LastRunAt:     lastRun.UTC().Format(time.RFC3339),
		LastRunResult: AutoCheckinRunResultFailed,
		Summary: &AutoCheckinRunSummary{
			TotalEligible: 1,
			Executed:      1,
			FailedCount:   1,
			NeedsRetry:    true,
		},
		Attempts: &AutoCheckinAttemptsTracker{
			Date:     todayString(now),
			Attempts: 1,
		},
		PendingRetry: true,
	}
	statusBytes, err := json.Marshal(failedStatus)
	require.NoError(t, err)
	require.NoError(t, memStore.Set(autoCheckinStatusKey, statusBytes, time.Hour))

	service := NewAutoCheckinService(db, memStore, encSvc)

	next, enabled, err := service.computeNextTriggerTime(context.Background())

	require.NoError(t, err)
	require.True(t, enabled)
	assert.WithinDuration(t, lastRun.Add(3*time.Minute), next.In(beijingLocation), 2*time.Second)
}

func sameDayFutureScheduleMinute(t *testing.T, now time.Time) time.Time {
	t.Helper()

	future := now.Add(30 * time.Minute).Truncate(time.Minute)
	if !future.After(now) {
		future = future.Add(time.Minute)
	}
	if future.In(beijingLocation).YearDay() != now.In(beijingLocation).YearDay() {
		t.Skip("cannot construct a later same-day HH:MM schedule in the final minutes of the day")
	}
	return future.In(beijingLocation)
}

func TestAutoCheckinServiceLoadConfigCanonicalizesLegacyTimesWithoutWriting(t *testing.T) {
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

	service := NewAutoCheckinService(db, nil, encSvc)
	cfg, err := service.loadConfig(context.Background())

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

func TestAutoCheckinServiceStopClosesSubscriptionsOnce(t *testing.T) {
	t.Parallel()

	configSubscription := &countingSubscription{channel: make(chan *store.Message)}
	runNowSubscription := &countingSubscription{channel: make(chan *store.Message)}
	service := NewAutoCheckinService(setupTestDB(t), nil, setupTestEncryption(t))
	service.subConfig = configSubscription
	service.subRunNow = runNowSubscription

	service.Stop(context.Background())
	service.Stop(context.Background())

	assert.Equal(t, int32(1), configSubscription.closeCount.Load())
	assert.Equal(t, int32(1), runNowSubscription.closeCount.Load())
}

func TestSub2APIAuthManagerReturnsPersistenceError(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
	}))
	t.Cleanup(server.Close)

	encryptedAuth, err := encSvc.Encrypt(`{"access_token":"old-token","refresh_token":"old-refresh-token"}`)
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Sub2API persistence failure",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	state := sub2APIAuthState{
		Site:         site,
		Config:       parseAuthConfig(site.AuthType, `{"access_token":"old-token","refresh_token":"old-refresh-token"}`),
		AccessToken:  "old-token",
		RefreshToken: "old-refresh-token",
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	manager := newSub2APIAuthManager(db, encSvc)
	err = manager.refresh(t.Context(), &state, server.Client(), false)

	require.Error(t, err)
	assert.Equal(t, "old-token", state.AccessToken)
	assert.Equal(t, "old-refresh-token", state.RefreshToken)
}

func TestSub2APIAuthManagerAdoptsCredentialsRotatedByAnotherInstance(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"refresh token already rotated"}`))
	}))
	t.Cleanup(server.Close)

	oldAuth, err := encSvc.Encrypt(`{"access_token":"old-token","refresh_token":"old-refresh-token"}`)
	require.NoError(t, err)
	site := ManagedSite{
		Name:      "Sub2API cross-instance refresh",
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: oldAuth,
	}
	require.NoError(t, db.Create(&site).Error)

	state := sub2APIAuthState{
		Site:             site,
		Config:           parseAuthConfig(site.AuthType, `{"access_token":"old-token","refresh_token":"old-refresh-token"}`),
		AccessToken:      "old-token",
		RefreshToken:     "old-refresh-token",
		RefreshAttempted: false,
	}

	rotatedAuth, err := encSvc.Encrypt(`{"access_token":"fresh-token","refresh_token":"fresh-refresh-token"}`)
	require.NoError(t, err)
	require.NoError(t, db.Model(&ManagedSite{}).Where("id = ?", site.ID).Update("auth_value", rotatedAuth).Error)

	manager := newSub2APIAuthManager(db, encSvc)
	err = manager.refresh(t.Context(), &state, server.Client(), false)

	require.NoError(t, err)
	assert.Equal(t, "fresh-token", state.AccessToken)
	assert.Equal(t, "fresh-refresh-token", state.RefreshToken)
	assert.True(t, state.RefreshAttempted)
}

func TestAutoCheckinRetriesSub2APIWithCredentialsRotatedByAnotherInstance(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	require.NoError(t, db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}))

	oldAuth, err := encSvc.Encrypt(`{"access_token":"old-token","refresh_token":"old-refresh-token"}`)
	require.NoError(t, err)
	rotatedAuth, err := encSvc.Encrypt(`{"access_token":"fresh-token","refresh_token":"fresh-refresh-token"}`)
	require.NoError(t, err)

	var site ManagedSite
	var checkinRequests atomic.Int32
	var refreshRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/check-in":
			checkinRequests.Add(1)
			if r.Header.Get("Authorization") == "Bearer fresh-token" {
				_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"access token expired"}`))
		case "/api/v1/auth/refresh":
			refreshRequests.Add(1)
			if updateErr := db.Model(&ManagedSite{}).Where("id = ?", site.ID).Update("auth_value", rotatedAuth).Error; updateErr != nil {
				http.Error(w, "failed to rotate credentials", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"refresh token already rotated"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	site = ManagedSite{
		Name:               "Sub2API cross-instance check-in",
		BaseURL:            server.URL,
		SiteType:           SiteTypeSub2API,
		AuthType:           AuthTypeAccessToken,
		AuthValue:          oldAuth,
		Enabled:            true,
		CheckInEnabled:     true,
		AutoCheckInEnabled: true,
	}
	require.NoError(t, db.Create(&site).Error)

	service := NewAutoCheckinService(db, nil, encSvc)
	result := service.checkInOne(t.Context(), site)

	assert.Equal(t, CheckinResultSuccess, result.Status)
	assert.Equal(t, int32(2), checkinRequests.Load())
	assert.Equal(t, int32(1), refreshRequests.Load())
	var logs []ManagedSiteCheckinLog
	require.NoError(t, db.Where("site_id = ?", site.ID).Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, CheckinResultSuccess, logs[0].Status)
}

func TestAutoCheckinFailsWhenRotatedCredentialsCannotPersist(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)
	expiredToken := testSub2APIJWTWithExp(time.Now().Add(-time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}}`))
		case "/api/v1/user/check-in":
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"message":"签到成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	encryptedAuth, err := encSvc.Encrypt(`{"access_token":"` + expiredToken + `","refresh_token":"old-refresh-token"}`)
	require.NoError(t, err)
	site := ManagedSite{
		ID:        1,
		BaseURL:   server.URL,
		SiteType:  SiteTypeSub2API,
		AuthType:  AuthTypeAccessToken,
		AuthValue: encryptedAuth,
	}
	service := NewAutoCheckinService(db, nil, encSvc)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	result := service.checkInOneRequest(t.Context(), site)

	assert.Equal(t, CheckinResultFailed, result.Status)
	assert.Equal(t, "credential update failed", result.Message)
}

func storeSuccessfulAutoCheckinStatus(memStore store.Store, now time.Time) error {
	successStatus := AutoCheckinStatus{
		LastRunAt:     now.UTC().Format(time.RFC3339),
		LastRunResult: AutoCheckinRunResultSuccess,
		Summary: &AutoCheckinRunSummary{
			TotalEligible: 1,
			Executed:      1,
			SuccessCount:  1,
		},
		Attempts: &AutoCheckinAttemptsTracker{
			Date:     todayString(now),
			Attempts: 1,
		},
		PendingRetry: false,
	}
	statusBytes, err := json.Marshal(successStatus)
	if err != nil {
		return err
	}
	return memStore.Set(autoCheckinStatusKey, statusBytes, time.Hour)
}

func testSub2APIJWTWithExp(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":` + strconv.FormatInt(exp.Unix(), 10) + `}`))
	return header + "." + payload + ".signature"
}

func testPoWMeetsDifficulty(challenge, nonce string, difficulty int) bool {
	sum := sha256.Sum256([]byte(challenge + nonce))
	fullBytes := difficulty / 8
	remainingBits := difficulty % 8
	for i := 0; i < fullBytes; i++ {
		if sum[i] != 0 {
			return false
		}
	}
	if remainingBits == 0 {
		return true
	}
	mask := byte(0xff << (8 - remainingBits))
	return sum[fullBytes]&mask == 0
}
