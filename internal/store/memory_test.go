package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoryStore_SetGet tests basic set and get operations
func TestMemoryStore_SetGet(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "test_key"
	value := []byte("test_value")

	// Set value
	err := store.Set(key, value, 0)
	require.NoError(t, err)

	// Get value
	retrieved, err := store.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, retrieved)
}

// TestMemoryStore_GetNonExistent tests getting a non-existent key
func TestMemoryStore_GetNonExistent(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	_, err := store.Get("non_existent")
	assert.Equal(t, ErrNotFound, err)
}

// TestMemoryStore_SetWithTTL tests set with TTL
func TestMemoryStore_SetWithTTL(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "ttl_key"
	value := []byte("ttl_value")
	ttl := 100 * time.Millisecond

	// Set with TTL
	err := store.Set(key, value, ttl)
	require.NoError(t, err)

	// Get immediately
	retrieved, err := store.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value, retrieved)

	// Wait for expiration using Eventually to avoid flakiness
	require.Eventually(t, func() bool {
		_, err = store.Get(key)
		return err == ErrNotFound
	}, time.Second, 10*time.Millisecond, "Key should expire after TTL")
}

// TestMemoryStore_Delete tests delete operation
func TestMemoryStore_Delete(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "delete_key"
	value := []byte("delete_value")

	// Set value
	err := store.Set(key, value, 0)
	require.NoError(t, err)

	// Delete
	err = store.Delete(key)
	require.NoError(t, err)

	// Verify deleted
	_, err = store.Get(key)
	assert.Equal(t, ErrNotFound, err)
}

// TestMemoryStore_Del tests batch delete operation
func TestMemoryStore_Del(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Set multiple keys
	keys := []string{"key1", "key2", "key3"}
	for _, key := range keys {
		err := store.Set(key, []byte(key+"_value"), 0)
		require.NoError(t, err)
	}

	// Delete all
	err := store.Del(keys...)
	require.NoError(t, err)

	// Verify all deleted
	for _, key := range keys {
		_, err := store.Get(key)
		assert.Equal(t, ErrNotFound, err)
	}
}

// TestMemoryStore_Exists tests exists operation
func TestMemoryStore_Exists(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "exists_key"
	value := []byte("exists_value")

	// Check non-existent
	exists, err := store.Exists(key)
	require.NoError(t, err)
	assert.False(t, exists)

	// Set value
	err = store.Set(key, value, 0)
	require.NoError(t, err)

	// Check exists
	exists, err = store.Exists(key)
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestMemoryStore_SetNX tests set if not exists operation
func TestMemoryStore_SetNX(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "setnx_key"
	value1 := []byte("value1")
	value2 := []byte("value2")

	// First SetNX should succeed
	ok, err := store.SetNX(key, value1, 0)
	require.NoError(t, err)
	assert.True(t, ok)

	// Second SetNX should fail
	ok, err = store.SetNX(key, value2, 0)
	require.NoError(t, err)
	assert.False(t, ok)

	// Verify original value
	retrieved, err := store.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value1, retrieved)
}

// TestMemoryStore_SetNXWithExpiredKey tests SetNX with expired key
func TestMemoryStore_SetNXWithExpiredKey(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "setnx_expired_key"
	value1 := []byte("value1")
	value2 := []byte("value2")

	// Set with short TTL
	ok, err := store.SetNX(key, value1, 50*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, ok)

	// Wait for expiration using Eventually to avoid flakiness
	require.Eventually(t, func() bool {
		_, err = store.Get(key)
		return err == ErrNotFound
	}, time.Second, 10*time.Millisecond, "Key should expire after TTL")

	// SetNX should succeed after expiration
	ok, err = store.SetNX(key, value2, 0)
	require.NoError(t, err)
	assert.True(t, ok)

	// Verify new value
	retrieved, err := store.Get(key)
	require.NoError(t, err)
	assert.Equal(t, value2, retrieved)
}

// TestMemoryStore_HSet tests hash set operation
func TestMemoryStore_HSet(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "hash_key"
	values := map[string]any{
		"field1": "value1",
		"field2": 123,
		"field3": true,
	}

	err := store.HSet(key, values)
	require.NoError(t, err)

	// Get all fields
	result, err := store.HGetAll(key)
	require.NoError(t, err)
	assert.Equal(t, "value1", result["field1"])
	assert.Equal(t, "123", result["field2"])
	assert.Equal(t, "true", result["field3"])
}

// TestMemoryStore_HIncrBy tests hash increment operation
func TestMemoryStore_HIncrBy(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "hash_incr_key"
	field := "counter"

	// Increment non-existent field
	newVal, err := store.HIncrBy(key, field, 5)
	require.NoError(t, err)
	assert.Equal(t, int64(5), newVal)

	// Increment again
	newVal, err = store.HIncrBy(key, field, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(8), newVal)

	// Decrement
	newVal, err = store.HIncrBy(key, field, -2)
	require.NoError(t, err)
	assert.Equal(t, int64(6), newVal)
}

// TestMemoryStore_LPush tests list push operation
func TestMemoryStore_LPush(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "list_key"

	// Push values
	err := store.LPush(key, "value1", "value2", "value3")
	require.NoError(t, err)

	// Check length
	length, err := store.LLen(key)
	require.NoError(t, err)
	assert.Equal(t, int64(3), length)
}

// TestMemoryStore_LRem tests list remove operation
func TestMemoryStore_LRem(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "list_rem_key"

	// Push values
	err := store.LPush(key, "value1", "value2", "value1", "value3")
	require.NoError(t, err)

	// Remove all occurrences of "value1"
	err = store.LRem(key, 0, "value1")
	require.NoError(t, err)

	// Check length
	length, err := store.LLen(key)
	require.NoError(t, err)
	assert.Equal(t, int64(2), length)
}

// TestMemoryStore_Rotate tests list rotate operation
func TestMemoryStore_Rotate(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "list_rotate_key"

	// Push values
	err := store.LPush(key, "value1", "value2", "value3")
	require.NoError(t, err)

	// Rotate
	item, err := store.Rotate(key)
	require.NoError(t, err)
	assert.NotEmpty(t, item)

	// Length should remain the same
	length, err := store.LLen(key)
	require.NoError(t, err)
	assert.Equal(t, int64(3), length)
}

// TestMemoryStore_SAdd tests set add operation
func TestMemoryStore_SAdd(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "set_key"

	// Add members
	err := store.SAdd(key, "member1", "member2", "member3")
	require.NoError(t, err)

	// Add duplicate
	err = store.SAdd(key, "member1")
	require.NoError(t, err)
}

// TestMemoryStore_SPopN tests set pop operation
func TestMemoryStore_SPopN(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	key := "set_pop_key"

	// Add members
	err := store.SAdd(key, "member1", "member2", "member3", "member4")
	require.NoError(t, err)

	// Pop 2 members
	popped, err := store.SPopN(key, 2)
	require.NoError(t, err)
	assert.Len(t, popped, 2)

	// Pop remaining
	popped, err = store.SPopN(key, 10)
	require.NoError(t, err)
	assert.Len(t, popped, 2)
}

// TestMemoryStore_PubSub tests publish/subscribe operations
func TestMemoryStore_PubSub(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	channel := "test_channel"
	message := []byte("test_message")

	// Subscribe
	sub, err := store.Subscribe(channel)
	require.NoError(t, err)
	defer sub.Close()

	// Publish
	err = store.Publish(channel, message)
	require.NoError(t, err)

	// Receive message
	select {
	case msg := <-sub.Channel():
		assert.Equal(t, channel, msg.Channel)
		assert.Equal(t, message, msg.Payload)
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

// TestMemoryStore_PubSubMultipleSubscribers tests multiple subscribers
func TestMemoryStore_PubSubMultipleSubscribers(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	channel := "multi_channel"
	message := []byte("multi_message")

	// Subscribe multiple times
	sub1, err := store.Subscribe(channel)
	require.NoError(t, err)
	defer sub1.Close()

	sub2, err := store.Subscribe(channel)
	require.NoError(t, err)
	defer sub2.Close()

	// Publish
	err = store.Publish(channel, message)
	require.NoError(t, err)

	// Both subscribers should receive
	received := 0
	timeout := time.After(1 * time.Second)

	for received < 2 {
		select {
		case msg := <-sub1.Channel():
			assert.Equal(t, message, msg.Payload)
			received++
		case msg := <-sub2.Channel():
			assert.Equal(t, message, msg.Payload)
			received++
		case <-timeout:
			t.Fatalf("Timeout, only received %d messages", received)
		}
	}
}

// TestMemoryStore_Clear tests clear operation
func TestMemoryStore_Clear(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	// Set multiple keys
	for i := 0; i < 10; i++ {
		key := "key_" + string(rune('0'+i))
		err := store.Set(key, []byte("value"), 0)
		require.NoError(t, err)
	}

	// Clear
	err := store.Clear()
	require.NoError(t, err)

	// Verify all cleared
	for i := 0; i < 10; i++ {
		key := "key_" + string(rune('0'+i))
		_, err := store.Get(key)
		assert.Equal(t, ErrNotFound, err)
	}
}

// TestMemoryStore_ConcurrentAccess tests concurrent access
func TestMemoryStore_ConcurrentAccess(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()

	const goroutines = 100
	const operations = 100

	done := make(chan bool, goroutines)
	errCh := make(chan error, goroutines*operations)

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			for j := 0; j < operations; j++ {
				key := "concurrent_key"
				value := []byte("value")
				if err := store.Set(key, value, 0); err != nil {
					errCh <- err
					break
				}
			}
			done <- true
		}(i)
	}

	// Wait for completion
	for i := 0; i < goroutines; i++ {
		<-done
	}
	close(errCh)

	// Check for errors
	for err := range errCh {
		assert.NoError(t, err)
	}

	// Verify store is still functional
	_, err := store.Get("concurrent_key")
	assert.NoError(t, err)
}

// BenchmarkMemoryStore_Set benchmarks set operation
func BenchmarkMemoryStore_Set(b *testing.B) {
	store := NewMemoryStore()
	defer store.Close()

	value := []byte("benchmark_value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Set("key", value, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemoryStore_Get benchmarks get operation
func BenchmarkMemoryStore_Get(b *testing.B) {
	store := NewMemoryStore()
	defer store.Close()

	value := []byte("benchmark_value")
	if err := store.Set("key", value, 0); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Get("key"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemoryStore_HIncrBy benchmarks hash increment operation
func BenchmarkMemoryStore_HIncrBy(b *testing.B) {
	store := NewMemoryStore()
	defer store.Close()

	key := "hash_key"
	field := "counter"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.HIncrBy(key, field, 1); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemoryStore_Publish benchmarks publish operation
func BenchmarkMemoryStore_Publish(b *testing.B) {
	store := NewMemoryStore()
	defer store.Close()

	channel := "bench_channel"
	message := []byte("bench_message")

	// Subscribe to avoid dropped message warnings
	sub, err := store.Subscribe(channel)
	if err != nil {
		b.Fatal(err)
	}
	defer sub.Close()

	// Drain messages in background
	go func() {
		for range sub.Channel() {
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Publish(channel, message); err != nil {
			b.Fatal(err)
		}
	}
}
