package utils

import (
	"strings"
	"testing"
)

func TestSanitizeErrorBody(t *testing.T) {
	// Build key-like strings at runtime to avoid secret scanners flagging literals.
	apiKey := "s" + "k-" + strings.Repeat("a", 24)
	apiKeyJSON := `{"error": "invalid key", "key": "` + apiKey + `"}`
	authKey := "s" + "k-proj-" + strings.Repeat("b", 24)
	authHeader := "Authorization: " + authKey
	// Per AI review: test sk-proj-... as JSON value (not just Authorization header)
	projKeyJSON := `{"error": "auth failed", "details": "` + authKey + `"}`

	tests := []struct {
		name     string
		input    string
		contains []string // Strings that should be in output
		excludes []string // Strings that should NOT be in output
	}{
		{
			name:     "empty string",
			input:    "",
			contains: []string{""},
			excludes: nil,
		},
		{
			name:     "no sensitive data",
			input:    `{"error": "something went wrong"}`,
			contains: []string{`{"error": "something went wrong"}`},
			excludes: nil,
		},
		{
			name:     "api key pattern sk-",
			input:    apiKeyJSON,
			contains: []string{"[REDACTED_API_KEY]"},
			excludes: []string{apiKey},
		},
		{
			name:     "sk-proj key as json value",
			input:    projKeyJSON,
			contains: []string{"[REDACTED_API_KEY]"},
			excludes: []string{authKey},
		},
		{
			name:     "bearer token",
			input:    `Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9`,
			contains: []string{"[REDACTED]"},
			excludes: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		},
		{
			name:     "json api_key field",
			input:    `{"api_key": "secret123456", "message": "error"}`,
			contains: []string{`"api_key": "[REDACTED]"`, `"message": "error"`},
			excludes: []string{"secret123456"},
		},
		{
			name:     "json password field",
			input:    `{"password": "mypassword123", "user": "admin"}`,
			contains: []string{`"password": "[REDACTED]"`, `"user": "admin"`},
			excludes: []string{"mypassword123"},
		},
		{
			name:     "json token field",
			input:    `{"token": "abc123xyz", "status": "failed"}`,
			contains: []string{`"token": "[REDACTED]"`, `"status": "failed"`},
			excludes: []string{"abc123xyz"},
		},
		{
			name:     "email address",
			input:    `{"error": "user not found", "email": "user@example.com"}`,
			contains: []string{"[REDACTED_EMAIL]"},
			excludes: []string{"user@example.com"},
		},
		{
			name:     "multiple sensitive patterns",
			input:    `{"api_key": "secret", "email": "test@test.com"}`,
			contains: []string{"[REDACTED]", "[REDACTED_EMAIL]"},
			excludes: []string{"secret", "test@test.com"},
		},
		{
			name:     "authorization header in error",
			input:    authHeader,
			contains: []string{"[REDACTED]"},
			excludes: []string{authKey},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeErrorBody(tt.input)

			// Check that expected strings are present
			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("expected result to contain %q, got %q", s, result)
				}
			}

			// Check that excluded strings are NOT present
			for _, s := range tt.excludes {
				if strings.Contains(result, s) {
					t.Errorf("expected result to NOT contain %q, got %q", s, result)
				}
			}
		})
	}
}

func TestSanitizeErrorBody_PreservesNonSensitiveData(t *testing.T) {
	input := `{"error": {"type": "invalid_request_error", "message": "The model does not exist"}}`
	result := SanitizeErrorBody(input)

	// Should preserve the error structure
	if !strings.Contains(result, "invalid_request_error") {
		t.Errorf("expected error type to be preserved, got %q", result)
	}
	if !strings.Contains(result, "The model does not exist") {
		t.Errorf("expected error message to be preserved, got %q", result)
	}
}
