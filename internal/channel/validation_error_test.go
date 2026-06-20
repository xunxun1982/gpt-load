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
		padding[i] = byte(32 + rng.Intn(95))
	}

	var compressed bytes.Buffer
	rawBody := append(append([]byte(nil), message...), padding...)
	require.LessOrEqual(t, int64(len(rawBody)), maxValidationErrorBodySize)
	writer, err := gzip.NewWriterLevel(&compressed, gzip.NoCompression)
	require.NoError(t, err)
	_, err = writer.Write(rawBody)
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

func TestValidationSuccessStatusRejectsBinaryBody(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte{0xff, 0xfe, 0xfd, 0x00, 0x81, 0x82})),
	}

	valid, err := validateKeyResponseStatus(resp)

	assert.False(t, valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation response")
}

func TestValidationSuccessStatusRejectsReplacementCharacterBody(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("��G0,tV]�����p4�gy��y���R���{e��U]YՂj_�pr����0zdG!�̄�1]Q���*���'��u�|w�î��^G�,9���\\���,%0F}Fh�ȧ")),
	}

	valid, err := validateKeyResponseStatus(resp)

	assert.False(t, valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation response")
	assert.NotContains(t, err.Error(), "�")
}

func TestValidationSuccessStatusDecodesCompressedJSONBody(t *testing.T) {
	t.Parallel()

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte(`{"id":"resp_test","object":"response"}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
		Body:       io.NopCloser(bytes.NewReader(compressed.Bytes())),
	}

	valid, err := validateKeyResponseStatus(resp)

	require.NoError(t, err)
	assert.True(t, valid)
}

func TestValidationSuccessStatusAcceptsSSEBody(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")),
	}

	valid, err := validateKeyResponseStatus(resp)

	require.NoError(t, err)
	assert.True(t, valid)
}
