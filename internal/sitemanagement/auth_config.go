package sitemanagement

import (
	"encoding/json"
	"strings"
)

// AuthConfig represents parsed authentication configuration with multiple auth methods.
// Supports both legacy single-auth format and new multi-auth format.
type AuthConfig struct {
	// AuthTypes contains the list of authentication types to try (e.g., ["access_token", "cookie"])
	AuthTypes []string
	// AuthValues maps auth type to its value (e.g., {"access_token": "xxx", "cookie": "yyy"})
	AuthValues map[string]string
}

// parseAuthConfig parses auth_type and auth_value fields into AuthConfig.
// Supports both legacy single-auth format and new multi-auth JSON format.
//
// Legacy format (backward compatible):
//   - auth_type: "access_token" or "cookie" or "none"
//   - auth_value: single encrypted value
//
// New format (multi-auth):
//   - auth_type: "access_token,cookie" (comma-separated)
//   - auth_value: JSON string {"access_token":"xxx","cookie":"yyy"}
//
// Returns AuthConfig with parsed types and values.
// If auth_type is "none" or empty, returns empty config.
func parseAuthConfig(authType, decryptedAuthValue string) AuthConfig {
	config := AuthConfig{
		AuthTypes:  []string{},
		AuthValues: make(map[string]string),
	}

	// Parse auth types (comma-separated)
	authType = strings.TrimSpace(authType)
	if authType == "" || authType == AuthTypeNone {
		return config
	}

	for _, t := range strings.Split(authType, ",") {
		t = strings.TrimSpace(t)
		if t != "" && t != AuthTypeNone {
			config.AuthTypes = append(config.AuthTypes, t)
		}
	}

	if len(config.AuthTypes) == 0 {
		return config
	}

	// Parse auth values
	decryptedAuthValue = strings.TrimSpace(decryptedAuthValue)
	if decryptedAuthValue == "" {
		return config
	}

	// Try to parse as JSON (new multi-auth format)
	var jsonValues map[string]string
	if err := json.Unmarshal([]byte(decryptedAuthValue), &jsonValues); err == nil {
		// Successfully parsed as JSON
		config.AuthValues = jsonValues
		return config
	}

	// Fallback to legacy single-auth format
	// If only one auth type, use the value directly
	if len(config.AuthTypes) == 1 {
		config.AuthValues[config.AuthTypes[0]] = decryptedAuthValue
	} else {
		// Multiple auth types but value is not JSON - invalid state
		// Assign the value to the first auth type as fallback
		config.AuthValues[config.AuthTypes[0]] = decryptedAuthValue
	}

	return config
}

// HasAuthType checks if the config contains the specified auth type.
func (c *AuthConfig) HasAuthType(authType string) bool {
	for _, t := range c.AuthTypes {
		if t == authType {
			return true
		}
	}
	return false
}

// GetAuthValue returns the auth value for the specified type.
// Returns empty string if the type is not found.
func (c *AuthConfig) GetAuthValue(authType string) string {
	return c.AuthValues[authType]
}

// IsEmpty returns true if the config has no auth types.
func (c *AuthConfig) IsEmpty() bool {
	return len(c.AuthTypes) == 0
}
