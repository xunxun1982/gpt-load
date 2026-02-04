package locales

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

// TestCommonMessageKeys verifies that common message keys exist
func TestCommonMessageKeys(t *testing.T) {
	requiredKeys := []string{
		"success",
		"error",
		"unauthorized",
		"forbidden",
		"not_found",
		"bad_request",
		"internal_error",
	}

	for _, key := range requiredKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Required key %q missing in English translations", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Required key %q missing in Chinese translations", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Required key %q missing in Japanese translations", key)
		}
	}
}

// TestAuthMessageKeys verifies that authentication message keys exist
func TestAuthMessageKeys(t *testing.T) {
	authKeys := []string{
		"auth.invalid_key",
		"auth.key_required",
		"auth.login_success",
		"auth.logout_success",
	}

	for _, key := range authKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Auth key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Auth key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Auth key %q missing in Japanese", key)
		}
	}
}

// TestGroupMessageKeys verifies that group message keys exist
func TestGroupMessageKeys(t *testing.T) {
	groupKeys := []string{
		"group.created",
		"group.updated",
		"group.deleted",
		"group.not_found",
		"group.name_exists",
	}

	for _, key := range groupKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Group key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Group key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Group key %q missing in Japanese", key)
		}
	}
}

// TestKeyMessageKeys verifies that key message keys exist
func TestKeyMessageKeys(t *testing.T) {
	keyKeys := []string{
		"key.created",
		"key.updated",
		"key.deleted",
		"key.not_found",
		"key.invalid",
	}

	for _, key := range keyKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Key message %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Key message %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Key message %q missing in Japanese", key)
		}
	}
}

// TestConfigMessageKeys verifies that config message keys exist
func TestConfigMessageKeys(t *testing.T) {
	configKeys := []string{
		"config.updated",
		"config.app_url",
		"config.proxy_keys",
		"config.log_retention_days",
		"config.request_timeout",
		"config.max_retries",
	}

	for _, key := range configKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Config key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Config key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Config key %q missing in Japanese", key)
		}
	}
}

// TestValidationMessageKeys verifies that validation message keys exist
func TestValidationMessageKeys(t *testing.T) {
	validationKeys := []string{
		"validation.invalid_group_name",
		"validation.group_not_found",
		"validation.invalid_status_filter",
		"validation.test_model_required",
	}

	for _, key := range validationKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Validation key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Validation key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Validation key %q missing in Japanese", key)
		}
	}
}

// TestChannelTypeTranslations verifies channel type translations exist
func TestChannelTypeTranslations(t *testing.T) {
	channelTypes := []string{
		"channel.openai",
		"channel.anthropic",
		"channel.gemini",
		"channel.codex",
		"channel.azure",
		"channel.custom",
	}

	for _, key := range channelTypes {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Channel type key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Channel type key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Channel type key %q missing in Japanese", key)
		}
	}
}

// TestDashboardMessageKeys verifies that dashboard message keys exist
func TestDashboardMessageKeys(t *testing.T) {
	dashboardKeys := []string{
		"dashboard.invalid_keys",
		"dashboard.success_requests",
		"dashboard.failed_requests",
		"dashboard.auth_key_missing",
		"dashboard.encryption_key_missing",
	}

	for _, key := range dashboardKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Dashboard key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Dashboard key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Dashboard key %q missing in Japanese", key)
		}
	}
}

// TestSecurityMessageKeys verifies that security message keys exist
func TestSecurityMessageKeys(t *testing.T) {
	securityKeys := []string{
		"security.password_too_short",
		"security.password_short",
		"security.password_weak_pattern",
		"security.password_low_complexity",
	}

	for _, key := range securityKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Security key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Security key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Security key %q missing in Japanese", key)
		}
	}
}

// TestBindingMessageKeys verifies that binding message keys exist
func TestBindingMessageKeys(t *testing.T) {
	bindingKeys := []string{
		"binding.group_not_found",
		"binding.site_not_found",
		"binding.aggregate_cannot_bind",
		"binding.child_group_cannot_bind",
	}

	for _, key := range bindingKeys {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Binding key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Binding key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Binding key %q missing in Japanese", key)
		}
	}
}
