package utils

import "regexp"

// Pre-compiled regex patterns for key parsing and validation.
// These patterns are compiled once at package initialization for better performance.
var (
	// DelimitersPattern matches common delimiters used in key text parsing.
	// Matches whitespace, commas, semicolons, pipes, newlines, carriage returns, and tabs.
	DelimitersPattern = regexp.MustCompile(`[\s,;\n\r\t]+`)

	// ValidKeyCharsPattern validates that a key contains only allowed characters.
	// Allowed characters: alphanumeric, underscore, hyphen, dot, slash, plus, equals, and colon.
	ValidKeyCharsPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-./+=:]+$`)
)
