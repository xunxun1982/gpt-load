package utils

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// Buffer pool size thresholds for tiered pooling strategy
const (
	// Tier 1: Small buffers (most common case)
	smallBufferThreshold = 64 * 1024 // 64KB

	// Tier 2: Medium buffers (larger API responses)
	mediumBufferThreshold = 256 * 1024 // 256KB

	// Tier 3: Large buffers (AI context with moderate length)
	largeBufferThreshold = 1024 * 1024 // 1MB

	// Tier 4: XLarge buffers (AI context with long history)
	xlargeBufferThreshold = 2 * 1024 * 1024 // 2MB

	// Buffers larger than xlargeBufferThreshold are not pooled to prevent excessive memory retention
	// This protects against edge cases like 150MB bulk imports which should not be pooled
)

// BufferPool manages a pool of bytes.Buffer for small requests (most common case).
// This pool handles the majority of requests efficiently with minimal memory overhead.
var BufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// MediumBufferPool manages a pool of bytes.Buffer for medium-sized requests (64KB-256KB).
// Pre-allocates 128KB to reduce reallocation.
var MediumBufferPool = sync.Pool{
	New: func() interface{} {
		buf := new(bytes.Buffer)
		buf.Grow(128 * 1024)
		return buf
	},
}

// LargeBufferPool manages a pool of bytes.Buffer for large requests (256KB-1MB).
// Pre-allocates 512KB to reduce reallocation.
var LargeBufferPool = sync.Pool{
	New: func() interface{} {
		buf := new(bytes.Buffer)
		buf.Grow(512 * 1024)
		return buf
	},
}

// XLargeBufferPool manages a pool of bytes.Buffer for extra large AI context requests (1MB-2MB).
// Pre-allocates 1MB to reduce reallocation for long context requests.
var XLargeBufferPool = sync.Pool{
	New: func() interface{} {
		buf := new(bytes.Buffer)
		buf.Grow(1024 * 1024)
		return buf
	},
}

// GetBuffer retrieves a buffer from the pool.
func GetBuffer() *bytes.Buffer {
	return BufferPool.Get().(*bytes.Buffer)
}

// GetBufferWithCapacity retrieves a buffer from the appropriate pool based on requested capacity.
// This enables efficient use of tiered pools for known buffer sizes.
// Falls back to small pool for unknown or small capacities.
func GetBufferWithCapacity(capacity int) *bytes.Buffer {
	switch {
	case capacity <= smallBufferThreshold:
		return BufferPool.Get().(*bytes.Buffer)
	case capacity <= mediumBufferThreshold:
		return MediumBufferPool.Get().(*bytes.Buffer)
	case capacity <= largeBufferThreshold:
		return LargeBufferPool.Get().(*bytes.Buffer)
	case capacity <= xlargeBufferThreshold:
		return XLargeBufferPool.Get().(*bytes.Buffer)
	default:
		// For huge buffers (>2MB), allocate directly without pooling
		buf := new(bytes.Buffer)
		buf.Grow(capacity)
		return buf
	}
}

// PutBuffer resets the buffer and returns it to the appropriate pool.
// Uses tiered pooling strategy:
// - Tier 1 (<=64KB): returned to small pool (most common case)
// - Tier 2 (64KB-256KB): returned to medium pool (larger API responses)
// - Tier 3 (256KB-1MB): returned to large pool (AI context with moderate length)
// - Tier 4 (1MB-2MB): returned to xlarge pool (AI context with long history)
// - Tier 5 (>2MB): discarded to prevent memory bloat
func PutBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}

	capacity := buf.Cap()

	// Discard huge buffers (>2MB) to prevent memory bloat
	if capacity > xlargeBufferThreshold {
		return
	}

	buf.Reset()

	// Route to appropriate pool based on size
	switch {
	case capacity <= smallBufferThreshold:
		BufferPool.Put(buf)
	case capacity <= mediumBufferThreshold:
		MediumBufferPool.Put(buf)
	case capacity <= largeBufferThreshold:
		LargeBufferPool.Put(buf)
	default:
		XLargeBufferPool.Put(buf)
	}
}

// ByteSlicePool provides reusable byte slices for common operations.
// Default size is 4KB which covers most API request/response bodies.
var ByteSlicePool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 4096)
		return &b
	},
}

// GetByteSlice retrieves a byte slice from the pool.
func GetByteSlice() *[]byte {
	return ByteSlicePool.Get().(*[]byte)
}

// PutByteSlice returns a byte slice to the pool.
// Slices larger than 64KB are not returned to avoid memory bloat.
func PutByteSlice(b *[]byte) {
	if b == nil {
		return
	}
	if cap(*b) > smallBufferThreshold {
		return
	}
	*b = (*b)[:0]
	ByteSlicePool.Put(b)
}

// jsonEncoderPool provides reusable JSON encoders with pooled buffers.
var jsonEncoderPool = sync.Pool{
	New: func() interface{} {
		buf := new(bytes.Buffer)
		return &JSONEncoder{
			buf:     buf,
			encoder: json.NewEncoder(buf),
		}
	},
}

// JSONEncoder wraps json.Encoder with pooled buffer for efficient JSON encoding.
type JSONEncoder struct {
	buf     *bytes.Buffer
	encoder *json.Encoder
}

// GetJSONEncoder retrieves a JSON encoder from the pool.
func GetJSONEncoder() *JSONEncoder {
	enc := jsonEncoderPool.Get().(*JSONEncoder)
	enc.buf.Reset()
	return enc
}

// Encode encodes v to JSON and returns the bytes.
// Note: The returned bytes are only valid until the next call to Encode
// or until PutJSONEncoder is called. Copy the bytes if you need to keep them.
func (e *JSONEncoder) Encode(v interface{}) ([]byte, error) {
	e.buf.Reset()
	if err := e.encoder.Encode(v); err != nil {
		return nil, err
	}
	// Remove trailing newline added by json.Encoder.Encode
	b := e.buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

// PutJSONEncoder returns the encoder to the pool.
func PutJSONEncoder(enc *JSONEncoder) {
	if enc == nil {
		return
	}
	// Discard encoders with large buffers to prevent memory bloat
	if enc.buf.Cap() > smallBufferThreshold {
		return
	}
	jsonEncoderPool.Put(enc)
}

// StringBuilderPool provides reusable string builders.
var StringBuilderPool = sync.Pool{
	New: func() interface{} {
		return new(strings.Builder)
	},
}

// GetStringBuilder retrieves a string builder from the pool.
func GetStringBuilder() *strings.Builder {
	sb := StringBuilderPool.Get().(*strings.Builder)
	sb.Reset()
	return sb
}

// PutStringBuilder returns a string builder to the pool.
func PutStringBuilder(sb *strings.Builder) {
	if sb == nil {
		return
	}
	// Discard large builders to prevent memory bloat
	if sb.Cap() > smallBufferThreshold {
		return
	}
	StringBuilderPool.Put(sb)
}

// MarshalJSON is a helper function that uses pooled encoder for JSON marshaling.
// It returns a newly allocated byte slice containing the JSON encoding.
// This is more efficient than json.Marshal for high-frequency operations.
func MarshalJSON(v interface{}) ([]byte, error) {
	enc := GetJSONEncoder()
	b, err := enc.Encode(v)
	if err != nil {
		PutJSONEncoder(enc)
		return nil, err
	}
	// Make a copy since encoder buffer will be reused
	result := make([]byte, len(b))
	copy(result, b)
	PutJSONEncoder(enc)
	return result, nil
}
