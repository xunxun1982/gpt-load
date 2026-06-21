package channel

import (
	"testing"
	"unicode/utf8"

	"gpt-load/internal/models"

	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestValidationPromptQueue(t *testing.T) {
	require.Len(t, validationPromptQueue, 100)

	seen := make(map[string]struct{}, len(validationPromptQueue))
	for _, prompt := range validationPromptQueue {
		require.NotEqual(t, validationDefaultPrompt, prompt)
		require.LessOrEqual(t, utf8.RuneCountInString(prompt), 8)
		require.NotContains(t, seen, prompt)
		seen[prompt] = struct{}{}
	}
}

func TestValidationPromptForGroup(t *testing.T) {
	require.Equal(t, validationDefaultPrompt, validationPromptForGroup(&models.Group{}))

	prompt := validationPromptForGroup(&models.Group{
		Config: datatypes.JSONMap{"validation_prompt_mode": "random_queue"},
	})
	require.NotEqual(t, validationDefaultPrompt, prompt)
	require.True(t, validationPromptInQueue(prompt), "prompt %q should come from validation queue", prompt)
}

func TestValidationConfigHelpers(t *testing.T) {
	group := &models.Group{
		Config: datatypes.JSONMap{
			"validation_stream":                     true,
			"responses_include_encrypted_reasoning": true,
		},
	}

	require.True(t, validationStreamEnabled(group))
	require.True(t, validationResponsesIncludeEncryptedReasoning(group))
	require.False(t, validationStreamEnabled(&models.Group{}))
	require.False(t, validationResponsesIncludeEncryptedReasoning(&models.Group{}))
	require.True(t, validationStreamEnabled(&models.Group{
		Config: datatypes.JSONMap{"force_stream": true},
	}))
	require.False(t, validationStreamEnabled(&models.Group{
		Config: datatypes.JSONMap{"validation_stream": true, "force_non_stream": true},
	}))
}

func TestValidationStreamPayloadValue(t *testing.T) {
	tests := []struct {
		name      string
		config    datatypes.JSONMap
		wantValue bool
		wantSet   bool
	}{
		{
			name:    "default follows request and omits stream field",
			config:  datatypes.JSONMap{},
			wantSet: false,
		},
		{
			name:      "validation stream enables stream field",
			config:    datatypes.JSONMap{"validation_stream": true},
			wantValue: true,
			wantSet:   true,
		},
		{
			name:      "force stream enables stream field",
			config:    datatypes.JSONMap{"force_stream": true},
			wantValue: true,
			wantSet:   true,
		},
		{
			name:      "force non stream writes explicit false",
			config:    datatypes.JSONMap{"force_non_stream": true},
			wantValue: false,
			wantSet:   true,
		},
		{
			name:      "force non stream overrides validation stream",
			config:    datatypes.JSONMap{"validation_stream": true, "force_non_stream": true},
			wantValue: false,
			wantSet:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotValue, gotSet := validationStreamPayloadValue(&models.Group{Config: tt.config})
			require.Equal(t, tt.wantSet, gotSet)
			require.Equal(t, tt.wantValue, gotValue)
		})
	}
}

func validationPromptInQueue(prompt string) bool {
	for _, queuedPrompt := range validationPromptQueue {
		if queuedPrompt == prompt {
			return true
		}
	}
	return false
}
