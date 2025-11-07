package utils

import "fmt"

// ProcessInChunks processes a slice in chunks of the specified size.
// It calls the provided function for each chunk.
// Returns an error if chunkSize is invalid or if the processing function fails.
func ProcessInChunks[T any](items []T, chunkSize int, fn func(chunk []T) error) error {
	if chunkSize <= 0 {
		return fmt.Errorf("chunk size must be positive, got %d", chunkSize)
	}

	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		chunk := items[i:end]
		if err := fn(chunk); err != nil {
			return err
		}
	}
	return nil
}

// ChunkSlice splits a slice into chunks of the specified size.
// Returns a slice of slices, where each inner slice is a chunk.
// Returns an error if chunkSize is invalid.
func ChunkSlice[T any](items []T, chunkSize int) ([][]T, error) {
	if chunkSize <= 0 {
		return nil, fmt.Errorf("chunk size must be positive, got %d", chunkSize)
	}

	// Pre-allocate chunks slice with estimated capacity
	chunks := make([][]T, 0, (len(items)+chunkSize-1)/chunkSize)
	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[i:end])
	}
	return chunks, nil
}
