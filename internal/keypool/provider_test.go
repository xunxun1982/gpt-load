package keypool

import (
	"context"
	"fmt"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type blockingHGetAllStore struct {
	store.Store
	started chan struct{}
	release chan struct{}
}

type resetLifecycleBarrierStore struct {
	store.Store
	existsStarted chan struct{}
	releaseExists chan struct{}
	deleteStarted chan struct{}
	existsOnce    sync.Once
}

type keyedHSetBarrierStore struct {
	store.Store
	blockKey string
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

type loadListDeleteBarrierStore struct {
	store.Store
	listKey       string
	deleteStarted chan struct{}
	releaseDelete chan struct{}
	once          sync.Once
}

type namedDialector struct {
	gorm.Dialector
	name string
}

func (d namedDialector) Name() string {
	return d.name
}

func (s *blockingHGetAllStore) HGetAll(key string) (map[string]string, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.release
	return s.Store.HGetAll(key)
}

func (s *resetLifecycleBarrierStore) Exists(key string) (bool, error) {
	s.existsOnce.Do(func() { close(s.existsStarted) })
	<-s.releaseExists
	return s.Store.Exists(key)
}

func (s *resetLifecycleBarrierStore) LRem(key string, count int64, value any) error {
	select {
	case s.deleteStarted <- struct{}{}:
	default:
	}
	return s.Store.LRem(key, count, value)
}

func (s *keyedHSetBarrierStore) HSet(key string, values map[string]any) error {
	if key == s.blockKey {
		s.once.Do(func() {
			close(s.started)
			<-s.release
		})
	}
	return s.Store.HSet(key, values)
}

func (s *loadListDeleteBarrierStore) Delete(key string) error {
	if key == s.listKey {
		s.once.Do(func() {
			close(s.deleteStarted)
			<-s.releaseDelete
		})
	}
	return s.Store.Delete(key)
}

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper() // Mark as test helper for better stack traces
	skipIfNoSQLite(t)

	// Use shared cache mode to allow multiple connections to access the same in-memory database
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Auto-migrate test models
	err = db.AutoMigrate(&models.APIKey{}, &models.Group{})
	require.NoError(t, err)

	return db
}

// setupTestProvider creates a test KeyProvider with in-memory store
func setupTestProvider(t *testing.T) (*KeyProvider, *gorm.DB, store.Store) {
	t.Helper() // Mark as test helper for better stack traces
	db := setupTestDB(t)
	memStore := store.NewMemoryStore()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	settingsManager := config.NewSystemSettingsManager()

	provider := NewProvider(db, memStore, settingsManager, encSvc)
	return provider, db, memStore
}

// createTestGroup creates a test group with required fields and unique name
func createTestGroup(t *testing.T, db *gorm.DB, name string) *models.Group {
	t.Helper()
	// Add timestamp to ensure unique names in shared cache mode
	uniqueName := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
	group := &models.Group{
		Name:        uniqueName,
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)
	return group
}

func createStatusTestKey(t *testing.T, db *gorm.DB, keyStore store.Store, groupID uint, failureCount int64) *models.APIKey {
	t.Helper()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	encryptedKey, err := encSvc.Encrypt(fmt.Sprintf("sk-status-%d", time.Now().UnixNano()))
	require.NoError(t, err)

	apiKey := &models.APIKey{
		GroupID:      groupID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash(encryptedKey),
		Status:       models.KeyStatusActive,
		FailureCount: failureCount,
	}
	require.NoError(t, db.Create(apiKey).Error)

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	require.NoError(t, keyStore.HSet(keyHashKey, map[string]any{
		"id":            apiKey.ID,
		"key_string":    encryptedKey,
		"key_hash":      apiKey.KeyHash,
		"status":        models.KeyStatusActive,
		"failure_count": failureCount,
		"group_id":      groupID,
		"created_at":    time.Now().Unix(),
	}))
	require.NoError(t, keyStore.LPush(fmt.Sprintf("group:%d:active_keys", groupID), apiKey.ID))
	return apiKey
}

func TestNewProvider(t *testing.T) {
	provider, _, _ := setupTestProvider(t)
	defer provider.Stop()
	assert.NotNil(t, provider)
	assert.NotNil(t, provider.db)
	assert.NotNil(t, provider.store)
	assert.NotNil(t, provider.statusCond)
	assert.NotNil(t, provider.statusEntries)
}

func TestStatusUpdateWorkerCountSerializesSQLiteDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "sqlite3"} {
		t.Run(dialect, func(t *testing.T) {
			db := &gorm.DB{Config: &gorm.Config{Dialector: namedDialector{
				Dialector: sqlite.Open(":memory:"),
				name:      dialect,
			}}}
			require.Equal(t, 1, statusUpdateWorkerCount(db))
		})
	}
}

func TestNewProviderSerializesSQLiteStatusWorkers(t *testing.T) {
	db := setupTestDB(t)
	memStore := store.NewMemoryStore()
	blockingStore := &blockingHGetAllStore{
		Store:   memStore,
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	provider := NewProvider(db, blockingStore, settingsManager, encSvc)
	defer func() {
		close(blockingStore.release)
		provider.Stop()
		require.NoError(t, memStore.Close())
	}()

	group := &models.Group{ID: 1}
	provider.UpdateStatus(&models.APIKey{ID: 1}, group, false, "upstream failure")
	provider.UpdateStatus(&models.APIKey{ID: 2}, group, false, "upstream failure")

	select {
	case <-blockingStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("status update worker did not start")
	}
	select {
	case <-blockingStore.started:
		t.Fatal("SQLite status updates must use one worker to match its single-writer database")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProviderStop(t *testing.T) {
	provider, _, _ := setupTestProvider(t)

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		provider.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("Provider.Stop() timed out")
	}
}

func TestUpdateStatusUncountedErrorDoesNotAllocateWhenDebugDisabled(t *testing.T) {
	previousLevel := logrus.GetLevel()
	logrus.SetLevel(logrus.InfoLevel)
	defer logrus.SetLevel(previousLevel)

	provider := &KeyProvider{}
	apiKey := &models.APIKey{ID: 1}
	group := &models.Group{ID: 1}
	allocs := testing.AllocsPerRun(1000, func() {
		provider.UpdateStatus(apiKey, group, false, "RESOURCE HAS BEEN EXHAUSTED")
	})
	require.Zero(t, allocs)
}

func TestResetGroupFailureCountSerializesDeletionWithStoreSync(t *testing.T) {
	db := setupTestDB(t)
	memStore := store.NewMemoryStore()
	barrierStore := &resetLifecycleBarrierStore{
		Store:         memStore,
		existsStarted: make(chan struct{}),
		releaseExists: make(chan struct{}),
		deleteStarted: make(chan struct{}, 1),
	}
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	provider := NewProvider(db, barrierStore, settingsManager, encSvc)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrierStore.releaseExists) }) }
	defer func() {
		release()
		provider.Stop()
		require.NoError(t, memStore.Close())
	}()

	group := createTestGroup(t, db, "reset-delete-race")
	apiKey := createStatusTestKey(t, db, memStore, group.ID, 2)

	resetDone := make(chan error, 1)
	go func() {
		_, resetErr := provider.ResetGroupActiveKeysFailureCount(group.ID)
		resetDone <- resetErr
	}()
	select {
	case <-barrierStore.existsStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("reset did not reach store existence check")
	}

	deleteDone := make(chan error, 1)
	go func() {
		if err := db.Delete(&models.APIKey{}, apiKey.ID).Error; err != nil {
			deleteDone <- err
			return
		}
		deleteDone <- provider.removeKeyFromStore(apiKey.ID, group.ID)
	}()

	select {
	case <-barrierStore.deleteStarted:
		t.Fatal("deletion interleaved with reset store synchronization")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	require.NoError(t, <-resetDone)
	require.NoError(t, <-deleteDone)

	details, err := memStore.HGetAll(fmt.Sprintf("key:%d", apiKey.ID))
	require.NoError(t, err)
	require.Empty(t, details, "deletion must remove the hash after reset synchronization")
}

func TestRemoveKeysWaitsForLifecycleLockBeforeDeletingFromDB(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	lockHeld := false
	defer func() {
		if lockHeld {
			provider.lifecycleMu.RUnlock()
		}
		provider.Stop()
		require.NoError(t, memStore.Close())
	}()

	group := createTestGroup(t, db, "delete-lock-order")
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	apiKey := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-delete-lock",
		KeyHash:  encSvc.Hash("sk-delete-lock"),
		Status:   models.KeyStatusActive,
	}
	require.NoError(t, db.Create(apiKey).Error)

	deleteStarted := make(chan struct{}, 1)
	callbackName := "test:remove-keys-lock-order"
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(_ *gorm.DB) {
		select {
		case deleteStarted <- struct{}{}:
		default:
		}
	}))
	defer db.Callback().Delete().Remove(callbackName)

	provider.lifecycleMu.RLock()
	lockHeld = true
	deleteDone := make(chan error, 1)
	go func() {
		_, err := provider.RemoveKeys(group.ID, []string{"sk-delete-lock"})
		deleteDone <- err
	}()

	select {
	case <-deleteStarted:
		t.Error("deletion SQL must not start while lifecycle lock is held")
	case <-time.After(100 * time.Millisecond):
	}

	provider.lifecycleMu.RUnlock()
	lockHeld = false
	select {
	case err := <-deleteDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RemoveKeys did not finish after releasing lifecycle lock")
	}
}

func TestProviderStopWaitsForInFlightWorker(t *testing.T) {
	db := setupTestDB(t)
	memStore := store.NewMemoryStore()
	blockingStore := &blockingHGetAllStore{
		Store:   memStore,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	provider := NewProvider(db, blockingStore, settingsManager, encSvc)

	var releaseOnce sync.Once
	releaseWorker := func() {
		releaseOnce.Do(func() { close(blockingStore.release) })
	}
	defer func() {
		releaseWorker()
		provider.Stop()
		provider.workerWg.Wait()
		require.NoError(t, memStore.Close())
	}()

	provider.UpdateStatus(&models.APIKey{ID: 1}, &models.Group{ID: 1}, false, "upstream failure")
	select {
	case <-blockingStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("status update worker did not start")
	}

	stopped := make(chan struct{})
	go func() {
		provider.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Provider.Stop returned while a status worker was still using its dependencies")
	case <-time.After(5200 * time.Millisecond):
	}

	releaseWorker()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Provider.Stop did not return after the status worker completed")
	}
}

func TestStatusQueueReleasesSparseHighWaterCapacity(t *testing.T) {
	provider := &KeyProvider{
		statusEntries:   make(map[uint]statusUpdateEntry, 5000),
		statusEntryPeak: 5000,
	}
	for id := uint(1); id <= 5000; id++ {
		provider.statusEntries[id] = statusUpdateEntry{inFlight: true}
	}
	for id := uint(1); id <= 1500; id++ {
		provider.finishStatusUpdate(id)
	}
	assert.Len(t, provider.statusEntries, 3500)
	assert.Equal(t, 5000, provider.statusEntryPeak, "near-threshold churn must not rebuild the map repeatedly")

	for id := uint(1501); id <= 4500; id++ {
		provider.finishStatusUpdate(id)
	}

	assert.Len(t, provider.statusEntries, 500)
	assert.LessOrEqual(t, provider.statusEntryPeak, largeStatusQueueSize)

	provider.statusReadyKeys = make([]uint, 5000, 8192)
	provider.statusReadyHead = 4500
	provider.compactStatusReadyLocked()

	assert.Zero(t, provider.statusReadyHead)
	assert.Len(t, provider.statusReadyKeys, 500)
	assert.LessOrEqual(t, cap(provider.statusReadyKeys), largeStatusQueueSize)
}

func TestUpdateStatusQueueSaturationDoesNotBlockCaller(t *testing.T) {
	db := setupTestDB(t)
	memStore := store.NewMemoryStore()
	workerCount := statusUpdateWorkerCount(db)
	blockingStore := &blockingHGetAllStore{
		Store:   memStore,
		started: make(chan struct{}, workerCount),
		release: make(chan struct{}),
	}
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	provider := NewProvider(db, blockingStore, settingsManager, encSvc)

	var releaseOnce sync.Once
	releaseWorkers := func() {
		releaseOnce.Do(func() { close(blockingStore.release) })
	}
	defer func() {
		releaseWorkers()
		provider.Stop()
		require.NoError(t, memStore.Close())
	}()

	group := &models.Group{ID: 1}
	update := func(id uint) {
		provider.UpdateStatus(&models.APIKey{ID: id}, group, false, "upstream failure")
	}

	for i := 0; i < workerCount; i++ {
		update(uint(i + 1))
	}
	for i := 0; i < workerCount; i++ {
		select {
		case <-blockingStore.started:
		case <-time.After(2 * time.Second):
			t.Fatal("status update worker did not reach the blocking store")
		}
	}

	const queueCapacity = 1000
	for i := 0; i < queueCapacity; i++ {
		update(uint(workerCount + i + 1))
	}

	returned := make(chan struct{})
	go func() {
		update(uint(workerCount + queueCapacity + 1))
		close(returned)
	}()

	blocked := false
	select {
	case <-returned:
	case <-time.After(250 * time.Millisecond):
		blocked = true
	}

	releaseWorkers()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateStatus did not return after releasing workers")
	}
	assert.False(t, blocked, "UpdateStatus must not run slow Store/DB work in the request goroutine")
}

func TestUpdateStatusCoalescesFailuresAtBlacklistThreshold(t *testing.T) {
	provider, db, keyStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "coalesced-failures")
	group.Config = map[string]any{"blacklist_threshold": 3}
	apiKey := createStatusTestKey(t, db, keyStore, group.ID, 0)

	for i := 0; i < 100; i++ {
		provider.UpdateStatus(apiKey, group, false, "upstream failure")
	}

	require.Eventually(t, func() bool {
		var persisted models.APIKey
		if err := db.First(&persisted, apiKey.ID).Error; err != nil {
			return false
		}
		details, err := keyStore.HGetAll(fmt.Sprintf("key:%d", apiKey.ID))
		if err != nil {
			return false
		}
		length, err := keyStore.LLen(fmt.Sprintf("group:%d:active_keys", group.ID))
		return err == nil && persisted.FailureCount == 3 && persisted.Status == models.KeyStatusInvalid &&
			details["failure_count"] == "3" && details["status"] == models.KeyStatusInvalid && length == 0
	}, 5*time.Second, 10*time.Millisecond)
}

func TestUpdateStatusAtBlacklistThresholdConvergesWithoutIncrement(t *testing.T) {
	provider, db, keyStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "threshold-convergence")
	group.Config = map[string]any{"blacklist_threshold": 3}
	apiKey := createStatusTestKey(t, db, keyStore, group.ID, 3)

	provider.UpdateStatus(apiKey, group, false, "upstream failure")

	require.Eventually(t, func() bool {
		var persisted models.APIKey
		if err := db.First(&persisted, apiKey.ID).Error; err != nil {
			return false
		}
		details, err := keyStore.HGetAll(fmt.Sprintf("key:%d", apiKey.ID))
		if err != nil {
			return false
		}
		length, err := keyStore.LLen(fmt.Sprintf("group:%d:active_keys", group.ID))
		return err == nil && persisted.FailureCount == 3 && persisted.Status == models.KeyStatusInvalid &&
			details["failure_count"] == "3" && details["status"] == models.KeyStatusInvalid && length == 0
	}, 5*time.Second, 10*time.Millisecond)
}

func TestUpdateStatusSuccessResetsEarlierFailuresBeforeLaterFailures(t *testing.T) {
	db := setupTestDB(t)
	memStore := store.NewMemoryStore()
	blockingStore := &blockingHGetAllStore{
		Store:   memStore,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	settingsManager := config.NewSystemSettingsManager()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	require.NoError(t, err)
	provider := NewProvider(db, blockingStore, settingsManager, encSvc)
	defer func() {
		provider.Stop()
		require.NoError(t, memStore.Close())
	}()

	group := createTestGroup(t, db, "success-boundary")
	group.Config = map[string]any{"blacklist_threshold": 100}
	apiKey := createStatusTestKey(t, db, memStore, group.ID, 0)

	provider.UpdateStatus(apiKey, group, false, "first failure")
	select {
	case <-blockingStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("status update worker did not reach the blocking store")
	}

	provider.UpdateStatus(apiKey, group, true, "")
	provider.UpdateStatus(apiKey, group, false, "failure after success")
	provider.UpdateStatus(apiKey, group, false, "second failure after success")
	close(blockingStore.release)

	require.Eventually(t, func() bool {
		var persisted models.APIKey
		if err := db.First(&persisted, apiKey.ID).Error; err != nil || persisted.FailureCount != 2 {
			return false
		}
		details, err := memStore.HGetAll(fmt.Sprintf("key:%d", apiKey.ID))
		return err == nil && details["failure_count"] == "2" && details["status"] == models.KeyStatusActive
	}, 5*time.Second, 10*time.Millisecond)
}

func TestUpdateStatusConcurrentFailuresDoNotLoseUpdates(t *testing.T) {
	provider, db, keyStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "concurrent-failures")
	group.Config = map[string]any{"blacklist_threshold": 10_000}
	apiKey := createStatusTestKey(t, db, keyStore, group.ID, 0)

	const (
		goroutines       = 32
		updatesPerWorker = 200
		totalUpdates     = int64(goroutines * updatesPerWorker)
	)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			<-start
			for range updatesPerWorker {
				provider.UpdateStatus(apiKey, group, false, "upstream failure")
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Eventually(t, func() bool {
		var persisted models.APIKey
		if err := db.First(&persisted, apiKey.ID).Error; err != nil || persisted.FailureCount != totalUpdates {
			return false
		}
		details, err := keyStore.HGetAll(fmt.Sprintf("key:%d", apiKey.ID))
		if err != nil || details["failure_count"] != strconv.FormatInt(totalUpdates, 10) {
			return false
		}
		provider.statusMu.Lock()
		defer provider.statusMu.Unlock()
		return len(provider.statusEntries) == 0 && len(provider.statusReadyKeys) == 0
	}, 10*time.Second, 10*time.Millisecond)
}

func TestStatusQueueClampsFailureCount(t *testing.T) {
	provider := &KeyProvider{
		statusEntries: map[uint]statusUpdateEntry{
			1: {
				batch: statusUpdateBatch{
					keyID:        1,
					group:        &models.Group{ID: 1},
					failureCount: math.MaxInt64,
				},
				inFlight: true,
			},
		},
	}

	provider.UpdateStatus(&models.APIKey{ID: 1}, &models.Group{ID: 1}, false, "upstream failure")

	require.Equal(t, int64(math.MaxInt64), provider.statusEntries[1].batch.failureCount)
}

func TestProcessStatusUpdateIgnoresNilGroup(t *testing.T) {
	provider := &KeyProvider{}
	provider.processStatusUpdate(statusUpdateBatch{keyID: 1, failureCount: 1})
}

func TestSelectKey_NoKeys(t *testing.T) {
	provider, _, _ := setupTestProvider(t)
	defer provider.Stop()

	_, err := provider.SelectKey(1)
	assert.Error(t, err)
}

func TestSelectKey_Success(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Create test key
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-test123")
	require.NoError(t, err)

	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-test123"),
		Status:       models.KeyStatusActive,
		FailureCount: 0,
	}
	require.NoError(t, db.Create(apiKey).Error)

	// Add key to store
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	require.NoError(t, memStore.LPush(activeKeysListKey, apiKey.ID))

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	keyDetails := map[string]any{
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    time.Now().Unix(),
	}
	require.NoError(t, memStore.HSet(keyHashKey, keyDetails))

	// Select key
	selectedKey, err := provider.SelectKey(group.ID)
	require.NoError(t, err)
	assert.NotNil(t, selectedKey)
	assert.Equal(t, "sk-test123", selectedKey.KeyValue) // Should be decrypted
	assert.Equal(t, models.KeyStatusActive, selectedKey.Status)
}

func TestUpdateStatus_Success(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	// Create test group with unique name to avoid conflicts in shared cache mode
	group := &models.Group{
		Name:        fmt.Sprintf("test-group-%d", time.Now().UnixNano()),
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	require.NoError(t, db.Create(group).Error)

	// Create test key
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-test123")
	require.NoError(t, err)

	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-test123"),
		Status:       models.KeyStatusActive,
		FailureCount: 1,
	}
	require.NoError(t, db.Create(apiKey).Error)

	// Setup store with correct key ID
	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	keyDetails := map[string]any{
		"id":            fmt.Sprintf("%d", apiKey.ID),
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "1",
		"created_at":    time.Now().Unix(),
	}
	require.NoError(t, memStore.HSet(keyHashKey, keyDetails))
	require.NoError(t, memStore.LPush(activeKeysListKey, apiKey.ID))

	// Update status to success
	provider.UpdateStatus(apiKey, group, true, "")

	// Wait for async processing with polling
	require.Eventually(t, func() bool {
		var updatedKey models.APIKey
		if err := db.First(&updatedKey, apiKey.ID).Error; err != nil {
			return false
		}
		return updatedKey.FailureCount == 0
	}, 5*time.Second, 10*time.Millisecond, "failure count should be reset")
}

func TestAddKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Prepare keys to add
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	keys := []models.APIKey{
		{
			GroupID:  group.ID,
			KeyValue: "sk-test1",
			KeyHash:  encSvc.Hash("sk-test1"),
			Status:   models.KeyStatusActive,
		},
		{
			GroupID:  group.ID,
			KeyValue: "sk-test2",
			KeyHash:  encSvc.Hash("sk-test2"),
			Status:   models.KeyStatusActive,
		},
	}

	// Add keys
	err := provider.AddKeys(group.ID, keys)
	require.NoError(t, err)

	// Verify keys were added
	var count int64
	db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count)
	assert.Equal(t, int64(2), count)
}

func TestRemoveKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Create test keys
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	key1 := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-test1",
		KeyHash:  encSvc.Hash("sk-test1"),
		Status:   models.KeyStatusActive,
	}
	require.NoError(t, db.Create(key1).Error)

	// Remove keys
	deletedCount, err := provider.RemoveKeys(group.ID, []string{"sk-test1"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), deletedCount)

	// Verify key was removed
	var count int64
	db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestRestoreKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Create invalid key
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	key := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     "sk-test1",
		KeyHash:      encSvc.Hash("sk-test1"),
		Status:       models.KeyStatusInvalid,
		FailureCount: 5,
	}
	require.NoError(t, db.Create(key).Error)

	// Restore keys
	restoredCount, err := provider.RestoreKeys(group.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), restoredCount)

	// Verify key was restored
	var updatedKey models.APIKey
	require.NoError(t, db.First(&updatedKey, key.ID).Error)
	assert.Equal(t, models.KeyStatusActive, updatedKey.Status)
	assert.Equal(t, int64(0), updatedKey.FailureCount)

	// Note: Active list verification skipped as LRange is not part of Store interface
	// The key restoration is verified through database status check above
}

func TestRestoreKeysReleasesLifecycleLockBeforeCallback(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()
	defer memStore.Close()

	group := createTestGroup(t, db, "restore-callback-lock")
	key := &models.APIKey{
		GroupID: group.ID, KeyValue: "restore-callback-key", KeyHash: provider.encryptionSvc.Hash("restore-callback-key"),
		Status: models.KeyStatusInvalid, FailureCount: 2,
	}
	require.NoError(t, db.Create(key).Error)

	callbackDone := make(chan error, 1)
	provider.CacheInvalidationCallback = func(groupID uint) {
		_, err := provider.RemoveKeys(groupID, []string{"restore-callback-key"})
		callbackDone <- err
	}

	resultDone := make(chan error, 1)
	go func() {
		_, err := provider.RestoreKeys(group.ID)
		resultDone <- err
	}()

	select {
	case err := <-resultDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RestoreKeys did not release lifecycle lock before invoking callback")
	}
	select {
	case err := <-callbackDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("restore callback did not complete")
	}
}

func TestRestoreKeysWaitsForLifecycleWriteLock(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()
	defer memStore.Close()

	group := createTestGroup(t, db, "restore-write-lock")
	key := &models.APIKey{
		GroupID: group.ID, KeyValue: "restore-write-lock-key",
		KeyHash: provider.encryptionSvc.Hash("restore-write-lock-key"),
		Status:  models.KeyStatusInvalid, FailureCount: 1,
	}
	require.NoError(t, db.Create(key).Error)

	provider.lifecycleMu.RLock()
	restoreDone := make(chan error, 1)
	go func() {
		_, err := provider.RestoreKeys(group.ID)
		restoreDone <- err
	}()

	select {
	case err := <-restoreDone:
		t.Fatalf("RestoreKeys must wait for an active lifecycle reader, got %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	provider.lifecycleMu.RUnlock()

	select {
	case err := <-restoreDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RestoreKeys did not finish after releasing lifecycle reader")
	}
}

func TestRemoveAllKeys(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Create multiple keys
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	for i := 0; i < 10; i++ {
		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: "sk-test",
			KeyHash:  encSvc.Hash("sk-test"),
			Status:   models.KeyStatusActive,
		}
		require.NoError(t, db.Create(key).Error)
	}

	// Remove all keys
	ctx := context.Background()
	deletedCount, err := provider.RemoveAllKeys(ctx, group.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(10), deletedCount)

	// Verify all keys were removed
	var count int64
	db.Model(&models.APIKey{}).Where("group_id = ?", group.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestRemoveAllKeysPreservesKeyAddedFromProgressCallback(t *testing.T) {
	provider, db, keyStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "remove-all-concurrent-add")
	existing := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-existing",
		KeyHash:  provider.encryptionSvc.Hash("sk-existing"),
		Status:   models.KeyStatusActive,
	}
	require.NoError(t, db.Create(existing).Error)
	require.NoError(t, provider.addKeyToStore(existing))

	added := []models.APIKey{{
		GroupID:  group.ID,
		KeyValue: "sk-added-during-progress",
		KeyHash:  provider.encryptionSvc.Hash("sk-added-during-progress"),
		Status:   models.KeyStatusActive,
	}}
	var addErr error
	progressCalls := 0

	deleted, err := provider.RemoveAllKeys(context.Background(), group.ID, func(int64) {
		progressCalls++
		addErr = provider.AddKeys(group.ID, added)
	})
	require.NoError(t, err)
	require.NoError(t, addErr)
	require.Equal(t, int64(1), deleted)
	require.Equal(t, 1, progressCalls)

	var persisted models.APIKey
	require.NoError(t, db.First(&persisted, added[0].ID).Error)
	details, err := keyStore.HGetAll(fmt.Sprintf("key:%d", added[0].ID))
	require.NoError(t, err)
	require.NotEmpty(t, details)
	length, err := keyStore.LLen(fmt.Sprintf("group:%d:active_keys", group.ID))
	require.NoError(t, err)
	require.Equal(t, int64(1), length, "a key added after the final delete batch must remain selectable")
}

func TestLoadKeysFromDB(t *testing.T) {
	provider, db, _ := setupTestProvider(t)
	defer provider.Stop()

	// Create test group
	group := createTestGroup(t, db, "test-group")

	// Create test keys
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	for i := 0; i < 5; i++ {
		key := &models.APIKey{
			GroupID:  group.ID,
			KeyValue: "sk-test",
			KeyHash:  encSvc.Hash("sk-test"),
			Status:   models.KeyStatusActive,
		}
		require.NoError(t, db.Create(key).Error)
	}

	// Load keys from DB
	err := provider.LoadKeysFromDB()
	require.NoError(t, err)

	// Verify keys were loaded by checking database
	var loadedKeys []models.APIKey
	require.NoError(t, db.Where("group_id = ? AND status = ?", group.ID, models.KeyStatusActive).Find(&loadedKeys).Error)
	assert.Equal(t, 5, len(loadedKeys))
}

func TestLoadKeysFromDBPreservesKeyHash(t *testing.T) {
	provider, db, keyStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "load-key-hash")
	keyHash := "load-key-hash-value"
	key := &models.APIKey{
		GroupID: group.ID, KeyValue: "stored-key", KeyHash: keyHash,
		Status: models.KeyStatusActive,
	}
	require.NoError(t, db.Create(key).Error)

	require.NoError(t, provider.LoadKeysFromDB())
	stored, err := keyStore.HGetAll(fmt.Sprintf("key:%d", key.ID))
	require.NoError(t, err)
	require.Equal(t, keyHash, stored["key_hash"])
}

func TestLoadKeysFromDBPreservesConcurrentAdd(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	provider.Stop()
	defer memStore.Close()

	group := createTestGroup(t, db, "load-concurrent-add")
	existing := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: "sk-existing",
		KeyHash:  provider.encryptionSvc.Hash("sk-existing"),
		Status:   models.KeyStatusActive,
	}
	require.NoError(t, db.Create(existing).Error)

	barrierStore := &keyedHSetBarrierStore{
		Store:    memStore,
		blockKey: fmt.Sprintf("key:%d", existing.ID),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	provider.store = barrierStore
	var releaseOnce sync.Once
	releaseLoad := func() { releaseOnce.Do(func() { close(barrierStore.release) }) }
	defer releaseLoad()

	loadDone := make(chan error, 1)
	go func() { loadDone <- provider.LoadKeysFromDB() }()
	select {
	case <-barrierStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("LoadKeysFromDB did not reach the cache write")
	}

	added := []models.APIKey{{
		GroupID:  group.ID,
		KeyValue: "sk-added-during-load",
		KeyHash:  provider.encryptionSvc.Hash("sk-added-during-load"),
		Status:   models.KeyStatusActive,
	}}
	addDone := make(chan error, 1)
	go func() { addDone <- provider.AddKeys(group.ID, added) }()

	select {
	case err := <-addDone:
		t.Fatalf("AddKeys interleaved with the active-list snapshot rebuild: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	releaseLoad()
	require.NoError(t, <-loadDone)
	require.NoError(t, <-addDone)
	length, err := memStore.LLen(fmt.Sprintf("group:%d:active_keys", group.ID))
	require.NoError(t, err)
	require.Equal(t, int64(2), length)
	details, err := memStore.HGetAll(fmt.Sprintf("key:%d", added[0].ID))
	require.NoError(t, err)
	require.NotEmpty(t, details)
}

func TestLoadGroupKeysToStorePreservesKeyHash(t *testing.T) {
	provider, db, keyStore := setupTestProvider(t)
	defer provider.Stop()
	group := createTestGroup(t, db, "load-group-key-hash")
	key := &models.APIKey{
		GroupID: group.ID, KeyValue: "stored-key", KeyHash: "stored-key-hash", Status: models.KeyStatusActive,
	}
	require.NoError(t, db.Create(key).Error)

	require.NoError(t, provider.LoadGroupKeysToStore(group.ID))
	stored, err := keyStore.HGetAll(fmt.Sprintf("key:%d", key.ID))
	require.NoError(t, err)
	require.Equal(t, key.KeyHash, stored["key_hash"])
}

func TestLoadGroupKeysToStoreDoesNotOverwriteConcurrentAdd(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	provider.Stop()
	defer memStore.Close()

	group := createTestGroup(t, db, "load-group-concurrent-add")
	existing := &models.APIKey{
		GroupID: group.ID, KeyValue: "existing", KeyHash: provider.encryptionSvc.Hash("existing"), Status: models.KeyStatusActive,
	}
	require.NoError(t, db.Create(existing).Error)

	listKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	barrierStore := &loadListDeleteBarrierStore{
		Store:         memStore,
		listKey:       listKey,
		deleteStarted: make(chan struct{}),
		releaseDelete: make(chan struct{}),
	}
	provider.store = barrierStore

	loadDone := make(chan error, 1)
	go func() { loadDone <- provider.LoadGroupKeysToStore(group.ID) }()
	select {
	case <-barrierStore.deleteStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("LoadGroupKeysToStore did not start rebuilding the active list")
	}

	added := []models.APIKey{{
		GroupID: group.ID, KeyValue: "added-during-load", KeyHash: provider.encryptionSvc.Hash("added-during-load"), Status: models.KeyStatusActive,
	}}
	addDone := make(chan error, 1)
	go func() { addDone <- provider.AddKeys(group.ID, added) }()

	select {
	case err := <-addDone:
		t.Fatalf("AddKeys completed while LoadGroupKeysToStore held the lifecycle lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(barrierStore.releaseDelete)
	require.NoError(t, <-loadDone)
	require.NoError(t, <-addDone)

	length, err := memStore.LLen(listKey)
	require.NoError(t, err)
	require.Equal(t, int64(2), length, "a concurrent key must survive the list rebuild")
	details, err := memStore.HGetAll(fmt.Sprintf("key:%d", added[0].ID))
	require.NoError(t, err)
	require.NotEmpty(t, details)
}

// setupBenchProvider creates a test KeyProvider for benchmarks
func setupBenchProvider(b *testing.B) (*KeyProvider, *gorm.DB, store.Store) {
	b.Helper()
	// Use shared cache mode to allow background workers to access the same in-memory database
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatalf("failed to connect to test database: %v", err)
	}

	if err := db.AutoMigrate(&models.APIKey{}, &models.Group{}); err != nil {
		b.Fatalf("failed to migrate test database: %v", err)
	}

	// Limit connections to prevent separate in-memory databases
	// KeyProvider spawns background workers that need to share the same database
	sqlDB, err := db.DB()
	if err != nil {
		b.Fatalf("failed to get sql DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	memStore := store.NewMemoryStore()
	encSvc, err := encryption.NewService("test-key-32-bytes-long-enough!!")
	if err != nil {
		b.Fatalf("failed to create encryption service: %v", err)
	}
	settingsManager := config.NewSystemSettingsManager()

	provider := NewProvider(db, memStore, settingsManager, encSvc)
	return provider, db, memStore
}

// Benchmark tests for PGO optimization
func BenchmarkSelectKey(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	defer provider.Stop()

	// Setup test data with unique name to avoid conflicts in shared cache
	group := &models.Group{
		Name:        fmt.Sprintf("bench-group-%d", time.Now().UnixNano()),
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatalf("failed to create test group: %v", err)
	}

	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, _ := encSvc.Encrypt("sk-bench")
	apiKey := &models.APIKey{
		GroupID:  group.ID,
		KeyValue: encryptedKey,
		KeyHash:  encSvc.Hash("sk-bench"),
		Status:   models.KeyStatusActive,
	}
	if err := db.Create(apiKey).Error; err != nil {
		b.Fatalf("failed to create test key: %v", err)
	}

	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	if err := memStore.LPush(activeKeysListKey, apiKey.ID); err != nil {
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

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = provider.SelectKey(group.ID)
	}
}

func BenchmarkUpdateStatus(b *testing.B) {
	provider, db, memStore := setupBenchProvider(b)
	blockingStore := &blockingHGetAllStore{
		Store:   memStore,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	provider.store = blockingStore
	defer func() {
		close(blockingStore.release)
		provider.Stop()
	}()

	// Setup test data with unique name to avoid conflicts in shared cache
	group := &models.Group{
		Name:        fmt.Sprintf("bench-update-%d", time.Now().UnixNano()),
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatalf("failed to create test group: %v", err)
	}

	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, _ := encSvc.Encrypt("sk-bench")
	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-bench"),
		Status:       models.KeyStatusActive,
		FailureCount: 0,
	}
	if err := db.Create(apiKey).Error; err != nil {
		b.Fatalf("failed to create test key: %v", err)
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

	provider.UpdateStatus(apiKey, group, true, "")
	select {
	case <-blockingStore.started:
	case <-time.After(2 * time.Second):
		b.Fatal("status update worker did not reach the blocking store")
	}

	b.Run("success", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			provider.UpdateStatus(apiKey, group, true, "")
		}
	})
	b.Run("failure", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			provider.UpdateStatus(apiKey, group, false, "upstream failure")
		}
	})
	b.Run("parallel_failure", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				provider.UpdateStatus(apiKey, group, false, "upstream failure")
			}
		})
	})
	b.Run("uncounted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			provider.UpdateStatus(apiKey, group, false, "resource has been exhausted")
		}
	})
}

func BenchmarkAddKeys(b *testing.B) {
	provider, db, _ := setupBenchProvider(b)
	defer provider.Stop()

	// Use unique name to avoid conflicts in shared cache
	group := &models.Group{
		Name:        fmt.Sprintf("bench-addkeys-%d", time.Now().UnixNano()),
		ChannelType: "openai",
		Enabled:     true,
		Upstreams:   []byte(`[{"url":"https://api.openai.com","weight":100}]`),
	}
	if err := db.Create(group).Error; err != nil {
		b.Fatalf("failed to create test group: %v", err)
	}

	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		keys := []models.APIKey{
			{
				GroupID:  group.ID,
				KeyValue: "sk-bench",
				KeyHash:  encSvc.Hash("sk-bench"),
				Status:   models.KeyStatusActive,
			},
		}
		_ = provider.AddKeys(group.ID, keys)
	}
}
