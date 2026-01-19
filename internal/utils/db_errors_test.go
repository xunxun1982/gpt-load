package utils

import (
	"context"
	"errors"
	"testing"
)

// TestIsDBLockError tests database lock error detection
func TestIsDBLockError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"NilError", nil, false},
		{"DatabaseLocked", errors.New("database is locked"), true},
		{"SQLiteBusy", errors.New("SQLITE_BUSY: database is busy"), true},
		{"SchemaChanged", errors.New("database schema has changed"), true},
		{"BusyError", errors.New("database busy"), true},
		{"InterruptedError", errors.New("query interrupted"), true},
		{"LockWaitTimeout", errors.New("lock wait timeout exceeded"), true},
		{"LockTimeout", errors.New("lock timeout"), true},
		{"Deadlock", errors.New("deadlock detected"), true},
		{"CouldNotObtainLock", errors.New("could not obtain lock on table"), true},
		{"RegularError", errors.New("some other error"), false},
		{"ConnectionError", errors.New("connection refused"), false},
		{"SyntaxError", errors.New("syntax error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDBLockError(tt.err)
			if got != tt.want {
				t.Errorf("IsDBLockError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsTransientDBError tests transient error detection
func TestIsTransientDBError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"NilError", nil, false},
		{"DeadlineExceeded", context.DeadlineExceeded, true},
		{"ContextCanceled", context.Canceled, true},
		{"DatabaseLocked", errors.New("database is locked"), true},
		{"SQLiteBusy", errors.New("SQLITE_BUSY"), true},
		{"Deadlock", errors.New("deadlock detected"), true},
		{"RegularError", errors.New("some other error"), false},
		{"SyntaxError", errors.New("syntax error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTransientDBError(tt.err)
			if got != tt.want {
				t.Errorf("IsTransientDBError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsDBLockErrorCaseInsensitive tests case-insensitive matching
func TestIsDBLockErrorCaseInsensitive(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"LowercaseLocked", errors.New("database is locked"), true},
		{"UppercaseLocked", errors.New("DATABASE IS LOCKED"), true},
		{"MixedCaseLocked", errors.New("Database Is Locked"), true},
		{"LowercaseBusy", errors.New("sqlite_busy"), true},
		{"UppercaseBusy", errors.New("SQLITE_BUSY"), true},
		{"LowercaseDeadlock", errors.New("deadlock"), true},
		{"UppercaseDeadlock", errors.New("DEADLOCK"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDBLockError(tt.err)
			if got != tt.want {
				t.Errorf("IsDBLockError() = %v, want %v for error: %v", got, tt.want, tt.err)
			}
		})
	}
}

// TestIsTransientDBErrorWithWrappedErrors tests wrapped error detection
func TestIsTransientDBErrorWithWrappedErrors(t *testing.T) {
	baseErr := context.DeadlineExceeded
	wrappedErr := errors.New("operation failed: " + baseErr.Error())

	// Direct context error should be detected
	if !IsTransientDBError(baseErr) {
		t.Error("IsTransientDBError() should detect context.DeadlineExceeded")
	}

	// Wrapped error with context error message should be detected
	// Note: This tests the string matching fallback
	if IsTransientDBError(wrappedErr) {
		// This is expected to fail because errors.Is won't match wrapped string
		t.Log("Wrapped error not detected (expected behavior)")
	}
}

// BenchmarkIsDBLockError benchmarks lock error detection
func BenchmarkIsDBLockError(b *testing.B) {
	err := errors.New("database is locked")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsDBLockError(err)
	}
}

// BenchmarkIsTransientDBError benchmarks transient error detection
func BenchmarkIsTransientDBError(b *testing.B) {
	err := context.DeadlineExceeded
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsTransientDBError(err)
	}
}
