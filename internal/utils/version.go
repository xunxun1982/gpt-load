package utils

import "strings"

// IsDottedNumericVersion reports whether version is an ASCII dotted numeric version
// with at least two non-empty segments, such as 1.2 or 1.2.3.4.
func IsDottedNumericVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
