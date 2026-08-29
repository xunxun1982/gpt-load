package keypool

import (
	"fmt"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"

	"github.com/stretchr/testify/require"
)

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
}

// TestHandleFailure_DeletedKeyCleansUpResidualStoreHash locks the fallback
// cleanup: when the DB row is already gone but the store hash still exists
// (deletion race), the failure handler must remove the residual hash instead
// of writing to it.
func TestHandleFailure_DeletedKeyCleansUpResidualStoreHash(t *testing.T) {
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

	// Simulate the DB row being deleted while the store hash is still present.
	require.NoError(t, db.Delete(&models.APIKey{}, apiKey.ID).Error)

	require.NoError(t, provider.handleFailure(apiKey, group, keyHashKey, activeKeysListKey))

	details, err := memStore.HGetAll(keyHashKey)
	require.NoError(t, err)
	require.Empty(t, details, "residual store hash of a deleted key must be cleaned up")
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
