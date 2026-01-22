// Package centralizedmgmt provides centralized API management for GPT-Load.
package centralizedmgmt

import "gpt-load/internal/types"

// ChannelCompatibility defines which channel types are compatible with each relay format.
// The order in the slice represents priority: first is preferred (native), rest are fallbacks.
type ChannelCompatibility struct {
	// Native channel type for this format (highest priority)
	Native string
	// Compatible channel types that can handle this format (lower priority)
	Compatible []string
}

// channelCompatibilityMap maps relay formats to their compatible channel types.
// Priority order: Native channel first, then compatible channels.
var channelCompatibilityMap = map[types.RelayFormat]ChannelCompatibility{
	// OpenAI formats - native to OpenAI, compatible with Azure and other OpenAI-compatible providers
	types.RelayFormatOpenAIChat: {
		Native:     "openai",
		Compatible: []string{"azure", "anthropic", "gemini", "codex"}, // CC support enables cross-channel
	},
	types.RelayFormatOpenAICompletion: {
		Native:     "openai",
		Compatible: []string{"azure"},
	},
	types.RelayFormatOpenAIEmbedding: {
		Native:     "openai",
		Compatible: []string{"azure"}, // Only OpenAI-compatible channels support embeddings
	},
	types.RelayFormatOpenAIImage: {
		Native:     "openai",
		Compatible: []string{"azure"},
	},
	types.RelayFormatOpenAIImageEdit: {
		Native:     "openai",
		Compatible: []string{"azure"},
	},
	types.RelayFormatOpenAIAudioTranscription: {
		Native:     "openai",
		Compatible: []string{"azure"},
	},
	types.RelayFormatOpenAIAudioTranslation: {
		Native:     "openai",
		Compatible: []string{"azure"},
	},
	types.RelayFormatOpenAIAudioSpeech: {
		Native:     "openai",
		Compatible: []string{"azure"},
	},
	types.RelayFormatOpenAIModeration: {
		Native:     "openai",
		Compatible: []string{"azure"},
	},

	// Claude format - native to Anthropic, compatible with OpenAI/Gemini/Codex via CC support
	types.RelayFormatClaude: {
		Native:     "anthropic",
		Compatible: []string{"openai", "azure", "gemini", "codex"}, // CC support enables conversion
	},

	// Codex format - native to Codex
	types.RelayFormatCodex: {
		Native:     "codex",
		Compatible: []string{}, // Codex format is specific to Codex channel
	},

	// Gemini format - native to Gemini
	types.RelayFormatGemini: {
		Native:     "gemini",
		Compatible: []string{}, // Gemini format is specific to Gemini channel
	},
}

// GetCompatibleChannels returns all compatible channel types for a given relay format.
// Returns native channel first, followed by compatible channels in priority order.
func GetCompatibleChannels(format types.RelayFormat) []string {
	compat, exists := channelCompatibilityMap[format]
	if !exists {
		return []string{} // Unknown format, no compatible channels
	}

	// Build result: native first, then compatible channels
	result := make([]string, 0, 1+len(compat.Compatible))
	result = append(result, compat.Native)
	result = append(result, compat.Compatible...)
	return result
}

// GetNativeChannel returns the native (preferred) channel type for a given relay format.
func GetNativeChannel(format types.RelayFormat) string {
	compat, exists := channelCompatibilityMap[format]
	if !exists {
		return "" // Unknown format
	}
	return compat.Native
}

// IsChannelCompatible checks if a channel type is compatible with a relay format.
func IsChannelCompatible(channelType string, format types.RelayFormat) bool {
	compat, exists := channelCompatibilityMap[format]
	if !exists {
		return false
	}

	// Check native channel
	if channelType == compat.Native {
		return true
	}

	// Check compatible channels
	for _, c := range compat.Compatible {
		if channelType == c {
			return true
		}
	}

	return false
}

// GetChannelPriority returns the priority of a channel type for a given relay format.
// Lower number = higher priority. Returns -1 if not compatible.
// Priority: 0 = native, 1+ = compatible channels in order.
func GetChannelPriority(channelType string, format types.RelayFormat) int {
	compat, exists := channelCompatibilityMap[format]
	if !exists {
		return -1
	}

	// Native channel has highest priority (0)
	if channelType == compat.Native {
		return 0
	}

	// Compatible channels have lower priority (1, 2, 3, ...)
	for i, c := range compat.Compatible {
		if channelType == c {
			return i + 1
		}
	}

	return -1 // Not compatible
}
