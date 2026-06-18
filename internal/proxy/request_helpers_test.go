package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/models"

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
		assert.False(t, allowsMissingStreamOverride("/v1/custom", http.MethodPost))
		assert.False(t, allowsMissingStreamOverride("/v1/chat/completions", http.MethodGet))
	})
}

func TestApplySimulatedClientHeaders(t *testing.T) {
	t.Run("no config preserves client headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("User-Agent", "custom-client/1.0")
		req.Header.Set("OpenAI-Beta", "custom-beta")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{}}, false)

		assert.Equal(t, "custom-client/1.0", req.Header.Get("User-Agent"))
		assert.Equal(t, "custom-beta", req.Header.Get("OpenAI-Beta"))
	})

	t.Run("codex preset sets client fingerprint without touching auth headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer upstream-key")
		req.Header.Set("x-api-key", "upstream-key")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, true)

		assert.Equal(t, buildCodexUserAgent(channel.DefaultCodexVersion), req.Header.Get("User-Agent"))
		assert.Equal(t, channel.DefaultCodexVersion, req.Header.Get("Version"))
		assert.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
		assert.Equal(t, "codex_cli_rs", req.Header.Get("originator"))
		assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer upstream-key", req.Header.Get("Authorization"))
		assert.Equal(t, "upstream-key", req.Header.Get("x-api-key"))
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
		assert.Empty(t, req.Header.Get("X-Claude-Code-Session-Id"))
	})

	t.Run("custom client versions override default user agents", func(t *testing.T) {
		codexReq := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		applySimulatedClientHeaders(codexReq, &models.Group{Config: datatypes.JSONMap{
			"simulated_client":        "codex",
			"simulated_codex_version": "0.150.1",
		}}, false)
		codexUA := codexReq.Header.Get("User-Agent")
		assert.Equal(t, buildCodexUserAgent("0.150.1"), codexUA)
		assert.Equal(t, 2, strings.Count(codexUA, "0.150.1"))
		assert.Equal(t, "0.150.1", codexReq.Header.Get("Version"))
		assert.Equal(t, "codex_cli_rs", codexReq.Header.Get("originator"))

		claudeReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		claudeReq.Header.Set("anthropic-beta", "custom-beta,claude-code-20250219")
		applySimulatedClientHeaders(claudeReq, &models.Group{Config: datatypes.JSONMap{
			"simulated_client":              "claude_code",
			"simulated_claude_code_version": "2.2.0",
		}}, false)
		assert.Equal(t, buildClaudeCodeUserAgent("2.2.0"), claudeReq.Header.Get("User-Agent"))
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

	t.Run("codex preset preserves existing runtime trace headers and does not synthesize missing ones", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Session_id", "client-session")
		req.Header.Set("X-Codex-Turn-Metadata", `{"source":"client"}`)
		req.Header.Set("X-Codex-Beta-Features", "client-beta")

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "codex",
		}}, false)

		assert.Equal(t, "client-session", req.Header.Get("Session_id"))
		assert.Equal(t, `{"source":"client"}`, req.Header.Get("X-Codex-Turn-Metadata"))
		assert.Equal(t, "client-beta", req.Header.Get("X-Codex-Beta-Features"))
		assert.Empty(t, req.Header.Get("x-client-request-id"))
		assert.Empty(t, req.Header.Get("x-codex-window-id"))
	})

	t.Run("does not modify request body", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"client-user"},"messages":[{"role":"user","content":"hello"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

		applySimulatedClientHeaders(req, &models.Group{Config: datatypes.JSONMap{
			"simulated_client": "claude_code",
		}}, false)

		got, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, got)
	})
}

func TestApplyResponsesIncludeConfig(t *testing.T) {
	ps := &ProxyServer{}

	t.Run("add encrypted reasoning include", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"responses_include_encrypted_reasoning": true}}
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
		group := &models.Group{Config: datatypes.JSONMap{"responses_include_encrypted_reasoning": true}}
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
