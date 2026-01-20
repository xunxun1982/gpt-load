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

	// Create test group and keys with unique name to avoid conflicts in shared cache
	group := &models.Group{
		Name:        fmt.Sprintf("bench-select-key-%d", time.Now().UnixNano()),
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
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
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

		// Use actual DB-assigned ID instead of sequential assumption
		if err := memStore.LPush(activeKeysListKey, apiKey.ID); err != nil {
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

	// Setup test data with unique name to avoid conflicts in shared cache
	group := &models.Group{
		Name:        fmt.Sprintf("concurrent-group-%d", time.Now().UnixNano()),
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
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
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

		// Use actual DB-assigned ID instead of sequential assumption
		if err := memStore.LPush(activeKeysListKey, apiKey.ID); err != nil {
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
		Name:        fmt.Sprintf("status-group-%d", time.Now().UnixNano()),
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

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
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
		b.StopTimer()
		// Reset key state to measure consistent behavior
		if err := db.Model(&models.APIKey{}).Where("id = ?", apiKey.ID).Updates(map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": 1,
		}).Error; err != nil {
			b.Fatal(err)
		}
		if err := memStore.HSet(keyHashKey, map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": "1",
		}); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		provider.UpdateStatus(apiKey, group, true, "")
	}
}

// BenchmarkUpdateStatusFailurePerformance benchmarks failure status updates
func BenchmarkUpdateStatusFailurePerformance(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        fmt.Sprintf("failure-group-%d", time.Now().UnixNano()),
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

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
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
		b.StopTimer()
		// Reset key state to measure consistent behavior
		if err := db.Model(&models.APIKey{}).Where("id = ?", apiKey.ID).Updates(map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": 0,
		}).Error; err != nil {
			b.Fatal(err)
		}
		if err := memStore.HSet(keyHashKey, map[string]any{
			"status":        models.KeyStatusActive,
			"failure_count": "0",
		}); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

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
				Name:        fmt.Sprintf("load-group-%d", time.Now().UnixNano()),
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
				if err := provider.LoadKeysFromDB(); err != nil {
					b.Fatal(err)
				}
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
				Name:        fmt.Sprintf("add-group-%d", time.Now().UnixNano()),
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
				if err := provider.AddKeys(group.ID, keys); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				if _, err := provider.RemoveAllKeys(context.Background(), group.ID); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}

// BenchmarkRemoveKeysPerformance benchmarks batch key removal
func BenchmarkRemoveKeysPerformance(b *testing.B) {
	provider, db, _ := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        fmt.Sprintf("remove-group-%d", time.Now().UnixNano()),
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

	keyValues := make([]string, 10)
	for i := 0; i < 10; i++ {
		keyValue := fmt.Sprintf("sk-remove-%d", i)
		keyValues[i] = keyValue
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Create keys for removal
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

		if _, err := provider.RemoveKeys(group.ID, keyValues); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRestoreKeysPerformance benchmarks key restoration
func BenchmarkRestoreKeysPerformance(b *testing.B) {
	provider, db, _ := setupBenchProvider(b)
	defer provider.Stop()

	group := &models.Group{
		Name:        fmt.Sprintf("restore-group-%d", time.Now().UnixNano()),
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

		if _, err := provider.RestoreKeys(group.ID); err != nil {
			b.Fatal(err)
		}
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
				Name:        fmt.Sprintf("removeall-group-%d", time.Now().UnixNano()),
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
				if _, err := provider.RemoveAllKeys(ctx, group.ID); err != nil {
					b.Fatal(err)
				}
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
		Name:        fmt.Sprintf("concurrent-ops-group-%d", time.Now().UnixNano()),
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
	keyIDs := make([]uint, 0, keyCount)
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
		keyIDs = append(keyIDs, apiKey.ID)

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
	for _, id := range keyIDs {
		if err := memStore.LPush(activeKeysListKey, id); err != nil {
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
				// Success update - use actual key IDs
				keyID := keyIDs[i%len(keyIDs)]
				apiKey := &models.APIKey{ID: keyID, GroupID: group.ID}
				provider.UpdateStatus(apiKey, group, true, "")
			} else {
				// Failure update - use actual key IDs
				keyID := keyIDs[i%len(keyIDs)]
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
		{fmt.Sprintf("small-group-%d", time.Now().UnixNano()), 5},
		{fmt.Sprintf("medium-group-%d", time.Now().UnixNano()), 20},
		{fmt.Sprintf("large-group-%d", time.Now().UnixNano()), 100},
	}

	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatal(err)
	}
	encryptedKey, err := encSvc.Encrypt("sk-realistic")
	if err != nil {
		b.Fatal(err)
	}

	var groupIDs []uint
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
		groupIDs = append(groupIDs, group.ID)

		activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
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

			// Use actual DB-assigned ID instead of sequential assumption
			if err := memStore.LPush(activeKeysListKey, apiKey.ID); err != nil {
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
			var groupID uint
			dist := i % 10
			if dist < 5 {
				groupID = groupIDs[0] // small
			} else if dist < 8 {
				groupID = groupIDs[1] // medium
			} else {
				groupID = groupIDs[2] // large
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
		Name:        fmt.Sprintf("memory-group-%d", time.Now().UnixNano()),
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

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
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
