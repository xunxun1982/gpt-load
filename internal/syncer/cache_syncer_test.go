package syncer

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/store"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStore is a mock implementation of store.Store for testing
type mockStore struct {
	mu            sync.RWMutex
	subscriptions map[string]*mockSubscription
	published     map[string][]byte
	publishErr    error
	subscribeErr  error
}

func newMockStore() *mockStore {
	return &mockStore{
		subscriptions: make(map[string]*mockSubscription),
		published:     make(map[string][]byte),
	}
}

func (m *mockStore) Get(key string) ([]byte, error) {
	return nil, nil
}

func (m *mockStore) Set(key string, value []byte, ttl time.Duration) error {
	return nil
}

func (m *mockStore) Delete(key string) error {
	return nil
}

func (m *mockStore) Del(keys ...string) error {
	return nil
}

func (m *mockStore) SetNX(key string, value []byte, ttl time.Duration) (bool, error) {
	return true, nil
}

func (m *mockStore) LLen(key string) (int64, error) {
	return 0, nil
}

func (m *mockStore) SAdd(key string, members ...any) error {
	return nil
}

func (m *mockStore) SPopN(key string, count int64) ([]string, error) {
	return nil, nil
}

func (m *mockStore) Exists(key string) (bool, error) {
	return false, nil
}

func (m *mockStore) HSet(key string, values map[string]any) error {
	return nil
}

func (m *mockStore) HGet(key string, field string) (string, error) {
	return "", nil
}

func (m *mockStore) HGetAll(key string) (map[string]string, error) {
	return nil, nil
}

func (m *mockStore) HIncrBy(key string, field string, incr int64) (int64, error) {
	return 0, nil
}

func (m *mockStore) LPush(key string, values ...any) error {
	return nil
}

func (m *mockStore) LRem(key string, count int64, value any) error {
	return nil
}

func (m *mockStore) Rotate(key string) (string, error) {
	return "", nil
}

func (m *mockStore) Publish(channel string, message []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.publishErr != nil {
		return m.publishErr
	}

	m.published[channel] = message

	// Notify all subscriptions
	if sub, ok := m.subscriptions[channel]; ok {
		sub.mu.Lock()
		if !sub.closed {
			select {
			case sub.ch <- &store.Message{Channel: channel, Payload: message}:
			default:
			}
		}
		sub.mu.Unlock()
	}

	return nil
}

func (m *mockStore) Subscribe(channel string) (store.Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.subscribeErr != nil {
		return nil, m.subscribeErr
	}

	sub := &mockSubscription{
		ch:     make(chan *store.Message, 10),
		closed: false,
	}
	m.subscriptions[channel] = sub

	return sub, nil
}

func (m *mockStore) Clear() error {
	return nil
}

func (m *mockStore) Close() error {
	m.mu.Lock()
	subs := make([]*mockSubscription, 0, len(m.subscriptions))
	for _, sub := range m.subscriptions {
		subs = append(subs, sub)
	}
	m.subscriptions = make(map[string]*mockSubscription)
	m.mu.Unlock()

	// Close all subscriptions to prevent goroutine leaks
	for _, sub := range subs {
		_ = sub.Close()
	}
	return nil
}

// mockSubscription implements store.Subscription
type mockSubscription struct {
	ch     chan *store.Message
	closed bool
	mu     sync.Mutex
}

func (s *mockSubscription) Channel() <-chan *store.Message {
	return s.ch
}

func (s *mockSubscription) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed {
		close(s.ch)
		s.closed = true
	}
	return nil
}

// TestNewCacheSyncer tests creating a new cache syncer
func TestNewCacheSyncer(t *testing.T) {
	t.Run("successful creation", func(t *testing.T) {
		store := newMockStore()
		loader := func() (string, error) {
			return "test data", nil
		}

		logger := logrus.NewEntry(logrus.New())
		syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)

		require.NoError(t, err)
		assert.NotNil(t, syncer)
		assert.Equal(t, "test data", syncer.Get())

		syncer.Stop()
	})

	t.Run("loader error", func(t *testing.T) {
		store := newMockStore()
		loader := func() (string, error) {
			return "", errors.New("load error")
		}

		logger := logrus.NewEntry(logrus.New())
		syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)

		assert.Error(t, err)
		assert.Nil(t, syncer)
	})
}

// TestGet tests getting cached data
func TestGet(t *testing.T) {
	store := newMockStore()
	loader := func() (int, error) {
		return 42, nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	require.NoError(t, err)
	defer syncer.Stop()

	result := syncer.Get()
	assert.Equal(t, 42, result)
}

// TestInvalidate tests cache invalidation
func TestInvalidate(t *testing.T) {
	store := newMockStore()
	loader := func() (string, error) {
		return "test data", nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	require.NoError(t, err)
	defer syncer.Stop()

	err = syncer.Invalidate()
	assert.NoError(t, err)

	// Check that message was published
	store.mu.RLock()
	msg, ok := store.published["test-channel"]
	store.mu.RUnlock()

	assert.True(t, ok)
	assert.Equal(t, []byte("reload"), msg)
}

// TestReload tests manual cache reload
func TestReload(t *testing.T) {
	store := newMockStore()
	counter := 0
	loader := func() (int, error) {
		counter++
		return counter, nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	require.NoError(t, err)
	defer syncer.Stop()

	// Initial value should be 1
	assert.Equal(t, 1, syncer.Get())

	// Reload should increment counter
	err = syncer.Reload()
	assert.NoError(t, err)
	assert.Equal(t, 2, syncer.Get())
}

// TestAfterReloadHook tests the after reload hook
func TestAfterReloadHook(t *testing.T) {
	store := newMockStore()
	loader := func() (string, error) {
		return "test data", nil
	}

	hookCalled := false
	var hookValue string
	afterReload := func(newValue string) {
		hookCalled = true
		hookValue = newValue
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, afterReload)
	require.NoError(t, err)
	defer syncer.Stop()

	// Hook should be called during initial load
	assert.True(t, hookCalled)
	assert.Equal(t, "test data", hookValue)

	// Reset and test reload
	hookCalled = false
	hookValue = ""

	err = syncer.Reload()
	assert.NoError(t, err)
	assert.True(t, hookCalled)
	assert.Equal(t, "test data", hookValue)
}

// TestStop tests stopping the syncer
func TestStop(t *testing.T) {
	store := newMockStore()
	loader := func() (string, error) {
		return "test data", nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	require.NoError(t, err)

	// Stop should not panic
	syncer.Stop()

	// Verify syncer is stopped (Get should still work)
	result := syncer.Get()
	assert.Equal(t, "test data", result)
}

// TestConcurrentAccess tests concurrent access to cache
func TestConcurrentAccess(t *testing.T) {
	store := newMockStore()
	loader := func() (int, error) {
		return 42, nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	require.NoError(t, err)
	defer syncer.Stop()

	// Concurrent reads
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = syncer.Get()
		}()
	}

	// Concurrent reloads
	errCh := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := syncer.Reload(); err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

// TestReloadError tests handling of reload errors
func TestReloadError(t *testing.T) {
	store := newMockStore()
	var shouldFail atomic.Bool
	loader := func() (string, error) {
		if shouldFail.Load() {
			return "", errors.New("load error")
		}
		return "test data", nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	require.NoError(t, err)
	defer syncer.Stop()

	// Initial value should be set
	assert.Equal(t, "test data", syncer.Get())

	// Make loader fail using atomic operation to avoid data race
	shouldFail.Store(true)

	// Reload should return error but cache should remain unchanged
	err = syncer.Reload()
	assert.Error(t, err)
	assert.Equal(t, "test data", syncer.Get())
}

// BenchmarkGet benchmarks cache get operation
func BenchmarkGet(b *testing.B) {
	store := newMockStore()
	loader := func() (string, error) {
		return "test data", nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer syncer.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = syncer.Get()
	}
}

// BenchmarkReload benchmarks cache reload operation
func BenchmarkReload(b *testing.B) {
	store := newMockStore()
	loader := func() (string, error) {
		return "test data", nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer syncer.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := syncer.Reload(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConcurrentGet benchmarks concurrent cache get operations
func BenchmarkConcurrentGet(b *testing.B) {
	store := newMockStore()
	loader := func() (string, error) {
		return "test data", nil
	}

	logger := logrus.NewEntry(logrus.New())
	syncer, err := NewCacheSyncer(loader, store, "test-channel", logger, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer syncer.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = syncer.Get()
		}
	})
}
