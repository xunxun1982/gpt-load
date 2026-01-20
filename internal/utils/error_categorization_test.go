package utils

import (
	"errors"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

// mockNetError implements net.Error for testing
type mockNetError struct {
	timeout   bool
	temporary bool
	msg       string
}

func (e *mockNetError) Error() string   { return e.msg }
func (e *mockNetError) Timeout() bool   { return e.timeout }
func (e *mockNetError) Temporary() bool { return e.temporary }

// TestCategorizeError tests error categorization
func TestCategorizeError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		wantType       ErrorCategory
		wantRetry      bool
		wantStatusCode int
	}{
		{
			"NilError",
			nil,
			"",
			false,
			0,
		},
		{
			"TimeoutError",
			&mockNetError{timeout: true, msg: "timeout"},
			ErrorCategoryTimeout,
			true,
			http.StatusGatewayTimeout,
		},
		{
			"ConnectionRefused",
			syscall.ECONNREFUSED,
			ErrorCategoryConnection,
			true,
			http.StatusServiceUnavailable,
		},
		{
			"TimeoutString",
			errors.New("request timeout"),
			ErrorCategoryTimeout,
			true,
			http.StatusGatewayTimeout,
		},
		{
			"DeadlineExceeded",
			errors.New("context deadline exceeded"),
			ErrorCategoryTimeout,
			true,
			http.StatusGatewayTimeout,
		},
		{
			"DNSError",
			errors.New("no such host"),
			ErrorCategoryDNS,
			true,
			http.StatusBadGateway,
		},
		{
			"DNSResolution",
			errors.New("name resolution failed"),
			ErrorCategoryDNS,
			true,
			http.StatusBadGateway,
		},
		{
			"ConnectionRefusedString",
			errors.New("connection refused"),
			ErrorCategoryConnection,
			true,
			http.StatusServiceUnavailable,
		},
		{
			"NoRouteToHost",
			errors.New("no route to host"),
			ErrorCategoryConnection,
			true,
			http.StatusServiceUnavailable,
		},
		{
			"TLSError",
			errors.New("tls handshake failed"),
			ErrorCategorySSL,
			false,
			http.StatusBadGateway,
		},
		{
			"CertificateError",
			errors.New("x509: certificate has expired"),
			ErrorCategorySSL,
			false,
			http.StatusBadGateway,
		},
		{
			"NetworkUnreachable",
			errors.New("network is unreachable"),
			ErrorCategoryNetwork,
			true,
			http.StatusBadGateway,
		},
		{
			"ConnectionReset",
			errors.New("connection reset by peer"),
			ErrorCategoryNetwork,
			true,
			http.StatusBadGateway,
		},
		{
			"BrokenPipe",
			errors.New("broken pipe"),
			ErrorCategoryNetwork,
			true,
			http.StatusBadGateway,
		},
		{
			"UnexpectedEOF",
			errors.New("unexpected EOF"),
			ErrorCategoryNetwork,
			true,
			http.StatusBadGateway,
		},
		{
			"UnknownError",
			errors.New("some random error"),
			ErrorCategoryUnknown,
			false,
			http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)

			if tt.err == nil {
				if result != nil {
					t.Errorf("CategorizeError() = %v, want nil", result)
				}
				return
			}

			if result == nil {
				t.Fatal("CategorizeError() returned nil for non-nil error")
			}

			if result.Type != tt.wantType {
				t.Errorf("CategorizeError() Type = %v, want %v", result.Type, tt.wantType)
			}

			if result.ShouldRetry != tt.wantRetry {
				t.Errorf("CategorizeError() ShouldRetry = %v, want %v", result.ShouldRetry, tt.wantRetry)
			}

			if result.StatusCode != tt.wantStatusCode {
				t.Errorf("CategorizeError() StatusCode = %v, want %v", result.StatusCode, tt.wantStatusCode)
			}

			if result.Err != tt.err {
				t.Errorf("CategorizeError() Err = %v, want %v", result.Err, tt.err)
			}
		})
	}
}

// TestShouldRetryHTTPStatus tests HTTP status retry logic
func TestShouldRetryHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{"OK", http.StatusOK, false},
		{"BadRequest", http.StatusBadRequest, false},
		{"Unauthorized", http.StatusUnauthorized, false},
		{"Forbidden", http.StatusForbidden, false},
		{"NotFound", http.StatusNotFound, false},
		{"RequestTimeout", http.StatusRequestTimeout, true},
		{"TooManyRequests", http.StatusTooManyRequests, true},
		{"InternalServerError", http.StatusInternalServerError, true},
		{"BadGateway", http.StatusBadGateway, true},
		{"ServiceUnavailable", http.StatusServiceUnavailable, true},
		{"GatewayTimeout", http.StatusGatewayTimeout, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRetryHTTPStatus(tt.statusCode)
			if got != tt.want {
				t.Errorf("ShouldRetryHTTPStatus(%d) = %v, want %v", tt.statusCode, got, tt.want)
			}
		})
	}
}

// TestIsRetryableError tests retryable error detection
func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"NilError", nil, false},
		{"TimeoutError", &mockNetError{timeout: true, msg: "timeout"}, true},
		{"DNSError", errors.New("no such host"), true},
		{"SSLError", errors.New("tls error"), false},
		{"UnknownError", errors.New("random error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCategorizeErrorCaseInsensitive tests case-insensitive error matching
func TestCategorizeErrorCaseInsensitive(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantType ErrorCategory
	}{
		{"LowercaseTimeout", errors.New("timeout"), ErrorCategoryTimeout},
		{"UppercaseTimeout", errors.New("TIMEOUT"), ErrorCategoryTimeout},
		{"MixedCaseTimeout", errors.New("TimeOut"), ErrorCategoryTimeout},
		{"LowercaseTLS", errors.New("tls error"), ErrorCategorySSL},
		{"UppercaseTLS", errors.New("TLS ERROR"), ErrorCategorySSL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CategorizeError(tt.err)
			if result.Type != tt.wantType {
				t.Errorf("CategorizeError() Type = %v, want %v", result.Type, tt.wantType)
			}
		})
	}
}

// BenchmarkCategorizeError benchmarks error categorization
func BenchmarkCategorizeError(b *testing.B) {
	err := errors.New("connection timeout")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CategorizeError(err)
	}
}

// BenchmarkCategorizeErrorNetError benchmarks net.Error categorization
func BenchmarkCategorizeErrorNetError(b *testing.B) {
	err := &mockNetError{timeout: true, msg: "timeout"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CategorizeError(err)
	}
}

// BenchmarkShouldRetryHTTPStatus benchmarks HTTP status retry check
func BenchmarkShouldRetryHTTPStatus(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ShouldRetryHTTPStatus(http.StatusServiceUnavailable)
	}
}

// TestRealNetError tests with real net.Error
// This test is gated behind ENABLE_NETWORK_TESTS to avoid CI flakiness
func TestRealNetError(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real network test in short mode")
	}
	if os.Getenv("ENABLE_NETWORK_TESTS") == "" {
		t.Skip("Skipping real network test (set ENABLE_NETWORK_TESTS=1 to enable)")
	}

	// Create a real timeout error using TEST-NET-1 (RFC 5737)
	// 192.0.2.1 is reserved for documentation and should not respond
	conn, err := net.DialTimeout("tcp", "192.0.2.1:80", 10*time.Millisecond)
	if conn != nil {
		conn.Close()
		t.Fatal("Unexpected successful connection to TEST-NET-1 address")
	}

	if err == nil {
		t.Fatal("Expected dial error but got nil")
	}

	result := CategorizeError(err)
	// 192.0.2.1 is TEST-NET-1, should produce timeout or network error
	if result.Type != ErrorCategoryTimeout && result.Type != ErrorCategoryNetwork && result.Type != ErrorCategoryConnection {
		t.Errorf("Unexpected categorization for network error: got %v, want timeout/network/connection", result.Type)
	}
}
