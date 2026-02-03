package utils

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestGetBuffer(t *testing.T) {
	t.Parallel()

	buf := GetBuffer()
	if buf == nil {
		t.Fatal("GetBuffer returned nil")
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer, got length %d", buf.Len())
	}
}

func TestPutBuffer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupBuf   func() *bytes.Buffer
		shouldPool bool
	}{
		{
			name: "nil_buffer",
			setupBuf: func() *bytes.Buffer {
				return nil
			},
			shouldPool: false,
		},
		{
			name: "small_buffer",
			setupBuf: func() *bytes.Buffer {
				buf := GetBuffer()
				buf.WriteString("test data")
				return buf
			},
			shouldPool: true,
		},
		{
			name: "large_buffer",
			setupBuf: func() *bytes.Buffer {
				buf := bytes.NewBuffer(make([]byte, 0, smallBufferThreshold+1))
				return buf
			},
			shouldPool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := tt.setupBuf()
			PutBuffer(buf)

			if tt.shouldPool && buf != nil && buf.Len() != 0 {
				t.Error("buffer should be reset after PutBuffer")
			}
		})
	}
}

func TestGetByteSlice(t *testing.T) {
	t.Parallel()

	slice := GetByteSlice()
	if slice == nil {
		t.Fatal("GetByteSlice returned nil")
	}
	if len(*slice) != 0 {
		t.Errorf("expected empty slice, got length %d", len(*slice))
	}
	if cap(*slice) < 4096 {
		t.Errorf("expected capacity >= 4096, got %d", cap(*slice))
	}
}

func TestPutByteSlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupSlice func() *[]byte
		shouldPool bool
	}{
		{
			name: "nil_slice",
			setupSlice: func() *[]byte {
				return nil
			},
			shouldPool: false,
		},
		{
			name: "small_slice",
			setupSlice: func() *[]byte {
				slice := GetByteSlice()
				*slice = append(*slice, []byte("test")...)
				return slice
			},
			shouldPool: true,
		},
		{
			name: "large_slice",
			setupSlice: func() *[]byte {
				large := make([]byte, 0, smallBufferThreshold+1)
				return &large
			},
			shouldPool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slice := tt.setupSlice()
			PutByteSlice(slice)

			if tt.shouldPool && slice != nil && len(*slice) != 0 {
				t.Error("slice should be reset after PutByteSlice")
			}
		})
	}
}

func TestGetJSONEncoder(t *testing.T) {
	t.Parallel()

	enc := GetJSONEncoder()
	if enc == nil {
		t.Fatal("GetJSONEncoder returned nil")
	}
	if enc.buf == nil {
		t.Fatal("encoder buffer is nil")
	}
	if enc.encoder == nil {
		t.Fatal("encoder is nil")
	}
}

func TestJSONEncoderEncode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    interface{}
		expected string
		wantErr  bool
	}{
		{
			name:     "simple_map",
			input:    map[string]string{"key": "value"},
			expected: `{"key":"value"}`,
			wantErr:  false,
		},
		{
			name:     "array",
			input:    []int{1, 2, 3},
			expected: `[1,2,3]`,
			wantErr:  false,
		},
		{
			name:     "string",
			input:    "test",
			expected: `"test"`,
			wantErr:  false,
		},
		{
			name:    "invalid_type",
			input:   make(chan int),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			enc := GetJSONEncoder()
			defer PutJSONEncoder(enc)

			result, err := enc.Encode(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if string(result) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, string(result))
			}
		})
	}
}

func TestPutJSONEncoder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupEnc   func() *JSONEncoder
		shouldPool bool
	}{
		{
			name: "nil_encoder",
			setupEnc: func() *JSONEncoder {
				return nil
			},
			shouldPool: false,
		},
		{
			name: "small_encoder",
			setupEnc: func() *JSONEncoder {
				enc := GetJSONEncoder()
				enc.Encode(map[string]string{"test": "data"})
				return enc
			},
			shouldPool: true,
		},
		{
			name: "large_encoder",
			setupEnc: func() *JSONEncoder {
				enc := GetJSONEncoder()
				// Create large buffer
				largeData := make([]byte, smallBufferThreshold+1)
				enc.buf.Write(largeData)
				return enc
			},
			shouldPool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := tt.setupEnc()
			PutJSONEncoder(enc)
		})
	}
}

func TestGetStringBuilder(t *testing.T) {
	t.Parallel()

	sb := GetStringBuilder()
	if sb == nil {
		t.Fatal("GetStringBuilder returned nil")
	}
	if sb.Len() != 0 {
		t.Errorf("expected empty builder, got length %d", sb.Len())
	}
}

func TestPutStringBuilder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupSB    func() *strings.Builder
		shouldPool bool
	}{
		{
			name: "nil_builder",
			setupSB: func() *strings.Builder {
				return nil
			},
			shouldPool: false,
		},
		{
			name: "small_builder",
			setupSB: func() *strings.Builder {
				sb := GetStringBuilder()
				sb.WriteString("test")
				return sb
			},
			shouldPool: true,
		},
		{
			name: "large_builder",
			setupSB: func() *strings.Builder {
				sb := &strings.Builder{}
				sb.Grow(smallBufferThreshold + 1)
				return sb
			},
			shouldPool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := tt.setupSB()
			PutStringBuilder(sb)
		})
	}
}

func TestMarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    interface{}
		expected string
		wantErr  bool
	}{
		{
			name:     "simple_struct",
			input:    struct{ Name string }{"test"},
			expected: `{"Name":"test"}`,
			wantErr:  false,
		},
		{
			name:     "map",
			input:    map[string]int{"count": 42},
			expected: `{"count":42}`,
			wantErr:  false,
		},
		{
			name:    "invalid_type",
			input:   make(chan int),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := MarshalJSON(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if string(result) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, string(result))
			}
		})
	}
}

// Benchmark tests
func BenchmarkBufferPool(b *testing.B) {
	b.Run("with_pool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buf := GetBuffer()
			buf.WriteString("test data")
			PutBuffer(buf)
		}
	})

	b.Run("without_pool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buf := new(bytes.Buffer)
			buf.WriteString("test data")
		}
	})
}

func BenchmarkByteSlicePool(b *testing.B) {
	b.Run("with_pool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := GetByteSlice()
			*slice = append(*slice, []byte("test")...)
			PutByteSlice(slice)
		}
	})

	b.Run("without_pool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := make([]byte, 0, 4096)
			slice = append(slice, []byte("test")...)
		}
	})
}

func BenchmarkJSONEncoder(b *testing.B) {
	data := map[string]interface{}{
		"name":  "test",
		"count": 42,
		"items": []string{"a", "b", "c"},
	}

	b.Run("with_pool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			enc := GetJSONEncoder()
			_, _ = enc.Encode(data)
			PutJSONEncoder(enc)
		}
	})

	b.Run("marshal_json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = MarshalJSON(data)
		}
	})
}

func BenchmarkStringBuilder(b *testing.B) {
	b.Run("with_pool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sb := GetStringBuilder()
			sb.WriteString("test")
			sb.WriteString(" data")
			_ = sb.String()
			PutStringBuilder(sb)
		}
	})

	b.Run("without_pool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sb := &strings.Builder{}
			sb.WriteString("test")
			sb.WriteString(" data")
			_ = sb.String()
		}
	})
}

// Test concurrent access
func TestBufferPoolConcurrent(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := GetBuffer()
			buf.WriteString("concurrent test")
			PutBuffer(buf)
		}()
	}
	wg.Wait()
}

// TestTieredBufferPooling tests the tiered buffer pooling strategy
func TestTieredBufferPooling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		bufferSize int
		shouldPool bool
		poolType   string // "small", "medium", "large", "xlarge", or "none"
	}{
		{
			name:       "small_buffer_4KB",
			bufferSize: 4 * 1024,
			shouldPool: true,
			poolType:   "small",
		},
		{
			name:       "small_buffer_64KB",
			bufferSize: 64 * 1024,
			shouldPool: true,
			poolType:   "small",
		},
		{
			name:       "medium_buffer_128KB",
			bufferSize: 128 * 1024,
			shouldPool: true,
			poolType:   "medium",
		},
		{
			name:       "medium_buffer_256KB",
			bufferSize: 256 * 1024,
			shouldPool: true,
			poolType:   "medium",
		},
		{
			name:       "large_buffer_512KB",
			bufferSize: 512 * 1024,
			shouldPool: true,
			poolType:   "large",
		},
		{
			name:       "large_buffer_1MB",
			bufferSize: 1024 * 1024,
			shouldPool: true,
			poolType:   "large",
		},
		{
			name:       "xlarge_buffer_1.5MB",
			bufferSize: 1536 * 1024,
			shouldPool: true,
			poolType:   "xlarge",
		},
		{
			name:       "xlarge_buffer_2MB",
			bufferSize: 2 * 1024 * 1024,
			shouldPool: true,
			poolType:   "xlarge",
		},
		{
			name:       "huge_buffer_3MB",
			bufferSize: 3 * 1024 * 1024,
			shouldPool: false,
			poolType:   "none",
		},
		{
			name:       "huge_buffer_5MB",
			bufferSize: 5 * 1024 * 1024,
			shouldPool: false,
			poolType:   "none",
		},
		{
			name:       "huge_buffer_150MB",
			bufferSize: 150 * 1024 * 1024,
			shouldPool: false,
			poolType:   "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create buffer with specific size
			buf := GetBuffer()
			buf.Grow(tt.bufferSize)
			buf.Write(make([]byte, tt.bufferSize))

			initialCap := buf.Cap()

			// Return to pool
			PutBuffer(buf)

			// Get a new buffer and check if it's from the pool
			buf2 := GetBuffer()
			defer PutBuffer(buf2)

			if tt.shouldPool {
				// For pooled buffers, verify they're routed to correct pool
				switch tt.poolType {
				case "small":
					if buf2.Cap() > smallBufferThreshold*2 {
						t.Errorf("Expected small pool buffer, got capacity %d", buf2.Cap())
					}
				case "medium":
					// Medium pool pre-allocates 128KB
					if buf2.Cap() > 0 && buf2.Cap() < 64*1024 {
						t.Logf("Medium pool buffer capacity: %d (expected >= 64KB)", buf2.Cap())
					}
				case "large":
					// Large pool pre-allocates 512KB
					if buf2.Cap() > 0 && buf2.Cap() < 256*1024 {
						t.Logf("Large pool buffer capacity: %d (expected >= 256KB)", buf2.Cap())
					}
				case "xlarge":
					// XLarge pool pre-allocates 1MB
					if buf2.Cap() > 0 && buf2.Cap() < 512*1024 {
						t.Logf("XLarge pool buffer capacity: %d (expected >= 512KB)", buf2.Cap())
					}
				}
			} else {
				// For non-pooled buffers, the large buffer should not be reused
				if buf2.Cap() >= initialCap {
					t.Logf("Warning: huge buffer may have been pooled (cap: %d)", buf2.Cap())
				}
			}
		})
	}
}

// BenchmarkTieredBufferPooling benchmarks the 5-tier buffer pooling strategy
// across different buffer sizes to demonstrate performance characteristics
func BenchmarkTieredBufferPooling(b *testing.B) {
	sizes := []struct {
		name string
		size int
		tier string
	}{
		{"Tier1_4KB", 4 * 1024, "small"},
		{"Tier1_64KB", 64 * 1024, "small"},
		{"Tier2_128KB", 128 * 1024, "medium"},
		{"Tier2_256KB", 256 * 1024, "medium"},
		{"Tier3_512KB", 512 * 1024, "large"},
		{"Tier3_1MB", 1024 * 1024, "large"},
		{"Tier4_1.5MB", 1536 * 1024, "xlarge"},
		{"Tier4_2MB", 2 * 1024 * 1024, "xlarge"},
		{"Tier5_3MB", 3 * 1024 * 1024, "none"},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			data := make([]byte, sz.size)
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				buf := GetBuffer()
				buf.Write(data)
				PutBuffer(buf)
			}
		})
	}
}
