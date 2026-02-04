package i18n

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

// TestSpecificTranslationKeys verifies that specific important keys exist
func TestSpecificTranslationKeys(t *testing.T) {
	requiredKeys := []string{
		"hub.access_key.created",
		"hub.access_key.updated",
		"hub.access_key.deleted",
		"hub.model_pool.updated",
		"hub.routing.model_required",
		"channel.type.openai",
		"relay_format.openai_chat",
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

// TestChannelTypeTranslations verifies channel type translations exist
func TestChannelTypeTranslations(t *testing.T) {
	channelTypes := []string{
		"channel.type.openai",
		"channel.type.anthropic",
		"channel.type.gemini",
		"channel.type.codex",
		"channel.type.azure",
		"channel.type.custom",
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

// TestRelayFormatTranslations verifies relay format translations exist
func TestRelayFormatTranslations(t *testing.T) {
	relayFormats := []string{
		"relay_format.openai_chat",
		"relay_format.openai_completion",
		"relay_format.claude",
		"relay_format.codex",
		"relay_format.gemini",
		"relay_format.unknown",
	}

	for _, key := range relayFormats {
		if _, exists := MessagesEnUS[key]; !exists {
			t.Errorf("Relay format key %q missing in English", key)
		}
		if _, exists := MessagesZhCN[key]; !exists {
			t.Errorf("Relay format key %q missing in Chinese", key)
		}
		if _, exists := MessagesJaJP[key]; !exists {
			t.Errorf("Relay format key %q missing in Japanese", key)
		}
	}
}
