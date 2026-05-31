package failover

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStatusCodeMatcher(t *testing.T) {
	t.Parallel()

	matcher, err := ParseStatusCodeMatcher("400-403, 405, 500-502")
	require.NoError(t, err)

	for _, code := range []int{400, 401, 403, 405, 500, 502} {
		assert.True(t, matcher.Match(code), "status %d should match", code)
	}
	for _, code := range []int{399, 404, 406, 503} {
		assert.False(t, matcher.Match(code), "status %d should not match", code)
	}
}

func TestParseStatusCodeMatcherMergesRanges(t *testing.T) {
	t.Parallel()

	matcher, err := ParseStatusCodeMatcher("502,500-501,503-504")
	require.NoError(t, err)

	assert.True(t, matcher.Match(500))
	assert.True(t, matcher.Match(504))
	assert.False(t, matcher.Match(505))
}

func TestParseStatusCodeMatcherAllowsBlankPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
	}{
		{name: "empty", pattern: ""},
		{name: "whitespace", pattern: " \t "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matcher, err := ParseStatusCodeMatcher(tt.pattern)
			require.NoError(t, err)
			assert.True(t, matcher.IsZero())
		})
	}
}

func TestParseStatusCodeMatcherRejectsInvalidPattern(t *testing.T) {
	t.Parallel()

	tests := []string{
		"400,",
		"99",
		"1000",
		"500-400",
		"abc",
		"400-401-402",
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			t.Parallel()
			_, err := ParseStatusCodeMatcher(tt)
			assert.Error(t, err)
		})
	}
}
