package handler

import (
	"net/http/httptest"
	"testing"

	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

func TestConvertModelRedirectRulesToExport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    datatypes.JSONMap
		expected map[string]string
	}{
		{
			name:     "nil_input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty_input",
			input:    datatypes.JSONMap{},
			expected: nil,
		},
		{
			name: "valid_rules",
			input: datatypes.JSONMap{
				"gpt-4": "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
			},
			expected: map[string]string{
				"gpt-4": "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
			},
		},
		{
			name: "mixed_types_filtered",
			input: datatypes.JSONMap{
				"valid": "model-name",
				"invalid": 123,
			},
			expected: map[string]string{
				"valid": "model-name",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ConvertModelRedirectRulesToExport(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertModelRedirectRulesToImport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    map[string]string
		expected datatypes.JSONMap
	}{
		{
			name:     "nil_input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty_input",
			input:    map[string]string{},
			expected: nil,
		},
		{
			name: "valid_rules",
			input: map[string]string{
				"gpt-4": "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
			},
			expected: datatypes.JSONMap{
				"gpt-4": "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ConvertModelRedirectRulesToImport(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParsePathRedirectsForExport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []byte
		expected []models.PathRedirectRule
	}{
		{
			name:     "nil_input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty_input",
			input:    []byte{},
			expected: nil,
		},
		{
			name:     "invalid_json",
			input:    []byte("invalid json"),
			expected: nil,
		},
		{
			name:  "valid_rules",
			input: []byte(`[{"from":"/v1/messages","to":"/v1/chat/completions"}]`),
			expected: []models.PathRedirectRule{
				{From: "/v1/messages", To: "/v1/chat/completions"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ParsePathRedirectsForExport(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertPathRedirectsToJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []models.PathRedirectRule
		expected string
	}{
		{
			name:     "nil_input",
			input:    nil,
			expected: "",
		},
		{
			name:     "empty_input",
			input:    []models.PathRedirectRule{},
			expected: "",
		},
		{
			name: "valid_rules",
			input: []models.PathRedirectRule{
				{From: "/v1/messages", To: "/v1/chat/completions"},
			},
			expected: `[{"from":"/v1/messages","to":"/v1/chat/completions"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ConvertPathRedirectsToJSON(tt.input)
			if tt.expected == "" {
				assert.Nil(t, result)
			} else {
				assert.JSONEq(t, tt.expected, string(result))
			}
		})
	}
}

func TestParseHeaderRulesForExport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []byte
		groupID  uint
		expected []models.HeaderRule
	}{
		{
			name:     "nil_input",
			input:    nil,
			groupID:  1,
			expected: []models.HeaderRule{},
		},
		{
			name:     "empty_input",
			input:    []byte{},
			groupID:  1,
			expected: []models.HeaderRule{},
		},
		{
			name:     "invalid_json",
			input:    []byte("invalid"),
			groupID:  1,
			expected: []models.HeaderRule{},
		},
		{
			name:    "valid_rules",
			input:   []byte(`[{"key":"Authorization","value":"Bearer token","action":"set"}]`),
			groupID: 1,
			expected: []models.HeaderRule{
				{Key: "Authorization", Value: "Bearer token", Action: "set"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ParseHeaderRulesForExport(tt.input, tt.groupID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertHeaderRulesToJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []models.HeaderRule
		expected string
	}{
		{
			name:     "nil_input",
			input:    nil,
			expected: "null",
		},
		{
			name:     "empty_input",
			input:    []models.HeaderRule{},
			expected: "[]",
		},
		{
			name: "valid_rules",
			input: []models.HeaderRule{
				{Key: "Authorization", Value: "Bearer token", Action: "set"},
			},
			expected: `[{"key":"Authorization","value":"Bearer token","action":"set"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ConvertHeaderRulesToJSON(tt.input)
			// ConvertHeaderRulesToJSON always returns valid JSON, never nil
			if string(result) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, string(result))
			}
		})
	}
}

func TestGetExportMode(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "no_query",
			query:    "",
			expected: "encrypted",
		},
		{
			name:     "mode_plain",
			query:    "?mode=plain",
			expected: "plain",
		},
		{
			name:     "mode_encrypted",
			query:    "?mode=encrypted",
			expected: "encrypted",
		},
		{
			name:     "export_mode_plain",
			query:    "?export_mode=plain",
			expected: "plain",
		},
		{
			name:     "invalid_mode",
			query:    "?mode=invalid",
			expected: "encrypted",
		},
		{
			name:     "case_insensitive",
			query:    "?mode=PLAIN",
			expected: "plain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/test"+tt.query, nil)

			result := GetExportMode(c)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetImportMode(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		query      string
		sampleKeys []string
		expected   string
	}{
		{
			name:       "explicit_plain",
			query:      "?mode=plain",
			sampleKeys: []string{},
			expected:   "plain",
		},
		{
			name:       "explicit_encrypted",
			query:      "?mode=encrypted",
			sampleKeys: []string{},
			expected:   "encrypted",
		},
		{
			name:       "filename_plain",
			query:      "?filename=export-plain.json",
			sampleKeys: []string{},
			expected:   "plain",
		},
		{
			name:       "filename_encrypted",
			query:      "?filename=export-enc.json",
			sampleKeys: []string{},
			expected:   "encrypted",
		},
		{
			name:       "heuristic_plain",
			query:      "",
			sampleKeys: []string{"sk-1234567890", "sk-abcdefghij"},
			expected:   "plain",
		},
		{
			name:       "heuristic_encrypted",
			query:      "",
			sampleKeys: []string{"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", "1234567890abcdef1234567890abcdef"},
			expected:   "encrypted",
		},
		{
			name:       "empty_samples_default_plain",
			query:      "",
			sampleKeys: []string{},
			expected:   "plain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/test"+tt.query, nil)

			result := GetImportMode(c, tt.sampleKeys)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLooksLikeHex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "empty_string",
			input:    "",
			expected: false,
		},
		{
			name:     "too_short",
			input:    "abc",
			expected: false,
		},
		{
			name:     "odd_length",
			input:    "a1b2c3d4e5f6a1b",
			expected: false,
		},
		{
			name:     "valid_hex",
			input:    "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			expected: true,
		},
		{
			name:     "invalid_chars",
			input:    "sk-1234567890abcdefghij",
			expected: false,
		},
		{
			name:     "valid_long_hex",
			input:    "1234567890abcdef1234567890abcdef1234567890abcdef",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := looksLikeHex(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Benchmark tests
func BenchmarkConvertModelRedirectRulesToExport(b *testing.B) {
	input := datatypes.JSONMap{
		"gpt-4": "gpt-4-turbo",
		"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
		"claude-3": "claude-3-opus",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ConvertModelRedirectRulesToExport(input)
	}
}

func BenchmarkLooksLikeHex(b *testing.B) {
	testStr := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = looksLikeHex(testStr)
	}
}

func BenchmarkParsePathRedirectsForExport(b *testing.B) {
	input := []byte(`[{"from":"/v1/messages","to":"/v1/chat/completions"},{"from":"/v1/completions","to":"/v1/chat/completions"}]`)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ParsePathRedirectsForExport(input)
	}
}
