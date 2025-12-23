package utils

import (
	"context"
	"errors"
	"strings"
)

// IsDBLockError reports whether err looks like a lock contention / deadlock / busy error.
// It is intended for retry/backoff decisions.
func IsDBLockError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "database schema has changed") ||
		strings.Contains(msg, "busy") ||
		strings.Contains(msg, "interrupted") ||
		strings.Contains(msg, "lock wait timeout") ||
		strings.Contains(msg, "lock timeout") ||
		strings.Contains(msg, "deadlock") ||
		strings.Contains(msg, "could not obtain lock")
}

// IsTransientDBError reports whether err is likely transient (timeout/cancel/lock contention).
// It is intended for decisions like serving stale cache or retrying in background jobs.
func IsTransientDBError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	return IsDBLockError(err)
}
