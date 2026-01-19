package encryption

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewService(t *testing.T) {
	t.Run("WithEncryptionKey", func(t *testing.T) {
		svc, err := NewService("test-encryption-key-32-bytes!!")
		require.NoError(t, err)
		assert.NotNil(t, svc)

		// Verify it's an AES service
		_, ok := svc.(*aesService)
		assert.True(t, ok, "Should create AES service with encryption key")
	})

	t.Run("WithoutEncryptionKey", func(t *testing.T) {
		svc, err := NewService("")
		require.NoError(t, err)
		assert.NotNil(t, svc)

		// Verify it's a noop service
		_, ok := svc.(*noopService)
		assert.True(t, ok, "Should create noop service without encryption key")
	})
}

func TestAESServiceEncryptDecrypt(t *testing.T) {
	svc, err := NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)

	testCases := []struct {
		name      string
		plaintext string
	}{
		{"EmptyString", ""},
		{"ShortString", "hello"},
		{"LongString", strings.Repeat("a", 1000)},
		{"SpecialChars", "!@#$%^&*()_+-=[]{}|;':\",./<>?"},
		{"Unicode", "你好世界🌍"},
		{"APIKey", "sk-1234567890abcdefghijklmnopqrstuvwxyz"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encrypt
			ciphertext, err := svc.Encrypt(tc.plaintext)
			require.NoError(t, err)
			assert.NotEmpty(t, ciphertext)

			// Verify ciphertext is hex encoded
			_, err = hex.DecodeString(ciphertext)
			assert.NoError(t, err, "Ciphertext should be valid hex")

			// Verify ciphertext is different from plaintext
			if tc.plaintext != "" {
				assert.NotEqual(t, tc.plaintext, ciphertext)
			}

			// Decrypt
			decrypted, err := svc.Decrypt(ciphertext)
			require.NoError(t, err)
			assert.Equal(t, tc.plaintext, decrypted)
		})
	}
}

func TestAESServiceEncryptUniqueness(t *testing.T) {
	svc, err := NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)

	plaintext := "test-data"

	// Encrypt same plaintext multiple times
	ciphertexts := make(map[string]bool)
	for i := 0; i < 10; i++ {
		ciphertext, err := svc.Encrypt(plaintext)
		require.NoError(t, err)
		ciphertexts[ciphertext] = true
	}

	// All ciphertexts should be unique (due to random nonce)
	assert.Equal(t, 10, len(ciphertexts), "Each encryption should produce unique ciphertext")
}

func TestAESServiceDecryptErrors(t *testing.T) {
	svc, err := NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)

	t.Run("InvalidHex", func(t *testing.T) {
		_, err := svc.Decrypt("not-hex-data")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid hex")
	})

	t.Run("TooShort", func(t *testing.T) {
		_, err := svc.Decrypt("abcd")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "too short")
	})

	t.Run("InvalidCiphertext", func(t *testing.T) {
		// Valid hex but invalid ciphertext
		invalidData := hex.EncodeToString([]byte("invalid-ciphertext-data"))
		_, err := svc.Decrypt(invalidData)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "decryption failed")
	})

	t.Run("TamperedCiphertext", func(t *testing.T) {
		// Encrypt valid data
		ciphertext, err := svc.Encrypt("test-data")
		require.NoError(t, err)

		// Tamper with ciphertext
		data, _ := hex.DecodeString(ciphertext)
		data[len(data)-1] ^= 0xFF // Flip last byte
		tampered := hex.EncodeToString(data)

		// Decryption should fail
		_, err = svc.Decrypt(tampered)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "decryption failed")
	})
}

func TestAESServiceHash(t *testing.T) {
	svc, err := NewService("test-encryption-key-32-bytes!!")
	require.NoError(t, err)

	t.Run("EmptyString", func(t *testing.T) {
		hash := svc.Hash("")
		assert.Empty(t, hash)
	})

	t.Run("NonEmptyString", func(t *testing.T) {
		hash := svc.Hash("test-data")
		assert.NotEmpty(t, hash)

		// Verify hash is hex encoded
		_, err := hex.DecodeString(hash)
		assert.NoError(t, err, "Hash should be valid hex")

		// Verify hash length (HMAC-SHA256 = 32 bytes = 64 hex chars)
		assert.Equal(t, 64, len(hash))
	})

	t.Run("Deterministic", func(t *testing.T) {
		plaintext := "test-data"
		hash1 := svc.Hash(plaintext)
		hash2 := svc.Hash(plaintext)
		assert.Equal(t, hash1, hash2, "Hash should be deterministic")
	})

	t.Run("DifferentInputs", func(t *testing.T) {
		hash1 := svc.Hash("test-data-1")
		hash2 := svc.Hash("test-data-2")
		assert.NotEqual(t, hash1, hash2, "Different inputs should produce different hashes")
	})
}

func TestNoopService(t *testing.T) {
	svc, err := NewService("")
	require.NoError(t, err)

	t.Run("EncryptPassthrough", func(t *testing.T) {
		plaintext := "test-data"
		ciphertext, err := svc.Encrypt(plaintext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, ciphertext, "Noop service should return plaintext as-is")
	})

	t.Run("DecryptPassthrough", func(t *testing.T) {
		ciphertext := "test-data"
		plaintext, err := svc.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, ciphertext, plaintext, "Noop service should return ciphertext as-is")
	})

	t.Run("HashWithoutKey", func(t *testing.T) {
		hash := svc.Hash("test-data")
		assert.NotEmpty(t, hash)

		// Verify hash is hex encoded
		_, err := hex.DecodeString(hash)
		assert.NoError(t, err, "Hash should be valid hex")

		// Verify hash length (SHA256 = 32 bytes = 64 hex chars)
		assert.Equal(t, 64, len(hash))
	})

	t.Run("HashEmptyString", func(t *testing.T) {
		hash := svc.Hash("")
		assert.Empty(t, hash)
	})
}

func TestDifferentKeys(t *testing.T) {
	svc1, err := NewService("key-1-must-be-32-bytes-long!!")
	require.NoError(t, err)

	svc2, err := NewService("key-2-must-be-32-bytes-long!!")
	require.NoError(t, err)

	plaintext := "test-data"

	t.Run("DifferentHashes", func(t *testing.T) {
		hash1 := svc1.Hash(plaintext)
		hash2 := svc2.Hash(plaintext)
		assert.NotEqual(t, hash1, hash2, "Different keys should produce different hashes")
	})

	t.Run("CannotDecryptWithDifferentKey", func(t *testing.T) {
		ciphertext, err := svc1.Encrypt(plaintext)
		require.NoError(t, err)

		// Try to decrypt with different key
		_, err = svc2.Decrypt(ciphertext)
		assert.Error(t, err, "Should not be able to decrypt with different key")
	})
}
