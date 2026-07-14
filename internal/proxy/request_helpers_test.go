package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

func TestApplyParamOverrides(t *testing.T) {
	ps := &ProxyServer{}

	t.Run("no overrides", func(t *testing.T) {
		group := &models.Group{
			ParamOverrides: datatypes.JSONMap{},
		}
		input := []byte(`{"model":"gpt-4","temperature":0.7}`)
		result, err := ps.applyParamOverrides(input, group)

		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("apply single override", func(t *testing.T) {
		group := &models.Group{
			ParamOverrides: datatypes.JSONMap{
				"temperature": 0.5,
			},
		}
		input := []byte(`{"model":"gpt-4","temperature":0.7}`)
		result, err := ps.applyParamOverrides(input, group)

		assert.NoError(t, err)

		var resultData map[string]any
		err = json.Unmarshal(result, &resultData)
		assert.NoError(t, err)
		assert.Equal(t, 0.5, resultData["temperature"])
	})

	t.Run("apply multiple overrides", func(t *testing.T) {
		group := &models.Group{
			ParamOverrides: datatypes.JSONMap{
				"temperature": 0.5,
				"max_tokens":  100,
			},
		}
		input := []byte(`{"model":"gpt-4"}`)
		result, err := ps.applyParamOverrides(input, group)

		assert.NoError(t, err)

		var resultData map[string]any
		err = json.Unmarshal(result, &resultData)
		assert.NoError(t, err)
		assert.Equal(t, 0.5, resultData["temperature"])
		assert.Equal(t, float64(100), resultData["max_tokens"])
	})

	t.Run("invalid JSON input", func(t *testing.T) {
		group := &models.Group{
			ParamOverrides: datatypes.JSONMap{"temperature": 0.5},
		}
		input := []byte(`{invalid json}`)
		result, err := ps.applyParamOverrides(input, group)

		// Should pass through on error
		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("empty input", func(t *testing.T) {
		group := &models.Group{
			ParamOverrides: datatypes.JSONMap{"temperature": 0.5},
		}
		input := []byte(``)
		result, err := ps.applyParamOverrides(input, group)

		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})
}

func TestApplyModelMapping(t *testing.T) {
	ps := &ProxyServer{}

	t.Run("no mapping configured", func(t *testing.T) {
		group := &models.Group{
			ModelMapping:      "",
			ModelMappingCache: map[string]string{},
		}
		input := []byte(`{"model":"gpt-4"}`)
		result, originalModel := ps.applyModelMapping(input, group)

		assert.Equal(t, input, result)
		assert.Equal(t, "", originalModel)
	})

	t.Run("apply mapping from cache", func(t *testing.T) {
		group := &models.Group{
			ModelMappingCache: map[string]string{
				"alias-model": "real-model",
			},
		}
		input := []byte(`{"model":"alias-model"}`)
		result, originalModel := ps.applyModelMapping(input, group)

		assert.NotEqual(t, input, result)
		assert.Equal(t, "alias-model", originalModel)

		var resultData map[string]any
		err := json.Unmarshal(result, &resultData)
		assert.NoError(t, err)
		assert.Equal(t, "real-model", resultData["model"])
	})

	t.Run("no mapping match", func(t *testing.T) {
		group := &models.Group{
			ModelMappingCache: map[string]string{
				"alias-model": "real-model",
			},
		}
		input := []byte(`{"model":"other-model"}`)
		result, originalModel := ps.applyModelMapping(input, group)

		assert.Equal(t, input, result)
		assert.Equal(t, "other-model", originalModel)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		group := &models.Group{
			ModelMappingCache: map[string]string{"alias": "real"},
		}
		input := []byte(`{invalid}`)
		result, originalModel := ps.applyModelMapping(input, group)

		assert.Equal(t, input, result)
		assert.Equal(t, "", originalModel)
	})

	t.Run("missing model field", func(t *testing.T) {
		group := &models.Group{
			ModelMappingCache: map[string]string{"alias": "real"},
		}
		input := []byte(`{"temperature":0.7}`)
		result, originalModel := ps.applyModelMapping(input, group)

		assert.Equal(t, input, result)
		assert.Equal(t, "", originalModel)
	})

	t.Run("empty model value", func(t *testing.T) {
		group := &models.Group{
			ModelMappingCache: map[string]string{"alias": "real"},
		}
		input := []byte(`{"model":""}`)
		result, originalModel := ps.applyModelMapping(input, group)

		assert.Equal(t, input, result)
		assert.Equal(t, "", originalModel)
	})
}

func TestApplyParallelToolCallsConfig(t *testing.T) {
	ps := &ProxyServer{}

	t.Run("no config", func(t *testing.T) {
		group := &models.Group{
			Config: datatypes.JSONMap{},
		}
		input := []byte(`{"model":"gpt-4","tools":[]}`)
		result, err := ps.applyParallelToolCallsConfig(input, group)

		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("apply parallel_tool_calls false", func(t *testing.T) {
		group := &models.Group{
			Config: datatypes.JSONMap{
				"parallel_tool_calls": false,
			},
		}
		input := []byte(`{"model":"gpt-4","tools":[{"type":"function"}]}`)
		result, err := ps.applyParallelToolCallsConfig(input, group)

		assert.NoError(t, err)

		var resultData map[string]any
		err = json.Unmarshal(result, &resultData)
		assert.NoError(t, err)
		assert.Equal(t, false, resultData["parallel_tool_calls"])
	})

	t.Run("no tools in request", func(t *testing.T) {
		group := &models.Group{
			Config: datatypes.JSONMap{
				"parallel_tool_calls": false,
			},
		}
		input := []byte(`{"model":"gpt-4"}`)
		result, err := ps.applyParallelToolCallsConfig(input, group)

		assert.NoError(t, err)
		// Should not add parallel_tool_calls if no tools
		assert.Equal(t, input, result)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		group := &models.Group{
			Config: datatypes.JSONMap{
				"parallel_tool_calls": false,
			},
		}
		input := []byte(`{invalid}`)
		result, err := ps.applyParallelToolCallsConfig(input, group)

		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})
}

func TestApplyStreamOverrideConfig(t *testing.T) {
	ps := &ProxyServer{}

	t.Run("force stream", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"force_stream": true}}
		input := []byte(`{"model":"gpt-4","stream":false}`)

		result, err := ps.applyStreamOverrideConfig(input, group, false)
		assert.NoError(t, err)

		var resultData map[string]any
		assert.NoError(t, json.Unmarshal(result, &resultData))
		assert.Equal(t, true, resultData["stream"])
	})

	t.Run("force non stream", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"force_non_stream": true}}
		input := []byte(`{"model":"gpt-4","stream":true}`)

		result, err := ps.applyStreamOverrideConfig(input, group, false)
		assert.NoError(t, err)

		var resultData map[string]any
		assert.NoError(t, json.Unmarshal(result, &resultData))
		assert.Equal(t, false, resultData["stream"])
	})

	t.Run("adds missing stream for known stream endpoint", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"force_stream": true}}
		input := []byte(`{"model":"gpt-4"}`)

		result, err := ps.applyStreamOverrideConfig(input, group, true)
		assert.NoError(t, err)

		var resultData map[string]any
		assert.NoError(t, json.Unmarshal(result, &resultData))
		assert.Equal(t, true, resultData["stream"])
	})

	t.Run("does not add missing stream for unknown schema", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"force_stream": true}}
		input := []byte(`{"model":"gpt-4"}`)

		result, err := ps.applyStreamOverrideConfig(input, group, false)
		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("no config", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{}}
		input := []byte(`{"model":"gpt-4"}`)

		result, err := ps.applyStreamOverrideConfig(input, group, false)
		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("conflicting config", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"force_stream": true, "force_non_stream": true}}
		input := []byte(`{"model":"gpt-4","stream":false}`)

		result, err := ps.applyStreamOverrideConfig(input, group, false)
		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("allows missing stream on chat completions and responses", func(t *testing.T) {
		assert.True(t, allowsMissingStreamOverride("/v1/chat/completions", http.MethodPost))
		assert.True(t, allowsMissingStreamOverride("/proxy/group/v1/chat/completions", http.MethodPost))
		assert.True(t, allowsMissingStreamOverride("/v1/responses", http.MethodPost))
		assert.True(t, allowsMissingStreamOverride("/v1/messages", http.MethodPost))
		assert.True(t, allowsMissingStreamOverride("/proxy/group/v1/messages", http.MethodPost))
		assert.False(t, allowsMissingStreamOverride("/v1/custom", http.MethodPost))
		assert.False(t, allowsMissingStreamOverride("/v1/chat/completions", http.MethodGet))
		assert.False(t, allowsMissingStreamOverride("/v1/messages", http.MethodGet))
	})
}

func TestApplySimulatedClientHeaders(t *testing.T) {
	t.Run("no config preserves client headers", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","input":"hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("User-Agent", "custom-client/1.0")
		req.Header.Set("OpenAI-Beta", "custom-beta")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{}}, false)

		assert.Equal(t, "custom-client/1.0", req.Header.Get("User-Agent"))
		assert.Equal(t, "custom-beta", req.Header.Get("OpenAI-Beta"))
		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})

	t.Run("codex preset sets client fingerprint without touching auth headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5","input":"hello"}`)))
		req.Header.Set("Authorization", "Bearer upstream-key")
		req.Header.Set("x-api-key", "upstream-key")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, true)

		assert.Equal(t, buildCodexUserAgent(channel.DefaultCodexVersion), req.Header.Get("User-Agent"))
		assert.Equal(t, channel.DefaultCodexVersion, req.Header.Get("Version"))
		assert.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
		assert.Equal(t, "codex-tui", req.Header.Get("originator"))
		assert.Empty(t, req.Header.Get("X-Codex-Beta-Features"))
		installationID := req.Header.Get("X-Codex-Installation-Id")
		assert.NotEmpty(t, installationID)
		sessionID := req.Header.Get("Session-Id")
		threadID := req.Header.Get("Thread-Id")
		assert.NotEmpty(t, sessionID)
		assert.NotEmpty(t, threadID)
		assert.Equal(t, threadID, req.Header.Get("x-client-request-id"))
		windowID := req.Header.Get("X-Codex-Window-Id")
		assert.NotEmpty(t, windowID)
		turnMetadataJSON := req.Header.Get("X-Codex-Turn-Metadata")
		var turnMetadata map[string]any
		assert.NoError(t, json.Unmarshal([]byte(turnMetadataJSON), &turnMetadata))
		assert.Equal(t, "turn", turnMetadata["request_kind"])
		assert.NotEmpty(t, turnMetadata["turn_id"])
		assert.Equal(t, windowID, turnMetadata["window_id"])
		assert.Equal(t, installationID, turnMetadata["installation_id"])
		assert.Equal(t, sessionID, turnMetadata["session_id"])
		assert.Equal(t, threadID, turnMetadata["thread_id"])
		assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer upstream-key", req.Header.Get("Authorization"))
		assert.Equal(t, "upstream-key", req.Header.Get("x-api-key"))

		body, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, int64(len(body)), req.ContentLength)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(body, &payload))
		clientMetadata, ok := payload["client_metadata"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, installationID, clientMetadata["x-codex-installation-id"])
			assert.Equal(t, sessionID, clientMetadata["session_id"])
			assert.Equal(t, threadID, clientMetadata["thread_id"])
			assert.Equal(t, turnMetadata["turn_id"], clientMetadata["turn_id"])
			assert.Equal(t, windowID, clientMetadata["x-codex-window-id"])
			assert.Equal(t, turnMetadataJSON, clientMetadata["x-codex-turn-metadata"])
		}
		if assert.NotNil(t, req.GetBody) {
			bodyCopy, getBodyErr := req.GetBody()
			if assert.NoError(t, getBodyErr) {
				defer bodyCopy.Close()
				copied, readErr := io.ReadAll(bodyCopy)
				assert.NoError(t, readErr)
				assert.Equal(t, body, copied)
			}
		}
	})

	t.Run("codex preset preserves existing openai beta tokens", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("OpenAI-Beta", "custom-beta,responses=experimental")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		assert.Equal(t, "custom-beta,responses=experimental", req.Header.Get("OpenAI-Beta"))
	})

	t.Run("codex preset preserves explicit media type headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Accept", "text/plain")
		req.Header.Set("Content-Type", "multipart/form-data; boundary=test")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		assert.Equal(t, "text/plain", req.Header.Get("Accept"))
		assert.Equal(t, "multipart/form-data; boundary=test", req.Header.Get("Content-Type"))
	})

	t.Run("claude code preset preserves explicit media type headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Accept", "text/plain")
		req.Header.Set("Content-Type", "multipart/form-data; boundary=test")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		assert.Equal(t, "text/plain", req.Header.Get("Accept"))
		assert.Equal(t, "multipart/form-data; boundary=test", req.Header.Get("Content-Type"))
	})

	t.Run("media type headers with blank values use preset defaults", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Accept", "  ")
		req.Header.Set("Content-Type", "\t")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, true)

		assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	})

	t.Run("claude code preset sets stainless fingerprint without touching auth headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer upstream-key")
		req.Header.Set("x-api-key", "upstream-key")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		assert.Equal(t, buildClaudeCodeUserAgent(channel.DefaultClaudeCodeVersion), req.Header.Get("User-Agent"))
		assert.Equal(t, "application/json", req.Header.Get("Accept"))
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.Equal(t, "cli", req.Header.Get("X-App"))
		assert.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
		assert.Contains(t, req.Header.Get("anthropic-beta"), "claude-code-20250219")
		assert.Contains(t, req.Header.Get("anthropic-beta"), "redact-thinking-2026-02-12")
		assert.Contains(t, req.Header.Get("anthropic-beta"), "context-management-2025-06-27")
		assert.Contains(t, req.Header.Get("anthropic-beta"), "prompt-caching-scope-2026-01-05")
		assert.Contains(t, req.Header.Get("anthropic-beta"), "mid-conversation-system-2026-04-07")
		assert.Contains(t, req.Header.Get("anthropic-beta"), "effort-2025-11-24")
		assert.Equal(t, "true", req.Header.Get("Anthropic-Dangerous-Direct-Browser-Access"))
		assert.Equal(t, "js", req.Header.Get("X-Stainless-Lang"))
		assert.Equal(t, "node", req.Header.Get("X-Stainless-Runtime"))
		assert.Equal(t, "0", req.Header.Get("X-Stainless-Retry-Count"))
		assert.Equal(t, "600", req.Header.Get("X-Stainless-Timeout"))
		assert.Equal(t, "Bearer upstream-key", req.Header.Get("Authorization"))
		assert.Equal(t, "upstream-key", req.Header.Get("x-api-key"))
		assert.NotEmpty(t, req.Header.Get("X-Claude-Code-Session-Id"))
	})

	t.Run("custom client versions override default user agents", func(t *testing.T) {
		codexReq := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		applySimulatedClientHeaders(codexReq, &models.Group{Config: datatypes.JSONMap{
			"simulated_client":        "codex",
			"simulated_codex_version": "1.32",
		}}, false)
		codexUA := codexReq.Header.Get("User-Agent")
		assert.Equal(t, buildCodexUserAgent("1.32"), codexUA)
		assert.Contains(t, codexUA, "codex-tui/1.32")
		assert.Contains(t, codexUA, "(codex-tui; 1.32)")
		assert.Equal(t, "1.32", codexReq.Header.Get("Version"))
		assert.Equal(t, "codex-tui", codexReq.Header.Get("originator"))

		claudeReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		claudeReq.Header.Set("anthropic-beta", "custom-beta,claude-code-20250219")
		applySimulatedClientHeaders(claudeReq, &models.Group{Config: datatypes.JSONMap{
			"simulated_client":              "claude_code",
			"simulated_claude_code_version": "1.32.6.9.8",
		}}, false)
		assert.Equal(t, buildClaudeCodeUserAgent("1.32.6.9.8"), claudeReq.Header.Get("User-Agent"))
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "claude-code-20250219")
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "custom-beta")
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "interleaved-thinking-2025-05-14")
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "redact-thinking-2026-02-12")
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "context-management-2025-06-27")
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "prompt-caching-scope-2026-01-05")
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "mid-conversation-system-2026-04-07")
		assert.Contains(t, claudeReq.Header.Get("anthropic-beta"), "effort-2025-11-24")
		assert.Equal(t, "0.94.0", claudeReq.Header.Get("X-Stainless-Package-Version"))
		assert.Equal(t, "v24.3.0", claudeReq.Header.Get("X-Stainless-Runtime-Version"))
		assert.Equal(t, "2023-06-01", claudeReq.Header.Get("anthropic-version"))
	})

	t.Run("codex preset preserves existing runtime identity values", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5","client_metadata":{"custom":"client"}}`)))
		req.Header.Set("Session_id", "client-session")
		req.Header.Set("Session-Id", "client-session-hyphen")
		req.Header.Set("Thread-Id", "client-thread")
		req.Header.Set("x-client-request-id", "client-request")
		req.Header.Set("X-Codex-Installation-Id", "client-installation")
		req.Header.Set("X-Codex-Window-Id", "client-window")
		req.Header.Set("X-Codex-Turn-Metadata", `{"source":"client"}`)
		req.Header.Set("X-Codex-Beta-Features", "client-beta")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		assert.Equal(t, "client-session", req.Header.Get("Session_id"))
		assert.Equal(t, "client-session-hyphen", req.Header.Get("Session-Id"))
		assert.Equal(t, "client-thread", req.Header.Get("Thread-Id"))
		assert.Equal(t, "client-thread", req.Header.Get("x-client-request-id"))
		assert.Equal(t, "client-installation", req.Header.Get("X-Codex-Installation-Id"))
		assert.Equal(t, "client-window", req.Header.Get("X-Codex-Window-Id"))
		assert.Equal(t, "client-beta", req.Header.Get("X-Codex-Beta-Features"))
		var turnMetadata map[string]any
		assert.NoError(t, json.Unmarshal([]byte(req.Header.Get("X-Codex-Turn-Metadata")), &turnMetadata))
		assert.Equal(t, "client", turnMetadata["source"])
		assert.Equal(t, "client-installation", turnMetadata["installation_id"])
		assert.Equal(t, "client-session-hyphen", turnMetadata["session_id"])
		assert.Equal(t, "client-thread", turnMetadata["thread_id"])
		assert.Equal(t, "client-window", turnMetadata["window_id"])
		body, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(body, &payload))
		metadata, ok := payload["client_metadata"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, "client", metadata["custom"])
			assert.Equal(t, "client-session-hyphen", metadata["session_id"])
			assert.Equal(t, "client-thread", metadata["thread_id"])
		}
	})

	t.Run("codex preset preserves large turn metadata numbers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))
		req.Header.Set("X-Codex-Turn-Metadata", `{"sequence":9007199254740993}`)

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		decoder := json.NewDecoder(bytes.NewBufferString(req.Header.Get("X-Codex-Turn-Metadata")))
		decoder.UseNumber()
		var turnMetadata map[string]any
		if assert.NoError(t, decoder.Decode(&turnMetadata)) {
			assert.Equal(t, json.Number("9007199254740993"), turnMetadata["sequence"])
		}
	})

	t.Run("codex preset ignores null turn metadata", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))
		req.Header.Set("X-Codex-Turn-Metadata", "null")

		assert.NotPanics(t, func() {
			applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
				"simulated_client": "codex",
			}}, false)
		})
		assert.NotEmpty(t, req.Header.Get("X-Codex-Turn-Metadata"))
	})

	t.Run("codex preset synthesizes consistent session routing identity", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		assert.NotEmpty(t, req.Header.Get("Session-Id"))
		assert.NotEmpty(t, req.Header.Get("Thread-Id"))
		assert.Equal(t, req.Header.Get("Thread-Id"), req.Header.Get("x-client-request-id"))
		assert.NotEmpty(t, req.Header.Get("X-Codex-Window-Id"))
		assert.NotEmpty(t, req.Header.Get("X-Codex-Turn-Metadata"))
		assert.Empty(t, req.Header.Get("X-Codex-Beta-Features"))
	})

	t.Run("claude code preset adds required messages identity", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, int64(len(got)), req.ContentLength)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(got, &payload))
		metadata, ok := payload["metadata"].(map[string]any)
		if assert.True(t, ok) {
			userID, ok := metadata["user_id"].(string)
			if assert.True(t, ok) {
				var userIDPayload map[string]string
				assert.NoError(t, json.Unmarshal([]byte(userID), &userIDPayload))
				assert.NotEmpty(t, userIDPayload["device_id"])
				assert.NotEmpty(t, userIDPayload["session_id"])
				assert.Equal(t, userIDPayload["session_id"], req.Header.Get("X-Claude-Code-Session-Id"))
			}
		}
		system, ok := payload["system"].([]any)
		if assert.True(t, ok) && assert.NotEmpty(t, system) {
			first, ok := system[0].(map[string]any)
			if assert.True(t, ok) {
				assert.Equal(t, "text", first["type"])
				assert.Contains(t, first["text"], "Claude Code")
			}
		}
		if assert.NotNil(t, req.GetBody) {
			bodyCopy, getBodyErr := req.GetBody()
			if assert.NoError(t, getBodyErr) {
				defer bodyCopy.Close()
				copied, readErr := io.ReadAll(bodyCopy)
				assert.NoError(t, readErr)
				assert.Equal(t, got, copied)
			}
		}
	})

	t.Run("claude code preset preserves valid messages identities without duplicate system prompt", func(t *testing.T) {
		const headerSessionID = "87654321-4321-4321-4321-cba987654321"
		const userID = `{"device_id":"client-device","account_uuid":"client-account","session_id":"12345678-1234-1234-1234-123456789abc"}`
		body, err := json.Marshal(map[string]any{
			"model":    "claude-sonnet-4-5",
			"metadata": map[string]any{"user_id": userID, "custom": "client"},
			"system":   []any{map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}},
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		})
		assert.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		req.Header.Set("X-Claude-Code-Session-Id", headerSessionID)

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(got, &payload))
		metadata, ok := payload["metadata"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, userID, metadata["user_id"])
			assert.Equal(t, "client", metadata["custom"])
		}
		system, ok := payload["system"].([]any)
		assert.True(t, ok)
		assert.Len(t, system, 1)
		assert.Equal(t, headerSessionID, req.Header.Get("X-Claude-Code-Session-Id"))
	})

	t.Run("claude code preset preserves legacy identity with empty account", func(t *testing.T) {
		const sessionID = "12345678-1234-1234-1234-123456789abc"
		const userID = "user_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef_account__session_" + sessionID
		body, err := json.Marshal(map[string]any{
			"model":    "claude-sonnet-4-5",
			"metadata": map[string]any{"user_id": userID},
			"system":   []any{map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}},
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		})
		assert.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		req.Header.Set("X-Claude-Code-Session-Id", sessionID)

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client":              "claude_code",
			"simulated_claude_code_version": "2.1.77",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(got, &payload))
		metadata, ok := payload["metadata"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, userID, metadata["user_id"])
		}
	})

	t.Run("claude code preset safely encodes explicit session header", func(t *testing.T) {
		const sessionID = `session-"quoted"}`
		body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		req.Header.Set("X-Claude-Code-Session-Id", sessionID)

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(got, &payload))
		metadata, ok := payload["metadata"].(map[string]any)
		if assert.True(t, ok) {
			userID, ok := metadata["user_id"].(string)
			if assert.True(t, ok) {
				var userIDPayload map[string]string
				if assert.NoError(t, json.Unmarshal([]byte(userID), &userIDPayload)) {
					assert.Equal(t, sessionID, userIDPayload["session_id"])
				}
			}
		}
	})

	t.Run("claude code preset leaves non-messages body unchanged", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})

	t.Run("codex preset leaves compact body unchanged", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})

	t.Run("codex preset leaves non-responses body unchanged for responses channel", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","input":"hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{
			ChannelType: "openai-response",
			Config:      datatypes.JSONMap{"simulated_client": "codex"},
		}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})

	t.Run("claude code preset leaves non-messages body unchanged for anthropic channel", func(t *testing.T) {
		body := []byte(`{"model":"claude-2","prompt":"hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/complete", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{
			ChannelType: "anthropic",
			Config:      datatypes.JSONMap{"simulated_client": "claude_code"},
		}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})

	t.Run("claude code preset leaves count tokens body unchanged", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})

	t.Run("claude code preset leaves messages subresource body unchanged", func(t *testing.T) {
		body := []byte(`{"requests":[{"custom_id":"batch-1","params":{"model":"claude-sonnet-4-5"}}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages/batches", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{
			ChannelType: "anthropic",
			Config: datatypes.JSONMap{
				"simulated_client": "claude_code",
			},
		}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})

	t.Run("target endpoints preserve invalid json replay body", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			mode string
			path string
		}{
			{name: "codex", mode: "codex", path: "/v1/responses"},
			{name: "claude_code", mode: "claude_code", path: "/v1/messages"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				body := []byte(`not-json`)
				req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(body))
				req.GetBody = nil

				applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
					"simulated_client": tt.mode,
				}}, false)

				got, err := io.ReadAll(req.Body)
				assert.NoError(t, err)
				assert.Equal(t, body, got)
				if assert.NotNil(t, req.GetBody) {
					replayBody, getBodyErr := req.GetBody()
					if assert.NoError(t, getBodyErr) {
						defer replayBody.Close()
						replayed, readErr := io.ReadAll(replayBody)
						assert.NoError(t, readErr)
						assert.Equal(t, body, replayed)
					}
				}
			})
		}
	})

	t.Run("target endpoints resync consumed body from get body", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			mode string
			path string
		}{
			{name: "codex", mode: "codex", path: "/v1/responses"},
			{name: "claude_code", mode: "claude_code", path: "/v1/messages"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				body := []byte(`not-json`)
				req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(body))
				req.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(body)), nil
				}
				req.ContentLength = -1
				_, err := io.ReadAll(req.Body)
				assert.NoError(t, err)

				applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
					"simulated_client": tt.mode,
				}}, false)

				got, err := io.ReadAll(req.Body)
				assert.NoError(t, err)
				assert.Equal(t, body, got)
				assert.Equal(t, int64(len(body)), req.ContentLength)
			})
		}
	})

	t.Run("codex body rewrite preserves large integers", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5","input":"hello","request_id":9007199254740993}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		decoder := json.NewDecoder(bytes.NewReader(got))
		decoder.UseNumber()
		var payload map[string]any
		assert.NoError(t, decoder.Decode(&payload))
		assert.Equal(t, json.Number("9007199254740993"), payload["request_id"])
	})
}

func TestShouldRemoveAcceptEncodingForProxyParsing(t *testing.T) {
	t.Run("plain passthrough keeps accept encoding", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/proxy/test/v1/chat/completions", nil)
		group := &models.Group{}
		assert.False(t, shouldRemoveAcceptEncodingForProxyParsing(c, group))
	})

	t.Run("models enhancement removes accept encoding", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/proxy/test/v1/models", nil)
		group := &models.Group{ModelMappingCache: map[string]string{"a": "b"}}
		assert.True(t, shouldRemoveAcceptEncodingForProxyParsing(c, group))
	})

	t.Run("function call conversion removes accept encoding", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/proxy/test/v1/chat/completions", nil)
		c.Set(ctxKeyFunctionCallEnabled, true)
		group := &models.Group{}
		assert.True(t, shouldRemoveAcceptEncodingForProxyParsing(c, group))
	})

	t.Run("cc conversion removes accept encoding", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/proxy/test/claude/v1/messages", nil)
		c.Set("cc_enabled", true)
		group := &models.Group{}
		assert.True(t, shouldRemoveAcceptEncodingForProxyParsing(c, group))
	})

	t.Run("forced stream collection removes accept encoding", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/proxy/test/v1/responses", nil)
		c.Set(ctxKeyOpenAIResponseForcedStream, true)
		group := &models.Group{}
		assert.True(t, shouldRemoveAcceptEncodingForProxyParsing(c, group))
	})
}

func TestRemoveAcceptEncodingForProxyParsing(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/proxy/test/v1/responses", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	c.Set(ctxKeyOpenAIResponseForcedStream, true)

	removeAcceptEncodingForProxyParsing(req, c, &models.Group{})
	assert.Empty(t, req.Header.Get("Accept-Encoding"))
}

func TestApplyResponsesIncludeConfig(t *testing.T) {
	ps := &ProxyServer{}

	t.Run("add encrypted reasoning include", func(t *testing.T) {
		group := &models.Group{
			ChannelType: "openai-response",
			Config:      datatypes.JSONMap{"responses_include_encrypted_reasoning": true},
		}
		input := []byte(`{"model":"gpt-5","include":["web_search_call.action.sources"]}`)

		result, err := ps.applyResponsesIncludeConfig(input, group)
		assert.NoError(t, err)

		var resultData map[string]any
		assert.NoError(t, json.Unmarshal(result, &resultData))
		include, ok := resultData["include"].([]any)
		if assert.True(t, ok) {
			assert.Contains(t, include, "web_search_call.action.sources")
			assert.Contains(t, include, responsesEncryptedReasoning)
		}
	})

	t.Run("does not duplicate existing include", func(t *testing.T) {
		group := &models.Group{
			ChannelType: "openai-response",
			Config:      datatypes.JSONMap{"responses_include_encrypted_reasoning": true},
		}
		input := []byte(`{"model":"gpt-5","include":["reasoning.encrypted_content"]}`)

		result, err := ps.applyResponsesIncludeConfig(input, group)
		assert.NoError(t, err)

		var resultData map[string]any
		assert.NoError(t, json.Unmarshal(result, &resultData))
		include, ok := resultData["include"].([]any)
		if assert.True(t, ok) {
			count := 0
			for _, item := range include {
				if item == responsesEncryptedReasoning {
					count++
				}
			}
			assert.Equal(t, 1, count)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{}}
		input := []byte(`{"model":"gpt-5"}`)

		result, err := ps.applyResponsesIncludeConfig(input, group)
		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("non responses group ignored", func(t *testing.T) {
		group := &models.Group{
			ChannelType: "openai",
			Config:      datatypes.JSONMap{"responses_include_encrypted_reasoning": true},
		}
		input := []byte(`{"model":"gpt-5"}`)

		result, err := ps.applyResponsesIncludeConfig(input, group)
		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})
}

func TestApplyGeminiNativeStreamPathOverride(t *testing.T) {
	assert.Equal(
		t,
		"/v1beta/models/gemini-pro:streamGenerateContent",
		applyGeminiNativeStreamPathOverride("/v1beta/models/gemini-pro:generateContent", true, false),
	)
	assert.Equal(
		t,
		"/v1beta/models/gemini-pro:generateContent",
		applyGeminiNativeStreamPathOverride("/v1beta/models/gemini-pro:streamGenerateContent", false, true),
	)
	assert.Equal(
		t,
		"/v1beta/openai/chat/completions",
		applyGeminiNativeStreamPathOverride("/v1beta/openai/chat/completions", true, false),
	)
	assert.Equal(
		t,
		"/v1beta/models/gemini-pro:generateContent",
		applyGeminiNativeStreamPathOverride("/v1beta/models/gemini-pro:generateContent", true, true),
	)
	assert.True(t, isGeminiNativeGenerateContentPath("/v1beta/models/gemini-pro:generateContent"))
	assert.True(t, isGeminiNativeGenerateContentPath("/v1beta/models/gemini-pro:streamGenerateContent"))
	assert.False(t, isGeminiNativeGenerateContentPath("/v1beta/openai/chat/completions"))
}

func TestLogUpstreamError(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		// Should not panic
		assert.NotPanics(t, func() {
			logUpstreamError("test context", nil)
		})
	})

	t.Run("non-nil error", func(t *testing.T) {
		// Should not panic
		assert.NotPanics(t, func() {
			logUpstreamError("test context", assert.AnError)
		})
	})
}

func TestHandleGzipCompression(t *testing.T) {
	t.Run("returns input unchanged", func(t *testing.T) {
		input := []byte("test data")
		result := handleGzipCompression(nil, input)
		assert.Equal(t, input, result)
	})
}

func TestModelListRedirectLogModels(t *testing.T) {
	t.Run("nil group", func(t *testing.T) {
		model, mappedModel := modelListRedirectLogModels(nil)
		assert.Empty(t, model)
		assert.Empty(t, mappedModel)
	})

	t.Run("no redirect rules", func(t *testing.T) {
		model, mappedModel := modelListRedirectLogModels(&models.Group{})
		assert.Empty(t, model)
		assert.Empty(t, mappedModel)
	})

	t.Run("v1 single mapping", func(t *testing.T) {
		group := &models.Group{
			ModelRedirectMap: map[string]string{
				"medreview": "deepseek-expert",
			},
		}

		model, mappedModel := modelListRedirectLogModels(group)
		assert.Equal(t, "deepseek-expert", model)
		assert.Equal(t, "medreview", mappedModel)
	})

	t.Run("v1 stable multiple mappings", func(t *testing.T) {
		group := &models.Group{
			ModelRedirectMap: map[string]string{
				"alias-b": "target-b",
				"alias-a": "target-a",
			},
		}

		model, mappedModel := modelListRedirectLogModels(group)
		assert.Equal(t, "target-a, target-b", model)
		assert.Equal(t, "alias-a, alias-b", mappedModel)
	})

	t.Run("v2 ignores v1 and filters disabled targets", func(t *testing.T) {
		enabled := true
		disabled := false
		group := &models.Group{
			ModelRedirectMap: map[string]string{
				"legacy": "legacy-target",
			},
			ModelRedirectMapV2: map[string]*models.ModelRedirectRuleV2{
				"medreview": {
					Targets: []models.ModelRedirectTarget{
						{Model: "deepseek-expert", Enabled: &enabled},
						{Model: ""},
						{Model: "deepseek-disabled", Enabled: &disabled},
						{Model: "deepseek-expert", Enabled: &enabled},
					},
				},
			},
		}

		model, mappedModel := modelListRedirectLogModels(group)
		assert.Equal(t, "deepseek-expert", model)
		assert.Equal(t, "medreview", mappedModel)
	})

	t.Run("v2 skips sources without valid targets", func(t *testing.T) {
		disabled := false
		group := &models.Group{
			ModelRedirectMapV2: map[string]*models.ModelRedirectRuleV2{
				"alias-a": {
					Targets: []models.ModelRedirectTarget{
						{Model: "disabled-target", Enabled: &disabled},
						{Model: ""},
					},
				},
				"alias-b": {
					Targets: []models.ModelRedirectTarget{
						{Model: "target-b"},
					},
				},
			},
		}

		model, mappedModel := modelListRedirectLogModels(group)
		assert.Equal(t, "target-b", model)
		assert.Equal(t, "alias-b", mappedModel)
	})
}
