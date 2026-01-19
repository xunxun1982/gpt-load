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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseUpstreamError(tt.body)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTruncateString tests string truncation
func TestTruncateString(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLength int
		expected  string
	}{
		{
			name:      "no truncation needed",
			input:     "short string",
			maxLength: 100,
			expected:  "short string",
		},
		{
			name:      "exact length",
			input:     "exact",
			maxLength: 5,
			expected:  "exact",
		},
		{
			name:      "truncation needed",
			input:     "this is a very long string that needs truncation",
			maxLength: 10,
			expected:  "this is a ",
		},
		{
			name:      "empty string",
			input:     "",
			maxLength: 10,
			expected:  "",
		},
		{
			name:      "zero max length",
			input:     "test",
			maxLength: 0,
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLength)
			assert.Equal(t, tt.expected, result)
			assert.LessOrEqual(t, len(result), tt.maxLength)
		})
	}
}

// TestMaxErrorBodyLength tests the constant value
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

// BenchmarkTruncateString benchmarks string truncation
func BenchmarkTruncateString(b *testing.B) {
	input := strings.Repeat("a", 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = truncateString(input, maxErrorBodyLength)
	}
}
