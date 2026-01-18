package types

import "testing"

func TestRelayFormat_String(t *testing.T) {
	tests := []struct {
		name   string
		format RelayFormat
		want   string
	}{
		{"OpenAI Chat", RelayFormatOpenAIChat, "openai_chat"},
		{"Claude", RelayFormatClaude, "claude"},
		{"Gemini", RelayFormatGemini, "gemini"},
		{"Image", RelayFormatOpenAIImage, "openai_image"},
		{"Audio Speech", RelayFormatOpenAIAudioSpeech, "openai_audio_speech"},
		{"Embedding", RelayFormatOpenAIEmbedding, "openai_embedding"},
		{"Unknown", RelayFormatUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.format.String(); got != tt.want {
				t.Errorf("RelayFormat.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRelayFormat_IsValid(t *testing.T) {
	tests := []struct {
		name   string
		format RelayFormat
		want   bool
	}{
		{"Valid OpenAI Chat", RelayFormatOpenAIChat, true},
		{"Valid Claude", RelayFormatClaude, true},
		{"Valid Gemini", RelayFormatGemini, true},
		{"Valid Image", RelayFormatOpenAIImage, true},
		{"Valid Audio", RelayFormatOpenAIAudioSpeech, true},
		{"Valid Embedding", RelayFormatOpenAIEmbedding, true},
		{"Invalid Unknown", RelayFormatUnknown, false},
		{"Invalid Empty", RelayFormat(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.format.IsValid(); got != tt.want {
				t.Errorf("RelayFormat.IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRelayFormat_SupportsStreaming(t *testing.T) {
	tests := []struct {
		name   string
		format RelayFormat
		want   bool
	}{
		{"OpenAI Chat supports streaming", RelayFormatOpenAIChat, true},
		{"OpenAI Completion supports streaming", RelayFormatOpenAICompletion, true},
		{"Claude supports streaming", RelayFormatClaude, true},
		{"Codex supports streaming", RelayFormatCodex, true},
		{"Gemini supports streaming", RelayFormatGemini, true},
		{"Image does not support streaming", RelayFormatOpenAIImage, false},
		{"Audio Speech does not support streaming", RelayFormatOpenAIAudioSpeech, false},
		{"Audio Transcription does not support streaming", RelayFormatOpenAIAudioTranscription, false},
		{"Embedding does not support streaming", RelayFormatOpenAIEmbedding, false},
		{"Moderation does not support streaming", RelayFormatOpenAIModeration, false},
		{"Unknown does not support streaming", RelayFormatUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.format.SupportsStreaming(); got != tt.want {
				t.Errorf("RelayFormat.SupportsStreaming() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRelayFormat_RequiresMultipart(t *testing.T) {
	tests := []struct {
		name   string
		format RelayFormat
		want   bool
	}{
		{"Image requires multipart", RelayFormatOpenAIImage, true},
		{"Image Edit requires multipart", RelayFormatOpenAIImageEdit, true},
		{"Audio Transcription requires multipart", RelayFormatOpenAIAudioTranscription, true},
		{"Audio Translation requires multipart", RelayFormatOpenAIAudioTranslation, true},
		{"Chat does not require multipart", RelayFormatOpenAIChat, false},
		{"Claude does not require multipart", RelayFormatClaude, false},
		{"Audio Speech does not require multipart", RelayFormatOpenAIAudioSpeech, false},
		{"Embedding does not require multipart", RelayFormatOpenAIEmbedding, false},
		{"Unknown does not require multipart", RelayFormatUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.format.RequiresMultipart(); got != tt.want {
				t.Errorf("RelayFormat.RequiresMultipart() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRelayFormatConstants verifies all format constants are defined correctly
func TestRelayFormatConstants(t *testing.T) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion,
		RelayFormatClaude,
		RelayFormatCodex,
		RelayFormatOpenAIImage,
		RelayFormatOpenAIImageEdit,
		RelayFormatOpenAIAudioTranscription,
		RelayFormatOpenAIAudioTranslation,
		RelayFormatOpenAIAudioSpeech,
		RelayFormatOpenAIEmbedding,
		RelayFormatOpenAIModeration,
		RelayFormatGemini,
		RelayFormatUnknown,
	}

	// Verify all formats have unique string values
	seen := make(map[string]bool)
	for _, format := range formats {
		str := format.String()
		if str == "" {
			t.Errorf("Format %v has empty string representation", format)
		}
		if seen[str] {
			t.Errorf("Duplicate format string: %s", str)
		}
		seen[str] = true
	}

	// Verify we have at least 13 unique formats
	if len(seen) < 13 {
		t.Errorf("Expected at least 13 unique formats, got %d", len(seen))
	}
}
