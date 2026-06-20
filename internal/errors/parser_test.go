package errors

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseUpstreamError tests parsing various upstream error formats
func TestParseUpstreamError(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		expected string
	}{
		{
			name:     "standard OpenAI format",
			body:     []byte(`{"error": {"message": "Invalid API key"}}`),
			expected: "Invalid API key",
		},
		{
			name:     "vendor format (Baidu)",
			body:     []byte(`{"error_msg": "Access denied"}`),
			expected: "Access denied",
		},
		{
			name:     "simple error format",
			body:     []byte(`{"error": "Rate limit exceeded"}`),
			expected: "Rate limit exceeded",
		},
		{
			name:     "root message format",
			body:     []byte(`{"message": "Service unavailable"}`),
			expected: "Service unavailable",
		},
		{
			name:     "invalid JSON",
			body:     []byte(`not a json`),
			expected: "not a json",
		},
		{
			name:     "empty body",
			body:     []byte(``),
			expected: "",
		},
		{
			name:     "whitespace in message",
			body:     []byte(`{"error": {"message": "  Error with spaces  "}}`),
			expected: "Error with spaces",
		},
		{
			name:     "nested error structure",
			body:     []byte(`{"error": {"message": "Nested error", "code": 400}}`),
			expected: "Nested error",
		},
		{
			name:     "multiple fields",
			body:     []byte(`{"error": {"message": "Primary error", "type": "invalid_request"}, "error_msg": "Secondary"}`),
			expected: "Primary error",
		},
		{
			name:     "long error message",
			body:     []byte(`{"error": {"message": "` + strings.Repeat("a", 3000) + `"}}`),
			expected: strings.Repeat("a", maxErrorBodyLength),
		},
		{
			name:     "standard message redacts api key",
			body:     []byte(`{"error": {"message": "upstream rejected key sk-abcdefghijklmnopqrstuvwxyz123456"}}`),
			expected: "upstream rejected key [REDACTED_API_KEY]",
		},
		{
			name:     "raw fallback redacts authorization header",
			body:     []byte("Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456\nrequest failed"),
			expected: "Authorization: [REDACTED]\nrequest failed",
		},
		{
			name:     "binary fallback becomes readable message",
			body:     []byte{0xff, 0xfe, 0xfd, 0x00, 0x81, 0x82},
			expected: "upstream returned unreadable binary error body",
		},
		{
			name:     "replacement characters fallback becomes readable message",
			body:     []byte("���D�AR�0E�{�Q�0l܎g����I4�)Ҕ�'p�"),
			expected: "upstream returned unreadable binary error body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseUpstreamError(tt.body)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestMaxErrorBodyLength validates that the limit is reasonable for error messages
// and hasn't been accidentally modified
func TestMaxErrorBodyLength(t *testing.T) {
	assert.Equal(t, 2048, maxErrorBodyLength)
}

// BenchmarkParseUpstreamError benchmarks parsing standard format
func BenchmarkParseUpstreamError(b *testing.B) {
	body := []byte(`{"error": {"message": "Invalid API key"}}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseUpstreamError(body)
	}
}

// BenchmarkParseUpstreamError_VendorFormat benchmarks parsing vendor format
func BenchmarkParseUpstreamError_VendorFormat(b *testing.B) {
	body := []byte(`{"error_msg": "Access denied"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseUpstreamError(body)
	}
}

// BenchmarkParseUpstreamError_InvalidJSON benchmarks parsing invalid JSON
func BenchmarkParseUpstreamError_InvalidJSON(b *testing.B) {
	body := []byte(`not a json`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseUpstreamError(body)
	}
}

// BenchmarkParseUpstreamError_LargeBody benchmarks parsing large error body
func BenchmarkParseUpstreamError_LargeBody(b *testing.B) {
	body := []byte(`{"error": {"message": "` + strings.Repeat("a", 5000) + `"}}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseUpstreamError(body)
	}
}
