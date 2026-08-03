package channel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/utils"

	"github.com/stretchr/testify/require"
)

func TestEnsureCodexRequestIdentityRejectsUnsafeMetadataHeaders(t *testing.T) {
	t.Parallel()

	for _, threadID := range []string{"line\nbreak", "\rtrimmed"} {
		payload, err := json.Marshal(map[string]any{
			"client_metadata": map[string]any{"thread_id": threadID},
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "https://example.test/v1/responses", bytes.NewReader(payload))

		require.NotEmpty(t, ensureCodexRequestIdentity(req))
		require.NotEmpty(t, req.Header.Get("Thread-Id"))
		require.NotEqual(t, strings.TrimSpace(threadID), req.Header.Get("Thread-Id"))
	}
}

func TestEnsureCodexRequestIdentityOmitsOversizedTurnMetadataHeader(t *testing.T) {
	t.Parallel()

	turnMetadata, err := json.Marshal(map[string]any{"payload": strings.Repeat("x", utils.MaxForwardedMetadataHeaderBytes)})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"client_metadata": map[string]any{"x-codex-turn-metadata": string(turnMetadata)},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "https://example.test/v1/responses", bytes.NewReader(payload))

	require.NotEmpty(t, ensureCodexRequestIdentity(req))
	require.Empty(t, req.Header.Get("X-Codex-Turn-Metadata"))
}
