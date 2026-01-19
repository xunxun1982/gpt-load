package i18n

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInit tests i18n initialization
func TestInit(t *testing.T) {
	err := Init()
	require.NoError(t, err)
	assert.NotNil(t, bundle)
}

// TestGetLocalizer tests getting a localizer
func TestGetLocalizer(t *testing.T) {
	// Initialize first
	err := Init()
	require.NoError(t, err)

	tests := []struct {
		name       string
		acceptLang string
	}{
		{
			name:       "Chinese",
			acceptLang: "zh-CN",
		},
		{
			name:       "English",
			acceptLang: "en-US",
		},
		{
			name:       "Japanese",
			acceptLang: "ja-JP",
		},
		{
			name:       "empty defaults to Chinese",
			acceptLang: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localizer := GetLocalizer(tt.acceptLang)
			assert.NotNil(t, localizer)
		})
	}
}

// TestParseAcceptLanguage tests Accept-Language header parsing
func TestParseAcceptLanguage(t *testing.T) {
	tests := []struct {
		name       string
		acceptLang string
		expected   []string
	}{
		{
			name:       "simple Chinese",
			acceptLang: "zh-CN",
			expected:   []string{"zh-CN"},
		},
		{
			name:       "simple English",
			acceptLang: "en-US",
			expected:   []string{"en-US"},
		},
		{
			name:       "with quality factor",
			acceptLang: "en-US;q=0.9",
			expected:   []string{"en-US"},
		},
		{
			name:       "multiple languages",
			acceptLang: "zh-CN,en-US;q=0.9",
			expected:   []string{"zh-CN"},
		},
		{
			name:       "empty",
			acceptLang: "",
			expected:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseAcceptLanguage(tt.acceptLang)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestNormalizeLanguageCode tests language code normalization
func TestNormalizeLanguageCode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "zh",
			input:    "zh",
			expected: "zh-CN",
		},
		{
			name:     "zh-CN",
			input:    "zh-CN",
			expected: "zh-CN",
		},
		{
			name:     "zh-Hans",
			input:    "zh-Hans",
			expected: "zh-CN",
		},
		{
			name:     "en",
			input:    "en",
			expected: "en-US",
		},
		{
			name:     "en-US",
			input:    "en-US",
			expected: "en-US",
		},
		{
			name:     "ja",
			input:    "ja",
			expected: "ja-JP",
		},
		{
			name:     "ja-JP",
			input:    "ja-JP",
			expected: "ja-JP",
		},
		{
			name:     "uppercase",
			input:    "ZH-CN",
			expected: "zh-CN",
		},
		{
			name:     "with spaces",
			input:    "  en-US  ",
			expected: "en-US",
		},
		{
			name:     "unknown defaults to Chinese",
			input:    "fr-FR",
			expected: "zh-CN",
		},
		{
			name:     "zh prefix",
			input:    "zh-TW",
			expected: "zh-CN",
		},
		{
			name:     "en prefix",
			input:    "en-GB",
			expected: "en-US",
		},
		{
			name:     "ja prefix",
			input:    "ja-KR",
			expected: "ja-JP",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeLanguageCode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestT tests message translation
func TestT(t *testing.T) {
	// Initialize first
	err := Init()
	require.NoError(t, err)

	localizer := GetLocalizer("en-US")

	tests := []struct {
		name   string
		msgID  string
		data   map[string]any
		hasMsg bool
	}{
		{
			name:   "existing message",
			msgID:  "common.success",
			hasMsg: true,
		},
		{
			name:   "non-existing message returns ID",
			msgID:  "non.existing.message",
			hasMsg: false,
		},
		{
			name:   "message with template data",
			msgID:  "common.success",
			data:   map[string]any{"name": "test"},
			hasMsg: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result string
			if tt.data != nil {
				result = T(localizer, tt.msgID, tt.data)
			} else {
				result = T(localizer, tt.msgID)
			}

			assert.NotEmpty(t, result)
			if !tt.hasMsg {
				// If message doesn't exist, should return message ID
				assert.Equal(t, tt.msgID, result)
			}
		})
	}
}

// TestGetMessages tests getting messages for different languages
func TestGetMessages(t *testing.T) {
	tests := []struct {
		name string
		lang string
	}{
		{
			name: "Chinese",
			lang: "zh-CN",
		},
		{
			name: "English",
			lang: "en-US",
		},
		{
			name: "Japanese",
			lang: "ja-JP",
		},
		{
			name: "unknown defaults to Chinese",
			lang: "fr-FR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := getMessages(tt.lang)
			assert.NotNil(t, messages)
			assert.NotEmpty(t, messages)
		})
	}
}

// BenchmarkGetLocalizer benchmarks getting a localizer
func BenchmarkGetLocalizer(b *testing.B) {
	if err := Init(); err != nil {
		b.Fatalf("Failed to initialize i18n: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetLocalizer("zh-CN")
	}
}

// BenchmarkParseAcceptLanguage benchmarks Accept-Language parsing
func BenchmarkParseAcceptLanguage(b *testing.B) {
	acceptLang := "zh-CN,en-US;q=0.9,ja-JP;q=0.8"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseAcceptLanguage(acceptLang)
	}
}

// BenchmarkNormalizeLanguageCode benchmarks language code normalization
func BenchmarkNormalizeLanguageCode(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = normalizeLanguageCode("zh-CN")
	}
}

// BenchmarkT benchmarks message translation
func BenchmarkT(b *testing.B) {
	if err := Init(); err != nil {
		b.Fatalf("Failed to initialize i18n: %v", err)
	}
	localizer := GetLocalizer("en-US")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = T(localizer, "common.success")
	}
}
