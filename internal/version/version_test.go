package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVersion tests the version constant
func TestVersion(t *testing.T) {
	t.Parallel()
	assert.NotEmpty(t, Version, "Version should not be empty")
	// Tightened regex to match full semver format with optional pre-release and build metadata
	assert.Regexp(t, `^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`, Version, "Version should follow semantic versioning format")
}

// versionSink prevents compiler optimization in benchmarks
var versionSink string

// BenchmarkVersionAccess benchmarks version access
func BenchmarkVersionAccess(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		versionSink = Version
	}
}
