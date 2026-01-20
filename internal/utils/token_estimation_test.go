package utils

import "testing"

// TestEstimateTokensFromString tests token estimation from strings
func TestEstimateTokensFromString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"EmptyString", "", 0},
		{"ShortString", "hello", 2},                                       // 5 runes / 4 = 1.25 -> 2
		{"MediumString", "hello world", 3},                                // 11 runes / 4 = 2.75 -> 3
		{"LongString", "The quick brown fox jumps over the lazy dog", 11}, // 43 runes -> (43+3)/4 = 11
		{"ChineseText", "你好世界", 1},                                        // 4 runes / 4 = 1
		{"MixedText", "Hello世界", 2},                                       // 7 runes / 4 = 1.75 -> 2
		{"EmojiText", "Hello🌍World", 3},                                   // 11 runes / 4 = 2.75 -> 3
		{"SingleChar", "a", 1},                                            // 1 rune / 4 = 0.25 -> 1
		{"ThreeChars", "abc", 1},                                          // 3 runes / 4 = 0.75 -> 1
		{"FourChars", "abcd", 1},                                          // 4 runes / 4 = 1
		{"FiveChars", "abcde", 2},                                         // 5 runes / 4 = 1.25 -> 2
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EstimateTokensFromString(tt.input)
			if result != tt.expected {
				t.Errorf("EstimateTokensFromString(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

// TestEstimateTokensFromBytes tests token estimation from byte slices
func TestEstimateTokensFromBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    []byte
		expected int
	}{
		{"EmptyBytes", []byte{}, 0},
		{"ShortBytes", []byte("hello"), 2},
		{"MediumBytes", []byte("hello world"), 3},
		{"ChineseBytes", []byte("你好世界"), 1},
		{"MixedBytes", []byte("Hello世界"), 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EstimateTokensFromBytes(tt.input)
			if result != tt.expected {
				t.Errorf("EstimateTokensFromBytes(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

// BenchmarkEstimateTokensFromString benchmarks token estimation
func BenchmarkEstimateTokensFromString(b *testing.B) {
	b.ReportAllocs()
	text := "The quick brown fox jumps over the lazy dog. This is a test sentence for benchmarking token estimation."
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateTokensFromString(text)
	}
}

// BenchmarkEstimateTokensFromBytes benchmarks token estimation from bytes
func BenchmarkEstimateTokensFromBytes(b *testing.B) {
	b.ReportAllocs()
	text := []byte("The quick brown fox jumps over the lazy dog. This is a test sentence for benchmarking token estimation.")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateTokensFromBytes(text)
	}
}
