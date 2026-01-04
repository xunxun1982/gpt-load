package utils

import (
	cryptoRand "crypto/rand"
	"encoding/base64"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

var randSeedOnce sync.Once

// ensureRandSeeded initializes the global math/rand source once.
// The top-level math/rand functions use a locked source internally
// and are safe for concurrent use after seeding.
func ensureRandSeeded() {
	randSeedOnce.Do(func() {
		rand.Seed(time.Now().UnixNano())
	})
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
	ensureRandSeeded()
	randomWeight := rand.Intn(totalWeight)

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
// It relies on math/rand's global locked source, which is concurrency-safe.
func generateRandomSuffixWithLength(length int) string {
	if length <= 0 {
		length = 4
	}
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	ensureRandSeeded()
	suffix := make([]byte, length)
	for i := range suffix {
		suffix[i] = charset[rand.Intn(len(charset))]
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

// GenerateSecureRandomString generates a cryptographically secure random string
// using crypto/rand. The output uses URL-safe base64 encoding (A-Z, a-z, 0-9, -, _).
// The length parameter specifies the desired output string length.
// This function is suitable for generating API keys, tokens, and other security-sensitive identifiers.
func GenerateSecureRandomString(length int) string {
	if length <= 0 {
		length = 48 // Default to 48 characters for API key compatibility
	}

	// Calculate bytes needed: base64 encoding produces 4 chars per 3 bytes
	// Formula computes ceil(length * 3 / 4) to ensure we have enough bytes
	bytesNeeded := (length*3 + 3) / 4

	randomBytes := make([]byte, bytesNeeded)
	if _, err := cryptoRand.Read(randomBytes); err != nil {
		// crypto/rand failure indicates a serious system problem (e.g., /dev/urandom unavailable).
		// Silently falling back to math/rand would undermine the security guarantee of this function.
		// Panic is appropriate here since this should never happen on any supported OS.
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}

	// Use URL-safe base64 encoding without padding
	encoded := base64.RawURLEncoding.EncodeToString(randomBytes)

	// Truncate to exact length
	if len(encoded) > length {
		return encoded[:length]
	}
	return encoded
}
