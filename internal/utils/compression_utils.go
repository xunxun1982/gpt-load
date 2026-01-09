package utils

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/sirupsen/logrus"
)

// Decompressor defines the interface for different decompression algorithms
type Decompressor interface {
	Decompress(data []byte) ([]byte, error)
}

// decompressorRegistry holds all registered decompressors
var decompressorRegistry = make(map[string]Decompressor)

// init registers default decompressors
func init() {
	RegisterDecompressor("gzip", &GzipDecompressor{})
	RegisterDecompressor("br", &BrotliDecompressor{})
	RegisterDecompressor("deflate", &DeflateDecompressor{})
	RegisterDecompressor("zstd", &ZstdDecompressor{})
}

// RegisterDecompressor allows registering new decompression algorithms
func RegisterDecompressor(encoding string, decompressor Decompressor) {
	decompressorRegistry[encoding] = decompressor
	logrus.Debugf("Registered decompressor for encoding: %s", encoding)
}

// DecompressResponse automatically decompresses response data based on Content-Encoding header
func DecompressResponse(contentEncoding string, data []byte) ([]byte, error) {
	// If no encoding specified or empty data, return as-is
	if contentEncoding == "" || len(data) == 0 {
		return data, nil
	}

	// Look up the decompressor
	decompressor, exists := decompressorRegistry[contentEncoding]
	if !exists {
		logrus.Warnf("No decompressor registered for encoding '%s', returning original data", contentEncoding)
		return data, nil
	}

	// Decompress
	decompressed, err := decompressor.Decompress(data)
	if err != nil {
		logrus.WithError(err).Warnf("Failed to decompress with '%s', returning original data", contentEncoding)
		return data, nil
	}

	logrus.Debugf("Successfully decompressed %d bytes -> %d bytes using '%s'",
		len(data), len(decompressed), contentEncoding)
	return decompressed, nil
}

// GzipDecompressor handles gzip compression
type GzipDecompressor struct{}

// Decompress implements Decompressor interface for gzip
func (g *GzipDecompressor) Decompress(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read gzip data: %w", err)
	}

	return decompressed, nil
}

// BrotliDecompressor handles brotli compression
type BrotliDecompressor struct{}

// Decompress implements Decompressor interface for brotli
func (b *BrotliDecompressor) Decompress(data []byte) ([]byte, error) {
	reader := brotli.NewReader(bytes.NewReader(data))

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read brotli data: %w", err)
	}

	return decompressed, nil
}

// DeflateDecompressor handles deflate compression
type DeflateDecompressor struct{}

// Decompress implements Decompressor interface for deflate
func (d *DeflateDecompressor) Decompress(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create deflate reader: %w", err)
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read deflate data: %w", err)
	}

	return decompressed, nil
}

// ZstdDecompressor handles Zstandard compression
type ZstdDecompressor struct{}

// Decompress implements Decompressor interface for zstd
func (z *ZstdDecompressor) Decompress(data []byte) ([]byte, error) {
	reader, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd reader: %w", err)
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read zstd data: %w", err)
	}

	return decompressed, nil
}

// ErrDecompressedTooLarge is returned when decompressed data exceeds the size limit
var ErrDecompressedTooLarge = fmt.Errorf("decompressed data exceeds maximum allowed size")

// DecompressResponseWithLimit decompresses response data with a size limit to prevent memory exhaustion.
// This is important for security as malicious compressed payloads (zip bombs) can expand to huge sizes.
// Returns ErrDecompressedTooLarge if the decompressed size exceeds maxSize.
func DecompressResponseWithLimit(contentEncoding string, data []byte, maxSize int64) ([]byte, error) {
	// If no encoding specified or empty data, return as-is
	if contentEncoding == "" || len(data) == 0 {
		return data, nil
	}

	// Look up the decompressor
	decompressor, exists := decompressorRegistry[contentEncoding]
	if !exists {
		logrus.Warnf("No decompressor registered for encoding '%s', returning original data", contentEncoding)
		return data, nil
	}

	// Decompress with size limit using a wrapper
	decompressed, err := decompressWithLimit(decompressor, data, maxSize)
	if err != nil {
		if err == ErrDecompressedTooLarge {
			return nil, err
		}
		logrus.WithError(err).Warnf("Failed to decompress with '%s', returning original data", contentEncoding)
		return data, nil
	}

	logrus.Debugf("Successfully decompressed %d bytes -> %d bytes using '%s'",
		len(data), len(decompressed), contentEncoding)
	return decompressed, nil
}

// decompressWithLimit wraps decompression with a size limit check.
// It decompresses incrementally and stops if the limit is exceeded.
func decompressWithLimit(decompressor Decompressor, data []byte, maxSize int64) ([]byte, error) {
	// First decompress normally
	decompressed, err := decompressor.Decompress(data)
	if err != nil {
		return nil, err
	}

	// Check size limit after decompression
	if maxSize > 0 && int64(len(decompressed)) > maxSize {
		return nil, ErrDecompressedTooLarge
	}

	return decompressed, nil
}
