package utils

// ProcessInChunks processes a slice in chunks of the specified size.
// It calls the provided function for each chunk.
func ProcessInChunks[T any](items []T, chunkSize int, fn func(chunk []T) error) error {
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
func ChunkSlice[T any](items []T, chunkSize int) [][]T {
	var chunks [][]T
	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[i:end])
	}
	return chunks
}
