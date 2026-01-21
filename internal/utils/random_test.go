package utils

import (
	"strings"
	"sync"
	"testing"
)

func TestWeightedRandomSelect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		weights       []int
		expectedValid bool
	}{
		{
			name:          "empty_weights",
			weights:       []int{},
			expectedValid: false,
		},
		{
			name:          "all_zero_weights",
			weights:       []int{0, 0, 0},
			expectedValid: false,
		},
		{
			name:          "single_positive_weight",
			weights:       []int{100},
			expectedValid: true,
		},
		{
			name:          "multiple_positive_weights",
			weights:       []int{10, 20, 30},
			expectedValid: true,
		},
		{
			name:          "mixed_weights",
			weights:       []int{0, 50, 0, 50},
			expectedValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := WeightedRandomSelect(tt.weights)

			if tt.expectedValid {
				if result < 0 || result >= len(tt.weights) {
					t.Errorf("expected valid index, got %d", result)
				}
				if tt.weights[result] <= 0 {
					t.Errorf("selected index %d has non-positive weight %d", result, tt.weights[result])
				}
			} else {
				if result != -1 {
					t.Errorf("expected -1 for invalid selection, got %d", result)
				}
			}
		})
	}
}

func TestWeightedRandomSelectDistribution(t *testing.T) {
	t.Parallel()

	weights := []int{10, 20, 30}
	iterations := 10000
	counts := make([]int, len(weights))

	for i := 0; i < iterations; i++ {
		idx := WeightedRandomSelect(weights)
		if idx >= 0 && idx < len(counts) {
			counts[idx]++
		}
	}

	// Check that all indices were selected at least once
	for i, count := range counts {
		if count == 0 {
			t.Errorf("index %d was never selected", i)
		}
	}

	// Rough distribution check: higher weights should have more selections
	if counts[0] >= counts[1] || counts[1] >= counts[2] {
		t.Logf("distribution may be skewed: %v (weights: %v)", counts, weights)
	}
}

func TestGenerateRandomSuffix(t *testing.T) {
	t.Parallel()

	const expectedLength = 4
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"

	for i := 0; i < 100; i++ {
		suffix := GenerateRandomSuffix()

		if len(suffix) != expectedLength {
			t.Errorf("expected length %d, got %d", expectedLength, len(suffix))
		}

		for _, ch := range suffix {
			if !strings.ContainsRune(charset, ch) {
				t.Errorf("invalid character %c in suffix %s", ch, suffix)
			}
		}
	}
}

func TestGenerateRandomSuffixUniqueness(t *testing.T) {
	t.Parallel()

	const iterations = 1000
	seen := make(map[string]bool, iterations)

	for i := 0; i < iterations; i++ {
		suffix := GenerateRandomSuffix()
		if seen[suffix] {
			t.Logf("duplicate suffix found: %s (this is rare but possible)", suffix)
		}
		seen[suffix] = true
	}

	// With 4 chars from 36-char charset, we have 36^4 = 1,679,616 possibilities
	// Getting 1000 unique values should be very likely
	if len(seen) < iterations*95/100 {
		t.Errorf("expected at least %d unique suffixes, got %d", iterations*95/100, len(seen))
	}
}

func TestGenerateTriggerSignal(t *testing.T) {
	t.Parallel()

	const expectedPrefix = "<<CALL_"
	const expectedSuffix = ">>"
	const expectedSuffixLength = 6

	for i := 0; i < 100; i++ {
		signal := GenerateTriggerSignal()

		if !strings.HasPrefix(signal, expectedPrefix) {
			t.Errorf("expected prefix %s, got %s", expectedPrefix, signal)
		}

		if !strings.HasSuffix(signal, expectedSuffix) {
			t.Errorf("expected suffix %s, got %s", expectedSuffix, signal)
		}

		// Extract the random part
		randomPart := signal[len(expectedPrefix) : len(signal)-len(expectedSuffix)]
		if len(randomPart) != expectedSuffixLength {
			t.Errorf("expected random part length %d, got %d", expectedSuffixLength, len(randomPart))
		}
	}
}

func TestGenerateSecureRandomString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		length         int
		expectedLength int
	}{
		{
			name:           "default_length",
			length:         0,
			expectedLength: 48,
		},
		{
			name:           "negative_length",
			length:         -1,
			expectedLength: 48,
		},
		{
			name:           "custom_length_16",
			length:         16,
			expectedLength: 16,
		},
		{
			name:           "custom_length_32",
			length:         32,
			expectedLength: 32,
		},
		{
			name:           "custom_length_64",
			length:         64,
			expectedLength: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := GenerateSecureRandomString(tt.length)

			if len(result) != tt.expectedLength {
				t.Errorf("expected length %d, got %d", tt.expectedLength, len(result))
			}

			// Check that it only contains URL-safe base64 characters
			const validChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
			for _, ch := range result {
				if !strings.ContainsRune(validChars, ch) {
					t.Errorf("invalid character %c in result %s", ch, result)
				}
			}
		})
	}
}

func TestGenerateSecureRandomStringUniqueness(t *testing.T) {
	t.Parallel()

	const iterations = 1000
	const length = 32
	seen := make(map[string]bool, iterations)

	for i := 0; i < iterations; i++ {
		str := GenerateSecureRandomString(length)
		if seen[str] {
			t.Errorf("duplicate secure random string found: %s", str)
		}
		seen[str] = true
	}

	if len(seen) != iterations {
		t.Errorf("expected %d unique strings, got %d", iterations, len(seen))
	}
}

func TestGenerateRandomSuffixWithLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		length         int
		expectedLength int
	}{
		{
			name:           "zero_length",
			length:         0,
			expectedLength: 4,
		},
		{
			name:           "negative_length",
			length:         -1,
			expectedLength: 4,
		},
		{
			name:           "custom_length_8",
			length:         8,
			expectedLength: 8,
		},
		{
			name:           "custom_length_16",
			length:         16,
			expectedLength: 16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := generateRandomSuffixWithLength(tt.length)

			if len(result) != tt.expectedLength {
				t.Errorf("expected length %d, got %d", tt.expectedLength, len(result))
			}
		})
	}
}

// Test concurrent access
func TestWeightedRandomSelectConcurrent(t *testing.T) {
	t.Parallel()

	weights := []int{10, 20, 30}
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				idx := WeightedRandomSelect(weights)
				if idx < 0 || idx >= len(weights) {
					t.Errorf("invalid index: %d", idx)
				}
			}
		}()
	}

	wg.Wait()
}

func TestGenerateSecureRandomStringConcurrent(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	results := make(chan string, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- GenerateSecureRandomString(32)
		}()
	}

	wg.Wait()
	close(results)

	seen := make(map[string]bool)
	for str := range results {
		if seen[str] {
			t.Errorf("duplicate string in concurrent generation: %s", str)
		}
		seen[str] = true
	}
}

// Benchmark tests
func BenchmarkWeightedRandomSelect(b *testing.B) {
	weights := []int{10, 20, 30, 40, 50}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = WeightedRandomSelect(weights)
	}
}

func BenchmarkGenerateRandomSuffix(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = GenerateRandomSuffix()
	}
}

func BenchmarkGenerateTriggerSignal(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = GenerateTriggerSignal()
	}
}

func BenchmarkGenerateSecureRandomString(b *testing.B) {
	b.Run("length_16", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = GenerateSecureRandomString(16)
		}
	})

	b.Run("length_32", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = GenerateSecureRandomString(32)
		}
	})

	b.Run("length_64", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = GenerateSecureRandomString(64)
		}
	})
}
