package centralizedmgmt

import (
	"context"
	"fmt"
	"gpt-load/internal/encryption"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// BenchmarkValidateAccessKey benchmarks the hot path of access key validation
// This is critical as it's called for every Hub API request
func BenchmarkValidateAccessKey(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	// Create test key
	_, keyValue, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-validate-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.ValidateAccessKey(ctx, keyValue)
	}
}

// BenchmarkValidateAccessKeyCached benchmarks cached validation
func BenchmarkValidateAccessKeyCached(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	_, keyValue, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-cached-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	// Warm up cache
	_, _ = svc.ValidateAccessKey(ctx, keyValue)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.ValidateAccessKey(ctx, keyValue)
	}
}

// BenchmarkValidateAccessKeyConcurrent benchmarks concurrent validation
func BenchmarkValidateAccessKeyConcurrent(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	_, keyValue, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-concurrent-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	// Warm up cache
	_, _ = svc.ValidateAccessKey(ctx, keyValue)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = svc.ValidateAccessKey(ctx, keyValue)
		}
	})
}

// BenchmarkIsModelAllowed benchmarks model permission checking
func BenchmarkIsModelAllowed(b *testing.B) {
	testCases := []struct {
		name          string
		allowedModels []string
		testModel     string
	}{
		{"EmptyList_AllAllowed", []string{}, "gpt-4"},
		{"SingleModel_Match", []string{"gpt-4"}, "gpt-4"},
		{"SingleModel_NoMatch", []string{"gpt-4"}, "claude-3"},
		{"MultipleModels_Match", []string{"gpt-4", "claude-3", "gemini"}, "claude-3"},
		{"MultipleModels_NoMatch", []string{"gpt-4", "claude-3", "gemini"}, "llama-2"},
		{"LargeList_Match", generateModelList(50), "model-25"},
		{"LargeList_NoMatch", generateModelList(50), "unknown-model"},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			// Create new service for each sub-benchmark to avoid name conflicts
			svc, db := setupBenchService(b)
			ctx := context.Background()

			dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
				Name:          fmt.Sprintf("bench-model-%s", tc.name),
				AllowedModels: tc.allowedModels,
				Enabled:       true,
			})
			if err != nil {
				b.Fatalf("Failed to create test key: %v", err)
			}

			var key HubAccessKey
			if err := db.First(&key, dto.ID).Error; err != nil {
				b.Fatalf("Failed to fetch key: %v", err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = svc.IsModelAllowed(&key, tc.testModel)
			}
		})
	}
}

// BenchmarkCreateAccessKey benchmarks key creation
func BenchmarkCreateAccessKey(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          fmt.Sprintf("bench-create-%d", i),
			AllowedModels: []string{},
			Enabled:       true,
		})
	}
}

// BenchmarkListAccessKeys benchmarks listing all keys
func BenchmarkListAccessKeys(b *testing.B) {
	sizes := []int{10, 50, 100, 500}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Keys%d", size), func(b *testing.B) {
			svc, _ := setupBenchService(b)
			ctx := context.Background()

			// Create test keys
			for i := 0; i < size; i++ {
				_, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
					Name:          fmt.Sprintf("bench-list-%d", i),
					AllowedModels: []string{},
					Enabled:       i%2 == 0, // Mix of enabled/disabled
				})
				if err != nil {
					b.Fatalf("Failed to create test key: %v", err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = svc.ListAccessKeys(ctx)
			}
		})
	}
}

// BenchmarkUpdateAccessKey benchmarks key updates
func BenchmarkUpdateAccessKey(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-update-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newName := fmt.Sprintf("updated-name-%d", i)
		_, _ = svc.UpdateAccessKey(ctx, dto.ID, UpdateAccessKeyParams{
			Name: &newName,
		})
	}
}

// BenchmarkDeleteAccessKey benchmarks key deletion
func BenchmarkDeleteAccessKey(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          fmt.Sprintf("bench-delete-%d", i),
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			b.Fatalf("Failed to create test key: %v", err)
		}
		b.StartTimer()

		_ = svc.DeleteAccessKey(ctx, dto.ID)
	}
}

// BenchmarkExportAccessKeys benchmarks key export
func BenchmarkExportAccessKeys(b *testing.B) {
	sizes := []int{10, 50, 100}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Keys%d", size), func(b *testing.B) {
			svc, _ := setupBenchService(b)
			ctx := context.Background()

			// Create test keys
			for i := 0; i < size; i++ {
				_, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
					Name:          fmt.Sprintf("bench-export-%d", i),
					AllowedModels: []string{"gpt-4", "claude-3"},
					Enabled:       true,
				})
				if err != nil {
					b.Fatalf("Failed to create test key: %v", err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = svc.ExportAccessKeys(ctx)
			}
		})
	}
}

// BenchmarkImportAccessKeys benchmarks key import
func BenchmarkImportAccessKeys(b *testing.B) {
	svc, db := setupBenchService(b)
	ctx := context.Background()

	// Create and export keys
	for i := 0; i < 10; i++ {
		_, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          fmt.Sprintf("bench-import-%d", i),
			AllowedModels: []string{"gpt-4"},
			Enabled:       true,
		})
		if err != nil {
			b.Fatalf("Failed to create test key: %v", err)
		}
	}

	exports, err := svc.ExportAccessKeys(ctx)
	if err != nil {
		b.Fatalf("Failed to export keys: %v", err)
	}

	// Modify names to avoid conflicts
	for i := range exports {
		exports[i].Name = fmt.Sprintf("import-test-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Clean up previous imports
		db.Where("name LIKE ?", "import-test-%").Delete(&HubAccessKey{})
		b.StartTimer()

		tx := db.Begin()
		_, _, _ = svc.ImportAccessKeys(ctx, tx, exports)
		tx.Commit()
	}
}

// BenchmarkBatchDeleteAccessKeys benchmarks batch deletion
func BenchmarkBatchDeleteAccessKeys(b *testing.B) {
	batchSizes := []int{10, 50, 100}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("Batch%d", batchSize), func(b *testing.B) {
			svc, _ := setupBenchService(b)
			ctx := context.Background()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// Create keys for deletion
				ids := make([]uint, batchSize)
				for j := 0; j < batchSize; j++ {
					dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
						Name:          fmt.Sprintf("bench-batch-delete-%d-%d", i, j),
						AllowedModels: []string{},
						Enabled:       true,
					})
					if err != nil {
						b.Fatalf("Failed to create test key: %v", err)
					}
					ids[j] = dto.ID
				}
				b.StartTimer()

				_, _ = svc.BatchDeleteAccessKeys(ctx, ids)
			}
		})
	}
}

// BenchmarkBatchUpdateAccessKeysEnabled benchmarks batch enable/disable
func BenchmarkBatchUpdateAccessKeysEnabled(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	// Create test keys
	ids := make([]uint, 50)
	for i := 0; i < 50; i++ {
		dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          fmt.Sprintf("bench-batch-update-%d", i),
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			b.Fatalf("Failed to create test key: %v", err)
		}
		ids[i] = dto.ID
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enabled := i%2 == 0
		_, _ = svc.BatchUpdateAccessKeysEnabled(ctx, ids, enabled)
	}
}

// BenchmarkRecordKeyUsage benchmarks usage recording
func BenchmarkRecordKeyUsage(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-usage-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = svc.RecordKeyUsage(ctx, dto.ID)
	}
}

// BenchmarkRecordKeyUsageConcurrent benchmarks concurrent usage recording
func BenchmarkRecordKeyUsageConcurrent(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-concurrent-usage-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = svc.RecordKeyUsage(ctx, dto.ID)
		}
	})
}

// BenchmarkCacheInvalidation benchmarks cache invalidation operations
func BenchmarkCacheInvalidation(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	// Create and cache multiple keys
	keyValues := make([]string, 10)
	for i := 0; i < 10; i++ {
		_, keyValue, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          fmt.Sprintf("bench-cache-%d", i),
			AllowedModels: []string{},
			Enabled:       true,
		})
		if err != nil {
			b.Fatalf("Failed to create test key: %v", err)
		}
		keyValues[i] = keyValue
		// Warm up cache
		_, _ = svc.ValidateAccessKey(ctx, keyValue)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Re-warm cache after invalidation to measure actual invalidation cost
		b.StopTimer()
		for _, keyValue := range keyValues {
			_, _ = svc.ValidateAccessKey(ctx, keyValue)
		}
		b.StartTimer()

		svc.InvalidateAllKeyCache()
	}
}

// BenchmarkRealisticWorkload simulates realistic Hub access key workload
func BenchmarkRealisticWorkload(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	// Create multiple keys with different configurations
	keys := []struct {
		name          string
		allowedModels []string
		enabled       bool
	}{
		{"prod-key-1", []string{}, true},
		{"prod-key-2", []string{"gpt-4", "claude-3"}, true},
		{"prod-key-3", []string{"gpt-4"}, true},
		{"test-key-1", []string{}, true},
		{"disabled-key", []string{}, false},
	}

	keyValues := make([]string, len(keys))
	for i, k := range keys {
		_, keyValue, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
			Name:          k.name,
			AllowedModels: k.allowedModels,
			Enabled:       k.enabled,
		})
		if err != nil {
			b.Fatalf("Failed to create test key: %v", err)
		}
		keyValues[i] = keyValue
	}

	b.ResetTimer()

	// Simulate realistic request distribution using b.RunParallel
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// 80% validation, 10% model check, 10% usage recording
			op := i % 10
			keyIdx := i % len(keyValues)

			if op < 8 {
				// Validate key
				_, _ = svc.ValidateAccessKey(ctx, keyValues[keyIdx])
			} else if op == 8 {
				// Check model permission
				key, err := svc.ValidateAccessKey(ctx, keyValues[keyIdx])
				if err == nil {
					_ = svc.IsModelAllowed(key, "gpt-4")
				}
			} else {
				// Record usage
				key, err := svc.ValidateAccessKey(ctx, keyValues[keyIdx])
				if err == nil {
					_ = svc.RecordKeyUsage(ctx, key.ID)
				}
			}
			i++
		}
	})
}

// BenchmarkMemoryAllocation benchmarks memory allocation patterns
func BenchmarkMemoryAllocation(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	_, keyValue, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-memory-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	// Warm up cache
	_, _ = svc.ValidateAccessKey(ctx, keyValue)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = svc.ValidateAccessKey(ctx, keyValue)
	}
}

// BenchmarkGetAccessKeyPlaintext benchmarks plaintext retrieval
func BenchmarkGetAccessKeyPlaintext(b *testing.B) {
	svc, _ := setupBenchService(b)
	ctx := context.Background()

	dto, _, err := svc.CreateAccessKey(ctx, CreateAccessKeyParams{
		Name:          "bench-plaintext-key",
		AllowedModels: []string{},
		Enabled:       true,
	})
	if err != nil {
		b.Fatalf("Failed to create test key: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.GetAccessKeyPlaintext(ctx, dto.ID)
	}
}

// Helper functions

func setupBenchService(b *testing.B) (*HubAccessKeyService, *gorm.DB) {
	// Use unique in-memory database per benchmark to avoid cross-benchmark state leakage
	// while maintaining shared cache semantics for parallel access within the same benchmark
	dsn := fmt.Sprintf("file:bench_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatalf("failed to connect to test database: %v", err)
	}

	if err := db.AutoMigrate(&HubAccessKey{}); err != nil {
		b.Fatalf("failed to migrate test database: %v", err)
	}

	encSvc, err := encryption.NewService("test-encryption-key-32chars!!")
	if err != nil {
		b.Fatalf("failed to create encryption service: %v", err)
	}

	svc := NewHubAccessKeyService(db, encSvc)
	return svc, db
}

func generateModelList(count int) []string {
	models := make([]string, count)
	for i := 0; i < count; i++ {
		models[i] = fmt.Sprintf("model-%d", i)
	}
	return models
}
