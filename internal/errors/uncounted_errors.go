package errors

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// unCountedSubstrings contains a list of substrings that indicate an error
var unCountedSubstrings = []string{
	"resource has been exhausted",
	"please reduce the length of the messages",
}

// IsUnCounted checks if the given error message contains substrings.
// Matching is case-insensitive. Pure-ASCII input uses a zero-allocation fast
// path; input containing non-ASCII runes falls back to a rune-aligned fold
// matcher, so Unicode case-fold equivalents (e.g. U+017F "ſ" folding to "s")
// match without splitting multi-byte runes.
func IsUnCounted(errorMsg string) bool {
	if errorMsg == "" {
		return false
	}

	// Fast exact matches (patterns are lowercase ASCII).
	for _, pattern := range unCountedSubstrings {
		if strings.Contains(errorMsg, pattern) {
			return true
		}
	}

	hasUpper := false
	hasNonASCII := false
	for i := 0; i < len(errorMsg); i++ {
		b := errorMsg[i]
		if b >= 0x80 {
			hasNonASCII = true
			break
		}
		if b >= 'A' && b <= 'Z' {
			hasUpper = true
		}
	}

	if !hasNonASCII {
		// Pure ASCII: byte windows align with runes, so a fixed-size window
		// plus EqualFold is exact and allocation-free.
		if !hasUpper {
			return false
		}
		for _, pattern := range unCountedSubstrings {
			for offset := 0; offset+len(pattern) <= len(errorMsg); offset++ {
				if strings.EqualFold(errorMsg[offset:offset+len(pattern)], pattern) {
					return true
				}
			}
		}
		return false
	}

	// Non-ASCII input: match rune by rune with Unicode simple folding, so
	// multi-byte runes are never split and fold equivalents whose encoded size
	// differs from the ASCII pattern still match.
	for _, pattern := range unCountedSubstrings {
		if containsFold(errorMsg, pattern) {
			return true
		}
	}
	return false
}

// containsFold reports whether s contains a rune-aligned window that is
// Unicode case-fold equivalent to sub. sub is expected to be ASCII, so its
// bytes are compared directly as runes. Matching semantics follow
// strings.EqualFold (Unicode simple folding), e.g. "ſ" equals "s".
func containsFold(s, sub string) bool {
	if len(sub) == 0 || len(s) < len(sub) {
		return false
	}
	for i := 0; i < len(s); {
		si := i
		matched := true
		for pi := 0; pi < len(sub); pi++ {
			if si >= len(s) {
				matched = false
				break
			}
			r, size := utf8.DecodeRuneInString(s[si:])
			if size == 0 {
				size = 1 // Defensive: DecodeRuneInString never returns 0 for non-empty input.
			}
			if !foldRuneEqual(r, rune(sub[pi])) {
				matched = false
				break
			}
			si += size
		}
		if matched {
			return true
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			size = 1 // Defensive: DecodeRuneInString never returns 0 for non-empty input.
		}
		i += size
	}
	return false
}

// foldRuneEqual reports whether a and b belong to the same Unicode
// simple-fold equivalence class, mirroring strings.EqualFold's per-rune rule.
func foldRuneEqual(a, b rune) bool {
	if a == b {
		return true
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}
