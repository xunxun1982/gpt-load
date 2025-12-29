package utils

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// maxPooledBufferSize is the maximum buffer size to return to pool.
// Buffers larger than this are discarded to prevent memory bloat.
const maxPooledBufferSize = 64 * 1024 // 64KB

// BufferPool manages a pool of bytes.Buffer to reduce garbage collection overhead.
var BufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// GetBuffer retrieves a buffer from the pool.
func GetBuffer() *bytes.Buffer {
	return BufferPool.Get().(*bytes.Buffer)
}

// PutBuffer resets the buffer and returns it to the pool.
// Buffers larger than 64KB are not returned to avoid memory bloat.
func PutBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	// Discard large buffers to prevent memory bloat
	if buf.Cap() > maxPooledBufferSize {
		return
	}
	buf.Reset()
	BufferPool.Put(buf)
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
	if cap(*b) > maxPooledBufferSize {
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
	if enc.buf.Cap() > maxPooledBufferSize {
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
	if sb.Cap() > maxPooledBufferSize {
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
