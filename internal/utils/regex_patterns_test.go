package utils

import (
	"testing"
)

func TestDelimitersPattern(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "comma separated",
			input:    "key1,key2,key3",
			expected: []string{"key1", "key2", "key3"},
		},
		{
			name:     "space separated",
			input:    "key1 key2 key3",
			expected: []string{"key1", "key2", "key3"},
		},
		{
			name:     "newline separated",
			input:    "key1\nkey2\nkey3",
			expected: []string{"key1", "key2", "key3"},
		},
		{
			name:     "mixed delimiters",
			input:    "key1, key2; key3\nkey4\tkey5",
			expected: []string{"key1", "key2", "key3", "key4", "key5"},
		},
		{
			name:     "multiple consecutive delimiters",
			input:    "key1,,,key2   key3",
			expected: []string{"key1", "key2", "key3"},
		},
		{
			name:     "carriage return",
			input:    "key1\r\nkey2\rkey3",
			expected: []string{"key1", "key2", "key3"},
		},
		{
			name:     "tab separated",
			input:    "key1\tkey2\tkey3",
			expected: []string{"key1", "key2", "key3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := DelimitersPattern.Split(tt.input, -1)
			// Filter out empty strings
			var filtered []string
			for _, s := range result {
				if s != "" {
					filtered = append(filtered, s)
				}
			}

			if len(filtered) != len(tt.expected) {
				t.Errorf("Expected %d parts, got %d", len(tt.expected), len(filtered))
				return
			}

			for i, expected := range tt.expected {
				if filtered[i] != expected {
					t.Errorf("Part %d: expected %q, got %q", i, expected, filtered[i])
				}
			}
		})
	}
}

func TestValidKeyCharsPattern(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid alphanumeric",
			input:    "sk1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid with hyphen",
			input:    "sk-1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid with underscore",
			input:    "sk_1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid with dot",
			input:    "sk.1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid with slash",
			input:    "sk/1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid with plus",
			input:    "sk+1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid with equals",
			input:    "sk=1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid with colon",
			input:    "sk:1234567890abcdef",
			expected: true,
		},
		{
			name:     "valid complex key",
			input:    "sk-proj_1234567890.abcdef/test+key=value:123",
			expected: true,
		},
		{
			name:     "invalid with space",
			input:    "sk 1234567890abcdef",
			expected: false,
		},
		{
			name:     "invalid with special chars",
			input:    "sk@1234567890abcdef",
			expected: false,
		},
		{
			name:     "invalid with comma",
			input:    "sk,1234567890abcdef",
			expected: false,
		},
		{
			name:     "invalid with semicolon",
			input:    "sk;1234567890abcdef",
			expected: false,
		},
		{
			name:     "invalid with newline",
			input:    "sk\n1234567890abcdef",
			expected: false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ValidKeyCharsPattern.MatchString(tt.input)
			if result != tt.expected {
				t.Errorf("ValidKeyCharsPattern.MatchString(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// Benchmark tests for regex performance
func BenchmarkDelimitersPattern(b *testing.B) {
	input := "key1, key2; key3\nkey4\tkey5, key6; key7\nkey8\tkey9, key10"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DelimitersPattern.Split(input, -1)
	}
}

func BenchmarkValidKeyCharsPattern(b *testing.B) {
	input := "sk-proj_1234567890.abcdef/test+key=value:123"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ValidKeyCharsPattern.MatchString(input)
	}
}
