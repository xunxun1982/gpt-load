package errors

import (
	"errors"
	"net/http"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// TestAPIError_Error tests the Error method implementation
func TestAPIError_Error(t *testing.T) {
	tests := []struct {
		name     string
		apiError *APIError
		expected string
	}{
		{
			name:     "standard error",
			apiError: ErrBadRequest,
			expected: "Invalid request parameters",
		},
		{
			name:     "custom error",
			apiError: &APIError{HTTPStatus: 500, Code: "TEST", Message: "Test message"},
			expected: "Test message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.apiError.Error())
		})
	}
}

// TestPredefinedErrors tests all predefined error constants
func TestPredefinedErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        *APIError
		statusCode int
		code       string
	}{
		{"ErrBadRequest", ErrBadRequest, http.StatusBadRequest, "BAD_REQUEST"},
		{"ErrInvalidJSON", ErrInvalidJSON, http.StatusBadRequest, "INVALID_JSON"},
		{"ErrValidation", ErrValidation, http.StatusBadRequest, "VALIDATION_FAILED"},
		{"ErrDuplicateResource", ErrDuplicateResource, http.StatusConflict, "DUPLICATE_RESOURCE"},
		{"ErrResourceNotFound", ErrResourceNotFound, http.StatusNotFound, "NOT_FOUND"},
		{"ErrInternalServer", ErrInternalServer, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR"},
		{"ErrDatabase", ErrDatabase, http.StatusInternalServerError, "DATABASE_ERROR"},
		{"ErrUnauthorized", ErrUnauthorized, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"ErrForbidden", ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
		{"ErrTaskInProgress", ErrTaskInProgress, http.StatusConflict, "TASK_IN_PROGRESS"},
		{"ErrBadGateway", ErrBadGateway, http.StatusBadGateway, "BAD_GATEWAY"},
		{"ErrNoActiveKeys", ErrNoActiveKeys, http.StatusServiceUnavailable, "NO_ACTIVE_KEYS"},
		{"ErrMaxRetriesExceeded", ErrMaxRetriesExceeded, http.StatusBadGateway, "MAX_RETRIES_EXCEEDED"},
		{"ErrNoKeysAvailable", ErrNoKeysAvailable, http.StatusServiceUnavailable, "NO_KEYS_AVAILABLE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.statusCode, tt.err.HTTPStatus)
			assert.Equal(t, tt.code, tt.err.Code)
			assert.NotEmpty(t, tt.err.Message)
		})
	}
}

// TestNewAPIError tests creating a new API error with custom message
func TestNewAPIError(t *testing.T) {
	customMsg := "Custom error message"
	err := NewAPIError(ErrBadRequest, customMsg)

	assert.Equal(t, ErrBadRequest.HTTPStatus, err.HTTPStatus)
	assert.Equal(t, ErrBadRequest.Code, err.Code)
	assert.Equal(t, customMsg, err.Message)
}

// TestNewAPIErrorWithUpstream tests creating an error from upstream response
func TestNewAPIErrorWithUpstream(t *testing.T) {
	statusCode := http.StatusBadGateway
	code := "UPSTREAM_ERROR"
	message := "Upstream service returned an error"

	err := NewAPIErrorWithUpstream(statusCode, code, message)

	assert.Equal(t, statusCode, err.HTTPStatus)
	assert.Equal(t, code, err.Code)
	assert.Equal(t, message, err.Message)
}

// TestNewValidationError tests creating a validation error
func TestNewValidationError(t *testing.T) {
	message := "Field 'email' is required"
	err := NewValidationError(message)

	assert.Equal(t, ErrValidation.HTTPStatus, err.HTTPStatus)
	assert.Equal(t, ErrValidation.Code, err.Code)
	assert.Equal(t, message, err.Message)
}

// TestNewAuthenticationError tests creating an authentication error
func TestNewAuthenticationError(t *testing.T) {
	message := "Invalid credentials"
	err := NewAuthenticationError(message)

	assert.Equal(t, ErrUnauthorized.HTTPStatus, err.HTTPStatus)
	assert.Equal(t, ErrUnauthorized.Code, err.Code)
	assert.Equal(t, message, err.Message)
}

// TestNewNotFoundError tests creating a not found error
func TestNewNotFoundError(t *testing.T) {
	message := "User not found"
	err := NewNotFoundError(message)

	assert.Equal(t, ErrResourceNotFound.HTTPStatus, err.HTTPStatus)
	assert.Equal(t, ErrResourceNotFound.Code, err.Code)
	assert.Equal(t, message, err.Message)
}

// TestNewForbiddenError tests creating a forbidden error
func TestNewForbiddenError(t *testing.T) {
	message := "Access denied"
	err := NewForbiddenError(message)

	assert.Equal(t, ErrForbidden.HTTPStatus, err.HTTPStatus)
	assert.Equal(t, ErrForbidden.Code, err.Code)
	assert.Equal(t, message, err.Message)
}

// TestParseDBError tests database error parsing
func TestParseDBError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected *APIError
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: nil,
		},
		{
			name:     "record not found",
			err:      gorm.ErrRecordNotFound,
			expected: ErrResourceNotFound,
		},
		{
			name:     "postgres unique violation",
			err:      &pgconn.PgError{Code: "23505"},
			expected: ErrDuplicateResource,
		},
		{
			name:     "mysql duplicate entry",
			err:      &mysql.MySQLError{Number: 1062},
			expected: ErrDuplicateResource,
		},
		{
			name:     "sqlite unique constraint",
			err:      errors.New("UNIQUE constraint failed: users.email"),
			expected: ErrDuplicateResource,
		},
		{
			name:     "generic database error",
			err:      errors.New("database connection failed"),
			expected: ErrDatabase,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseDBError(tt.err)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected.HTTPStatus, result.HTTPStatus)
				assert.Equal(t, tt.expected.Code, result.Code)
			}
		})
	}
}

// BenchmarkNewAPIError benchmarks creating new API errors
func BenchmarkNewAPIError(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewAPIError(ErrBadRequest, "test message")
	}
}

// BenchmarkParseDBError benchmarks database error parsing
func BenchmarkParseDBError(b *testing.B) {
	err := gorm.ErrRecordNotFound
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseDBError(err)
	}
}

// BenchmarkParseDBError_PostgreSQL benchmarks PostgreSQL error parsing
func BenchmarkParseDBError_PostgreSQL(b *testing.B) {
	err := &pgconn.PgError{Code: "23505"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseDBError(err)
	}
}

// BenchmarkParseDBError_MySQL benchmarks MySQL error parsing
func BenchmarkParseDBError_MySQL(b *testing.B) {
	err := &mysql.MySQLError{Number: 1062}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseDBError(err)
	}
}
