package keypool

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// recoveryBarrierStore pauses the recovery LRem after the existence check has
// completed, making the deletion/recovery ordering deterministic.
type recoveryBarrierStore struct {
	store.Store
	startLRem chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (s *recoveryBarrierStore) LRem(key string, count int64, value any) error {
	s.once.Do(func() {
		close(s.startLRem)
		<-s.release
	})
	return s.Store.LRem(key, count, value)
}

type failureWriteBarrierStore struct {
	store.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *failureWriteBarrierStore) HIncrBy(key, field string, incr int64) (int64, error) {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.Store.HIncrBy(key, field, incr)
}

type cacheWriteBarrierStore struct {
	store.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *cacheWriteBarrierStore) HSet(key string, values map[string]any) error {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.Store.HSet(key, values)
}

// TestHandleFailure_DeletedKeyDoesNotResurrectStoreHash locks the guard against
// resurrecting a deleted key's store hash. A failure task may still be queued
// while the key is removed from the DB and its store hash is deleted; the
// failure handler must bail out early instead of recreating the orphan hash.
func TestHandleFailure_DeletedKeyDoesNotResurrectStoreHash(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "test-group")
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-deleted-key")
	require.NoError(t, err)
	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-deleted-key"),
		Status:       models.KeyStatusActive,
		FailureCount: 0,
	}
	require.NoError(t, db.Create(apiKey).Error)

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	keyDetails := map[string]any{
		"id":            fmt.Sprintf("%d", apiKey.ID),
		"key_string":    encryptedKey,
		"status":        models.KeyStatusActive,
		"failure_count": "0",
		"created_at":    time.Now().Unix(),
	}
	require.NoError(t, memStore.HSet(keyHashKey, keyDetails))
	require.NoError(t, memStore.LPush(activeKeysListKey, apiKey.ID))

	// Simulate the key being deleted while a failure task is still in flight:
	// both the DB row and the store hash are gone.
	require.NoError(t, db.Delete(&models.APIKey{}, apiKey.ID).Error)
	require.NoError(t, memStore.Delete(keyHashKey))

	// Call the failure handler synchronously: whether invoked via the worker
	// pool or inline, it must not resurrect the deleted key's store hash.
	require.NoError(t, provider.handleFailure(apiKey, group, keyHashKey, activeKeysListKey))

	details, err := memStore.HGetAll(keyHashKey)
	require.NoError(t, err)
	require.Empty(t, details, "deleted key hash must not be resurrected in store")
	length, err := memStore.LLen(activeKeysListKey)
	require.NoError(t, err)
	require.Zero(t, length, "deleted key ID must be removed from the active list")
}

// TestHandleFailure_DeletedKeyCleansUpResidualStoreHash locks the fallback
// cleanup: when the DB row is already gone but the store hash still exists
// (deletion race), the failure handler must remove the residual hash instead
// of writing to it.
func TestHandleFailure_DeletedKeyCleansUpResidualStoreHash(t *testing.T) {
	for _, failureCount := range []int64{0, 3} {
		t.Run(fmt.Sprintf("failure_count_%d", failureCount), func(t *testing.T) {
			provider, db, memStore := setupTestProvider(t)
			defer provider.Stop()

			group := createTestGroup(t, db, "test-group")
			group.Config = map[string]any{"blacklist_threshold": 3}
			encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
			encryptedKey, err := encSvc.Encrypt("sk-deleted-key")
			require.NoError(t, err)
			apiKey := &models.APIKey{
				GroupID:      group.ID,
				KeyValue:     encryptedKey,
				KeyHash:      encSvc.Hash("sk-deleted-key"),
				Status:       models.KeyStatusActive,
				FailureCount: failureCount,
			}
			require.NoError(t, db.Create(apiKey).Error)

			keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
			activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
			keyDetails := map[string]any{
				"id":            fmt.Sprintf("%d", apiKey.ID),
				"key_string":    encryptedKey,
				"status":        models.KeyStatusActive,
				"failure_count": failureCount,
				"created_at":    time.Now().Unix(),
			}
			require.NoError(t, memStore.HSet(keyHashKey, keyDetails))
			require.NoError(t, memStore.LPush(activeKeysListKey, apiKey.ID))

			// Simulate the DB row being deleted while the store hash is still present.
			require.NoError(t, db.Delete(&models.APIKey{}, apiKey.ID).Error)

			require.NoError(t, provider.handleFailure(apiKey, group, keyHashKey, activeKeysListKey))

			details, err := memStore.HGetAll(keyHashKey)
			require.NoError(t, err)
			require.Empty(t, details, "residual store hash of a deleted key must be cleaned up")
			length, err := memStore.LLen(activeKeysListKey)
			require.NoError(t, err)
			require.Zero(t, length, "deleted key ID must be removed from the active list")
		})
	}
}

func TestHandleFailure_DeletionDuringStoreWriteDoesNotRecreateHash(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	provider.Stop()
	defer func() { require.NoError(t, memStore.Close()) }()

	group := createTestGroup(t, db, "test-group")
	apiKey := createStatusTestKey(t, db, memStore, group.ID, 0)
	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)

	barrierStore := &failureWriteBarrierStore{
		Store:   memStore,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseStore := func() {
		releaseOnce.Do(func() { close(barrierStore.release) })
	}
	defer releaseStore()
	provider.store = barrierStore

	done := make(chan error, 1)
	go func() {
		done <- provider.handleFailure(apiKey, group, keyHashKey, activeKeysListKey)
	}()

	select {
	case <-barrierStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("failure handling did not reach the store write")
	}

	require.NoError(t, db.Delete(&models.APIKey{}, apiKey.ID).Error)
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- provider.removeKeyFromStore(apiKey.ID, group.ID)
	}()
	releaseStore()
	require.NoError(t, <-done)
	require.NoError(t, <-deleteDone)

	details, err := memStore.HGetAll(keyHashKey)
	require.NoError(t, err)
	require.Empty(t, details, "failure write must not recreate a deleted key hash")
}

func TestLoadGroupKeysToStore_DeletionDuringCacheWriteDoesNotResurrectKey(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	provider.Stop()
	defer func() { require.NoError(t, memStore.Close()) }()

	group := createTestGroup(t, db, "load-delete-race")
	apiKey := createStatusTestKey(t, db, memStore, group.ID, 0)
	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)

	barrierStore := &cacheWriteBarrierStore{
		Store:   memStore,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	provider.store = barrierStore

	loadDone := make(chan error, 1)
	go func() {
		loadDone <- provider.LoadGroupKeysToStore(group.ID)
	}()

	select {
	case <-barrierStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("group cache load did not reach the first cache write")
	}

	require.NoError(t, db.Delete(&models.APIKey{}, apiKey.ID).Error)
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- provider.removeKeyFromStore(apiKey.ID, group.ID)
	}()

	select {
	case err := <-deleteDone:
		t.Fatalf("deletion interleaved with group cache load: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(barrierStore.release)
	require.NoError(t, <-loadDone)
	require.NoError(t, <-deleteDone)

	details, err := memStore.HGetAll(keyHashKey)
	require.NoError(t, err)
	require.Empty(t, details, "deleted key hash must not be resurrected by group cache loading")
	length, err := memStore.LLen(activeKeysListKey)
	require.NoError(t, err)
	require.Zero(t, length, "deleted key ID must not be restored to the active list")
}

// TestHandleSuccess_DeletedKeyDoesNotResurrectStoreHash locks the same guard
// for the success path: a queued success task for a deleted key must not
// recreate the orphan store hash via HSet.
func TestHandleSuccess_DeletedKeyDoesNotResurrectStoreHash(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "test-group")
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-deleted-key")
	require.NoError(t, err)
	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-deleted-key"),
		Status:       models.KeyStatusActive,
		FailureCount: 1,
	}
	require.NoError(t, db.Create(apiKey).Error)

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

	// Simulate the key being deleted while a success task is still in flight.
	require.NoError(t, db.Delete(&models.APIKey{}, apiKey.ID).Error)
	require.NoError(t, memStore.Delete(keyHashKey))

	require.NoError(t, provider.handleSuccess(apiKey.ID, keyHashKey, activeKeysListKey, group.ID))

	details, err := memStore.HGetAll(keyHashKey)
	require.NoError(t, err)
	require.Empty(t, details, "deleted key hash must not be resurrected by a success update")
	length, err := memStore.LLen(activeKeysListKey)
	require.NoError(t, err)
	require.Zero(t, length, "deleted key ID must be removed from the active list")
}

func TestHandleSuccess_MySQLUnchangedRowDoesNotDeleteExistingKey(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { require.NoError(t, sqlDB.Close()) }()

	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)

	memStore := store.NewMemoryStore()
	defer func() { require.NoError(t, memStore.Close()) }()
	provider := &KeyProvider{
		db:              db,
		store:           memStore,
		settingsManager: config.NewSystemSettingsManager(),
	}

	const (
		keyID             = uint(7)
		groupID           = uint(11)
		keyHashKey        = "key:7"
		activeKeysListKey = "group:11:active_keys"
	)
	require.NoError(t, memStore.HSet(keyHashKey, map[string]any{
		"id":            keyID,
		"status":        models.KeyStatusInvalid,
		"failure_count": 0,
	}))
	require.NoError(t, memStore.LPush(activeKeysListKey, keyID))

	mock.ExpectExec("UPDATE `api_keys`").
		WithArgs(int64(0), models.KeyStatusActive, keyID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `api_keys`").
		WithArgs(keyID).
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))

	err = provider.handleSuccess(keyID, keyHashKey, activeKeysListKey, groupID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	// Queue the deferred Close as the final expectation so its error is asserted.
	mock.ExpectClose()

	details, err := memStore.HGetAll(keyHashKey)
	require.NoError(t, err)
	require.Equal(t, models.KeyStatusActive, details["status"])
	length, err := memStore.LLen(activeKeysListKey)
	require.NoError(t, err)
	require.Equal(t, int64(1), length)
}

// TestResetGroupActiveKeysFailureCountSkipsMissingStoreHash locks the reset
// behavior: resetting failure_count must not create an id-less orphan hash
// when key:<id> is absent from the store (such a hash would make
// handleSuccess/handleFailure bail out early and linger as an orphan).
func TestResetGroupActiveKeysFailureCountSkipsMissingStoreHash(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "test-group")
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-reset-key")
	require.NoError(t, err)
	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-reset-key"),
		Status:       models.KeyStatusActive,
		FailureCount: 3,
	}
	require.NoError(t, db.Create(apiKey).Error)

	// Deliberately do NOT seed the store hash: the reset must skip instead of
	// creating a hash that lacks the "id" field.
	reset, err := provider.ResetGroupActiveKeysFailureCount(group.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), reset)

	details, err := memStore.HGetAll(fmt.Sprintf("key:%d", apiKey.ID))
	require.NoError(t, err)
	require.Empty(t, details, "reset must not create an id-less orphan hash")
}

func TestResetStoreFailureCountLockedCanRunUnderLifecycleReadLock(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer func() { require.NoError(t, memStore.Close()) }()
	defer provider.Stop()

	group := createTestGroup(t, db, "reset-locked-helper")
	key := createStatusTestKey(t, db, memStore, group.ID, 3)

	provider.lifecycleMu.RLock()
	updated, err := provider.resetStoreFailureCountLocked(key.ID)
	provider.lifecycleMu.RUnlock()

	require.NoError(t, err)
	require.True(t, updated)
	details, err := memStore.HGetAll(fmt.Sprintf("key:%d", key.ID))
	require.NoError(t, err)
	require.Equal(t, "0", details["failure_count"])
}

func TestResetGroupFailureCountReleasesLifecycleLockBeforeCallback(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer func() { require.NoError(t, memStore.Close()) }()
	defer provider.Stop()

	group := createTestGroup(t, db, "reset-callback-lock")
	createStatusTestKey(t, db, memStore, group.ID, 2)

	callbackDone := make(chan error, 1)
	var callbackOnce sync.Once
	provider.CacheInvalidationCallback = func(groupID uint) {
		callbackOnce.Do(func() { callbackDone <- provider.ClearAllKeys() })
	}

	resetDone := make(chan error, 1)
	go func() {
		_, err := provider.ResetGroupActiveKeysFailureCount(group.ID)
		resetDone <- err
	}()

	select {
	case err := <-resetDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ResetGroupActiveKeysFailureCount did not release lifecycle lock before callback")
	}
	select {
	case err := <-callbackDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("reset callback did not complete")
	}
}

func TestResetAllFailureCountReleasesLifecycleLockBeforeCallback(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer func() { require.NoError(t, memStore.Close()) }()
	defer provider.Stop()

	group := createTestGroup(t, db, "reset-all-callback-lock")
	createStatusTestKey(t, db, memStore, group.ID, 2)

	callbackDone := make(chan error, 1)
	var callbackOnce sync.Once
	provider.CacheInvalidationCallback = func(groupID uint) {
		callbackOnce.Do(func() { callbackDone <- provider.ClearAllKeys() })
	}

	resetDone := make(chan error, 1)
	go func() {
		_, err := provider.ResetAllActiveKeysFailureCount()
		resetDone <- err
	}()

	select {
	case err := <-resetDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ResetAllActiveKeysFailureCount did not release lifecycle lock before callback")
	}
	select {
	case err := <-callbackDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("reset-all callback did not complete")
	}
}

// TestHandleSuccess_RecoversInvalidKeyToActiveList locks the normal recovery
// path: a key leaving the invalid state is re-added to the active list and its
// store hash is refreshed with the active status.
func TestHandleSuccess_RecoversInvalidKeyToActiveList(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "test-group")
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-recover-key")
	require.NoError(t, err)
	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-recover-key"),
		Status:       models.KeyStatusInvalid,
		FailureCount: 2,
	}
	require.NoError(t, db.Create(apiKey).Error)

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	keyDetails := map[string]any{
		"id":            fmt.Sprintf("%d", apiKey.ID),
		"key_string":    encryptedKey,
		"status":        models.KeyStatusInvalid,
		"failure_count": "2",
		"created_at":    time.Now().Unix(),
	}
	require.NoError(t, memStore.HSet(keyHashKey, keyDetails))

	require.NoError(t, provider.handleSuccess(apiKey.ID, keyHashKey, activeKeysListKey, group.ID))

	n, err := memStore.LLen(activeKeysListKey)
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "recovered key must be re-added to the active list")

	details, err := memStore.HGetAll(keyHashKey)
	require.NoError(t, err)
	require.Equal(t, models.KeyStatusActive, details["status"])
}

// TestHandleSuccess_RecoveryDeletionBarrierKeepsActiveListClean verifies that
// deletion cannot interleave between recovery's existence check and LPush.
func TestHandleSuccess_RecoveryDeletionBarrierKeepsActiveListClean(t *testing.T) {
	provider, db, memStore := setupTestProvider(t)
	defer provider.Stop()

	group := createTestGroup(t, db, "test-group")
	encSvc, _ := encryption.NewService("test-key-32-bytes-long-enough!!")
	encryptedKey, err := encSvc.Encrypt("sk-recovery-race-key")
	require.NoError(t, err)
	apiKey := &models.APIKey{
		GroupID:      group.ID,
		KeyValue:     encryptedKey,
		KeyHash:      encSvc.Hash("sk-recovery-race-key"),
		Status:       models.KeyStatusInvalid,
		FailureCount: 2,
	}
	require.NoError(t, db.Create(apiKey).Error)

	keyHashKey := fmt.Sprintf("key:%d", apiKey.ID)
	activeKeysListKey := fmt.Sprintf("group:%d:active_keys", group.ID)
	require.NoError(t, memStore.HSet(keyHashKey, map[string]any{
		"id":            fmt.Sprintf("%d", apiKey.ID),
		"key_string":    encryptedKey,
		"status":        models.KeyStatusInvalid,
		"failure_count": "2",
		"created_at":    time.Now().Unix(),
	}))

	barrier := &recoveryBarrierStore{
		Store:     memStore,
		startLRem: make(chan struct{}),
		release:   make(chan struct{}),
	}
	provider.store = barrier

	recoveryDone := make(chan error, 1)
	go func() {
		recoveryDone <- provider.handleSuccess(apiKey.ID, keyHashKey, activeKeysListKey, group.ID)
	}()
	<-barrier.startLRem

	dbDeleted := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		if err := db.Delete(&models.APIKey{}, apiKey.ID).Error; err != nil {
			deleteDone <- err
			return
		}
		close(dbDeleted)
		deleteDone <- provider.removeKeyFromStore(apiKey.ID, group.ID)
	}()
	<-dbDeleted
	close(barrier.release)

	require.NoError(t, <-recoveryDone)
	require.NoError(t, <-deleteDone)

	length, err := memStore.LLen(activeKeysListKey)
	require.NoError(t, err)
	require.Equal(t, int64(0), length, "deleted key must not remain in active list")
}
