package utils

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

// TruncateString shortens a string to a maximum length. It is rune-aware to
// avoid cutting multi-byte UTF-8 characters in the middle. For performance,
// it avoids allocating a []rune slice when no truncation is needed.
func TruncateString(s string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLength {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLength])
}

// IsCanonicalHourMinute reports whether value is a valid zero-padded HH:MM time.
func IsCanonicalHourMinute(value string) bool {
	normalized, ok := NormalizeHourMinute(value)
	// Go's time parser accepts a single-digit hour for layout 15, so compare canonical width.
	return ok && normalized == strings.TrimSpace(value)
}

// NormalizeHourMinute converts a valid one- or two-digit hour/minute pair to HH:MM.
func NormalizeHourMinute(value string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return "", false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return "", false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return "", false
	}
	return time.Date(0, time.January, 1, hour, minute, 0, 0, time.UTC).Format("15:04"), true
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
