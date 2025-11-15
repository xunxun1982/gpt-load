package utils

import (
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
}

// CategorizeError analyzes an error and returns detailed categorization.
// This implements "fast fail" error handling for better API stability and predictability.
// Performance: O(1) for most error types using string matching.
func CategorizeError(err error) *CategorizedError {
	if err == nil {
		return nil
	}

	errorMessage := strings.ToLower(err.Error())

	// Timeout errors - should retry
	if strings.Contains(errorMessage, "timeout") ||
		strings.Contains(errorMessage, "deadline exceeded") ||
		strings.Contains(errorMessage, "context deadline exceeded") {
		return &CategorizedError{
			Type:       ErrorCategoryTimeout,
			Message:    "Request timeout - the target service took too long to respond",
			StatusCode: http.StatusGatewayTimeout,
			ShouldRetry: true,
		}
	}

	// DNS resolution errors - should retry
	if strings.Contains(errorMessage, "no such host") ||
		strings.Contains(errorMessage, "dns") ||
		strings.Contains(errorMessage, "name resolution") {
		return &CategorizedError{
			Type:       ErrorCategoryDNS,
			Message:    "DNS resolution failed - unable to resolve target hostname",
			StatusCode: http.StatusBadGateway,
			ShouldRetry: true,
		}
	}

	// Connection errors - should retry
	if strings.Contains(errorMessage, "connection refused") ||
		strings.Contains(errorMessage, "connect: connection refused") ||
		strings.Contains(errorMessage, "no route to host") {
		return &CategorizedError{
			Type:       ErrorCategoryConnection,
			Message:    "Connection refused - target service is not accepting connections",
			StatusCode: http.StatusServiceUnavailable,
			ShouldRetry: true,
		}
	}

	// SSL/TLS errors - should NOT retry (configuration issue)
	if strings.Contains(errorMessage, "tls") ||
		strings.Contains(errorMessage, "ssl") ||
		strings.Contains(errorMessage, "certificate") ||
		strings.Contains(errorMessage, "x509") {
		return &CategorizedError{
			Type:       ErrorCategorySSL,
			Message:    "SSL/TLS error - certificate or encryption issue",
			StatusCode: http.StatusBadGateway,
			ShouldRetry: false,
		}
	}

	// Network errors - should retry
	if strings.Contains(errorMessage, "network") ||
		strings.Contains(errorMessage, "connection reset") ||
		strings.Contains(errorMessage, "broken pipe") ||
		strings.Contains(errorMessage, "eof") {
		return &CategorizedError{
			Type:       ErrorCategoryNetwork,
			Message:    "Network error - unable to reach the target service",
			StatusCode: http.StatusBadGateway,
			ShouldRetry: true,
		}
	}

	// Check for specific error types
	if netErr, ok := err.(net.Error); ok {
		if netErr.Timeout() {
			return &CategorizedError{
				Type:       ErrorCategoryTimeout,
				Message:    "Request timeout - the target service took too long to respond",
				StatusCode: http.StatusGatewayTimeout,
				ShouldRetry: true,
			}
		}
	}

	// Check for syscall errors
	if opErr, ok := err.(*net.OpError); ok {
		if opErr.Err == syscall.ECONNREFUSED {
			return &CategorizedError{
				Type:       ErrorCategoryConnection,
				Message:    "Connection refused - target service is not accepting connections",
				StatusCode: http.StatusServiceUnavailable,
				ShouldRetry: true,
			}
		}
	}

	// Unknown error - should retry with caution
	return &CategorizedError{
		Type:       ErrorCategoryUnknown,
		Message:    "Unexpected error: " + err.Error(),
		StatusCode: http.StatusInternalServerError,
		ShouldRetry: true,
	}
}

// ShouldRetryHTTPStatus determines if an HTTP status code should trigger a retry
func ShouldRetryHTTPStatus(statusCode int) bool {
	// Retry on server errors (5xx) and specific client errors
	switch statusCode {
	case http.StatusTooManyRequests,        // 429
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
