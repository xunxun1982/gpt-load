package errors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsUnCounted tests the uncounted error detection
func TestIsUnCounted(t *testing.T) {
	tests := []struct {
		name     string
		errorMsg string
		expected bool
	}{
		{
			name:     "empty string",
			errorMsg: "",
			expected: false,
		},
		{
			name:     "resource exhausted",
			errorMsg: "resource has been exhausted",
			expected: true,
		},
		{
			name:     "resource exhausted uppercase",
			errorMsg: "RESOURCE HAS BEEN EXHAUSTED",
			expected: true,
		},
		{
			name:     "resource exhausted mixed case",
			errorMsg: "Resource Has Been Exhausted",
			expected: true,
		},
		{
			name:     "reduce message length",
			errorMsg: "please reduce the length of the messages",
			expected: true,
		},
		{
			name:     "reduce message length uppercase",
			errorMsg: "PLEASE REDUCE THE LENGTH OF THE MESSAGES",
			expected: true,
		},
		{
			name:     "reduce message length mixed case",
			errorMsg: "Please Reduce The Length Of The Messages",
			expected: true,
		},
		{
			name:     "partial match resource exhausted",
			errorMsg: "Error: resource has been exhausted, please try again later",
			expected: true,
		},
		{
			name:     "partial match reduce length",
			errorMsg: "Error: please reduce the length of the messages or split into multiple requests",
			expected: true,
		},
		{
			name:     "non-uncounted error",
			errorMsg: "database connection failed",
			expected: false,
		},
		{
			name:     "similar but not matching",
			errorMsg: "resource is available",
			expected: false,
		},
		{
			name:     "another non-matching error",
			errorMsg: "please try again",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsUnCounted(tt.errorMsg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestUnCountedSubstrings tests all uncounted error substrings
func TestUnCountedSubstrings(t *testing.T) {
	// Verify all substrings are properly defined
	assert.NotEmpty(t, unCountedSubstrings)
	assert.Contains(t, unCountedSubstrings, "resource has been exhausted")
	assert.Contains(t, unCountedSubstrings, "please reduce the length of the messages")
}

// BenchmarkIsUnCounted benchmarks uncounted error detection
func BenchmarkIsUnCounted(b *testing.B) {
	errorMsg := "resource has been exhausted"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsUnCounted(errorMsg)
	}
}

// BenchmarkIsUnCounted_NonUncounted benchmarks non-uncounted error detection
func BenchmarkIsUnCounted_NonUncounted(b *testing.B) {
	errorMsg := "database connection failed"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsUnCounted(errorMsg)
	}
}

// BenchmarkIsUnCounted_LongMessage benchmarks with long error message
func BenchmarkIsUnCounted_LongMessage(b *testing.B) {
	errorMsg := "This is a very long error message that contains the phrase resource has been exhausted somewhere in the middle of the text"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsUnCounted(errorMsg)
	}
}
