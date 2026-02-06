package types

import (
	"testing"
)

// BenchmarkRelayFormatString benchmarks string conversion
func BenchmarkRelayFormatString(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion,
		RelayFormatClaude,
		RelayFormatCodex,
		RelayFormatOpenAIImage,
		RelayFormatGemini,
		RelayFormatUnknown,
	}

	for _, format := range formats {
		b.Run(format.String(), func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_ = format.String()
			}
		})
	}
}

// BenchmarkIsValid benchmarks format validation
func BenchmarkIsValid(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion,
		RelayFormatClaude,
		RelayFormatCodex,
		RelayFormatOpenAIImage,
		RelayFormatGemini,
		RelayFormatUnknown,
	}

	for _, format := range formats {
		b.Run(format.String(), func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_ = format.IsValid()
			}
		})
	}
}

// BenchmarkSupportsStreaming benchmarks streaming support check
func BenchmarkSupportsStreaming(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion,
		RelayFormatClaude,
		RelayFormatCodex,
		RelayFormatOpenAIImage,
		RelayFormatGemini,
	}

	for _, format := range formats {
		b.Run(format.String(), func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_ = format.SupportsStreaming()
			}
		})
	}
}

// BenchmarkRequiresMultipart benchmarks multipart requirement check
func BenchmarkRequiresMultipart(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAIImage,
		RelayFormatOpenAIImageEdit,
		RelayFormatOpenAIAudioTranscription,
		RelayFormatOpenAIAudioTranslation,
	}

	for _, format := range formats {
		b.Run(format.String(), func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_ = format.RequiresMultipart()
			}
		})
	}
}

// BenchmarkFormatOperations benchmarks combined format operations
func BenchmarkFormatOperations(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion,
		RelayFormatClaude,
		RelayFormatCodex,
		RelayFormatOpenAIImage,
		RelayFormatGemini,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		format := formats[i%len(formats)]
		_ = format.String()
		_ = format.IsValid()
		_ = format.SupportsStreaming()
		_ = format.RequiresMultipart()
	}
}

// BenchmarkRealisticFormatChecks simulates realistic format checking workload
func BenchmarkRealisticFormatChecks(b *testing.B) {
	// Simulate realistic distribution of format checks
	formats := []RelayFormat{
		RelayFormatOpenAIChat, // 60% - most common
		RelayFormatOpenAIChat,
		RelayFormatOpenAIChat,
		RelayFormatOpenAIChat,
		RelayFormatOpenAIChat,
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion, // 15%
		RelayFormatOpenAICompletion,
		RelayFormatClaude,                   // 10% - Claude
		RelayFormatOpenAIEmbedding,          // 8%
		RelayFormatOpenAIImage,              // 5%
		RelayFormatOpenAIAudioTranscription, // 2%
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		format := formats[i%len(formats)]
		_ = format.IsValid()
		_ = format.SupportsStreaming()
	}
}

// BenchmarkFormatComparison benchmarks format equality checks
func BenchmarkFormatComparison(b *testing.B) {
	format1 := RelayFormatOpenAIChat
	format2 := RelayFormatOpenAIChat
	format3 := RelayFormatClaude

	b.Run("Equal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = format1 == format2
		}
	})

	b.Run("NotEqual", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = format1 == format3
		}
	})
}

// BenchmarkConcurrentFormatChecks benchmarks concurrent format checking
func BenchmarkConcurrentFormatChecks(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion,
		RelayFormatClaude,
		RelayFormatOpenAIEmbedding,
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			format := formats[i%len(formats)]
			_ = format.IsValid()
			_ = format.SupportsStreaming()
			i++
		}
	})
}

// BenchmarkFormatSwitchStatement benchmarks switch-based format handling
func BenchmarkFormatSwitchStatement(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatOpenAICompletion,
		RelayFormatClaude,
		RelayFormatCodex,
		RelayFormatOpenAIImage,
		RelayFormatGemini,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		format := formats[i%len(formats)]

		// Simulate typical switch-based format handling
		var result string
		switch format {
		case RelayFormatOpenAIChat:
			result = "chat"
		case RelayFormatOpenAICompletion:
			result = "completion"
		case RelayFormatClaude:
			result = "claude"
		case RelayFormatCodex:
			result = "codex"
		case RelayFormatOpenAIImage:
			result = "image"
		case RelayFormatGemini:
			result = "gemini"
		default:
			result = "unknown"
		}
		_ = result
	}
}

// BenchmarkSupportsStreamingConcurrent benchmarks concurrent streaming support checks
// This simulates realistic concurrent request processing
func BenchmarkSupportsStreamingConcurrent(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatClaude,
		RelayFormatGemini,
		RelayFormatCodex,
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			format := formats[i%len(formats)]
			_ = format.SupportsStreaming()
			i++
		}
	})
}

// BenchmarkRealisticFormatChecksConcurrent benchmarks concurrent format checks
// This represents the actual production workload with multiple concurrent requests
func BenchmarkRealisticFormatChecksConcurrent(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatClaude,
		RelayFormatGemini,
		RelayFormatCodex,
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			format := formats[i%len(formats)]

			// Simulate typical format checks in request processing
			_ = format.SupportsStreaming()
			_ = format.String()

			i++
		}
	})
}

// BenchmarkFormatStringConversion benchmarks format to string conversion
// This is called frequently for logging and debugging
func BenchmarkFormatStringConversion(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatClaude,
		RelayFormatGemini,
		RelayFormatCodex,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		format := formats[i%len(formats)]
		_ = format.String()
	}
}

// BenchmarkFormatStringConversionConcurrent benchmarks concurrent string conversion
func BenchmarkFormatStringConversionConcurrent(b *testing.B) {
	formats := []RelayFormat{
		RelayFormatOpenAIChat,
		RelayFormatClaude,
		RelayFormatGemini,
		RelayFormatCodex,
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			format := formats[i%len(formats)]
			_ = format.String()
			i++
		}
	})
}
