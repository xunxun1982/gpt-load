package errors

import "strings"

// unCountedSubstrings contains a list of substrings that indicate an error
var unCountedSubstrings = []string{
	"resource has been exhausted",
	"please reduce the length of the messages",
}

// IsUnCounted checks if the given error message contains substrings
func IsUnCounted(errorMsg string) bool {
	if errorMsg == "" {
		return false
	}

	needsFold := false
	for _, pattern := range unCountedSubstrings {
		if strings.Contains(errorMsg, pattern) {
			return true
		}
	}
	for i := 0; i < len(errorMsg); i++ {
		if errorMsg[i] >= 'A' && errorMsg[i] <= 'Z' {
			needsFold = true
			break
		}
	}
	if !needsFold {
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
