package proxy

import (
	"testing"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/assert"
)

func TestMin(t *testing.T) {
	tests := []struct {
		name     string
		a, b     int
		expected int
	}{
		{"a smaller", 1, 2, 1},
		{"b smaller", 5, 3, 3},
		{"equal", 4, 4, 4},
		{"negative", -1, 2, -1},
		{"both negative", -5, -3, -5},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable for parallel subtests
		t.Run(tt.name, func(t *testing.T) {
			result := min(tt.a, tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsModelsEndpoint(t *testing.T) {
	ps := &ProxyServer{}

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"v1 models", "/v1/models", true},
		{"v1 models with trailing slash", "/v1/models/", true},
		{"v1beta models", "/v1beta/models", true},
		{"root models", "/models", true},
		{"chat completions", "/v1/chat/completions", false},
		{"embeddings", "/v1/embeddings", false},
		{"empty path", "", false},
		{"models in middle", "/api/models/list", false},
	}

	for _, tt := range tests {
		tt := tt // Capture range variable for parallel subtests
		t.Run(tt.name, func(t *testing.T) {
			result := ps.isModelsEndpoint(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEnhanceModelsResponse(t *testing.T) {
	ps := &ProxyServer{}

	t.Run("OpenAI format - add aliases", func(t *testing.T) {
		group := &models.Group{
			Name: "test-group",
			ModelMappingCache: map[string]string{
				"alias-model-1": "real-model-1",
				"alias-model-2": "real-model-2",
			},
		}

		input := `{"data":[{"id":"existing-model","object":"model"}],"object":"list"}`
		result, err := ps.enhanceModelsResponse([]byte(input), group)

		assert.NoError(t, err)
		assert.Contains(t, string(result), "alias-model-1")
		assert.Contains(t, string(result), "alias-model-2")
		assert.Contains(t, string(result), "existing-model")
	})

	t.Run("OpenAI format - skip existing aliases", func(t *testing.T) {
		group := &models.Group{
			Name: "test-group",
			ModelMappingCache: map[string]string{
				"existing-model": "real-model",
			},
		}

		input := `{"data":[{"id":"existing-model","object":"model"}],"object":"list"}`
		result, err := ps.enhanceModelsResponse([]byte(input), group)

		assert.NoError(t, err)
		// Should not duplicate existing model
		assert.Equal(t, 1, countOccurrences(string(result), "existing-model"))
	})

	t.Run("Gemini format - add aliases", func(t *testing.T) {
		group := &models.Group{
			Name: "test-group",
			ModelMappingCache: map[string]string{
				"alias-model": "real-model",
			},
		}

		input := `{"models":[{"name":"existing-model","display_name":"Existing"}]}`
		result, err := ps.enhanceModelsResponse([]byte(input), group)

		assert.NoError(t, err)
		assert.Contains(t, string(result), "alias-model")
		assert.Contains(t, string(result), "existing-model")
	})

	t.Run("invalid JSON", func(t *testing.T) {
		group := &models.Group{
			Name:              "test-group",
			ModelMappingCache: map[string]string{"alias": "real"},
		}

		input := `{invalid json`
		result, err := ps.enhanceModelsResponse([]byte(input), group)

		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("unknown format", func(t *testing.T) {
		group := &models.Group{
			Name:              "test-group",
			ModelMappingCache: map[string]string{"alias": "real"},
		}

		input := `{"unknown_field":[{"id":"model"}]}`
		result, err := ps.enhanceModelsResponse([]byte(input), group)

		assert.NoError(t, err)
		// Should return original when format is unknown
		assert.Equal(t, input, string(result))
	})

	t.Run("empty mapping cache", func(t *testing.T) {
		group := &models.Group{
			Name:              "test-group",
			ModelMappingCache: map[string]string{},
		}

		input := `{"data":[{"id":"model"}]}`
		result, err := ps.enhanceModelsResponse([]byte(input), group)

		assert.NoError(t, err)
		// Should return original when no mappings
		assert.Equal(t, input, string(result))
	})
}

// Helper function to count occurrences of a substring
func countOccurrences(s, substr string) int {
	count := 0
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			count++
		}
	}
	return count
}
