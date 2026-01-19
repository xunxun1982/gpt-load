package utils

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/sirupsen/logrus"
)

// Decompressor defines the interface for different decompression algorithms
type Decompressor interface {
	Decompress(data []byte) ([]byte, error)
	// NewReader creates a streaming reader for decompression.
	// Returns the reader and a cleanup function that must be called when done.
	NewReader(data []byte) (io.Reader, func(), error)
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

// NewReader creates a streaming gzip reader
func (g *GzipDecompressor) NewReader(data []byte) (io.Reader, func(), error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	cleanup := func() { reader.Close() }
	return reader, cleanup, nil
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

// NewReader creates a streaming brotli reader
func (b *BrotliDecompressor) NewReader(data []byte) (io.Reader, func(), error) {
	reader := brotli.NewReader(bytes.NewReader(data))
	// Brotli reader doesn't need explicit close
	cleanup := func() {}
	return reader, cleanup, nil
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

// NewReader creates a streaming deflate reader
func (d *DeflateDecompressor) NewReader(data []byte) (io.Reader, func(), error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create deflate reader: %w", err)
	}
	cleanup := func() { reader.Close() }
	return reader, cleanup, nil
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

// NewReader creates a streaming zstd reader
func (z *ZstdDecompressor) NewReader(data []byte) (io.Reader, func(), error) {
	reader, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create zstd reader: %w", err)
	}
	cleanup := func() { reader.Close() }
	return reader, cleanup, nil
}

// ErrDecompressedTooLarge is returned when decompressed data exceeds the size limit.
// Using errors.New instead of fmt.Errorf for sentinel errors is more idiomatic
// and avoids the overhead of fmt.Errorf when no formatting is needed.
var ErrDecompressedTooLarge = errors.New("decompressed data exceeds maximum allowed size")

// compositeReadCloser wraps a reader with multiple closers
type compositeReadCloser struct {
	io.Reader
	closers []func() error
}

// Close calls all closers in order
func (c *compositeReadCloser) Close() error {
	var firstErr error
	for _, closer := range c.closers {
		if err := closer(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// NewDecompressReader creates a decompression reader for streaming responses.
// It wraps the original reader with the appropriate decompression reader based on Content-Encoding.
// The returned reader must be closed by the caller.
// Supports: gzip, deflate, br (brotli), zstd
// Content-Encoding is normalized (lowercase, trimmed) to handle case/whitespace variants
func NewDecompressReader(contentEncoding string, body io.ReadCloser) (io.ReadCloser, error) {
	// Normalize encoding to handle case/whitespace variants (e.g., "GZip", " gzip ")
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	if encoding == "" || encoding == "identity" {
		return body, nil
	}

	switch encoding {
	case "gzip":
		gzipReader, err := gzip.NewReader(body)
		if err != nil {
			// Close body on decoder creation failure to prevent resource leak
			body.Close()
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		return &compositeReadCloser{
			Reader: gzipReader,
			closers: []func() error{
				gzipReader.Close,
				body.Close,
			},
		}, nil

	case "deflate":
		deflateReader, err := zlib.NewReader(body)
		if err != nil {
			// Close body on decoder creation failure to prevent resource leak
			body.Close()
			return nil, fmt.Errorf("failed to create deflate reader: %w", err)
		}
		return &compositeReadCloser{
			Reader: deflateReader,
			closers: []func() error{
				deflateReader.Close,
				body.Close,
			},
		}, nil

	case "br":
		brotliReader := brotli.NewReader(body)
		return &compositeReadCloser{
			Reader: brotliReader,
			closers: []func() error{
				body.Close,
			},
		}, nil

	case "zstd":
		zstdReader, err := zstd.NewReader(body)
		if err != nil {
			// Close body on decoder creation failure to prevent resource leak
			body.Close()
			return nil, fmt.Errorf("failed to create zstd reader: %w", err)
		}
		return &compositeReadCloser{
			Reader: zstdReader,
			closers: []func() error{
				func() error {
					zstdReader.Close()
					return nil
				},
				body.Close,
			},
		}, nil

	default:
		logrus.Warnf("Unsupported content encoding '%s', returning original body", contentEncoding)
		return body, nil
	}
}

// DecompressResponseWithLimit decompresses response data with a size limit to prevent memory exhaustion.
// This uses io.LimitReader to stop decompression early when the limit is reached, preventing zip bomb attacks.
// Returns ErrDecompressedTooLarge if the decompressed size exceeds maxSize.
// maxSize must be >= 0; values close to math.MaxInt64 should be avoided to prevent overflow.
func DecompressResponseWithLimit(contentEncoding string, data []byte, maxSize int64) ([]byte, error) {
	// If no encoding specified or empty data, return as-is
	if contentEncoding == "" || len(data) == 0 {
		return data, nil
	}

	// Validate maxSize to prevent edge cases and overflow
	if maxSize < 0 {
		return nil, fmt.Errorf("maxSize must be >= 0: %d", maxSize)
	}

	// Look up the decompressor
	decompressor, exists := decompressorRegistry[contentEncoding]
	if !exists {
		logrus.Warnf("No decompressor registered for encoding '%s', returning original data", contentEncoding)
		return data, nil
	}

	// Create streaming reader for decompression
	reader, cleanup, err := decompressor.NewReader(data)
	if err != nil {
		logrus.WithError(err).Warnf("Failed to create decompression reader for '%s', returning original data", contentEncoding)
		return data, nil
	}
	defer cleanup()

	// Use io.LimitReader to prevent reading more than maxSize+1 bytes.
	// Reading maxSize+1 allows us to detect if the data exceeds the limit.
	// This stops decompression early, preventing zip bomb memory exhaustion.
	// Guard against overflow when maxSize is very large (close to MaxInt64)
	limit := maxSize + 1
	if maxSize > (1<<62) { // Avoid overflow for very large maxSize values
		limit = maxSize
	}
	limitedReader := io.LimitReader(reader, limit)
	decompressed, err := io.ReadAll(limitedReader)
	if err != nil {
		logrus.WithError(err).Warnf("Failed to decompress with '%s', returning original data", contentEncoding)
		return data, nil
	}

	// Check if we hit the limit (read more than maxSize bytes)
	if int64(len(decompressed)) > maxSize {
		return nil, ErrDecompressedTooLarge
	}

	logrus.Debugf("Successfully decompressed %d bytes -> %d bytes using '%s'",
		len(data), len(decompressed), contentEncoding)
	return decompressed, nil
}
