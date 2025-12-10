package utils

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

var (
	rng     *rand.Rand
	rngOnce sync.Once
)

// GetRand returns a thread-safe random number generator
func GetRand() *rand.Rand {
	rngOnce.Do(func() {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	})
	return rng
}

// WeightedRandomSelect selects an index from a list based on weights
// Returns -1 if no valid selection can be made
func WeightedRandomSelect(weights []int) int {
	if len(weights) == 0 {
		return -1
	}

	// Calculate total weight and build available list
	totalWeight := 0
	for _, w := range weights {
		if w > 0 {
			totalWeight += w
		}
	}

	if totalWeight == 0 {
		// If all weights are 0, no valid selection (all disabled)
		return -1
	}

	// Generate random number in range [0, totalWeight)
	randomWeight := GetRand().Intn(totalWeight)

	// Select based on cumulative weight
	cumulativeWeight := 0
	for i, w := range weights {
		if w > 0 {
			cumulativeWeight += w
			if randomWeight < cumulativeWeight {
				return i
			}
		}
	}

	// Fallback: return last valid index
	for i := len(weights) - 1; i >= 0; i-- {
		if weights[i] > 0 {
			return i
		}
	}

	return -1
}

// generateRandomSuffixWithLength generates a random suffix of the given length using
// lowercase letters and numbers. Lengths less than or equal to zero fall back to 4.
func generateRandomSuffixWithLength(length int) string {
	if length <= 0 {
		length = 4
	}
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	rng := GetRand()
	suffix := make([]byte, length)
	for i := range suffix {
		suffix[i] = charset[rng.Intn(len(charset))]
	}
	return string(suffix)
}

// GenerateRandomSuffix generates a random 4-character suffix using lowercase letters and numbers.
// The dedicated helper preserves backward compatibility for existing callers that expect 4 chars.
func GenerateRandomSuffix() string {
	return generateRandomSuffixWithLength(4)
}

// GenerateTriggerSignal returns the preferred b4u2cc-style trigger signal used by the
// function call prompt. The format is <<CALL_xxxxxx>> with a six-character random suffix.
func GenerateTriggerSignal() string {
	suffix := generateRandomSuffixWithLength(6)
	return fmt.Sprintf("<<CALL_%s>>", suffix)
}
