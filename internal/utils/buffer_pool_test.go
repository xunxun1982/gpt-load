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
		name      string
		setupBuf  func() *bytes.Buffer
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
				buf := bytes.NewBuffer(make([]byte, 0, maxPooledBufferSize+1))
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
		name      string
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
				large := make([]byte, 0, maxPooledBufferSize+1)
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
				largeData := make([]byte, maxPooledBufferSize+1)
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
				sb.Grow(maxPooledBufferSize + 1)
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
