package sitemanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTaskTypeConstantsSync verifies that local task type constants match services package
// Uses string literals to avoid import cycle
func TestTaskTypeConstantsSync(t *testing.T) {
	assert.Equal(t, "KEY_IMPORT", taskTypeKeyImport, "taskTypeKeyImport must match services.TaskTypeKeyImport")
	assert.Equal(t, "KEY_DELETE", taskTypeKeyDelete, "taskTypeKeyDelete must match services.TaskTypeKeyDelete")
	assert.Equal(t, "KEY_RESTORE", taskTypeKeyRestore, "taskTypeKeyRestore must match services.TaskTypeKeyRestore")
}
