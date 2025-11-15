package utils

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"
)

// ErrorCategory represents the type of error encountered during proxy operations.
type ErrorCategory string

const (
	ErrorCategoryTimeout    ErrorCategory = "TIMEOUT"
	ErrorCategoryNetwork    ErrorCategory = "NETWORK"
	ErrorCategoryDNS        ErrorCategory = "DNS"
	ErrorCategoryConnection ErrorCategory = "CONNECTION"
	ErrorCategorySSL        ErrorCategory = "SSL"
	ErrorCategoryUnknown    ErrorCategory = "UNKNOWN"
)

// CategorizedError contains detailed error information with retry guidance.
type CategorizedError struct {
	Type        ErrorCategory
	Message     string
	StatusCode  int
	ShouldRetry bool
	Err         error // Original error for debugging and observability
}

// CategorizeError analyzes an error and returns detailed categorization.
// This implements "fast fail" error handling for better API stability and predictability.
// Performance: O(n) in the length of the error message due to substring checks (n is typically small).
func CategorizeError(err error) *CategorizedError {
	if err == nil {
		return nil
	}

	// Prefer concrete error types first for accuracy and performance.
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return &CategorizedError{
			Type:        ErrorCategoryTimeout,
			Message:     "Request timeout - the target service took too long to respond",
			StatusCode:  http.StatusGatewayTimeout,
			ShouldRetry: true,
			Err:         err,
		}
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return &CategorizedError{
			Type:        ErrorCategoryConnection,
			Message:     "Connection refused - target service is not accepting connections",
			StatusCode:  http.StatusServiceUnavailable,
			ShouldRetry: true,
			Err:         err,
		}
	}

	// Fall back to string-based heuristics for other error types.
	errorMessage := strings.ToLower(err.Error())

	// Timeout errors - string fallback
	if strings.Contains(errorMessage, "timeout") ||
		strings.Contains(errorMessage, "deadline exceeded") ||
		strings.Contains(errorMessage, "context deadline exceeded") {
		return &CategorizedError{
			Type:        ErrorCategoryTimeout,
			Message:     "Request timeout - the target service took too long to respond",
			StatusCode:  http.StatusGatewayTimeout,
			ShouldRetry: true,
			Err:         err,
		}
	}

	// DNS resolution errors - narrower heuristics to avoid misclassification.
	if strings.Contains(errorMessage, "no such host") ||
		strings.Contains(errorMessage, "name resolution") ||
		strings.Contains(errorMessage, "server misbehaving") {
		return &CategorizedError{
			Type:        ErrorCategoryDNS,
			Message:     "DNS resolution failed - unable to resolve target hostname",
			StatusCode:  http.StatusBadGateway,
			ShouldRetry: true,
			Err:         err,
		}
	}

	// Connection errors - string-based cases not covered by specific types.
	if strings.Contains(errorMessage, "connection refused") ||
		strings.Contains(errorMessage, "connect: connection refused") ||
		strings.Contains(errorMessage, "no route to host") {
		return &CategorizedError{
			Type:        ErrorCategoryConnection,
			Message:     "Connection refused - target service is not accepting connections",
			StatusCode:  http.StatusServiceUnavailable,
			ShouldRetry: true,
			Err:         err,
		}
	}

	// SSL/TLS errors - should NOT retry (configuration issue)
	if strings.Contains(errorMessage, "tls") ||
		strings.Contains(errorMessage, "ssl") ||
		strings.Contains(errorMessage, "certificate") ||
		strings.Contains(errorMessage, "x509") {
		return &CategorizedError{
			Type:        ErrorCategorySSL,
			Message:     "SSL/TLS error - certificate or encryption issue",
			StatusCode:  http.StatusBadGateway,
			ShouldRetry: false,
			Err:         err,
		}
	}

	// Network errors - narrower heuristics to avoid generic "network" / "eof" matches.
	if strings.Contains(errorMessage, "network is unreachable") ||
		strings.Contains(errorMessage, "network unreachable") ||
		strings.Contains(errorMessage, "connection reset") ||
		strings.Contains(errorMessage, "broken pipe") ||
		strings.Contains(errorMessage, "unexpected eof") {
		return &CategorizedError{
			Type:        ErrorCategoryNetwork,
			Message:     "Network error - unable to reach the target service",
			StatusCode:  http.StatusBadGateway,
			ShouldRetry: true,
			Err:         err,
		}
	}

	// Unknown error - default to no retry to avoid retry storms; make configurable if needed.
	return &CategorizedError{
		Type:        ErrorCategoryUnknown,
		Message:     "Unexpected error: " + err.Error(),
		StatusCode:  http.StatusInternalServerError,
		ShouldRetry: false,
		Err:         err,
	}
}

// ShouldRetryHTTPStatus determines if an HTTP status code should trigger a retry
func ShouldRetryHTTPStatus(statusCode int) bool {
	// Retry on server errors (5xx) and specific client errors
	switch statusCode {
	case http.StatusTooManyRequests,        // 429
		http.StatusRequestTimeout,          // 408
		http.StatusInternalServerError,     // 500
		http.StatusBadGateway,              // 502
		http.StatusServiceUnavailable,      // 503
		http.StatusGatewayTimeout:          // 504
		return true
	default:
		return false
	}
}

// IsRetryableError checks if an error should trigger a retry
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	categorized := CategorizeError(err)
	return categorized.ShouldRetry
}
