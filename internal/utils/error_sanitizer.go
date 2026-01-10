// Package utils provides utility functions for the application.
package utils

import (
	"regexp"
	"strings"
)

// Compiled regex patterns for sensitive data detection.
// Pre-compiled for performance in hot paths.
var (
	// API key patterns (OpenAI sk-..., Anthropic, etc.)
	// Matches standalone API keys like sk-1234567890abcdefghij or sk-proj-abcdefghij...
	// Per AI review: allow hyphens after sk- to cover formats like sk-proj-...
	apiKeyStandalonePattern = regexp.MustCompile(`\bsk-[a-zA-Z0-9][a-zA-Z0-9-]{19,}\b`)
	// Bearer token pattern (avoid spanning newlines)
	bearerPattern = regexp.MustCompile(`(?i)\bBearer[ \t]+[a-zA-Z0-9\-._~+/]+=*`)
	// Authorization header pattern (redact entire value on the line)
	authHeaderPattern = regexp.MustCompile(`(?im)\bAuthorization:\s*[^\r\n]*`)
	// Generic secret/password/token patterns in JSON
	// Added "authorization" to key list per AI review
	secretJSONPattern = regexp.MustCompile(`(?i)"(api_key|apikey|secret|password|token|auth|authorization|credential|private_key)":\s*"[^"]*"`)
	// Email pattern (basic PII)
	emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	// Per AI review: add cloud provider key patterns for broader coverage
	// AWS Access Key ID pattern (starts with AKIA, ABIA, ACCA, ASIA)
	awsKeyPattern = regexp.MustCompile(`\b(AKIA|ABIA|ACCA|ASIA)[A-Z0-9]{16}\b`)
	// AWS Secret Access Key pattern (40 character base64-like string)
	awsSecretPattern = regexp.MustCompile(`(?i)(aws_secret_access_key|aws_secret_key)\s*[=:]\s*[A-Za-z0-9/+=]{40}`)
)

// SanitizeErrorBody removes or masks sensitive data from error response bodies
// before logging. This helps prevent accidental leakage of API keys, tokens,
// passwords, and other sensitive information in logs.
//
// IMPORTANT: Always call SanitizeErrorBody BEFORE TruncateString to prevent
// leaking truncated secrets. If truncation cuts a token, it may no longer match
// the sanitization regex patterns.
//
// Correct usage:
//
//	safePreview := utils.TruncateString(utils.SanitizeErrorBody(body), 512)
//
// Incorrect usage (may leak secrets):
//
//	safePreview := utils.SanitizeErrorBody(utils.TruncateString(body, 512))
//
// Patterns redacted:
// - API keys (sk-...)
// - Bearer tokens
// - Authorization header values
// - JSON fields containing secrets/passwords/tokens
// - Email addresses (basic PII protection)
func SanitizeErrorBody(body string) string {
	if body == "" {
		return body
	}

	// Apply redaction patterns in order of specificity
	result := body

	// Redact JSON secret fields first (most specific)
	result = secretJSONPattern.ReplaceAllStringFunc(result, func(match string) string {
		// Extract the field name and redact only the value
		idx := strings.Index(match, ":")
		if idx > 0 {
			return match[:idx+1] + ` "[REDACTED]"`
		}
		return "[REDACTED_SECRET]"
	})

	// Redact standalone API keys (sk-...)
	result = apiKeyStandalonePattern.ReplaceAllString(result, "[REDACTED_API_KEY]")

	// Redact Bearer tokens
	result = bearerPattern.ReplaceAllString(result, "Bearer [REDACTED]")

	// Redact Authorization header values
	result = authHeaderPattern.ReplaceAllString(result, "Authorization: [REDACTED]")

	// Redact email addresses
	result = emailPattern.ReplaceAllString(result, "[REDACTED_EMAIL]")

	// Per AI review: redact cloud provider keys for broader coverage
	// Redact AWS Access Key IDs
	result = awsKeyPattern.ReplaceAllString(result, "[REDACTED_AWS_KEY]")
	// Redact AWS Secret Access Keys
	result = awsSecretPattern.ReplaceAllString(result, "[REDACTED_AWS_SECRET]")

	return result
}
