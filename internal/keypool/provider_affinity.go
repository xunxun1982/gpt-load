package keypool

import (
	"crypto/subtle"
	"fmt"
	"gpt-load/internal/models"
	"strconv"
	"time"
)

func (p *KeyProvider) decryptStoredKey(keyID uint, keyDetails map[string]string) (string, error) {
	storedValue := keyDetails["key_string"]
	decryptedValue, err := p.encryptionSvc.Decrypt(storedValue)
	if err == nil {
		return decryptedValue, nil
	}

	storedHash := keyDetails["key_hash"]
	plainHash := p.encryptionSvc.Hash(storedValue)
	if storedHash != "" && plainHash != "" && subtle.ConstantTimeCompare([]byte(storedHash), []byte(plainHash)) == 1 {
		return storedValue, nil
	}
	return "", fmt.Errorf("failed to decrypt key ID %d: %w", keyID, err)
}

// GetActiveKeyByID returns a fresh decrypted API key from the current store state.
func (p *KeyProvider) GetActiveKeyByID(groupID, keyID uint) (*models.APIKey, error) {
	keyHashKey := "key:" + strconv.FormatUint(uint64(keyID), 10)
	keyDetails, err := p.store.HGetAll(keyHashKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get key details for key ID %d: %w", keyID, err)
	}
	if len(keyDetails) == 0 {
		return nil, fmt.Errorf("key ID %d not found", keyID)
	}

	storedGroupID, err := strconv.ParseUint(keyDetails["group_id"], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid key record for key ID %d: invalid group_id", keyID)
	}
	if uint(storedGroupID) != groupID {
		return nil, fmt.Errorf("key ID %d group mismatch", keyID)
	}
	if keyDetails["status"] == "" {
		return nil, fmt.Errorf("invalid key record for key ID %d: missing status", keyID)
	}
	if keyDetails["status"] != models.KeyStatusActive {
		return nil, fmt.Errorf("key ID %d is not active", keyID)
	}

	encryptedKeyValue, ok := keyDetails["key_string"]
	if !ok || encryptedKeyValue == "" {
		return nil, fmt.Errorf("invalid key record for key ID %d: missing key_string", keyID)
	}
	decryptedKeyValue, err := p.decryptStoredKey(keyID, keyDetails)
	if err != nil {
		return nil, err
	}

	failureCount, _ := strconv.ParseInt(keyDetails["failure_count"], 10, 64)
	createdAt, _ := strconv.ParseInt(keyDetails["created_at"], 10, 64)
	return &models.APIKey{
		ID:           keyID,
		KeyValue:     decryptedKeyValue,
		KeyHash:      keyDetails["key_hash"],
		Status:       keyDetails["status"],
		FailureCount: failureCount,
		GroupID:      groupID,
		CreatedAt:    time.Unix(createdAt, 0),
	}, nil
}
