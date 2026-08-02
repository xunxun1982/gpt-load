package keypool

import (
	"fmt"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func storeAffinityKey(t *testing.T, provider *KeyProvider, keyStore store.Store, groupID, keyID uint, status, plaintext string) string {
	t.Helper()
	encrypted, err := provider.encryptionSvc.Encrypt(plaintext)
	require.NoError(t, err)
	require.NoError(t, keyStore.HSet(fmt.Sprintf("key:%d", keyID), map[string]any{
		"id":            keyID,
		"key_string":    encrypted,
		"status":        status,
		"failure_count": 0,
		"group_id":      groupID,
		"created_at":    time.Now().Unix(),
	}))
	return encrypted
}

func TestGetActiveKeyByIDReturnsFreshDecryptedKey(t *testing.T) {
	provider, _, keyStore := setupTestProvider(t)
	defer provider.Stop()

	const groupID, keyID = uint(41), uint(101)
	encrypted := storeAffinityKey(t, provider, keyStore, groupID, keyID, models.KeyStatusActive, "sk-affinity")

	first, err := provider.GetActiveKeyByID(groupID, keyID)
	require.NoError(t, err)
	require.Equal(t, keyID, first.ID)
	require.Equal(t, groupID, first.GroupID)
	require.Equal(t, models.KeyStatusActive, first.Status)
	require.Equal(t, "sk-affinity", first.KeyValue)

	stored, err := keyStore.HGetAll(fmt.Sprintf("key:%d", keyID))
	require.NoError(t, err)
	require.Equal(t, encrypted, stored["key_string"])

	updatedEncrypted, err := provider.encryptionSvc.Encrypt("sk-updated")
	require.NoError(t, err)
	require.NoError(t, keyStore.HSet(fmt.Sprintf("key:%d", keyID), map[string]any{
		"key_string": updatedEncrypted,
	}))

	first.KeyValue = "mutated"
	second, err := provider.GetActiveKeyByID(groupID, keyID)
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.Equal(t, "sk-updated", second.KeyValue)

	stored, err = keyStore.HGetAll(fmt.Sprintf("key:%d", keyID))
	require.NoError(t, err)
	require.Equal(t, updatedEncrypted, stored["key_string"])

	require.NoError(t, keyStore.HSet(fmt.Sprintf("key:%d", keyID), map[string]any{
		"status": models.KeyStatusInvalid,
	}))
	inactive, err := provider.GetActiveKeyByID(groupID, keyID)
	require.Nil(t, inactive)
	require.ErrorContains(t, err, "not active")
}

func TestGetActiveKeyByIDRejectsUnavailableRecords(t *testing.T) {
	provider, _, keyStore := setupTestProvider(t)
	defer provider.Stop()

	const groupID = uint(42)
	storeAffinityKey(t, provider, keyStore, groupID+1, 202, models.KeyStatusActive, "sk-other-group")
	storeAffinityKey(t, provider, keyStore, groupID, 203, models.KeyStatusInvalid, "sk-invalid")
	require.NoError(t, keyStore.HSet("key:204", map[string]any{
		"id":         204,
		"key_string": "sk-malformed",
		"status":     models.KeyStatusActive,
		"group_id":   "not-a-number",
	}))

	tests := []struct {
		name    string
		keyID   uint
		wantErr string
	}{
		{name: "missing", keyID: 201, wantErr: "not found"},
		{name: "wrong group", keyID: 202, wantErr: "group mismatch"},
		{name: "inactive", keyID: 203, wantErr: "not active"},
		{name: "invalid record", keyID: 204, wantErr: "invalid key record"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := provider.GetActiveKeyByID(groupID, tt.keyID)
			require.Nil(t, key)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestGetActiveKeyByIDDoesNotChangeSelectKeyRotation(t *testing.T) {
	provider, _, keyStore := setupTestProvider(t)
	defer provider.Stop()

	const groupID = uint(43)
	storeAffinityKey(t, provider, keyStore, groupID, 301, models.KeyStatusActive, "sk-first")
	storeAffinityKey(t, provider, keyStore, groupID, 302, models.KeyStatusActive, "sk-second")
	require.NoError(t, keyStore.LPush(fmt.Sprintf("group:%d:active_keys", groupID), 301, 302))

	_, err := provider.GetActiveKeyByID(groupID, 301)
	require.NoError(t, err)

	selected, err := provider.SelectKey(groupID)
	require.NoError(t, err)
	require.Equal(t, uint(302), selected.ID)
}
