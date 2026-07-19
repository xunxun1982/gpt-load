package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/models"
	"gpt-load/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				"gpt-4":         "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
			},
			expected: map[string]string{
				"gpt-4":         "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
			},
		},
		{
			name: "mixed_types_filtered",
			input: datatypes.JSONMap{
				"valid":   "model-name",
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
				"gpt-4":         "gpt-4-turbo",
				"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
			},
			expected: datatypes.JSONMap{
				"gpt-4":         "gpt-4-turbo",
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

func TestConvertChildGroupsForExportAndImport(t *testing.T) {
	t.Parallel()

	source := []services.ChildGroupExport{
		{
			Name:               "child-group",
			DisplayName:        "Child Group",
			Description:        "Child description",
			Enabled:            true,
			ProxyKeys:          "proxy-key",
			Sort:               7,
			TestModel:          "gpt-4o-mini",
			Config:             json.RawMessage(`{"base_url":"https://child.example.com"}`),
			HeaderRules:        json.RawMessage(`[{"key":"X-Test","value":"ok"}]`),
			ModelRedirectRules: json.RawMessage(`{"gpt-4":"gpt-4o"}`),
			Keys: []services.KeyExportInfo{
				{KeyValue: "encrypted-key", Status: models.KeyStatusActive},
			},
		},
	}

	exported, err := ConvertChildGroupsForExport(source, "plain", func(value string) (string, error) {
		return "plain-" + value, nil
	})
	require.NoError(t, err)
	assert.Len(t, exported, 1)
	assert.Equal(t, "child-group", exported[0].Name)
	assert.Equal(t, "plain-encrypted-key", exported[0].Keys[0].KeyValue)
	assert.Equal(t, map[string]string{"gpt-4": "gpt-4o"}, exported[0].ModelRedirectRules)
	assert.Equal(t, map[string]any{"base_url": "https://child.example.com"}, exported[0].Config)

	imported := ConvertChildGroupsForImport(exported, true, func(value string) (string, error) {
		return "encrypted-" + value, nil
	})
	assert.Len(t, imported, 1)
	assert.Equal(t, "child-group", imported[0].Name)
	assert.JSONEq(t, `{"base_url":"https://child.example.com"}`, string(imported[0].Config))
	assert.JSONEq(t, `{"gpt-4":"gpt-4o"}`, string(imported[0].ModelRedirectRules))
	assert.Equal(t, "encrypted-plain-encrypted-key", imported[0].Keys[0].KeyValue)
}

func TestSanitizeProxyFieldsForEncryptedGroupExport(t *testing.T) {
	t.Parallel()

	const proxyURL = "http://user:pass@proxy.example.test:8080"
	group := GroupExportInfo{
		Upstreams: json.RawMessage(`[{"url":"https://api.example.com","weight":100,"proxy_url":"` + proxyURL + `"}]`),
		Config: map[string]any{
			"proxy_url": proxyURL,
			"keep":      "value",
		},
	}

	require.NoError(t, sanitizeGroupProxyFieldsForExport(&group, false))
	assert.NotContains(t, string(group.Upstreams), proxyURL)
	assert.JSONEq(t, `[{"url":"https://api.example.com","weight":100,"proxy_url":""}]`, string(group.Upstreams))
	assert.Empty(t, group.Config["proxy_url"])
	assert.Equal(t, "value", group.Config["keep"])

	escaped := GroupExportInfo{
		Upstreams: json.RawMessage(`[{"url":"https://api.example.com","weight":100,"\u0070roxy_url":"` + proxyURL + `"}]`),
	}
	require.NoError(t, sanitizeGroupProxyFieldsForExport(&escaped, false))
	assert.NotContains(t, string(escaped.Upstreams), proxyURL)
	caseVariant := GroupExportInfo{
		Upstreams: json.RawMessage(`[{"url":"https://api.example.com","weight":100,"PROXY_URL":"` + proxyURL + `","Proxy_URL":"` + proxyURL + `"}]`),
		Config:    map[string]any{"PROXY_URL": proxyURL},
	}
	require.NoError(t, sanitizeGroupProxyFieldsForExport(&caseVariant, false))
	assert.NotContains(t, string(caseVariant.Upstreams), proxyURL)
	assert.Empty(t, caseVariant.Config["PROXY_URL"])

	plain := GroupExportInfo{
		Upstreams: json.RawMessage(`[{"url":"https://api.example.com","weight":100,"proxy_url":"` + proxyURL + `"}]`),
		Config:    map[string]any{"proxy_url": proxyURL},
	}
	require.NoError(t, sanitizeGroupProxyFieldsForExport(&plain, true))
	assert.Contains(t, string(plain.Upstreams), proxyURL)
	assert.Equal(t, proxyURL, plain.Config["proxy_url"])
}

func TestSanitizeProxyFieldsForEncryptedGroupExportFailsClosed(t *testing.T) {
	t.Parallel()

	const proxyURL = "http://user:pass@proxy.example.test:8080"
	group := GroupExportInfo{
		Upstreams: json.RawMessage(`[{"proxy_url":"` + proxyURL + `"`),
	}

	err := sanitizeGroupProxyFieldsForExport(&group, false)
	require.ErrorIs(t, err, errEncryptedExportProxySanitization)
	assert.NotContains(t, err.Error(), proxyURL)
}

func TestSanitizeSystemSettingsForEncryptedExport(t *testing.T) {
	t.Parallel()

	const proxyURL = "http://user:pass@proxy.example.test:8080"
	settings := map[string]string{"PROXY_URL": proxyURL, "request_timeout": "60"}
	sanitized := sanitizeSystemSettingsForExport(settings, false)
	assert.Empty(t, sanitized["PROXY_URL"])
	assert.Equal(t, "60", sanitized["request_timeout"])
	assert.Equal(t, proxyURL, settings["PROXY_URL"])
	assert.Equal(t, proxyURL, sanitizeSystemSettingsForExport(settings, true)["PROXY_URL"])
}

func TestConvertChildGroupsForExportOnlyIncludesProxyConfigInPlainMode(t *testing.T) {
	t.Parallel()

	const proxyURL = "http://user:pass@proxy.example.test:8080"
	source := []services.ChildGroupExport{{
		Name:   "child-group",
		Config: json.RawMessage(`{"proxy_url":"` + proxyURL + `","keep":"value"}`),
	}}

	encrypted, err := ConvertChildGroupsForExport(source, "encrypted", nil)
	require.NoError(t, err)
	require.Len(t, encrypted, 1)
	assert.Empty(t, encrypted[0].Config["proxy_url"])
	assert.Equal(t, "value", encrypted[0].Config["keep"])

	plain, err := ConvertChildGroupsForExport(source, "plain", func(value string) (string, error) {
		return value, nil
	})
	require.NoError(t, err)
	require.Len(t, plain, 1)
	assert.Equal(t, proxyURL, plain[0].Config["proxy_url"])
}

func TestConvertChildGroupsForExportDoesNotReturnCiphertextOnDecryptFailure(t *testing.T) {
	t.Parallel()

	exported, err := ConvertChildGroupsForExport([]services.ChildGroupExport{{
		Name: "child-group",
		Keys: []services.KeyExportInfo{{
			KeyValue: "sensitive-invalid-ciphertext",
			Status:   models.KeyStatusActive,
		}},
	}}, "plain", func(string) (string, error) {
		return "", errors.New("forced decrypt failure")
	})

	require.Error(t, err)
	assert.Nil(t, exported)
	assert.NotContains(t, err.Error(), "sensitive-invalid-ciphertext")
	assert.NotContains(t, err.Error(), "forced decrypt failure")
}

func TestConvertGroupForImportPreservesChildGroups(t *testing.T) {
	t.Parallel()

	groupExport := GroupExportData{
		Group: GroupExportInfo{
			Name:        "parent",
			GroupType:   "standard",
			ChannelType: "openai",
			Enabled:     true,
			TestModel:   "gpt-4o-mini",
			Upstreams:   json.RawMessage(`[{"url":"https://parent.example.com","weight":1}]`),
		},
		Keys: []KeyExportInfo{
			{KeyValue: "parent-key", Status: models.KeyStatusActive},
		},
		ChildGroups: []ChildGroupExportInfo{
			{
				Name:               "child-a",
				DisplayName:        "Child A",
				Enabled:            true,
				Sort:               1,
				TestModel:          "child-a-model",
				Config:             map[string]any{"base_url": "https://child-a.example.com"},
				ModelRedirectRules: map[string]string{"gpt-4": "gpt-4o"},
				Keys: []KeyExportInfo{
					{KeyValue: "child-a-key", Status: models.KeyStatusInvalid},
				},
			},
			{
				Name:                 "child-b",
				DisplayName:          "Child B",
				Enabled:              false,
				Sort:                 2,
				TestModel:            "child-b-model",
				ModelRedirectRulesV2: json.RawMessage(`{"source":{"targets":[{"model":"target","weight":100}]}}`),
				Keys: []KeyExportInfo{
					{KeyValue: "child-b-key", Status: models.KeyStatusInvalid},
				},
			},
		},
	}

	converted := ConvertGroupForImport(groupExport, true, func(value string) (string, error) {
		return "encrypted-" + value, nil
	})
	require.Len(t, converted.Keys, 1)
	assert.Equal(t, "encrypted-parent-key", converted.Keys[0].KeyValue)
	require.Len(t, converted.ChildGroups, 2)
	assert.Equal(t, "child-a", converted.ChildGroups[0].Name)
	assert.Equal(t, "encrypted-child-a-key", converted.ChildGroups[0].Keys[0].KeyValue)
	assert.JSONEq(t, `{"base_url":"https://child-a.example.com"}`, string(converted.ChildGroups[0].Config))
	assert.JSONEq(t, `{"gpt-4":"gpt-4o"}`, string(converted.ChildGroups[0].ModelRedirectRules))
	assert.Equal(t, "child-b", converted.ChildGroups[1].Name)
	assert.Equal(t, "encrypted-child-b-key", converted.ChildGroups[1].Keys[0].KeyValue)
	assert.JSONEq(t, `{"source":{"targets":[{"model":"target","weight":100}]}}`, string(converted.ChildGroups[1].ModelRedirectRulesV2))
}

func TestGroupImportHelpersCountAndSampleChildKeys(t *testing.T) {
	t.Parallel()

	groups := []GroupExportData{
		{
			Group: GroupExportInfo{Name: "parent-a"},
			ChildGroups: []ChildGroupExportInfo{
				{
					Name: "child-a",
					Keys: []KeyExportInfo{
						{KeyValue: "child-a-key-1"},
						{KeyValue: "child-a-key-2"},
					},
				},
			},
		},
		{
			Group: GroupExportInfo{Name: "parent-b"},
			Keys: []KeyExportInfo{
				{KeyValue: "parent-b-key"},
			},
			ChildGroups: []ChildGroupExportInfo{
				{
					Name: "child-b",
					Keys: []KeyExportInfo{
						{KeyValue: "child-b-key"},
					},
				},
			},
		},
	}

	assert.Equal(t, 4, CountGroupExportKeys(groups))
	assert.Equal(t, []string{"child-a-key-1", "child-a-key-2", "parent-b-key", "child-b-key"}, CollectGroupImportSampleKeys(groups))
}

func TestGetImportModeUsesChildOnlyEncryptedKeys(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	groups := []GroupExportData{
		{
			Group: GroupExportInfo{Name: "parent-with-only-child-keys"},
			ChildGroups: []ChildGroupExportInfo{
				{
					Name: "child",
					Keys: []KeyExportInfo{
						{KeyValue: strings.Repeat("ab", 28)},
					},
				},
			},
		},
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req, err := http.NewRequest(http.MethodPost, "/api/groups/import", nil)
	require.NoError(t, err)
	c.Request = req

	assert.Equal(t, "encrypted", GetImportMode(c, CollectGroupImportSampleKeys(groups)))
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
			sampleKeys: []string{strings.Repeat("ab", 28), strings.Repeat("cd", 29)},
			expected:   "encrypted",
		},
		{
			name:       "heuristic_tie_stays_plain",
			query:      "",
			sampleKeys: []string{strings.Repeat("ab", 28), "plain-auth-value"},
			expected:   "plain",
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
			name:     "hex_below_ciphertext_length",
			input:    "1234567890123456",
			expected: false,
		},
		{
			name:     "invalid_chars",
			input:    "sk-1234567890abcdefghij",
			expected: false,
		},
		{
			name:     "minimum_ciphertext_length",
			input:    strings.Repeat("ab", 28),
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
		"gpt-4":         "gpt-4-turbo",
		"gpt-3.5-turbo": "gpt-3.5-turbo-16k",
		"claude-3":      "claude-3-opus",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ConvertModelRedirectRulesToExport(input)
	}
}

func BenchmarkLooksLikeHex(b *testing.B) {
	testStr := strings.Repeat("ab", 28)

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
