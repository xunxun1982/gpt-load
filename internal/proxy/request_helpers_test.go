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
		json.Unmarshal(result, &resultData)
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
		json.Unmarshal(result, &resultData)
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
		json.Unmarshal(result, &resultData)
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
		json.Unmarshal(result, &resultData)
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
