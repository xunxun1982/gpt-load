package sitemanagement

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAuthConfig_LegacySingleAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		authType          string
		decryptedValue    string
		expectedTypes     []string
		expectedValues    map[string]string
		expectedIsEmpty   bool
		expectedHasAccess bool
		expectedHasCookie bool
	}{
		{
			name:              "legacy access_token",
			authType:          "access_token",
			decryptedValue:    "sk-1234567890",
			expectedTypes:     []string{"access_token"},
			expectedValues:    map[string]string{"access_token": "sk-1234567890"},
			expectedIsEmpty:   false,
			expectedHasAccess: true,
			expectedHasCookie: false,
		},
		{
			name:              "legacy cookie",
			authType:          "cookie",
			decryptedValue:    "session=abc123; token=xyz",
			expectedTypes:     []string{"cookie"},
			expectedValues:    map[string]string{"cookie": "session=abc123; token=xyz"},
			expectedIsEmpty:   false,
			expectedHasAccess: false,
			expectedHasCookie: true,
		},
		{
			name:              "none auth type",
			authType:          "none",
			decryptedValue:    "",
			expectedTypes:     []string{},
			expectedValues:    map[string]string{},
			expectedIsEmpty:   true,
			expectedHasAccess: false,
			expectedHasCookie: false,
		},
		{
			name:              "empty auth type",
			authType:          "",
			decryptedValue:    "some-value",
			expectedTypes:     []string{},
			expectedValues:    map[string]string{},
			expectedIsEmpty:   true,
			expectedHasAccess: false,
			expectedHasCookie: false,
		},
		{
			name:              "empty value",
			authType:          "access_token",
			decryptedValue:    "",
			expectedTypes:     []string{"access_token"},
			expectedValues:    map[string]string{},
			expectedIsEmpty:   false,
			expectedHasAccess: true,
			expectedHasCookie: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := parseAuthConfig(tt.authType, tt.decryptedValue)

			assert.Equal(t, tt.expectedTypes, config.AuthTypes)
			assert.Equal(t, tt.expectedValues, config.AuthValues)
			assert.Equal(t, tt.expectedIsEmpty, config.IsEmpty())
			assert.Equal(t, tt.expectedHasAccess, config.HasAuthType("access_token"))
			assert.Equal(t, tt.expectedHasCookie, config.HasAuthType("cookie"))

			// Test GetAuthValue
			for authType, expectedValue := range tt.expectedValues {
				assert.Equal(t, expectedValue, config.GetAuthValue(authType))
			}
		})
	}
}

func TestParseAuthConfig_MultiAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		authType          string
		decryptedValue    string
		expectedTypes     []string
		expectedValues    map[string]string
		expectedIsEmpty   bool
		expectedHasAccess bool
		expectedHasCookie bool
	}{
		{
			name:     "multi-auth with both access_token and cookie",
			authType: "access_token,cookie",
			decryptedValue: func() string {
				data := map[string]string{
					"access_token": "sk-1234567890",
					"cookie":       "session=abc123; cf_clearance=xyz",
				}
				b, _ := json.Marshal(data)
				return string(b)
			}(),
			expectedTypes: []string{"access_token", "cookie"},
			expectedValues: map[string]string{
				"access_token": "sk-1234567890",
				"cookie":       "session=abc123; cf_clearance=xyz",
			},
			expectedIsEmpty:   false,
			expectedHasAccess: true,
			expectedHasCookie: true,
		},
		{
			name:     "multi-auth with spaces in auth_type",
			authType: " access_token , cookie ",
			decryptedValue: func() string {
				data := map[string]string{
					"access_token": "sk-test",
					"cookie":       "session=test",
				}
				b, _ := json.Marshal(data)
				return string(b)
			}(),
			expectedTypes: []string{"access_token", "cookie"},
			expectedValues: map[string]string{
				"access_token": "sk-test",
				"cookie":       "session=test",
			},
			expectedIsEmpty:   false,
			expectedHasAccess: true,
			expectedHasCookie: true,
		},
		{
			name:     "multi-auth with only one value in JSON",
			authType: "access_token,cookie",
			decryptedValue: func() string {
				data := map[string]string{
					"access_token": "sk-only-token",
				}
				b, _ := json.Marshal(data)
				return string(b)
			}(),
			expectedTypes: []string{"access_token", "cookie"},
			expectedValues: map[string]string{
				"access_token": "sk-only-token",
			},
			expectedIsEmpty:   false,
			expectedHasAccess: true,
			expectedHasCookie: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := parseAuthConfig(tt.authType, tt.decryptedValue)

			assert.Equal(t, tt.expectedTypes, config.AuthTypes)
			assert.Equal(t, tt.expectedValues, config.AuthValues)
			assert.Equal(t, tt.expectedIsEmpty, config.IsEmpty())
			assert.Equal(t, tt.expectedHasAccess, config.HasAuthType("access_token"))
			assert.Equal(t, tt.expectedHasCookie, config.HasAuthType("cookie"))

			// Test GetAuthValue
			for authType, expectedValue := range tt.expectedValues {
				assert.Equal(t, expectedValue, config.GetAuthValue(authType))
			}
		})
	}
}

func TestParseAuthConfig_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		authType       string
		decryptedValue string
		expectedTypes  []string
		expectedValues map[string]string
	}{
		{
			name:           "auth_type with none mixed in",
			authType:       "access_token,none,cookie",
			decryptedValue: `{"access_token":"sk-test","cookie":"session=test"}`,
			expectedTypes:  []string{"access_token", "cookie"},
			expectedValues: map[string]string{
				"access_token": "sk-test",
				"cookie":       "session=test",
			},
		},
		{
			name:           "invalid JSON fallback to first auth type",
			authType:       "access_token,cookie",
			decryptedValue: "not-a-json-value",
			expectedTypes:  []string{"access_token", "cookie"},
			expectedValues: map[string]string{
				"access_token": "not-a-json-value",
			},
		},
		{
			name:           "empty JSON object",
			authType:       "access_token,cookie",
			decryptedValue: "{}",
			expectedTypes:  []string{"access_token", "cookie"},
			expectedValues: map[string]string{},
		},
		{
			name:           "multi-auth with empty value",
			authType:       "access_token,cookie",
			decryptedValue: "",
			expectedTypes:  []string{"access_token", "cookie"},
			expectedValues: map[string]string{},
		},
		{
			name:           "only whitespace in auth_type",
			authType:       "  ,  ,  ",
			decryptedValue: "some-value",
			expectedTypes:  []string{},
			expectedValues: map[string]string{},
		},
		{
			name:           "auth_type with trailing comma",
			authType:       "access_token,cookie,",
			decryptedValue: `{"access_token":"sk-test","cookie":"session=test"}`,
			expectedTypes:  []string{"access_token", "cookie"},
			expectedValues: map[string]string{
				"access_token": "sk-test",
				"cookie":       "session=test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := parseAuthConfig(tt.authType, tt.decryptedValue)

			assert.Equal(t, tt.expectedTypes, config.AuthTypes)
			assert.Equal(t, tt.expectedValues, config.AuthValues)
		})
	}
}
