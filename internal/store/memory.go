package store

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// memoryStoreItem holds the value and expiration timestamp for a key.
type memoryStoreItem struct {
	value     []byte
	expiresAt int64 // Unix-nano timestamp. 0 for no expiry.
}

// MemoryStore is an in-memory key-value store that is safe for concurrent use.
type MemoryStore struct {
	mu              sync.RWMutex
	data            map[string]any
	muSubscribers   sync.RWMutex
	subscribers     map[string]map[chan *Message]struct{}
	droppedMessages atomic.Int64
	stopCleanup     chan struct{} // Channel to stop cleanup goroutine
}

// NOTE: This store uses the global logrus logger configured at application startup to stay aligned
// with the rest of the project. If pluggable logging is required in the future, this can be
// refactored to depend on an internal logging interface instead of the package-level logger.

// NewMemoryStore creates and returns a new MemoryStore instance.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{
		data:        make(map[string]any),
		subscribers: make(map[string]map[chan *Message]struct{}),
		stopCleanup: make(chan struct{}),
	}
	// Start background goroutine to periodically clean expired items
	// This prevents memory leaks from expired items that are never accessed
	go s.cleanupExpiredItems()
	return s
}

// Close cleans up resources.
func (s *MemoryStore) Close() error {
	// Stop cleanup goroutine
	close(s.stopCleanup)

	// Close all subscriber channels to prevent goroutine leaks
	// Note: We don't close channels directly here to avoid double-close panics.
	// Instead, we remove them from tracking and let memorySubscription.Close() handle cleanup.
	s.muSubscribers.Lock()
	for channel := range s.subscribers {
		delete(s.subscribers, channel)
	}
	s.muSubscribers.Unlock()

	return nil
}

// Set stores a key-value pair.
func (s *MemoryStore) Set(key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expiresAt int64
	if ttl > 0 {
		expiresAt = time.Now().UnixNano() + ttl.Nanoseconds()
	}

	s.data[key] = memoryStoreItem{
		value:     value,
		expiresAt: expiresAt,
	}
	return nil
}

// Get retrieves a value by its key.
func (s *MemoryStore) Get(key string) ([]byte, error) {
	s.mu.RLock()
	rawItem, exists := s.data[key]
	s.mu.RUnlock()

	if !exists {
		return nil, ErrNotFound
	}

	item, ok := rawItem.(memoryStoreItem)
	if !ok {
		return nil, fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}

	if item.expiresAt > 0 && time.Now().UnixNano() > item.expiresAt {
		s.mu.Lock()
		delete(s.data, key)
		s.mu.Unlock()
		return nil, ErrNotFound
	}

	return item.value, nil
}

// Delete removes a value by its key.
func (s *MemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// Del removes multiple values by their keys.
func (s *MemoryStore) Del(keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.data, key)
	}
	return nil
}

// Exists checks if a key exists.
func (s *MemoryStore) Exists(key string) (bool, error) {
	s.mu.RLock()
	rawItem, exists := s.data[key]
	s.mu.RUnlock()

	if !exists {
		return false, nil
	}

	if item, ok := rawItem.(memoryStoreItem); ok {
		if item.expiresAt > 0 && time.Now().UnixNano() > item.expiresAt {
			s.mu.Lock()
			delete(s.data, key)
			s.mu.Unlock()
			return false, nil
		}
	}

	return true, nil
}

// SetNX sets a key-value pair if the key does not already exist.
func (s *MemoryStore) SetNX(key string, value []byte, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rawItem, exists := s.data[key]
	if exists {
		if item, ok := rawItem.(memoryStoreItem); ok {
			if item.expiresAt == 0 || time.Now().UnixNano() < item.expiresAt {
				return false, nil
			}
		} else {
			// Key exists but is not a simple K/V item, treat as existing
			return false, nil
		}
	}

	// Key does not exist or is expired, so we can set it.
	var expiresAt int64
	if ttl > 0 {
		expiresAt = time.Now().UnixNano() + ttl.Nanoseconds()
	}
	s.data[key] = memoryStoreItem{
		value:     value,
		expiresAt: expiresAt,
	}
	return true, nil
}

// --- HASH operations ---

func (s *MemoryStore) HSet(key string, values map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var hash map[string]string
	rawHash, exists := s.data[key]
	if !exists {
		hash = make(map[string]string)
		s.data[key] = hash
	} else {
		var ok bool
		hash, ok = rawHash.(map[string]string)
		if !ok {
			return fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
		}
	}

	for field, value := range values {
		hash[field] = fmt.Sprint(value)
	}
	return nil
}

func (s *MemoryStore) HGetAll(key string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rawHash, exists := s.data[key]
	if !exists {
		return make(map[string]string), nil
	}

	hash, ok := rawHash.(map[string]string)
	if !ok {
		return nil, fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}

	result := make(map[string]string, len(hash))
	for k, v := range hash {
		result[k] = v
	}

	return result, nil
}

func (s *MemoryStore) HIncrBy(key, field string, incr int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var hash map[string]string
	rawHash, exists := s.data[key]
	if !exists {
		hash = make(map[string]string)
		s.data[key] = hash
	} else {
		var ok bool
		hash, ok = rawHash.(map[string]string)
		if !ok {
			return 0, fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
		}
	}

	currentVal, _ := strconv.ParseInt(hash[field], 10, 64)
	newVal := currentVal + incr
	hash[field] = strconv.FormatInt(newVal, 10)

	return newVal, nil
}

// --- LIST operations ---

func (s *MemoryStore) LPush(key string, values ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []string
	rawList, exists := s.data[key]
	if !exists {
		list = make([]string, 0)
	} else {
		var ok bool
		list, ok = rawList.([]string)
		if !ok {
			return fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
		}
	}

	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = fmt.Sprint(v)
	}

	s.data[key] = append(strValues, list...) // Prepend
	return nil
}

func (s *MemoryStore) LRem(key string, count int64, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rawList, exists := s.data[key]
	if !exists {
		return nil
	}

	list, ok := rawList.([]string)
	if !ok {
		return fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}

	strValue := fmt.Sprint(value)
	newList := make([]string, 0, len(list))

	if count != 0 {
		return fmt.Errorf("LRem with non-zero count is not implemented in MemoryStore")
	}

	for _, item := range list {
		if item != strValue {
			newList = append(newList, item)
		}
	}
	s.data[key] = newList
	return nil
}

func (s *MemoryStore) Rotate(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rawList, exists := s.data[key]
	if !exists {
		return "", ErrNotFound
	}

	list, ok := rawList.([]string)
	if !ok {
		return "", fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}

	if len(list) == 0 {
		return "", ErrNotFound
	}

	lastIndex := len(list) - 1
	item := list[lastIndex]

	// "LPUSH"
	newList := append([]string{item}, list[:lastIndex]...)
	s.data[key] = newList

	return item, nil
}

// LLen returns the length of a list.
func (s *MemoryStore) LLen(key string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rawItem, exists := s.data[key]
	if !exists {
		return 0, nil
	}

	// Support both list and set types for flexibility
	switch v := rawItem.(type) {
	case []string:
		return int64(len(v)), nil
	case map[string]struct{}:
		return int64(len(v)), nil
	default:
		return 0, fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}
}

// --- SET operations ---

// SAdd adds members to a set.
func (s *MemoryStore) SAdd(key string, members ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var set map[string]struct{}
	rawSet, exists := s.data[key]
	if !exists {
		set = make(map[string]struct{})
		s.data[key] = set
	} else {
		var ok bool
		set, ok = rawSet.(map[string]struct{})
		if !ok {
			return fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
		}
	}

	for _, member := range members {
		set[fmt.Sprint(member)] = struct{}{}
	}
	return nil
}

// SPopN randomly removes and returns the given number of members from a set.
func (s *MemoryStore) SPopN(key string, count int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rawSet, exists := s.data[key]
	if !exists {
		return []string{}, nil
	}

	set, ok := rawSet.(map[string]struct{})
	if !ok {
		return nil, fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}

	if count > int64(len(set)) {
		count = int64(len(set))
	}

	popped := make([]string, 0, count)
	for member := range set {
		if int64(len(popped)) >= count {
			break
		}
		popped = append(popped, member)
		delete(set, member)
	}

	return popped, nil
}

// --- Pub/Sub operations ---

// memorySubscription implements the Subscription interface for the in-memory store.
type memorySubscription struct {
	store     *MemoryStore
	channel   string
	msgChan   chan *Message
	closeOnce sync.Once // Ensure Close is idempotent to prevent double-close panics
}

// Channel returns the message channel for the subscription.
func (ms *memorySubscription) Channel() <-chan *Message {
	return ms.msgChan
}

// Close removes the subscription from the store.
// Uses sync.Once to ensure idempotent behavior and prevent double-close panics.
func (ms *memorySubscription) Close() error {
	ms.closeOnce.Do(func() {
		ms.store.muSubscribers.Lock()
		defer ms.store.muSubscribers.Unlock()

		if subs, ok := ms.store.subscribers[ms.channel]; ok {
			delete(subs, ms.msgChan)
			if len(subs) == 0 {
				delete(ms.store.subscribers, ms.channel)
			}
		}
		close(ms.msgChan)
	})
	return nil
}

// Publish sends a message to all subscribers of a channel.
// NOTE: This uses at-most-once delivery semantics. Messages may be dropped under backpressure
// to avoid blocking publishers and to prevent unbounded memory or goroutine growth.
// High-throughput benchmarks and acceptable drop thresholds should be validated by callers.
func (s *MemoryStore) Publish(channel string, message []byte) error {
	s.muSubscribers.RLock()
	defer s.muSubscribers.RUnlock()

	msg := &Message{
		Channel: channel,
		Payload: message,
	}

	if subs, ok := s.subscribers[channel]; ok {
		subscriberCount := len(subs)
		payloadSize := len(message)
		droppedCount := 0

		for subCh := range subs {
			select {
			case subCh <- msg:
			default:
				droppedCount++
			}
		}

		if droppedCount > 0 {
			s.droppedMessages.Add(int64(droppedCount))

			if logrus.IsLevelEnabled(logrus.DebugLevel) {
				logrus.WithFields(logrus.Fields{
					"channel":            channel,
					"subscribers":        subscriberCount,
					"dropped_this_call":  droppedCount,
					"payload_size_bytes": payloadSize,
					"dropped_total":      s.droppedMessages.Load(),
				}).Debug("Dropped messages due to full subscriber buffers")
			}
		}
	}
	return nil
}

// Subscribe listens for messages on a given channel.
func (s *MemoryStore) Subscribe(channel string) (Subscription, error) {
	s.muSubscribers.Lock()
	defer s.muSubscribers.Unlock()

	msgChan := make(chan *Message, 10) // Buffered channel

	if _, ok := s.subscribers[channel]; !ok {
		s.subscribers[channel] = make(map[chan *Message]struct{})
	}
	s.subscribers[channel][msgChan] = struct{}{}

	sub := &memorySubscription{
		store:   s,
		channel: channel,
		msgChan: msgChan,
	}

	return sub, nil
}

// Clear clears all data.
func (s *MemoryStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear all data
	s.data = make(map[string]any)

	return nil
}

// DroppedMessages returns the total number of messages dropped due to subscriber backpressure.
// This is a lightweight global metric for observability and does not reset the internal counter.
// Per-channel drop statistics are intentionally not tracked here to keep the implementation simple
// and fast; callers can layer additional metrics if needed.
func (s *MemoryStore) DroppedMessages() int64 {
	return s.droppedMessages.Load()
}

// cleanupExpiredItems periodically removes expired items from the store.
// This prevents memory leaks from expired items that are never accessed again.
// Runs every 5 minutes to balance memory usage and CPU overhead.
func (s *MemoryStore) cleanupExpiredItems() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.performCleanup()
		case <-s.stopCleanup:
			logrus.Debug("MemoryStore cleanup goroutine stopped")
			return
		}
	}
}

// performCleanup scans the store and removes expired items.
func (s *MemoryStore) performCleanup() {
	now := time.Now().UnixNano()
	expiredKeys := make([]string, 0, 100) // Pre-allocate for common case

	// First pass: identify expired keys (read lock)
	s.mu.RLock()
	for key, rawItem := range s.data {
		if item, ok := rawItem.(memoryStoreItem); ok {
			if item.expiresAt > 0 && now > item.expiresAt {
				expiredKeys = append(expiredKeys, key)
			}
		}
	}
	s.mu.RUnlock()

	// Second pass: delete expired keys (write lock)
	if len(expiredKeys) > 0 {
		deletedCount := 0
		s.mu.Lock()
		for _, key := range expiredKeys {
			// Double-check expiration under write lock to avoid race conditions
			if rawItem, exists := s.data[key]; exists {
				if item, ok := rawItem.(memoryStoreItem); ok {
					if item.expiresAt > 0 && now > item.expiresAt {
						delete(s.data, key)
						deletedCount++
					}
				}
			}
		}
		s.mu.Unlock()

		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.Debugf("MemoryStore cleanup: removed %d expired items", deletedCount)
		}
	}
}
