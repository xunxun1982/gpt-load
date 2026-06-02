package proxy

import (
	"encoding/json"
	"testing"

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

		result, err := ps.applyStreamOverrideConfig(input, group)
		assert.NoError(t, err)

		var resultData map[string]any
		assert.NoError(t, json.Unmarshal(result, &resultData))
		assert.Equal(t, true, resultData["stream"])
	})

	t.Run("force non stream", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"force_non_stream": true}}
		input := []byte(`{"model":"gpt-4","stream":true}`)

		result, err := ps.applyStreamOverrideConfig(input, group)
		assert.NoError(t, err)

		var resultData map[string]any
		assert.NoError(t, json.Unmarshal(result, &resultData))
		assert.Equal(t, false, resultData["stream"])
	})

	t.Run("no config", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{}}
		input := []byte(`{"model":"gpt-4"}`)

		result, err := ps.applyStreamOverrideConfig(input, group)
		assert.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("conflicting config", func(t *testing.T) {
		group := &models.Group{Config: datatypes.JSONMap{"force_stream": true, "force_non_stream": true}}
		input := []byte(`{"model":"gpt-4","stream":false}`)

		result, err := ps.applyStreamOverrideConfig(input, group)
		assert.NoError(t, err)
		assert.Equal(t, input, result)
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
