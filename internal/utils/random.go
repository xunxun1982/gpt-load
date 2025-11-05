package utils

import (
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
		// If all weights are 0, select randomly from all
		return GetRand().Intn(len(weights))
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
