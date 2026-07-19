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

	t.Run("single auth type - JSON supplemental value preserves existing token", func(t *testing.T) {
		existingValue := `{"access_token":"old-token","refresh_token":"old-refresh"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		result, err := svc.mergeAuthValues("access_token", encrypted, `{"refresh_token":"new-refresh"}`)
		require.NoError(t, err)

		var parsed map[string]string
		err = json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)
		assert.Equal(t, "old-token", parsed["access_token"])
		assert.Equal(t, "new-refresh", parsed["refresh_token"])
	})

	t.Run("single auth type - plain text update replaces supplemental values", func(t *testing.T) {
		existingValue := `{"access_token":"old-token","refresh_token":"old-refresh"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		result, err := svc.mergeAuthValues("access_token", encrypted, "new-token")
		require.NoError(t, err)
		assert.Equal(t, "new-token", result)
	})

	t.Run("single auth type - replacing access token drops stale expiration metadata", func(t *testing.T) {
		existingValue := `{"access_token":"old-token","refresh_token":"old-refresh","token_expires_at":"2020-01-01T00:00:00Z"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		result, err := svc.mergeAuthValues("access_token", encrypted, `{"access_token":"new-token"}`)
		require.NoError(t, err)

		var parsed map[string]string
		require.NoError(t, json.Unmarshal([]byte(result), &parsed))
		assert.Equal(t, "new-token", parsed[AuthTypeAccessToken])
		assert.Equal(t, "old-refresh", parsed[authFieldRefreshToken])
		assert.NotContains(t, parsed, authFieldTokenExpiresAt)
	})

	t.Run("single auth type - explicit new expiration metadata wins", func(t *testing.T) {
		existingValue := `{"access_token":"old-token","token_expires_at":"2020-01-01T00:00:00Z"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		result, err := svc.mergeAuthValues("access_token", encrypted, `{"access_token":"new-token","token_expires_at":"2030-01-01T00:00:00Z"}`)
		require.NoError(t, err)

		var parsed map[string]string
		require.NoError(t, json.Unmarshal([]byte(result), &parsed))
		assert.Equal(t, "new-token", parsed[AuthTypeAccessToken])
		assert.Equal(t, "2030-01-01T00:00:00Z", parsed[authFieldTokenExpiresAt])
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

	t.Run("multi-auth - preserves and updates Sub2API supplemental values", func(t *testing.T) {
		existingValue := `{"access_token":"old-token","cookie":"old-cookie","refresh_token":"old-refresh","token_expires_at":"2020-01-01T00:00:00Z"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		result, err := svc.mergeAuthValues("access_token,cookie", encrypted, `{"access_token":"new-token","refresh_token":"new-refresh"}`)
		require.NoError(t, err)

		var parsed map[string]string
		require.NoError(t, json.Unmarshal([]byte(result), &parsed))
		assert.Equal(t, "new-token", parsed[AuthTypeAccessToken])
		assert.Equal(t, "old-cookie", parsed[AuthTypeCookie])
		assert.Equal(t, "new-refresh", parsed[authFieldRefreshToken])
		assert.NotContains(t, parsed, authFieldTokenExpiresAt)
	})

	t.Run("cookie-only auth drops stale Sub2API supplemental values", func(t *testing.T) {
		existingValue := `{"cookie":"old-cookie","refresh_token":"stale-refresh","token_expires_at":"2020-01-01T00:00:00Z"}`
		encrypted, err := encSvc.Encrypt(existingValue)
		require.NoError(t, err)

		result, err := svc.mergeAuthValues(AuthTypeCookie, encrypted, `{"cookie":"new-cookie"}`)
		require.NoError(t, err)

		var parsed map[string]string
		require.NoError(t, json.Unmarshal([]byte(result), &parsed))
		assert.Equal(t, "new-cookie", parsed[AuthTypeCookie])
		assert.NotContains(t, parsed, authFieldRefreshToken)
		assert.NotContains(t, parsed, authFieldTokenExpiresAt)
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

func TestSiteService_ReconcileAuthValuePreservesRefreshOnlySub2APIToken(t *testing.T) {
	encSvc, err := encryption.NewService("test-key-32-bytes-long-exactly!")
	require.NoError(t, err)
	svc := &SiteService{encryptionSvc: encSvc}

	existing, err := encSvc.Encrypt(`{"refresh_token":"refresh-only"}`)
	require.NoError(t, err)

	reconciled, err := svc.reconcileAuthValueForTypeChange(
		AuthTypeAccessToken,
		AuthTypeAccessToken+","+AuthTypeCookie,
		existing,
	)
	require.NoError(t, err)

	decrypted, err := encSvc.Decrypt(reconciled)
	require.NoError(t, err)
	var values map[string]string
	require.NoError(t, json.Unmarshal([]byte(decrypted), &values))
	assert.Equal(t, "refresh-only", values[authFieldRefreshToken])
}
