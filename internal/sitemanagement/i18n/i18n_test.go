package sitemanagementi18n

import (
	"testing"
)

// TestMessagesEnUSNotEmpty verifies that English translations are not empty
func TestMessagesEnUSNotEmpty(t *testing.T) {
	if len(MessagesEnUS) == 0 {
		t.Fatal("MessagesEnUS should not be empty")
	}
}

// TestMessagesZhCNNotEmpty verifies that Chinese translations are not empty
func TestMessagesZhCNNotEmpty(t *testing.T) {
	if len(MessagesZhCN) == 0 {
		t.Fatal("MessagesZhCN should not be empty")
	}
}

// TestMessagesJaJPNotEmpty verifies that Japanese translations are not empty
func TestMessagesJaJPNotEmpty(t *testing.T) {
	if len(MessagesJaJP) == 0 {
		t.Fatal("MessagesJaJP should not be empty")
	}
}

// TestTranslationKeysConsistency verifies all languages have the same keys
func TestTranslationKeysConsistency(t *testing.T) {
	// Check if all languages have the same number of keys
	enCount := len(MessagesEnUS)
	zhCount := len(MessagesZhCN)
	jaCount := len(MessagesJaJP)

	if enCount != zhCount || enCount != jaCount {
		t.Errorf("Translation key count mismatch: EN=%d, ZH=%d, JA=%d", enCount, zhCount, jaCount)
	}

	// Check if all keys in English exist in other languages
	for key := range MessagesEnUS {
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Key %q exists in English but missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Key %q exists in English but missing in Japanese", key)
		}
	}

	// Check if all keys in Chinese exist in other languages
	for key := range MessagesZhCN {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Key %q exists in Chinese but missing in English", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Key %q exists in Chinese but missing in Japanese", key)
		}
	}

	// Check if all keys in Japanese exist in other languages
	for key := range MessagesJaJP {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Key %q exists in Japanese but missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Key %q exists in Japanese but missing in Chinese", key)
		}
	}
}

// TestTranslationValuesNotEmpty verifies that translation values are not empty
func TestTranslationValuesNotEmpty(t *testing.T) {
	tests := []struct {
		name     string
		messages map[string]string
	}{
		{"English", MessagesEnUS},
		{"Chinese", MessagesZhCN},
		{"Japanese", MessagesJaJP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.messages {
				if value == "" {
					t.Errorf("Translation value for key %q is empty in %s", key, tt.name)
				}
			}
		})
	}
}

// TestValidationMessageKeys verifies that validation message keys exist
func TestValidationMessageKeys(t *testing.T) {
	validationKeys := []string{
		"site_management.validation.name_required",
		"site_management.validation.name_duplicate",
		"site_management.validation.invalid_base_url",
		"site_management.validation.invalid_auth_type",
		"site_management.validation.auth_value_requires_auth_type",
		"site_management.validation.time_window_required",
		"site_management.validation.invalid_time",
		"site_management.validation.invalid_schedule_mode",
		"site_management.validation.deterministic_time_required",
		"site_management.validation.duplicate_time",
		"site_management.validation.schedule_times_required",
	}

	for _, key := range validationKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Validation key %q missing in English translations", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Validation key %q missing in Chinese translations", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Validation key %q missing in Japanese translations", key)
		}
	}
}

// TestCheckinMessageKeys verifies that check-in message keys exist
func TestCheckinMessageKeys(t *testing.T) {
	checkinKeys := []string{
		"site_management.checkin.failed",
		"site_management.checkin.disabled",
		"site_management.checkin.stealth_requires_cookie",
		"site_management.checkin.missing_cf_cookies",
		"site_management.checkin.cloudflare_challenge",
		"site_management.checkin.anyrouter_requires_cookie",
	}

	for _, key := range checkinKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Check-in key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Check-in key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Check-in key %q missing in Japanese", key)
		}
	}
}

// TestAllKeysHavePrefix verifies that all keys have the site_management prefix
func TestAllKeysHavePrefix(t *testing.T) {
	prefix := "site_management."

	tests := []struct {
		name     string
		messages map[string]string
	}{
		{"English", MessagesEnUS},
		{"Chinese", MessagesZhCN},
		{"Japanese", MessagesJaJP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key := range tt.messages {
				if len(key) < len(prefix) || key[:len(prefix)] != prefix {
					t.Errorf("Key %q in %s does not have required prefix %q", key, tt.name, prefix)
				}
			}
		})
	}
}

// TestKeyCategories verifies that keys are properly categorized
func TestKeyCategories(t *testing.T) {
	categories := map[string][]string{
		"validation": {
			"site_management.validation.name_required",
			"site_management.validation.invalid_base_url",
		},
		"checkin": {
			"site_management.checkin.failed",
			"site_management.checkin.disabled",
		},
	}

	for category, keys := range categories {
		t.Run(category, func(t *testing.T) {
			for _, key := range keys {
				if _, exists := MessagesEnUS[key]; !exists {
					t.Errorf("Category %q key %q missing in English", category, key)
				}
				if _, exists := MessagesZhCN[key]; !exists {
					t.Errorf("Category %q key %q missing in Chinese", category, key)
				}
				if _, exists := MessagesJaJP[key]; !exists {
					t.Errorf("Category %q key %q missing in Japanese", category, key)
				}
			}
		})
	}
}

// TestNoEmptyTranslations verifies no translation is an empty string
// Note: This test is intentionally similar to TestTranslationValuesNotEmpty
// for defense-in-depth validation, following the pattern in other i18n test files.
func TestNoEmptyTranslations(t *testing.T) {
	allMaps := map[string]map[string]string{
		"English":  MessagesEnUS,
		"Chinese":  MessagesZhCN,
		"Japanese": MessagesJaJP,
	}

	for lang, messages := range allMaps {
		for key, value := range messages {
			if value == "" {
				t.Errorf("Empty translation for key %q in %s", key, lang)
			}
		}
	}
}

// TestTranslationConsistencyAcrossLanguages verifies structural consistency
func TestTranslationConsistencyAcrossLanguages(t *testing.T) {
	// All three languages should have exactly the same keys
	enKeys := make(map[string]bool)
	zhKeys := make(map[string]bool)
	jaKeys := make(map[string]bool)

	for key := range MessagesEnUS {
		enKeys[key] = true
	}
	for key := range MessagesZhCN {
		zhKeys[key] = true
	}
	for key := range MessagesJaJP {
		jaKeys[key] = true
	}

	// Check for missing keys in Chinese
	for key := range enKeys {
		if !zhKeys[key] {
			t.Errorf("Key %q exists in English but missing in Chinese", key)
		}
		if !jaKeys[key] {
			t.Errorf("Key %q exists in English but missing in Japanese", key)
		}
	}

	// Check for extra keys in Chinese
	for key := range zhKeys {
		if !enKeys[key] {
			t.Errorf("Key %q exists in Chinese but missing in English", key)
		}
	}

	// Check for extra keys in Japanese
	for key := range jaKeys {
		if !enKeys[key] {
			t.Errorf("Key %q exists in Japanese but missing in English", key)
		}
	}
}
