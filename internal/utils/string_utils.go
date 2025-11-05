package utils

import (
	"strings"
)

// MaskAPIKey masks an API key for safe logging.
// Example: "sk-1234567890abcdef" -> "sk-1****cdef"
func MaskAPIKey(key string) string {
	length := len(key)
	if length <= 8 {
		return key
	}
	// Use strings.Builder for better performance in hot path (logging)
	var b strings.Builder
	// Pre-allocate exactly 12 bytes: 4 (prefix) + 4 (****) + 4 (suffix)
	// The result is always 12 bytes regardless of original key length
	b.Grow(12)
	b.WriteString(key[:4])
	b.WriteString("****")
	b.WriteString(key[length-4:])
	return b.String()
}

// TruncateString shortens a string to a maximum length.
func TruncateString(s string, maxLength int) string {
	if len(s) > maxLength {
		return s[:maxLength]
	}
	return s
}

// SplitAndTrim splits a string by a separator
func SplitAndTrim(s string, sep string) []string {
	if s == "" {
		return []string{}
	}

	parts := strings.Split(s, sep)
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}

// StringToSet converts a separator-delimited string into a set
func StringToSet(s string, sep string) map[string]struct{} {
	parts := SplitAndTrim(s, sep)
	if len(parts) == 0 {
		return nil
	}

	set := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		set[part] = struct{}{}
	}
	return set
}
