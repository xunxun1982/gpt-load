package centralizedmgmt

import (
	"gpt-load/internal/types"
	"testing"
)

func TestGetCompatibleChannels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		format         types.RelayFormat
		wantNative     string
		wantCompatible []string
	}{
		{
			name:           "OpenAI Chat",
			format:         types.RelayFormatOpenAIChat,
			wantNative:     "openai",
			wantCompatible: []string{"azure", "anthropic", "gemini", "codex"},
		},
		{
			name:           "OpenAI Embedding",
			format:         types.RelayFormatOpenAIEmbedding,
			wantNative:     "openai",
			wantCompatible: []string{"azure"},
		},
		{
			name:           "Claude",
			format:         types.RelayFormatClaude,
			wantNative:     "anthropic",
			wantCompatible: []string{"openai", "azure", "gemini", "codex"},
		},
		{
			name:           "Gemini",
			format:         types.RelayFormatGemini,
			wantNative:     "gemini",
			wantCompatible: []string{},
		},
		{
			name:           "Codex",
			format:         types.RelayFormatCodex,
			wantNative:     "codex",
			wantCompatible: []string{},
		},
		{
			name:           "Unknown format",
			format:         types.RelayFormat("unknown"),
			wantNative:     "",
			wantCompatible: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCompatibleChannels(tt.format)

			if tt.wantNative == "" {
				// Unknown format should return empty slice
				if len(got) != 0 {
					t.Errorf("GetCompatibleChannels() = %v, want empty slice", got)
				}
				return
			}

			// Check native channel (first element)
			if len(got) == 0 {
				t.Fatalf("GetCompatibleChannels() returned empty slice, want native=%s", tt.wantNative)
			}
			if got[0] != tt.wantNative {
				t.Errorf("GetCompatibleChannels() native = %v, want %v", got[0], tt.wantNative)
			}

			// Check compatible channels (rest of elements)
			gotCompatible := got[1:]
			if len(gotCompatible) != len(tt.wantCompatible) {
				t.Errorf("GetCompatibleChannels() compatible count = %d, want %d", len(gotCompatible), len(tt.wantCompatible))
			}

			for i, want := range tt.wantCompatible {
				if i >= len(gotCompatible) {
					break
				}
				if gotCompatible[i] != want {
					t.Errorf("GetCompatibleChannels() compatible[%d] = %v, want %v", i, gotCompatible[i], want)
				}
			}
		})
	}
}

func TestGetNativeChannel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		format types.RelayFormat
		want   string
	}{
		{"OpenAI Chat", types.RelayFormatOpenAIChat, "openai"},
		{"OpenAI Embedding", types.RelayFormatOpenAIEmbedding, "openai"},
		{"Claude", types.RelayFormatClaude, "anthropic"},
		{"Gemini", types.RelayFormatGemini, "gemini"},
		{"Codex", types.RelayFormatCodex, "codex"},
		{"Unknown", types.RelayFormat("unknown"), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetNativeChannel(tt.format)
			if got != tt.want {
				t.Errorf("GetNativeChannel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsChannelCompatible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		channelType string
		format      types.RelayFormat
		want        bool
	}{
		// OpenAI Chat - compatible with many channels
		{"OpenAI native for chat", "openai", types.RelayFormatOpenAIChat, true},
		{"Azure compatible for chat", "azure", types.RelayFormatOpenAIChat, true},
		{"Anthropic compatible for chat", "anthropic", types.RelayFormatOpenAIChat, true},
		{"Gemini compatible for chat", "gemini", types.RelayFormatOpenAIChat, true},
		{"Codex compatible for chat", "codex", types.RelayFormatOpenAIChat, true},

		// OpenAI Embedding - only OpenAI-compatible channels
		{"OpenAI native for embedding", "openai", types.RelayFormatOpenAIEmbedding, true},
		{"Azure compatible for embedding", "azure", types.RelayFormatOpenAIEmbedding, true},
		{"Anthropic NOT compatible for embedding", "anthropic", types.RelayFormatOpenAIEmbedding, false},
		{"Gemini NOT compatible for embedding", "gemini", types.RelayFormatOpenAIEmbedding, false},
		{"Codex NOT compatible for embedding", "codex", types.RelayFormatOpenAIEmbedding, false},

		// Claude - native to Anthropic, compatible with others via CC
		{"Anthropic native for Claude", "anthropic", types.RelayFormatClaude, true},
		{"OpenAI compatible for Claude", "openai", types.RelayFormatClaude, true},
		{"Azure compatible for Claude", "azure", types.RelayFormatClaude, true},
		{"Gemini compatible for Claude", "gemini", types.RelayFormatClaude, true},
		{"Codex compatible for Claude", "codex", types.RelayFormatClaude, true},

		// Gemini - only native
		{"Gemini native", "gemini", types.RelayFormatGemini, true},
		{"OpenAI NOT compatible for Gemini", "openai", types.RelayFormatGemini, false},
		{"Anthropic NOT compatible for Gemini", "anthropic", types.RelayFormatGemini, false},

		// Codex - only native
		{"Codex native", "codex", types.RelayFormatCodex, true},
		{"OpenAI NOT compatible for Codex", "openai", types.RelayFormatCodex, false},

		// Unknown format
		{"Unknown format", "openai", types.RelayFormat("unknown"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsChannelCompatible(tt.channelType, tt.format)
			if got != tt.want {
				t.Errorf("IsChannelCompatible(%s, %s) = %v, want %v", tt.channelType, tt.format, got, tt.want)
			}
		})
	}
}

func TestGetChannelPriority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		channelType string
		format      types.RelayFormat
		want        int
	}{
		// Native channels have priority 0
		{"OpenAI native for chat", "openai", types.RelayFormatOpenAIChat, 0},
		{"Anthropic native for Claude", "anthropic", types.RelayFormatClaude, 0},
		{"Gemini native", "gemini", types.RelayFormatGemini, 0},

		// Compatible channels have priority 1+
		{"Azure compatible for chat", "azure", types.RelayFormatOpenAIChat, 1},
		{"Anthropic compatible for chat", "anthropic", types.RelayFormatOpenAIChat, 2},
		{"Gemini compatible for chat", "gemini", types.RelayFormatOpenAIChat, 3},
		{"Codex compatible for chat", "codex", types.RelayFormatOpenAIChat, 4},

		// Incompatible channels have priority -1
		{"Anthropic NOT compatible for embedding", "anthropic", types.RelayFormatOpenAIEmbedding, -1},
		{"OpenAI NOT compatible for Gemini", "openai", types.RelayFormatGemini, -1},

		// Unknown format
		{"Unknown format", "openai", types.RelayFormat("unknown"), -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetChannelPriority(tt.channelType, tt.format)
			if got != tt.want {
				t.Errorf("GetChannelPriority(%s, %s) = %v, want %v", tt.channelType, tt.format, got, tt.want)
			}
		})
	}
}

// Benchmark tests for performance
func BenchmarkGetCompatibleChannels(b *testing.B) {
	format := types.RelayFormatOpenAIChat
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetCompatibleChannels(format)
	}
}

func BenchmarkIsChannelCompatible(b *testing.B) {
	channelType := "openai"
	format := types.RelayFormatOpenAIChat
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsChannelCompatible(channelType, format)
	}
}

func BenchmarkGetChannelPriority(b *testing.B) {
	channelType := "openai"
	format := types.RelayFormatOpenAIChat
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetChannelPriority(channelType, format)
	}
}
