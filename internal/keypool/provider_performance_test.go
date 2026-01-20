package keypool

import (
	"context"
	"fmt"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"testing"
	"time"

	"gorm.io/datatypes"
)

// testGroupUpstreams is the default upstreams configuration for test groups
var testGroupUpstreams = datatypes.JSON([]byte(`[{"url":"https://api.openai.com","weight":100}]`))

// BenchmarkSelectKeyPerformance benchmarks the hot path of key selection
// This is the most critical operation as it's called for every API request
func BenchmarkSelectKeyPerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	// Create test group and keys
	group := &models.Group{
		Name:        "bench-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   testGroupUpstreams,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-bench-key")
	if err != nil {
		b.Fatal(err)
	}

	// Create multiple keys to simulate realistic pool
	keyCount := 10
	for i := 0; i < keyCount; i++ {
		apiKey := &models.APIKey{
			GroupID:      group.ID,
			KeyValue:     encryptedKey,
			KeyHash:      encSvc.Hash(fmt.Sprintf("sk-bench-key-%d", i)),
			Status:       models.KeyStatusActive,
			FailureCount: 0,
		}
		if err := db.Create(apiKey).Error; err != nil {
			b.Fatal(err)
		}

		// Add to store
		keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
		keyDetails := map[string]any{
			"key_string":    encryptedKey,
			"status":        models.KeyStatusActive,
			"failure_count": "0",
			"created_at":    time.Now().Unix(),
		}
		if err := memStore.HSet(keyHashKey, keyDetails); err != nil {
			b.Fatal(err)
		}
	}

	// Setup active keys list
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	for i := 1; i <= keyCount; i++ {
		if err := memStore.LPush(activeKeysListKey, uint(i)); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = provider.SelectKey(group.ID)
	}
}

// BenchmarkSelectKeyConcurrentPerformance benchmarks concurrent key selection
// Simulates realistic load with multiple goroutines selecting keys
func BenchmarkSelectKeyConcurrentPerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	// Setup test data
	group := &models.Group{
		Name:        "concurrent-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   testGroupUpstreams,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-concurrent-key")
	if err != nil {
		b.Fatal(err)
	}

	keyCount := 20
	for i := 0; i < keyCount; i++ {
		apiKey := &models.APIKey{
			GroupID:      group.ID,
			KeyValue:     encryptedKey,
			KeyHash:      encSvc.Hash(fmt.Sprintf("sk-concurrent-%d", i)),
			Status:       models.KeyStatusActive,
			FailureCount: 0,
		}
		if err := db.Create(apiKey).Error; err != nil {
			b.Fatal(err)
		}

		keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
		keyDetails := map[string]any{
			"key_string":    encryptedKey,
			"status":        models.KeyStatusActive,
			"failure_count": "0",
			"created_at":    time.Now().Unix(),
		}
		if err := memStore.HSet(keyHashKey, keyDetails); err != nil {
			b.Fatal(err)
		}
	}

	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	for i := 1; i <= keyCount; i++ {
		if err := memStore.LPush(activeKeysListKey, uint(i)); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = provider.SelectKey(group.ID)
		}
	})
}

// BenchmarkUpdateStatusSuccessPerformance benchmarks successful status updates
func BenchmarkUpdateStatusSuccessPerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        "status-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   testGroupUpstreams,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-status-key")
	if err != nil {
		b.Fatal(err)
	}

	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-status-key"),
		Status:       models.KeyStatusActive,
		FailureCount: 1,
	}
	if err := db.Create(apiKey).Error; err != nil {
		b.Fatal(err)
	}

	keyHashKey := "key:1"
	activeKeysListKey := "group:1:active_keys"
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "1",
		"created_at":    time.Now().Unix(),
	}
	if err := memStore.HSet(keyHashKey, keyDetails); err != nil {
		b.Fatal(err)
	}
	if err := memStore.LPush(activeKeysListKey, apiKey.ID); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.UpdateStatus(apiKey, group, true, "")
	}
}

// BenchmarkUpdateStatusFailurePerformance benchmarks failure status updates
func BenchmarkUpdateStatusFailurePerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        "failure-group",
		ChannelType: "openai",
		Enabled:     true,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-failure-key")
	if err != nil {
		b.Fatal(err)
	}

	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-failure-key"),
		Status:       models.KeyStatusActive,
		FailureCount: 0,
	}
	if err := db.Create(apiKey).Error; err != nil {
		b.Fatal(err)
	}

	keyHashKey := "key:1"
	activeKeysListKey := "group:1:active_keys"
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    time.Now().Unix(),
	}
	if err := memStore.HSet(keyHashKey, keyDetails); err != nil {
		b.Fatal(err)
	}
	if err := memStore.LPush(activeKeysListKey, apiKey.ID); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.UpdateStatus(apiKey, group, false, "API error")
	}
}

// BenchmarkLoadKeysFromDBPerformance benchmarks loading keys from database
func BenchmarkLoadKeysFromDBPerformance(b *testing.B) {
	sizes := []int{100, 500, 1000, 5000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Keys%d", size), func(b *testing.B) {
			provider, db, _ := setupBenchProvider(b)
			defer provider.Stop()

			// Create test data
			group := &models.Group{
				Name:        "load-group",
				ChannelType: "openai",
				Enabled:     true,
				Upstreams:   testGroupUpstreams,
			}
			if err := db.Create(group).Error; err != nil {
				b.Fatal(err)
			}

			encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
			if err != nil {
				b.Fatal(err)
			}
			encryptedKey, err := encSvc.Encrypt("sk-load-key")
			if err != nil {
				b.Fatal(err)
			}

			for i := 0; i < size; i++ {
				apiKey := &models.APIKey{
					GroupID:      group.ID,
					KeyValue:     encryptedKey,
					KeyHash:      encSvc.Hash(fmt.Sprintf("sk-load-%d", i)),
					Status:       models.KeyStatusActive,
					FailureCount: 0,
				}
				if err := db.Create(apiKey).Error; err != nil {
					b.Fatal(err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = provider.LoadKeysFromDB()
			}
		})
	}
}

// BenchmarkAddKeysPerformance benchmarks batch key addition
func BenchmarkAddKeysPerformance(b *testing.B) {
	batchSizes := []int{10, 50, 100, 500}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("Batch%d", batchSize), func(b *testing.B) {
			provider, db, _ := setupBenchProvider(b)
			defer provider.Stop()

			group := &models.Group{
				Name:        "add-group",
				ChannelType: "openai",
				Enabled:     true,
				Upstreams:   testGroupUpstreams,
			}
			if err := db.Create(group).Error; err != nil {
				b.Fatal(err)
			}

			encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
			if err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				keys := make([]models.APIKey, batchSize)
				for j := 0; j < batchSize; j++ {
					keys[j] = models.APIKey{
						GroupID:  group.ID,
						KeyValue: "sk-add-key",
						KeyHash:  encSvc.Hash(fmt.Sprintf("sk-add-%d-%d", i, j)),
						Status:   models.KeyStatusActive,
					}
				}
				_ = provider.AddKeys(group.ID, keys)
			}
		})
	}
}

// BenchmarkRemoveKeysPerformance benchmarks batch key removal
func BenchmarkRemoveKeysPerformance(b *testing.B) {
	provider, db, _ := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        "remove-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   testGroupUpstreams,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}

	// Pre-create keys for removal
	keyValues := make([]string, 10)
	for i := 0; i < 10; i++ {
		keyValue := fmt.Sprintf("sk-remove-%d", i)
		keyValues[i] = keyValue
		apiKey := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: keyValue,
			KeyHash:  encSvc.Hash(keyValue),
			Status:   models.KeyStatusActive,
		}
		if err := db.Create(apiKey).Error; err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Re-create keys for next iteration
		for _, kv := range keyValues {
			apiKey := &models.APIKey{
				GroupID:  group.ID,
				KeyValue: kv,
				KeyHash:  encSvc.Hash(kv),
				Status:   models.KeyStatusActive,
			}
			if err := db.Create(apiKey).Error; err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()

		_, _ = provider.RemoveKeys(group.ID, keyValues)
	}
}

// BenchmarkRestoreKeysPerformance benchmarks key restoration
func BenchmarkRestoreKeysPerformance(b *testing.B) {
	provider, db, _ := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        "restore-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   testGroupUpstreams,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}

	// Create invalid keys
	for i := 0; i < 10; i++ {
		apiKey := &models.APIKey{
			GroupID:      group.ID,
			KeyValue:     "sk-restore",
			KeyHash:      encSvc.Hash(fmt.Sprintf("sk-restore-%d", i)),
			Status:       models.KeyStatusInvalid,
			FailureCount: 5,
		}
		if err := db.Create(apiKey).Error; err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Reset keys to invalid for next iteration
		if err := db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Updates(map[string]any{
			"status":        models.KeyStatusInvalid,
			"failure_count": 5,
		}).Error; err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		_, _ = provider.RestoreKeys(group.ID)
	}
}

// BenchmarkRemoveAllKeysPerformance benchmarks complete key removal for a group
func BenchmarkRemoveAllKeysPerformance(b *testing.B) {
	sizes := []int{100, 500, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Keys%d", size), func(b *testing.B) {
			provider, db, _ := setupBenchProvider(b)
			defer provider.Stop()

			group := &models.Group{
				Name:        "removeall-group",
				ChannelType: "openai",
				Enabled:     true,
				Upstreams:   testGroupUpstreams,
			}
			if err := db.Create(group).Error; err != nil {
				b.Fatal(err)
			}

			encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
			if err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// Create keys for removal
				for j := 0; j < size; j++ {
					apiKey := &models.APIKey{
						GroupID:  group.ID,
						KeyValue: "sk-removeall",
						KeyHash:  encSvc.Hash(fmt.Sprintf("sk-removeall-%d-%d", i, j)),
						Status:   models.KeyStatusActive,
					}
					if err := db.Create(apiKey).Error; err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()

				ctx := context.Background()
				_, _ = provider.RemoveAllKeys(ctx, group.ID)
			}
		})
	}
}

// BenchmarkConcurrentOperationsPerformance benchmarks mixed concurrent operations
func BenchmarkConcurrentOperationsPerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	// Setup test data
	group := &models.Group{
		Name:        "concurrent-ops-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   testGroupUpstreams,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-concurrent-ops")
	if err != nil {
		b.Fatal(err)
	}

	keyCount := 50
	for i := 0; i < keyCount; i++ {
		apiKey := &models.APIKey{
			GroupID:      group.ID,
			KeyValue:     encryptedKey,
			KeyHash:      encSvc.Hash(fmt.Sprintf("sk-concurrent-ops-%d", i)),
			Status:       models.KeyStatusActive,
			FailureCount: 0,
		}
		if err := db.Create(apiKey).Error; err != nil {
			b.Fatal(err)
		}

		keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
		keyDetails := map[string]any{
			"key_string":    encryptedKey,
			"status":        models.KeyStatusActive,
			"failure_count": "0",
			"created_at":    time.Now().Unix(),
		}
		if err := memStore.HSet(keyHashKey, keyDetails); err != nil {
			b.Fatal(err)
		}
	}

	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	for i := 1; i <= keyCount; i++ {
		if err := memStore.LPush(activeKeysListKey, uint(i)); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// Mix of operations: 70% select, 20% success update, 10% failure update
			op := i % 10
			if op < 7 {
				// Select key
				_, _ = provider.SelectKey(group.ID)
			} else if op < 9 {
				// Success update - distribute across available keys
				keyID := uint((i % keyCount) + 1)
				apiKey := &models.APIKey{ID: keyID, GroupID: group.ID}
				provider.UpdateStatus(apiKey, group, true, "")
			} else {
				// Failure update - distribute across available keys
				keyID := uint((i % keyCount) + 1)
				apiKey := &models.APIKey{ID: keyID, GroupID: group.ID}
				provider.UpdateStatus(apiKey, group, false, "error")
			}
			i++
		}
	})
}

// BenchmarkRealisticWorkloadPerformance simulates realistic production workload
func BenchmarkRealisticWorkloadPerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	// Setup multiple groups with different key counts
	groups := []struct {
		name     string
		keyCount int
	}{
		{"small-group", 5},
		{"medium-group", 20},
		{"large-group", 100},
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-realistic")
	if err != nil {
		b.Fatal(err)
	}

	for _, g := range groups {
		group := &models.Group{
			Name:        g.name,
			ChannelType: "openai",
			Enabled:     true,
			Upstreams:   testGroupUpstreams,
		}
		if err := db.Create(group).Error; err != nil {
			b.Fatal(err)
		}

		for i := 0; i < g.keyCount; i++ {
			apiKey := &models.APIKey{
				GroupID:      group.ID,
				KeyValue:     encryptedKey,
				KeyHash:      encSvc.Hash(fmt.Sprintf("sk-%s-%d", g.name, i)),
				Status:       models.KeyStatusActive,
				FailureCount: 0,
			}
			if err := db.Create(apiKey).Error; err != nil {
				b.Fatal(err)
			}

			keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
			keyDetails := map[string]any{
				"key_string":    encryptedKey,
				"status":        models.KeyStatusActive,
				"failure_count": "0",
				"created_at":    time.Now().Unix(),
			}
			if err := memStore.HSet(keyHashKey, keyDetails); err != nil {
				b.Fatal(err)
			}
		}

		activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
		for i := 1; i <= g.keyCount; i++ {
			if err := memStore.LPush(activeKeysListKey, uint(i)); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.ResetTimer()

	// Simulate realistic request distribution using b.RunParallel
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// Select group based on realistic distribution
			// 50% small, 30% medium, 20% large
			groupID := uint(1)
			dist := i % 10
			if dist < 5 {
				groupID = 1 // small
			} else if dist < 8 {
				groupID = 2 // medium
			} else {
				groupID = 3 // large
			}

			_, _ = provider.SelectKey(groupID)
			i++
		}
	})
}

// BenchmarkMemoryAllocationPerformance benchmarks memory allocation patterns
func BenchmarkMemoryAllocationPerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        "memory-group",
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   testGroupUpstreams,
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatal(err)
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-memory")
	if err != nil {
		b.Fatal(err)
	}

	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-memory"),
		Status:       models.KeyStatusActive,
		FailureCount: 0,
	}
	if err := db.Create(apiKey).Error; err != nil {
		b.Fatal(err)
	}

	keyHashKey := "key:1"
	activeKeysListKey := "group:1:active_keys"
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    time.Now().Unix(),
	}
	if err := memStore.HSet(keyHashKey, keyDetails); err != nil {
		b.Fatal(err)
	}
	if err := memStore.LPush(activeKeysListKey, apiKey.ID); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = provider.SelectKey(group.ID)
	}
}
