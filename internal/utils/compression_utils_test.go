package utils

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestNewDecompressReader_NoEncoding verifies that non-compressed data is returned as-is
func TestNewDecompressReader_NoEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		contentEncoding string
		data            string
	}{
		{
			name:            "empty encoding",
			contentEncoding: "",
			data:            "Hello, World!",
		},
		{
			name:            "identity encoding",
			contentEncoding: "identity",
			data:            "Hello, World!",
		},
		{
			name:            "unsupported encoding",
			contentEncoding: "unknown",
			data:            "Hello, World!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a ReadCloser from the test data
			body := io.NopCloser(strings.NewReader(tt.data))

			// Call NewDecompressReader
			reader, err := NewDecompressReader(tt.contentEncoding, body)
			if err != nil {
				t.Fatalf("NewDecompressReader failed: %v", err)
			}
			defer reader.Close()

			// Read the data
			result, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("Failed to read from reader: %v", err)
			}

			// Verify the data is unchanged
			if string(result) != tt.data {
				t.Errorf("Expected %q, got %q", tt.data, string(result))
			}
		})
	}
}

// TestDecompressResponseWithLimit_NoEncoding verifies that non-compressed data is returned as-is
func TestDecompressResponseWithLimit_NoEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		contentEncoding string
		data            []byte
	}{
		{
			name:            "empty encoding",
			contentEncoding: "",
			data:            []byte("Hello, World!"),
		},
		{
			name:            "identity encoding",
			contentEncoding: "identity",
			data:            []byte("Hello, World!"),
		},
		{
			name:            "unsupported encoding",
			contentEncoding: "unknown",
			data:            []byte("Hello, World!"),
		},
		{
			name:            "empty data",
			contentEncoding: "gzip",
			data:            []byte{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call DecompressResponseWithLimit
			result, err := DecompressResponseWithLimit(tt.contentEncoding, tt.data, 1024*1024)
			if err != nil {
				t.Fatalf("DecompressResponseWithLimit failed: %v", err)
			}

			// Verify the data is unchanged
			if !bytes.Equal(result, tt.data) {
				t.Errorf("Expected %q, got %q", tt.data, result)
			}
		})
	}
}

// TestDecompressResponseWithLimit_InvalidGzipData verifies graceful handling of invalid gzip data
func TestDecompressResponseWithLimit_InvalidGzipData(t *testing.T) {
	t.Parallel()
	// Non-gzip data with gzip Content-Encoding header (simulates misconfigured upstream)
	invalidGzipData := []byte("This is not gzip data")

	// Should return original data when decompression fails
	result, err := DecompressResponseWithLimit("gzip", invalidGzipData, 1024*1024)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should return original data unchanged
	if !bytes.Equal(result, invalidGzipData) {
		t.Errorf("Expected original data to be returned unchanged")
	}
}

// TestDecompressResponseWithLimit_ExceedsLimit verifies that ErrDecompressedTooLarge is returned
// when decompressed data exceeds the specified size limit
func TestDecompressResponseWithLimit_ExceedsLimit(t *testing.T) {
	t.Parallel()
	// Create valid gzip data that decompresses to more than the limit
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	// Write data larger than test limit
	testData := bytes.Repeat([]byte("x"), 1000)
	_, err := gw.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}
	err = gw.Close()
	if err != nil {
		t.Fatalf("Failed to close gzip writer: %v", err)
	}

	// Set limit smaller than decompressed size
	result, err := DecompressResponseWithLimit("gzip", buf.Bytes(), 100)
	if !errors.Is(err, ErrDecompressedTooLarge) {
		t.Fatalf("Expected ErrDecompressedTooLarge, got: %v", err)
	}
	if result != nil {
		t.Errorf("Expected nil result when limit exceeded")
	}
}
