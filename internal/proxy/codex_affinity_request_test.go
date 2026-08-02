package proxy

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexSelectionErrorMappingSanitizesClientMessages(t *testing.T) {
	rawErr := errors.New(`Post "https://upstream.example/v1/responses?key=plain-secret": dial failed`)

	status, apiErr := mapCodexSelectionError(rawErr, &models.APIKey{}, false)
	require.Equal(t, 500, status)
	require.Contains(t, apiErr.Message, "Failed to select upstream")
	require.NotContains(t, apiErr.Message, "plain-secret")
	require.True(t, strings.Contains(apiErr.Message, "key=[REDACTED]"))

	status, apiErr = mapCodexSelectionError(rawErr, nil, true)
	require.Equal(t, 503, status)
	require.Equal(t, "NO_KEYS_AVAILABLE", apiErr.Code)
	require.NotContains(t, apiErr.Message, "plain-secret")
}

func TestCodexAffinityKeyPrefersCanonicalThreadMetadata(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/proxy/codex/v1/responses", nil)
	c.Request.Header.Set("Thread-Id", "stale-header-thread")
	group := &models.Group{
		ID:          1,
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      map[string]any{"codex_affinity_enabled": true},
	}
	body := []byte(`{"client_metadata":{"thread_id":"flat-thread","x-codex-turn-metadata":"{\"thread_id\":\"canonical-thread\",\"session_id\":\"canonical-thread\",\"window_id\":\"canonical-thread:1\"}"}}`)

	require.Equal(t, "canonical-thread", codexAffinityKey(c, group, body))
}

func TestCodexAffinityKeyFallsBackToThreadHeaderWithoutMetadata(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/proxy/codex/v1/responses", nil)
	c.Request.Header.Set("Thread-Id", "header-thread")
	group := &models.Group{
		ID:          1,
		GroupType:   "standard",
		ChannelType: "openai-response",
		Config:      map[string]any{"codex_affinity_enabled": true},
	}

	require.Equal(t, "header-thread", codexAffinityKey(c, group, []byte(`{"model":"gpt-5"}`)))
}

func TestSanitizeCodexIdentityChangeSynchronizesCompatibilityHeaders(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/proxy/codex/v1/responses", nil)
	c.Request.Header.Set("X-Codex-Turn-State", "stale-turn-state")
	c.Request.Header.Set("X-Codex-Installation-Id", "stale-installation")
	c.Request.Header.Set("Session-Id", "stale-session")
	c.Request.Header.Set("Thread-Id", "stale-thread")
	c.Request.Header.Set("X-Codex-Window-Id", "stale-window")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"thread_id":"stale-thread"}`)

	turnMetadata := `{"installation_id":"install-new","session_id":"session-new","thread_id":"thread-new","turn_id":"turn-new","window_id":"window-new","request_kind":"turn"}`
	body := []byte(`{"model":"gpt-5","client_metadata":{"x-codex-installation-id":"install-new","session_id":"session-new","thread_id":"thread-new","turn_id":"turn-new","x-codex-window-id":"window-new","x-codex-turn-metadata":` + string(mustJSONMarshal(t, turnMetadata)) + `},"input":"hello"}`)

	_, err := sanitizeCodexIdentityChange(c, body, &models.Group{ChannelType: "openai-response"}, false)
	require.NoError(t, err)
	require.Empty(t, c.Request.Header.Get("X-Codex-Turn-State"))
	require.Equal(t, "install-new", c.Request.Header.Get("X-Codex-Installation-Id"))
	require.Equal(t, "session-new", c.Request.Header.Get("Session-Id"))
	require.Equal(t, "thread-new", c.Request.Header.Get("Thread-Id"))
	require.Equal(t, "window-new", c.Request.Header.Get("X-Codex-Window-Id"))
	require.JSONEq(t, turnMetadata, c.Request.Header.Get("X-Codex-Turn-Metadata"))
}

func TestSanitizeCodexIdentityChangeClearsStaleCompatibilityHeadersWithoutMetadata(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/proxy/codex/v1/responses", nil)
	for _, name := range []string{
		"X-Codex-Installation-Id", "Session-Id", "Thread-Id", "X-Codex-Window-Id",
		"X-Codex-Parent-Thread-Id", "X-Codex-Turn-Metadata", "X-OpenAI-Subagent",
		"Session_ID", "Thread_ID",
	} {
		c.Request.Header.Set(name, "stale")
	}

	_, err := sanitizeCodexIdentityChange(c, []byte(`{"model":"gpt-5","input":"hello"}`), &models.Group{ChannelType: "openai-response"}, false)
	require.NoError(t, err)
	for _, name := range []string{
		"X-Codex-Installation-Id", "Session-Id", "Thread-Id", "X-Codex-Window-Id",
		"X-Codex-Parent-Thread-Id", "X-Codex-Turn-Metadata", "X-OpenAI-Subagent",
		"Session_ID", "Thread_ID",
	} {
		require.Empty(t, c.Request.Header.Get(name), name)
	}
}

func TestSanitizeCodexIdentityChangeBoundsTurnMetadataCompatibilityHeader(t *testing.T) {
	t.Parallel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/proxy/codex/v1/responses", nil)
	turnMetadata := `{"thread_id":"thread-new","parent_thread_id":"parent-new","request_kind":"turn","code_mode_tool_names":{"large":"mapping"}}`
	body := []byte(`{"model":"gpt-5","client_metadata":{"thread_id":"thread-new","x-codex-parent-thread-id":"parent-new","x-openai-subagent":"review","x-codex-turn-metadata":` + string(mustJSONMarshal(t, turnMetadata)) + `},"input":"hello"}`)

	sanitized, err := sanitizeCodexIdentityChange(c, body, &models.Group{ChannelType: "openai-response"}, false)
	require.NoError(t, err)
	require.Contains(t, string(sanitized), "code_mode_tool_names")
	require.JSONEq(t, `{"thread_id":"thread-new","parent_thread_id":"parent-new","request_kind":"turn"}`, c.Request.Header.Get("X-Codex-Turn-Metadata"))
	require.Equal(t, "parent-new", c.Request.Header.Get("X-Codex-Parent-Thread-Id"))
	require.Equal(t, "review", c.Request.Header.Get("X-OpenAI-Subagent"))
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}
