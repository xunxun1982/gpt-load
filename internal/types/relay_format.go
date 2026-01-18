// Package types defines common types used across the application
package types

// RelayFormat represents the API format/protocol for a request
type RelayFormat string

const (
	// RelayFormatOpenAIChat represents OpenAI chat completions format
	RelayFormatOpenAIChat RelayFormat = "openai_chat"

	// RelayFormatOpenAICompletion represents OpenAI legacy completions format
	RelayFormatOpenAICompletion RelayFormat = "openai_completion"

	// RelayFormatClaude represents Anthropic Claude messages format
	RelayFormatClaude RelayFormat = "claude"

	// RelayFormatCodex represents OpenAI Codex responses format
	RelayFormatCodex RelayFormat = "codex"

	// RelayFormatOpenAIImage represents OpenAI image generation format
	RelayFormatOpenAIImage RelayFormat = "openai_image"

	// RelayFormatOpenAIImageEdit represents OpenAI image edit format
	RelayFormatOpenAIImageEdit RelayFormat = "openai_image_edit"

	// RelayFormatOpenAIAudioTranscription represents OpenAI audio transcription format
	RelayFormatOpenAIAudioTranscription RelayFormat = "openai_audio_transcription"

	// RelayFormatOpenAIAudioTranslation represents OpenAI audio translation format
	RelayFormatOpenAIAudioTranslation RelayFormat = "openai_audio_translation"

	// RelayFormatOpenAIAudioSpeech represents OpenAI text-to-speech format
	RelayFormatOpenAIAudioSpeech RelayFormat = "openai_audio_speech"

	// RelayFormatOpenAIEmbedding represents OpenAI embeddings format
	RelayFormatOpenAIEmbedding RelayFormat = "openai_embedding"

	// RelayFormatOpenAIModeration represents OpenAI moderation format
	RelayFormatOpenAIModeration RelayFormat = "openai_moderation"

	// RelayFormatGemini represents Google Gemini format
	RelayFormatGemini RelayFormat = "gemini"

	// RelayFormatUnknown represents unknown or unsupported format
	RelayFormatUnknown RelayFormat = "unknown"
)

// String returns the string representation of RelayFormat
func (r RelayFormat) String() string {
	return string(r)
}

// IsValid checks if the relay format is valid (not unknown)
func (r RelayFormat) IsValid() bool {
	return r != RelayFormatUnknown && r != ""
}

// SupportsStreaming returns true if the format supports streaming responses
func (r RelayFormat) SupportsStreaming() bool {
	switch r {
	case RelayFormatOpenAIChat, RelayFormatOpenAICompletion, RelayFormatClaude, RelayFormatCodex, RelayFormatGemini:
		return true
	default:
		return false
	}
}

// RequiresMultipart returns true if the format requires multipart/form-data
func (r RelayFormat) RequiresMultipart() bool {
	switch r {
	case RelayFormatOpenAIImage, RelayFormatOpenAIImageEdit,
		RelayFormatOpenAIAudioTranscription, RelayFormatOpenAIAudioTranslation:
		return true
	default:
		return false
	}
}
