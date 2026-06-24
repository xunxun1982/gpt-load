package sitemanagement

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStealthHeadersMatchChromeProfileVersion(t *testing.T) {
	t.Parallel()

	headers := stealthHeaders()

	assert.Contains(t, headers["User-Agent"], "Chrome/146.0.0.0")
	assert.Contains(t, headers["Sec-Ch-Ua"], `v="146"`)
	assert.NotContains(t, strings.ToLower(headers["Sec-Ch-Ua"]), "120")
	assert.NotContains(t, headers, "Accept-Encoding")
}
