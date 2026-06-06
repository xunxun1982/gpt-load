package sitemanagement

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
