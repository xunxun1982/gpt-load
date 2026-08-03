package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/utils"

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

func TestPrepareStandardCodexAffinityClassifiesUpstreamSelectionFailure(t *testing.T) {
	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	group := createTestGroup(t, db, "standard-affinity-selection-error", "openai-response")
	group.Config = map[string]any{"codex_affinity_enabled": true}
	require.NoError(t, db.Save(group).Error)
	createTestKey(t, db, group.ID, "sk-selection-error", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/proxy/"+group.Name+"/v1/responses", nil)
	retryCtx := &retryContext{originalBodyBytes: []byte(`{"model":"gpt-5","input":"hello"}`)}
	rawErr := errors.New("upstream selection failed")
	_, err := ps.prepareStandardCodexAffinity(c, &testChannelProxy{selectErr: rawErr}, group, retryCtx.originalBodyBytes, retryCtx)
	require.ErrorIs(t, err, rawErr)

	status, apiErr := mapCodexSelectionError(err, nil, true)
	require.Equal(t, 500, status)
	require.Equal(t, "INTERNAL_SERVER_ERROR", apiErr.Code)
}

func TestStandardCodexDispatchRejectsEmptyFallbackResolution(t *testing.T) {
	db := setupTestDB(t)
	ps := setupTestProxyServer(t, db)
	group := createTestGroup(t, db, "standard-affinity-empty-fallback", "openai-response")
	createTestKey(t, db, group.ID, "sk-empty-fallback", ps.encryptionSvc)
	require.NoError(t, ps.keyProvider.LoadKeysFromDB())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/proxy/"+group.Name+"/v1/responses", nil)
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	retryCtx := &retryContext{
		codexAffinityEnabled: true,
		codexSelection: &codexExecutionSelection{binding: codexAffinityBinding{
			executionGroupID: group.ID, keyID: 1, upstreamIdentity: testChannelProxyIdentity,
		}},
	}

	_, upstream, _, err := ps.standardCodexDispatchSelection(c, &testChannelProxy{
		client: &http.Client{}, url: "https://upstream.example/v1/responses", resolveEmpty: true,
	}, group, body, retryCtx)
	require.Error(t, err)
	require.Nil(t, upstream)
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

func TestSyncCodexHeaderRejectsInvalidFieldValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"line\rbreak", "line\nbreak", "null\x00byte", "delete\x7fbyte"} {
		header := http.Header{"Thread-Id": []string{"stale"}}
		syncCodexHeader(header, "Thread-Id", value)
		require.Empty(t, header.Get("Thread-Id"))
	}

	header := make(http.Header)
	syncCodexHeader(header, "Thread-Id", "tab\tvalue")
	require.Equal(t, "tab\tvalue", header.Get("Thread-Id"))
}

func TestSyncCodexCompatibilityHeadersRejectsControlsBeforeTrimming(t *testing.T) {
	t.Parallel()

	header := http.Header{"Thread-Id": []string{"stale"}}
	syncCodexCompatibilityHeaders(header, []byte(`{"client_metadata":{"thread_id":"\ntrimmed"}}`))
	require.Empty(t, header.Get("Thread-Id"))
}

func TestBoundedCodexTurnMetadataHeaderRejectsOversizedValue(t *testing.T) {
	t.Parallel()

	original := `{"payload":"` + strings.Repeat("x", utils.MaxForwardedMetadataHeaderBytes) + `"}`
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(original), &metadata))
	require.Empty(t, boundedCodexTurnMetadataHeader(metadata, original))
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}
