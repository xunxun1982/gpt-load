package utils

import (
	"unicode/utf8"
)

// EstimateTokensFromString estimates token count using ~4 runes per token heuristic.
// NOTE: This is an approximation and may differ from actual tokenizers
// (language mix, code/JSON, and model-specific tokenization).
func EstimateTokensFromString(text string) int {
	if text == "" {
		return 0
	}
	count := utf8.RuneCountInString(text)
	if count <= 0 {
		return 0
	}
	// Heuristic: ~4 runes per token
	return (count + 3) / 4
}

// EstimateTokensFromBytes estimates token count from byte slice.
// NOTE: This is an approximation and may differ from actual tokenizers
// (language mix, code/JSON, and model-specific tokenization).
func EstimateTokensFromBytes(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	count := utf8.RuneCount(b)
	if count <= 0 {
		return 0
	}
	// Heuristic: ~4 runes per token
	return (count + 3) / 4
}
