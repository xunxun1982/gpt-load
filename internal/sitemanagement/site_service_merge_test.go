package sitemanagement

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/encryption"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSiteService_MergeAuthValues(t *testing.T) {
	encSvc, err := encryption.NewService("test-key-32-bytes-long-exactly!")
	require.NoError(t, err)
	svc := &SiteService{
		encryptionSvc: encSvc,
	}

	t.Run("single auth type - no merge needed", func(t *testing.T) {
		result, err := svc.mergeAuthValues("access_token", "", "new-token-value")
		require.NoError(t, err)
		assert.Equal(t, "new-token-value", result)
	})

	t.Run("multi-auth - new JSON with both values", func(t *testing.T) {
		newValue := `{"access_token":"new-token","cookie":"new-cookie"}`
		result, err := svc.mergeAuthValues("access_token,cookie", "", newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "new-token", parsed["access_token"])
		assert.Equal(t, "new-cookie", parsed["cookie"])
	})

	t.Run("multi-auth - merge with existing values", func(t *testing.T) {
		// Existing: both access_token and cookie
		existingValue := `{"access_token":"old-token","cookie":"old-cookie"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		// New: only update access_token
		newValue := `{"access_token":"new-token"}`
		result, err := svc.mergeAuthValues("access_token,cookie", encrypted, newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "new-token", parsed["access_token"])
		assert.Equal(t, "old-cookie", parsed["cookie"], "should preserve existing cookie")
	})

	t.Run("multi-auth - merge with existing values (update cookie only)", func(t *testing.T) {
		// Existing: both access_token and cookie
		existingValue := `{"access_token":"old-token","cookie":"old-cookie"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		// New: only update cookie
		newValue := `{"cookie":"new-cookie"}`
		result, err := svc.mergeAuthValues("access_token,cookie", encrypted, newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "old-token", parsed["access_token"], "should preserve existing token")
		assert.Equal(t, "new-cookie", parsed["cookie"])
	})

	t.Run("multi-auth - plain text new value", func(t *testing.T) {
		// Existing: JSON format
		existingValue := `{"access_token":"old-token","cookie":"old-cookie"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		// New: plain text (legacy format) - should be assigned to first auth type
		newValue := "new-token-plain"
		result, err := svc.mergeAuthValues("access_token,cookie", encrypted, newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "new-token-plain", parsed["access_token"])
		assert.Equal(t, "old-cookie", parsed["cookie"], "should preserve existing cookie")
	})

	t.Run("multi-auth - legacy existing value", func(t *testing.T) {
		// Existing: plain text (legacy format)
		existingValue := "old-token-plain"
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		// New: JSON format with only cookie
		newValue := `{"cookie":"new-cookie"}`
		result, err := svc.mergeAuthValues("access_token,cookie", encrypted, newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "old-token-plain", parsed["access_token"], "should preserve legacy token")
		assert.Equal(t, "new-cookie", parsed["cookie"])
	})

	t.Run("multi-auth - no existing value", func(t *testing.T) {
		// New: only one value provided
		newValue := `{"access_token":"new-token"}`
		result, err := svc.mergeAuthValues("access_token,cookie", "", newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "new-token", parsed["access_token"])
		assert.NotContains(t, parsed, "cookie", "should not have cookie key if not provided")
	})

	t.Run("multi-auth - decryption failure", func(t *testing.T) {
		// Invalid encrypted value (should be handled gracefully)
		invalidEncrypted := "invalid-encrypted-data"

		// New: JSON format
		newValue := `{"access_token":"new-token"}`
		result, err := svc.mergeAuthValues("access_token,cookie", invalidEncrypted, newValue)
		require.NoError(t, err)

		// Should proceed without merge (best effort)
		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "new-token", parsed["access_token"])
		assert.NotContains(t, parsed, "cookie")
	})

	t.Run("multi-auth - auth type with spaces", func(t *testing.T) {
		// Auth type with spaces
		newValue := `{"access_token":"new-token","cookie":"new-cookie"}`
		result, err := svc.mergeAuthValues(" access_token , cookie ", "", newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "new-token", parsed["access_token"])
		assert.Equal(t, "new-cookie", parsed["cookie"])
	})

	t.Run("multi-auth - auth type with none mixed in", func(t *testing.T) {
		// Auth type with "none" should be filtered out
		newValue := `{"access_token":"new-token"}`
		result, err := svc.mergeAuthValues("none,access_token,cookie", "", newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "new-token", parsed["access_token"])
		assert.NotContains(t, parsed, "none")
	})

	t.Run("multi-auth - empty string values should not override", func(t *testing.T) {
		// Existing: both values
		existingValue := `{"access_token":"old-token","cookie":"old-cookie"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		// New: empty string for access_token (should not override)
		newValue := `{"access_token":"","cookie":"new-cookie"}`
		result, err := svc.mergeAuthValues("access_token,cookie", encrypted, newValue)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "old-token", parsed["access_token"], "empty string should not override")
		assert.Equal(t, "new-cookie", parsed["cookie"])
	})
}
