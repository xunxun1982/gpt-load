package sitemanagement

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.True(t, strings.Contains(result.Message, "browser"))
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
	t.Parallel()

	now := time.Date(2026, 6, 13, 1, 30, 0, 0, beijingLocation)
	cfg := &AutoCheckinConfig{
		ScheduleMode: AutoCheckinScheduleModeRandom,
		WindowStart:  "23:00",
		WindowEnd:    "02:00",
	}

	next, err := computeNextRegularTrigger(cfg, now, true)

	require.NoError(t, err)
	assert.True(t, !next.In(beijingLocation).Before(time.Date(2026, 6, 13, 23, 0, 0, 0, beijingLocation)) &&
		next.In(beijingLocation).Before(time.Date(2026, 6, 14, 2, 0, 0, 0, beijingLocation)),
		"successful auto check-in during a cross-midnight random window should skip to the next calendar day's window")
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
