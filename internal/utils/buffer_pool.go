package utils

import (
	"bytes"
	"sync"
)

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
func PutBuffer(buf *bytes.Buffer) {
	buf.Reset()
	BufferPool.Put(buf)
}
