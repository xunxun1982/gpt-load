package utils

import (
	"errors"
	"testing"
)

// TestProcessInChunks tests chunk processing
func TestProcessInChunks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		items     []int
		chunkSize int
		wantErr   bool
		wantCalls int
	}{
		{
			"EmptySlice",
			[]int{},
			10,
			false,
			0,
		},
		{
			"SingleChunk",
			[]int{1, 2, 3},
			10,
			false,
			1,
		},
		{
			"MultipleChunks",
			[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			3,
			false,
			4,
		},
		{
			"ExactChunks",
			[]int{1, 2, 3, 4, 5, 6},
			3,
			false,
			2,
		},
		{
			"InvalidChunkSize",
			[]int{1, 2, 3},
			0,
			true,
			0,
		},
		{
			"NegativeChunkSize",
			[]int{1, 2, 3},
			-1,
			true,
			0,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			callCount := 0
			err := ProcessInChunks(tt.items, tt.chunkSize, func(chunk []int) error {
				callCount++
				return nil
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("ProcessInChunks() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && callCount != tt.wantCalls {
				t.Errorf("ProcessInChunks() callCount = %d, want %d", callCount, tt.wantCalls)
			}
		})
	}
}

// TestProcessInChunksError tests error handling
func TestProcessInChunksError(t *testing.T) {
	t.Parallel()
	items := []int{1, 2, 3, 4, 5}
	expectedErr := errors.New("processing error")

	callCount := 0
	err := ProcessInChunks(items, 2, func(chunk []int) error {
		callCount++
		if callCount == 2 {
			return expectedErr
		}
		return nil
	})

	if err != expectedErr {
		t.Errorf("ProcessInChunks() error = %v, want %v", err, expectedErr)
	}

	if callCount != 2 {
		t.Errorf("ProcessInChunks() should stop after error, callCount = %d, want 2", callCount)
	}
}

// TestChunkSlice tests slice chunking
func TestChunkSlice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		items      []int
		chunkSize  int
		wantChunks int
		wantErr    bool
	}{
		{
			"EmptySlice",
			[]int{},
			10,
			0,
			false,
		},
		{
			"SingleChunk",
			[]int{1, 2, 3},
			10,
			1,
			false,
		},
		{
			"MultipleChunks",
			[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			3,
			4,
			false,
		},
		{
			"ExactChunks",
			[]int{1, 2, 3, 4, 5, 6},
			3,
			2,
			false,
		},
		{
			"InvalidChunkSize",
			[]int{1, 2, 3},
			0,
			0,
			true,
		},
		{
			"NegativeChunkSize",
			[]int{1, 2, 3},
			-1,
			0,
			true,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			chunks, err := ChunkSlice(tt.items, tt.chunkSize)

			if (err != nil) != tt.wantErr {
				t.Errorf("ChunkSlice() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if len(chunks) != tt.wantChunks {
					t.Errorf("ChunkSlice() chunks = %d, want %d", len(chunks), tt.wantChunks)
				}

				// Verify all items are present
				totalItems := 0
				for _, chunk := range chunks {
					totalItems += len(chunk)
				}
				if totalItems != len(tt.items) {
					t.Errorf("ChunkSlice() total items = %d, want %d", totalItems, len(tt.items))
				}
			}
		})
	}
}

// TestChunkSliceContent tests chunk content correctness
func TestChunkSliceContent(t *testing.T) {
	t.Parallel()
	items := []int{1, 2, 3, 4, 5, 6, 7}
	chunks, err := ChunkSlice(items, 3)

	if err != nil {
		t.Fatalf("ChunkSlice() error = %v", err)
	}

	expected := [][]int{
		{1, 2, 3},
		{4, 5, 6},
		{7},
	}

	if len(chunks) != len(expected) {
		t.Fatalf("ChunkSlice() chunks = %d, want %d", len(chunks), len(expected))
	}

	for i, chunk := range chunks {
		if len(chunk) != len(expected[i]) {
			t.Errorf("Chunk %d length = %d, want %d", i, len(chunk), len(expected[i]))
			continue
		}
		for j, item := range chunk {
			if item != expected[i][j] {
				t.Errorf("Chunk %d item %d = %d, want %d", i, j, item, expected[i][j])
			}
		}
	}
}

// TestChunkSliceStrings tests chunking with strings
func TestChunkSliceStrings(t *testing.T) {
	t.Parallel()
	items := []string{"a", "b", "c", "d", "e"}
	chunks, err := ChunkSlice(items, 2)

	if err != nil {
		t.Fatalf("ChunkSlice() error = %v", err)
	}

	expected := [][]string{
		{"a", "b"},
		{"c", "d"},
		{"e"},
	}

	if len(chunks) != len(expected) {
		t.Fatalf("ChunkSlice() chunks = %d, want %d", len(chunks), len(expected))
	}

	for i, chunk := range chunks {
		if len(chunk) != len(expected[i]) {
			t.Errorf("Chunk %d length = %d, want %d", i, len(chunk), len(expected[i]))
			continue
		}
		for j, item := range chunk {
			if item != expected[i][j] {
				t.Errorf("Chunk %d item %d = %s, want %s", i, j, item, expected[i][j])
			}
		}
	}
}

// BenchmarkProcessInChunks benchmarks chunk processing
func BenchmarkProcessInChunks(b *testing.B) {
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ProcessInChunks(items, 100, func(chunk []int) error {
			return nil
		})
	}
}

// BenchmarkChunkSlice benchmarks slice chunking
func BenchmarkChunkSlice(b *testing.B) {
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ChunkSlice(items, 100)
	}
}
