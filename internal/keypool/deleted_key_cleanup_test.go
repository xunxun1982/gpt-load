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
