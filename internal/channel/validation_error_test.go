package channel

import (
	"bytes"
	"compress/gzip"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"gpt-load/internal/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseValidationErrorResponseDecompressesBeforeLimit(t *testing.T) {
	t.Parallel()

	message := []byte("compressed upstream validation failure ")
	maxRawPadding := int(maxValidationErrorBodySize) - len(message)
	padding := make([]byte, maxRawPadding)
	rng := rand.New(rand.NewSource(1))
	for i := range padding {
		padding[i] = byte(rng.Intn(256))
	}

	var compressed bytes.Buffer
	rawBody := append(append([]byte(nil), message...), padding...)
	require.LessOrEqual(t, int64(len(rawBody)), maxValidationErrorBodySize)
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write(rawBody)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.Greater(t, int64(compressed.Len()), maxValidationErrorBodySize)

	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   io.NopCloser(bytes.NewReader(compressed.Bytes())),
	}

	parsed, err := parseValidationErrorResponse(resp)

	require.NoError(t, err)
	assert.Contains(t, parsed, "compressed upstream validation failure")
	assert.NotContains(t, strings.ToLower(parsed), "failed to create gzip reader")
}

func TestParseValidationErrorResponseRejectsDecompressedBodyOverLimit(t *testing.T) {
	t.Parallel()

	rawBody := strings.Repeat("x", int(maxValidationErrorBodySize)+1)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte(rawBody))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   io.NopCloser(bytes.NewReader(compressed.Bytes())),
	}

	parsed, err := parseValidationErrorResponse(resp)

	require.ErrorIs(t, err, utils.ErrDecompressedTooLarge)
	assert.Empty(t, parsed)
}
