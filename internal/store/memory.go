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

// memoryList keeps list values in a circular logical order. Rotate only moves
// the head cursor, so the request hot path remains O(1) and allocation-free.
type memoryList struct {
	values []string
	head   int
}

func (l *memoryList) at(index int) string {
	return l.values[(l.head+index)%len(l.values)]
}

// MemoryStore is an in-memory key-value store that is safe for concurrent use.
type MemoryStore struct {
	mu              sync.RWMutex
	data            map[string]any
	muSubscribers   sync.RWMutex
	subscribers     map[string]map[chan *Message]struct{}
	droppedMessages atomic.Int64
	stopCleanup     chan struct{} // Channel to stop cleanup goroutine
	closeOnce       sync.Once     // Ensure Close is idempotent
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
// Idempotent: safe to call multiple times without panicking.
func (s *MemoryStore) Close() error {
	s.closeOnce.Do(func() {
		// Stop cleanup goroutine
		close(s.stopCleanup)

		// Close all subscriber channels to prevent goroutine leaks
		// This ensures all blocked goroutines on <-sub.Channel() are unblocked
		s.muSubscribers.Lock()
		for channel, subs := range s.subscribers {
			for subCh := range subs {
				close(subCh)
			}
			delete(s.subscribers, channel)
		}
		s.muSubscribers.Unlock()
	})

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
		s.deleteExpired(key)
		// A concurrent Set may have refreshed the key between the snapshot above
		// and the deletion recheck inside deleteExpired; re-read the current
		// snapshot so a refreshed value is not masked by the stale pre-expiration
		// item. A miss (or still-expired value) below means the key is truly gone.
		s.mu.RLock()
		rawItem, exists = s.data[key]
		s.mu.RUnlock()
		if !exists {
			return nil, ErrNotFound
		}
		item, ok = rawItem.(memoryStoreItem)
		if !ok {
			return nil, fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
		}
		if item.expiresAt > 0 && time.Now().UnixNano() > item.expiresAt {
			return nil, ErrNotFound
		}
		return item.value, nil
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
			s.deleteExpired(key)
			// Re-read after deleteExpired, mirroring Get: a concurrent Set may
			// have refreshed the key between our snapshot and deleteExpired's
			// recheck, and the refreshed value must not be masked by the stale
			// pre-expiration item. Non-item data below still counts as existing.
			s.mu.RLock()
			rawItem, exists = s.data[key]
			s.mu.RUnlock()
			if !exists {
				return false, nil
			}
			if item2, ok2 := rawItem.(memoryStoreItem); ok2 {
				if item2.expiresAt > 0 && time.Now().UnixNano() > item2.expiresAt {
					return false, nil
				}
			}
		}
	}

	return true, nil
}

// testHookBeforeExpiryDelete is invoked by deleteExpired before it takes the
// write lock. Tests use it to deterministically interleave a concurrent Set
// exactly between Get/Exists's snapshot read and deleteExpired's recheck; it
// is nil in production.
var testHookBeforeExpiryDelete func(key string)

func (s *MemoryStore) deleteExpired(key string) {
	if testHookBeforeExpiryDelete != nil {
		testHookBeforeExpiryDelete(key)
	}
	now := time.Now().UnixNano()
	s.mu.Lock()
	defer s.mu.Unlock()

	rawItem, exists := s.data[key]
	if !exists {
		return
	}
	item, ok := rawItem.(memoryStoreItem)
	if ok && item.expiresAt > 0 && now > item.expiresAt {
		delete(s.data, key)
	}
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

	var list *memoryList
	rawList, exists := s.data[key]
	if !exists {
		list = &memoryList{}
		s.data[key] = list
	} else {
		var ok bool
		list, ok = rawList.(*memoryList)
		if !ok {
			return fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
		}
	}

	if len(values) == 0 {
		return nil
	}

	newValues := make([]string, len(values)+len(list.values))
	for i, value := range values {
		newValues[i] = fmt.Sprint(value)
	}
	for i := range list.values {
		newValues[len(values)+i] = list.at(i)
	}
	list.values = newValues
	list.head = 0
	return nil
}

func (s *MemoryStore) LRem(key string, count int64, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rawList, exists := s.data[key]
	if !exists {
		return nil
	}

	list, ok := rawList.(*memoryList)
	if !ok {
		return fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}

	strValue := fmt.Sprint(value)

	if count != 0 {
		return fmt.Errorf("LRem with non-zero count is not implemented in MemoryStore")
	}

	remaining := 0
	for i := range list.values {
		if list.at(i) != strValue {
			remaining++
		}
	}
	if remaining == len(list.values) {
		return nil
	}

	newValues := make([]string, 0, remaining)
	for i := range list.values {
		item := list.at(i)
		if item != strValue {
			newValues = append(newValues, item)
		}
	}
	list.values = newValues
	list.head = 0
	return nil
}

func (s *MemoryStore) Rotate(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rawList, exists := s.data[key]
	if !exists {
		return "", ErrNotFound
	}

	list, ok := rawList.(*memoryList)
	if !ok {
		return "", fmt.Errorf("type mismatch: key '%s' holds a different data type", key)
	}

	if len(list.values) == 0 {
		return "", ErrNotFound
	}

	if list.head == 0 {
		list.head = len(list.values) - 1
	} else {
		list.head--
	}
	return list.values[list.head], nil
}

// LLen returns the length of a list.
// Note: This method only supports list types. For set cardinality, use SCard instead.
func (s *MemoryStore) LLen(key string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rawItem, exists := s.data[key]
	if !exists {
		return 0, nil
	}

	list, ok := rawItem.(*memoryList)
	if !ok {
		return 0, fmt.Errorf("type mismatch: key '%s' is not a list", key)
	}

	return int64(len(list.values)), nil
}

// SCard returns the cardinality (number of elements) of a set.
// This follows Redis semantics where SCARD is used for sets and LLEN for lists.
func (s *MemoryStore) SCard(key string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rawItem, exists := s.data[key]
	if !exists {
		return 0, nil
	}

	set, ok := rawItem.(map[string]struct{})
	if !ok {
		return 0, fmt.Errorf("type mismatch: key '%s' is not a set", key)
	}

	return int64(len(set)), nil
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
			// Only close if still tracked (not already closed by MemoryStore.Close)
			if _, exists := subs[ms.msgChan]; exists {
				delete(subs, ms.msgChan)
				close(ms.msgChan)
				if len(subs) == 0 {
					delete(ms.store.subscribers, ms.channel)
				}
			}
			// If not found, MemoryStore.Close() already closed it
		}
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
