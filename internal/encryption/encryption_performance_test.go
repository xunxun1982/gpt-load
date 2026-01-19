package encryption

import (
	"strings"
	"sync"
	"testing"
)

// BenchmarkEncrypt benchmarks encryption operation
func BenchmarkEncrypt(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")

	testCases := []struct {
		name string
		data string
	}{
		{"Empty", ""},
		{"Short_10B", "1234567890"},
		{"Medium_100B", strings.Repeat("a", 100)},
		{"APIKey_50B", "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"},
		{"Large_1KB", strings.Repeat("x", 1024)},
		{"Large_10KB", strings.Repeat("y", 10240)},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = svc.Encrypt(tc.data)
			}
		})
	}
}

// BenchmarkDecrypt benchmarks decryption operation
func BenchmarkDecrypt(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")

	testCases := []struct {
		name string
		data string
	}{
		{"Empty", ""},
		{"Short_10B", "1234567890"},
		{"Medium_100B", strings.Repeat("a", 100)},
		{"APIKey_50B", "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"},
		{"Large_1KB", strings.Repeat("x", 1024)},
		{"Large_10KB", strings.Repeat("y", 10240)},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			// Pre-encrypt data
			ciphertext, _ := svc.Encrypt(tc.data)
			b.SetBytes(int64(len(tc.data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = svc.Decrypt(ciphertext)
			}
		})
	}
}

// BenchmarkHash benchmarks hash operation
func BenchmarkHash(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")

	testCases := []struct {
		name string
		data string
	}{
		{"Empty", ""},
		{"Short_10B", "1234567890"},
		{"Medium_100B", strings.Repeat("a", 100)},
		{"APIKey_50B", "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"},
		{"Large_1KB", strings.Repeat("x", 1024)},
		{"Large_10KB", strings.Repeat("y", 10240)},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = svc.Hash(tc.data)
			}
		})
	}
}

// BenchmarkEncryptDecryptCycle benchmarks full encrypt-decrypt cycle
func BenchmarkEncryptDecryptCycle(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")
	data := "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ciphertext, _ := svc.Encrypt(data)
		_, _ = svc.Decrypt(ciphertext)
	}
}

// BenchmarkConcurrentEncrypt benchmarks concurrent encryption
func BenchmarkConcurrentEncrypt(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")
	data := "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = svc.Encrypt(data)
		}
	})
}

// BenchmarkConcurrentDecrypt benchmarks concurrent decryption
func BenchmarkConcurrentDecrypt(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")
	data := "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
	ciphertext, _ := svc.Encrypt(data)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = svc.Decrypt(ciphertext)
		}
	})
}

// BenchmarkConcurrentHash benchmarks concurrent hashing
func BenchmarkConcurrentHash(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")
	data := "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = svc.Hash(data)
		}
	})
}

// BenchmarkNoopService benchmarks noop service (no encryption)
func BenchmarkNoopService(b *testing.B) {
	svc, _ := NewService("")
	data := "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"

	b.Run("Encrypt", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = svc.Encrypt(data)
		}
	})

	b.Run("Decrypt", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = svc.Decrypt(data)
		}
	})

	b.Run("Hash", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = svc.Hash(data)
		}
	})
}

// BenchmarkRealisticWorkload simulates realistic encryption workload
func BenchmarkRealisticWorkload(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")

	// Realistic API key distribution
	keys := []string{
		"sk-short123",
		"sk-1234567890abcdefghijklmnopqrstuvwxyz",
		"sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"sk-very-long-key-" + strings.Repeat("x", 100),
	}

	b.Run("EncryptMix", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			key := keys[i%len(keys)]
			_, _ = svc.Encrypt(key)
		}
	})

	b.Run("HashMix", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			key := keys[i%len(keys)]
			_ = svc.Hash(key)
		}
	})

	b.Run("FullCycleMix", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			key := keys[i%len(keys)]
			ciphertext, _ := svc.Encrypt(key)
			_, _ = svc.Decrypt(ciphertext)
			_ = svc.Hash(key)
		}
	})
}

// BenchmarkBatchOperations benchmarks batch encryption operations
func BenchmarkBatchOperations(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")

	// Generate batch of keys
	keys := make([]string, 100)
	for i := range keys {
		keys[i] = "sk-key-" + strings.Repeat("x", 40)
	}

	b.Run("SequentialEncrypt", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, key := range keys {
				_, _ = svc.Encrypt(key)
			}
		}
	})

	b.Run("ParallelEncrypt", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var wg sync.WaitGroup
			for _, key := range keys {
				wg.Add(1)
				go func(k string) {
					defer wg.Done()
					_, _ = svc.Encrypt(k)
				}(key)
			}
			wg.Wait()
		}
	})
}

// BenchmarkMemoryAllocation benchmarks memory allocation patterns
func BenchmarkMemoryAllocation(b *testing.B) {
	svc, _ := NewService("test-encryption-key-32-bytes!!")
	data := "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"

	b.Run("Encrypt", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = svc.Encrypt(data)
		}
	})

	b.Run("Decrypt", func(b *testing.B) {
		ciphertext, _ := svc.Encrypt(data)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = svc.Decrypt(ciphertext)
		}
	})

	b.Run("Hash", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = svc.Hash(data)
		}
	})
}
