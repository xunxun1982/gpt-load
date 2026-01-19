package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVersion tests the version constant
func TestVersion(t *testing.T) {
	assert.NotEmpty(t, Version, "Version should not be empty")
	assert.Regexp(t, `^\d+\.\d+\.\d+`, Version, "Version should follow semantic versioning format")
}

// BenchmarkVersionAccess benchmarks version access
func BenchmarkVersionAccess(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Version
	}
}
