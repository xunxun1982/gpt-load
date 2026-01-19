package errors

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsIgnorableError tests the ignorable error detection
func TestIsIgnorableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "context canceled",
			err:      errors.New("context canceled"),
			expected: true,
		},
		{
			name:     "connection reset by peer",
			err:      errors.New("read tcp: connection reset by peer"),
			expected: true,
		},
		{
			name:     "broken pipe",
			err:      errors.New("write tcp: broken pipe"),
			expected: true,
		},
		{
			name:     "use of closed network connection",
			err:      errors.New("use of closed network connection"),
			expected: true,
		},
		{
			name:     "request canceled",
			err:      errors.New("request canceled while waiting for connection"),
			expected: true,
		},
		{
			name:     "non-ignorable error",
			err:      errors.New("database connection failed"),
			expected: false,
		},
		{
			name:     "partial match should work",
			err:      errors.New("error: context canceled due to timeout"),
			expected: true,
		},
		{
			name:     "case sensitive match",
			err:      errors.New("Context Canceled"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsIgnorableError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIgnorableErrorSubstrings tests all ignorable error substrings
func TestIgnorableErrorSubstrings(t *testing.T) {
	// Verify all substrings are properly defined
	assert.NotEmpty(t, ignorableErrorSubstrings)
	assert.Contains(t, ignorableErrorSubstrings, "context canceled")
	assert.Contains(t, ignorableErrorSubstrings, "connection reset by peer")
	assert.Contains(t, ignorableErrorSubstrings, "broken pipe")
	assert.Contains(t, ignorableErrorSubstrings, "use of closed network connection")
	assert.Contains(t, ignorableErrorSubstrings, "request canceled")
}

// BenchmarkIsIgnorableError benchmarks ignorable error detection
func BenchmarkIsIgnorableError(b *testing.B) {
	err := errors.New("context canceled")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsIgnorableError(err)
	}
}

// BenchmarkIsIgnorableError_NonIgnorable benchmarks non-ignorable error detection
func BenchmarkIsIgnorableError_NonIgnorable(b *testing.B) {
	err := errors.New("database connection failed")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsIgnorableError(err)
	}
}
