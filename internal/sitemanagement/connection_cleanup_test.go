package sitemanagement

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/store"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestAutoCheckinService_CloseIdleConnections tests that idle connections are properly closed
func TestAutoCheckinService_CloseIdleConnections(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	service := NewAutoCheckinService(db, nil, encSvc)

	// Verify default client exists
	assert.NotNil(t, service.client)

	// Call closeIdleConnections - should not panic
	service.closeIdleConnections()

	// Verify transport is accessible
	transport, ok := service.client.Transport.(*http.Transport)
	assert.True(t, ok)
	assert.NotNil(t, transport)
}

// TestAutoCheckinService_CloseIdleConnections_WithProxyClients tests cleanup with cached proxy clients
func TestAutoCheckinService_CloseIdleConnections_WithProxyClients(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	service := NewAutoCheckinService(db, nil, encSvc)

	// Simulate cached proxy clients
	proxyClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 5,
			IdleConnTimeout:     10 * time.Second,
		},
	}
	service.proxyClients.Store("http://proxy1:8080", proxyClient)
	service.proxyClients.Store("http://proxy2:8080", proxyClient)

	// Call closeIdleConnections
	service.closeIdleConnections()

	// Verify proxy clients still exist (not deleted, just connections closed)
	count := 0
	service.proxyClients.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	assert.Equal(t, 2, count)
}

// TestAutoCheckinService_Stop_CleansUpCache tests that Stop() properly cleans up cache
func TestAutoCheckinService_Stop_CleansUpCache(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	service := NewAutoCheckinService(db, nil, encSvc)

	// Add some proxy clients to cache
	proxyClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:    10,
			IdleConnTimeout: 10 * time.Second,
		},
	}
	service.proxyClients.Store("http://proxy1:8080", proxyClient)
	service.proxyClients.Store("http://proxy2:8080", proxyClient)

	// Stop service
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	service.Stop(ctx)

	// Verify cache is cleared
	count := 0
	service.proxyClients.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	assert.Equal(t, 0, count, "Proxy client cache should be empty after Stop()")
}

// TestBalanceService_CloseIdleConnections tests that idle connections are properly closed
func TestBalanceService_CloseIdleConnections(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	service := NewBalanceService(db, encSvc)

	// Verify default client exists
	assert.NotNil(t, service.client)

	// Call closeIdleConnections - should not panic
	service.closeIdleConnections()

	// Verify transport is accessible
	transport, ok := service.client.Transport.(*http.Transport)
	assert.True(t, ok)
	assert.NotNil(t, transport)
}

// TestBalanceService_Stop_CleansUpCache tests that Stop() properly cleans up cache
func TestBalanceService_Stop_CleansUpCache(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	service := NewBalanceService(db, encSvc)

	// Add some proxy clients to cache
	proxyClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:    10,
			IdleConnTimeout: 10 * time.Second,
		},
	}
	service.proxyClients.Store("http://proxy1:8080", proxyClient)
	service.proxyClients.Store("http://proxy2:8080", proxyClient)

	// Stop service
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	service.Stop(ctx)

	// Verify cache is cleared
	count := 0
	service.proxyClients.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	assert.Equal(t, 0, count, "Proxy client cache should be empty after Stop()")
}

// TestStealthClientManager_CloseIdleConnections tests stealth client cleanup
func TestStealthClientManager_CloseIdleConnections(t *testing.T) {
	t.Parallel()

	manager := NewStealthClientManager(10 * time.Second)

	// Get some clients to populate cache
	client1 := manager.GetClient("")
	client2 := manager.GetClient("http://proxy:8080")

	assert.NotNil(t, client1)
	assert.NotNil(t, client2)

	// Call CloseIdleConnections - should not panic
	manager.CloseIdleConnections()

	// Verify clients still exist in cache
	count := 0
	manager.clients.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	assert.Equal(t, 2, count, "Clients should still be cached after CloseIdleConnections()")
}

// TestStealthClientManager_Cleanup tests stealth client cache cleanup
func TestStealthClientManager_Cleanup(t *testing.T) {
	t.Parallel()

	manager := NewStealthClientManager(10 * time.Second)

	// Get some clients to populate cache
	client1 := manager.GetClient("")
	client2 := manager.GetClient("http://proxy:8080")

	assert.NotNil(t, client1)
	assert.NotNil(t, client2)

	// Call Cleanup
	manager.Cleanup()

	// Verify cache is cleared
	count := 0
	manager.clients.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	assert.Equal(t, 0, count, "Client cache should be empty after Cleanup()")
}

// TestIdleConnTimeout_Configuration tests that IdleConnTimeout is set to 5 seconds for aggressive memory release
func TestIdleConnTimeout_Configuration(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	t.Run("AutoCheckinService", func(t *testing.T) {
		t.Parallel()
		service := NewAutoCheckinService(db, nil, encSvc)
		transport, ok := service.client.Transport.(*http.Transport)
		assert.True(t, ok)
		assert.Equal(t, 5*time.Second, transport.IdleConnTimeout, "IdleConnTimeout should be 5 seconds for aggressive memory release")
		assert.Equal(t, 50, transport.MaxIdleConns, "MaxIdleConns should be 50 for aggressive memory release")
		assert.Equal(t, 10, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost should be 10 for aggressive memory release")
	})

	t.Run("BalanceService", func(t *testing.T) {
		t.Parallel()
		service := NewBalanceService(db, encSvc)
		transport, ok := service.client.Transport.(*http.Transport)
		assert.True(t, ok)
		assert.Equal(t, 5*time.Second, transport.IdleConnTimeout, "IdleConnTimeout should be 5 seconds for aggressive memory release")
		assert.Equal(t, 50, transport.MaxIdleConns, "MaxIdleConns should be 50 for aggressive memory release")
		assert.Equal(t, 10, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost should be 10 for aggressive memory release")
	})

	t.Run("StealthClientManager", func(t *testing.T) {
		t.Parallel()
		manager := NewStealthClientManager(10 * time.Second)
		client := manager.GetClient("")
		transport, ok := client.Transport.(*http.Transport)
		assert.True(t, ok)
		assert.Equal(t, 5*time.Second, transport.IdleConnTimeout, "IdleConnTimeout should be 5 seconds for aggressive memory release")
		assert.Equal(t, 50, transport.MaxIdleConns, "MaxIdleConns should be 50 for aggressive memory release")
		assert.Equal(t, 10, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost should be 10 for aggressive memory release")
	})
}

// TestConcurrentCloseIdleConnections tests concurrent calls to closeIdleConnections
func TestConcurrentCloseIdleConnections(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	service := NewAutoCheckinService(db, nil, encSvc)

	// Add some proxy clients
	for i := 0; i < 10; i++ {
		proxyClient := &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:    10,
				IdleConnTimeout: 10 * time.Second,
			},
		}
		service.proxyClients.Store(fmt.Sprintf("http://proxy%d:8080", i), proxyClient)
	}

	// Call closeIdleConnections concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.closeIdleConnections()
		}()
	}

	// Should not panic or deadlock
	wg.Wait()
}

// TestHTTPClientReuse tests that HTTP clients are properly reused
func TestHTTPClientReuse(t *testing.T) {
	t.Parallel()

	manager := NewStealthClientManager(10 * time.Second)

	// Get client twice with same proxy URL
	client1 := manager.GetClient("http://proxy:8080")
	client2 := manager.GetClient("http://proxy:8080")

	// Should return the same client instance
	assert.Same(t, client1, client2, "Same proxy URL should return same client instance")

	// Different proxy URL should return different client
	client3 := manager.GetClient("http://proxy2:8080")
	assert.NotSame(t, client1, client3, "Different proxy URL should return different client instance")
}

// BenchmarkCloseIdleConnections benchmarks the closeIdleConnections method
func BenchmarkCloseIdleConnections(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	service := NewAutoCheckinService(db, nil, encSvc)

	// Add some proxy clients
	for i := 0; i < 100; i++ {
		proxyClient := &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:    10,
				IdleConnTimeout: 10 * time.Second,
			},
		}
		service.proxyClients.Store(fmt.Sprintf("http://proxy%d:8080", i), proxyClient)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		service.closeIdleConnections()
	}
}

// BenchmarkStealthClientManagerCleanup benchmarks the Cleanup method
func BenchmarkStealthClientManagerCleanup(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		manager := NewStealthClientManager(10 * time.Second)
		// Populate cache
		for j := 0; j < 100; j++ {
			manager.GetClient(fmt.Sprintf("http://proxy%d:8080", j))
		}
		b.StartTimer()

		manager.Cleanup()
	}
}

// TestAutoCheckinService_Integration tests the full lifecycle with mock server
func TestAutoCheckinService_Integration(t *testing.T) {
	t.Parallel()

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"message":"checked in"}`))
	}))
	defer server.Close()

	// Setup service
	db := setupTestDB(t)
	encSvc := setupTestEncryption(t)

	// Auto-migrate tables
	err := db.AutoMigrate(&ManagedSite{}, &ManagedSiteCheckinLog{}, &ManagedSiteSetting{})
	require.NoError(t, err)

	// Create in-memory store
	memStore := store.NewMemoryStore()

	service := NewAutoCheckinService(db, memStore, encSvc)

	// Verify service is properly initialized
	assert.NotNil(t, service.client)
	assert.NotNil(t, service.stealthClientMgr)

	// Test closeIdleConnections
	service.closeIdleConnections()

	// Test Stop
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	service.Stop(ctx)
}
